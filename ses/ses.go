package ses

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/gsoultan/gsmail"
)

// Sender represents the AWS SES configuration and implements the Sender interface.
//
// A Sender is safe for concurrent use, but its fields are not: they are read
// on every Send, so changing one while a send is in flight is a data race.
// Configure it fully before first use. SetRetryConfig is the exception and may
// be called at any time.
type Sender struct {
	gsmail.BaseProvider
	Region    string
	AccessKey string
	SecretKey string
	Endpoint  string // Optional for testing/mocking

	mu     sync.RWMutex
	client *sesv2.Client

	// Deliverability
	DKIMConfig *gsmail.DKIMOptions
}

// NewSender creates a new AWS SES provider.
func NewSender(region, accessKey, secretKey, endpoint string) *Sender {
	return &Sender{
		Region:    region,
		AccessKey: accessKey,
		SecretKey: secretKey,
		Endpoint:  endpoint,
	}
}

func (p *Sender) getClient(ctx context.Context) (*sesv2.Client, error) {
	p.mu.RLock()
	if p.client != nil {
		client := p.client
		p.mu.RUnlock()
		return client, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.client != nil {
		return p.client, nil
	}

	// AccessKey and SecretKey are only applied when both are set. Leaving them
	// empty keeps the default AWS credential chain (IAM role, environment,
	// profile) intact; overriding it with empty static credentials would break
	// every ambient-credential deployment.
	opts := []func(*config.LoadOptions) error{config.WithRegion(p.Region)}
	if p.AccessKey != "" && p.SecretKey != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(p.AccessKey, p.SecretKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	p.client = sesv2.NewFromConfig(awsCfg, func(o *sesv2.Options) {
		if p.Endpoint != "" {
			o.BaseEndpoint = aws.String(p.Endpoint)
		}
	})

	return p.client, nil
}

// Ping checks the connection to AWS SES by getting the client.
func (p *Sender) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		client, err := p.getClient(ctx)
		if err != nil {
			return fmt.Errorf("ses ping: %w", err)
		}
		_, err = client.GetAccount(ctx, &sesv2.GetAccountInput{})
		if err != nil {
			return fmt.Errorf("ses get account: %w", err)
		}
		return nil
	})
}

// Send sends an email using AWS SES.
//
// A message is sent through the SES "simple" content API when it can be
// expressed that way. Anything that cannot be — attachments, both a text and
// an HTML body, custom headers, or DKIM signing configured on this sender — is
// rendered locally and sent as a raw MIME message instead, so no part of the
// Email is silently dropped.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	if err := gsmail.RejectEnvelope("ses", email); err != nil {
		return err
	}
	needsRaw := len(email.Attachments) > 0 ||
		(len(email.Body) > 0 && len(email.HTMLBody) > 0) ||
		len(email.Headers) > 0 ||
		p.DKIMConfig != nil

	if needsRaw {
		return p.sendRaw(ctx, email)
	}
	return p.sendSimple(ctx, email)
}

// destination builds the SES envelope shared by both send paths.
func destination(email gsmail.Email) *types.Destination {
	return &types.Destination{
		ToAddresses:  gsmail.FormatAddressList(email.To),
		CcAddresses:  gsmail.FormatAddressList(email.Cc),
		BccAddresses: gsmail.FormatAddressList(email.Bcc),
	}
}

func (p *Sender) sendSimple(ctx context.Context, email gsmail.Email) error {
	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(gsmail.FormatAddress(email.From)),
		Destination:      destination(email),
		Content:          &types.EmailContent{},
	}

	if email.ReplyTo != "" {
		input.ReplyToAddresses = []string{gsmail.FormatAddress(email.ReplyTo)}
	}

	input.Content.Simple = &types.Message{
		Subject: &types.Content{Data: aws.String(email.Subject)},
		Body:    &types.Body{},
	}

	// Body is text/plain and HTMLBody is text/html, as the field names say.
	// This used to sniff for markup, which misread ordinary prose containing
	// an angle bracket and sent it as HTML.
	if len(email.Body) > 0 {
		input.Content.Simple.Body.Text = &types.Content{Data: aws.String(string(email.Body))}
	}
	if len(email.HTMLBody) > 0 {
		input.Content.Simple.Body.Html = &types.Content{Data: aws.String(string(email.HTMLBody))}
	}

	return p.send(ctx, input)
}

func (p *Sender) sendRaw(ctx context.Context, email gsmail.Email) error {
	// Render once, outside the retry loop, so every attempt carries the same
	// Date and Message-ID and a DKIM signature is computed only once.
	var raw []byte
	err := gsmail.WithMessage(email, func(msg []byte) error {
		if p.DKIMConfig != nil {
			signed, signErr := gsmail.SignDKIM(msg, *p.DKIMConfig)
			if signErr != nil {
				return gsmail.NonRetryable(fmt.Errorf("dkim sign: %w", signErr))
			}
			raw = signed
			return nil
		}
		// The slice handed to us is only valid until this callback returns.
		raw = make([]byte, len(msg))
		copy(raw, msg)
		return nil
	})
	if err != nil {
		return err
	}

	return p.send(ctx, &sesv2.SendEmailInput{
		Destination: destination(email),
		Content:     &types.EmailContent{Raw: &types.RawMessage{Data: raw}},
	})
}

func (p *Sender) send(ctx context.Context, input *sesv2.SendEmailInput) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		client, err := p.getClient(ctx)
		if err != nil {
			return fmt.Errorf("get ses client: %w", err)
		}
		if _, err := client.SendEmail(ctx, input); err != nil {
			return fmt.Errorf("send email via ses: %w", err)
		}
		return nil
	})
}

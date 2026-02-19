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

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(p.Region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(p.AccessKey, p.SecretKey, "")),
	)
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
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		client, err := p.getClient(ctx)
		if err != nil {
			return fmt.Errorf("get ses client: %w", err)
		}

		input := &sesv2.SendEmailInput{
			FromEmailAddress: aws.String(email.From),
			Destination: &types.Destination{
				ToAddresses:  email.To,
				CcAddresses:  email.Cc,
				BccAddresses: email.Bcc,
			},
			Content: &types.EmailContent{},
		}

		if email.ReplyTo != "" {
			input.ReplyToAddresses = []string{email.ReplyTo}
		}

		hasAttachments := len(email.Attachments) > 0
		hasBothBodies := len(email.Body) > 0 && len(email.HTMLBody) > 0

		if !hasAttachments && !hasBothBodies {
			input.Content.Simple = &types.Message{
				Subject: &types.Content{
					Data: aws.String(email.Subject),
				},
				Body: &types.Body{},
			}
			body := email.Body
			if len(body) == 0 && len(email.HTMLBody) > 0 {
				body = email.HTMLBody
			}
			if gsmail.IsHTML(body) {
				input.Content.Simple.Body.Html = &types.Content{
					Data: aws.String(gsmail.UnsafeBytesToString(body)),
				}
			} else {
				input.Content.Simple.Body.Text = &types.Content{
					Data: aws.String(gsmail.UnsafeBytesToString(body)),
				}
			}
		} else {
			bufPtr := gsmail.GetBuffer()
			defer gsmail.PutBuffer(bufPtr)

			gsmail.BuildMessage(bufPtr, email)

			// DKIM Signing for raw messages
			if p.DKIMConfig != nil {
				signed, err := gsmail.SignDKIM(*bufPtr, *p.DKIMConfig)
				if err != nil {
					return fmt.Errorf("dkim sign: %w", err)
				}
				*bufPtr = signed
			}

			input.Content.Raw = &types.RawMessage{
				Data: *bufPtr,
			}
		}

		_, err = client.SendEmail(ctx, input)
		if err != nil {
			return fmt.Errorf("send email via ses: %w", err)
		}

		return nil
	})
}

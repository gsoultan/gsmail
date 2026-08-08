package sendgrid

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/gsoultan/gsmail"
)

// Sender represents the SendGrid provider and implements the Sender interface.
//
// A Sender is safe for concurrent use, but its fields are not: they are read
// on every Send, so changing one while a send is in flight is a data race.
// Configure it fully before first use. SetRetryConfig is the exception and may
// be called at any time.
type Sender struct {
	gsmail.BaseProvider
	APIKey  string
	Client  *http.Client
	BaseURL string // Default: https://api.sendgrid.com
}

// NewSender creates a new SendGrid provider.
func NewSender(apiKey string) *Sender {
	return &Sender{
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 30 * time.Second},
		BaseURL: "https://api.sendgrid.com",
	}
}

type sendgridRequest struct {
	Personalizations []personalization `json:"personalizations"`
	From             address           `json:"from"`
	ReplyTo          *address          `json:"reply_to,omitempty"`
	Subject          string            `json:"subject"`
	Content          []content         `json:"content"`
	Attachments      []attachment      `json:"attachments,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
}

type personalization struct {
	To  []address `json:"to"`
	Cc  []address `json:"cc,omitempty"`
	Bcc []address `json:"bcc,omitempty"`
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type content struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type attachment struct {
	Content     string `json:"content"`
	Type        string `json:"type,omitempty"`
	Filename    string `json:"filename"`
	Disposition string `json:"disposition,omitempty"`
	ContentID   string `json:"content_id,omitempty"`
}

// Send sends an email using the SendGrid API.
//
// A 4xx other than 408 or 429 is reported as a permanent gsmail.HTTPError and
// is not retried; a 429 honours the server's Retry-After header.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	reqBody, err := p.buildRequest(email)
	if err != nil {
		return err
	}

	// Marshal once: the payload does not change between attempts.
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return gsmail.NonRetryable(fmt.Errorf("marshal request: %w", err))
	}

	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/v3/mail/send", bytes.NewReader(jsonBody))
		if err != nil {
			return gsmail.NonRetryable(fmt.Errorf("create request: %w", err))
		}

		req.Header.Set("Authorization", "Bearer "+p.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.Client.Do(req)
		if err != nil {
			return fmt.Errorf("http execute: %w", err)
		}
		defer gsmail.DrainAndClose(resp.Body)

		if resp.StatusCode >= 400 {
			return gsmail.NewHTTPError("sendgrid", resp)
		}

		return nil
	})
}

func (p *Sender) buildRequest(email gsmail.Email) (sendgridRequest, error) {
	req := sendgridRequest{
		From:    parseAddress(email.From),
		Subject: email.Subject,
	}

	if email.ReplyTo != "" {
		addr := parseAddress(email.ReplyTo)
		req.ReplyTo = &addr
	}

	pers := personalization{}
	for _, to := range email.To {
		pers.To = append(pers.To, parseAddress(to))
	}
	for _, cc := range email.Cc {
		pers.Cc = append(pers.Cc, parseAddress(cc))
	}
	for _, bcc := range email.Bcc {
		pers.Bcc = append(pers.Bcc, parseAddress(bcc))
	}
	req.Personalizations = []personalization{pers}

	// Body is text/plain and HTMLBody is text/html, as the field names say.
	// This used to sniff Body for markup, which misread ordinary prose --
	// "Please review <p1> pricing" was sent as HTML and the recipient's
	// renderer ate the word. SetBody routes HTML to HTMLBody, so there is
	// nothing left for sniffing to discover.
	//
	// SendGrid requires text/plain before text/html.
	if len(email.Body) > 0 {
		req.Content = append(req.Content, content{
			Type:  "text/plain",
			Value: string(email.Body),
		})
	}
	if len(email.HTMLBody) > 0 {
		req.Content = append(req.Content, content{
			Type:  "text/html",
			Value: string(email.HTMLBody),
		})
	}

	// SendGrid rejects a request with no content at all.
	if len(req.Content) == 0 {
		return sendgridRequest{}, gsmail.NonRetryable(
			fmt.Errorf("sendgrid: email has neither Body nor HTMLBody"))
	}

	for _, att := range email.Attachments {
		// An attachment carrying a Content-ID is referenced from the HTML by
		// cid:, so it has to be declared inline. Sending it as "attachment"
		// leaves the image broken in the body and duplicated at the bottom.
		disposition := "attachment"
		if att.ContentID != "" {
			disposition = "inline"
		}
		req.Attachments = append(req.Attachments, attachment{
			Content:     base64.StdEncoding.EncodeToString(att.Data),
			Type:        att.ContentType,
			Filename:    att.Filename,
			Disposition: disposition,
			ContentID:   att.ContentID,
		})
	}

	// Custom headers (List-Unsubscribe, In-Reply-To, X-*). The reserved names
	// SendGrid generates itself are filtered out, mirroring BuildMessage.
	hdrs, err := gsmail.CustomHeaders(email.Headers)
	if err != nil {
		return sendgridRequest{}, err
	}
	req.Headers = hdrs

	return req, nil
}

func parseAddress(s string) address {
	if a, err := gsmail.ParseEmailAddress(s); err == nil && a != nil {
		return address{Email: a.Address, Name: a.Name}
	}
	return address{Email: s}
}

// Ping checks the connection to SendGrid by querying API scopes.
func (p *Sender) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", p.BaseURL+"/v3/scopes", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
		resp, err := p.Client.Do(req)
		if err != nil {
			return err
		}
		defer gsmail.DrainAndClose(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return gsmail.NewHTTPError("sendgrid ping", resp)
		}
		return nil
	})
}

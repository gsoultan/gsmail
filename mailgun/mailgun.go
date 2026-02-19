package mailgun

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/gsoultan/gsmail"
)

// Sender represents the Mailgun provider and implements the Sender interface.
type Sender struct {
	gsmail.BaseProvider
	Domain  string
	APIKey  string
	Client  *http.Client
	BaseURL string // Default: https://api.mailgun.net/v3
}

// NewSender creates a new Mailgun provider.
func NewSender(domain, apiKey string) *Sender {
	return &Sender{
		Domain:  domain,
		APIKey:  apiKey,
		Client:  &http.Client{Timeout: 30 * time.Second},
		BaseURL: "https://api.mailgun.net/v3",
	}
}

// Send sends an email using the Mailgun API.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)

		_ = writer.WriteField("from", email.From)
		for _, to := range email.To {
			_ = writer.WriteField("to", to)
		}
		for _, cc := range email.Cc {
			_ = writer.WriteField("cc", cc)
		}
		for _, bcc := range email.Bcc {
			_ = writer.WriteField("bcc", bcc)
		}
		_ = writer.WriteField("subject", email.Subject)

		if email.ReplyTo != "" {
			_ = writer.WriteField("h:Reply-To", email.ReplyTo)
		}

		if len(email.Body) > 0 && !gsmail.IsHTML(email.Body) {
			_ = writer.WriteField("text", string(email.Body))
		}

		htmlBody := email.HTMLBody
		if len(htmlBody) == 0 && gsmail.IsHTML(email.Body) {
			htmlBody = email.Body
		}
		if len(htmlBody) > 0 {
			_ = writer.WriteField("html", string(htmlBody))
		}

		for _, att := range email.Attachments {
			// Mailgun supports "attachment" for regular and "inline" for inline
			fieldName := "attachment"
			if att.ContentID != "" {
				fieldName = "inline"
			}
			part, err := writer.CreateFormFile(fieldName, att.Filename)
			if err != nil {
				return err
			}
			_, _ = part.Write(att.Data)
		}

		writer.Close()

		req, err := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("%s/%s/messages", p.BaseURL, p.Domain), body)
		if err != nil {
			return err
		}

		req.SetBasicAuth("api", p.APIKey)
		req.Header.Set("Content-Type", writer.FormDataContentType())

		resp, err := p.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("mailgun error (status %d): %s", resp.StatusCode, string(b))
		}

		return nil
	})
}

// Ping checks the connection to Mailgun by querying domain information.
func (p *Sender) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/domains/%s", p.BaseURL, p.Domain), nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth("api", p.APIKey)
		resp, err := p.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("mailgun ping failed: status %d", resp.StatusCode)
		}
		return nil
	})
}

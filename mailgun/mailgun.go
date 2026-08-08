package mailgun

import (
	"bytes"
	"context"
	"fmt"
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
//
// A 4xx other than 408 or 429 is reported as a permanent gsmail.HTTPError and
// is not retried; a 429 honours the server's Retry-After header.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	// Build the multipart payload once. It is identical on every attempt, and
	// re-encoding attachments per retry is pure waste.
	body, contentType, err := buildForm(email)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/%s/messages", p.BaseURL, p.Domain)

	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return gsmail.NonRetryable(err)
		}

		req.SetBasicAuth("api", p.APIKey)
		req.Header.Set("Content-Type", contentType)

		resp, err := p.Client.Do(req)
		if err != nil {
			return err
		}
		defer gsmail.DrainAndClose(resp.Body)

		if resp.StatusCode >= 400 {
			return gsmail.NewHTTPError("mailgun", resp)
		}

		return nil
	})
}

// buildForm renders the email as a multipart/form-data payload for the
// Mailgun messages endpoint.
func buildForm(email gsmail.Email) ([]byte, string, error) {
	buf := &bytes.Buffer{}
	writer := multipart.NewWriter(buf)

	_ = writer.WriteField("from", gsmail.FormatAddress(email.From))
	for _, to := range email.To {
		_ = writer.WriteField("to", gsmail.FormatAddress(to))
	}
	for _, cc := range email.Cc {
		_ = writer.WriteField("cc", gsmail.FormatAddress(cc))
	}
	for _, bcc := range email.Bcc {
		_ = writer.WriteField("bcc", gsmail.FormatAddress(bcc))
	}
	_ = writer.WriteField("subject", email.Subject)

	if email.ReplyTo != "" {
		_ = writer.WriteField("h:Reply-To", gsmail.FormatAddress(email.ReplyTo))
	}

	// Custom headers (List-Unsubscribe, In-Reply-To, X-*) ride along as
	// "h:Name" fields, which is Mailgun's pass-through convention.
	hdrs, err := gsmail.CustomHeaders(email.Headers)
	if err != nil {
		return nil, "", err
	}
	for name, value := range hdrs {
		_ = writer.WriteField("h:"+name, value)
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
			return nil, "", gsmail.NonRetryable(fmt.Errorf("mailgun: attach %q: %w", att.Filename, err))
		}
		if _, err := part.Write(att.Data); err != nil {
			return nil, "", gsmail.NonRetryable(fmt.Errorf("mailgun: attach %q: %w", att.Filename, err))
		}
	}

	if err := writer.Close(); err != nil {
		return nil, "", gsmail.NonRetryable(fmt.Errorf("mailgun: finalise form: %w", err))
	}

	return buf.Bytes(), writer.FormDataContentType(), nil
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
		defer gsmail.DrainAndClose(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return gsmail.NewHTTPError("mailgun ping", resp)
		}
		return nil
	})
}

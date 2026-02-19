package sendgrid

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"time"

	"github.com/gsoultan/gsmail"
)

// Sender represents the SendGrid provider and implements the Sender interface.
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
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		reqBody, err := p.buildRequest(email)
		if err != nil {
			return err
		}

		jsonBody, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+"/v3/mail/send", bytes.NewReader(jsonBody))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+p.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := p.Client.Do(req)
		if err != nil {
			return fmt.Errorf("http execute: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			var errResp struct {
				Errors []struct {
					Message string `json:"message"`
				} `json:"errors"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&errResp)
			errMsg := "unknown error"
			if len(errResp.Errors) > 0 {
				errMsg = errResp.Errors[0].Message
			}
			return fmt.Errorf("sendgrid error (status %d): %s", resp.StatusCode, errMsg)
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

	// Plain text body
	if len(email.Body) > 0 && !gsmail.IsHTML(email.Body) {
		req.Content = append(req.Content, content{
			Type:  "text/plain",
			Value: string(email.Body),
		})
	}

	// HTML body
	htmlBody := email.HTMLBody
	if len(htmlBody) == 0 && gsmail.IsHTML(email.Body) {
		htmlBody = email.Body
	}

	if len(htmlBody) > 0 {
		req.Content = append(req.Content, content{
			Type:  "text/html",
			Value: string(htmlBody),
		})
	}

	for _, att := range email.Attachments {
		req.Attachments = append(req.Attachments, attachment{
			Content:     base64.StdEncoding.EncodeToString(att.Data),
			Type:        att.ContentType,
			Filename:    att.Filename,
			Disposition: "attachment",
			ContentID:   att.ContentID,
		})
	}

	return req, nil
}

func parseAddress(s string) address {
	if a, err := mail.ParseAddress(s); err == nil {
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
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("sendgrid ping failed: status %d", resp.StatusCode)
		}
		return nil
	})
}

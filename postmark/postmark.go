package postmark

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gsoultan/gsmail"
)

// Sender represents the Postmark provider and implements the Sender interface.
type Sender struct {
	gsmail.BaseProvider
	ServerToken string
	Client      *http.Client
	BaseURL     string // Default: https://api.postmarkapp.com
}

// NewSender creates a new Postmark provider.
func NewSender(serverToken string) *Sender {
	return &Sender{
		ServerToken: serverToken,
		Client:      &http.Client{Timeout: 30 * time.Second},
		BaseURL:     "https://api.postmarkapp.com",
	}
}

type postmarkRequest struct {
	From        string       `json:"From"`
	To          string       `json:"To"`
	Cc          string       `json:"Cc,omitempty"`
	Bcc         string       `json:"Bcc,omitempty"`
	Subject     string       `json:"Subject"`
	TextBody    string       `json:"TextBody,omitempty"`
	HtmlBody    string       `json:"HtmlBody,omitempty"`
	ReplyTo     string       `json:"ReplyTo,omitempty"`
	Attachments []attachment `json:"Attachments,omitempty"`
}

type attachment struct {
	Name        string `json:"Name"`
	Content     string `json:"Content"`
	ContentType string `json:"ContentType"`
	ContentID   string `json:"ContentID,omitempty"`
}

// Send sends an email using the Postmark API.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		reqBody := postmarkRequest{
			From:    email.From,
			To:      strings.Join(email.To, ","),
			Cc:      strings.Join(email.Cc, ","),
			Bcc:     strings.Join(email.Bcc, ","),
			Subject: email.Subject,
			ReplyTo: email.ReplyTo,
		}

		if len(email.Body) > 0 {
			if gsmail.IsHTML(email.Body) {
				reqBody.HtmlBody = string(email.Body)
			} else {
				reqBody.TextBody = string(email.Body)
			}
		}
		if len(email.HTMLBody) > 0 {
			reqBody.HtmlBody = string(email.HTMLBody)
		}

		for _, att := range email.Attachments {
			reqBody.Attachments = append(reqBody.Attachments, attachment{
				Name:        att.Filename,
				Content:     base64.StdEncoding.EncodeToString(att.Data),
				ContentType: att.ContentType,
				ContentID:   att.ContentID,
			})
		}

		jsonBody, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", p.BaseURL+"/email", bytes.NewReader(jsonBody))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}

		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Postmark-Server-Token", p.ServerToken)

		resp, err := p.Client.Do(req)
		if err != nil {
			return fmt.Errorf("http execute: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("postmark error: status %d", resp.StatusCode)
		}

		return nil
	})
}

// Ping checks the connection to Postmark by querying server information.
func (p *Sender) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		req, err := http.NewRequestWithContext(ctx, "GET", p.BaseURL+"/server", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Postmark-Server-Token", p.ServerToken)
		resp, err := p.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("postmark ping failed: status %d", resp.StatusCode)
		}
		return nil
	})
}

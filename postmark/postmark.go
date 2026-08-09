package postmark

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/gsoultan/gsmail"
)

// Sender represents the Postmark provider and implements the Sender interface.
//
// A Sender is safe for concurrent use, but its fields are not: they are read
// on every Send, so changing one while a send is in flight is a data race.
// Configure it fully before first use. SetRetryConfig is the exception and may
// be called at any time.
type Sender struct {
	gsmail.BaseProvider
	ServerToken string
	Client      *http.Client
	BaseURL     string // Default: https://api.postmarkapp.com

	// MessageStream selects the Postmark stream to send on, e.g. "outbound"
	// for transactional mail or "broadcast" for bulk. Empty uses the server's
	// default stream. Postmark rejects bulk mail sent on a transactional
	// stream, so set this when sending campaigns.
	MessageStream string
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
	From          string       `json:"From"`
	To            string       `json:"To"`
	Cc            string       `json:"Cc,omitempty"`
	Bcc           string       `json:"Bcc,omitempty"`
	Subject       string       `json:"Subject"`
	TextBody      string       `json:"TextBody,omitempty"`
	HtmlBody      string       `json:"HtmlBody,omitempty"`
	ReplyTo       string       `json:"ReplyTo,omitempty"`
	MessageStream string       `json:"MessageStream,omitempty"`
	Headers       []header     `json:"Headers,omitempty"`
	Attachments   []attachment `json:"Attachments,omitempty"`
}

type header struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type attachment struct {
	Name        string `json:"Name"`
	Content     string `json:"Content"`
	ContentType string `json:"ContentType"`
	ContentID   string `json:"ContentID,omitempty"`
}

// Send sends an email using the Postmark API.
//
// A 4xx other than 408 or 429 is reported as a permanent gsmail.HTTPError and
// is not retried; a 429 honours the server's Retry-After header. The error
// carries Postmark's response body, which names the specific ErrorCode.
func (p *Sender) Send(ctx context.Context, email gsmail.Email) error {
	if err := gsmail.RejectEnvelope("postmark", email); err != nil {
		return err
	}
	reqBody := postmarkRequest{
		From:          gsmail.FormatAddress(email.From),
		To:            gsmail.FormatAddresses(email.To),
		Cc:            gsmail.FormatAddresses(email.Cc),
		Bcc:           gsmail.FormatAddresses(email.Bcc),
		Subject:       email.Subject,
		ReplyTo:       gsmail.FormatAddress(email.ReplyTo),
		MessageStream: p.MessageStream,
	}

	// Body is text/plain and HTMLBody is text/html. Sniffing Body for markup
	// misread ordinary prose containing an angle bracket.
	if len(email.Body) > 0 {
		reqBody.TextBody = string(email.Body)
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

	// Custom headers (List-Unsubscribe, In-Reply-To, X-*).
	hdrs, err := gsmail.CustomHeaders(email.Headers)
	if err != nil {
		return err
	}
	for _, name := range sortedNames(hdrs) {
		reqBody.Headers = append(reqBody.Headers, header{Name: name, Value: hdrs[name]})
	}

	// Marshal once: the payload does not change between attempts.
	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return gsmail.NonRetryable(fmt.Errorf("marshal request: %w", err))
	}

	return gsmail.Retry(ctx, p.GetRetryConfig(), func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+"/email", bytes.NewReader(jsonBody))
		if err != nil {
			return gsmail.NonRetryable(fmt.Errorf("create request: %w", err))
		}

		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Postmark-Server-Token", p.ServerToken)

		resp, err := p.Client.Do(req)
		if err != nil {
			return fmt.Errorf("http execute: %w", err)
		}
		defer gsmail.DrainAndClose(resp.Body)

		if resp.StatusCode != http.StatusOK {
			return gsmail.NewHTTPError("postmark", resp)
		}

		return nil
	})
}

// sortedNames returns map keys in a stable order so the marshalled request is
// byte-identical for the same input.
func sortedNames(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		defer gsmail.DrainAndClose(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return gsmail.NewHTTPError("postmark ping", resp)
		}
		return nil
	})
}

package postmark_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/postmark"
	"github.com/gsoultan/gsmail/providertest"
)

func TestConformance(t *testing.T) {
	providertest.Run(t, providertest.Harness{
		Name: "postmark",

		NewSender: func(t *testing.T, baseURL string) gsmail.Sender {
			s := postmark.NewSender("test-token")
			s.BaseURL = baseURL
			return s
		},

		Decode: func(t *testing.T, r *http.Request, body []byte) providertest.Sent {
			var req struct {
				From     string `json:"From"`
				To       string `json:"To"`
				Cc       string `json:"Cc"`
				Bcc      string `json:"Bcc"`
				Subject  string `json:"Subject"`
				TextBody string `json:"TextBody"`
				HtmlBody string `json:"HtmlBody"`
				Headers  []struct {
					Name  string `json:"Name"`
					Value string `json:"Value"`
				} `json:"Headers"`
				Attachments []struct {
					Name      string `json:"Name"`
					ContentID string `json:"ContentID"`
				} `json:"Attachments"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode postmark request: %v", err)
			}

			split := func(s string) []string {
				if s == "" {
					return nil
				}
				parts := strings.Split(s, ",")
				for i := range parts {
					parts[i] = strings.TrimSpace(parts[i])
				}
				return parts
			}

			out := providertest.Sent{
				From:    req.From,
				To:      split(req.To),
				Cc:      split(req.Cc),
				Bcc:     split(req.Bcc),
				Subject: req.Subject,
				Text:    req.TextBody,
				HTML:    req.HtmlBody,
				Headers: map[string]string{},
			}
			for _, h := range req.Headers {
				out.Headers[h.Name] = h.Value
			}
			for _, a := range req.Attachments {
				// Postmark infers inline from the presence of ContentID
				// rather than carrying an explicit disposition.
				disposition := "attachment"
				if a.ContentID != "" {
					disposition = "inline"
				}
				out.Attachments = append(out.Attachments, providertest.Attachment{
					Filename:    a.Name,
					Disposition: disposition,
					ContentID:   a.ContentID,
				})
			}
			return out
		},
	})
}

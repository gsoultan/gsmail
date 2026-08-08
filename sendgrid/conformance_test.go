package sendgrid_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/providertest"
	"github.com/gsoultan/gsmail/sendgrid"
)

func TestConformance(t *testing.T) {
	providertest.Run(t, providertest.Harness{
		Name:          "sendgrid",
		SuccessStatus: http.StatusAccepted,

		NewSender: func(t *testing.T, baseURL string) gsmail.Sender {
			s := sendgrid.NewSender("test-key")
			s.BaseURL = baseURL
			return s
		},

		Decode: func(t *testing.T, r *http.Request, body []byte) providertest.Sent {
			var req struct {
				Personalizations []struct {
					To  []struct{ Email string } `json:"to"`
					Cc  []struct{ Email string } `json:"cc"`
					Bcc []struct{ Email string } `json:"bcc"`
				} `json:"personalizations"`
				From    struct{ Email string } `json:"from"`
				Subject string                 `json:"subject"`
				Content []struct {
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"content"`
				Headers     map[string]string `json:"headers"`
				Attachments []struct {
					Filename    string `json:"filename"`
					Disposition string `json:"disposition"`
					ContentID   string `json:"content_id"`
				} `json:"attachments"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode sendgrid request: %v", err)
			}

			out := providertest.Sent{
				From:    req.From.Email,
				Subject: req.Subject,
				Headers: req.Headers,
			}
			if len(req.Personalizations) > 0 {
				p := req.Personalizations[0]
				for _, a := range p.To {
					out.To = append(out.To, a.Email)
				}
				for _, a := range p.Cc {
					out.Cc = append(out.Cc, a.Email)
				}
				for _, a := range p.Bcc {
					out.Bcc = append(out.Bcc, a.Email)
				}
			}
			for _, c := range req.Content {
				switch c.Type {
				case "text/plain":
					out.Text = c.Value
				case "text/html":
					out.HTML = c.Value
				}
			}
			for _, a := range req.Attachments {
				out.Attachments = append(out.Attachments, providertest.Attachment{
					Filename:    a.Filename,
					Disposition: a.Disposition,
					ContentID:   a.ContentID,
				})
			}
			return out
		},
	})
}

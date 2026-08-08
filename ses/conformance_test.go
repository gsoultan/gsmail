package ses_test

import (
	"encoding/json"
	"net/http"
	"net/mail"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/providertest"
	"github.com/gsoultan/gsmail/ses"
)

func TestConformance(t *testing.T) {
	providertest.Run(t, providertest.Harness{
		Name:        "ses",
		SuccessBody: `{"MessageId":"m-1"}`,

		// SES speaks through the AWS SDK, which owns its own retry policy and
		// error types. Status-code classification is not this provider's to
		// control, so those cases are skipped rather than asserted falsely.
		SkipRetryChecks: true,

		NewSender: func(t *testing.T, baseURL string) gsmail.Sender {
			return ses.NewSender("us-east-1", "AKIATEST", "secret", baseURL)
		},

		Decode: func(t *testing.T, r *http.Request, body []byte) providertest.Sent {
			var req struct {
				Content struct {
					Simple *struct {
						Subject struct{ Data string }
						Body    struct {
							Text *struct{ Data string }
							Html *struct{ Data string }
						}
					}
					Raw *struct{ Data []byte }
				}
				Destination struct {
					ToAddresses  []string
					CcAddresses  []string
					BccAddresses []string
				}
				FromEmailAddress string
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode ses request: %v", err)
			}

			out := providertest.Sent{
				From: req.FromEmailAddress,
				To:   req.Destination.ToAddresses,
				Cc:   req.Destination.CcAddresses,
				Bcc:  req.Destination.BccAddresses,
			}

			if s := req.Content.Simple; s != nil {
				out.Subject = s.Subject.Data
				if s.Body.Text != nil {
					out.Text = s.Body.Text.Data
				}
				if s.Body.Html != nil {
					out.HTML = s.Body.Html.Data
				}
				return out
			}

			// The raw path carries everything inside a MIME message.
			raw := req.Content.Raw.Data
			parsed, err := gsmail.ParseRawEmail(raw)
			if err != nil {
				t.Fatalf("parse raw ses message: %v", err)
			}
			out.Subject = parsed.Subject
			out.Text = string(parsed.Body)
			out.HTML = string(parsed.HTMLBody)
			if len(out.To) == 0 {
				out.To = parsed.To
			}

			msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
			if err != nil {
				t.Fatalf("read raw ses headers: %v", err)
			}
			out.Headers = map[string]string{}
			for name, values := range msg.Header {
				if len(values) == 0 {
					continue
				}
				switch strings.ToLower(name) {
				case "from", "to", "cc", "bcc", "reply-to", "subject",
					"mime-version", "content-type", "content-transfer-encoding",
					"date", "message-id":
					continue
				}
				out.Headers[name] = values[0]
			}

			for _, att := range parsed.Attachments {
				disposition := "attachment"
				if att.ContentID != "" {
					disposition = "inline"
				}
				out.Attachments = append(out.Attachments, providertest.Attachment{
					Filename:    att.Filename,
					Disposition: disposition,
					ContentID:   att.ContentID,
				})
			}
			return out
		},
	})
}

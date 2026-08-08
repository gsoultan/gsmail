package mailgun_test

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/mailgun"
	"github.com/gsoultan/gsmail/providertest"
)

func TestConformance(t *testing.T) {
	providertest.Run(t, providertest.Harness{
		Name: "mailgun",

		NewSender: func(t *testing.T, baseURL string) gsmail.Sender {
			s := mailgun.NewSender("example.test", "test-key")
			s.BaseURL = baseURL
			return s
		},

		Decode: func(t *testing.T, r *http.Request, body []byte) providertest.Sent {
			// The body was already consumed by the suite, so re-parse the
			// multipart form from the captured bytes.
			mr, err := multipartReader(r, body)
			if err != nil {
				t.Fatalf("decode mailgun form: %v", err)
			}

			out := providertest.Sent{Headers: map[string]string{}}
			for {
				part, err := mr.NextPart()
				if err != nil {
					break
				}
				name := part.FormName()
				filename := part.FileName()

				buf := new(strings.Builder)
				_, _ = copyTo(buf, part)
				value := buf.String()
				_ = part.Close()

				switch {
				case filename != "":
					disposition := "attachment"
					if name == "inline" {
						disposition = "inline"
					}
					out.Attachments = append(out.Attachments, providertest.Attachment{
						Filename:    filename,
						Disposition: disposition,
					})
				case name == "from":
					out.From = value
				case name == "to":
					out.To = append(out.To, value)
				case name == "cc":
					out.Cc = append(out.Cc, value)
				case name == "bcc":
					out.Bcc = append(out.Bcc, value)
				case name == "subject":
					out.Subject = value
				case name == "text":
					out.Text = value
				case name == "html":
					out.HTML = value
				case strings.HasPrefix(name, "h:"):
					out.Headers[strings.TrimPrefix(name, "h:")] = value
				}
			}
			return out
		},
	})
}

// multipartReader rebuilds a reader over the captured body using the boundary
// from the request's Content-Type.
func multipartReader(r *http.Request, body []byte) (*multipart.Reader, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return nil, err
	}
	boundary, ok := params["boundary"]
	if !ok {
		return nil, errNoBoundary
	}
	return multipart.NewReader(bytes.NewReader(body), boundary), nil
}

var errNoBoundary = errors.New("mailgun conformance: request has no multipart boundary")

func copyTo(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }

package ses_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/ses"
)

// sesRequest is the subset of the SESv2 SendEmail payload these tests inspect.
type sesRequest struct {
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
}

func newSender(t *testing.T) (*ses.Sender, *sesRequest) {
	t.Helper()

	captured := &sesRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, captured); err != nil {
			t.Errorf("decode SES request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"MessageId":"m-1"}`)
	}))
	t.Cleanup(srv.Close)

	return ses.NewSender("us-east-1", "AKIATEST", "secret", srv.URL), captured
}

func base() gsmail.Email {
	return gsmail.Email{
		From:    "a@example.com",
		To:      []string{"b@example.com"},
		Subject: "hi",
		Body:    []byte("hello"),
	}
}

// A plain message can use the cheaper simple content API.
func TestSimpleMessageUsesSimpleContent(t *testing.T) {
	s, got := newSender(t)

	if err := s.Send(context.Background(), base()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Content.Simple == nil {
		t.Fatal("expected the simple content path")
	}
	if got.Content.Raw != nil {
		t.Error("raw content should not be set for a plain message")
	}
	if got.Content.Simple.Body.Text == nil || got.Content.Simple.Body.Text.Data != "hello" {
		t.Errorf("unexpected body: %+v", got.Content.Simple.Body)
	}
	if len(got.Destination.ToAddresses) != 1 {
		t.Errorf("recipients lost: %+v", got.Destination)
	}
}

// Custom headers cannot be expressed through the simple API, so the message
// has to be rendered locally and sent raw. Previously they were dropped.
func TestCustomHeadersForceRawAndSurvive(t *testing.T) {
	s, got := newSender(t)

	email := base()
	email.SetHeader("List-Unsubscribe", "<https://x.test/u>")

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Content.Raw == nil {
		t.Fatal("expected the raw content path when custom headers are present")
	}
	if !strings.Contains(string(got.Content.Raw.Data), "List-Unsubscribe: <https://x.test/u>") {
		t.Errorf("List-Unsubscribe missing from the raw message:\n%s", got.Content.Raw.Data)
	}
}

func TestAttachmentsForceRaw(t *testing.T) {
	s, got := newSender(t)

	email := base()
	email.Attachments = []gsmail.Attachment{{Filename: "a.txt", ContentType: "text/plain", Data: []byte("data")}}

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Content.Raw == nil {
		t.Fatal("expected the raw content path for an attachment")
	}
	if !strings.Contains(string(got.Content.Raw.Data), `filename="a.txt"`) {
		t.Errorf("attachment missing from the raw message:\n%s", got.Content.Raw.Data)
	}
}

// DKIMConfig used to apply only on the raw branch, so a plain message went out
// silently unsigned.
func TestDKIMConfigForcesRawAndSigns(t *testing.T) {
	s, got := newSender(t)
	s.DKIMConfig = &gsmail.DKIMOptions{
		Domain:     "example.com",
		Selector:   "s1",
		PrivateKey: testKeyPEM,
	}

	if err := s.Send(context.Background(), base()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Content.Raw == nil {
		t.Fatal("a DKIM-signing sender must use the raw path even for a plain message")
	}
	if !strings.Contains(string(got.Content.Raw.Data), "DKIM-Signature:") {
		t.Errorf("message was not signed:\n%s", got.Content.Raw.Data)
	}
}

// The raw payload must outlive the pooled buffer it was rendered into.
func TestRawDataIsNotAliasedToAPooledBuffer(t *testing.T) {
	s, got := newSender(t)

	email := base()
	email.SetHeader("X-Marker", "sentinel-value")

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Churn the shared buffer pool; a stale alias would be overwritten.
	for i := 0; i < 64; i++ {
		if _, err := gsmail.RenderMessage(gsmail.Email{
			From: "x@example.com", To: []string{"y@example.com"},
			Body: []byte(strings.Repeat("Z", 512)),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(got.Content.Raw.Data), "sentinel-value") {
		t.Error("raw message was corrupted by buffer reuse")
	}
}

// Decoding the base64 the SDK sent proves the wire format is intact.
func TestRawIsValidBase64OnTheWire(t *testing.T) {
	s, got := newSender(t)

	email := base()
	email.SetHeader("X-Test", "1")
	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := gsmail.ParseRawEmail(got.Content.Raw.Data); err != nil {
		t.Errorf("raw message does not parse back: %v", err)
	}
}

// A 2048-bit test key. Generated for this test only; not used anywhere else.
const testKeyPEM = `-----BEGIN RSA PRIVATE KEY-----
MIIEowIBAAKCAQEA4E+oOyG24VQGoAMusLHHzXtD8uCf/s1yUXip3lyI6nsG2A4w
ga1MMAwSjuge8afQOx3L8L6cUKsuW7y29jQiVRFDW43o1IcDWk4Uy8genXofrxER
UA8bM+mMzCKGHFeZnpuVGlt4cv1Uij8kZ1fuyIwTWs7ariFFLULUWoxk2EmCBefx
7UsHNK5khvjj/7jbobLet29qotsDxUh/UXWPUenny38x4INy/44Z2h73XjtY5tio
iK2cDxq+5GKoGYKAnLHUM74MyXIRYvLOiWdyot5wIbReQmyJtbK/m8yzBMuen+Cn
5vGtBO5F3O2PY402kT67m0QR8rRzOLdohLga6QIDAQABAoIBAE4zdzMmTdvAr46Z
jW2MjVvV3ZqPNThf57r/ljkviYw11+z7BW4wPJ+DlfS8eA1HtBDoEnGcAmMdSsww
vpiXFGET46fHkaSGbWTOU/G3kvTT3rfp+18t5Q30HmIMpzS6VZQ2KYVG3nc4WoDY
ApkEzvqb2yONei+66aMd6WqoB4BfeRHhZ+VLBe/f0dHBo/yyB+7zsYVto1VqoLu5
aa5qHaa5b5LixRG74fLIrtJzcWtvAXQvbm4FaJYUR6WkiX4C4xY7VJb45mG+ZPXz
OzJ346Z5Wrc+zUEIBlkOgjjyB/h5p2HIsJ0TM9P4E5JTzvWD3zfvokdsq01f/DUp
DtTbxQECgYEA9ZMevFAc52X5xWyIhLLcCe5UuJaeCrw1/F1+qJKxes1gnhvhgzg/
aoRITUm8X3pTL32dNvuvZkV4tgVdqq6riNzUfZua36N+q8CwQWPHeZvEMRT72xA0
PmYUboB5hjvEbRduuTM6fhKAttKyoKOAtujUxzRHppmM7agiU/5yeLsCgYEA6dVy
gvv7cZzNs1fj8CuhP8MxU2G5QQgw+KJL7fZ6FcNhmSUDPXyDRlhf9Gq2cvkOYaqo
DYux8hLPmFtMK6QRZeY/GW/UU5KJMWrF8dV0syFPTCE6+MzfSvC6sdR3BN6FwstQ
BfFXnxc5fEd1XFwpqrrkSGbRiveBDNt1e87wAqsCgYEA2sSQOfQoe5/lzZFtYMGx
sgsmYDaVXjzi3wovPl9ISnzRmKh/0pT2MZ7chjWs4WWo24LM3mGClNpIuea31ci7
OTZ4+dj4NEiDHOCQZABOgLBaK9tkrneWAwyPIQ3EtOdjike4tLXFYvB6x+OVi/N8
Q/XRMBELz4e0+zawNiFTuucCgYAKiUScpFADIYafQyGRK9YbMmdhk3Cufnj+awmy
0j1UB7a5GNLZjWe43riMIdbQvWopenASFC5TcweJnOuEt+LUzZggREqz7VFjOaVr
rSuR+rlA++pVVZ3mGYzAAIvQW1p5mYGkkuhY0coUUH/4RmrWN4+bt45PjbFx692S
U5O6+wKBgEmCrkz7wf9YHKqIK8fXI2n0hHgr5ZcNubloLD5iJeQb2lY2WmkBVcoT
ygXlPgFy5VdwPieL5erpWSIVcEumaNEt5qf2w7DsCxrb3vJ2qKQLQjkxpNOE9+tB
aB3qJzfUK3Od9WMaeDzuNf/ysycTzTisjHJvng6hxLzZJ0FkAefT
-----END RSA PRIVATE KEY-----`

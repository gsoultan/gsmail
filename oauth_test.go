package gsmail

import (
	"encoding/base64"
	"net/smtp"
	"strings"
	"testing"
)

func TestXOAUTH2SMTPAuthStart(t *testing.T) {
	a := NewXOAUTH2Auth("user@example.com", "ya29.test-token")
	mech, ir, err := a.Start(&smtp.ServerInfo{})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if mech != "XOAUTH2" {
		t.Fatalf("expected mech XOAUTH2, got %q", mech)
	}
	// ir is already the wire-format (not base64). Ensure it contains user and bearer.
	irStr := string(ir)
	if !strings.Contains(irStr, "user=user@example.com") || !strings.Contains(irStr, "auth=Bearer ya29.test-token") {
		t.Fatalf("unexpected XOAUTH2 initial response: %q", irStr)
	}
}

func TestXOAUTH2SASLClientStart(t *testing.T) {
	c := NewXOAUTH2Client("user@example.com", "token")
	mech, ir, err := c.Start()
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if mech != "XOAUTH2" {
		t.Fatalf("expected mech XOAUTH2, got %q", mech)
	}
	want := "user=user@example.com\x01auth=Bearer token\x01\x01"
	if string(ir) != want {
		t.Fatalf("unexpected initial response: got %q want %q", string(ir), want)
	}
}

func TestOAuthBearerSMTPAuthStart(t *testing.T) {
	a := NewOAuthBearerAuth("user@example.com", "token")
	mech, ir, err := a.Start(&smtp.ServerInfo{})
	if err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	if mech != "OAUTHBEARER" {
		t.Fatalf("expected mech OAUTHBEARER, got %q", mech)
	}
	if len(ir) == 0 {
		t.Fatalf("expected non-empty initial response for OAUTHBEARER")
	}
}

func TestSMTPAuthNextIgnoresWhenNoMore(t *testing.T) {
	a := NewXOAUTH2Auth("user", "token").(*SMTPAuth)
	resp, err := a.Next(nil, false)
	if err != nil {
		t.Fatalf("Next returned error: %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response when more=false, got %v", resp)
	}
}

func TestXOAUTH2InitialResponseIsBase64Encodable(t *testing.T) {
	c := NewXOAUTH2Client("u", "tok")
	_, ir, err := c.Start()
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}
	// Ensure the response can be base64-encoded (server sends as base64 arg to AUTH)
	_ = base64.StdEncoding.EncodeToString(ir)
}

package imap

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
)

// The IMAP receive paths handle input from a server the caller does not
// control, so the interesting cases are the ones where the server misbehaves.

func TestPing(t *testing.T) {
	s := startFakeIMAP(t)
	if err := idleReceiver(s).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingFailsOnUnreachableHost(t *testing.T) {
	f := NewReceiver("127.0.0.1", 1, "u", "p", false) // nothing listening
	f.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0, InitialInterval: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := f.Ping(ctx); err == nil {
		t.Fatal("expected an error against a closed port")
	}
}

func TestReceiveReturnsNewestFirst(t *testing.T) {
	s := startFakeIMAP(t)
	emails, err := idleReceiver(s).Receive(context.Background(), 10)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(emails) == 0 {
		t.Fatal("expected at least one message")
	}
	if emails[0].Subject != "hi" {
		t.Errorf("Subject = %q, want %q", emails[0].Subject, "hi")
	}
	if emails[0].From == "" {
		t.Error("From was not parsed out of the fetched message")
	}
}

func TestSearchAppliesCriteria(t *testing.T) {
	s := startFakeIMAP(t)

	emails, err := idleReceiver(s).Search(context.Background(), gsmail.SearchOptions{
		From:    "a@example.com",
		Subject: "hi",
		Since:   time.Now().Add(-24 * time.Hour),
		Before:  time.Now().Add(24 * time.Hour),
		Unseen:  true,
	}, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(emails) == 0 {
		t.Fatal("expected at least one match")
	}
}

// A cancelled context must abort before the connection is dialled.
func TestReceiveRespectsCancelledContext(t *testing.T) {
	s := startFakeIMAP(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := idleReceiver(s).Receive(ctx, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// OAuth over a plaintext connection would hand the bearer token to anyone on
// the path. It must be refused unless explicitly permitted.
func TestOAuthRequiresTLS(t *testing.T) {
	s := startFakeIMAP(t)

	f := idleReceiver(s) // plaintext: the fake server offers no STARTTLS
	f.AuthMethod = gsmail.AuthXOAUTH2
	f.TokenSource = func(context.Context) (string, error) { return "secret-token", nil }

	_, err := f.Receive(context.Background(), 5)
	if err == nil {
		t.Fatal("expected OAuth over plaintext to be refused")
	}
	if !strings.Contains(err.Error(), "requires TLS") {
		t.Errorf("got %v, want an error naming the TLS requirement", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Error("the bearer token leaked into the error message")
	}
}

func TestOAuthReportsMissingTokenSource(t *testing.T) {
	s := startFakeIMAP(t)

	f := idleReceiver(s)
	f.AuthMethod = gsmail.AuthOAUTHBEARER
	f.AllowInsecureAuth = true // isolate the nil-token-source path

	if _, err := f.Receive(context.Background(), 5); err == nil ||
		!strings.Contains(err.Error(), "token source") {
		t.Errorf("got %v, want an error naming the missing token source", err)
	}
}

func TestOAuthPropagatesTokenSourceFailure(t *testing.T) {
	s := startFakeIMAP(t)

	sentinel := errors.New("token endpoint unavailable")
	f := idleReceiver(s)
	f.AuthMethod = gsmail.AuthXOAUTH2
	f.AllowInsecureAuth = true
	f.TokenSource = func(context.Context) (string, error) { return "", sentinel }

	_, err := f.Receive(context.Background(), 5)
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v, want the token source error to be wrapped", err)
	}
}

// tlsConfig is the single place the TLS floor is decided, so its defaults are
// worth pinning: 1.1 was the old default and is a downgrade from Go's own.
func TestTLSConfigDefaults(t *testing.T) {
	f := NewReceiver("imap.example.com", 993, "u", "p", true)

	cfg := f.tlsConfig()
	if cfg.MinVersion != DefaultMinTLSVersion {
		t.Errorf("MinVersion = %#x, want %#x (TLS 1.2)", cfg.MinVersion, DefaultMinTLSVersion)
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Error("the default TLS floor must not be below 1.2")
	}
	if cfg.CipherSuites != nil {
		t.Error("CipherSuites should default to nil so the list tracks the standard library")
	}
	if cfg.ServerName != "imap.example.com" {
		t.Errorf("ServerName = %q; certificate verification depends on it", cfg.ServerName)
	}
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must default to false")
	}

	f.MinVersion = tls.VersionTLS13
	f.InsecureSkipVerify = true
	f.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
	cfg = f.tlsConfig()
	if cfg.MinVersion != tls.VersionTLS13 || !cfg.InsecureSkipVerify || len(cfg.CipherSuites) != 1 {
		t.Errorf("explicit TLS settings were not plumbed through: %+v", cfg)
	}
}

// Receiver must satisfy the interface it claims.
var _ gsmail.Receiver = (*Receiver)(nil)

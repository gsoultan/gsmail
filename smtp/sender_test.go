package smtp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
)

func senderFor(t *testing.T, addr string) *Sender {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	s := NewSender(host, port, "", "", false)
	s.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0, InitialInterval: time.Millisecond})
	return s
}

func TestPing(t *testing.T) {
	s := senderFor(t, fakeSMTPServer(t))
	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPingFailsOnClosedPort(t *testing.T) {
	s := NewSender("127.0.0.1", 1, "", "", false)
	s.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0, InitialInterval: time.Millisecond})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := s.Ping(ctx); err == nil {
		t.Fatal("expected an error against a closed port")
	}
}

// tlsConfig is the single place the TLS floor is decided. TLS 1.1 was the old
// default and is a downgrade from Go's own client default, so pin it.
func TestTLSConfigDefaults(t *testing.T) {
	s := NewSender("smtp.example.com", 587, "u", "p", false)

	cfg := s.tlsConfig("smtp.example.com")
	if cfg.MinVersion != DefaultMinTLSVersion {
		t.Errorf("MinVersion = %#x, want %#x (TLS 1.2)", cfg.MinVersion, DefaultMinTLSVersion)
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Error("the default TLS floor must not be below 1.2")
	}
	if cfg.CipherSuites != nil {
		t.Error("CipherSuites should default to nil so the list tracks the standard library")
	}
	if cfg.ServerName != "smtp.example.com" {
		t.Errorf("ServerName = %q; certificate verification depends on it", cfg.ServerName)
	}
	if cfg.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must default to false")
	}

	s.MinVersion = tls.VersionTLS13
	s.MaxVersion = tls.VersionTLS13
	s.InsecureSkipVerify = true
	s.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
	cfg = s.tlsConfig("other.example.com")
	if cfg.MinVersion != tls.VersionTLS13 || cfg.MaxVersion != tls.VersionTLS13 ||
		!cfg.InsecureSkipVerify || len(cfg.CipherSuites) != 1 {
		t.Errorf("explicit TLS settings were not plumbed through: %+v", cfg)
	}
}

func TestUseOAuth(t *testing.T) {
	s := NewSender("smtp.example.com", 587, "user@example.com", "", false)

	called := false
	s.UseOAuth(gsmail.AuthXOAUTH2, func(context.Context) (string, error) {
		called = true
		return "tok", nil
	})

	if s.AuthMethod != gsmail.AuthXOAUTH2 {
		t.Errorf("AuthMethod = %q, want XOAUTH2", s.AuthMethod)
	}
	if s.TokenSource == nil {
		t.Fatal("TokenSource was not set")
	}
	if _, err := s.TokenSource(context.Background()); err != nil || !called {
		t.Error("the configured token source was not the one stored")
	}
}

// A bearer token must not be handed to a server that offers no TLS. The fake
// server advertises no STARTTLS, so this is the stripped-TLS case.
func TestOAuthRefusesPlaintext(t *testing.T) {
	s := senderFor(t, fakeSMTPServer(t))
	s.Username = "user@example.com"
	s.UseOAuth(gsmail.AuthXOAUTH2, func(context.Context) (string, error) {
		return "secret-bearer-token", nil
	})

	err := s.Send(context.Background(), gsmail.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("x"),
	})
	if err == nil {
		t.Fatal("expected OAuth over plaintext to be refused")
	}
	if !strings.Contains(err.Error(), "requires TLS") {
		t.Errorf("got %v, want an error naming the TLS requirement", err)
	}
	if strings.Contains(err.Error(), "secret-bearer-token") {
		t.Error("the bearer token leaked into the error message")
	}
}

func TestOAuthReportsMissingTokenSource(t *testing.T) {
	s := senderFor(t, fakeSMTPServer(t))
	s.AuthMethod = gsmail.AuthOAUTHBEARER // no TokenSource

	err := s.Send(context.Background(), gsmail.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("x"),
	})
	if err == nil || !strings.Contains(err.Error(), "token source") {
		t.Errorf("got %v, want an error naming the missing token source", err)
	}
	if gsmail.IsRetryable(err) {
		t.Error("a missing token source is a configuration error, not a transient one")
	}
}

func TestOAuthPropagatesTokenSourceFailure(t *testing.T) {
	sentinel := errors.New("token endpoint unavailable")

	s := senderFor(t, fakeSMTPServer(t))
	s.UseOAuth(gsmail.AuthXOAUTH2, func(context.Context) (string, error) { return "", sentinel })

	err := s.Send(context.Background(), gsmail.Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("x"),
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("got %v, want the token source error to be wrapped", err)
	}
}

func TestPoolStatsRequiresPool(t *testing.T) {
	s := NewSender("smtp.example.com", 587, "", "", false)

	if _, err := s.PoolStats(); err == nil {
		t.Error("PoolStats should report that no pool is enabled")
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close without a pool should be a no-op, got %v", err)
	}

	s.EnablePool(PoolConfig{MaxIdle: 1})
	if _, err := s.PoolStats(); err != nil {
		t.Errorf("PoolStats with a pool enabled: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// A message that cannot be rendered must fail before a connection is opened,
// and must not be retried.
func TestRenderFailureIsPermanentAndNeverDials(t *testing.T) {
	s := senderFor(t, fakeSMTPServer(t))
	s.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 3, InitialInterval: time.Millisecond})

	e := gsmail.Email{From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("x")}
	e.SetHeader("X-Bad\r\nBcc", "attacker@evil.test")

	err := s.Send(context.Background(), e)
	if err == nil {
		t.Fatal("expected an error for an illegal header name")
	}
	if gsmail.IsRetryable(err) {
		t.Error("an illegal header name is permanent, not retryable")
	}
}

// Sender must satisfy the interface it claims.
var _ gsmail.Sender = (*Sender)(nil)

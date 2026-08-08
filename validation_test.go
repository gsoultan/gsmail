package gsmail

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// startMockMXServer starts a minimal SMTP server that accepts
// exist@example.com and rejects everything else. It returns the port it
// listens on and stops when the test finishes.
func startMockMXServer(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				fmt.Fprint(c, "220 mail.example.com ESMTP\r\n")
				buf := make([]byte, 1024)
				for {
					_ = c.SetDeadline(time.Now().Add(2 * time.Second))
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					cmd := string(buf[:n])
					switch {
					case strings.HasPrefix(cmd, "HELO"), strings.HasPrefix(cmd, "EHLO"):
						fmt.Fprint(c, "250-mail.example.com\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(cmd, "MAIL FROM"):
						fmt.Fprint(c, "250 OK\r\n")
					case strings.HasPrefix(cmd, "RCPT TO:<exist@example.com>"):
						fmt.Fprint(c, "250 OK\r\n")
					case strings.HasPrefix(cmd, "RCPT TO"):
						fmt.Fprint(c, "550 User not found\r\n")
					case strings.HasPrefix(cmd, "QUIT"):
						fmt.Fprint(c, "221 Goodbye\r\n")
						return
					}
				}
			}(conn)
		}
	}()

	return port
}

func TestValidatorMailboxProbe(t *testing.T) {
	t.Parallel()

	port := startMockMXServer(t)

	v := Validator{
		CheckMX:      true,
		CheckMailbox: true,
		SMTPPort:     port,
		Resolver: stubResolver{
			mx: func(ctx context.Context, domain string) ([]*net.MX, error) {
				if domain == "example.com" {
					return []*net.MX{{Host: "127.0.0.1", Pref: 10}}, nil
				}
				return nil, fmt.Errorf("no such domain")
			},
		},
	}

	t.Run("ValidExistence", func(t *testing.T) {
		if err := v.Validate(t.Context(), "exist@example.com"); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("InvalidExistence", func(t *testing.T) {
		if err := v.Validate(t.Context(), "nonexist@example.com"); err == nil {
			t.Error("expected error for non-existent user")
		}
	})

	t.Run("InvalidDomain", func(t *testing.T) {
		if err := v.Validate(t.Context(), "test@nodomain.com"); err == nil {
			t.Error("expected error for non-existent domain")
		}
	})

	t.Run("DisposableDomain", func(t *testing.T) {
		err := v.Validate(t.Context(), "user@10minutemail.com")
		if !errors.Is(err, ErrDisposableEmail) {
			t.Errorf("expected ErrDisposableEmail, got: %v", err)
		}
		if IsRetryable(err) {
			t.Error("a disposable address must not be retryable")
		}
	})
}

func TestValidatorOfflineByDefault(t *testing.T) {
	t.Parallel()

	// The zero Validator must never touch the network. A resolver that fails
	// the test if called proves it.
	v := Validator{Resolver: stubResolver{
		mx: func(ctx context.Context, name string) ([]*net.MX, error) {
			t.Error("zero Validator must not perform an MX lookup")
			return nil, nil
		},
	}}

	if err := v.Validate(t.Context(), "someone@example.com"); err != nil {
		t.Errorf("expected a well formed address to pass, got %v", err)
	}
}

func TestValidatorRejectsBadSyntax(t *testing.T) {
	t.Parallel()

	err := ValidateEmailSyntax("not-an-address")
	if !errors.Is(err, ErrInvalidEmailFormat) {
		t.Fatalf("expected ErrInvalidEmailFormat, got %v", err)
	}
	if IsRetryable(err) {
		t.Error("a malformed address must not be retryable")
	}
}

func TestValidatorCheckMXOnly(t *testing.T) {
	t.Parallel()

	v := Validator{
		CheckMX: true,
		Resolver: stubResolver{
			mx: func(ctx context.Context, domain string) ([]*net.MX, error) {
				return nil, nil // resolves, but no records
			},
		},
	}

	err := v.Validate(t.Context(), "someone@example.com")
	if !errors.Is(err, ErrNoMXRecords) {
		t.Fatalf("expected ErrNoMXRecords, got %v", err)
	}
}

func TestValidatorCustomDisposableList(t *testing.T) {
	t.Parallel()

	v := Validator{DisposableDomains: map[string]struct{}{"blocked.example": {}}}

	if err := v.Validate(t.Context(), "user@blocked.example"); !errors.Is(err, ErrDisposableEmail) {
		t.Errorf("expected the custom list to reject the address, got %v", err)
	}
	// A domain from the built-in list must pass once the list is overridden.
	if err := v.Validate(t.Context(), "user@10minutemail.com"); err != nil {
		t.Errorf("expected the overridden list to allow the address, got %v", err)
	}
}

func TestDefaultDisposableDomainsIsACopy(t *testing.T) {
	t.Parallel()

	set := DefaultDisposableDomains()
	if len(set) == 0 {
		t.Fatal("expected a non-empty default set")
	}
	delete(set, "10minutemail.com")

	if !IsDisposableEmail("user@10minutemail.com") {
		t.Error("mutating the returned map must not affect package state")
	}
}

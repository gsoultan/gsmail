package gsmail_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
	gimap "github.com/gsoultan/gsmail/imap"
	gpop3 "github.com/gsoultan/gsmail/pop3"
	gsmtp "github.com/gsoultan/gsmail/smtp"
)

func TestSMTPSenderOAuth2(t *testing.T) {
	// Start a mock SMTP server
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	addr := l.Addr().String()
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	fmt.Sscanf(port, "%d", &p)

	// Channel to signal authentication was attempted with correct mechanism
	authAttempted := make(chan string, 1)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Send welcome
		fmt.Fprintf(conn, "220 Welcome\r\n")

		// Simple state machine for mock SMTP
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Fields(line)
			if len(parts) == 0 {
				continue
			}
			switch parts[0] {
			case "EHLO":
				fmt.Fprintf(conn, "250-Hello\r\n250-AUTH XOAUTH2\r\n250 OK\r\n")
			case "AUTH":
				if len(parts) > 1 {
					authAttempted <- parts[1]
				}
				fmt.Fprintf(conn, "235 Authentication succeeded\r\n")
			case "MAIL":
				fmt.Fprintf(conn, "250 OK\r\n")
			case "RCPT":
				fmt.Fprintf(conn, "250 OK\r\n")
			case "DATA":
				fmt.Fprintf(conn, "354 Start mail input; end with <CRLF>.<CRLF>\r\n")
				for scanner.Scan() {
					if scanner.Text() == "." {
						break
					}
				}
				fmt.Fprintf(conn, "250 OK\r\n")
			case "QUIT":
				fmt.Fprintf(conn, "221 Bye\r\n")
				return
			}
		}
	}()

	sender := gsmtp.NewSender(host, p, "user@example.com", "", false)
	sender.AllowInsecureAuth = true // required for OAuth2 without TLS in this test
	sender.UseOAuth(gsmail.AuthXOAUTH2, func(ctx context.Context) (string, error) {
		return "test-token", nil
	})

	email := gsmail.Email{
		From:    "user@example.com",
		To:      []string{"to@example.com"},
		Subject: "Test",
		Body:    []byte("test body"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = sender.Send(ctx, email)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case mech := <-authAttempted:
		if mech != "XOAUTH2" {
			t.Errorf("expected mechanism XOAUTH2, got %q", mech)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for auth attempt")
	}
}

func TestPOP3ReceiverOAuth2(t *testing.T) {
	// Start a mock POP3 server
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	addr := l.Addr().String()
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	fmt.Sscanf(port, "%d", &p)

	authAttempted := make(chan string, 1)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		fmt.Fprintf(conn, "+OK Welcome\r\n")

		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Fields(line)
			if len(parts) == 0 {
				continue
			}
			switch parts[0] {
			case "CAPA":
				fmt.Fprintf(conn, "+OK Capability list follows\r\nSASL XOAUTH2\r\n.\r\n")
			case "AUTH":
				if len(parts) > 1 {
					authAttempted <- parts[1]
				}
				fmt.Fprintf(conn, "+OK Success\r\n")
			case "STAT":
				fmt.Fprintf(conn, "+OK 0 0\r\n")
			case "QUIT":
				fmt.Fprintf(conn, "+OK Bye\r\n")
				return
			}
		}
	}()

	receiver := gpop3.NewReceiver(host, p, "user@example.com", "", false)
	receiver.AllowInsecureAuth = true
	receiver.AuthMethod = gsmail.AuthXOAUTH2
	receiver.TokenSource = func(ctx context.Context) (string, error) {
		return "test-token", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = receiver.Receive(ctx, 1)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	select {
	case mech := <-authAttempted:
		if mech != "XOAUTH2" {
			t.Errorf("expected mechanism XOAUTH2, got %q", mech)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for auth attempt")
	}
}

func TestIMAPReceiverOAuth2(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	addr := l.Addr().String()
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	fmt.Sscanf(port, "%d", &p)

	authAttempted := make(chan string, 1)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		fmt.Fprintf(conn, "* OK IMAP4rev1 Service Ready\r\n")

		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			line := string(buf[:n])
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			cmd := strings.ToUpper(parts[1])

			switch cmd {
			case "CAPABILITY":
				fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 AUTH=XOAUTH2\r\n%s OK CAPABILITY completed\r\n", tag)
			case "AUTHENTICATE":
				if len(parts) > 2 {
					authAttempted <- parts[2]
				}
				fmt.Fprintf(conn, "%s OK AUTHENTICATE completed\r\n", tag)
			case "SELECT":
				fmt.Fprintf(conn, "* 0 EXISTS\r\n%s OK [READ-ONLY] SELECT completed\r\n", tag)
			case "LOGOUT":
				fmt.Fprintf(conn, "* BYE IMAP4rev1 Server logging out\r\n%s OK LOGOUT completed\r\n", tag)
				return
			}
		}
	}()

	receiver := gimap.NewReceiver(host, p, "user@example.com", "", false)
	receiver.AllowInsecureAuth = true
	receiver.AuthMethod = gsmail.AuthXOAUTH2
	receiver.TokenSource = func(ctx context.Context) (string, error) {
		return "test-token", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err = receiver.Receive(ctx, 1)
	if err != nil {
		t.Fatalf("Receive failed: %v", err)
	}

	select {
	case mech := <-authAttempted:
		if mech != "XOAUTH2" {
			t.Errorf("expected mechanism XOAUTH2, got %q", mech)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timeout waiting for auth attempt")
	}
}

func TestIMAPReceiverSTARTTLS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping flaky IMAP STARTTLS test on Windows")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()

	addr := l.Addr().String()
	host, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)

	startTLSAttempted := make(chan bool, 1)

	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		fmt.Fprintf(conn, "* OK IMAP4rev1 Service Ready\r\n")

		buf := make([]byte, 1024)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				return
			}
			line := string(buf[:n])
			parts := strings.Fields(line)
			if len(parts) < 2 {
				continue
			}
			tag := parts[0]
			cmd := strings.ToUpper(parts[1])

			switch cmd {
			case "CAPABILITY":
				fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 STARTTLS\r\n%s OK CAPABILITY completed\r\n", tag)
			case "STARTTLS":
				fmt.Fprintf(conn, "%s OK Begin TLS negotiation now\r\n", tag)
				startTLSAttempted <- true
				return // Close connection immediately to fail the handshake fast
			case "LOGOUT":
				fmt.Fprintf(conn, "* BYE IMAP4rev1 logging out\r\n%s OK LOGOUT completed\r\n", tag)
				return
			}
		}
	}()

	receiver := gimap.NewReceiver(host, p, "user@example.com", "pass", false)
	receiver.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0})
	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	// This will fail because our mock doesn't actually perform the TLS handshake
	_ = receiver.Ping(ctx)

	select {
	case <-startTLSAttempted:
		// Success
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for STARTTLS attempt")
	}
}

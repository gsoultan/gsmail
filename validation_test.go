package gsmail

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestValidateEmailExistence(t *testing.T) {
	// 1. Mock SMTP Server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()

	_, port, _ := net.SplitHostPort(ln.Addr().String())
	oldPort := smtpPort
	smtpPort = port
	defer func() { smtpPort = oldPort }()

	stop := make(chan struct{})
	defer close(stop)

	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if tcpLn, ok := ln.(*net.TCPListener); ok {
					tcpLn.SetDeadline(time.Now().Add(100 * time.Millisecond))
				}
				conn, err := ln.Accept()
				if err != nil {
					continue
				}
				go func(c net.Conn) {
					defer c.Close()
					fmt.Fprint(c, "220 mail.example.com ESMTP\r\n")
					buf := make([]byte, 1024)
					for {
						c.SetDeadline(time.Now().Add(1 * time.Second))
						n, err := c.Read(buf)
						if err != nil {
							return
						}
						cmd := UnsafeBytesToString(buf[:n])
						if strings.HasPrefix(cmd, "HELO") || strings.HasPrefix(cmd, "EHLO") {
							fmt.Fprint(c, "250-mail.example.com\r\n250 AUTH PLAIN\r\n")
						} else if strings.HasPrefix(cmd, "MAIL FROM") {
							fmt.Fprint(c, "250 OK\r\n")
						} else if strings.HasPrefix(cmd, "RCPT TO:<exist@example.com>") {
							fmt.Fprint(c, "250 OK\r\n")
						} else if strings.HasPrefix(cmd, "RCPT TO") {
							fmt.Fprint(c, "550 User not found\r\n")
						} else if strings.HasPrefix(cmd, "QUIT") {
							fmt.Fprint(c, "221 Goodbye\r\n")
							return
						}
					}
				}(conn)
			}
		}
	}()

	// 2. Mock MX Lookup
	oldLookupMX := lookupMX
	lookupMX = func(ctx context.Context, domain string) ([]*net.MX, error) {
		if domain == "example.com" {
			return []*net.MX{{Host: "127.0.0.1", Pref: 10}}, nil
		}
		return nil, fmt.Errorf("no such domain")
	}
	defer func() { lookupMX = oldLookupMX }()

	t.Run("ValidExistence", func(t *testing.T) {
		err := ValidateEmailExistence(context.Background(), "exist@example.com")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("InvalidExistence", func(t *testing.T) {
		err := ValidateEmailExistence(context.Background(), "nonexist@example.com")
		if err == nil {
			t.Error("expected error for non-existent user")
		}
	})

	t.Run("InvalidDomain", func(t *testing.T) {
		err := ValidateEmailExistence(context.Background(), "test@nodomain.com")
		if err == nil {
			t.Error("expected error for non-existent domain")
		}
	})
}

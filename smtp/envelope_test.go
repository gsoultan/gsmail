package smtp

import (
	"bufio"
	"context"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
)

// capture records what a single SMTP conversation asked the server to do.
type capture struct {
	rcptTo []string
	data   string
}

// serveOnce answers one SMTP conversation and records RCPT TO and the message
// body, which is what distinguishes the envelope from the headers.
func serveOnce(t *testing.T) (host string, port int, got *capture, done chan struct{}) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	h, p, _ := net.SplitHostPort(ln.Addr().String())
	portInt, _ := strconv.Atoi(p)

	got = &capture{}
	done = make(chan struct{})

	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := textproto.NewReader(bufio.NewReader(conn))
		writer := textproto.NewWriter(bufio.NewWriter(conn))
		_ = writer.PrintfLine("220 localhost ESMTP")

		var body strings.Builder
		for {
			line, err := reader.ReadLine()
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
				_ = writer.PrintfLine("250-localhost")
				_ = writer.PrintfLine("250 OK")
			case strings.HasPrefix(line, "MAIL FROM:"):
				_ = writer.PrintfLine("250 OK")
			case strings.HasPrefix(line, "RCPT TO:"):
				addr := strings.Trim(strings.TrimPrefix(line, "RCPT TO:"), "<> ")
				got.rcptTo = append(got.rcptTo, addr)
				_ = writer.PrintfLine("250 OK")
			case line == "DATA":
				_ = writer.PrintfLine("354 Start mail input; end with <CRLF>.<CRLF>")
				for {
					l, err := reader.ReadLine()
					if err != nil || l == "." {
						break
					}
					body.WriteString(l)
					body.WriteString("\n")
				}
				got.data = body.String()
				_ = writer.PrintfLine("250 OK")
			case line == "QUIT":
				_ = writer.PrintfLine("221 Goodbye")
				return
			default:
				_ = writer.PrintfLine("250 OK")
			}
		}
	}()

	return h, portInt, got, done
}

func headerLine(message, name string) string {
	for _, line := range strings.Split(message, "\n") {
		if strings.HasPrefix(strings.ToLower(line), strings.ToLower(name)+":") {
			return strings.TrimSpace(line[len(name)+1:])
		}
	}
	return ""
}

// Without Envelope the behaviour is unchanged: every address named in the
// headers is also delivered to.
func TestWithoutEnvelopeEveryNamedAddressIsDelivered(t *testing.T) {
	host, port, got, done := serveOnce(t)

	err := NewSender(host, port, "", "", false).Send(context.Background(), gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"a@example.com"},
		Cc:      []string{"b@example.com"},
		Bcc:     []string{"c@example.com"},
		Subject: "Hi",
		Body:    []byte("hello"),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	<-done

	if len(got.rcptTo) != 3 {
		t.Fatalf("expected 3 envelope recipients, got %v", got.rcptTo)
	}
}

// The point of the field: one copy per recipient, with the headers still
// describing the whole audience. Without this, showing a Cc list would deliver
// a copy to every Cc address for every recipient.
func TestEnvelopeReplacesTheDerivedRecipientList(t *testing.T) {
	host, port, got, done := serveOnce(t)

	err := NewSender(host, port, "", "", false).Send(context.Background(), gsmail.Email{
		From:     "sender@example.com",
		To:       []string{"a@example.com"},
		Cc:       []string{"b@example.com", "c@example.com"},
		Envelope: []string{"b@example.com"},
		Subject:  "Hi",
		Body:     []byte("hello"),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	<-done

	if len(got.rcptTo) != 1 || got.rcptTo[0] != "b@example.com" {
		t.Fatalf("envelope should be exactly the one address, got %v", got.rcptTo)
	}

	// The headers must still name everyone, or the recipient cannot see who
	// else was copied — which is the whole reason for the field.
	if to := headerLine(got.data, "To"); !strings.Contains(to, "a@example.com") {
		t.Errorf("To header lost the original recipient: %q", to)
	}
	cc := headerLine(got.data, "Cc")
	if !strings.Contains(cc, "b@example.com") || !strings.Contains(cc, "c@example.com") {
		t.Errorf("Cc header should list the whole cc audience, got %q", cc)
	}
}

// Bcc is deliberately not folded in when Envelope is set: the caller decides
// who receives a copy, and a blind address must not be disclosed in a header.
func TestEnvelopeIsNotSupplementedByBcc(t *testing.T) {
	host, port, got, done := serveOnce(t)

	err := NewSender(host, port, "", "", false).Send(context.Background(), gsmail.Email{
		From:     "sender@example.com",
		To:       []string{"a@example.com"},
		Bcc:      []string{"hidden@example.com"},
		Envelope: []string{"a@example.com"},
		Subject:  "Hi",
		Body:     []byte("hello"),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	<-done

	if len(got.rcptTo) != 1 || got.rcptTo[0] != "a@example.com" {
		t.Fatalf("Bcc must not be added to an explicit envelope, got %v", got.rcptTo)
	}
	if strings.Contains(got.data, "hidden@example.com") {
		t.Error("a blind address appeared in the message")
	}
}

func TestAnEmptyEnvelopeWithNoAddressesIsRejected(t *testing.T) {
	err := NewSender("127.0.0.1", 1, "", "", false).Send(context.Background(), gsmail.Email{
		From:    "sender@example.com",
		Subject: "Hi",
		Body:    []byte("hello"),
	})
	if err == nil {
		t.Fatal("a message with no recipients must be refused")
	}
	if !strings.Contains(err.Error(), "no recipients") {
		t.Errorf("unexpected error: %v", err)
	}
}

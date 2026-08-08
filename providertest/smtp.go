package providertest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"

	"github.com/gsoultan/gsmail"
)

// RunSMTP is the conformance suite for a Sender that speaks SMTP.
//
// It asserts the same message-level contract as Run -- custom headers reach
// the wire, reserved headers do not, a cid: attachment is inline -- against a
// message decoded off the wire rather than out of a JSON body, so the two
// transports cannot drift apart on the things that matter. They have drifted
// twice already.
//
// The failure cases differ because the protocols do: SMTP has reply codes
// rather than status codes, no Retry-After, and no response body to carry a
// provider's explanation.
func RunSMTP(t *testing.T, h SMTPHarness) {
	t.Helper()

	if h.NewSender == nil {
		t.Fatal("providertest: SMTPHarness needs NewSender")
	}

	t.Run("DeliversBasicMessage", func(t *testing.T) {
		got := h.capture(t, basicEmail())
		if got.Subject != "conformance" {
			t.Errorf("Subject = %q, want %q", got.Subject, "conformance")
		}
		if !strings.Contains(got.Text, "hello") {
			t.Errorf("body = %q, want it to contain %q", got.Text, "hello")
		}
	})

	t.Run("DeliversAllRecipients", func(t *testing.T) {
		assertRecipients(t, h.capture(t, recipientEmail()))
	})

	// Bcc recipients belong in the envelope only. Writing them into the
	// message header discloses them to every other recipient.
	t.Run("BccIsEnvelopeOnly", func(t *testing.T) {
		got, raw := h.captureRaw(t, recipientEmail())
		if len(got.Bcc) != 1 {
			t.Errorf("Bcc recipient missing from RCPT TO: %v", got.Bcc)
		}
		headers, _, _ := strings.Cut(raw, "\r\n\r\n")
		if strings.Contains(strings.ToLower(headers), "bcc:") {
			t.Errorf("Bcc leaked into the message headers:\n%s", headers)
		}
		if strings.Contains(headers, "d@example.com") {
			t.Errorf("the Bcc address appears in the headers:\n%s", headers)
		}
	})

	t.Run("ForwardsCustomHeaders", func(t *testing.T) {
		assertCustomHeaders(t, h.capture(t, customHeaderEmail()))
	})

	t.Run("DropsReservedHeaders", func(t *testing.T) {
		assertReservedHeaders(t, h.capture(t, reservedHeaderEmail()))
	})

	t.Run("MarksContentIDAttachmentInline", func(t *testing.T) {
		assertInlineAttachment(t, h.capture(t, inlineAttachmentEmail()))
	})

	// Every message needs a Date and a Message-ID; some receivers reject mail
	// without them outright.
	t.Run("GeneratesDateAndMessageID", func(t *testing.T) {
		_, raw := h.captureRaw(t, basicEmail())
		headers, _, _ := strings.Cut(raw, "\r\n\r\n")
		for _, name := range []string{"Date:", "Message-ID:"} {
			if !strings.Contains(headers, name) {
				t.Errorf("generated message is missing %s\n%s", name, headers)
			}
		}
	})

	t.Run("RejectsIllegalHeaderName", func(t *testing.T) {
		srv := startFakeSMTP(t, faultNone)
		s := h.sender(t, srv)

		e := basicEmail()
		e.SetHeader("X-Bad\r\nBcc", "attacker@evil.test")

		err := s.Send(context.Background(), e)
		if err == nil {
			t.Fatal("expected an error for a header name containing CRLF")
		}
		if gsmail.IsRetryable(err) {
			t.Error("an illegal header name is permanent, not retryable")
		}
		if n := srv.deliveries(); n != 0 {
			t.Errorf("delivered %d message(s) despite an invalid header", n)
		}
	})

	t.Run("RejectsMessageWithNoRecipients", func(t *testing.T) {
		srv := startFakeSMTP(t, faultNone)
		s := h.sender(t, srv)

		e := basicEmail()
		e.To = nil

		if err := s.Send(context.Background(), e); err == nil {
			t.Fatal("expected an error for a message with no recipients")
		}
		if n := srv.connections(); n != 0 {
			t.Errorf("opened %d connection(s) for a message with no recipients", n)
		}
	})

	// A 5xx reply is a permanent refusal. Sending four times just puts four
	// rejections on the record with the receiving system.
	t.Run("DoesNotRetryPermanentReply", func(t *testing.T) {
		srv := startFakeSMTP(t, faultPermanent)
		s := h.sender(t, srv)

		if err := s.Send(context.Background(), basicEmail()); err == nil {
			t.Fatal("expected an error on a 550 reply")
		}
		if n := srv.rcptAttempts(); n != 1 {
			t.Errorf("attempted RCPT %d times after a 550, want 1", n)
		}
	})

	t.Run("RetriesTransientReply", func(t *testing.T) {
		srv := startFakeSMTP(t, faultTransient)
		s := h.sender(t, srv)

		if err := s.Send(context.Background(), basicEmail()); err == nil {
			t.Fatal("expected an error on a 451 reply")
		}
		if want := fastRetries.MaxRetries + 1; srv.rcptAttempts() != want {
			t.Errorf("attempted RCPT %d times after a 451, want %d", srv.rcptAttempts(), want)
		}
	})

	t.Run("RespectsMaxRetriesZero", func(t *testing.T) {
		srv := startFakeSMTP(t, faultTransient)
		s := h.NewSender(t, srv.host, srv.port)
		s.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0})

		if err := s.Send(context.Background(), basicEmail()); err == nil {
			t.Fatal("expected an error")
		}
		if n := srv.rcptAttempts(); n != 1 {
			t.Errorf("attempted RCPT %d times with MaxRetries=0, want 1", n)
		}
	})

	t.Run("RespectsContextCancellation", func(t *testing.T) {
		srv := startFakeSMTP(t, faultTransient)
		s := h.sender(t, srv)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := s.Send(ctx, basicEmail())
		if err == nil {
			t.Fatal("expected an error for a cancelled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want an error wrapping context.Canceled", err)
		}
	})
}

// SMTPHarness adapts an SMTP-speaking Sender to the suite. Unlike the HTTP
// harness it needs no Decode: the wire format is standardised, so the suite
// parses the delivered message itself.
type SMTPHarness struct {
	// Name identifies the provider in test output.
	Name string

	// NewSender builds a Sender pointed at the given host and port, with no
	// TLS and no authentication.
	NewSender func(t *testing.T, host string, port int) gsmail.Sender
}

func (h SMTPHarness) sender(t *testing.T, srv *fakeSMTP) gsmail.Sender {
	t.Helper()
	s := h.NewSender(t, srv.host, srv.port)
	s.SetRetryConfig(fastRetries)
	return s
}

// capture sends one message and returns the normalised view of it.
func (h SMTPHarness) capture(t *testing.T, email gsmail.Email) Sent {
	t.Helper()
	got, _ := h.captureRaw(t, email)
	return got
}

// captureRaw also returns the exact bytes that crossed the wire.
func (h SMTPHarness) captureRaw(t *testing.T, email gsmail.Email) (Sent, string) {
	t.Helper()

	srv := startFakeSMTP(t, faultNone)
	s := h.sender(t, srv)

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}

	d := srv.lastDelivery()
	if d == nil {
		t.Fatal("provider never delivered a message")
	}
	return decodeSMTP(t, d), d.data
}

// decodeSMTP turns a delivered SMTP transaction into the shared view.
func decodeSMTP(t *testing.T, d *delivery) Sent {
	t.Helper()

	parsed, err := gsmail.ParseRawEmail([]byte(d.data))
	if err != nil {
		t.Fatalf("delivered message does not parse: %v\n%s", err, d.data)
	}

	msg, err := mail.ReadMessage(strings.NewReader(d.data))
	if err != nil {
		t.Fatalf("delivered message has unreadable headers: %v", err)
	}

	out := Sent{
		From:    d.from,
		Subject: parsed.Subject,
		Text:    string(parsed.Body),
		HTML:    string(parsed.HTMLBody),
		Headers: map[string]string{},
	}

	// SMTP carries recipients in the envelope, so classify the RCPT TO list
	// against the message headers rather than trusting either alone.
	inHeader := func(field string) map[string]bool {
		set := map[string]bool{}
		list, err := msg.Header.AddressList(field)
		if err != nil {
			return set
		}
		for _, a := range list {
			set[a.Address] = true
		}
		return set
	}
	toSet, ccSet := inHeader("To"), inHeader("Cc")

	for _, rcpt := range d.rcpt {
		switch {
		case toSet[rcpt]:
			out.To = append(out.To, rcpt)
		case ccSet[rcpt]:
			out.Cc = append(out.Cc, rcpt)
		default:
			// In neither header: an envelope-only recipient, i.e. Bcc.
			out.Bcc = append(out.Bcc, rcpt)
		}
	}

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
		out.Attachments = append(out.Attachments, Attachment{
			Filename:    att.Filename,
			Disposition: disposition,
			ContentID:   att.ContentID,
		})
	}
	return out
}

// --- fake SMTP server ----------------------------------------------------

type fault int

const (
	faultNone fault = iota
	faultPermanent
	faultTransient
)

type delivery struct {
	from string
	rcpt []string
	data string
}

type fakeSMTP struct {
	host string
	port int

	mu        sync.Mutex
	delivered []*delivery
	rcptCount int
	connCount int
}

func (s *fakeSMTP) lastDelivery() *delivery {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.delivered) == 0 {
		return nil
	}
	return s.delivered[len(s.delivered)-1]
}

func (s *fakeSMTP) deliveries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.delivered)
}

func (s *fakeSMTP) rcptAttempts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rcptCount
}

func (s *fakeSMTP) connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connCount
}

// startFakeSMTP runs a minimal ESMTP server that injects the given fault.
// It deliberately does not advertise STARTTLS or AUTH, so the sender takes
// its plain path.
func startFakeSMTP(t *testing.T, f fault) *fakeSMTP {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	var port int
	_, _ = fmt.Sscanf(portStr, "%d", &port)

	s := &fakeSMTP{host: host, port: port}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.connCount++
			s.mu.Unlock()
			go s.serve(conn, f)
		}
	}()

	return s
}

func (s *fakeSMTP) serve(conn net.Conn, f fault) {
	defer conn.Close()

	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)
	out := func(format string, a ...any) {
		fmt.Fprintf(w, format+"\r\n", a...)
		_ = w.Flush()
	}

	out("220 fake ESMTP ready")

	cur := &delivery{}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
			// No STARTTLS, no AUTH: keep the sender on its plain path.
			out("250-fake greets you")
			out("250 8BITMIME")

		case strings.HasPrefix(upper, "MAIL FROM:"):
			cur = &delivery{from: extractAddr(line)}
			out("250 sender ok")

		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.rcptCount++
			s.mu.Unlock()
			switch f {
			case faultPermanent:
				out("550 5.1.1 no such user")
			case faultTransient:
				out("451 4.3.0 try again later")
			default:
				cur.rcpt = append(cur.rcpt, extractAddr(line))
				out("250 recipient ok")
			}

		case upper == "DATA":
			out("354 send it")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" || l == ".\n" {
					break
				}
				// Undo dot-stuffing.
				if strings.HasPrefix(l, "..") {
					l = l[1:]
				}
				body.WriteString(l)
			}
			cur.data = body.String()
			s.mu.Lock()
			s.delivered = append(s.delivered, cur)
			s.mu.Unlock()
			out("250 queued")

		case upper == "RSET":
			cur = &delivery{}
			out("250 reset")

		case upper == "NOOP":
			out("250 ok")

		case upper == "QUIT":
			out("221 bye")
			return

		default:
			out("250 ok")
		}
	}
}

// extractAddr pulls the address out of "MAIL FROM:<a@b.test>".
func extractAddr(line string) string {
	i := strings.IndexByte(line, '<')
	j := strings.LastIndexByte(line, '>')
	if i >= 0 && j > i {
		return line[i+1 : j]
	}
	if k := strings.IndexByte(line, ':'); k >= 0 {
		return strings.TrimSpace(line[k+1:])
	}
	return ""
}

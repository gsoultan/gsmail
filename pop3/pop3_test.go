package pop3

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
)

// fakePOP3 speaks enough POP3 for USER/PASS/STAT/RETR/NOOP/QUIT.
type fakePOP3 struct {
	addr string

	mu       sync.Mutex
	messages []string
	failRetr map[int]bool
	commands []string
}

func startFakePOP3(t *testing.T, messages []string) *fakePOP3 {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &fakePOP3{addr: ln.Addr().String(), messages: messages, failRetr: map[int]bool{}}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()

	return s
}

func (s *fakePOP3) record(cmd string) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	s.mu.Unlock()
}

func (s *fakePOP3) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *fakePOP3) serve(conn net.Conn) {
	defer conn.Close()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)
	out := func(format string, a ...any) {
		fmt.Fprintf(w, format+"\r\n", a...)
		_ = w.Flush()
	}

	out("+OK fake POP3 ready")

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		cmd := strings.ToUpper(fields[0])
		s.record(cmd)

		switch cmd {
		case "CAPA":
			out("-ERR not supported")
		case "USER", "PASS":
			out("+OK")
		case "AUTH":
			out("-ERR unsupported mechanism")
		case "STAT":
			total := 0
			for _, m := range s.messages {
				total += len(m)
			}
			out("+OK %d %d", len(s.messages), total)
		case "NOOP":
			out("+OK")
		case "RETR":
			var n int
			if len(fields) > 1 {
				fmt.Sscanf(fields[1], "%d", &n)
			}
			s.mu.Lock()
			fail := s.failRetr[n]
			s.mu.Unlock()
			if fail || n < 1 || n > len(s.messages) {
				out("-ERR no such message")
				continue
			}
			body := s.messages[n-1]
			out("+OK %d octets", len(body))
			for _, l := range strings.Split(body, "\r\n") {
				if strings.HasPrefix(l, ".") {
					l = "." + l // byte-stuffing
				}
				out("%s", l)
			}
			out(".")
		case "QUIT":
			out("+OK bye")
			return
		default:
			out("+OK")
		}
	}
}

func (s *fakePOP3) receiver(t *testing.T) *Receiver {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(s.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	f := NewReceiver(host, port, "user", "pass", false)
	f.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0, InitialInterval: time.Millisecond})
	return f
}

func msg(subject, body string) string {
	return "From: sender@example.com\r\n" +
		"To: rcpt@example.com\r\n" +
		"Subject: " + subject + "\r\n" +
		"\r\n" + body
}

func TestPing(t *testing.T) {
	s := startFakePOP3(t, nil)
	if err := s.receiver(t).Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if !contains(s.seen(), "NOOP") {
		t.Errorf("Ping did not issue NOOP, saw %v", s.seen())
	}
}

// limit flows into the RETR loop bounds; a non-positive value must be refused
// before any connection is opened.
func TestReceiveRejectsNonPositiveLimit(t *testing.T) {
	s := startFakePOP3(t, []string{msg("one", "body")})
	f := s.receiver(t)

	for _, limit := range []int{0, -1, -1000} {
		emails, err := f.Receive(context.Background(), limit)
		if !errors.Is(err, gsmail.ErrInvalidLimit) {
			t.Errorf("Receive(%d) = %v, want ErrInvalidLimit", limit, err)
		}
		if emails != nil {
			t.Errorf("Receive(%d) returned %d emails alongside the error", limit, len(emails))
		}
		if gsmail.IsRetryable(err) {
			t.Errorf("Receive(%d): a bad limit is permanent", limit)
		}
	}
	if len(s.seen()) != 0 {
		t.Errorf("an invalid limit still contacted the server: %v", s.seen())
	}
}

func TestReceiveReturnsNewestFirst(t *testing.T) {
	s := startFakePOP3(t, []string{
		msg("oldest", "1"),
		msg("middle", "2"),
		msg("newest", "3"),
	})

	emails, err := s.receiver(t).Receive(context.Background(), 10)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(emails) != 3 {
		t.Fatalf("got %d emails, want 3", len(emails))
	}
	want := []string{"newest", "middle", "oldest"}
	for i, w := range want {
		if emails[i].Subject != w {
			t.Errorf("emails[%d].Subject = %q, want %q", i, emails[i].Subject, w)
		}
	}
}

func TestReceiveHonoursLimit(t *testing.T) {
	s := startFakePOP3(t, []string{
		msg("a", "1"), msg("b", "2"), msg("c", "3"), msg("d", "4"),
	})

	emails, err := s.receiver(t).Receive(context.Background(), 2)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(emails) != 2 {
		t.Fatalf("got %d emails, want 2", len(emails))
	}
	if emails[0].Subject != "d" || emails[1].Subject != "c" {
		t.Errorf("got %q,%q; want the two newest (d,c)", emails[0].Subject, emails[1].Subject)
	}
}

func TestReceiveEmptyMailbox(t *testing.T) {
	s := startFakePOP3(t, nil)
	emails, err := s.receiver(t).Receive(context.Background(), 10)
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(emails) != 0 {
		t.Errorf("got %d emails from an empty mailbox", len(emails))
	}
}

// A cancelled context must stop the RETR loop rather than draining the mailbox.
func TestReceiveRespectsContextCancellation(t *testing.T) {
	msgs := make([]string, 50)
	for i := range msgs {
		msgs[i] = msg(fmt.Sprintf("s%d", i), strings.Repeat("x", 256))
	}
	s := startFakePOP3(t, msgs)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.receiver(t).Receive(ctx, 50)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// A message the server refuses must surface as an error, not be silently
// skipped, so the caller knows the batch is incomplete.
func TestReceiveSurfacesRetrFailure(t *testing.T) {
	s := startFakePOP3(t, []string{msg("a", "1"), msg("b", "2")})
	s.mu.Lock()
	s.failRetr[2] = true // the newest message fails
	s.mu.Unlock()

	_, err := s.receiver(t).Receive(context.Background(), 10)
	if err == nil {
		t.Fatal("expected an error when RETR fails")
	}
	if !strings.Contains(err.Error(), "retr") {
		t.Errorf("error should name the failing operation, got %v", err)
	}
}

func TestSearchAndIdleAreUnsupported(t *testing.T) {
	s := startFakePOP3(t, nil)
	f := s.receiver(t)

	if _, err := f.Search(context.Background(), gsmail.SearchOptions{}, 10); err == nil {
		t.Error("Search should report that POP3 cannot search")
	}

	emails, errs := f.Idle(context.Background())
	if _, open := <-emails; open {
		t.Error("Idle should return a closed email channel")
	}
	err, open := <-errs
	if !open || err == nil {
		t.Error("Idle should report that POP3 cannot idle")
	}
}

// InsecureSkipVerify was declared and never read, so setting it did nothing.
func TestInsecureSkipVerifyIsPlumbedThrough(t *testing.T) {
	f := NewReceiver("mail.example.com", 995, "u", "p", true)
	if got := f.opt(); got.TLSSkipVerify {
		t.Error("TLSSkipVerify should default to false")
	}

	f.InsecureSkipVerify = true
	got := f.opt()
	if !got.TLSSkipVerify {
		t.Error("InsecureSkipVerify was ignored; setting it must reach the client")
	}
	if !got.TLSEnabled {
		t.Error("SSL was not plumbed through")
	}
	if got.Host != "mail.example.com" || got.Port != 995 {
		t.Errorf("host/port not plumbed through: %+v", got)
	}
}

// Receiver must satisfy the interface it claims.
var _ gsmail.Receiver = (*Receiver)(nil)

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

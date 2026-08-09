package imap

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
)

// fakeIMAP speaks just enough IMAP4rev1 for connect -> login -> select ->
// idle -> search -> fetch. It is deliberately minimal and only understands the
// command shapes this package issues.
type fakeIMAP struct {
	addr string

	mu           sync.Mutex
	conns        int
	idleStop     chan struct{} // closed when the server sees DONE
	commands     []string      // every command verb the server saw
	selected     []string      // mailboxes SELECTed
	supportsMove bool
}

func (s *fakeIMAP) record(cmd string) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	s.mu.Unlock()
}

func (s *fakeIMAP) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func (s *fakeIMAP) selectedMailboxes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.selected...)
}

func (s *fakeIMAP) sawCommand(prefix string) bool {
	for _, c := range s.seen() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func startFakeIMAP(t *testing.T) *fakeIMAP {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	s := &fakeIMAP{addr: ln.Addr().String(), idleStop: make(chan struct{})}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns++
			s.mu.Unlock()
			go s.serve(conn)
		}
	}()

	return s
}

func (s *fakeIMAP) serve(conn net.Conn) {
	defer conn.Close()

	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	// The update pusher writes concurrently with the command loop, so every
	// write goes through this lock.
	var wmu sync.Mutex
	out := func(format string, a ...any) {
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprintf(w, format+"\r\n", a...)
		_ = w.Flush()
	}
	raw := func(s string) {
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprint(w, s)
		_ = w.Flush()
	}

	out("* OK [CAPABILITY IMAP4rev1 IDLE] fake ready")

	inIdle := false
	idleTag := ""
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		if inIdle {
			if strings.EqualFold(line, "DONE") {
				inIdle = false
				// Must echo the tag go-imap used for the IDLE command;
				// anything else leaves the client waiting forever.
				out("%s OK IDLE terminated", idleTag)
			}
			continue
		}

		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		tag, cmd := parts[0], strings.ToUpper(parts[1])
		s.record(strings.ToUpper(line[len(tag)+1:]))

		switch cmd {
		case "CAPABILITY":
			s.mu.Lock()
			move := s.supportsMove
			s.mu.Unlock()
			if move {
				out("* CAPABILITY IMAP4rev1 IDLE MOVE")
			} else {
				out("* CAPABILITY IMAP4rev1 IDLE")
			}
			out("%s OK CAPABILITY done", tag)
		case "LOGIN":
			out("%s OK LOGIN done", tag)
		case "SELECT":
			if len(parts) > 2 {
				s.mu.Lock()
				s.selected = append(s.selected, strings.Trim(parts[2], `"`))
				s.mu.Unlock()
			}
			out("* 1 EXISTS")
			out("* 0 RECENT")
			out("* FLAGS (\\Seen)")
			out("* OK [UIDVALIDITY 1] UIDs valid")
			out("%s OK [READ-WRITE] SELECT done", tag)
		case "IDLE":
			inIdle = true
			idleTag = tag
			out("+ idling")
			// Push a steady stream of unilateral updates. If the client stops
			// draining them while it fetches, this write blocks and the whole
			// exchange deadlocks -- which is exactly the regression guarded here.
			go func() {
				for i := 0; i < 200; i++ {
					out("* %d EXISTS", i+1)
					time.Sleep(time.Millisecond)
				}
			}()
		case "SEARCH":
			out("* SEARCH 1")
			out("%s OK SEARCH done", tag)
		case "UID":
			sub := ""
			if len(parts) > 2 {
				sub = strings.ToUpper(strings.Fields(parts[2])[0])
			}
			switch sub {
			case "SEARCH":
				out("* SEARCH 1")
				out("%s OK UID SEARCH done", tag)
			case "FETCH":
				body := "From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
				out("* 1 FETCH (UID 101 RFC822 {%d}", len(body))
				raw(body)
				out(")")
				out("%s OK UID FETCH done", tag)
			default:
				out("%s OK UID %s done", tag, sub)
			}
		case "FETCH":
			body := "From: a@example.com\r\nTo: b@example.com\r\nSubject: hi\r\n\r\nbody\r\n"
			out("* 1 FETCH (UID 101 RFC822 {%d}", len(body))
			raw(body)
			out(")")
			out("%s OK FETCH done", tag)
		case "STORE", "EXPUNGE", "COPY", "MOVE":
			out("%s OK %s done", tag, cmd)
		case "LIST":
			out(`* LIST (\HasNoChildren) "/" "INBOX"`)
			out(`* LIST (\HasNoChildren) "/" "Archive"`)
			out(`* LIST (\HasNoChildren) "/" "Work/Reports"`)
			out("%s OK LIST done", tag)
		case "LOGOUT":
			out("* BYE logging out")
			out("%s OK LOGOUT done", tag)
			return
		case "NOOP":
			out("%s OK NOOP done", tag)
		default:
			out("%s OK done", tag)
		}
	}
}

func idleReceiver(s *fakeIMAP) *Receiver {
	host, portStr, _ := net.SplitHostPort(s.addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

	f := NewReceiver(host, port, "u", "p", false)
	f.SetRetryConfig(gsmail.RetryConfig{MaxRetries: 0, InitialInterval: time.Millisecond})
	return f
}

// Cancelling the context must tear Idle down promptly and leave nothing
// running: not the IDLE goroutine, not the update drain, not the connection.
func TestIdleShutsDownCleanlyOnCancel(t *testing.T) {
	s := startFakeIMAP(t)
	f := idleReceiver(s)

	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		emails, errs := f.Idle(ctx)

		// Let it reach the IDLE state and start receiving updates.
		time.Sleep(150 * time.Millisecond)
		cancel()

		// Comfortably longer than idleShutdownTimeout. Giving the test the
		// same budget as the implementation's own worst case makes a
		// slow-but-correct shutdown indistinguishable from a hung one, which
		// is a coin flip on a loaded runner rather than a test.
		deadline := time.After(4 * idleShutdownTimeout)
		for emails != nil || errs != nil {
			select {
			case _, ok := <-emails:
				if !ok {
					emails = nil
				}
			case _, ok := <-errs:
				if !ok {
					errs = nil
				}
			case <-deadline:
				t.Fatalf("Idle did not shut down within %s (iteration %d)", 4*idleShutdownTimeout, i)
			}
		}
	}

	// Give the runtime a moment to reap.
	for i := 0; i < 100; i++ {
		if runtime.NumGoroutine() <= before+2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	t.Errorf("goroutine leak after Idle shutdown: %d -> %d\n%s", before, runtime.NumGoroutine(), buf[:n])
}

// A burst of unilateral updates arriving while the client fetches must not
// wedge the connection reader.
func TestIdleSurvivesUpdateFlood(t *testing.T) {
	s := startFakeIMAP(t)
	f := idleReceiver(s)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	emails, errs := f.Idle(ctx)

	received := 0
	timeout := time.After(6 * time.Second)
	for {
		select {
		case _, ok := <-emails:
			if !ok {
				t.Logf("delivered %d emails before shutdown", received)
				return
			}
			received++
		case err, ok := <-errs:
			if ok && err != nil {
				t.Logf("idle reported: %v", err)
			}
			if !ok {
				errs = nil
			}
		case <-timeout:
			t.Fatal("Idle wedged: no shutdown after the update flood (reader deadlock)")
		}
	}
}

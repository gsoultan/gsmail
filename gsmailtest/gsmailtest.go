// Package gsmailtest provides test doubles for code that sends or receives
// mail with gsmail.
//
// It is aimed at application authors. providertest, by contrast, is for people
// implementing a new provider: it asserts that a Sender obeys the library's
// contract. This package is the other side -- it lets you assert that *your*
// code sends what you expected, without a network, a provider account, or a
// hand-rolled fake in every project.
//
//	sender := gsmailtest.NewSender()
//	svc := NewSignupService(sender)
//	svc.Welcome(ctx, "alice@example.com")
//
//	msg := sender.MustLast(t)
//	if msg.Subject != "Welcome" { ... }
//
// Every type here is safe for concurrent use, because the code under test may
// well send from a worker pool.
package gsmailtest

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gsoultan/gsmail"
)

// TB is the subset of testing.TB these helpers need. Taking an interface keeps
// this package free of a dependency on the testing package's concrete types
// and lets the helpers be used from benchmarks and fuzz targets too.
type TB interface {
	Helper()
	Fatalf(format string, args ...any)
}

// Sender records the messages sent through it instead of delivering them.
//
// The zero value is ready to use.
type Sender struct {
	gsmail.BaseProvider

	mu       sync.Mutex
	sent     []gsmail.Email
	failWith error
	failNext []error
	pingErr  error
	onSend   func(gsmail.Email)
}

// NewSender returns a Sender that accepts everything.
func NewSender() *Sender { return &Sender{} }

// Send records the message, or returns a configured failure.
func (s *Sender) Send(ctx context.Context, email gsmail.Email) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if len(s.failNext) > 0 {
		err := s.failNext[0]
		s.failNext = s.failNext[1:]
		s.mu.Unlock()
		if err != nil {
			return err
		}
		s.mu.Lock()
	}
	if s.failWith != nil {
		err := s.failWith
		s.mu.Unlock()
		return err
	}

	// Copy the recipient slices. The caller may reuse or mutate the Email
	// after Send returns, and a recorded message that changes afterwards makes
	// for a baffling test failure.
	recorded := email
	recorded.To = append([]string(nil), email.To...)
	recorded.Cc = append([]string(nil), email.Cc...)
	recorded.Bcc = append([]string(nil), email.Bcc...)
	recorded.Body = append([]byte(nil), email.Body...)
	recorded.HTMLBody = append([]byte(nil), email.HTMLBody...)
	if email.Headers != nil {
		recorded.Headers = make(map[string]string, len(email.Headers))
		for k, v := range email.Headers {
			recorded.Headers[k] = v
		}
	}

	s.sent = append(s.sent, recorded)
	hook := s.onSend
	s.mu.Unlock()

	if hook != nil {
		hook(recorded)
	}
	return nil
}

// Ping reports the configured ping result.
func (s *Sender) Ping(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pingErr
}

// Sent returns every message recorded so far.
func (s *Sender) Sent() []gsmail.Email {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]gsmail.Email(nil), s.sent...)
}

// Count reports how many messages were sent.
func (s *Sender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// Last returns the most recent message.
func (s *Sender) Last() (gsmail.Email, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return gsmail.Email{}, false
	}
	return s.sent[len(s.sent)-1], true
}

// MustLast returns the most recent message, failing the test if none was sent.
func (s *Sender) MustLast(t TB) gsmail.Email {
	t.Helper()
	email, ok := s.Last()
	if !ok {
		t.Fatalf("gsmailtest: expected a message to have been sent, but none was")
	}
	return email
}

// MustCount fails the test unless exactly n messages were sent.
func (s *Sender) MustCount(t TB, n int) {
	t.Helper()
	if got := s.Count(); got != n {
		t.Fatalf("gsmailtest: %d message(s) sent, want %d\n%s", got, n, s.summary())
	}
}

// To returns every message addressed to the given recipient, in To, Cc or Bcc.
// The comparison ignores case and display names.
func (s *Sender) To(address string) []gsmail.Email {
	want := gsmail.NormalizeAddress(address)

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []gsmail.Email
	for _, e := range s.sent {
		for _, list := range [][]string{e.To, e.Cc, e.Bcc} {
			if containsAddress(list, want) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// MustTo returns the single message addressed to the given recipient, failing
// the test if there is not exactly one.
func (s *Sender) MustTo(t TB, address string) gsmail.Email {
	t.Helper()
	matches := s.To(address)
	switch len(matches) {
	case 1:
		return matches[0]
	case 0:
		t.Fatalf("gsmailtest: no message was sent to %q\n%s", address, s.summary())
	default:
		t.Fatalf("gsmailtest: %d messages were sent to %q, want 1", len(matches), address)
	}
	return gsmail.Email{}
}

// Reset discards recorded messages and clears any configured failures.
func (s *Sender) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = nil
	s.failWith = nil
	s.failNext = nil
	s.pingErr = nil
}

// FailWith makes every subsequent Send return err. Pass nil to stop failing.
func (s *Sender) FailWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWith = err
}

// FailNextWith queues errors for the next sends, one per call, so a retry or
// failover path can be driven through a specific sequence. A nil entry lets
// that send succeed.
func (s *Sender) FailNextWith(errs ...error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = append(s.failNext, errs...)
}

// FailPingWith makes Ping return err.
func (s *Sender) FailPingWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pingErr = err
}

// OnSend registers a callback invoked after each recorded message. Use it to
// drive a workflow that reacts to mail being sent.
func (s *Sender) OnSend(fn func(gsmail.Email)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSend = fn
}

// summary renders the recorded messages for a failure message. A test that
// says "no message sent to bob@example.com" is far easier to fix when it also
// shows what *was* sent.
func (s *Sender) summary() string {
	if len(s.sent) == 0 {
		return "  (no messages were sent)"
	}
	var b strings.Builder
	b.WriteString("  messages sent:\n")
	for i, e := range s.sent {
		fmt.Fprintf(&b, "    [%d] to=%v cc=%v bcc=%v subject=%q\n",
			i, e.To, e.Cc, e.Bcc, e.Subject)
	}
	return strings.TrimRight(b.String(), "\n")
}

func containsAddress(list []string, want string) bool {
	for _, a := range list {
		if gsmail.NormalizeAddress(a) == want {
			return true
		}
	}
	return false
}

// --- Receiver ------------------------------------------------------------

// Receiver serves a fixed set of messages instead of connecting to a server.
//
// The zero value returns nothing.
type Receiver struct {
	gsmail.BaseProvider

	mu       sync.Mutex
	inbox    []gsmail.Email
	err      error
	idleChan chan gsmail.Email
}

// NewReceiver returns a Receiver serving the given messages, newest first.
func NewReceiver(emails ...gsmail.Email) *Receiver {
	return &Receiver{inbox: emails}
}

// Add appends messages to the inbox.
func (r *Receiver) Add(emails ...gsmail.Email) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inbox = append(r.inbox, emails...)
}

// FailWith makes every operation return err.
func (r *Receiver) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// Receive returns up to limit messages.
func (r *Receiver) Receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
	if err := gsmail.CheckLimit(limit); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return nil, r.err
	}
	if limit > len(r.inbox) {
		limit = len(r.inbox)
	}
	return append([]gsmail.Email(nil), r.inbox[:limit]...), nil
}

// Search filters the inbox on From, Subject and Unseen-agnostic criteria.
// It is a convenience, not a faithful IMAP SEARCH.
func (r *Receiver) Search(ctx context.Context, opts gsmail.SearchOptions, limit int) ([]gsmail.Email, error) {
	all, err := r.Receive(ctx, max(limit, 1))
	if err != nil {
		return nil, err
	}

	var out []gsmail.Email
	for _, e := range all {
		if opts.From != "" && !strings.Contains(strings.ToLower(e.From), strings.ToLower(opts.From)) {
			continue
		}
		if opts.Subject != "" && !strings.Contains(strings.ToLower(e.Subject), strings.ToLower(opts.Subject)) {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Idle returns a channel fed by Push. The channels close when ctx is done.
func (r *Receiver) Idle(ctx context.Context) (<-chan gsmail.Email, <-chan error) {
	emails := make(chan gsmail.Email, 16)
	errs := make(chan error, 1)

	r.mu.Lock()
	r.idleChan = emails
	failure := r.err
	r.mu.Unlock()

	if failure != nil {
		errs <- failure
		close(emails)
		close(errs)
		return emails, errs
	}

	go func() {
		<-ctx.Done()
		r.mu.Lock()
		r.idleChan = nil
		r.mu.Unlock()
		close(emails)
		close(errs)
	}()

	return emails, errs
}

// Push delivers a message to an active Idle listener. It reports whether there
// was one; a push with nobody listening is dropped rather than blocking.
func (r *Receiver) Push(email gsmail.Email) bool {
	r.mu.Lock()
	ch := r.idleChan
	r.mu.Unlock()

	if ch == nil {
		return false
	}
	select {
	case ch <- email:
		return true
	default:
		return false
	}
}

// Ping reports the configured failure, if any.
func (r *Receiver) Ping(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// ErrNotDelivered is a convenient stand-in for a provider failure in tests.
var ErrNotDelivered = errors.New("gsmailtest: simulated delivery failure")

var (
	_ gsmail.Sender   = (*Sender)(nil)
	_ gsmail.Receiver = (*Receiver)(nil)
)

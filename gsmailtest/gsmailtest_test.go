package gsmailtest_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/gsmailtest"
)

// signupService stands in for the application code this package exists to
// help test.
type signupService struct{ mail gsmail.Sender }

func (s *signupService) Welcome(ctx context.Context, address, name string) error {
	e := gsmail.Email{
		From:    "no-reply@example.com",
		To:      []string{address},
		Subject: "Welcome, " + name,
		Body:    []byte("Thanks for signing up."),
	}
	return gsmail.Send(ctx, s.mail, e)
}

func TestSenderRecordsWhatTheApplicationSent(t *testing.T) {
	sender := gsmailtest.NewSender()
	svc := &signupService{mail: sender}

	if err := svc.Welcome(context.Background(), "Alice <Alice@Example.COM>", "Alice"); err != nil {
		t.Fatalf("Welcome: %v", err)
	}

	sender.MustCount(t, 1)
	msg := sender.MustLast(t)
	if msg.Subject != "Welcome, Alice" {
		t.Errorf("Subject = %q", msg.Subject)
	}

	// Assertions must not depend on the spelling the caller happened to use.
	got := sender.MustTo(t, "alice@example.com")
	if got.Subject != msg.Subject {
		t.Error("MustTo returned a different message")
	}
}

// A recorded message must not change when the caller reuses the Email.
func TestSenderSnapshotsTheMessage(t *testing.T) {
	sender := gsmailtest.NewSender()

	e := gsmail.Email{
		From: "a@example.com",
		To:   []string{"b@example.com"},
		Body: []byte("original"),
	}
	e.SetHeader("X-Tag", "first")

	if err := sender.Send(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	// Mutate everything the caller could plausibly reuse.
	e.To[0] = "hijacked@example.com"
	e.Body[0] = 'X'
	e.Headers["X-Tag"] = "second"

	got := sender.MustLast(t)
	if got.To[0] != "b@example.com" {
		t.Errorf("recorded To changed to %q after the caller mutated theirs", got.To[0])
	}
	if string(got.Body) != "original" {
		t.Errorf("recorded Body changed to %q", got.Body)
	}
	if got.Headers["X-Tag"] != "first" {
		t.Errorf("recorded header changed to %q", got.Headers["X-Tag"])
	}
}

func TestSenderFailure(t *testing.T) {
	sender := gsmailtest.NewSender()
	sender.FailWith(gsmailtest.ErrNotDelivered)

	err := (&signupService{mail: sender}).Welcome(context.Background(), "a@example.com", "A")
	if !errors.Is(err, gsmailtest.ErrNotDelivered) {
		t.Fatalf("got %v, want ErrNotDelivered", err)
	}
	if sender.Count() != 0 {
		t.Error("a failed send should not be recorded")
	}

	sender.FailWith(nil)
	if err := (&signupService{mail: sender}).Welcome(context.Background(), "a@example.com", "A"); err != nil {
		t.Fatalf("clearing the failure did not work: %v", err)
	}
}

// Driving a retry or failover path needs a specific sequence of outcomes.
func TestFailNextWithScriptsASequence(t *testing.T) {
	sender := gsmailtest.NewSender()
	boom := errors.New("transient")
	sender.FailNextWith(boom, boom, nil)

	var results []error
	for i := 0; i < 3; i++ {
		results = append(results, sender.Send(context.Background(), gsmail.Email{From: "a@example.com"}))
	}

	if !errors.Is(results[0], boom) || !errors.Is(results[1], boom) {
		t.Errorf("first two sends should fail: %v", results)
	}
	if results[2] != nil {
		t.Errorf("third send should succeed, got %v", results[2])
	}
	if sender.Count() != 1 {
		t.Errorf("recorded %d messages, want 1", sender.Count())
	}
}

func TestSenderWorksWithInterceptors(t *testing.T) {
	sender := gsmailtest.NewSender()

	var logged int
	wrapped := gsmail.WrapSender(sender, func(ctx context.Context, e gsmail.Email, next func(context.Context, gsmail.Email) error) error {
		logged++
		return next(ctx, e)
	})

	if err := wrapped.Send(context.Background(), gsmail.Email{From: "a@example.com", To: []string{"b@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if logged != 1 || sender.Count() != 1 {
		t.Errorf("interceptor ran %d times, sender recorded %d", logged, sender.Count())
	}
}

func TestSenderRespectsContext(t *testing.T) {
	sender := gsmailtest.NewSender()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := sender.Send(ctx, gsmail.Email{From: "a@example.com"}); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

// The code under test may send from a worker pool.
func TestSenderIsConcurrencySafe(t *testing.T) {
	sender := gsmailtest.NewSender()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = sender.Send(context.Background(), gsmail.Email{
					From: "a@example.com",
					To:   []string{fmt.Sprintf("u%d@example.com", i)},
				})
				_ = sender.Count()
				_, _ = sender.Last()
			}
		}(i)
	}
	wg.Wait()

	if got := sender.Count(); got != 800 {
		t.Errorf("recorded %d messages, want 800", got)
	}
}

// A failure message that only says "no message sent" leaves you guessing.
func TestFailureMessagesShowWhatWasSent(t *testing.T) {
	sender := gsmailtest.NewSender()
	_ = sender.Send(context.Background(), gsmail.Email{
		From: "a@example.com", To: []string{"actual@example.com"}, Subject: "Hello",
	})

	rec := &recordingTB{}
	sender.MustTo(rec, "expected@example.com")

	if !rec.failed {
		t.Fatal("MustTo should have failed")
	}
	if !strings.Contains(rec.message, "actual@example.com") || !strings.Contains(rec.message, "Hello") {
		t.Errorf("the failure should show what was sent, got:\n%s", rec.message)
	}
}

func TestMustLastFailsWhenNothingWasSent(t *testing.T) {
	rec := &recordingTB{}
	gsmailtest.NewSender().MustLast(rec)
	if !rec.failed {
		t.Error("MustLast should fail when no message was sent")
	}
}

func TestOnSendHook(t *testing.T) {
	sender := gsmailtest.NewSender()

	var seen []string
	sender.OnSend(func(e gsmail.Email) { seen = append(seen, e.Subject) })

	_ = sender.Send(context.Background(), gsmail.Email{From: "a@example.com", Subject: "one"})
	_ = sender.Send(context.Background(), gsmail.Email{From: "a@example.com", Subject: "two"})

	if len(seen) != 2 || seen[0] != "one" || seen[1] != "two" {
		t.Errorf("OnSend saw %v", seen)
	}
}

func TestReset(t *testing.T) {
	sender := gsmailtest.NewSender()
	_ = sender.Send(context.Background(), gsmail.Email{From: "a@example.com"})
	sender.FailWith(gsmailtest.ErrNotDelivered)

	sender.Reset()

	if sender.Count() != 0 {
		t.Error("Reset should discard recorded messages")
	}
	if err := sender.Send(context.Background(), gsmail.Email{From: "a@example.com"}); err != nil {
		t.Errorf("Reset should clear the configured failure, got %v", err)
	}
}

// --- Receiver ------------------------------------------------------------

func TestReceiver(t *testing.T) {
	r := gsmailtest.NewReceiver(
		gsmail.Email{From: "a@example.com", Subject: "first"},
		gsmail.Email{From: "b@example.com", Subject: "second"},
	)

	got, err := r.Receive(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d messages, want 2", len(got))
	}

	limited, err := r.Receive(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("limit was ignored: got %d", len(limited))
	}
}

// The double must enforce the same contract as the real receivers, or code
// that passes its tests fails in production.
func TestReceiverRejectsNonPositiveLimit(t *testing.T) {
	r := gsmailtest.NewReceiver(gsmail.Email{Subject: "x"})
	if _, err := r.Receive(context.Background(), 0); !errors.Is(err, gsmail.ErrInvalidLimit) {
		t.Errorf("got %v, want ErrInvalidLimit", err)
	}
}

func TestReceiverSearch(t *testing.T) {
	r := gsmailtest.NewReceiver(
		gsmail.Email{From: "alice@example.com", Subject: "Invoice March"},
		gsmail.Email{From: "bob@example.com", Subject: "Lunch"},
		gsmail.Email{From: "alice@example.com", Subject: "Invoice April"},
	)

	got, err := r.Search(context.Background(), gsmail.SearchOptions{From: "alice", Subject: "invoice"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("got %d matches, want 2", len(got))
	}
}

func TestReceiverIdle(t *testing.T) {
	r := gsmailtest.NewReceiver()

	ctx, cancel := context.WithCancel(context.Background())
	emails, errs := r.Idle(ctx)

	if !r.Push(gsmail.Email{Subject: "pushed"}) {
		t.Fatal("Push reported no listener")
	}
	select {
	case got := <-emails:
		if got.Subject != "pushed" {
			t.Errorf("Subject = %q", got.Subject)
		}
	default:
		t.Fatal("the pushed message did not arrive")
	}

	cancel()
	for range emails {
	}
	for range errs {
	}
}

func TestReceiverFailure(t *testing.T) {
	r := gsmailtest.NewReceiver(gsmail.Email{Subject: "x"})
	r.FailWith(gsmailtest.ErrNotDelivered)

	if _, err := r.Receive(context.Background(), 5); !errors.Is(err, gsmailtest.ErrNotDelivered) {
		t.Errorf("Receive: got %v", err)
	}
	if err := r.Ping(context.Background()); !errors.Is(err, gsmailtest.ErrNotDelivered) {
		t.Errorf("Ping: got %v", err)
	}
}

// recordingTB captures a failure instead of aborting, so the Must* helpers can
// be tested.
type recordingTB struct {
	failed  bool
	message string
}

func (r *recordingTB) Helper() {}
func (r *recordingTB) Fatalf(format string, args ...any) {
	r.failed = true
	r.message = fmt.Sprintf(format, args...)
}

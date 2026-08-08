package gsmail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingSender struct {
	BaseProvider
	mu   sync.Mutex
	sent []Email
	err  error
}

func (s *recordingSender) Send(_ context.Context, e Email) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, e)
	return nil
}
func (s *recordingSender) Ping(context.Context) error { return nil }

func (s *recordingSender) last() (Email, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return Email{}, false
	}
	return s.sent[len(s.sent)-1], true
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func TestNormalizeAddress(t *testing.T) {
	tests := map[string]string{
		"alice@example.com":               "alice@example.com",
		"Alice@Example.COM":               "alice@example.com",
		"  alice@example.com  ":           "alice@example.com",
		"Alice Smith <Alice@EXAMPLE.com>": "alice@example.com",
		`"Doe, John" <J.Doe@Example.com>`: "j.doe@example.com",
		"":                                "",
	}
	for in, want := range tests {
		if got := NormalizeAddress(in); got != want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", in, got, want)
		}
	}
}

// A bounce recorded under one spelling must suppress every other spelling of
// the same mailbox, or the list silently fails to do its job.
func TestSuppressionIsCaseAndFormatInsensitive(t *testing.T) {
	list := NewMemorySuppressionList()
	list.Add("Alice@Example.COM", ReasonHardBounce)

	for _, spelling := range []string{
		"alice@example.com",
		"ALICE@EXAMPLE.COM",
		"Alice Smith <alice@example.com>",
		"  alice@example.com ",
	} {
		got, err := list.Suppressed(context.Background(), spelling)
		if err != nil {
			t.Fatal(err)
		}
		if !got {
			t.Errorf("Suppressed(%q) = false; the same mailbox must match", spelling)
		}
	}
	if list.Len() != 1 {
		t.Errorf("Len = %d, want 1: spellings must not create separate entries", list.Len())
	}
}

func TestSuppressionInterceptorFiltersRecipients(t *testing.T) {
	list := NewMemorySuppressionList()
	list.Add("dead@example.com", ReasonHardBounce)
	list.Add("complained@example.com", ReasonComplaint)

	inner := &recordingSender{}
	sender := WrapSender(inner, SuppressionInterceptor(list))

	err := sender.Send(context.Background(), Email{
		From: "a@example.com",
		To:   []string{"live@example.com", "dead@example.com"},
		Cc:   []string{"complained@example.com"},
		Bcc:  []string{"other@example.com"},
		Body: []byte("hi"),
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	got, ok := inner.last()
	if !ok {
		t.Fatal("nothing reached the sender")
	}
	if len(got.To) != 1 || got.To[0] != "live@example.com" {
		t.Errorf("To = %v, want [live@example.com]", got.To)
	}
	if len(got.Cc) != 0 {
		t.Errorf("Cc = %v, want empty", got.Cc)
	}
	if len(got.Bcc) != 1 || got.Bcc[0] != "other@example.com" {
		t.Errorf("Bcc = %v, want [other@example.com]", got.Bcc)
	}
}

// Nothing may be transmitted when there is nobody left to send to.
func TestSuppressionInterceptorBlocksWhenAllSuppressed(t *testing.T) {
	list := NewMemorySuppressionList()
	list.Add("a@example.com", ReasonHardBounce)
	list.Add("b@example.com", ReasonComplaint)

	inner := &recordingSender{}
	sender := WrapSender(inner, SuppressionInterceptor(list))

	err := sender.Send(context.Background(), Email{
		From: "s@example.com",
		To:   []string{"a@example.com"},
		Cc:   []string{"B@Example.com"},
		Body: []byte("hi"),
	})
	if !errors.Is(err, ErrAllRecipientsSuppressed) {
		t.Fatalf("got %v, want ErrAllRecipientsSuppressed", err)
	}
	if IsRetryable(err) {
		t.Error("a fully suppressed message is permanent; retrying changes nothing")
	}
	if inner.count() != 0 {
		t.Error("the message was transmitted despite every recipient being suppressed")
	}
}

// The caller's Email must not be mutated: they may reuse it, and silently
// emptying their slice would be a nasty surprise.
func TestSuppressionInterceptorDoesNotMutateCallerEmail(t *testing.T) {
	list := NewMemorySuppressionList()
	list.Add("dead@example.com", ReasonHardBounce)

	original := Email{
		From: "a@example.com",
		To:   []string{"live@example.com", "dead@example.com"},
		Body: []byte("hi"),
	}

	sender := WrapSender(&recordingSender{}, SuppressionInterceptor(list))
	if err := sender.Send(context.Background(), original); err != nil {
		t.Fatal(err)
	}

	if len(original.To) != 2 {
		t.Errorf("the caller's To slice was modified: %v", original.To)
	}
}

// A suppression list that cannot be reached is not evidence that an address is
// safe to mail.
func TestSuppressionFailsClosedByDefault(t *testing.T) {
	boom := errors.New("datastore unavailable")
	broken := SuppressorFunc(func(context.Context, string) (bool, error) { return false, boom })

	inner := &recordingSender{}
	sender := WrapSender(inner, SuppressionInterceptor(broken))

	err := sender.Send(context.Background(), Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the lookup error", err)
	}
	if inner.count() != 0 {
		t.Error("the message was sent despite the suppression check failing")
	}
}

func TestSuppressionCanFailOpen(t *testing.T) {
	broken := SuppressorFunc(func(context.Context, string) (bool, error) {
		return false, errors.New("datastore unavailable")
	})

	inner := &recordingSender{}
	sender := WrapSender(inner, SuppressionInterceptorWith(broken, SuppressionOptions{IgnoreErrors: true}))

	if err := sender.Send(context.Background(), Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if inner.count() != 1 {
		t.Error("IgnoreErrors should let the message through")
	}
}

func TestSuppressionReportsDrops(t *testing.T) {
	list := NewMemorySuppressionList()
	list.Add("dead@example.com", ReasonHardBounce)

	var dropped []string
	var mu sync.Mutex
	sender := WrapSender(&recordingSender{}, SuppressionInterceptorWith(list, SuppressionOptions{
		OnSuppressed: func(_ context.Context, addr string) {
			mu.Lock()
			defer mu.Unlock()
			dropped = append(dropped, addr)
		},
	}))

	if err := sender.Send(context.Background(), Email{
		From: "a@example.com",
		To:   []string{"live@example.com", "dead@example.com"},
		Body: []byte("hi"),
	}); err != nil {
		t.Fatal(err)
	}

	if len(dropped) != 1 || dropped[0] != "dead@example.com" {
		t.Errorf("OnSuppressed saw %v, want [dead@example.com]", dropped)
	}
}

func TestNilSuppressorIsAPassThrough(t *testing.T) {
	inner := &recordingSender{}
	sender := WrapSender(inner, SuppressionInterceptor(nil))

	if err := sender.Send(context.Background(), Email{
		From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi"),
	}); err != nil {
		t.Fatal(err)
	}
	if inner.count() != 1 {
		t.Error("a nil Suppressor should not block anything")
	}
}

// A soft bounce is transient. Suppressing on one permanently cuts off a live
// recipient whose mailbox happened to be full.
func TestOnlyHardBouncesSuppress(t *testing.T) {
	list := NewMemorySuppressionList()

	if list.AddBounce(&Bounce{Type: BounceSoft, EmailAddress: "full@example.com"}) {
		t.Error("a soft bounce must not suppress")
	}
	if !list.AddBounce(&Bounce{Type: BounceHard, EmailAddress: "gone@example.com", Reason: "550 unknown"}) {
		t.Error("a hard bounce must suppress")
	}
	if list.AddBounce(nil) {
		t.Error("a nil bounce must not suppress")
	}
	if list.AddBounce(&Bounce{Type: BounceHard}) {
		t.Error("a bounce with no address must not suppress")
	}

	if list.Len() != 1 {
		t.Fatalf("Len = %d, want 1", list.Len())
	}
	entry, ok := list.Entry("gone@example.com")
	if !ok {
		t.Fatal("the hard bounce was not recorded")
	}
	if entry.Reason != ReasonHardBounce || entry.Detail != "550 unknown" {
		t.Errorf("entry = %+v; the provider's reason should be kept", entry)
	}
}

func TestComplaintsSuppress(t *testing.T) {
	list := NewMemorySuppressionList()
	if !list.AddComplaint(&Complaint{EmailAddress: "angry@example.com", Type: "abuse"}) {
		t.Fatal("a complaint must suppress")
	}
	entry, _ := list.Entry("angry@example.com")
	if entry.Reason != ReasonComplaint {
		t.Errorf("Reason = %q, want %q", entry.Reason, ReasonComplaint)
	}
}

func TestRemoveUnsuppresses(t *testing.T) {
	list := NewMemorySuppressionList()
	list.Add("a@example.com", ReasonUnsubscribe)
	list.Remove("A@Example.com")

	if got, _ := list.Suppressed(context.Background(), "a@example.com"); got {
		t.Error("Remove should accept any spelling of the address")
	}
}

// The whole point: a webhook payload goes in, and the next send to that
// address is blocked.
func TestBounceWebhookClosesTheLoop(t *testing.T) {
	const payload = `{"notificationType":"Bounce","bounce":{"bounceType":"Permanent",` +
		`"bouncedRecipients":[{"emailAddress":"gone@example.com","status":"5.1.1",` +
		`"diagnosticCode":"smtp; 550 user unknown"}],"timestamp":"2024-01-01T00:00:00Z"},` +
		`"mail":{"messageId":"m-1"}}`

	event, err := ParseSESWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("ParseSESWebhook: %v", err)
	}

	list := NewMemorySuppressionList()
	if !list.Record(event) {
		t.Fatal("Record did not suppress the bounced address")
	}

	inner := &recordingSender{}
	sender := WrapSender(inner, SuppressionInterceptor(list))

	err = sender.Send(context.Background(), Email{
		From: "s@example.com", To: []string{"gone@example.com"}, Body: []byte("hi"),
	})
	if !errors.Is(err, ErrAllRecipientsSuppressed) {
		t.Fatalf("got %v, want the send to be blocked", err)
	}
	if inner.count() != 0 {
		t.Error("mail was sent to an address that had already hard-bounced")
	}
}

func TestRecordHandlesWebhookBatches(t *testing.T) {
	const batch = `[{"email":"b@example.com","event":"bounce","timestamp":1700000000},
	                {"email":"c@example.com","event":"spamreport","timestamp":1700000001},
	                {"email":"d@example.com","event":"open","timestamp":1700000002}]`

	events, err := ParseSendGridWebhook([]byte(batch))
	if err != nil {
		t.Fatal(err)
	}

	list := NewMemorySuppressionList()
	for _, e := range events {
		list.Record(e)
	}
	if list.Len() != 2 {
		t.Errorf("Len = %d, want 2 (the bounce and the complaint, not the open)", list.Len())
	}
}

func TestSuppressionListIsConcurrencySafe(t *testing.T) {
	list := NewMemorySuppressionList()
	var wg sync.WaitGroup

	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				addr := "user" + strings.Repeat("x", i%3) + "@example.com"
				list.Add(addr, ReasonManual)
				_, _ = list.Suppressed(context.Background(), addr)
				_, _ = list.Entry(addr)
				_ = list.Len()
				if j%20 == 0 {
					list.Remove(addr)
					_ = list.Entries()
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestSuppressionEntryKeepsTimestamp(t *testing.T) {
	ts := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	list := NewMemorySuppressionList()
	list.AddBounce(&Bounce{Type: BounceHard, EmailAddress: "a@example.com", Timestamp: ts})

	entry, ok := list.Entry("a@example.com")
	if !ok {
		t.Fatal("not recorded")
	}
	if !entry.At.Equal(ts) {
		t.Errorf("At = %v, want the bounce timestamp %v", entry.At, ts)
	}
}

var _ Suppressor = (*MemorySuppressionList)(nil)

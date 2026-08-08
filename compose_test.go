package gsmail

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- unsubscribe ---------------------------------------------------------

func TestSetOneClickUnsubscribe(t *testing.T) {
	var e Email
	if err := e.SetOneClickUnsubscribe("https://example.com/u?t=abc"); err != nil {
		t.Fatalf("SetOneClickUnsubscribe: %v", err)
	}

	if got := e.Headers["List-Unsubscribe"]; got != "<https://example.com/u?t=abc>" {
		t.Errorf("List-Unsubscribe = %q", got)
	}
	if got := e.Headers["List-Unsubscribe-Post"]; got != ListUnsubscribePostValue {
		t.Errorf("List-Unsubscribe-Post = %q, want %q", got, ListUnsubscribePostValue)
	}
	if !e.HasOneClickUnsubscribe() {
		t.Error("HasOneClickUnsubscribe should be true")
	}
}

func TestSetOneClickUnsubscribeAcceptsMailtoFallback(t *testing.T) {
	var e Email
	if err := e.SetOneClickUnsubscribe("https://example.com/u", "mailto:unsub@example.com"); err != nil {
		t.Fatalf("SetOneClickUnsubscribe: %v", err)
	}
	want := "<https://example.com/u>, <mailto:unsub@example.com>"
	if got := e.Headers["List-Unsubscribe"]; got != want {
		t.Errorf("List-Unsubscribe = %q, want %q", got, want)
	}
}

// One-click works by the provider POSTing to an https endpoint. A mailto:
// target alone cannot satisfy it, and emitting the pair anyway would claim
// compliance the message does not have.
func TestOneClickRequiresHTTPS(t *testing.T) {
	var e Email
	err := e.SetOneClickUnsubscribe("mailto:unsub@example.com")
	if !errors.Is(err, ErrNoHTTPSUnsubscribe) {
		t.Fatalf("got %v, want ErrNoHTTPSUnsubscribe", err)
	}
	if len(e.Headers) != 0 {
		t.Errorf("no headers should be set on failure, got %v", e.Headers)
	}
}

// An unsubscribe URL carries a token identifying the recipient. Sending it in
// clear is a disclosure.
func TestUnsubscribeRejectsUnsafeSchemes(t *testing.T) {
	for _, target := range []string{
		"http://example.com/u",
		"javascript:alert(1)",
		"ftp://example.com/u",
		"data:text/html,x",
	} {
		var e Email
		if err := e.SetListUnsubscribe(target); !errors.Is(err, ErrUnsafeUnsubscribeScheme) {
			t.Errorf("SetListUnsubscribe(%q) = %v, want ErrUnsafeUnsubscribeScheme", target, err)
		}
	}
}

func TestUnsubscribeRejectsHeaderInjection(t *testing.T) {
	var e Email
	err := e.SetListUnsubscribe("https://example.com/u\r\nBcc: attacker@evil.test")
	if err == nil {
		t.Fatal("expected an error for a target containing CRLF")
	}
	if len(e.Headers) != 0 {
		t.Error("no header should be set when the target is rejected")
	}
}

func TestUnsubscribeRequiresATarget(t *testing.T) {
	var e Email
	if err := e.SetListUnsubscribe(); !errors.Is(err, ErrNoUnsubscribeTarget) {
		t.Errorf("got %v, want ErrNoUnsubscribeTarget", err)
	}
	if err := e.SetListUnsubscribe("   "); !errors.Is(err, ErrNoUnsubscribeTarget) {
		t.Errorf("blank target: got %v, want ErrNoUnsubscribeTarget", err)
	}
}

func TestUnsubscribeAcceptsBracketedTargets(t *testing.T) {
	var e Email
	if err := e.SetListUnsubscribe("<https://example.com/u>"); err != nil {
		t.Fatal(err)
	}
	if got := e.Headers["List-Unsubscribe"]; got != "<https://example.com/u>" {
		t.Errorf("brackets were doubled: %q", got)
	}
}

// Either header alone does not satisfy RFC 8058.
func TestHasOneClickUnsubscribeRequiresBothHeaders(t *testing.T) {
	var onlyList Email
	onlyList.SetHeader("List-Unsubscribe", "<https://example.com/u>")
	if onlyList.HasOneClickUnsubscribe() {
		t.Error("List-Unsubscribe alone is not one-click compliant")
	}

	var onlyPost Email
	onlyPost.SetHeader("List-Unsubscribe-Post", ListUnsubscribePostValue)
	if onlyPost.HasOneClickUnsubscribe() {
		t.Error("List-Unsubscribe-Post alone is not one-click compliant")
	}

	var wrongValue Email
	wrongValue.SetHeader("List-Unsubscribe", "<https://example.com/u>")
	wrongValue.SetHeader("List-Unsubscribe-Post", "List-Unsubscribe=Two-Click")
	if wrongValue.HasOneClickUnsubscribe() {
		t.Error("only the exact RFC 8058 value counts")
	}
}

func TestRequireOneClickUnsubscribeInterceptor(t *testing.T) {
	inner := &scriptedSender{}
	sender := WrapSender(inner, RequireOneClickUnsubscribe())

	bare := Email{From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi")}
	err := sender.Send(context.Background(), bare)
	if err == nil {
		t.Fatal("expected bulk mail without the header pair to be refused")
	}
	if IsRetryable(err) {
		t.Error("a missing unsubscribe header is permanent")
	}
	if int(inner.calls.Load()) != 0 {
		t.Error("the message was sent despite failing the check")
	}

	compliant := bare
	if err := compliant.SetOneClickUnsubscribe("https://example.com/u"); err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), compliant); err != nil {
		t.Fatalf("a compliant message was refused: %v", err)
	}
	if int(inner.calls.Load()) != 1 {
		t.Error("the compliant message did not reach the sender")
	}
}

// The headers must survive rendering, not just sit on the struct.
func TestUnsubscribeHeadersReachTheWire(t *testing.T) {
	e := Email{From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hi")}
	if err := e.SetOneClickUnsubscribe("https://example.com/u?t=abc"); err != nil {
		t.Fatal(err)
	}

	raw, err := RenderMessage(e)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(raw)
	if !strings.Contains(msg, "List-Unsubscribe: <https://example.com/u?t=abc>") {
		t.Errorf("List-Unsubscribe missing:\n%s", msg)
	}
	if !strings.Contains(msg, "List-Unsubscribe-Post: "+ListUnsubscribePostValue) {
		t.Errorf("List-Unsubscribe-Post missing:\n%s", msg)
	}
}

// --- failover ------------------------------------------------------------

type scriptedSender struct {
	BaseProvider
	name  string
	err   error
	calls atomic.Int32
	log   *[]string
	mu    *sync.Mutex
}

func (s *scriptedSender) Send(context.Context, Email) error {
	s.calls.Add(1)
	if s.log != nil {
		s.mu.Lock()
		*s.log = append(*s.log, s.name)
		s.mu.Unlock()
	}
	return s.err
}
func (s *scriptedSender) Ping(context.Context) error { return s.err }

func TestFailoverUsesTheFirstWorkingSender(t *testing.T) {
	var mu sync.Mutex
	var order []string

	primary := &scriptedSender{name: "primary", err: errors.New("smtp unreachable"), log: &order, mu: &mu}
	backup := &scriptedSender{name: "backup", log: &order, mu: &mu}
	third := &scriptedSender{name: "third", log: &order, mu: &mu}

	s := FailoverSender(primary, backup, third)
	if err := s.Send(context.Background(), Email{From: "a@example.com"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(order) != 2 || order[0] != "primary" || order[1] != "backup" {
		t.Errorf("call order = %v, want [primary backup]", order)
	}
	if third.calls.Load() != 0 {
		t.Error("the third sender should not have been tried after a success")
	}
}

// A permanent rejection means the message is wrong, not the provider. Offering
// it to every backup just collects the same rejection several times.
func TestFailoverStopsOnPermanentFailure(t *testing.T) {
	primary := &scriptedSender{name: "primary", err: NonRetryable(errors.New("invalid recipient"))}
	backup := &scriptedSender{name: "backup"}

	s := FailoverSender(primary, backup)
	if err := s.Send(context.Background(), Email{From: "a@example.com"}); err == nil {
		t.Fatal("expected the permanent failure to surface")
	}
	if backup.calls.Load() != 0 {
		t.Error("a permanent failure must not be retried on the backup")
	}
}

func TestFailoverJoinsEveryError(t *testing.T) {
	first := errors.New("first down")
	second := errors.New("second down")

	s := FailoverSender(
		&scriptedSender{err: first},
		&scriptedSender{err: second},
	)
	err := s.Send(context.Background(), Email{From: "a@example.com"})
	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Errorf("got %v, want both underlying errors joined", err)
	}
}

func TestFailoverReportsEachFailover(t *testing.T) {
	var mu sync.Mutex
	var seen []int

	s := FailoverSenderWithCallback(
		func(_ context.Context, i int, _ error) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, i)
		},
		&scriptedSender{err: errors.New("down")},
		&scriptedSender{},
	)
	if err := s.Send(context.Background(), Email{From: "a@example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != 0 {
		t.Errorf("callback saw %v, want [0]; a silent failover hides an outage", seen)
	}
}

func TestFailoverWithNoSenders(t *testing.T) {
	s := FailoverSender()
	if err := s.Send(context.Background(), Email{}); !errors.Is(err, ErrNoSenders) {
		t.Errorf("got %v, want ErrNoSenders", err)
	}
	if err := s.Ping(context.Background()); !errors.Is(err, ErrNoSenders) {
		t.Errorf("Ping: got %v, want ErrNoSenders", err)
	}
}

func TestFailoverPingSucceedsIfAnySenderIsUp(t *testing.T) {
	s := FailoverSender(
		&scriptedSender{err: errors.New("down")},
		&scriptedSender{},
	)
	if err := s.Ping(context.Background()); err != nil {
		t.Errorf("Ping should succeed when a backup is reachable: %v", err)
	}
}

// --- rate limiting -------------------------------------------------------

func TestTokenBucketPaces(t *testing.T) {
	b := NewTokenBucket(20*time.Millisecond, 1)

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	// One token is available immediately; the next three each wait ~20ms.
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Errorf("4 sends at 20ms apart took %v; the limiter is not pacing", elapsed)
	}
}

func TestTokenBucketAllowsABurst(t *testing.T) {
	b := NewTokenBucket(time.Second, 5)

	start := time.Now()
	for i := 0; i < 5; i++ {
		if err := b.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("a burst of 5 took %v; it should be immediate", elapsed)
	}
}

func TestTokenBucketRespectsContext(t *testing.T) {
	b := NewTokenBucket(time.Hour, 1)
	if err := b.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := b.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Wait blocked for %v after the context expired", elapsed)
	}
}

func TestTokenBucketIsConcurrencySafe(t *testing.T) {
	b := NewTokenBucket(time.Microsecond, 10)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := b.Wait(context.Background()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestRateLimitedSender(t *testing.T) {
	inner := &scriptedSender{}
	s := RateLimitedSender(inner, NewTokenBucket(15*time.Millisecond, 1))

	start := time.Now()
	for i := 0; i < 3; i++ {
		if err := s.Send(context.Background(), Email{From: "a@example.com"}); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("3 sends took %v; the limiter is not applied", elapsed)
	}
	if int(inner.calls.Load()) != 3 {
		t.Errorf("sent %d, want 3", int(inner.calls.Load()))
	}
}

func TestRateLimitedSenderWithNilLimiterIsAPassThrough(t *testing.T) {
	inner := &scriptedSender{}
	if s := RateLimitedSender(inner, nil); s != Sender(inner) {
		t.Error("a nil limiter should return the original sender unchanged")
	}
}

// --- token caching -------------------------------------------------------

func TestCachingTokenSourceReusesAValidToken(t *testing.T) {
	var refreshes atomic.Int32
	ts := CachingTokenSource(func(context.Context) (string, time.Time, error) {
		refreshes.Add(1)
		return "tok", time.Now().Add(time.Hour), nil
	}, time.Minute)

	for i := 0; i < 100; i++ {
		tok, err := ts(context.Background())
		if err != nil || tok != "tok" {
			t.Fatalf("got %q, %v", tok, err)
		}
	}
	if n := refreshes.Load(); n != 1 {
		t.Errorf("refreshed %d times, want 1; TokenSource is called on every send", n)
	}
}

func TestCachingTokenSourceRenewsBeforeExpiry(t *testing.T) {
	var refreshes atomic.Int32
	// Expires inside the leeway window, so every call must renew.
	ts := CachingTokenSource(func(context.Context) (string, time.Time, error) {
		refreshes.Add(1)
		return "tok", time.Now().Add(10 * time.Second), nil
	}, time.Minute)

	for i := 0; i < 3; i++ {
		if _, err := ts(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := refreshes.Load(); n != 3 {
		t.Errorf("refreshed %d times, want 3: a token expiring within the leeway must be renewed", n)
	}
}

// Concurrent callers arriving on an expired token must produce one refresh,
// not one each: a thundering herd at the identity provider is how you get
// rate-limited out of sending entirely.
func TestCachingTokenSourceSingleFlights(t *testing.T) {
	var refreshes atomic.Int32
	ts := CachingTokenSource(func(context.Context) (string, time.Time, error) {
		refreshes.Add(1)
		time.Sleep(20 * time.Millisecond)
		return "tok", time.Now().Add(time.Hour), nil
	}, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := ts(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	if n := refreshes.Load(); n != 1 {
		t.Errorf("32 concurrent callers caused %d refreshes, want 1", n)
	}
}

func TestCachingTokenSourcePropagatesFailure(t *testing.T) {
	sentinel := errors.New("identity provider down")
	ts := CachingTokenSource(func(context.Context) (string, time.Time, error) {
		return "", time.Time{}, sentinel
	}, time.Minute)

	if _, err := ts(context.Background()); !errors.Is(err, sentinel) {
		t.Errorf("got %v, want the refresh error", err)
	}
}

func TestCachingTokenSourceRejectsEmptyToken(t *testing.T) {
	ts := CachingTokenSource(func(context.Context) (string, time.Time, error) {
		return "", time.Now().Add(time.Hour), nil
	}, time.Minute)

	if _, err := ts(context.Background()); err == nil {
		t.Error("an empty token should be an error, not a cached value")
	}
}

func TestCachingTokenSourceWithoutRefreshFunc(t *testing.T) {
	ts := CachingTokenSource(nil, time.Minute)
	if _, err := ts(context.Background()); err == nil {
		t.Error("expected an error when no refresh function is configured")
	}
}

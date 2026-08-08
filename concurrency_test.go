package gsmail

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- goroutine leak detector -------------------------------------------

func goroutineCount() int {
	// Let anything transient wind down first.
	for i := 0; i < 50; i++ {
		runtime.Gosched()
		time.Sleep(2 * time.Millisecond)
	}
	return runtime.NumGoroutine()
}

func dumpGoroutines() string {
	buf := make([]byte, 1<<20)
	n := runtime.Stack(buf, true)
	return string(buf[:n])
}

func assertNoLeak(t *testing.T, before int) {
	t.Helper()
	after := goroutineCount()
	if after > before+2 { // small slack for runtime/netpoll workers
		t.Errorf("goroutine leak: %d -> %d\n%s", before, after, dumpGoroutines())
	}
}

// ---- BaseProvider: public field + mutex ---------------------------------

// RetryConfig is an exported field guarded by an unexported mutex, and the doc
// comment invites callers to set it directly. GetRetryConfig reads it under
// RLock while SetRetryConfig writes under Lock -- but a caller touching the
// field directly races with both.
func TestBaseProviderConcurrentConfig(t *testing.T) {
	var p BaseProvider

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				p.SetRetryConfig(RetryConfig{MaxRetries: i, InitialInterval: time.Millisecond})
			}
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = p.GetRetryConfig()
			}
		}()
	}
	wg.Wait()
}

// ---- BackgroundSender ---------------------------------------------------

type blockingSender struct {
	release chan struct{}
	sent    int64
	mu      sync.Mutex
}

func (s *blockingSender) Send(ctx context.Context, email Email) error {
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s.mu.Lock()
	s.sent++
	s.mu.Unlock()
	return nil
}
func (s *blockingSender) Ping(context.Context) error { return nil }
func (s *blockingSender) SetRetryConfig(RetryConfig) {}

func TestBackgroundSenderNoGoroutineLeak(t *testing.T) {
	before := goroutineCount()

	for i := 0; i < 20; i++ {
		bs := NewBackgroundSender(&blockingSender{}, 16)
		bs.Start(4)
		for j := 0; j < 32; j++ {
			_ = bs.TrySend(Email{From: "a@b.test", To: []string{"c@d.test"}, Body: []byte("x")})
		}
		bs.Stop()
	}

	assertNoLeak(t, before)
}

func TestBackgroundSenderStopNowDoesNotLeak(t *testing.T) {
	before := goroutineCount()

	for i := 0; i < 20; i++ {
		s := &blockingSender{release: make(chan struct{})}
		bs := NewBackgroundSender(s, 8)
		bs.Start(4)
		for j := 0; j < 8; j++ {
			_ = bs.TrySend(Email{From: "a@b.test", To: []string{"c@d.test"}})
		}
		bs.StopNow() // abandon queued work while workers are blocked
	}

	assertNoLeak(t, before)
}

// Send/Stop racing must never panic on a closed channel.
func TestBackgroundSenderSendStopRace(t *testing.T) {
	for i := 0; i < 50; i++ {
		bs := NewBackgroundSender(&blockingSender{}, 4)
		bs.Start(2)

		var wg sync.WaitGroup
		for j := 0; j < 8; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for k := 0; k < 50; k++ {
					_ = bs.TrySend(Email{From: "a@b.test"})
				}
			}()
		}
		go bs.Stop()
		wg.Wait()
		bs.Stop() // idempotent
	}
}

// ---- HealthChecker ------------------------------------------------------

type slowResolver struct {
	delay time.Duration
}

func (r slowResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	select {
	case <-time.After(r.delay):
		return []*net.MX{{Host: "mx.test", Pref: 10}}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r slowResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	select {
	case <-time.After(r.delay):
		return []string{"v=spf1 -all"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// CheckDomainHealth fans out one goroutine per check plus a closer. When the
// caller's context expires mid-flight it returns early; nothing may be left
// running.
func TestHealthCheckerNoLeakOnCancel(t *testing.T) {
	before := goroutineCount()

	for i := 0; i < 20; i++ {
		h := HealthChecker{Resolver: slowResolver{delay: 2 * time.Second}}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		_, _ = h.CheckDomainHealth(ctx, "example.com", []string{"s1", "s2", "s3"})
		cancel()
	}

	// The in-flight lookups hold their own timers; give them room to unwind.
	time.Sleep(300 * time.Millisecond)
	assertNoLeak(t, before)
}

func TestHealthCheckerConcurrent(t *testing.T) {
	h := HealthChecker{Resolver: slowResolver{delay: time.Millisecond}}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.CheckDomainHealth(context.Background(), "example.com", []string{"s1", "s2"})
		}()
	}
	wg.Wait()
}

// ---- buffer pool --------------------------------------------------------

// Concurrent renders share one sync.Pool. A slice handed to two callers, or
// retained past its Put, corrupts output.
func TestRenderMessageConcurrentIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("marker-%04d", i)
			for j := 0; j < 100; j++ {
				raw, err := RenderMessage(Email{
					From:    "a@b.test",
					To:      []string{"c@d.test"},
					Subject: marker,
					Body:    []byte(strings.Repeat(marker, 20)),
				})
				if err != nil {
					t.Errorf("RenderMessage: %v", err)
					return
				}
				if !strings.Contains(string(raw), marker) {
					t.Errorf("buffer cross-talk: %q missing from rendered message", marker)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestWithMessageConcurrentIsolation(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			marker := fmt.Sprintf("wm-%04d", i)
			for j := 0; j < 100; j++ {
				err := WithMessage(Email{
					From: "a@b.test", To: []string{"c@d.test"},
					Subject: marker,
					Body:    []byte(strings.Repeat(marker, 20)),
				}, func(msg []byte) error {
					if !strings.Contains(string(msg), marker) {
						return fmt.Errorf("buffer cross-talk: %q missing", marker)
					}
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// ---- template cache -----------------------------------------------------

func TestSetBodyConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				var e Email
				if err := e.SetBody("<h1>{{.N}}</h1>", map[string]int{"N": i}); err != nil {
					t.Error(err)
					return
				}
				want := fmt.Sprintf("<h1>%d</h1>", i)
				if string(e.HTMLBody) != want {
					t.Errorf("got %q want %q", e.HTMLBody, want)
					return
				}
			}
		}(i)
	}
	wg.Wait()
}

// ---- webhook verifiers --------------------------------------------------

func TestSendGridVerifierConcurrentInit(t *testing.T) {
	v := &SendGridVerifier{PublicKey: "not-valid-base64-!!!"}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = v.Verify(nil, nil)
		}()
	}
	wg.Wait()
}

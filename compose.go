package gsmail

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Sender is small enough that useful behaviour composes as wrappers rather
// than as options on every provider. These are the two that were missing.

// --- Failover ------------------------------------------------------------

// ErrNoSenders is returned when a composite sender has nothing to send with.
var ErrNoSenders = errors.New("gsmail: no senders configured")

type failoverSender struct {
	senders []Sender
	onFail  func(ctx context.Context, index int, err error)
}

// FailoverSender tries each sender in order until one succeeds.
//
// It moves on only for failures that are worth retrying elsewhere. A permanent
// rejection -- an invalid recipient, a malformed message, a suppressed address
// -- means the message is the problem, not the provider, so it is returned
// immediately rather than offered to every backup in turn. Context
// cancellation stops the walk for the same reason.
//
// Every sender's error is joined into the returned error, so a total failure
// says what each one did rather than only the last.
func FailoverSender(senders ...Sender) Sender {
	return &failoverSender{senders: senders}
}

// FailoverSenderWithCallback is FailoverSender plus a hook invoked each time a
// sender is passed over. Use it to record which provider is degrading; without
// it a silent failover hides an outage until the primary is entirely dead.
func FailoverSenderWithCallback(onFail func(ctx context.Context, index int, err error), senders ...Sender) Sender {
	return &failoverSender{senders: senders, onFail: onFail}
}

func (f *failoverSender) Send(ctx context.Context, email Email) error {
	if len(f.senders) == 0 {
		return NonRetryable(ErrNoSenders)
	}

	var errs []error
	for i, s := range f.senders {
		if s == nil {
			continue
		}
		err := s.Send(ctx, email)
		if err == nil {
			return nil
		}
		errs = append(errs, fmt.Errorf("sender %d: %w", i, err))

		if f.onFail != nil {
			f.onFail(ctx, i, err)
		}

		// A permanent failure is a property of the message, not the provider.
		if !IsRetryable(err) {
			return errors.Join(errs...)
		}
		if ctx.Err() != nil {
			return errors.Join(append(errs, ctx.Err())...)
		}
	}
	if len(errs) == 0 {
		return NonRetryable(ErrNoSenders)
	}
	return errors.Join(errs...)
}

// Ping reports success if any sender is reachable.
func (f *failoverSender) Ping(ctx context.Context) error {
	if len(f.senders) == 0 {
		return NonRetryable(ErrNoSenders)
	}
	var errs []error
	for i, s := range f.senders {
		if s == nil {
			continue
		}
		if err := s.Ping(ctx); err == nil {
			return nil
		} else {
			errs = append(errs, fmt.Errorf("sender %d: %w", i, err))
		}
	}
	return errors.Join(errs...)
}

// SetRetryConfig applies the configuration to every underlying sender.
func (f *failoverSender) SetRetryConfig(config RetryConfig) {
	for _, s := range f.senders {
		if s != nil {
			s.SetRetryConfig(config)
		}
	}
}

// --- Rate limiting -------------------------------------------------------

// Limiter paces outbound sends. golang.org/x/time/rate.Limiter satisfies it,
// and so does TokenBucket below.
//
// It is an interface rather than a concrete type so this package does not
// force a dependency on callers who already have a limiter.
type Limiter interface {
	// Wait blocks until the caller may proceed, or the context is done.
	Wait(ctx context.Context) error
}

type rateLimitedSender struct {
	Sender
	limiter Limiter
}

// RateLimitedSender paces sends through a Limiter.
//
// The retry logic already reacts to a 429 after the fact; this avoids
// provoking one. That distinction matters because providers count rejected
// requests against you, so backing off after being told to is strictly worse
// than not exceeding the limit.
func RateLimitedSender(s Sender, limiter Limiter) Sender {
	if limiter == nil {
		return s
	}
	return &rateLimitedSender{Sender: s, limiter: limiter}
}

func (r *rateLimitedSender) Send(ctx context.Context, email Email) error {
	if err := r.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("gsmail: rate limiter: %w", err)
	}
	return r.Sender.Send(ctx, email)
}

// TokenBucket is a simple rate limiter, provided so the common case needs no
// extra dependency. It refills at a steady rate up to a burst size.
//
// The zero value is not usable; construct one with NewTokenBucket.
type TokenBucket struct {
	mu       sync.Mutex
	interval time.Duration
	burst    int
	tokens   float64
	last     time.Time
	// now is overridable for tests.
	now func() time.Time
}

// NewTokenBucket returns a limiter permitting one send per interval, allowing
// up to burst sends to accumulate while idle. A burst below one is raised to
// one, since a bucket that can never hold a token would block forever.
func NewTokenBucket(interval time.Duration, burst int) *TokenBucket {
	if burst < 1 {
		burst = 1
	}
	if interval < 0 {
		interval = 0
	}
	return &TokenBucket{
		interval: interval,
		burst:    burst,
		tokens:   float64(burst),
		last:     time.Now(),
		now:      time.Now,
	}
}

// Wait blocks until a token is available or ctx is done.
func (b *TokenBucket) Wait(ctx context.Context) error {
	for {
		delay, ok := b.reserve()
		if ok {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// reserve takes a token if one is available, otherwise reports how long to
// wait before trying again.
func (b *TokenBucket) reserve() (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.interval <= 0 {
		return 0, true
	}

	now := b.now()
	elapsed := now.Sub(b.last)
	if elapsed > 0 {
		b.tokens += float64(elapsed) / float64(b.interval)
		if b.tokens > float64(b.burst) {
			b.tokens = float64(b.burst)
		}
		b.last = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}

	// Wait for the remaining fraction of a token.
	need := 1 - b.tokens
	return time.Duration(need * float64(b.interval)), false
}

// --- OAuth token caching -------------------------------------------------

// RefreshFunc obtains a new access token and reports when it expires.
type RefreshFunc func(ctx context.Context) (token string, expiry time.Time, err error)

// CachingTokenSource wraps a refresh function so a token is fetched once and
// reused until shortly before it expires.
//
// TokenSource is called on every send and on every retry. Without caching that
// is a round trip to the identity provider per message, and under concurrency
// it is a thundering herd of them. Refresh is single-flighted: concurrent
// callers arriving on an expired token produce one refresh, not one each.
//
// leeway is how early to renew. Zero uses DefaultTokenLeeway; a token that
// expires in transit fails the send it was fetched for.
func CachingTokenSource(refresh RefreshFunc, leeway time.Duration) TokenSource {
	if leeway <= 0 {
		leeway = DefaultTokenLeeway
	}
	c := &cachingTokenSource{refresh: refresh, leeway: leeway, now: time.Now}
	return c.Token
}

// DefaultTokenLeeway is how far ahead of expiry a cached token is renewed.
const DefaultTokenLeeway = 60 * time.Second

type cachingTokenSource struct {
	refresh RefreshFunc
	leeway  time.Duration
	now     func() time.Time

	mu     sync.Mutex
	token  string
	expiry time.Time
}

func (c *cachingTokenSource) Token(ctx context.Context) (string, error) {
	if c.refresh == nil {
		return "", NonRetryable(errors.New("gsmail: CachingTokenSource has no refresh function"))
	}

	// The lock is held across the refresh so that concurrent callers on an
	// expired token perform one refresh between them rather than one each.
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && c.now().Before(c.expiry.Add(-c.leeway)) {
		return c.token, nil
	}

	token, expiry, err := c.refresh(ctx)
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", NonRetryable(errors.New("gsmail: token refresh returned an empty token"))
	}

	c.token, c.expiry = token, expiry
	return token, nil
}

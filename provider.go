package gsmail

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

// RetryConfig defines the configuration for connection retries.
//
// MaxRetries counts retries, not attempts: 0 means "try once, never retry".
type RetryConfig struct {
	MaxRetries      int           // Maximum number of retries.
	InitialInterval time.Duration // Initial interval between retries.
	MaxInterval     time.Duration // Maximum interval between retries.
	Multiplier      float64       // Exponential backoff multiplier.
}

// DefaultRetryConfig returns a default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:      3,
		InitialInterval: 500 * time.Millisecond,
		MaxInterval:     5 * time.Second,
		Multiplier:      2.0,
	}
}

// normalize fills in unset backoff parameters without touching MaxRetries, so
// that a deliberately configured MaxRetries of 0 disables retrying.
func (c RetryConfig) normalize() RetryConfig {
	d := DefaultRetryConfig()
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.InitialInterval <= 0 {
		c.InitialInterval = d.InitialInterval
	}
	if c.MaxInterval <= 0 {
		c.MaxInterval = d.MaxInterval
	}
	if c.Multiplier < 1 {
		c.Multiplier = d.Multiplier
	}
	return c
}

// Sender defines the interface for different email delivery methods.
type Sender interface {
	Send(ctx context.Context, email Email) error
	Ping(ctx context.Context) error
	SetRetryConfig(config RetryConfig)
}

// SearchOptions defines the criteria for searching emails.
type SearchOptions struct {
	From    string
	Subject string
	Since   time.Time
	Before  time.Time
	Unseen  bool
}

// Receiver defines the interface for different email receiving methods.
type Receiver interface {
	Receive(ctx context.Context, limit int) ([]Email, error)
	Search(ctx context.Context, options SearchOptions, limit int) ([]Email, error)
	Idle(ctx context.Context) (<-chan Email, <-chan error)
	Ping(ctx context.Context) error
	SetRetryConfig(config RetryConfig)
}

// Pinger is implemented by anything whose connectivity can be checked.
// Both Sender and Receiver satisfy it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// AddressValidator is implemented by providers that can validate a recipient
// address. It is intentionally not part of Sender or Receiver: validation is
// an optional operation that may hit the network, so callers must ask for it
// explicitly.
type AddressValidator interface {
	Validate(ctx context.Context, email string) error
}

// BaseProvider implements common logic for all providers.
//
// The zero value is ready to use and retries with DefaultRetryConfig. Do not
// copy a BaseProvider after first use.
//
// The configuration is held in an atomic pointer rather than a mutex-guarded
// exported field. The exported field was a data race waiting to happen — its
// own documentation invited callers to assign it directly while GetRetryConfig
// read it under a lock — and every Send goes through GetRetryConfig, so the
// read side wants to be free.
type BaseProvider struct {
	retryConfig atomic.Pointer[RetryConfig]
}

// SetRetryConfig sets the retry configuration for the provider.
// Passing a config with MaxRetries set to 0 disables retrying entirely.
// It is safe to call concurrently with Send.
func (p *BaseProvider) SetRetryConfig(config RetryConfig) {
	normalized := config.normalize()
	p.retryConfig.Store(&normalized)
}

// GetRetryConfig returns the effective retry configuration. The default is
// only substituted when no configuration was supplied at all; an explicitly
// configured MaxRetries of 0 is honoured.
func (p *BaseProvider) GetRetryConfig() RetryConfig {
	if c := p.retryConfig.Load(); c != nil {
		return *c
	}
	return DefaultRetryConfig()
}

// Validate performs offline validation of an address: syntax plus the built-in
// disposable-domain list. It never touches the network.
//
// For MX or mailbox checks build a Validator explicitly and read the caveats
// documented there before enabling Validator.CheckMailbox.
func (p *BaseProvider) Validate(_ context.Context, email string) error {
	return ValidateEmailSyntax(email)
}

// ErrNonRetryable marks an error as permanent. Retry stops immediately when an
// error wraps it.
var ErrNonRetryable = errors.New("gsmail: non-retryable error")

// ErrInvalidLimit is returned by Receive and Search when the requested limit is
// not positive. A non-positive limit is rejected rather than reinterpreted:
// treating it as "no limit" would silently pull an entire mailbox, and
// treating it as "none" would hide a caller's arithmetic bug.
var ErrInvalidLimit = errors.New("gsmail: limit must be greater than zero")

// CheckLimit validates a Receive or Search limit. Providers call it before
// sizing any buffer or worker pool from caller input.
func CheckLimit(limit int) error {
	if limit <= 0 {
		return NonRetryable(fmt.Errorf("%w (got %d)", ErrInvalidLimit, limit))
	}
	return nil
}

// Retryable lets an error state whether the operation that produced it may be
// retried. Errors that do not implement it are retried by default.
type Retryable interface {
	Retryable() bool
}

// RetryAfterProvider lets an error carry a server-requested delay, such as the
// Retry-After header on an HTTP 429.
type RetryAfterProvider interface {
	RetryAfter() time.Duration
}

type nonRetryableError struct{ err error }

func (e *nonRetryableError) Error() string   { return e.err.Error() }
func (e *nonRetryableError) Unwrap() error   { return e.err }
func (e *nonRetryableError) Retryable() bool { return false }
func (e *nonRetryableError) Is(target error) bool {
	return target == ErrNonRetryable
}

// NonRetryable marks err as permanent so that Retry gives up immediately.
// It returns nil for a nil error.
func NonRetryable(err error) error {
	if err == nil {
		return nil
	}
	return &nonRetryableError{err: err}
}

// IsRetryable reports whether an operation that failed with err is worth
// retrying. Context cancellation and errors marked with NonRetryable are not;
// everything else is, unless the error implements Retryable and says no.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var r Retryable
	if errors.As(err, &r) {
		return r.Retryable()
	}
	return !errors.Is(err, ErrNonRetryable)
}

// retryAfter extracts a server-requested delay from err, if it carries one.
func retryAfter(err error) (time.Duration, bool) {
	var ra RetryAfterProvider
	if errors.As(err, &ra) {
		if d := ra.RetryAfter(); d > 0 {
			return d, true
		}
	}
	return 0, false
}

// Retry executes fn with exponential backoff.
//
// It stops early when the context is done or when the returned error is not
// retryable (see IsRetryable), which keeps permanent failures such as an
// invalid recipient from being sent four times. An error carrying a
// Retry-After hint overrides the computed backoff for that attempt.
func Retry(ctx context.Context, config RetryConfig, fn func() error) error {
	config = config.normalize()

	var lastErr error
	interval := config.InitialInterval

	for i := 0; i <= config.MaxRetries; i++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), lastErr)
		default:
		}

		err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		if !IsRetryable(err) {
			return err
		}

		if i == config.MaxRetries {
			break
		}

		wait := interval
		if d, ok := retryAfter(err); ok {
			wait = d
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), lastErr)
		case <-timer.C:
		}

		interval = time.Duration(float64(interval) * config.Multiplier)
		if interval > config.MaxInterval {
			interval = config.MaxInterval
		}
	}

	return lastErr
}

// ErrEnvelopeUnsupported is returned by providers that cannot separate the
// envelope recipients from the address headers.
//
// Silently ignoring Email.Envelope would be worse than refusing it: a caller
// setting it is rendering one copy per recipient, so delivering to everyone
// named in To and Cc instead would send each of them a copy per recipient.
var ErrEnvelopeUnsupported = errors.New("gsmail: this provider cannot deliver to an explicit envelope; the recipient list is derived from To, Cc and Bcc")

// RejectEnvelope reports the error a provider should return when a message sets
// Email.Envelope and the transport cannot honour it.
func RejectEnvelope(provider string, email Email) error {
	if len(email.Envelope) == 0 {
		return nil
	}
	return NonRetryable(fmt.Errorf("%s: %w", provider, ErrEnvelopeUnsupported))
}

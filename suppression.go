package gsmail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// This file closes a loop the package previously left open. ParseBounce and the
// webhook parsers tell you an address is dead; nothing consumed that. Sending
// again to an address the receiving system has already rejected is the single
// strongest negative deliverability signal there is, so bounce parsing is only
// worth anything if something acts on it.

// ErrAllRecipientsSuppressed is returned when every recipient of a message is
// suppressed and there is nobody left to send to.
var ErrAllRecipientsSuppressed = errors.New("gsmail: every recipient is suppressed")

// Suppressor reports whether mail should be withheld from an address.
//
// Implementations are called once per recipient on every send, so they should
// be fast and safe for concurrent use. Back them with whatever store already
// holds your bounce history; MemorySuppressionList is provided for tests and
// small deployments.
type Suppressor interface {
	Suppressed(ctx context.Context, address string) (bool, error)
}

// SuppressorFunc adapts a function to Suppressor.
type SuppressorFunc func(ctx context.Context, address string) (bool, error)

// Suppressed calls f.
func (f SuppressorFunc) Suppressed(ctx context.Context, address string) (bool, error) {
	return f(ctx, address)
}

// SuppressionOptions configures SuppressionInterceptorWith.
type SuppressionOptions struct {
	// OnSuppressed is called for each recipient removed from a message.
	// Use it to record the drop; without it the removal is silent.
	OnSuppressed func(ctx context.Context, address string)

	// IgnoreErrors lets a message through when the Suppressor itself fails.
	//
	// The default is to fail the send. A suppression list that cannot be
	// reached is not evidence that an address is deliverable, and the cost of
	// a delayed message is lower than the cost of mailing an address that
	// already generated a complaint.
	IgnoreErrors bool
}

// SuppressionInterceptor withholds mail from suppressed recipients.
//
// Suppressed addresses are removed from To, Cc and Bcc before the message
// reaches the sender. If that leaves no recipients, the send fails with
// ErrAllRecipientsSuppressed and nothing is transmitted.
//
// Removal is deliberately silent unless you supply OnSuppressed through
// SuppressionInterceptorWith: a partially delivered message still succeeds, so
// record the drops if you need to account for them.
func SuppressionInterceptor(s Suppressor) SendInterceptor {
	return SuppressionInterceptorWith(s, SuppressionOptions{})
}

// SuppressionInterceptorWith is SuppressionInterceptor with explicit options.
func SuppressionInterceptorWith(s Suppressor, opts SuppressionOptions) SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error {
		if s == nil {
			return next(ctx, email)
		}

		keep := func(list []string) ([]string, error) {
			if len(list) == 0 {
				return list, nil
			}
			out := make([]string, 0, len(list))
			for _, addr := range list {
				suppressed, err := s.Suppressed(ctx, NormalizeAddress(addr))
				if err != nil {
					if opts.IgnoreErrors {
						out = append(out, addr)
						continue
					}
					return nil, fmt.Errorf("gsmail: suppression lookup for %q: %w", addr, err)
				}
				if suppressed {
					if opts.OnSuppressed != nil {
						opts.OnSuppressed(ctx, addr)
					}
					continue
				}
				out = append(out, addr)
			}
			return out, nil
		}

		var err error
		if email.To, err = keep(email.To); err != nil {
			return err
		}
		if email.Cc, err = keep(email.Cc); err != nil {
			return err
		}
		if email.Bcc, err = keep(email.Bcc); err != nil {
			return err
		}

		if len(email.To)+len(email.Cc)+len(email.Bcc) == 0 {
			return NonRetryable(ErrAllRecipientsSuppressed)
		}

		return next(ctx, email)
	}
}

// NormalizeAddress reduces an address to the form a suppression list should be
// keyed by: the bare addr-spec, lowercased.
//
// "Alice <Alice@Example.COM>" and "alice@example.com" are the same mailbox and
// must not occupy two entries, or a bounce recorded under one spelling fails
// to suppress the other.
func NormalizeAddress(address string) string {
	s := strings.TrimSpace(address)
	if a, err := ParseEmailAddress(s); err == nil && a != nil {
		s = a.Address
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// SuppressionReason records why an address was suppressed.
type SuppressionReason string

const (
	// ReasonHardBounce is a permanent delivery failure.
	ReasonHardBounce SuppressionReason = "hard_bounce"
	// ReasonComplaint is a spam complaint. These matter more than bounces:
	// continuing to mail a complainant is what gets a sending domain blocked.
	ReasonComplaint SuppressionReason = "complaint"
	// ReasonUnsubscribe is a recipient opting out.
	ReasonUnsubscribe SuppressionReason = "unsubscribe"
	// ReasonManual is an operator decision.
	ReasonManual SuppressionReason = "manual"
)

// SuppressionEntry is one address on a suppression list.
type SuppressionEntry struct {
	Address string
	Reason  SuppressionReason
	// At is when the address was suppressed. Zero if unknown.
	At time.Time
	// Detail carries the provider's explanation, when there is one.
	Detail string
}

// MemorySuppressionList is an in-memory Suppressor.
//
// It is intended for tests and for deployments small enough that losing the
// list on restart is acceptable. Anything else should implement Suppressor
// over durable storage: a suppression list that forgets is a suppression list
// that re-sends to addresses that already complained.
type MemorySuppressionList struct {
	mu      sync.RWMutex
	entries map[string]SuppressionEntry
}

// NewMemorySuppressionList returns an empty list ready for use.
func NewMemorySuppressionList() *MemorySuppressionList {
	return &MemorySuppressionList{entries: make(map[string]SuppressionEntry)}
}

// Add suppresses an address.
func (l *MemorySuppressionList) Add(address string, reason SuppressionReason) {
	l.AddEntry(SuppressionEntry{Address: address, Reason: reason, At: time.Now()})
}

// AddEntry suppresses an address with full detail. A later entry replaces an
// earlier one for the same address.
func (l *MemorySuppressionList) AddEntry(e SuppressionEntry) {
	key := NormalizeAddress(e.Address)
	if key == "" {
		return
	}
	e.Address = key

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = make(map[string]SuppressionEntry)
	}
	l.entries[key] = e
}

// AddBounce suppresses the address from a hard bounce and reports whether it
// did. A soft bounce is transient -- a full mailbox, a greylisting relay -- and
// is deliberately ignored, because suppressing on one is how a live recipient
// gets permanently cut off.
func (l *MemorySuppressionList) AddBounce(b *Bounce) bool {
	if b == nil || b.Type != BounceHard || b.EmailAddress == "" {
		return false
	}
	l.AddEntry(SuppressionEntry{
		Address: b.EmailAddress,
		Reason:  ReasonHardBounce,
		At:      b.Timestamp,
		Detail:  b.Reason,
	})
	return true
}

// AddComplaint suppresses the address from a spam complaint and reports
// whether it did.
func (l *MemorySuppressionList) AddComplaint(c *Complaint) bool {
	if c == nil || c.EmailAddress == "" {
		return false
	}
	l.AddEntry(SuppressionEntry{
		Address: c.EmailAddress,
		Reason:  ReasonComplaint,
		At:      c.Timestamp,
		Detail:  c.Type,
	})
	return true
}

// Remove takes an address off the list, for example after a recipient
// re-subscribes.
func (l *MemorySuppressionList) Remove(address string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, NormalizeAddress(address))
}

// Suppressed implements Suppressor.
func (l *MemorySuppressionList) Suppressed(_ context.Context, address string) (bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, found := l.entries[NormalizeAddress(address)]
	return found, nil
}

// Entry returns the recorded entry for an address.
func (l *MemorySuppressionList) Entry(address string) (SuppressionEntry, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	e, found := l.entries[NormalizeAddress(address)]
	return e, found
}

// Len reports how many addresses are suppressed.
func (l *MemorySuppressionList) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.entries)
}

// Entries returns a snapshot of the list.
func (l *MemorySuppressionList) Entries() []SuppressionEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()

	out := make([]SuppressionEntry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e)
	}
	return out
}

// Record routes any event produced by the webhook parsers or ParseBounce onto
// the list, suppressing hard bounces and complaints and ignoring the rest.
// It reports whether the event resulted in a suppression.
//
// It accepts `any` so the output of ParseSESWebhook, ParseMailgunWebhook and
// ParsePostmarkWebhook -- which return a Bounce or a Complaint depending on
// the payload -- can be handed straight over.
func (l *MemorySuppressionList) Record(event any) bool {
	switch e := event.(type) {
	case *Bounce:
		return l.AddBounce(e)
	case *Complaint:
		return l.AddComplaint(e)
	case []any:
		suppressed := false
		for _, item := range e {
			if l.Record(item) {
				suppressed = true
			}
		}
		return suppressed
	}
	return false
}

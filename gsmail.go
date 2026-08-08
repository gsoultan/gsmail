package gsmail

import (
	"context"
	"fmt"
)

const (
	// HeaderMIME is the standard MIME-Version header.
	HeaderMIME = "MIME-Version: 1.0"
	// HeaderHTML is the default Content-Type for HTML emails.
	HeaderHTML = "Content-Type: text/html; charset=\"UTF-8\""
	// HeaderPlain is the default Content-Type for plaintext emails.
	HeaderPlain = "Content-Type: text/plain; charset=\"UTF-8\""
)

// Send sends an email using the specified sender.
func Send(ctx context.Context, s Sender, email Email) error {
	if s == nil {
		return fmt.Errorf("sender is nil")
	}
	return s.Send(ctx, email)
}

// Receive retrieves emails using the specified fetcher.
func Receive(ctx context.Context, f Receiver, limit int) ([]Email, error) {
	if f == nil {
		return nil, fmt.Errorf("receiver is nil")
	}
	return f.Receive(ctx, limit)
}

// Search searches for emails using the specified receiver.
func Search(ctx context.Context, f Receiver, options SearchOptions, limit int) ([]Email, error) {
	if f == nil {
		return nil, fmt.Errorf("receiver is nil")
	}
	return f.Search(ctx, options, limit)
}

// Idle waits for new emails using the specified receiver.
func Idle(ctx context.Context, f Receiver) (<-chan Email, <-chan error) {
	if f == nil {
		emailChan := make(chan Email)
		errChan := make(chan error, 1)
		close(emailChan)
		errChan <- fmt.Errorf("receiver is nil")
		close(errChan)
		return emailChan, errChan
	}
	return f.Idle(ctx)
}

// Ping checks the connection of the given sender or receiver. Both Sender and
// Receiver satisfy Pinger, so the check is enforced at compile time.
func Ping(ctx context.Context, p Pinger) error {
	if p == nil {
		return fmt.Errorf("pinger is nil")
	}
	return p.Ping(ctx)
}

// Validate validates an address using the given provider, if that provider
// supports validation. Validation is not part of the Sender or Receiver
// contract, so a provider that cannot validate reports an error rather than
// silently succeeding.
func Validate(ctx context.Context, p any, email string) error {
	v, ok := p.(AddressValidator)
	if !ok {
		return fmt.Errorf("provider does not implement AddressValidator")
	}
	return v.Validate(ctx, email)
}

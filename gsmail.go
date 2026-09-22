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

// Ping checks the connection of the given sender or receiver. Both Sender and
// Receiver satisfy Pinger, so the check is enforced at compile time.
func Ping(ctx context.Context, p Pinger) error {
	if p == nil {
		return fmt.Errorf("pinger is nil")
	}
	return p.Ping(ctx)
}

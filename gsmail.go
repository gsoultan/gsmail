// Package gsmail provides a high-performance email library.
package gsmail

import (
	"context"
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
	return s.Send(ctx, email)
}

// Receive retrieves emails using the specified fetcher.
func Receive(ctx context.Context, f Receiver, limit int) ([]Email, error) {
	return f.Receive(ctx, limit)
}

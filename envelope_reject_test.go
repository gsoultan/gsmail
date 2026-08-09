package gsmail

import (
	"errors"
	"strings"
	"testing"
)

// A provider that cannot separate the envelope from the headers must refuse a
// message that sets Envelope rather than ignore it.
//
// Ignoring it is the dangerous option: a caller sets Envelope precisely because
// it is rendering one copy per recipient, so falling back to "deliver to
// everyone in To and Cc" would send each of them one copy per recipient.
func TestRejectEnvelope(t *testing.T) {
	t.Run("passes a message that does not set an envelope", func(t *testing.T) {
		if err := RejectEnvelope("sendgrid", Email{To: []string{"a@example.com"}}); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("refuses a message that does", func(t *testing.T) {
		err := RejectEnvelope("sendgrid", Email{
			To:       []string{"a@example.com"},
			Envelope: []string{"a@example.com"},
		})
		if err == nil {
			t.Fatal("expected an error")
		}
		if !errors.Is(err, ErrEnvelopeUnsupported) {
			t.Errorf("error should wrap ErrEnvelopeUnsupported, got %v", err)
		}
		// Retrying cannot help: the transport will never support it.
		if IsRetryable(err) {
			t.Error("the refusal must be non-retryable")
		}
		if !errors.Is(err, ErrNonRetryable) {
			t.Error("the refusal should be marked non-retryable")
		}
		if got := err.Error(); !strings.Contains(got, "sendgrid") {
			t.Errorf("the error should name the provider, got %q", got)
		}
	})
}

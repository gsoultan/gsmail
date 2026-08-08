package sendgrid_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/sendgrid"
)

func newSender(t *testing.T, h http.HandlerFunc) (*sendgrid.Sender, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	s := sendgrid.NewSender("key")
	s.BaseURL = srv.URL
	s.SetRetryConfig(gsmail.RetryConfig{
		MaxRetries:      3,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2,
	})
	return s, srv
}

func sample() gsmail.Email {
	return gsmail.Email{
		From: "a@example.com",
		To:   []string{"b@example.com"},
		Body: []byte("hello"),
	}
}

// A 400 means the request is wrong; sending it four times will not make it
// right, and for an invalid recipient it means four rejections on the record.
func TestPermanentStatusIsNotRetried(t *testing.T) {
	for _, status := range []int{
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusUnprocessableEntity,
	} {
		var calls atomic.Int32
		s, _ := newSender(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"errors":[{"message":"bad recipient"}]}`)
		})

		err := s.Send(context.Background(), sample())
		if err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if got := calls.Load(); got != 1 {
			t.Errorf("status %d: sent %d times, want 1", status, got)
		}

		var httpErr *gsmail.HTTPError
		if !errors.As(err, &httpErr) {
			t.Fatalf("status %d: got %T, want *gsmail.HTTPError", status, err)
		}
		if httpErr.StatusCode != status {
			t.Errorf("got status %d, want %d", httpErr.StatusCode, status)
		}
		// The provider's own explanation has to survive into the error.
		if httpErr.Body == "" {
			t.Error("response body was dropped; the caller cannot tell what went wrong")
		}
	}
}

func TestTransientStatusIsRetried(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway} {
		var calls atomic.Int32
		s, _ := newSender(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.WriteHeader(status)
		})

		if err := s.Send(context.Background(), sample()); err == nil {
			t.Fatalf("status %d: expected an error", status)
		}
		if got := calls.Load(); got != 4 { // 1 attempt + 3 retries
			t.Errorf("status %d: sent %d times, want 4", status, got)
		}
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	var calls atomic.Int32
	s, _ := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})

	start := time.Now()
	if err := s.Send(context.Background(), sample()); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The configured backoff is 1ms; only the server's Retry-After explains a
	// pause of roughly a second.
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("waited %v, expected to honour the 1s Retry-After", elapsed)
	}
}

// List-Unsubscribe is required of bulk senders by Gmail and Yahoo. It used to
// be silently dropped on every API provider while working over SMTP.
func TestCustomHeadersReachTheAPI(t *testing.T) {
	var payload struct {
		Headers     map[string]string `json:"headers"`
		Attachments []struct {
			Disposition string `json:"disposition"`
			ContentID   string `json:"content_id"`
		} `json:"attachments"`
	}

	s, _ := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	})

	email := sample()
	email.SetHeader("List-Unsubscribe", "<https://x.test/u>")
	email.SetHeader("X-Campaign", "spring")
	email.SetHeader("Subject", "reserved, must be dropped")
	email.Attachments = []gsmail.Attachment{
		{Filename: "logo.png", ContentType: "image/png", ContentID: "logo", Data: []byte("x")},
	}

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if payload.Headers["List-Unsubscribe"] != "<https://x.test/u>" {
		t.Errorf("List-Unsubscribe missing, got headers %v", payload.Headers)
	}
	if payload.Headers["X-Campaign"] != "spring" {
		t.Errorf("X-Campaign missing, got headers %v", payload.Headers)
	}
	if _, found := payload.Headers["Subject"]; found {
		t.Error("reserved header Subject leaked into the API payload")
	}

	if len(payload.Attachments) != 1 {
		t.Fatalf("got %d attachments, want 1", len(payload.Attachments))
	}
	// A cid:-referenced image declared as "attachment" renders as a broken
	// image plus a duplicate at the bottom of the message.
	if payload.Attachments[0].Disposition != "inline" {
		t.Errorf("attachment with a Content-ID has disposition %q, want inline",
			payload.Attachments[0].Disposition)
	}
}

func TestInvalidHeaderNameIsRejected(t *testing.T) {
	s, _ := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should never have been sent")
		w.WriteHeader(http.StatusAccepted)
	})

	email := sample()
	email.SetHeader("X-Bad\r\nBcc", "attacker@evil.test")

	if err := s.Send(context.Background(), email); err == nil {
		t.Fatal("expected an error for an illegal header name")
	} else if gsmail.IsRetryable(err) {
		t.Error("an illegal header name is permanent, not retryable")
	}
}

func TestEmptyBodyIsRejectedBeforeSending(t *testing.T) {
	s, _ := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("request should never have been sent")
	})

	err := s.Send(context.Background(), gsmail.Email{From: "a@example.com", To: []string{"b@example.com"}})
	if err == nil {
		t.Fatal("expected an error for an email with no body")
	}
	if gsmail.IsRetryable(err) {
		t.Error("a missing body is permanent, not retryable")
	}
}

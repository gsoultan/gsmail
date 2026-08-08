package postmark_test

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
	"github.com/gsoultan/gsmail/postmark"
)

func newSender(t *testing.T, h http.HandlerFunc) *postmark.Sender {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	s := postmark.NewSender("token")
	s.BaseURL = srv.URL
	s.SetRetryConfig(gsmail.RetryConfig{
		MaxRetries:      3,
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		Multiplier:      2,
	})
	return s
}

func sample() gsmail.Email {
	return gsmail.Email{From: "a@example.com", To: []string{"b@example.com"}, Body: []byte("hello")}
}

func TestPermanentStatusIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	s := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"ErrorCode":406,"Message":"You tried to send to a recipient that has been marked as inactive."}`)
	})

	err := s.Send(context.Background(), sample())
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("sent %d times, want 1", got)
	}

	var httpErr *gsmail.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("got %T, want *gsmail.HTTPError", err)
	}
	// "postmark error: status 422" told the caller nothing; the ErrorCode is
	// what identifies the problem.
	if httpErr.Body == "" {
		t.Error("Postmark's ErrorCode payload was dropped from the error")
	}
}

func TestTransientStatusIsRetried(t *testing.T) {
	var calls atomic.Int32
	s := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	if err := s.Send(context.Background(), sample()); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("sent %d times, want 4", got)
	}
}

func TestCustomHeadersAndMessageStream(t *testing.T) {
	var payload struct {
		MessageStream string `json:"MessageStream"`
		Headers       []struct {
			Name  string `json:"Name"`
			Value string `json:"Value"`
		} `json:"Headers"`
	}

	s := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	s.MessageStream = "broadcast"

	email := sample()
	email.SetHeader("List-Unsubscribe", "<https://x.test/u>")
	email.SetHeader("Cc", "reserved, must be dropped")

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if payload.MessageStream != "broadcast" {
		t.Errorf("MessageStream = %q, want broadcast", payload.MessageStream)
	}

	found := map[string]string{}
	for _, h := range payload.Headers {
		found[h.Name] = h.Value
	}
	if found["List-Unsubscribe"] != "<https://x.test/u>" {
		t.Errorf("List-Unsubscribe missing, got %v", found)
	}
	if _, leaked := found["Cc"]; leaked {
		t.Error("reserved header Cc leaked into the API payload")
	}
}

package mailgun_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
	"github.com/gsoultan/gsmail/mailgun"
)

func newSender(t *testing.T, h http.HandlerFunc) *mailgun.Sender {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	s := mailgun.NewSender("example.test", "key")
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
		http.Error(w, `{"message":"not a valid address"}`, http.StatusBadRequest)
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
	if httpErr.Body == "" {
		t.Error("Mailgun's explanation was dropped from the error")
	}
}

func TestTransientStatusIsRetried(t *testing.T) {
	var calls atomic.Int32
	s := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	if err := s.Send(context.Background(), sample()); err == nil {
		t.Fatal("expected an error")
	}
	if got := calls.Load(); got != 4 {
		t.Errorf("sent %d times, want 4", got)
	}
}

func TestCustomHeadersBecomeHFields(t *testing.T) {
	var got map[string][]string
	s := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse form: %v", err)
		}
		got = r.MultipartForm.Value
		w.WriteHeader(http.StatusOK)
	})

	email := sample()
	email.SetHeader("List-Unsubscribe", "<https://x.test/u>")
	email.SetHeader("From", "reserved, must be dropped")

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if v := got["h:List-Unsubscribe"]; len(v) != 1 || v[0] != "<https://x.test/u>" {
		t.Errorf("h:List-Unsubscribe = %v, want [<https://x.test/u>]", v)
	}
	if _, found := got["h:From"]; found {
		t.Error("reserved header From leaked into the form")
	}
}

// The multipart payload is built once and replayed from a byte slice, so a
// retry must send exactly the same bytes rather than an empty body.
func TestRetryResendsTheFullBody(t *testing.T) {
	var lengths []int64
	var calls atomic.Int32
	s := newSender(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		lengths = append(lengths, n)
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	email := sample()
	email.Attachments = []gsmail.Attachment{{Filename: "a.txt", Data: []byte("some data")}}

	if err := s.Send(context.Background(), email); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(lengths) != 3 {
		t.Fatalf("got %d attempts, want 3", len(lengths))
	}
	for i, n := range lengths {
		if n == 0 {
			t.Errorf("attempt %d sent an empty body", i+1)
		}
		if n != lengths[0] {
			t.Errorf("attempt %d sent %d bytes, first attempt sent %d", i+1, n, lengths[0])
		}
	}
}

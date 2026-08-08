package otelgs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// recorder installs a fresh span recorder as the global tracer provider and
// returns the spans produced by fn.
func recorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	sr := tracetest.NewSpanRecorder()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr)))
	return sr
}

func personalEmail() gsmail.Email {
	return gsmail.Email{
		From:     "alice.sender@example.com",
		To:       []string{"bob.recipient@example.com", "carol@example.com"},
		Cc:       []string{"dave@example.com"},
		Bcc:      []string{"erin@example.com"},
		Subject:  "Your invoice for March",
		Body:     []byte("body text"),
		HTMLBody: []byte("<p>body</p>"),
	}
}

// attrsOf flattens a span's attributes into name -> printed value.
func attrsOf(span sdktrace.ReadOnlySpan) map[string]string {
	out := map[string]string{}
	for _, kv := range span.Attributes() {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

type stubSender struct {
	gsmail.BaseProvider
	err error
}

func (s *stubSender) Send(context.Context, gsmail.Email) error { return s.err }
func (s *stubSender) Ping(context.Context) error               { return nil }

// The default interceptor must not put personal data into traces. Traces are
// retained as long as logs, and gsmail.LoggerInterceptor makes the same
// promise; the two must not disagree.
func TestSendInterceptorRecordsNoPersonalData(t *testing.T) {
	sr := recorder(t)

	wrapped := gsmail.WrapSender(&stubSender{}, SendInterceptor())
	if err := wrapped.Send(context.Background(), personalEmail()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	span := spans[0]

	// Nothing anywhere in the span may carry an address or the subject.
	haystack := span.Name()
	for k, v := range attrsOf(span) {
		haystack += " " + k + "=" + v
	}
	for _, secret := range []string{
		"alice.sender@example.com", "bob.recipient@example.com",
		"carol@example.com", "dave@example.com", "erin@example.com",
		"Your invoice for March",
	} {
		if strings.Contains(haystack, secret) {
			t.Errorf("personal data leaked into the span: %q\nspan: %s", secret, haystack)
		}
	}

	attrs := attrsOf(span)
	if attrs["email.recipients"] != "4" {
		t.Errorf("email.recipients = %q, want 4 (2 To + 1 Cc + 1 Bcc)", attrs["email.recipients"])
	}
	if attrs["email.body_bytes"] == "" {
		t.Error("email.body_bytes should be recorded; it carries no personal data")
	}
}

// The verbose variant is the documented opt-in for teams whose retention and
// access controls allow it.
func TestVerboseSendInterceptorRecordsPersonalData(t *testing.T) {
	sr := recorder(t)

	wrapped := gsmail.WrapSender(&stubSender{}, VerboseSendInterceptor())
	if err := wrapped.Send(context.Background(), personalEmail()); err != nil {
		t.Fatalf("Send: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	attrs := attrsOf(spans[0])

	if attrs["email.from"] != "alice.sender@example.com" {
		t.Errorf("email.from = %q", attrs["email.from"])
	}
	if attrs["email.subject"] != "Your invoice for March" {
		t.Errorf("email.subject = %q", attrs["email.subject"])
	}
	if !strings.Contains(attrs["email.to"], "bob.recipient@example.com") {
		t.Errorf("email.to = %q", attrs["email.to"])
	}
}

// A failed send must mark the span itself as errored, not merely attach an
// event. Backends that surface failures key off the status code.
func TestSpanStatusReflectsOutcome(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		sr := recorder(t)
		sendErr := errors.New("upstream refused")

		wrapped := gsmail.WrapSender(&stubSender{err: sendErr}, SendInterceptor())
		if err := wrapped.Send(context.Background(), personalEmail()); !errors.Is(err, sendErr) {
			t.Fatalf("Send returned %v, want the underlying error", err)
		}

		span := sr.Ended()[0]
		if span.Status().Code != codes.Error {
			t.Errorf("span status = %v, want Error", span.Status().Code)
		}
		if len(span.Events()) == 0 {
			t.Error("the error should also be recorded as a span event")
		}
	})

	t.Run("success", func(t *testing.T) {
		sr := recorder(t)
		wrapped := gsmail.WrapSender(&stubSender{}, SendInterceptor())
		if err := wrapped.Send(context.Background(), personalEmail()); err != nil {
			t.Fatal(err)
		}
		if code := sr.Ended()[0].Status().Code; code != codes.Ok {
			t.Errorf("span status = %v, want Ok", code)
		}
	})
}

type stubReceiver struct {
	gsmail.BaseProvider
	emails []gsmail.Email
	err    error
}

func (r *stubReceiver) Receive(context.Context, int) ([]gsmail.Email, error) {
	return r.emails, r.err
}
func (r *stubReceiver) Search(context.Context, gsmail.SearchOptions, int) ([]gsmail.Email, error) {
	return nil, nil
}
func (r *stubReceiver) Idle(context.Context) (<-chan gsmail.Email, <-chan error) { return nil, nil }
func (r *stubReceiver) Ping(context.Context) error                               { return nil }

func TestReceiveInterceptor(t *testing.T) {
	t.Run("records the limit and the count", func(t *testing.T) {
		sr := recorder(t)

		rcv := &stubReceiver{emails: []gsmail.Email{personalEmail(), personalEmail()}}
		wrapped := gsmail.WrapReceiver(rcv, ReceiveInterceptor())

		got, err := wrapped.Receive(context.Background(), 25)
		if err != nil {
			t.Fatalf("Receive: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d emails, want 2", len(got))
		}

		attrs := attrsOf(sr.Ended()[0])
		if attrs["email.limit"] != "25" {
			t.Errorf("email.limit = %q, want 25", attrs["email.limit"])
		}
		if attrs["email.count"] != "2" {
			t.Errorf("email.count = %q, want 2", attrs["email.count"])
		}
	})

	t.Run("marks the span on failure and omits the count", func(t *testing.T) {
		sr := recorder(t)

		wrapped := gsmail.WrapReceiver(&stubReceiver{err: errors.New("imap down")}, ReceiveInterceptor())
		if _, err := wrapped.Receive(context.Background(), 5); err == nil {
			t.Fatal("expected an error")
		}

		span := sr.Ended()[0]
		if span.Status().Code != codes.Error {
			t.Errorf("span status = %v, want Error", span.Status().Code)
		}
		if _, present := attrsOf(span)["email.count"]; present {
			t.Error("email.count should not be set when the receive failed")
		}
	})

	t.Run("does not record received message contents", func(t *testing.T) {
		sr := recorder(t)

		wrapped := gsmail.WrapReceiver(&stubReceiver{emails: []gsmail.Email{personalEmail()}}, ReceiveInterceptor())
		if _, err := wrapped.Receive(context.Background(), 1); err != nil {
			t.Fatal(err)
		}

		for k, v := range attrsOf(sr.Ended()[0]) {
			if strings.Contains(v, "@example.com") || strings.Contains(v, "invoice") {
				t.Errorf("received message content leaked into attribute %s=%q", k, v)
			}
		}
	})
}

// The interceptor must pass the span's context down, or downstream spans are
// orphaned instead of nesting under the send.
func TestInterceptorPropagatesSpanContext(t *testing.T) {
	sr := recorder(t)

	var childSeen bool
	inner := gsmail.SendInterceptor(func(ctx context.Context, e gsmail.Email, next func(context.Context, gsmail.Email) error) error {
		_, span := otel.Tracer("child").Start(ctx, "child.work")
		span.End()
		childSeen = true
		return next(ctx, e)
	})

	wrapped := gsmail.WrapSender(&stubSender{}, SendInterceptor(), inner)
	if err := wrapped.Send(context.Background(), personalEmail()); err != nil {
		t.Fatal(err)
	}
	if !childSeen {
		t.Fatal("inner interceptor never ran")
	}

	spans := sr.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2 (parent send + child)", len(spans))
	}

	var parent, child sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == "child.work" {
			child = s
		} else {
			parent = s
		}
	}
	if child == nil || parent == nil {
		t.Fatal("expected one child.work span and one send span")
	}
	if child.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Error("the child span is not nested under the send span; ctx was not propagated")
	}
}

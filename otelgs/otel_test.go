package otelgs

import (
	"context"
	"testing"

	"github.com/gsoultan/gsmail"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type mockSender struct {
	gsmail.BaseProvider
}

func (m *mockSender) Send(ctx context.Context, email gsmail.Email) error {
	return nil
}
func (m *mockSender) Validate(ctx context.Context, email string) error { return nil }
func (m *mockSender) Ping(ctx context.Context) error                   { return nil }

func TestOTelInterceptor(t *testing.T) {
	// Setup OTel recorder
	sr := tracetest.NewSpanRecorder()
	tp := trace.NewTracerProvider(trace.WithSpanProcessor(sr))
	otel.SetTracerProvider(tp)

	sender := &mockSender{}
	wrapped := gsmail.WrapSender(sender, SendInterceptor())

	email := gsmail.Email{
		From:    "sender@example.com",
		To:      []string{"receiver@example.com"},
		Subject: "Test Subject",
	}

	err := wrapped.Send(context.Background(), email)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	spans := sr.Ended()
	if len(spans) != 1 {
		t.Fatalf("Expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.Name() != "gsmail.Send" {
		t.Errorf("Expected span name gsmail.Send, got %s", span.Name())
	}
}

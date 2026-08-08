package otelgs

import (
	"context"

	"github.com/gsoultan/gsmail"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/gsoultan/gsmail"
)

// SendInterceptor returns a SendInterceptor that records spans for email sends.
//
// It deliberately records no personal data: the sender, the recipient
// addresses and the subject line are omitted, because traces are usually
// retained far longer than the message itself. This mirrors
// gsmail.LoggerInterceptor. Use VerboseSendInterceptor if you accept that.
func SendInterceptor() gsmail.SendInterceptor {
	tracer := otel.Tracer(instrumentationName)
	return func(ctx context.Context, email gsmail.Email, next func(context.Context, gsmail.Email) error) error {
		ctx, span := tracer.Start(ctx, "gsmail.Send", trace.WithAttributes(
			attribute.Int("email.recipients", len(email.To)+len(email.Cc)+len(email.Bcc)),
			attribute.Int("email.attachments", len(email.Attachments)),
			attribute.Int("email.body_bytes", len(email.Body)+len(email.HTMLBody)),
		))
		defer span.End()

		return record(span, next(ctx, email))
	}
}

// VerboseSendInterceptor behaves like SendInterceptor but also records the
// sender, recipients and subject.
//
// Those fields are personal data. Only use this where your trace retention and
// access controls allow it.
func VerboseSendInterceptor() gsmail.SendInterceptor {
	tracer := otel.Tracer(instrumentationName)
	return func(ctx context.Context, email gsmail.Email, next func(context.Context, gsmail.Email) error) error {
		ctx, span := tracer.Start(ctx, "gsmail.Send", trace.WithAttributes(
			attribute.String("email.from", email.From),
			attribute.StringSlice("email.to", email.To),
			attribute.String("email.subject", email.Subject),
		))
		defer span.End()

		return record(span, next(ctx, email))
	}
}

// record attaches err to the span and marks the span itself as failed, so the
// error shows up in backends that key off span status rather than events.
func record(span trace.Span, err error) error {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	span.SetStatus(codes.Ok, "")
	return nil
}

// ReceiveInterceptor returns a ReceiveInterceptor that records spans for email receives.
func ReceiveInterceptor() gsmail.ReceiveInterceptor {
	tracer := otel.Tracer(instrumentationName)
	return func(ctx context.Context, limit int, next func(context.Context, int) ([]gsmail.Email, error)) ([]gsmail.Email, error) {
		ctx, span := tracer.Start(ctx, "gsmail.Receive", trace.WithAttributes(
			attribute.Int("email.limit", limit),
		))
		defer span.End()

		emails, err := next(ctx, limit)
		if err == nil {
			span.SetAttributes(attribute.Int("email.count", len(emails)))
		}
		return emails, record(span, err)
	}
}

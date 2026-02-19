package otelgs

import (
	"context"

	"github.com/gsoultan/gsmail"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/gsoultan/gsmail"
)

// SendInterceptor returns a SendInterceptor that records spans for email sends.
func SendInterceptor() gsmail.SendInterceptor {
	tracer := otel.Tracer(instrumentationName)
	return func(ctx context.Context, email gsmail.Email, next func(context.Context, gsmail.Email) error) error {
		ctx, span := tracer.Start(ctx, "gsmail.Send", trace.WithAttributes(
			attribute.String("email.from", email.From),
			attribute.StringSlice("email.to", email.To),
			attribute.String("email.subject", email.Subject),
		))
		defer span.End()

		err := next(ctx, email)
		if err != nil {
			span.RecordError(err)
		}
		return err
	}
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
		if err != nil {
			span.RecordError(err)
		} else {
			span.SetAttributes(attribute.Int("email.count", len(emails)))
		}
		return emails, err
	}
}

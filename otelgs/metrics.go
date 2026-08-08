package otelgs

import (
	"context"
	"time"

	"github.com/gsoultan/gsmail"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Tracing answers "what happened to this message". Metrics answer "is sending
// healthy right now", which is the question an alert is built on, and there
// was no way to ask it.
//
// Like the tracing interceptors, nothing here records personal data: an
// address or a subject as a metric attribute would be unbounded cardinality
// as well as a privacy problem.

// Metric names, exported so dashboards and alerts can refer to them without
// hardcoding strings that might drift.
const (
	MetricSendCount     = "gsmail.send.count"
	MetricSendDuration  = "gsmail.send.duration"
	MetricSendBytes     = "gsmail.send.bytes"
	MetricRecipients    = "gsmail.send.recipients"
	MetricReceiveCount  = "gsmail.receive.count"
	MetricReceivedTotal = "gsmail.receive.messages"
)

// Attribute keys used by the metrics above.
const (
	// AttrOutcome is "success" or "error".
	AttrOutcome = "outcome"
	// AttrErrorKind distinguishes a permanent failure from a retryable one,
	// which is the distinction that matters when deciding whether to page.
	AttrErrorKind = "error.kind"
)

// Metrics holds the instruments used by the interceptors. Build one with
// NewMetrics when you want to share instruments or use a non-global provider.
type Metrics struct {
	sendCount     metric.Int64Counter
	sendDuration  metric.Float64Histogram
	sendBytes     metric.Int64Histogram
	recipients    metric.Int64Histogram
	receiveCount  metric.Int64Counter
	receivedTotal metric.Int64Counter
}

// NewMetrics creates the instruments on the given meter. A nil meter uses the
// global provider.
func NewMetrics(m metric.Meter) (*Metrics, error) {
	if m == nil {
		m = otel.Meter(instrumentationName)
	}

	var (
		out Metrics
		err error
	)
	if out.sendCount, err = m.Int64Counter(MetricSendCount,
		metric.WithDescription("Messages submitted for delivery."),
		metric.WithUnit("{message}")); err != nil {
		return nil, err
	}
	if out.sendDuration, err = m.Float64Histogram(MetricSendDuration,
		metric.WithDescription("Time spent in Send, including retries."),
		metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if out.sendBytes, err = m.Int64Histogram(MetricSendBytes,
		metric.WithDescription("Body size of submitted messages."),
		metric.WithUnit("By")); err != nil {
		return nil, err
	}
	if out.recipients, err = m.Int64Histogram(MetricRecipients,
		metric.WithDescription("Recipients per submitted message."),
		metric.WithUnit("{recipient}")); err != nil {
		return nil, err
	}
	if out.receiveCount, err = m.Int64Counter(MetricReceiveCount,
		metric.WithDescription("Receive operations performed."),
		metric.WithUnit("{operation}")); err != nil {
		return nil, err
	}
	if out.receivedTotal, err = m.Int64Counter(MetricReceivedTotal,
		metric.WithDescription("Messages retrieved."),
		metric.WithUnit("{message}")); err != nil {
		return nil, err
	}
	return &out, nil
}

// SendMetricsInterceptor records send counts, durations and sizes.
//
// It uses the global meter provider. Use Metrics.SendInterceptor to supply
// your own. Instrument creation cannot fail meaningfully at this level, so a
// failure yields a no-op interceptor rather than a panic: metrics are not
// worth taking an application down for.
func SendMetricsInterceptor() gsmail.SendInterceptor {
	m, err := NewMetrics(nil)
	if err != nil {
		return func(ctx context.Context, email gsmail.Email, next func(context.Context, gsmail.Email) error) error {
			return next(ctx, email)
		}
	}
	return m.SendInterceptor()
}

// SendInterceptor returns an interceptor recording to these instruments.
func (m *Metrics) SendInterceptor() gsmail.SendInterceptor {
	return func(ctx context.Context, email gsmail.Email, next func(context.Context, gsmail.Email) error) error {
		start := time.Now()
		err := next(ctx, email)
		elapsed := time.Since(start).Seconds()

		attrs := outcomeAttrs(err)
		set := metric.WithAttributes(attrs...)

		m.sendCount.Add(ctx, 1, set)
		m.sendDuration.Record(ctx, elapsed, set)
		m.sendBytes.Record(ctx, int64(len(email.Body)+len(email.HTMLBody)), set)
		m.recipients.Record(ctx, int64(len(email.To)+len(email.Cc)+len(email.Bcc)), set)

		return err
	}
}

// ReceiveMetricsInterceptor records receive counts using the global meter.
func ReceiveMetricsInterceptor() gsmail.ReceiveInterceptor {
	m, err := NewMetrics(nil)
	if err != nil {
		return func(ctx context.Context, limit int, next func(context.Context, int) ([]gsmail.Email, error)) ([]gsmail.Email, error) {
			return next(ctx, limit)
		}
	}
	return m.ReceiveInterceptor()
}

// ReceiveInterceptor returns an interceptor recording to these instruments.
func (m *Metrics) ReceiveInterceptor() gsmail.ReceiveInterceptor {
	return func(ctx context.Context, limit int, next func(context.Context, int) ([]gsmail.Email, error)) ([]gsmail.Email, error) {
		emails, err := next(ctx, limit)

		set := metric.WithAttributes(outcomeAttrs(err)...)
		m.receiveCount.Add(ctx, 1, set)
		if err == nil {
			m.receivedTotal.Add(ctx, int64(len(emails)), set)
		}
		return emails, err
	}
}

// outcomeAttrs classifies the result. A permanent failure and a transient one
// need separate series: the first means a message will never be delivered, the
// second usually means wait.
func outcomeAttrs(err error) []attribute.KeyValue {
	if err == nil {
		return []attribute.KeyValue{attribute.String(AttrOutcome, "success")}
	}
	kind := "permanent"
	if gsmail.IsRetryable(err) {
		kind = "retryable"
	}
	return []attribute.KeyValue{
		attribute.String(AttrOutcome, "error"),
		attribute.String(AttrErrorKind, kind),
	}
}

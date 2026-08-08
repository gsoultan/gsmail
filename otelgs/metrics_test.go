package otelgs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gsoultan/gsmail"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// collect runs fn against a fresh in-memory meter and returns what was
// recorded.
func collect(t *testing.T, fn func(m *Metrics)) metricdata.ResourceMetrics {
	t.Helper()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))

	m, err := NewMetrics(provider.Meter("test"))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	fn(m)

	var out metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &out); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return out
}

// findMetric locates a metric by name across all scopes.
func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// sumFor totals an Int64 counter across data points matching every wanted
// attribute.
func sumFor(t *testing.T, rm metricdata.ResourceMetrics, name string, want map[string]string) int64 {
	t.Helper()

	m, ok := findMetric(rm, name)
	if !ok {
		t.Fatalf("metric %q was not recorded", name)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q is %T, want an Int64 sum", name, m.Data)
	}

	var total int64
	for _, dp := range sum.DataPoints {
		matches := true
		for k, v := range want {
			got, present := dp.Attributes.Value(attribute.Key(k))
			if !present || got.AsString() != v {
				matches = false
				break
			}
		}
		if matches {
			total += dp.Value
		}
	}
	return total
}

func TestSendMetricsRecordOutcomes(t *testing.T) {
	permanent := gsmail.NonRetryable(errors.New("invalid recipient"))
	transient := errors.New("connection reset")

	rm := collect(t, func(m *Metrics) {
		send := gsmail.WrapSender(&stubSender{}, m.SendInterceptor())
		for i := 0; i < 3; i++ {
			_ = send.Send(context.Background(), personalEmail())
		}

		failPermanent := gsmail.WrapSender(&stubSender{err: permanent}, m.SendInterceptor())
		_ = failPermanent.Send(context.Background(), personalEmail())

		failTransient := gsmail.WrapSender(&stubSender{err: transient}, m.SendInterceptor())
		_ = failTransient.Send(context.Background(), personalEmail())
		_ = failTransient.Send(context.Background(), personalEmail())
	})

	if got := sumFor(t, rm, MetricSendCount, map[string]string{AttrOutcome: "success"}); got != 3 {
		t.Errorf("successful sends = %d, want 3", got)
	}

	// A permanent failure and a transient one need separate series: one means
	// the message will never be delivered, the other usually means wait.
	if got := sumFor(t, rm, MetricSendCount, map[string]string{
		AttrOutcome: "error", AttrErrorKind: "permanent",
	}); got != 1 {
		t.Errorf("permanent failures = %d, want 1", got)
	}
	if got := sumFor(t, rm, MetricSendCount, map[string]string{
		AttrOutcome: "error", AttrErrorKind: "retryable",
	}); got != 2 {
		t.Errorf("retryable failures = %d, want 2", got)
	}
}

func TestSendMetricsRecordSizeAndRecipients(t *testing.T) {
	rm := collect(t, func(m *Metrics) {
		send := gsmail.WrapSender(&stubSender{}, m.SendInterceptor())
		_ = send.Send(context.Background(), personalEmail())
	})

	for _, name := range []string{MetricSendDuration, MetricSendBytes, MetricRecipients} {
		if _, ok := findMetric(rm, name); !ok {
			t.Errorf("metric %q was not recorded", name)
		}
	}

	m, _ := findMetric(rm, MetricRecipients)
	hist, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("%s is %T, want an Int64 histogram", MetricRecipients, m.Data)
	}
	if len(hist.DataPoints) == 0 || hist.DataPoints[0].Sum != 4 {
		t.Errorf("recipient count = %v, want 4 (2 To + 1 Cc + 1 Bcc)", hist.DataPoints)
	}
}

// Metric attributes must not carry addresses or subjects: that is unbounded
// cardinality as well as a privacy problem.
func TestMetricsRecordNoPersonalData(t *testing.T) {
	rm := collect(t, func(m *Metrics) {
		send := gsmail.WrapSender(&stubSender{}, m.SendInterceptor())
		_ = send.Send(context.Background(), personalEmail())

		recv := gsmail.WrapReceiver(&stubReceiver{emails: []gsmail.Email{personalEmail()}}, m.ReceiveInterceptor())
		_, _ = recv.Receive(context.Background(), 5)
	})

	var haystack strings.Builder
	for _, scope := range rm.ScopeMetrics {
		for _, metricEntry := range scope.Metrics {
			haystack.WriteString(metricEntry.Name)
			switch data := metricEntry.Data.(type) {
			case metricdata.Sum[int64]:
				for _, dp := range data.DataPoints {
					haystack.WriteString(dp.Attributes.Encoded(nil))
				}
			case metricdata.Histogram[int64]:
				for _, dp := range data.DataPoints {
					haystack.WriteString(dp.Attributes.Encoded(nil))
				}
			case metricdata.Histogram[float64]:
				for _, dp := range data.DataPoints {
					haystack.WriteString(dp.Attributes.Encoded(nil))
				}
			}
		}
	}

	for _, secret := range []string{
		"alice.sender@example.com", "bob.recipient@example.com",
		"carol@example.com", "dave@example.com", "erin@example.com",
		"Your invoice for March",
	} {
		if strings.Contains(haystack.String(), secret) {
			t.Errorf("personal data leaked into metric attributes: %q", secret)
		}
	}
}

func TestReceiveMetrics(t *testing.T) {
	rm := collect(t, func(m *Metrics) {
		recv := gsmail.WrapReceiver(
			&stubReceiver{emails: []gsmail.Email{personalEmail(), personalEmail(), personalEmail()}},
			m.ReceiveInterceptor())
		_, _ = recv.Receive(context.Background(), 10)

		failing := gsmail.WrapReceiver(&stubReceiver{err: errors.New("imap down")}, m.ReceiveInterceptor())
		_, _ = failing.Receive(context.Background(), 10)
	})

	if got := sumFor(t, rm, MetricReceiveCount, map[string]string{AttrOutcome: "success"}); got != 1 {
		t.Errorf("successful receives = %d, want 1", got)
	}
	if got := sumFor(t, rm, MetricReceiveCount, map[string]string{AttrOutcome: "error"}); got != 1 {
		t.Errorf("failed receives = %d, want 1", got)
	}
	if got := sumFor(t, rm, MetricReceivedTotal, map[string]string{AttrOutcome: "success"}); got != 3 {
		t.Errorf("messages retrieved = %d, want 3", got)
	}
}

// A failed receive must not contribute a message count, or the "messages
// retrieved" series silently under-reports against its own operation count.
func TestFailedReceiveRecordsNoMessages(t *testing.T) {
	rm := collect(t, func(m *Metrics) {
		recv := gsmail.WrapReceiver(&stubReceiver{err: errors.New("down")}, m.ReceiveInterceptor())
		_, _ = recv.Receive(context.Background(), 10)
	})

	if _, ok := findMetric(rm, MetricReceivedTotal); ok {
		if got := sumFor(t, rm, MetricReceivedTotal, nil); got != 0 {
			t.Errorf("messages recorded on a failed receive = %d, want 0", got)
		}
	}
}

// The global-provider constructors must work without configuration.
func TestGlobalInterceptorsAreUsable(t *testing.T) {
	send := gsmail.WrapSender(&stubSender{}, SendMetricsInterceptor())
	if err := send.Send(context.Background(), personalEmail()); err != nil {
		t.Errorf("Send: %v", err)
	}

	recv := gsmail.WrapReceiver(&stubReceiver{}, ReceiveMetricsInterceptor())
	if _, err := recv.Receive(context.Background(), 5); err != nil {
		t.Errorf("Receive: %v", err)
	}
}

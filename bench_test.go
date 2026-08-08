package gsmail

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func benchEmail(nHeaders, bodyKB int) Email {
	e := Email{
		From:    "Sender Name <sender@example.com>",
		To:      []string{"a@example.com", "b@example.com"},
		Cc:      []string{"c@example.com"},
		Subject: "Benchmark subject line",
		Body:    []byte(strings.Repeat("x", bodyKB*1024)),
	}
	for i := 0; i < nHeaders; i++ {
		e.SetHeader(fmt.Sprintf("X-Custom-Header-%02d", i), "some reasonably long header value here")
	}
	return e
}

func BenchmarkRenderMessage_NoCustomHeaders(b *testing.B) {
	e := benchEmail(0, 4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := RenderMessage(e); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkWithMessage_NoCustomHeaders(b *testing.B) {
	e := benchEmail(0, 4)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := WithMessage(e, func(msg []byte) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

// Header count is the axis to watch: writeHeader consults HasHeader, which
// rescans the whole accumulated buffer each time.
func BenchmarkWithMessage_HeaderScaling(b *testing.B) {
	for _, n := range []int{0, 4, 8, 16, 32, 64} {
		e := benchEmail(n, 1)
		b.Run(fmt.Sprintf("headers=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := WithMessage(e, func(msg []byte) error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkWithMessage_BodyScaling(b *testing.B) {
	for _, kb := range []int{1, 16, 256} {
		e := benchEmail(4, kb)
		b.Run(fmt.Sprintf("body=%dKB", kb), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(kb * 1024))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := WithMessage(e, func(msg []byte) error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkHasHeader(b *testing.B) {
	e := benchEmail(16, 1)
	raw, err := RenderMessage(e)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = HasHeader(raw, "Content-Type")
	}
}

var retrySink int

func BenchmarkGetRetryConfig(b *testing.B) {
	var p BaseProvider
	p.SetRetryConfig(DefaultRetryConfig())
	n := 0
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n += p.GetRetryConfig().MaxRetries
	}
	retrySink = n
}

// Every Send calls GetRetryConfig; under fan-out the RWMutex read lock is a
// shared cache line.
func BenchmarkGetRetryConfigParallel(b *testing.B) {
	var p BaseProvider
	p.SetRetryConfig(DefaultRetryConfig())
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		n := 0
		for pb.Next() {
			n += p.GetRetryConfig().MaxRetries
		}
		atomic.AddInt64(&retryParallelSink, int64(n))
	})
}

func BenchmarkFormatAddresses(b *testing.B) {
	addrs := []string{"Alice <a@example.com>", "b@example.com", "Carol Smith <c@example.com>"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = FormatAddresses(addrs)
	}
}

func BenchmarkEncodeHeaderASCII(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = encodeHeader("A perfectly ordinary ASCII subject line")
	}
}

func BenchmarkSanitizeHeaderValueClean(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = sanitizeHeaderValue("A perfectly ordinary ASCII subject line")
	}
}

func BenchmarkWriteMIMEBase64(b *testing.B) {
	data := []byte(strings.Repeat("x", 64*1024))
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bufPtr := getBuffer()
		if err := writeMIMEBase64(newBufferWriter(bufPtr), data); err != nil {
			b.Fatal(err)
		}
		putBuffer(bufPtr)
	}
}

func BenchmarkRenderMessageParallel(b *testing.B) {
	e := benchEmail(4, 4)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := WithMessage(e, func(msg []byte) error { return nil }); err != nil {
				b.Fatal(err)
			}
		}
	})
}

var retryParallelSink int64

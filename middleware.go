package gsmail

import (
	"context"
	"fmt"
	"time"
)

// SendInterceptor is a function that intercepts the Send call.
type SendInterceptor func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error

// ReceiveInterceptor is a function that intercepts the Receive call.
type ReceiveInterceptor func(ctx context.Context, limit int, next func(ctx context.Context, limit int) ([]Email, error)) ([]Email, error)

// WrapSender wraps a Sender with one or more SendInterceptors.
func WrapSender(s Sender, interceptors ...SendInterceptor) Sender {
	for i := len(interceptors) - 1; i >= 0; i-- {
		s = &interceptedSender{
			Sender:      s,
			interceptor: interceptors[i],
		}
	}
	return s
}

// WrapReceiver wraps a Receiver with one or more ReceiveInterceptors.
func WrapReceiver(r Receiver, interceptors ...ReceiveInterceptor) Receiver {
	for i := len(interceptors) - 1; i >= 0; i-- {
		r = &interceptedReceiver{
			Receiver:    r,
			interceptor: interceptors[i],
		}
	}
	return r
}

type interceptedSender struct {
	Sender
	interceptor SendInterceptor
}

func (s *interceptedSender) Send(ctx context.Context, email Email) error {
	return s.interceptor(ctx, email, s.Sender.Send)
}

type interceptedReceiver struct {
	Receiver
	interceptor ReceiveInterceptor
}

func (r *interceptedReceiver) Receive(ctx context.Context, limit int) ([]Email, error) {
	return r.interceptor(ctx, limit, r.Receiver.Receive)
}

func (r *interceptedReceiver) Search(ctx context.Context, options SearchOptions, limit int) ([]Email, error) {
	return r.Receiver.Search(ctx, options, limit)
}

// --- Built-in Interceptors ---

// LoggerInterceptor returns a simple logging interceptor.
func LoggerInterceptor(logFn func(string, ...any)) SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error {
		start := time.Now()
		err := next(ctx, email)
		logFn("Send email: from=%s to=%v subject=%q duration=%v err=%v",
			email.From, email.To, email.Subject, time.Since(start), err)
		return err
	}
}

// RecoveryInterceptor returns an interceptor that recovers from panics.
func RecoveryInterceptor() SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic in sender: %v", r)
			}
		}()
		return next(ctx, email)
	}
}

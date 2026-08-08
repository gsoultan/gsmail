package gsmail

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"
)

// SendInterceptor is a function that intercepts the Send call.
type SendInterceptor func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error

// ReceiveInterceptor is a function that intercepts the Receive call.
type ReceiveInterceptor func(ctx context.Context, limit int, next func(ctx context.Context, limit int) ([]Email, error)) ([]Email, error)

// SearchInterceptor is a function that intercepts the Search call.
type SearchInterceptor func(ctx context.Context, options SearchOptions, limit int, next func(ctx context.Context, options SearchOptions, limit int) ([]Email, error)) ([]Email, error)

// IdleInterceptor is a function that intercepts the Idle call.
type IdleInterceptor func(ctx context.Context, next func(ctx context.Context) (<-chan Email, <-chan error)) (<-chan Email, <-chan error)

// ReceiverInterceptors bundles the hooks WrapReceiverWith can install.
// A nil field leaves that method untouched.
type ReceiverInterceptors struct {
	Receive ReceiveInterceptor
	Search  SearchInterceptor
	Idle    IdleInterceptor
}

// WrapSender wraps a Sender with one or more SendInterceptors.
// The first interceptor is the outermost one.
func WrapSender(s Sender, interceptors ...SendInterceptor) Sender {
	for i := len(interceptors) - 1; i >= 0; i-- {
		if interceptors[i] == nil {
			continue
		}
		s = &interceptedSender{
			Sender:      s,
			interceptor: interceptors[i],
		}
	}
	return s
}

// WrapReceiver wraps a Receiver with one or more ReceiveInterceptors.
// Only Receive is intercepted; use WrapReceiverWith to also hook Search or Idle.
func WrapReceiver(r Receiver, interceptors ...ReceiveInterceptor) Receiver {
	for i := len(interceptors) - 1; i >= 0; i-- {
		if interceptors[i] == nil {
			continue
		}
		r = &interceptedReceiver{
			Receiver: r,
			receive:  interceptors[i],
		}
	}
	return r
}

// WrapReceiverWith wraps a Receiver, intercepting every method for which an
// interceptor is supplied.
func WrapReceiverWith(r Receiver, interceptors ReceiverInterceptors) Receiver {
	if interceptors.Receive == nil && interceptors.Search == nil && interceptors.Idle == nil {
		return r
	}
	return &interceptedReceiver{
		Receiver: r,
		receive:  interceptors.Receive,
		search:   interceptors.Search,
		idle:     interceptors.Idle,
	}
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
	receive ReceiveInterceptor
	search  SearchInterceptor
	idle    IdleInterceptor
}

func (r *interceptedReceiver) Receive(ctx context.Context, limit int) ([]Email, error) {
	if r.receive == nil {
		return r.Receiver.Receive(ctx, limit)
	}
	return r.receive(ctx, limit, r.Receiver.Receive)
}

func (r *interceptedReceiver) Search(ctx context.Context, options SearchOptions, limit int) ([]Email, error) {
	if r.search == nil {
		return r.Receiver.Search(ctx, options, limit)
	}
	return r.search(ctx, options, limit, r.Receiver.Search)
}

func (r *interceptedReceiver) Idle(ctx context.Context) (<-chan Email, <-chan error) {
	if r.idle == nil {
		return r.Receiver.Idle(ctx)
	}
	return r.idle(ctx, r.Receiver.Idle)
}

// --- Built-in Interceptors ---

// LoggerInterceptor returns a logging interceptor.
//
// It deliberately records no personal data: recipient addresses and the
// subject line are omitted, because logs are usually retained far longer than
// the message itself. Use VerboseLoggerInterceptor if you accept that.
func LoggerInterceptor(logFn func(string, ...any)) SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error {
		start := time.Now()
		err := next(ctx, email)
		logFn("gsmail: send recipients=%d attachments=%d bytes=%d duration=%v err=%v",
			len(email.To)+len(email.Cc)+len(email.Bcc),
			len(email.Attachments),
			len(email.Body)+len(email.HTMLBody),
			time.Since(start), err)
		return err
	}
}

// VerboseLoggerInterceptor returns a logging interceptor that includes the
// sender, recipients and subject.
//
// Those fields are personal data. Only use this where your log retention and
// access controls allow it.
func VerboseLoggerInterceptor(logFn func(string, ...any)) SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) error {
		start := time.Now()
		err := next(ctx, email)
		logFn("gsmail: send from=%s to=%v subject=%q duration=%v err=%v",
			email.From, email.To, email.Subject, time.Since(start), err)
		return err
	}
}

// RecoveryInterceptor returns an interceptor that converts a panic in the
// underlying sender into an error. The stack trace is attached to the error so
// the panic site is not lost.
func RecoveryInterceptor() SendInterceptor {
	return RecoveryInterceptorWithLogger(nil)
}

// RecoveryInterceptorWithLogger behaves like RecoveryInterceptor and, when
// logFn is non-nil, also logs the panic value together with its stack trace.
func RecoveryInterceptorWithLogger(logFn func(string, ...any)) SendInterceptor {
	return func(ctx context.Context, email Email, next func(ctx context.Context, email Email) error) (err error) {
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				if logFn != nil {
					logFn("gsmail: panic in sender: %v\n%s", r, stack)
				}
				err = NonRetryable(fmt.Errorf("gsmail: panic in sender: %v\n%s", r, stack))
			}
		}()
		return next(ctx, email)
	}
}

package gsmail_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
)

func TestRetry(t *testing.T) {
	t.Run("SuccessFirstTry", func(t *testing.T) {
		config := gsmail.RetryConfig{
			MaxRetries:      3,
			InitialInterval: 1 * time.Millisecond,
			MaxInterval:     10 * time.Millisecond,
			Multiplier:      2.0,
		}

		calls := 0
		err := gsmail.Retry(context.Background(), config, func() error {
			calls++
			return nil
		})

		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 call, got %d", calls)
		}
	})

	t.Run("SuccessAfterRetries", func(t *testing.T) {
		config := gsmail.RetryConfig{
			MaxRetries:      3,
			InitialInterval: 1 * time.Millisecond,
			MaxInterval:     10 * time.Millisecond,
			Multiplier:      2.0,
		}

		calls := 0
		err := gsmail.Retry(context.Background(), config, func() error {
			calls++
			if calls < 3 {
				return errors.New("temporary error")
			}
			return nil
		})

		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
		if calls != 3 {
			t.Errorf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("FailureAllRetries", func(t *testing.T) {
		config := gsmail.RetryConfig{
			MaxRetries:      2,
			InitialInterval: 1 * time.Millisecond,
			MaxInterval:     10 * time.Millisecond,
			Multiplier:      2.0,
		}

		calls := 0
		expectedErr := errors.New("permanent error")
		err := gsmail.Retry(context.Background(), config, func() error {
			calls++
			return expectedErr
		})

		if !errors.Is(err, expectedErr) {
			t.Errorf("expected error %v, got %v", expectedErr, err)
		}
		if calls != 3 { // Initial try + 2 retries
			t.Errorf("expected 3 calls, got %d", calls)
		}
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		config := gsmail.RetryConfig{
			MaxRetries:      10,
			InitialInterval: 100 * time.Millisecond,
			MaxInterval:     1 * time.Second,
			Multiplier:      2.0,
		}

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		err := gsmail.Retry(ctx, config, func() error {
			return errors.New("temporary error")
		})

		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected error context.Canceled, got %v", err)
		}
	})
}

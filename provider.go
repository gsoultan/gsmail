package gsmail

import (
	"context"
	"time"
)

// RetryConfig defines the configuration for connection retries.
type RetryConfig struct {
	MaxRetries      int           // Maximum number of retries.
	InitialInterval time.Duration // Initial interval between retries.
	MaxInterval     time.Duration // Maximum interval between retries.
	Multiplier      float64       // Exponential backoff multiplier.
}

// DefaultRetryConfig returns a default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:      3,
		InitialInterval: 500 * time.Millisecond,
		MaxInterval:     5 * time.Second,
		Multiplier:      2.0,
	}
}

// Sender defines the interface for different email delivery methods.
type Sender interface {
	Send(ctx context.Context, email Email) error
	Validate(ctx context.Context, email string) error
	Ping(ctx context.Context) error
	SetRetryConfig(config RetryConfig)
}

// Receiver defines the interface for different email receiving methods.
type Receiver interface {
	Receive(ctx context.Context, limit int) ([]Email, error)
	Validate(ctx context.Context, email string) error
	Ping(ctx context.Context) error
	SetRetryConfig(config RetryConfig)
}

// BaseProvider implements common logic for all providers.
type BaseProvider struct {
	RetryConfig RetryConfig
}

// SetRetryConfig sets the retry configuration for the provider.
func (p *BaseProvider) SetRetryConfig(config RetryConfig) {
	p.RetryConfig = config
}

// GetRetryConfig returns the retry configuration, or default if not set.
func (p *BaseProvider) GetRetryConfig() RetryConfig {
	if p.RetryConfig.MaxRetries <= 0 {
		return DefaultRetryConfig()
	}
	return p.RetryConfig
}

// Retry executes the given function with retries based on the provided configuration.
func Retry(ctx context.Context, config RetryConfig, fn func() error) error {
	var lastErr error
	interval := config.InitialInterval

	for i := 0; i <= config.MaxRetries; i++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := fn(); err == nil {
			return nil
		} else {
			lastErr = err
		}

		if i == config.MaxRetries {
			break
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}

		interval = time.Duration(float64(interval) * config.Multiplier)
		if interval > config.MaxInterval {
			interval = config.MaxInterval
		}
	}

	return lastErr
}

// Validate performs comprehensive email validation: format check, disposable/temporary domain rejection, and existence verification (MX lookup + SMTP RCPT).
func (p *BaseProvider) Validate(ctx context.Context, email string) error {
	return ValidateEmailExistence(ctx, email)
}

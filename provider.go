package gsmail

import "context"

// Sender defines the interface for different email delivery methods.
type Sender interface {
	Send(ctx context.Context, email Email) error
	Validate(ctx context.Context, email string) error
	Ping(ctx context.Context) error
}

// Receiver defines the interface for different email receiving methods.
type Receiver interface {
	Receive(ctx context.Context, limit int) ([]Email, error)
	Validate(ctx context.Context, email string) error
	Ping(ctx context.Context) error
}

// BaseProvider implements the Validate method common to all providers.
type BaseProvider struct{}

// Validate checks if the email address exists using the ValidateEmailExistence utility.
func (BaseProvider) Validate(ctx context.Context, email string) error {
	return ValidateEmailExistence(ctx, email)
}

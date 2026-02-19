package pop3

import (
	"context"
	"encoding/base64"
	"fmt"

	sasl "github.com/emersion/go-sasl"
	"github.com/gsoultan/gsmail"
	gopop3 "github.com/knadh/go-pop3"
)

// Receiver represents the POP3 server configuration and implements the Receiver interface.
type Receiver struct {
	gsmail.BaseProvider
	Host               string
	Port               int
	Username           string
	Password           string
	SSL                bool
	InsecureSkipVerify bool

	// Modern auth (POP3 XOAUTH2 is not supported by this client)
	AuthMethod        gsmail.AuthMethod
	TokenSource       gsmail.TokenSource
	AllowInsecureAuth bool
}

// NewReceiver creates a new POP3 receiver.
func NewReceiver(host string, port int, username, password string, ssl bool) *Receiver {
	return &Receiver{
		Host:               host,
		Port:               port,
		Username:           username,
		Password:           password,
		SSL:                ssl,
		InsecureSkipVerify: false,
	}
}

// Ping checks the connection to the POP3 server.
func (f *Receiver) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		p := gopop3.New(gopop3.Opt{
			Host:       f.Host,
			Port:       f.Port,
			TLSEnabled: f.SSL,
		})

		conn, err := p.NewConn()
		if err != nil {
			return fmt.Errorf("pop3 dial: %w", err)
		}
		defer func() { _ = conn.Quit() }()

		if err := conn.Noop(); err != nil {
			return fmt.Errorf("pop3 noop: %w", err)
		}

		return nil
	})
}

// Search is not supported by POP3.
func (f *Receiver) Search(ctx context.Context, options gsmail.SearchOptions, limit int) ([]gsmail.Email, error) {
	return nil, fmt.Errorf("search not supported by POP3")
}

// Idle is not supported by POP3.
func (f *Receiver) Idle(ctx context.Context) (<-chan gsmail.Email, <-chan error) {
	emailChan := make(chan gsmail.Email)
	errChan := make(chan error, 1)
	close(emailChan)
	errChan <- fmt.Errorf("idle not supported by POP3")
	close(errChan)
	return emailChan, errChan
}

// Receive retrieves emails using POP3.
func (f *Receiver) Receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
	var emails []gsmail.Email
	err := gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		var err error
		emails, err = f.receive(ctx, limit)
		return err
	})
	return emails, err
}

func (f *Receiver) receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
	p := gopop3.New(gopop3.Opt{
		Host:       f.Host,
		Port:       f.Port,
		TLSEnabled: f.SSL,
	})

	conn, err := p.NewConn()
	if err != nil {
		return nil, fmt.Errorf("pop3 dial: %w", err)
	}
	defer func() { _ = conn.Quit() }()

	// Authenticate
	if f.AuthMethod == gsmail.AuthXOAUTH2 || f.AuthMethod == gsmail.AuthOAUTHBEARER {
		if !f.SSL && !f.AllowInsecureAuth {
			return nil, fmt.Errorf("pop3 oauth2 requires TLS; enable SSL or AllowInsecureAuth for testing")
		}
		if f.TokenSource == nil {
			return nil, fmt.Errorf("pop3 oauth2 token source is nil")
		}
		tok, err := f.TokenSource(ctx)
		if err != nil {
			return nil, fmt.Errorf("pop3 token source: %w", err)
		}

		var authClient sasl.Client
		if f.AuthMethod == gsmail.AuthXOAUTH2 {
			authClient = gsmail.NewXOAUTH2Client(f.Username, tok)
		} else {
			authClient = sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: f.Username, Token: tok})
		}

		mech, ir, err := authClient.Start()
		if err != nil {
			return nil, fmt.Errorf("sasl start: %w", err)
		}

		ir64 := base64.StdEncoding.EncodeToString(ir)
		// POP3 AUTH mechanism [initial-response]
		// Use false for isMulti as AUTH mechanism is not expected to return multiline response
		_, err = conn.Cmd("AUTH", false, mech, ir64)
		if err != nil {
			return nil, fmt.Errorf("pop3 authenticate: %w", err)
		}
	} else {
		if err := conn.Auth(f.Username, f.Password); err != nil {
			return nil, fmt.Errorf("pop3 auth: %w", err)
		}
	}

	count, _, err := conn.Stat()
	if err != nil {
		return nil, fmt.Errorf("pop3 stat: %w", err)
	}

	if count == 0 {
		return nil, nil
	}

	start := count
	end := count - limit + 1
	if end < 1 {
		end = 1
	}

	emails := make([]gsmail.Email, 0, start-end+1)
	for i := start; i >= end; i-- {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return emails, ctx.Err()
		default:
		}

		// RetrRaw returns a *bytes.Buffer.
		// POP3 connections are sequential and not thread-safe.
		buf, err := conn.RetrRaw(i)
		if err != nil {
			return emails, fmt.Errorf("pop3 retr %d: %w", i, err)
		}

		email, err := gsmail.ParseRawEmail(buf.Bytes())
		if err != nil {
			continue
		}
		emails = append(emails, email)
	}

	return emails, nil
}

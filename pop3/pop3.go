package pop3

import (
	"context"
	"fmt"
	"sync"

	"github.com/gsoultan/gsmail"
	gopop3 "github.com/knadh/go-pop3"
)

// Receiver represents the POP3 server configuration.
type Receiver struct {
	gsmail.BaseProvider
	Host     string
	Port     int
	Username string
	Password string
	SSL      bool
}

// NewReceiver creates a new POP3 receiver.
func NewReceiver(host string, port int, username, password string, ssl bool) *Receiver {
	return &Receiver{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		SSL:      ssl,
	}
}

// Receive retrieves emails using POP3.
func (f *Receiver) Receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
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

	if err := conn.Auth(f.Username, f.Password); err != nil {
		return nil, fmt.Errorf("pop3 auth: %w", err)
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

	type result struct {
		index int
		email gsmail.Email
		err   error
	}

	results := make(chan result, start-end+1)
	var wg sync.WaitGroup

	for i := start; i >= end; i-- {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Check context cancellation
			select {
			case <-ctx.Done():
				return
			default:
			}

			// RetrRaw returns a *bytes.Buffer
			buf, err := conn.RetrRaw(idx)
			if err != nil {
				results <- result{err: fmt.Errorf("pop3 retr %d: %w", idx, err)}
				return
			}

			email, err := gsmail.ParseRawEmail(buf.Bytes())
			if err != nil {
				return
			}
			results <- result{index: idx, email: email}
		}(i)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	emailsMap := make(map[int]gsmail.Email)
	var fetchErr error
	for res := range results {
		if res.err != nil {
			fetchErr = res.err
		} else {
			emailsMap[res.index] = res.email
		}
	}

	emails := make([]gsmail.Email, 0, len(emailsMap))
	for i := start; i >= end; i-- {
		if email, ok := emailsMap[i]; ok {
			emails = append(emails, email)
		}
	}

	if fetchErr != nil {
		return emails, fetchErr
	}

	return emails, nil
}

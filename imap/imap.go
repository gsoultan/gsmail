package imap

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	goimap "github.com/emersion/go-imap"
	"github.com/emersion/go-imap/client"
	"github.com/gsoultan/gsmail"
)

// Receiver represents the IMAP server configuration.
type Receiver struct {
	gsmail.BaseProvider
	Host     string
	Port     int
	Username string
	Password string
	SSL      bool
}

// NewReceiver creates a new IMAP receiver.
func NewReceiver(host string, port int, username, password string, ssl bool) *Receiver {
	return &Receiver{
		Host:     host,
		Port:     port,
		Username: username,
		Password: password,
		SSL:      ssl,
	}
}

// Receive retrieves emails using IMAP.
func (f *Receiver) Receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
	addr := net.JoinHostPort(f.Host, fmt.Sprintf("%d", f.Port))

	var conn net.Conn
	var err error
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err = d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("imap dial: %w", err)
	}

	var c *client.Client
	if f.SSL {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: f.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("imap tls handshake: %w", err)
		}
		c, err = client.New(tlsConn)
		if err != nil {
			_ = tlsConn.Close()
			return nil, fmt.Errorf("imap client new: %w", err)
		}
	} else {
		c, err = client.New(conn)
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("imap client new: %w", err)
		}
	}

	defer func() { _ = c.Logout() }()

	if err := c.Login(f.Username, f.Password); err != nil {
		return nil, fmt.Errorf("imap login: %w", err)
	}

	mbox, err := c.Select("INBOX", false)
	if err != nil {
		return nil, fmt.Errorf("imap select inbox: %w", err)
	}

	if mbox.Messages == 0 {
		return nil, nil
	}

	start := mbox.Messages
	var end uint32 = 1
	if mbox.Messages > uint32(limit) {
		end = start - uint32(limit) + 1
	}

	seqset := new(goimap.SeqSet)
	seqset.AddRange(end, start)

	messages := make(chan *goimap.Message, limit)
	done := make(chan error, 1)
	go func() {
		done <- c.Fetch(seqset, []goimap.FetchItem{goimap.FetchRFC822}, messages)
	}()

	type result struct {
		index int
		email gsmail.Email
		err   error
	}
	results := make(chan result, limit)
	var wg sync.WaitGroup

	// Use a small worker pool or just spawn for each to process concurrently
	// Since limit is usually small, spawning per message is fine, but we should be careful.
	// For high performance, we'll use a semaphore-like approach or just processing as they come.

	count := 0
	for msg := range messages {
		wg.Add(1)
		go func(m *goimap.Message, idx int) {
			defer wg.Done()
			for _, literal := range m.Body {
				raw, err := io.ReadAll(literal)
				if err != nil {
					results <- result{err: fmt.Errorf("imap read body: %w", err)}
					return
				}
				email, err := gsmail.ParseRawEmail(raw)
				if err != nil {
					continue
				}
				results <- result{index: idx, email: email}
			}
		}(msg, count)
		count++
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

	if err := <-done; err != nil {
		if fetchErr != nil {
			fetchErr = fmt.Errorf("%v (fetch error: %w)", fetchErr, err)
		} else {
			fetchErr = fmt.Errorf("imap fetch error: %w", err)
		}
	}

	emails := make([]gsmail.Email, 0, len(emailsMap))
	for i := 0; i < count; i++ {
		if email, ok := emailsMap[i]; ok {
			emails = append(emails, email)
		}
	}

	if fetchErr != nil {
		return emails, fetchErr
	}

	// The map approach maintains order (sort of, by rebuilding it)
	// Newest first was handled by reversing before.
	// The original code reversed at the end.
	// Let's re-verify the reverse logic.

	// Reverse to have newest first
	for i, j := 0, len(emails)-1; i < j; i, j = i+1, j-1 {
		emails[i], emails[j] = emails[j], emails[i]
	}

	return emails, nil
}

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
	idle "github.com/emersion/go-imap-idle"
	"github.com/emersion/go-imap/client"
	sasl "github.com/emersion/go-sasl"
	"github.com/gsoultan/gsmail"
)

// Receiver represents the IMAP server configuration and implements the Receiver interface.
type Receiver struct {
	gsmail.BaseProvider
	Host               string
	Port               int
	Username           string
	Password           string
	SSL                bool
	InsecureSkipVerify bool

	// Modern auth
	AuthMethod        gsmail.AuthMethod
	TokenSource       gsmail.TokenSource
	AllowInsecureAuth bool

	// TLS configuration (optional)
	// CipherSuites restricts TLS 1.2 (and 1.1) cipher suites; nil uses default secure set.
	// TLS 1.3 cipher suites are not configurable in Go.
	CipherSuites []uint16
	// MinVersion is the minimum TLS version (e.g. tls.VersionTLS11, tls.VersionTLS12); 0 uses default (TLS 1.1).
	MinVersion uint16
	// MaxVersion is the maximum TLS version (e.g. tls.VersionTLS12); 0 means no limit (allows TLS 1.3).
	MaxVersion uint16
}

// NewReceiver creates a new IMAP receiver.
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

// defaultCipherSuites is the default secure set for TLS 1.1/1.2 (no RC4/3DES).
var defaultCipherSuites = []uint16{
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
	tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_RSA_WITH_AES_128_CBC_SHA,
	tls.TLS_RSA_WITH_AES_256_CBC_SHA,
}

// tlsConfig returns a TLS configuration. CipherSuites applies only to TLS 1.1/1.2;
// TLS 1.3 cipher suites are not configurable in Go.
func (f *Receiver) tlsConfig() *tls.Config {
	cipherSuites := f.CipherSuites
	if len(cipherSuites) == 0 {
		cipherSuites = defaultCipherSuites
	}
	minVer := f.MinVersion
	if minVer == 0 {
		minVer = tls.VersionTLS11
	}
	return &tls.Config{
		ServerName:         f.Host,
		MinVersion:         minVer,
		MaxVersion:         f.MaxVersion,
		CipherSuites:       cipherSuites,
		InsecureSkipVerify: f.InsecureSkipVerify,
	}
}

// Ping checks the connection to the IMAP server.
func (f *Receiver) Ping(ctx context.Context) error {
	return gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		c, _, err := f.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout() }()

		if err := c.Noop(); err != nil {
			return fmt.Errorf("imap noop: %w", err)
		}

		return nil
	})
}

func (f *Receiver) connect(ctx context.Context) (*client.Client, bool, error) {
	addr := net.JoinHostPort(f.Host, fmt.Sprintf("%d", f.Port))

	var conn net.Conn
	var err error
	d := net.Dialer{Timeout: 30 * time.Second}
	conn, err = d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, false, fmt.Errorf("imap dial: %w", err)
	}

	var c *client.Client
	var tlsOn bool
	if f.SSL {
		tlsConn := tls.Client(conn, f.tlsConfig())
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = tlsConn.Close()
			return nil, false, fmt.Errorf("imap tls handshake: %w", err)
		}
		c, err = client.New(tlsConn)
		if err != nil {
			_ = tlsConn.Close()
			return nil, false, fmt.Errorf("imap client new: %w", err)
		}
		tlsOn = true
	} else {
		c, err = client.New(conn)
		if err != nil {
			_ = conn.Close()
			return nil, false, fmt.Errorf("imap client new: %w", err)
		}

		// Try STARTTLS if not using SSL
		if ok, _ := c.SupportStartTLS(); ok {
			if err := c.StartTLS(f.tlsConfig()); err != nil {
				_ = c.Logout()
				return nil, false, fmt.Errorf("imap starttls: %w", err)
			}
			tlsOn = true
		}
	}
	return c, tlsOn, nil
}

func (f *Receiver) authenticate(ctx context.Context, c *client.Client, tlsOn bool) error {
	if f.AuthMethod == gsmail.AuthXOAUTH2 || f.AuthMethod == gsmail.AuthOAUTHBEARER {
		if !tlsOn && !f.AllowInsecureAuth {
			return fmt.Errorf("imap oauth2 requires TLS; enable SSL/STARTTLS or AllowInsecureAuth for testing")
		}
		if f.TokenSource == nil {
			return fmt.Errorf("imap oauth2 token source is nil")
		}
		tok, err := f.TokenSource(ctx)
		if err != nil {
			return fmt.Errorf("imap token source: %w", err)
		}
		var authClient sasl.Client
		if f.AuthMethod == gsmail.AuthXOAUTH2 {
			authClient = gsmail.NewXOAUTH2Client(f.Username, tok)
		} else {
			authClient = sasl.NewOAuthBearerClient(&sasl.OAuthBearerOptions{Username: f.Username, Token: tok})
		}
		if err := c.Authenticate(authClient); err != nil {
			return fmt.Errorf("imap authenticate: %w", err)
		}
	} else {
		if err := c.Login(f.Username, f.Password); err != nil {
			return fmt.Errorf("imap login: %w", err)
		}
	}
	return nil
}

// Receive retrieves emails using IMAP.
func (f *Receiver) Receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
	var emails []gsmail.Email
	err := gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		c, tlsOn, err := f.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout() }()

		if err := f.authenticate(ctx, c, tlsOn); err != nil {
			return err
		}

		mbox, err := c.Select("INBOX", false)
		if err != nil {
			return fmt.Errorf("imap select inbox: %w", err)
		}

		if mbox.Messages == 0 {
			emails = nil
			return nil
		}

		start := mbox.Messages
		var end uint32 = 1
		if mbox.Messages > uint32(limit) {
			end = start - uint32(limit) + 1
		}

		seqset := new(goimap.SeqSet)
		seqset.AddRange(end, start)

		emails, err = f.fetch(ctx, c, seqset, limit)
		return err
	})
	return emails, err
}

// Search searches for emails using IMAP.
func (f *Receiver) Search(ctx context.Context, options gsmail.SearchOptions, limit int) ([]gsmail.Email, error) {
	var emails []gsmail.Email
	err := gsmail.Retry(ctx, f.GetRetryConfig(), func() error {
		c, tlsOn, err := f.connect(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = c.Logout() }()

		if err := f.authenticate(ctx, c, tlsOn); err != nil {
			return err
		}

		if _, err := c.Select("INBOX", false); err != nil {
			return fmt.Errorf("imap select inbox: %w", err)
		}

		criteria := goimap.NewSearchCriteria()
		if options.From != "" {
			criteria.Header.Set("From", options.From)
		}
		if options.Subject != "" {
			criteria.Header.Set("Subject", options.Subject)
		}
		if !options.Since.IsZero() {
			criteria.Since = options.Since
		}
		if !options.Before.IsZero() {
			criteria.Before = options.Before
		}
		if options.Unseen {
			criteria.WithoutFlags = []string{goimap.SeenFlag}
		}

		uids, err := c.Search(criteria)
		if err != nil {
			return fmt.Errorf("imap search: %w", err)
		}

		if len(uids) == 0 {
			emails = nil
			return nil
		}

		// Take the last N (newest)
		if len(uids) > limit {
			uids = uids[len(uids)-limit:]
		}

		seqset := new(goimap.SeqSet)
		seqset.AddNum(uids...)

		emails, err = f.fetch(ctx, c, seqset, limit)
		return err
	})
	return emails, err
}

// Idle waits for new emails and sends them to the returned channel.
func (f *Receiver) Idle(ctx context.Context) (<-chan gsmail.Email, <-chan error) {
	emailChan := make(chan gsmail.Email, 10)
	errChan := make(chan error, 1)

	go func() {
		defer close(emailChan)
		defer close(errChan)

		c, tlsOn, err := f.connect(ctx)
		if err != nil {
			errChan <- err
			return
		}
		defer func() { _ = c.Logout() }()

		if err := f.authenticate(ctx, c, tlsOn); err != nil {
			errChan <- err
			return
		}

		if _, err := c.Select("INBOX", false); err != nil {
			errChan <- err
			return
		}

		idleClient := idle.NewClient(c)

		// Create a channel for mailbox updates
		updates := make(chan client.Update, 10)
		c.Updates = updates

		stop := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			// IDLE with fallback for servers that don't support it
			done <- idleClient.IdleWithFallback(stop, 29*time.Minute)
		}()

		for {
			select {
			case <-ctx.Done():
				close(stop)
				return
			case err := <-done:
				if err != nil {
					errChan <- fmt.Errorf("idle error: %w", err)
				}
				return
			case update := <-updates:
				if mboxUpdate, ok := update.(*client.MailboxUpdate); ok {
					// When mailbox is updated, fetch unseen messages
					criteria := goimap.NewSearchCriteria()
					criteria.WithoutFlags = []string{goimap.SeenFlag}
					uids, err := c.Search(criteria)
					if err == nil && len(uids) > 0 {
						seqset := new(goimap.SeqSet)
						seqset.AddNum(uids...)
						emails, err := f.fetch(ctx, c, seqset, len(uids))
						if err == nil {
							for _, e := range emails {
								select {
								case emailChan <- e:
								case <-ctx.Done():
									return
								}
							}
						}
					}
					_ = mboxUpdate
				}
			}
		}
	}()

	return emailChan, errChan
}

func (f *Receiver) fetch(ctx context.Context, c *client.Client, seqset *goimap.SeqSet, limit int) ([]gsmail.Email, error) {
	type indexedMessage struct {
		idx int
		msg *goimap.Message
	}
	messages := make(chan indexedMessage, limit)
	done := make(chan error, 1)
	fetchMessages := make(chan *goimap.Message, limit)
	go func() {
		done <- c.Fetch(seqset, []goimap.FetchItem{goimap.FetchRFC822}, fetchMessages)
	}()

	count := 0
	go func() {
		defer close(messages)
		for msg := range fetchMessages {
			messages <- indexedMessage{idx: count, msg: msg}
			count++
		}
	}()

	type result struct {
		index int
		email gsmail.Email
		err   error
	}
	results := make(chan result, limit)
	var wg sync.WaitGroup

	numWorkers := 10
	if limit < numWorkers {
		numWorkers = limit
	}

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for res := range messages {
				select {
				case <-ctx.Done():
					return
				default:
				}

				m := res.msg
				if m == nil {
					continue
				}

				for _, literal := range m.Body {
					raw, err := io.ReadAll(literal)
					if err != nil {
						results <- result{err: fmt.Errorf("imap read body: %w", err)}
						continue
					}

					email, err := gsmail.ParseRawEmail(raw)
					if err != nil {
						continue
					}
					results <- result{index: res.idx, email: email}
				}
			}
		}()
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

	// Reverse to have newest first
	for i, j := 0, len(emails)-1; i < j; i, j = i+1, j-1 {
		emails[i], emails[j] = emails[j], emails[i]
	}

	return emails, nil
}

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

	// Mailbox is the folder to read. Empty means INBOX.
	//
	// This used to be hardcoded, so the package could only ever see INBOX --
	// no Archive, no label, no shared folder.
	Mailbox string

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

// DefaultMinTLSVersion is the minimum TLS version used when MinVersion is 0.
//
// TLS 1.0 and 1.1 are deprecated by RFC 8996 and Go's own client default is
// TLS 1.2; pinning anything lower would be a downgrade from the standard
// library, not a hardening measure.
const DefaultMinTLSVersion = tls.VersionTLS12

// maxFetchWorkers bounds the goroutines that parse fetched messages.
const maxFetchWorkers = 10

// updateBufferSize is the capacity of the IMAP unilateral-update channel.
// It only has to absorb a burst while the drain goroutine is scheduled.
const updateBufferSize = 64

// idleShutdownTimeout bounds how long Idle waits for an in-flight IDLE to
// unwind before tearing the connection down anyway.
const idleShutdownTimeout = 10 * time.Second

// idleRefreshInterval is how long a single IDLE command runs before being
// renewed. RFC 2177 requires re-issuing at least every 29 minutes.
const idleRefreshInterval = 29 * time.Minute

// tlsConfig returns a TLS configuration.
//
// CipherSuites is left nil unless the caller sets it, so the suite list tracks
// the Go standard library rather than a hand-maintained copy that silently
// goes stale. Note that Go only honours CipherSuites for TLS 1.2 and below;
// TLS 1.3 suites are not configurable.
func (f *Receiver) tlsConfig() *tls.Config {
	minVer := f.MinVersion
	if minVer == 0 {
		minVer = DefaultMinTLSVersion
	}
	return &tls.Config{
		ServerName:         f.Host,
		MinVersion:         minVer,
		MaxVersion:         f.MaxVersion,
		CipherSuites:       f.CipherSuites,
		InsecureSkipVerify: f.InsecureSkipVerify,
	}
}

// mailbox returns the folder to operate on, defaulting to INBOX.
func (f *Receiver) mailbox() string {
	if f.Mailbox != "" {
		return f.Mailbox
	}
	return "INBOX"
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

// Receive retrieves the newest messages from INBOX, most recent first.
// limit must be greater than zero; see gsmail.ErrInvalidLimit.
func (f *Receiver) Receive(ctx context.Context, limit int) ([]gsmail.Email, error) {
	if err := gsmail.CheckLimit(limit); err != nil {
		return nil, err
	}

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

		mbox, err := c.Select(f.mailbox(), false)
		if err != nil {
			return fmt.Errorf("imap select %q: %w", f.mailbox(), err)
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

// Search searches INBOX and returns at most limit messages, newest first.
// limit must be greater than zero; see gsmail.ErrInvalidLimit.
func (f *Receiver) Search(ctx context.Context, options gsmail.SearchOptions, limit int) ([]gsmail.Email, error) {
	if err := gsmail.CheckLimit(limit); err != nil {
		return nil, err
	}

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

		if _, err := c.Select(f.mailbox(), false); err != nil {
			return fmt.Errorf("imap select %q: %w", f.mailbox(), err)
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

		if _, err := c.Select(f.mailbox(), false); err != nil {
			errChan <- err
			return
		}

		idleClient := idle.NewClient(c)

		// go-imap delivers unilateral updates on the connection's own reader
		// goroutine. If nothing drains this channel the reader blocks, and the
		// response to whatever command we are waiting on can never arrive.
		// Drain it continuously and coalesce into one pending signal: "the
		// mailbox changed" does not need a queue.
		updates := make(chan client.Update, updateBufferSize)
		c.Updates = updates

		poke := make(chan struct{}, 1)
		quit := make(chan struct{})
		defer close(quit)

		go func() {
			for {
				select {
				case u := <-updates:
					if _, ok := u.(*client.MailboxUpdate); !ok {
						continue
					}
					select {
					case poke <- struct{}{}:
					default:
					}
				case <-quit:
					return
				}
			}
		}()

		// An IMAP connection carries one command at a time, and IDLE occupies
		// it until the client sends DONE. Searching or fetching while the IDLE
		// goroutine still holds the connection is both a data race on the
		// go-imap writer and a protocol violation, so each round here is:
		// idle -> interrupt -> do the work -> idle again.
		for {
			stop := make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- idleClient.IdleWithFallback(stop, idleRefreshInterval)
			}()

			// endIdle sends DONE and waits for the IDLE command to complete,
			// so the connection is ours again before we touch it.
			endIdle := func() error {
				close(stop)
				select {
				case err := <-done:
					return err
				case <-time.After(idleShutdownTimeout):
					// The connection is wedged. Report it and let the deferred
					// Logout tear it down rather than hanging the caller.
					return fmt.Errorf("imap: idle did not terminate within %s", idleShutdownTimeout)
				}
			}

			var idleErr error
			select {
			case <-ctx.Done():
				_ = endIdle()
				return
			case idleErr = <-done:
				// IDLE ended on its own: the refresh interval elapsed, or the
				// connection failed. Either way we now own the connection.
			case <-poke:
				idleErr = endIdle()
			}

			if idleErr != nil {
				errChan <- fmt.Errorf("idle error: %w", idleErr)
				return
			}

			// The connection is free; safe to issue commands.
			criteria := goimap.NewSearchCriteria()
			criteria.WithoutFlags = []string{goimap.SeenFlag}
			uids, err := c.Search(criteria)
			if err != nil {
				errChan <- fmt.Errorf("imap search: %w", err)
				return
			}
			if len(uids) == 0 {
				continue
			}

			seqset := new(goimap.SeqSet)
			seqset.AddNum(uids...)
			emails, err := f.fetch(ctx, c, seqset, len(uids))
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				errChan <- fmt.Errorf("imap fetch: %w", err)
				return
			}

			for _, e := range emails {
				select {
				case emailChan <- e:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return emailChan, errChan
}

func (f *Receiver) fetch(ctx context.Context, c *client.Client, seqset *goimap.SeqSet, limit int) ([]gsmail.Email, error) {
	// Never size a channel or a worker pool straight from caller input.
	// A negative limit panics make(chan), and a zero limit spawns no workers,
	// so nothing drains `messages`: the indexer blocks, c.Fetch blocks, and
	// the final <-done below hangs with no context escape. Callers are checked
	// in Receive and Search; this floor keeps the invariant local too.
	if limit < 1 {
		limit = 1
	}

	type indexedMessage struct {
		idx int
		msg *goimap.Message
	}
	messages := make(chan indexedMessage, limit)
	done := make(chan error, 1)
	fetchMessages := make(chan *goimap.Message, limit)
	go func() {
		done <- c.Fetch(seqset, []goimap.FetchItem{goimap.FetchRFC822, goimap.FetchUid}, fetchMessages)
	}()

	// c.Fetch is not context-aware, and an IMAP connection carries one command
	// at a time. Simply returning when the context fires would leave FETCH in
	// flight on a client the caller is about to log out of -- concurrent use of
	// a connection go-imap does not synchronise. Instead, tear the connection
	// down so the command fails fast, and always wait for it below.
	watchdog := make(chan struct{})
	defer close(watchdog)
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Terminate()
		case <-watchdog:
		}
	}()

	// Index and forward the fetched messages.
	//
	// The counter is goroutine-local. It used to be shared with the collector
	// below, which read it to order the results -- but the workers abandon
	// `messages` when the context is cancelled, so the collector could reach
	// that read while this goroutine was still incrementing. That was a data
	// race; ordering is now derived from the collected indices instead.
	//
	// fetchMessages is drained to completion even after cancellation. c.Fetch
	// writes into it and only returns once it is emptied, so bailing out early
	// would strand the Fetch goroutine and leave `done` unwritten.
	go func() {
		defer close(messages)
		idx := 0
		cancelled := false
		for msg := range fetchMessages {
			if cancelled {
				continue
			}
			select {
			case messages <- indexedMessage{idx: idx, msg: msg}:
				idx++
			case <-ctx.Done():
				cancelled = true
			}
		}
	}()

	type result struct {
		index int
		email gsmail.Email
		err   error
	}
	results := make(chan result, limit)
	var wg sync.WaitGroup

	numWorkers := maxFetchWorkers
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
					// Carry the server-side identity through, so the caller can
					// mark, move or delete the message afterwards.
					email.UID = m.Uid
					email.Mailbox = f.mailbox()
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

	// Always wait for FETCH to finish. The watchdog above guarantees this
	// terminates: either the server responds, or the context fires and the
	// connection is torn down under it.
	if err := <-done; err != nil {
		switch {
		case ctx.Err() != nil:
			// The failure is the cancellation, not the server.
			if fetchErr == nil {
				fetchErr = ctx.Err()
			}
		case fetchErr != nil:
			fetchErr = fmt.Errorf("%v (fetch error: %w)", fetchErr, err)
		default:
			fetchErr = fmt.Errorf("imap fetch error: %w", err)
		}
	} else if ctx.Err() != nil && fetchErr == nil {
		fetchErr = ctx.Err()
	}

	// Restore the order the server delivered messages in, using the highest
	// index actually collected rather than a counter shared with the indexer
	// goroutine.
	emails := make([]gsmail.Email, 0, len(emailsMap))
	if len(emailsMap) > 0 {
		maxIdx := 0
		for idx := range emailsMap {
			if idx > maxIdx {
				maxIdx = idx
			}
		}
		for i := 0; i <= maxIdx; i++ {
			if email, ok := emailsMap[i]; ok {
				emails = append(emails, email)
			}
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

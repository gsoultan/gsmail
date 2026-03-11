package smtp

import (
	"context"
	"errors"
	"net/smtp"
	"sync"
	"time"
)

var (
	ErrPoolClosed = errors.New("pool is closed")
	ErrPoolFull   = errors.New("pool is full")
)

// PoolConfig defines the configuration for the SMTP connection pool.
type PoolConfig struct {
	MaxIdle     int           // Maximum number of idle connections in the pool.
	MaxOpen     int           // Maximum number of open connections (idle + active). 0 means no limit.
	IdleTimeout time.Duration // Maximum amount of time a connection may be idle before being closed.
	MaxLifetime time.Duration // Maximum amount of time a connection may be reused.
	Wait        bool          // If true, Get will block until a connection is available or ctx is cancelled.
}

// Stats holds statistics of the pool.
type Stats struct {
	OpenConnections int           // Number of established connections.
	IdleConnections int           // Number of idle connections.
	InUse           int           // Number of connections currently in use.
	WaitCount       int64         // Total number of connections waited for.
	WaitDuration    time.Duration // Total time blocked waiting for a connection.
}

// pooledClient wraps an smtp.Client with metadata for pool management.
type pooledClient struct {
	client    *smtp.Client
	createdAt time.Time
	lastUsed  time.Time
}

// Pool manages a pool of SMTP connections.
type Pool struct {
	config PoolConfig
	dialer func(ctx context.Context) (*smtp.Client, error)

	mu      sync.Mutex
	idle    []*pooledClient
	open    int
	closed  bool
	waiters []chan *smtp.Client
	active  map[*smtp.Client]time.Time // Track creation time of active connections

	// Stats
	waitCount    int64
	waitDuration time.Duration
}

// NewPool creates a new SMTP connection pool.
func NewPool(config PoolConfig, dialer func(ctx context.Context) (*smtp.Client, error)) *Pool {
	if config.MaxIdle <= 0 {
		config.MaxIdle = 2
	}
	return &Pool{
		config: config,
		dialer: dialer,
		active: make(map[*smtp.Client]time.Time),
	}
}

// Get retrieves a connection from the pool or creates a new one.
func (p *Pool) Get(ctx context.Context) (*smtp.Client, error) {
	start := time.Now()
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrPoolClosed
		}

		// Try to get an idle connection
		if len(p.idle) > 0 {
			pc := p.idle[len(p.idle)-1]
			p.idle = p.idle[:len(p.idle)-1]
			p.mu.Unlock()

			// Check if the connection has timed out or reached max lifetime
			if (p.config.IdleTimeout > 0 && time.Since(pc.lastUsed) > p.config.IdleTimeout) ||
				(p.config.MaxLifetime > 0 && time.Since(pc.createdAt) > p.config.MaxLifetime) {
				_ = pc.client.Quit()
				p.mu.Lock()
				p.decOpenLocked()
				p.mu.Unlock()
				continue
			}

			// Check if the connection is still alive using NOOP
			if err := pc.client.Noop(); err != nil {
				_ = pc.client.Close()
				p.mu.Lock()
				p.decOpenLocked()
				p.mu.Unlock()
				continue
			}

			p.mu.Lock()
			p.active[pc.client] = pc.createdAt
			p.mu.Unlock()
			return pc.client, nil
		}

		// No idle connection, check if we can open a new one
		if p.config.MaxOpen > 0 && p.open >= p.config.MaxOpen {
			if !p.config.Wait {
				p.mu.Unlock()
				return nil, ErrPoolFull
			}

			// Wait for a connection
			waitChan := make(chan *smtp.Client, 1)
			p.waiters = append(p.waiters, waitChan)
			p.waitCount++
			p.mu.Unlock()

			select {
			case <-ctx.Done():
				p.mu.Lock()
				// Remove ourself from waiters
				for i, w := range p.waiters {
					if w == waitChan {
						p.waiters = append(p.waiters[:i], p.waiters[i+1:]...)
						break
					}
				}
				p.mu.Unlock()
				return nil, ctx.Err()
			case client := <-waitChan:
				p.mu.Lock()
				p.waitDuration += time.Since(start)
				p.mu.Unlock()
				if client == nil {
					// Signalled to try again (e.g. space opened up)
					continue
				}
				return client, nil
			}
		}

		p.open++
		p.mu.Unlock()

		client, err := p.dialer(ctx)
		if err != nil {
			p.mu.Lock()
			p.decOpenLocked()
			p.mu.Unlock()
			return nil, err
		}

		p.mu.Lock()
		p.active[client] = time.Now()
		p.mu.Unlock()

		return client, nil
	}
}

// Put returns a connection to the pool.
func (p *Pool) Put(client *smtp.Client, err error) {
	if client == nil {
		return
	}

	p.mu.Lock()
	createdAt, ok := p.active[client]
	delete(p.active, client)
	p.mu.Unlock()

	// If there was an error or max lifetime reached, don't return the connection to the pool
	if err != nil || (p.config.MaxLifetime > 0 && ok && time.Since(createdAt) > p.config.MaxLifetime) {
		_ = client.Close()
		p.mu.Lock()
		p.decOpenLocked()
		p.mu.Unlock()
		return
	}

	// Reset the client state before returning it to the pool
	if err := client.Reset(); err != nil {
		_ = client.Close()
		p.mu.Lock()
		p.decOpenLocked()
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	if p.closed {
		p.open--
		p.mu.Unlock()
		_ = client.Quit()
		return
	}

	// Give directly to a waiter if any
	if len(p.waiters) > 0 {
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		p.active[client] = createdAt
		p.mu.Unlock()
		w <- client
		return
	}

	// Exceeding MaxIdle
	if len(p.idle) >= p.config.MaxIdle {
		p.open--
		p.mu.Unlock()
		_ = client.Quit()
		return
	}

	p.idle = append(p.idle, &pooledClient{
		client:    client,
		createdAt: createdAt,
		lastUsed:  time.Now(),
	})
	p.mu.Unlock()
}

// Close closes the pool and all its connections.
func (p *Pool) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	idle := p.idle
	p.idle = nil
	p.open = 0
	for _, w := range p.waiters {
		w <- nil
	}
	p.waiters = nil
	p.mu.Unlock()

	for _, pc := range idle {
		_ = pc.client.Quit()
	}
	return nil
}

// Stats returns the current statistics of the pool.
func (p *Pool) Stats() Stats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Stats{
		OpenConnections: p.open,
		IdleConnections: len(p.idle),
		InUse:           len(p.active),
		WaitCount:       p.waitCount,
		WaitDuration:    p.waitDuration,
	}
}

func (p *Pool) decOpenLocked() {
	p.open--
	if len(p.waiters) > 0 {
		w := p.waiters[0]
		p.waiters = p.waiters[1:]
		w <- nil
	}
}

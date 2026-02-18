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
}

// pooledClient wraps an smtp.Client with metadata for pool management.
type pooledClient struct {
	client   *smtp.Client
	lastUsed time.Time
}

// Pool manages a pool of SMTP connections.
type Pool struct {
	config PoolConfig
	dialer func(ctx context.Context) (*smtp.Client, error)

	mu     sync.Mutex
	idle   []*pooledClient
	open   int
	closed bool
}

// NewPool creates a new SMTP connection pool.
func NewPool(config PoolConfig, dialer func(ctx context.Context) (*smtp.Client, error)) *Pool {
	if config.MaxIdle <= 0 {
		config.MaxIdle = 2
	}
	return &Pool{
		config: config,
		dialer: dialer,
	}
}

// Get retrieves a connection from the pool or creates a new one.
func (p *Pool) Get(ctx context.Context) (*smtp.Client, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrPoolClosed
	}

	// Try to get an idle connection
	for len(p.idle) > 0 {
		pc := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]

		// Check if the connection has timed out
		if p.config.IdleTimeout > 0 && time.Since(pc.lastUsed) > p.config.IdleTimeout {
			_ = pc.client.Quit()
			p.open--
			continue
		}

		// Check if the connection is still alive using NOOP
		if err := pc.client.Noop(); err != nil {
			_ = pc.client.Close()
			p.open--
			continue
		}

		p.mu.Unlock()
		return pc.client, nil
	}

	// No idle connection, check if we can open a new one
	if p.config.MaxOpen > 0 && p.open >= p.config.MaxOpen {
		p.mu.Unlock()
		return nil, ErrPoolFull
	}

	p.open++
	p.mu.Unlock()

	client, err := p.dialer(ctx)
	if err != nil {
		p.mu.Lock()
		p.open--
		p.mu.Unlock()
		return nil, err
	}

	return client, nil
}

// Put returns a connection to the pool.
func (p *Pool) Put(client *smtp.Client, err error) {
	if client == nil {
		return
	}

	// If there was an error, don't return the connection to the pool
	if err != nil {
		_ = client.Close()
		p.mu.Lock()
		p.open--
		p.mu.Unlock()
		return
	}

	// Reset the client state before returning it to the pool
	if err := client.Reset(); err != nil {
		_ = client.Close()
		p.mu.Lock()
		p.open--
		p.mu.Unlock()
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed || len(p.idle) >= p.config.MaxIdle {
		_ = client.Quit()
		p.open--
		return
	}

	p.idle = append(p.idle, &pooledClient{
		client:   client,
		lastUsed: time.Now(),
	})
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
	p.mu.Unlock()

	for _, pc := range idle {
		_ = pc.client.Quit()
	}
	return nil
}

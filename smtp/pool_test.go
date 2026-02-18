package smtp

import (
	"context"
	"net"
	"net/smtp"
	"testing"
	"time"
)

func TestPool(t *testing.T) {
	// Mock SMTP server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	host, _, _ := net.SplitHostPort(addr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				// Basic SMTP handshake
				_, _ = c.Write([]byte("220 localhost ESMTP\r\n"))
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					cmd := string(buf[:n])
					switch {
					case cmd == "EHLO localhost\r\n" || cmd == "HELO localhost\r\n":
						_, _ = c.Write([]byte("250-localhost\r\n250 AUTH PLAIN\r\n"))
					case cmd == "NOOP\r\n":
						_, _ = c.Write([]byte("250 OK\r\n"))
					case cmd == "RSET\r\n":
						_, _ = c.Write([]byte("250 OK\r\n"))
					case cmd == "QUIT\r\n":
						_, _ = c.Write([]byte("221 Goodbye\r\n"))
						return
					default:
						_, _ = c.Write([]byte("250 OK\r\n"))
					}
				}
			}(conn)
		}
	}()

	dialer := func(ctx context.Context) (*smtp.Client, error) {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, err
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if err := client.Hello("localhost"); err != nil {
			_ = client.Close()
			return nil, err
		}
		return client, nil
	}

	config := PoolConfig{
		MaxIdle:     2,
		MaxOpen:     5,
		IdleTimeout: time.Minute,
	}
	pool := NewPool(config, dialer)
	defer pool.Close()

	ctx := context.Background()

	// Test Get and Put
	client1, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get client: %v", err)
	}
	if pool.open != 1 {
		t.Errorf("Expected open connections to be 1, got %d", pool.open)
	}

	pool.Put(client1, nil)
	if len(pool.idle) != 1 {
		t.Errorf("Expected idle connections to be 1, got %d", len(pool.idle))
	}

	// Test reuse
	client2, err := pool.Get(ctx)
	if err != nil {
		t.Fatalf("Failed to get client: %v", err)
	}
	if client2 != client1 {
		t.Errorf("Expected same client to be reused")
	}
	if len(pool.idle) != 0 {
		t.Errorf("Expected idle connections to be 0, got %d", len(pool.idle))
	}

	pool.Put(client2, nil)

	// Test MaxOpen
	var clients []*smtp.Client
	for i := 0; i < 5; i++ {
		c, err := pool.Get(ctx)
		if err != nil {
			t.Fatalf("Failed to get client %d: %v", i, err)
		}
		clients = append(clients, c)
	}

	_, err = pool.Get(ctx)
	if err != ErrPoolFull {
		t.Errorf("Expected ErrPoolFull, got %v", err)
	}

	for _, c := range clients {
		pool.Put(c, nil)
	}

	if len(pool.idle) != 2 { // MaxIdle is 2
		t.Errorf("Expected idle connections to be 2, got %d", len(pool.idle))
	}
}

func TestPool_IdleTimeout(t *testing.T) {
	// Mock SMTP server (similar to above but simpler)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	addr := ln.Addr().String()
	host, _, _ := net.SplitHostPort(addr)

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte("220 localhost ESMTP\r\n"))
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					if string(buf[:n]) == "QUIT\r\n" {
						return
					}
					_, _ = c.Write([]byte("250 OK\r\n"))
				}
			}(conn)
		}
	}()

	dialer := func(ctx context.Context) (*smtp.Client, error) {
		conn, _ := net.Dial("tcp", addr)
		client, _ := smtp.NewClient(conn, host)
		_ = client.Hello("localhost")
		return client, nil
	}

	config := PoolConfig{
		MaxIdle:     2,
		IdleTimeout: 100 * time.Millisecond,
	}
	pool := NewPool(config, dialer)
	defer pool.Close()

	ctx := context.Background()
	c, _ := pool.Get(ctx)
	pool.Put(c, nil)

	if len(pool.idle) != 1 {
		t.Errorf("Expected 1 idle connection")
	}

	time.Sleep(200 * time.Millisecond)

	c2, _ := pool.Get(ctx)
	if c2 == c {
		t.Errorf("Expected new client after idle timeout")
	}
}

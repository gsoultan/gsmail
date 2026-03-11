package smtp

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"sync"
	"testing"
	"time"

	"github.com/gsoultan/gsmail"
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

func TestPoolConcurrency(t *testing.T) {
	// Mock SMTP server that counts active connections
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	host, _, _ := net.SplitHostPort(addr)

	var activeConns int32
	var mu sync.Mutex
	var maxConns int32

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				mu.Lock()
				activeConns++
				if activeConns > maxConns {
					maxConns = activeConns
				}
				mu.Unlock()

				defer func() {
					mu.Lock()
					activeConns--
					mu.Unlock()
				}()

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
						// Add a small delay to simulate network latency
						time.Sleep(10 * time.Millisecond)
						_, _ = c.Write([]byte("250 OK\r\n"))
					case cmd == "RSET\r\n":
						_, _ = c.Write([]byte("250 OK\r\n"))
					case cmd == "QUIT\r\n":
						_, _ = c.Write([]byte("221 Goodbye\r\n"))
						return
					case cmd == "DATA\r\n":
						_, _ = c.Write([]byte("354 Start mail input; end with <CRLF>.<CRLF>\r\n"))
						// Wait for end of data
						for {
							n, _ := c.Read(buf)
							if n >= 3 && string(buf[n-3:n]) == ".\r\n" {
								break
							}
						}
						_, _ = c.Write([]byte("250 OK\r\n"))
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

	// Pool configuration to handle 100 concurrent emails
	config := PoolConfig{
		MaxIdle:     20,
		MaxOpen:     100,
		IdleTimeout: 5 * time.Minute,
	}
	pool := NewPool(config, dialer)
	defer pool.Close()

	ctx := context.Background()
	numConcurrent := 100
	var wg sync.WaitGroup
	errChan := make(chan error, numConcurrent)

	start := time.Now()
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			client, err := pool.Get(ctx)
			if err != nil {
				errChan <- fmt.Errorf("goroutine %d: failed to get client: %v", id, err)
				return
			}

			// Simulate some work
			time.Sleep(50 * time.Millisecond)

			// Use the client
			if err := client.Noop(); err != nil {
				errChan <- fmt.Errorf("goroutine %d: noop failed: %v", id, err)
				pool.Put(client, err)
				return
			}

			pool.Put(client, nil)
		}(i)
	}

	wg.Wait()
	close(errChan)

	duration := time.Since(start)
	t.Logf("Time to complete %d concurrent sends: %v", numConcurrent, duration)

	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		t.Errorf("Encountered %d errors during concurrent test: %v", len(errors), errors[0])
	}

	if int(maxConns) > config.MaxOpen {
		t.Errorf("Max concurrent connections exceeded MaxOpen: %d > %d", maxConns, config.MaxOpen)
	}

	t.Logf("Max concurrent connections observed: %d", maxConns)
}

func TestPoolConcurrencyWithReuse(t *testing.T) {
	// Mock SMTP server with latency
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
					cmd := string(buf[:n])
					if cmd == "NOOP\r\n" {
						time.Sleep(20 * time.Millisecond) // Simulate latency
					}
					if cmd == "QUIT\r\n" {
						_, _ = c.Write([]byte("221 Goodbye\r\n"))
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
		MaxIdle: 50,
		MaxOpen: 100,
	}
	pool := NewPool(config, dialer)
	defer pool.Close()

	ctx := context.Background()

	// Fill the pool with some connections
	clients := make([]*smtp.Client, 50)
	for i := range 50 {
		clients[i], _ = pool.Get(ctx)
	}
	for _, c := range clients {
		pool.Put(c, nil)
	}

	// Now run concurrent Get calls
	numConcurrent := 50
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, _ := pool.Get(ctx)
			if c != nil {
				pool.Put(c, nil)
			}
		}()
	}
	wg.Wait()
	duration := time.Since(start)

	// If it's concurrent (optimal), it should take much less than 50 * 20ms = 1000ms.
	if duration > 500*time.Millisecond {
		t.Errorf("Expected concurrent NOOP checks, but it seems serialized. Duration: %v", duration)
	}

	t.Logf("Time for %d concurrent Get with reuse: %v", numConcurrent, duration)
}

func TestSenderConcurrencyWithPool(t *testing.T) {
	// Mock SMTP server
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	addr := ln.Addr().String()
	_, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

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
					cmd := string(buf[:n])
					if cmd == "DATA\r\n" {
						_, _ = c.Write([]byte("354 Start mail input; end with <CRLF>.<CRLF>\r\n"))
						for {
							n, _ := c.Read(buf)
							if n >= 3 && string(buf[n-3:n]) == ".\r\n" {
								break
							}
						}
						_, _ = c.Write([]byte("250 OK\r\n"))
						continue
					}
					if cmd == "QUIT\r\n" {
						_, _ = c.Write([]byte("221 Goodbye\r\n"))
						return
					}
					_, _ = c.Write([]byte("250 OK\r\n"))
				}
			}(conn)
		}
	}()

	sender := NewSender("127.0.0.1", port, "", "", false)
	sender.EnablePool(PoolConfig{
		MaxIdle: 10,
		MaxOpen: 50, // Limit to 50 to test retries
	})
	defer sender.Close()

	numConcurrent := 100
	var wg sync.WaitGroup
	errChan := make(chan error, numConcurrent)

	ctx := context.Background()
	email := gsmail.Email{
		From:    "test@example.com",
		To:      []string{"to@example.com"},
		Subject: "Concurrency test",
		Body:    []byte("Hello"),
	}

	for i := 0; i < numConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sender.Send(ctx, email); err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	if len(errors) > 0 {
		t.Errorf("Encountered %d errors: %v", len(errors), errors[0])
	} else {
		t.Logf("Successfully sent %d emails with 100 concurrent workers and MaxOpen=50 (retries worked!)", numConcurrent)
	}
}

func TestPoolWaitAnd1000Concurrent(t *testing.T) {
	// Mock SMTP server
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	addr := ln.Addr().String()

	var activeConns int64
	var maxActiveConns int64
	var mu sync.Mutex

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			activeConns++
			if activeConns > maxActiveConns {
				maxActiveConns = activeConns
			}
			mu.Unlock()

			go func(c net.Conn) {
				defer func() {
					c.Close()
					mu.Lock()
					activeConns--
					mu.Unlock()
				}()
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
					case cmd == "QUIT\r\n":
						_, _ = c.Write([]byte("221 Goodbye\r\n"))
						return
					case cmd == "DATA\r\n":
						_, _ = c.Write([]byte("354 Go ahead\r\n"))
					default:
						_, _ = c.Write([]byte("250 OK\r\n"))
					}
				}
			}(conn)
		}
	}()

	sender := NewSender("127.0.0.1", 0, "", "", false)
	// Override port
	_, portStr, _ := net.SplitHostPort(addr)
	fmt.Sscanf(portStr, "%d", &sender.Port)

	// Configure pool with Wait=true and MaxOpen=50
	sender.EnablePool(PoolConfig{
		MaxIdle:     20,
		MaxOpen:     50,
		IdleTimeout: time.Minute,
		Wait:        true,
	})
	defer sender.Close()

	ctx := context.Background()
	numEmails := 1000
	var wg sync.WaitGroup
	wg.Add(numEmails)

	errChan := make(chan error, numEmails)

	startTime := time.Now()
	for i := 0; i < numEmails; i++ {
		go func() {
			defer wg.Done()
			email := gsmail.Email{
				From:    "sender@example.com",
				To:      []string{"receiver@example.com"},
				Subject: "Test",
				Body:    []byte("Hello"),
			}
			err := sender.Send(ctx, email)
			if err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)
	duration := time.Since(startTime)

	for err := range errChan {
		t.Errorf("Send failed: %v", err)
	}

	stats, _ := sender.PoolStats()
	t.Logf("Sent %d emails in %v", numEmails, duration)
	t.Logf("Max active connections on server: %d", maxActiveConns)
	t.Logf("Pool stats: %+v", stats)

	if maxActiveConns > 50 {
		t.Errorf("Expected max active connections <= 50, got %d", maxActiveConns)
	}
}

func TestPoolMaxLifetime(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	addr := ln.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	var port int
	fmt.Sscanf(portStr, "%d", &port)

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
		MaxLifetime: 200 * time.Millisecond,
	}
	pool := NewPool(config, dialer)
	defer pool.Close()

	ctx := context.Background()
	c1, _ := pool.Get(ctx)
	pool.Put(c1, nil)

	time.Sleep(100 * time.Millisecond)
	c2, _ := pool.Get(ctx)
	if c2 != c1 {
		t.Errorf("Expected reuse before max lifetime")
	}
	pool.Put(c2, nil)

	time.Sleep(150 * time.Millisecond) // Total > 200ms
	c3, _ := pool.Get(ctx)
	if c3 == c1 {
		t.Errorf("Expected new connection after max lifetime")
	}
	pool.Put(c3, nil)
}

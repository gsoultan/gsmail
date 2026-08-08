package smtp

import (
	"context"
	"net"
	"net/smtp"
	"sync"
	"testing"
	"time"
)

// fakeSMTPServer speaks just enough ESMTP for smtp.NewClient, NOOP, RSET and
// QUIT, which is all the pool exercises.
func fakeSMTPServer(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = c.Write([]byte("220 fake ESMTP\r\n"))
				buf := make([]byte, 512)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					cmd := string(buf[:n])
					switch {
					case len(cmd) >= 4 && (cmd[:4] == "EHLO" || cmd[:4] == "HELO"):
						_, _ = c.Write([]byte("250 fake\r\n"))
					case len(cmd) >= 4 && cmd[:4] == "QUIT":
						_, _ = c.Write([]byte("221 bye\r\n"))
						return
					default:
						_, _ = c.Write([]byte("250 ok\r\n"))
					}
				}
			}(conn)
		}
	}()

	return ln.Addr().String()
}

// A waiter whose context expires at the same moment Put hands it a connection
// used to lose the race and strand that connection: still live, still in
// p.active, still counted against MaxOpen, and unreachable by anyone. Leak
// MaxOpen of them and the pool wedges permanently.
//
// Every caller here returns what it borrows, so once they have all finished,
// p.active must be empty.
func TestPoolDoesNotStrandConnectionOnWaiterTimeout(t *testing.T) {
	addr := fakeSMTPServer(t)

	p := NewPool(PoolConfig{MaxIdle: 4, MaxOpen: 2, Wait: true},
		func(ctx context.Context) (*smtp.Client, error) { return smtp.Dial(addr) })
	defer p.Close()

	var wg sync.WaitGroup
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Deadlines land all over the handoff window.
			ctx, cancel := context.WithTimeout(context.Background(),
				time.Duration(i%17)*time.Millisecond)
			defer cancel()

			c, err := p.Get(ctx)
			if err != nil {
				return
			}
			p.Put(c, nil)
		}(i)
	}
	wg.Wait()

	// Let any handoff still in flight settle.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && p.Stats().InUse != 0 {
		time.Sleep(10 * time.Millisecond)
	}

	if st := p.Stats(); st.InUse != 0 {
		t.Errorf("%d connection(s) stranded after every caller returned (open=%d idle=%d)",
			st.InUse, st.OpenConnections, st.IdleConnections)
	}
}

// Close used to zero p.open while connections were still checked out, so the
// subsequent Put drove the count negative.
func TestPoolOpenCountStaysSaneAcrossClose(t *testing.T) {
	addr := fakeSMTPServer(t)

	p := NewPool(PoolConfig{MaxIdle: 2},
		func(ctx context.Context) (*smtp.Client, error) { return smtp.Dial(addr) })

	checkedOut, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	idle, err := p.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.Put(idle, nil) // parks one connection in the idle list

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// The still-checked-out connection is accounted for until its holder
	// returns it.
	if st := p.Stats(); st.OpenConnections != 1 {
		t.Errorf("after Close: OpenConnections = %d, want 1 (the checked-out conn)", st.OpenConnections)
	}

	p.Put(checkedOut, nil)
	if st := p.Stats(); st.OpenConnections != 0 {
		t.Errorf("after final Put: OpenConnections = %d, want 0", st.OpenConnections)
	}
}

// A pool that leaks slots eventually reports MaxOpen forever and refuses to
// hand anything out. Churning far more requests than MaxOpen must leave the
// pool usable.
func TestPoolRemainsUsableAfterChurn(t *testing.T) {
	addr := fakeSMTPServer(t)

	p := NewPool(PoolConfig{MaxIdle: 2, MaxOpen: 2, Wait: true},
		func(ctx context.Context) (*smtp.Client, error) { return smtp.Dial(addr) })
	defer p.Close()

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(),
				time.Duration(i%11)*time.Millisecond)
			defer cancel()
			if c, err := p.Get(ctx); err == nil {
				p.Put(c, nil)
			}
		}(i)
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := p.Get(ctx)
	if err != nil {
		t.Fatalf("pool became unusable after churn: %v (stats %+v)", err, p.Stats())
	}
	p.Put(c, nil)
}

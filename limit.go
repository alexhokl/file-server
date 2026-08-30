package main

import (
	"net"
	"sync"
)

// limitedListener caps the number of concurrently open connections accepted
// from a listener. Connections closed by the caller release their slot, so
// the count tracks live connections rather than cumulative accepts.
//
// Accept never blocks forever waiting for a slot: an accepted connection
// beyond the cap is closed immediately, mirroring how OpenSSH's MaxStartups
// sheds load. Accept returns once a connection is admitted or the underlying
// listener fails.
type limitedListener struct {
	net.Listener

	mu       sync.Mutex
	count    int
	maxConns int
}

// limitedConn decrements the listener counter exactly once, no matter how
// many times Close is called.
type limitedConn struct {
	net.Conn

	once     sync.Once
	listener *limitedListener
}

// newLimitedListener wraps listener so that at most maxConns connections are
// open at once. A maxConns of zero or less admits everything.
func newLimitedListener(listener net.Listener, maxConns int) *limitedListener {
	return &limitedListener{
		Listener: listener,
		maxConns: maxConns,
	}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		l.mu.Lock()
		if l.maxConns > 0 && l.count >= l.maxConns {
			l.mu.Unlock()
			// The connection is accepted from the underlying listener only
			// to be refused here; the client sees an immediate disconnect.
			_ = conn.Close()
			continue
		}
		l.count++
		l.mu.Unlock()

		return &limitedConn{Conn: conn, listener: l}, nil
	}
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.listener.mu.Lock()
		c.listener.count--
		c.listener.mu.Unlock()
	})
	return err
}
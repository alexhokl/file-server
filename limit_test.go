package main

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newListenerPair(t *testing.T, maxConns int) *limitedListener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	return newLimitedListener(listener, maxConns)
}

// dialAccepted dials a client connection and returns once the background
// accept loop has admitted it server-side.
func dialAccepted(t *testing.T, l *limitedListener, accepted <-chan net.Conn) net.Conn {
	t.Helper()

	client, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	select {
	case conn := <-accepted:
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	case <-time.After(2 * time.Second):
		t.Fatal("the connection was not accepted in time")
		return nil
	}
}

func TestLimitedListener_AdmitsUpToCap(t *testing.T) {
	l := newListenerPair(t, 3)
	accepted := acceptInBackground(t, l)

	for i := 0; i < 3; i++ {
		dialAccepted(t, l, accepted)
	}

	l.mu.Lock()
	count := l.count
	l.mu.Unlock()
	assert.Equal(t, 3, count)
}

// acceptInBackground mirrors the server's Serve loop: a continuous Accept
// loop that hands admitted connections to the channel.
func acceptInBackground(t *testing.T, l *limitedListener) <-chan net.Conn {
	t.Helper()

	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	return accepted
}

func TestLimitedListener_RefusesBeyondCap(t *testing.T) {
	l := newListenerPair(t, 1)
	accepted := acceptInBackground(t, l)

	first := dialAccepted(t, l, accepted)

	// A second connection is accepted by the underlying listener but refused
	// by the wrapper: the client sees an immediate EOF, not a held socket.
	excess, err := net.Dial("tcp", l.Addr().String())
	require.NoError(t, err)
	defer func() { _ = excess.Close() }()

	oneByte := make([]byte, 1)
	_ = excess.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = excess.Read(oneByte)
	require.Error(t, err, "the excess connection must be disconnected")
	assert.ErrorIs(t, err, io.EOF, "the refusal must arrive as EOF, not a timeout")

	l.mu.Lock()
	count := l.count
	l.mu.Unlock()
	assert.Equal(t, 1, count, "the refused connection must not hold a slot")

	// Closing the admitted connection releases its slot.
	require.NoError(t, first.Close())

	// The next connection is admitted again.
	next := dialAccepted(t, l, accepted)
	l.mu.Lock()
	count = l.count
	l.mu.Unlock()
	assert.Equal(t, 1, count)
	_ = next
}

func TestLimitedConn_DoubleCloseDecrementsOnce(t *testing.T) {
	l := newListenerPair(t, 2)
	accepted := acceptInBackground(t, l)

	conn := dialAccepted(t, l, accepted)

	require.NoError(t, conn.Close())
	// The second close reports the already-closed connection; that is
	// net.Conn behaviour and must not release a second slot.
	require.Error(t, conn.Close())

	l.mu.Lock()
	count := l.count
	l.mu.Unlock()
	assert.Equal(t, 0, count, "a double Close must release exactly one slot")
}

func TestLimitedListener_ZeroCapAdmitsEverything(t *testing.T) {
	l := newListenerPair(t, 0)
	accepted := acceptInBackground(t, l)

	for i := 0; i < 5; i++ {
		dialAccepted(t, l, accepted)
	}

	l.mu.Lock()
	count := l.count
	l.mu.Unlock()
	assert.Equal(t, 5, count)
}
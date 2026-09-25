// Package wire holds what the node-to-node protocols share: length-prefixed
// frames, and a listener that splits connections by their first byte so a
// new protocol can share a port with an old one.
package wire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

// WriteFrame writes b as a uvarint length followed by the bytes.
func WriteFrame(w *bufio.Writer, b []byte) error {
	var n [binary.MaxVarintLen64]byte
	if _, err := w.Write(n[:binary.PutUvarint(n[:], uint64(len(b)))]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

// ReadFrame reads one frame of at most max bytes.
func ReadFrame(r *bufio.Reader, max int) ([]byte, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > uint64(max) {
		return nil, fmt.Errorf("frame of %d bytes is over the %d byte limit", n, max)
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return b, err
}

// BufferedConn reads through the reader that peeked at the first byte.
type BufferedConn struct {
	net.Conn
	R *bufio.Reader
}

func (c *BufferedConn) Read(p []byte) (int, error) { return c.R.Read(p) }

// Mux accepts every connection on a listener and reads its first byte. A
// connection whose first byte matches goes to the handler, with that byte
// still unread; any other is returned by Accept, so the Mux can stand in for
// the listener of the protocol that was there first.
type Mux struct {
	ln    net.Listener
	match func(first byte) bool
	rest  chan net.Conn

	mu      sync.Mutex
	handler func(net.Conn) // nil until set: matching connections are closed
	handled map[net.Conn]struct{}
	closed  bool
	done    chan struct{}
}

func NewMux(ln net.Listener, match func(first byte) bool) *Mux {
	m := &Mux{
		ln:      ln,
		match:   match,
		rest:    make(chan net.Conn),
		handled: map[net.Conn]struct{}{},
		done:    make(chan struct{}),
	}
	go m.serve()
	return m
}

// SetHandler starts serving matching connections with fn, each on its own
// goroutine. The connection is closed when fn returns.
func (m *Mux) SetHandler(fn func(net.Conn)) {
	m.mu.Lock()
	m.handler = fn
	m.mu.Unlock()
}

func (m *Mux) serve() {
	for {
		c, err := m.ln.Accept()
		if err != nil {
			select {
			case <-m.done:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			log.Printf("%s: accept: %v", m.ln.Addr(), err)
			return
		}
		go m.route(c)
	}
}

// route reads the first byte. Clients of both protocols write as soon as
// they connect, so the deadline only drops connections that say nothing.
func (m *Mux) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	conn := &BufferedConn{Conn: c, R: br}

	if !m.match(first[0]) {
		select {
		case m.rest <- conn:
		case <-m.done:
			_ = c.Close()
		}
		return
	}
	m.mu.Lock()
	handler := m.handler
	if handler == nil || m.closed {
		m.mu.Unlock()
		_ = c.Close()
		return
	}
	m.handled[c] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.handled, c)
		m.mu.Unlock()
		_ = c.Close()
	}()
	handler(conn)
}

// Accept returns the next connection that did not match.
func (m *Mux) Accept() (net.Conn, error) {
	select {
	case c := <-m.rest:
		return c, nil
	case <-m.done:
		return nil, net.ErrClosed
	}
}

// Close stops accepting and closes the connections being handled.
func (m *Mux) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	close(m.done)
	m.mu.Unlock()
	m.CloseHandled()
	return m.ln.Close()
}

// CloseHandled closes the connections being handled, leaving the listener.
func (m *Mux) CloseHandled() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for c := range m.handled {
		_ = c.Close()
	}
}

func (m *Mux) Addr() net.Addr { return m.ln.Addr() }

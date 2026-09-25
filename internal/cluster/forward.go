package cluster

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/raft"
)

// Node-to-node traffic shares the Raft port. hashicorp/raft opens each of its
// connections with an RPC type byte, 0 to 4. A connection that opens with
// forwardByte instead carries writes a follower passes on to the leader, so
// clients never need to be involved and HTTP stays for clients alone.
const forwardByte byte = 'F'

// Replies to a forwarded write.
const (
	forwardOK byte = iota
	forwardNotLeader
	forwardError
)

// maxForwardFrame bounds a request or reply read from the network.
const maxForwardFrame = 64 << 20

// errForwardUnsupported means the node dropped a fresh forwarding connection
// without a reply, as a v0.6 node does: its Raft transport reads 'F' as an
// unknown RPC type and closes the connection.
var errForwardUnsupported = errors.New("leader does not accept forwarded writes over the raft port")

// muxLayer is the StreamLayer given to the Raft transport. It accepts every
// connection on the Raft port, hands forwarding connections to the forward
// handler and queues the rest for Raft.
type muxLayer struct {
	ln        net.Listener
	advertise net.Addr
	raftConns chan net.Conn

	mu      sync.Mutex
	forward func(net.Conn) // nil until the node can serve forwarded writes
	conns   map[net.Conn]struct{}
	closed  bool
	done    chan struct{}
}

func newMuxLayer(bind string, advertise *net.TCPAddr) (*muxLayer, error) {
	if advertise.IP == nil || advertise.IP.IsUnspecified() {
		return nil, errors.New("local bind address is not advertisable")
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	l := &muxLayer{
		ln:        ln,
		advertise: advertise,
		raftConns: make(chan net.Conn),
		conns:     map[net.Conn]struct{}{},
		done:      make(chan struct{}),
	}
	go l.serve()
	return l, nil
}

func (l *muxLayer) serve() {
	for {
		c, err := l.ln.Accept()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			log.Printf("raft port: accept: %v", err)
			return
		}
		go l.route(c)
	}
}

// route reads a connection's first byte to tell whose it is. Raft writes its
// RPC type as soon as it connects, so the deadline only drops connections
// that never say anything.
func (l *muxLayer) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	if err != nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	conn := &bufferedConn{Conn: c, r: br}

	if first[0] != forwardByte {
		select {
		case l.raftConns <- conn:
		case <-l.done:
			_ = c.Close()
		}
		return
	}
	_, _ = br.Discard(1)
	l.mu.Lock()
	handler := l.forward
	if handler == nil || l.closed {
		l.mu.Unlock()
		_ = c.Close()
		return
	}
	l.conns[c] = struct{}{}
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		delete(l.conns, c)
		l.mu.Unlock()
		_ = c.Close()
	}()
	handler(conn)
}

func (l *muxLayer) setForward(fn func(net.Conn)) {
	l.mu.Lock()
	l.forward = fn
	l.mu.Unlock()
}

func (l *muxLayer) Accept() (net.Conn, error) {
	select {
	case c := <-l.raftConns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

// Close stops accepting and closes the forwarding connections being served.
func (l *muxLayer) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	close(l.done)
	l.mu.Unlock()
	l.closeForwardConns()
	return l.ln.Close()
}

// closeForwardConns closes the forwarding connections being served.
func (l *muxLayer) closeForwardConns() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for c := range l.conns {
		_ = c.Close()
	}
}

func (l *muxLayer) Addr() net.Addr { return l.advertise }

func (l *muxLayer) Dial(addr raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(addr), timeout)
}

// bufferedConn reads through the reader that peeked at the first byte.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// A frame is a uvarint length followed by that many bytes. A request is one
// frame holding the command, encoded as it is in the Raft log. A reply is a
// status byte and one frame: empty for forwardOK, the leader's HTTP address
// for forwardNotLeader, the error text for forwardError.

func writeFrame(w *bufio.Writer, b []byte) error {
	var n [binary.MaxVarintLen64]byte
	if _, err := w.Write(n[:binary.PutUvarint(n[:], uint64(len(b)))]); err != nil {
		return err
	}
	_, err := w.Write(b)
	return err
}

func readFrame(r *bufio.Reader) ([]byte, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return nil, err
	}
	if n > maxForwardFrame {
		return nil, fmt.Errorf("forwarded frame of %d bytes is too large", n)
	}
	b := make([]byte, n)
	_, err = io.ReadFull(r, b)
	return b, err
}

// serveForward answers forwarded writes on one connection until it closes.
// A node that is not the leader says so rather than passing the write on
// again, so a write is forwarded at most once.
func (m *Manager) serveForward(c net.Conn) {
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	for {
		req, err := readFrame(r)
		if err != nil {
			return
		}
		status, msg := forwardOK, ""
		if err := m.applyForwarded(req); err != nil {
			if nl, ok := AsNotLeader(err); ok {
				status, msg = forwardNotLeader, nl.LeaderHTTPAddr
			} else {
				status, msg = forwardError, err.Error()
			}
		}
		if err := w.WriteByte(status); err != nil {
			return
		}
		if err := writeFrame(w, []byte(msg)); err != nil {
			return
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

func (m *Manager) applyForwarded(req []byte) error {
	c, err := decodeCommand(req)
	if err != nil {
		return err
	}
	switch c.Op {
	case "set", "del", "clone", "vacuum":
	default:
		return fmt.Errorf("unknown op: %s", c.Op)
	}
	if m.raft.State() != raft.Leader {
		return &NotLeaderError{LeaderHTTPAddr: m.leaderHTTPAddr()}
	}
	return m.raft.Apply(req, applyTimeout).Error()
}

// forwardConn is a connection to the leader's Raft port kept for reuse.
type forwardConn struct {
	c net.Conn
	r *bufio.Reader
	w *bufio.Writer
}

// forwardPool keeps idle forwarding connections per address, and remembers
// the addresses that turned out not to accept them.
type forwardPool struct {
	mu          sync.Mutex
	idle        map[string][]*forwardConn
	unsupported map[string]time.Time // until when to go straight to HTTP
	closed      bool
}

// retryUnsupported is how long a follower sends writes for a v0.6 leader
// over HTTP before trying its Raft port again. Each try costs a round trip
// and an error in that leader's log, but the leader may have been upgraded.
const retryUnsupported = 30 * time.Second

func (p *forwardPool) isUnsupported(addr string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return time.Now().Before(p.unsupported[addr])
}

func (p *forwardPool) markUnsupported(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unsupported == nil {
		p.unsupported = map[string]time.Time{}
	}
	p.unsupported[addr] = time.Now().Add(retryUnsupported)
}

const maxIdleForward = 4

func (p *forwardPool) get(addr string) *forwardConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	cs := p.idle[addr]
	if len(cs) == 0 {
		return nil
	}
	fc := cs[len(cs)-1]
	p.idle[addr] = cs[:len(cs)-1]
	return fc
}

func (p *forwardPool) put(addr string, fc *forwardConn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.idle[addr]) >= maxIdleForward {
		_ = fc.c.Close()
		return
	}
	if p.idle == nil {
		p.idle = map[string][]*forwardConn{}
	}
	p.idle[addr] = append(p.idle[addr], fc)
}

func (p *forwardPool) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for _, cs := range p.idle {
		for _, fc := range cs {
			_ = fc.c.Close()
		}
	}
	p.idle = nil
}

func dialForward(addr string) (*forwardConn, error) {
	c, err := net.DialTimeout("tcp", addr, applyTimeout)
	if err != nil {
		return nil, err
	}
	fc := &forwardConn{c: c, r: bufio.NewReader(c), w: bufio.NewWriter(c)}
	if err := fc.w.WriteByte(forwardByte); err != nil {
		_ = c.Close()
		return nil, err
	}
	return fc, nil
}

// forwardTCP sends one command to the node at addr, its Raft address. A
// pooled connection the leader has since closed is replaced once.
func (m *Manager) forwardTCP(addr string, req []byte) error {
	if m.fwd.isUnsupported(addr) {
		return errForwardUnsupported
	}
	if fc := m.fwd.get(addr); fc != nil {
		sent, err := roundTrip(fc, req)
		if sent {
			m.fwd.put(addr, fc)
			return err
		}
		_ = fc.c.Close()
	}
	fc, err := dialForward(addr)
	if err != nil {
		return err
	}
	sent, err := roundTrip(fc, req)
	if !sent {
		_ = fc.c.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || isConnReset(err) {
			m.fwd.markUnsupported(addr)
			return errForwardUnsupported
		}
		return err
	}
	m.fwd.put(addr, fc)
	return err
}

// roundTrip sends req and reads the reply. sent reports whether a whole reply
// came back, which leaves the connection fit for reuse; err is then the
// leader's answer.
func roundTrip(fc *forwardConn, req []byte) (sent bool, err error) {
	_ = fc.c.SetDeadline(time.Now().Add(applyTimeout))
	defer func() { _ = fc.c.SetDeadline(time.Time{}) }()
	if err := writeFrame(fc.w, req); err != nil {
		return false, err
	}
	if err := fc.w.Flush(); err != nil {
		return false, err
	}
	status, err := fc.r.ReadByte()
	if err != nil {
		return false, err
	}
	msg, err := readFrame(fc.r)
	if err != nil {
		return false, err
	}
	switch status {
	case forwardOK:
		return true, nil
	case forwardNotLeader:
		return true, &NotLeaderError{LeaderHTTPAddr: string(msg)}
	case forwardError:
		return true, errors.New(string(msg))
	}
	return false, fmt.Errorf("unknown forward reply %d", status)
}

func isConnReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

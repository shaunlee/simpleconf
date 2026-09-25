package cluster

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/hashicorp/raft"
	"github.com/shaunlee/simpleconf/internal/wire"
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

// muxLayer is the StreamLayer given to the Raft transport: the Raft port,
// split by first byte between Raft and forwarded writes.
type muxLayer struct {
	*wire.Mux
	advertise net.Addr
}

func newMuxLayer(bind string, advertise *net.TCPAddr) (*muxLayer, error) {
	if advertise.IP == nil || advertise.IP.IsUnspecified() {
		return nil, errors.New("local bind address is not advertisable")
	}
	ln, err := net.Listen("tcp", bind)
	if err != nil {
		return nil, err
	}
	mux := wire.NewMux(ln, func(first byte) bool { return first == forwardByte })
	return &muxLayer{Mux: mux, advertise: advertise}, nil
}

func (l *muxLayer) Addr() net.Addr { return l.advertise }

func (l *muxLayer) Dial(addr raft.ServerAddress, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp", string(addr), timeout)
}

// A frame is a uvarint length followed by that many bytes. A request is one
// frame holding the command, encoded as it is in the Raft log. A reply is a
// status byte and one frame: empty for forwardOK, the leader's HTTP address
// for forwardNotLeader, the error text for forwardError.

// serveForward answers forwarded writes on one connection until it closes.
// A node that is not the leader says so rather than passing the write on
// again, so a write is forwarded at most once.
func (m *Manager) serveForward(c net.Conn) {
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	if b, err := r.ReadByte(); err != nil || b != forwardByte {
		return
	}
	for {
		req, err := wire.ReadFrame(r, maxForwardFrame)
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
		if err := wire.WriteFrame(w, []byte(msg)); err != nil {
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
	if err := wire.WriteFrame(fc.w, req); err != nil {
		return false, err
	}
	if err := fc.w.Flush(); err != nil {
		return false, err
	}
	status, err := fc.r.ReadByte()
	if err != nil {
		return false, err
	}
	msg, err := wire.ReadFrame(fc.r, maxForwardFrame)
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

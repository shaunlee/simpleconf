package peers

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/shaunlee/simpleconf/internal/db"
	"github.com/shaunlee/simpleconf/internal/wire"
)

// The peers protocol shares the peers port with the HTTP API it replaces.
// A connection opens with hello and the peer answers with the same line. A
// v0.7 or older peer port only speaks HTTP and answers "HTTP/1.1 400", which
// tells the sender to use HTTP with that peer.
const hello = "SCP1\r\n"

// Requests, after hello.
const (
	reqBatch    byte = 'B' // apply ops in order
	reqDocument byte = 'G' // the whole document, for Restore
)

// Ops in a batch.
const (
	opSet    byte = 's'
	opDel    byte = 'd'
	opClone  byte = 'c'
	opVacuum byte = 'v'
)

const (
	maxBatchOps   = 256
	maxBatchBytes = 1 << 20
	maxValue      = 64 << 20 // one key, value or message
	maxDocument   = 1 << 30  // Restore
	batchTimeout  = 5 * time.Second
	helloTimeout  = requestTimeout
)

// errPeerUnsupported means the peer's port answered hello with something
// else: it predates the peers protocol.
var errPeerUnsupported = errors.New("peer does not speak the peers protocol")

// op is a queued write as the peers protocol carries it.
type op struct {
	kind     byte
	key, to  string // clone: key is the source
	raw      []byte // set: the JSON text as the client sent it
	queuedAs syncOp // for log lines
}

// toOp reads a queued syncOp. The queue keeps the HTTP shape it has always
// had on disk, so records written by v0.7 and earlier still replay.
func toOp(s syncOp) (op, error) {
	o := op{queuedAs: s}
	switch {
	case s.Method == "PUT" && strings.HasPrefix(s.Path, "/db/"):
		o.kind, o.key, o.raw = opSet, strings.TrimPrefix(s.Path, "/db/"), s.Body
	case s.Method == "DELETE" && strings.HasPrefix(s.Path, "/db/"):
		o.kind, o.key = opDel, strings.TrimPrefix(s.Path, "/db/")
	case s.Method == "POST" && strings.HasPrefix(s.Path, "/clone/"):
		from, to, ok := strings.Cut(strings.TrimPrefix(s.Path, "/clone/"), "/")
		if !ok || strings.Contains(to, "/") {
			return op{}, fmt.Errorf("bad clone path %q", s.Path)
		}
		o.kind, o.key, o.to = opClone, from, to
	case s.Method == "POST" && s.Path == "/vacuum":
		o.kind = opVacuum
	default:
		return op{}, fmt.Errorf("unknown queued op %s %s", s.Method, s.Path)
	}
	return o, nil
}

func writeOp(w *bufio.Writer, o op) error {
	if err := w.WriteByte(o.kind); err != nil {
		return err
	}
	switch o.kind {
	case opSet:
		if err := wire.WriteFrame(w, []byte(o.key)); err != nil {
			return err
		}
		return wire.WriteFrame(w, o.raw)
	case opDel:
		return wire.WriteFrame(w, []byte(o.key))
	case opClone:
		if err := wire.WriteFrame(w, []byte(o.key)); err != nil {
			return err
		}
		return wire.WriteFrame(w, []byte(o.to))
	}
	return nil
}

func readOp(r *bufio.Reader) (op, error) {
	var o op
	kind, err := r.ReadByte()
	if err != nil {
		return o, err
	}
	o.kind = kind
	readString := func() (string, error) {
		b, err := wire.ReadFrame(r, maxValue)
		return string(b), err
	}
	switch kind {
	case opSet:
		if o.key, err = readString(); err != nil {
			return o, err
		}
		o.raw, err = wire.ReadFrame(r, maxValue)
	case opDel:
		o.key, err = readString()
	case opClone:
		if o.key, err = readString(); err != nil {
			return o, err
		}
		o.to, err = readString()
	case opVacuum:
	default:
		err = fmt.Errorf("unknown op %q", kind)
	}
	return o, err
}

// batchResult is the peer's answer to a batch. It applied the first done
// ops, of which the ones in rejected failed for good and are dropped. If
// done is short of the batch, stopped says why: the peer is refusing writes
// and the rest must be sent again later.
type batchResult struct {
	done     int
	rejected map[int]string
	stopped  string
}

func writeUvarint(w *bufio.Writer, n int) error {
	var b [binary.MaxVarintLen64]byte
	_, err := w.Write(b[:binary.PutUvarint(b[:], uint64(n))])
	return err
}

func readCount(r *bufio.Reader, max int) (int, error) {
	n, err := binary.ReadUvarint(r)
	if err != nil {
		return 0, err
	}
	if n > uint64(max) {
		return 0, fmt.Errorf("count %d is over the limit of %d", n, max)
	}
	return int(n), nil
}

// applyBatch applies ops in order, as the HTTP routes did one at a time:
// a write the peer refuses stops the batch, so that it and everything after
// it are sent again; any other error drops that one write.
func applyBatch(ops []op) batchResult {
	res := batchResult{rejected: map[int]string{}}
	for i, o := range ops {
		err := applyOp(o)
		if errors.Is(err, db.ErrWritesRefused) {
			res.stopped = err.Error()
			return res
		}
		if err != nil {
			res.rejected[i] = err.Error()
		}
		res.done = i + 1
	}
	return res
}

// applyOp applies one op to the local document. Tests replace it.
var applyOp = func(o op) error {
	switch o.kind {
	case opSet:
		return db.SetRaw(o.key, o.raw)
	case opDel:
		return db.Del(o.key)
	case opClone:
		return db.Clone(o.key, o.to)
	case opVacuum:
		db.Vacuum()
	}
	return nil
}

// serveTCP answers one connection of the peers protocol until it closes.
func serveTCP(c net.Conn) {
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(r, got); err != nil || string(got) != hello {
		return
	}
	if _, err := w.WriteString(hello); err != nil || w.Flush() != nil {
		return
	}
	for {
		kind, err := r.ReadByte()
		if err != nil {
			return
		}
		switch kind {
		case reqBatch:
			err = serveBatch(r, w)
		case reqDocument:
			err = wire.WriteFrame(w, []byte(db.Get("")))
		default:
			err = fmt.Errorf("unknown request %q", kind)
		}
		if err == nil {
			err = w.Flush()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				log.Printf("peers: %s: %v", c.RemoteAddr(), err)
			}
			return
		}
	}
}

func serveBatch(r *bufio.Reader, w *bufio.Writer) error {
	n, err := readCount(r, maxBatchOps)
	if err != nil {
		return err
	}
	ops := make([]op, n)
	for i := range ops {
		if ops[i], err = readOp(r); err != nil {
			return err
		}
	}
	res := applyBatch(ops)
	if err := writeUvarint(w, res.done); err != nil {
		return err
	}
	if err := writeUvarint(w, len(res.rejected)); err != nil {
		return err
	}
	for i, msg := range res.rejected {
		if err := writeUvarint(w, i); err != nil {
			return err
		}
		if err := wire.WriteFrame(w, []byte(msg)); err != nil {
			return err
		}
	}
	return wire.WriteFrame(w, []byte(res.stopped))
}

// peerConn is a worker's connection to its peer.
type peerConn struct {
	c net.Conn
	r *bufio.Reader
	w *bufio.Writer
}

// tcpAddr turns a configured peer address into host:port. Addresses are
// written as HTTP URLs in configs from before the peers protocol; an https
// one names a peer behind TLS, which only HTTP can reach.
func tcpAddr(addr string) (string, bool) {
	if strings.HasPrefix(addr, "https://") {
		return "", false
	}
	return strings.TrimSuffix(strings.TrimPrefix(addr, "http://"), "/"), true
}

func dialPeer(addr string) (*peerConn, error) {
	host, ok := tcpAddr(addr)
	if !ok {
		return nil, errPeerUnsupported
	}
	c, err := net.DialTimeout("tcp", host, helloTimeout)
	if err != nil {
		return nil, err
	}
	pc := &peerConn{c: c, r: bufio.NewReader(c), w: bufio.NewWriter(c)}
	_ = c.SetDeadline(time.Now().Add(helloTimeout))
	if _, err := pc.w.WriteString(hello); err != nil || pc.w.Flush() != nil {
		_ = c.Close()
		return nil, fmt.Errorf("peers: sending hello to %s failed", host)
	}
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(pc.r, got); err != nil || string(got) != hello {
		_ = c.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, err
		}
		return nil, errPeerUnsupported
	}
	_ = c.SetDeadline(time.Time{})
	return pc, nil
}

func (pc *peerConn) sendBatch(ops []op) (batchResult, error) {
	res := batchResult{rejected: map[int]string{}}
	_ = pc.c.SetDeadline(time.Now().Add(batchTimeout))
	defer func() { _ = pc.c.SetDeadline(time.Time{}) }()
	if err := pc.w.WriteByte(reqBatch); err != nil {
		return res, err
	}
	if err := writeUvarint(pc.w, len(ops)); err != nil {
		return res, err
	}
	for _, o := range ops {
		if err := writeOp(pc.w, o); err != nil {
			return res, err
		}
	}
	if err := pc.w.Flush(); err != nil {
		return res, err
	}
	var err error
	if res.done, err = readCount(pc.r, len(ops)); err != nil {
		return res, err
	}
	nrej, err := readCount(pc.r, res.done)
	if err != nil {
		return res, err
	}
	for range nrej {
		i, err := readCount(pc.r, res.done-1)
		if err != nil {
			return res, err
		}
		msg, err := wire.ReadFrame(pc.r, maxValue)
		if err != nil {
			return res, err
		}
		res.rejected[i] = string(msg)
	}
	stopped, err := wire.ReadFrame(pc.r, maxValue)
	res.stopped = string(stopped)
	return res, err
}

func (pc *peerConn) document() (string, error) {
	_ = pc.c.SetDeadline(time.Now().Add(requestTimeout * 5))
	defer func() { _ = pc.c.SetDeadline(time.Time{}) }()
	if err := pc.w.WriteByte(reqDocument); err != nil {
		return "", err
	}
	if err := pc.w.Flush(); err != nil {
		return "", err
	}
	b, err := wire.ReadFrame(pc.r, maxDocument)
	return string(b), err
}

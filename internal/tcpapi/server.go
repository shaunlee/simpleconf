package tcpapi

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/shaunlee/simpleconf/internal/cluster"
	"github.com/shaunlee/simpleconf/internal/db"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type Server struct {
	wg       sync.WaitGroup
	exit     atomic.Bool
	mu       sync.Mutex
	listener *net.TCPListener
}

func New() *Server {
	return &Server{}
}

func (p *Server) Listen(addr string) error {
	// "tcp" rather than "tcp4" so a wildcard address listens on both stacks:
	// localhost resolves to ::1 first on plenty of systems.
	raddr, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return err
	}
	lc, err := net.ListenTCP("tcp", raddr)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.listener = lc
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.listener = nil
		p.mu.Unlock()
		lc.Close()
	}()

	for !p.exit.Load() {
		conn, err := lc.AcceptTCP()
		if err != nil {
			if p.exit.Load() {
				break
			}
			return err
		}
		// Keepalive only speeds up noticing a dead peer; serve without it.
		_ = conn.SetKeepAlive(true)
		_ = conn.SetKeepAlivePeriod(10 * time.Second)
		p.wg.Add(1)
		go p.handle(conn)
	}
	p.wg.Wait()
	return nil
}

func (p *Server) Shutdown() {
	p.exit.Store(true)
	p.mu.Lock()
	if p.listener != nil {
		p.listener.Close()
	}
	p.mu.Unlock()
}

func (p *Server) handle(conn net.Conn) {
	defer p.wg.Done()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for !p.exit.Load() {
		l, err := readlineFlush(reader, writer)
		if err != nil {
			break
		}
		if len(l) == 0 {
			continue
		}
		switch l[0] {
		case '=':
			err = writeBulk(writer, db.Get(string(l[1:])))
		case '+':
			if len(l) == 1 {
				err = writelines(writer, "-ERR the key path is required\n")
			} else if nl, rerr := readlineFlush(reader, writer); rerr != nil {
				err = rerr
			} else {
				err = writeResult(writer, cluster.ApplySetRaw(string(l[1:]), nl))
			}
		case '-':
			if len(l) == 1 {
				err = writelines(writer, "-ERR the key path is required\n")
			} else {
				err = writeResult(writer, cluster.ApplyDelete(string(l[1:])))
			}
		case '<':
			if len(l) == 1 {
				err = writelines(writer, "-ERR the source key path is required\n")
			} else if nl, rerr := readlineFlush(reader, writer); rerr != nil {
				err = rerr
			} else if len(nl) <= 1 || nl[0] != '>' {
				err = writelines(writer, "-ERR the target key path is required\n")
			} else {
				err = writeResult(writer, cluster.ApplyClone(string(l[1:]), string(nl[1:])))
			}
		case '*':
			err = writeResult(writer, cluster.ApplyVacuum())
		case 'p', 'P':
			if bytes.EqualFold(l, []byte("PING")) {
				err = writelines(writer, "+PONG\n")
				break
			}
			fallthrough
		default:
			err = writelines(writer, "-ERR unknown command\n")
		}
		if err != nil {
			break
		}
	}
	writer.Flush()
}

// writeResult replies to a write command: +OK on success, the leader's
// address when this node is a follower, or the error otherwise.
func writeResult(writer *bufio.Writer, err error) error {
	if err == nil {
		return writelines(writer, "+OK\n")
	}
	if nl, ok := cluster.AsNotLeader(err); ok {
		return writelines(writer, fmt.Sprintf("-ERR not leader %s\n", nl.LeaderHTTPAddr))
	}
	return writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error()))
}

// readlineFlush flushes pending output before a read that would have to wait
// on the socket. Flushing here instead of after every command lets a pipelining
// client collect many replies per write syscall; a ping-pong client is
// unaffected, since it never has a further command buffered.
func readlineFlush(reader *bufio.Reader, writer *bufio.Writer) ([]byte, error) {
	if !hasBufferedLine(reader) {
		if err := writer.Flush(); err != nil {
			return nil, err
		}
	}
	return readline(reader)
}

func hasBufferedLine(reader *bufio.Reader) bool {
	n := reader.Buffered()
	if n == 0 {
		return false
	}
	b, err := reader.Peek(n)
	return err == nil && bytes.IndexByte(b, '\n') >= 0
}

func readline(reader *bufio.Reader) ([]byte, error) {
	if line, err := reader.ReadBytes('\n'); err != nil {
		return nil, err
	} else {
		return bytes.TrimSpace(line), nil
	}
}

// writeBulk writes a "$<len>\n<val>\n" reply without going through fmt,
// which dominated the cost of a GET.
func writeBulk(writer *bufio.Writer, val string) error {
	var n [20]byte
	writer.WriteByte('$')
	writer.Write(strconv.AppendInt(n[:0], int64(len(val)), 10))
	writer.WriteByte('\n')
	writer.WriteString(val)
	return writer.WriteByte('\n')
}

func writelines(writer *bufio.Writer, lines ...string) error {
	for _, l := range lines {
		if _, err := writer.WriteString(l); err != nil {
			return err
		}
	}
	return nil
}

package server

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/shaunlee/simpleconf/cluster"
	"github.com/shaunlee/simpleconf/db"
	"net"
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
		conn.SetKeepAlive(true)
		conn.SetKeepAlivePeriod(10 * time.Second)
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
outer:
	for !p.exit.Load() {
		if l, err := readlineFlush(reader, writer); err != nil {
			break
		} else if len(l) == 0 {
			continue
		} else {
			switch l[0] {
			case '=':
				k := string(l[1:])
				val := db.Get(k)
				if err := writelines(writer, fmt.Sprintf("$%d\n", len(val)), fmt.Sprintf("%s\n", val)); err != nil {
					break outer
				}
			case '+':
				if len(l) == 1 {
					if err := writelines(writer, "-ERR the key path is required\n"); err != nil {
						break outer
					}
				} else if nl, err := readlineFlush(reader, writer); err != nil {
					break outer
				} else {
					k := string(l[1:])
					var v any
					if err := json.Unmarshal(nl, &v); err != nil {
						if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
							break outer
						}
					} else if err := cluster.ApplySet(k, v); err != nil {
						if nl, ok := cluster.AsNotLeader(err); ok {
							if err := writelines(writer, fmt.Sprintf("-ERR not leader %s\n", nl.LeaderHTTPAddr)); err != nil {
								break outer
							}
							continue
						}
						if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
							break outer
						}
					} else {
						if err := writelines(writer, "+OK\n"); err != nil {
							break outer
						}
					}
				}
			case '-':
				if len(l) == 1 {
					if err := writelines(writer, "-ERR the key path is required\n"); err != nil {
						break outer
					}
				} else {
					k := string(l[1:])
					if err := cluster.ApplyDelete(k); err != nil {
						if nl, ok := cluster.AsNotLeader(err); ok {
							if err := writelines(writer, fmt.Sprintf("-ERR not leader %s\n", nl.LeaderHTTPAddr)); err != nil {
								break outer
							}
							continue
						}
						if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
							break outer
						}
						continue
					}
					if err := writelines(writer, "+OK\n"); err != nil {
						break outer
					}
				}
			case '<':
				if len(l) == 1 {
					if err := writelines(writer, "-ERR the source key path is required\n"); err != nil {
						break outer
					}
				} else if nl, err := readlineFlush(reader, writer); err != nil {
					break outer
				} else if len(nl) <= 1 || nl[0] != '>' {
					if err := writelines(writer, "-ERR the target key path is required\n"); err != nil {
						break outer
					}
				} else {
					fk := string(l[1:])
					tk := string(nl[1:])
					if err := cluster.ApplyClone(fk, tk); err != nil {
						if nlErr, ok := cluster.AsNotLeader(err); ok {
							if err := writelines(writer, fmt.Sprintf("-ERR not leader %s\n", nlErr.LeaderHTTPAddr)); err != nil {
								break outer
							}
							continue
						}
						if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
							break outer
						}
						continue
					}
					if err := writelines(writer, "+OK\n"); err != nil {
						break outer
					}
				}
			case '*':
				if err := cluster.ApplyVacuum(); err != nil {
					if nl, ok := cluster.AsNotLeader(err); ok {
						if err := writelines(writer, fmt.Sprintf("-ERR not leader %s\n", nl.LeaderHTTPAddr)); err != nil {
							break outer
						}
						continue
					}
					if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
						break outer
					}
					continue
				}
				if err := writelines(writer, "+OK\n"); err != nil {
					break outer
				}
			case 'p', 'P':
				if bytes.EqualFold(l, []byte("PING")) {
					if err := writelines(writer, "+PONG\n"); err != nil {
						break outer
					}
					continue
				}
				fallthrough
			default:
				if err := writelines(writer, "-ERR unknown command\n"); err != nil {
					break outer
				}
			}
		}
	}
	writer.Flush()
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

func writelines(writer *bufio.Writer, lines ...string) error {
	for _, l := range lines {
		if _, err := writer.WriteString(l); err != nil {
			return err
		}
	}
	return nil
}

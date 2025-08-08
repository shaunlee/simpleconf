package server

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/shaunlee/simpleconf/db"
	"net"
	"sync"
	"time"
)

type Server struct {
	wg   sync.WaitGroup
	exit bool
}

func New() *Server {
	return &Server{}
}

func (p *Server) Listen(addr string) error {
	raddr, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return err
	}
	lc, err := net.ListenTCP("tcp4", raddr)
	if err != nil {
		return err
	}
	defer lc.Close()
	for !p.exit {
		conn, err := lc.AcceptTCP()
		if err != nil {
			return err
		}
		conn.SetKeepAlivePeriod(10 * time.Second)
		p.wg.Add(1)
		go p.handle(conn)
	}
	p.wg.Wait()
	return nil
}

func (p *Server) Shutdown() {
	p.exit = true
}

func (p *Server) handle(conn net.Conn) {
	defer p.wg.Done()
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	for !p.exit {
		if l, err := readline(reader); err != nil {
			break
		} else if len(l) == 0 {
			continue
		} else {
			switch l[0] {
			case '=':
				if len(l) == 1 {
					if err := writelines(writer, fmt.Sprintf("$%d\n", len(db.Configuration)), fmt.Sprintf("%s\n", db.Configuration)); err != nil {
						break
					}
				} else {
					k := string(l[1:])
					val := db.Get(k)
					if err := writelines(writer, fmt.Sprintf("$%d\n", len(val)), fmt.Sprintf("%s\n", val)); err != nil {
						break
					}
				}
			case '+':
				if len(l) == 1 {
					if err := writelines(writer, "-ERR the key path is required\n"); err != nil {
						break
					}
				} else if nl, err := readline(reader); err != nil {
					break
				} else {
					k := string(l[1:])
					var v any
					if err := json.Unmarshal(nl, &v); err != nil {
						if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
							break
						}
					} else if err := db.Set(k, v); err != nil {
						if err := writelines(writer, fmt.Sprintf("-ERR %s\n", err.Error())); err != nil {
							break
						}
					} else {
						if err := writelines(writer, "+OK\n"); err != nil {
							break
						}
					}
				}
			case '-':
				if len(l) == 1 {
					if err := writelines(writer, "-ERR the key path is required\n"); err != nil {
						break
					}
				} else {
					k := string(l[1:])
					db.Del(k)
					if err := writelines(writer, "+OK\n"); err != nil {
						break
					}
				}
			case '<':
				if len(l) == 1 {
					if err := writelines(writer, "-ERR the source key path is required\n"); err != nil {
						break
					}
				} else if nl, err := readline(reader); err != nil {
					break
				} else if len(nl) <= 1 || nl[0] != '>' {
					if err := writelines(writer, "-ERR the target key path is required\n"); err != nil {
						break
					}
				} else {
					fk := string(l[1:])
					tk := string(nl[1:])
					db.Clone(fk, tk)
					if err := writelines(writer, "+OK\n"); err != nil {
						break
					}
				}
			case '*':
				db.Vacuum()
				if err := writelines(writer, "+OK\n"); err != nil {
					break
				}
			case 'p', 'P':
				if bytes.EqualFold(l, []byte("PING")) {
					if err := writelines(writer, "+PONG\n"); err != nil {
						break
					}
					continue
				}
				fallthrough
			default:
				if err := writelines(writer, "-ERR unknown command\n"); err != nil {
					break
				}
			}
		}
		if err := writer.Flush(); err != nil {
			break
		}
	}
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
			break
		}
	}
	return nil
}

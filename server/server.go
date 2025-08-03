package server

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/shaunlee/simpleconf/db"
	"net"
)

type Server struct {
	exit bool
}

func New() *Server {
	return &Server{}
}

func (p *Server) Listen(addr string) error {
	lc, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer lc.Close()
	for !p.exit {
		conn, err := lc.Accept()
		if err != nil {
			return err
		}
		go p.handle(conn)
	}
	return nil
}

func (p *Server) Shutdown() {
	p.exit = true
}

func (p *Server) handle(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	for !p.exit {
		if l, err := readline(reader); err != nil {
			break
		} else if len(l) == 0 {
			continue
		} else {
			switch l[0] {
			case '=':
				if len(l) == 1 {
					fmt.Fprintf(conn, "$%d\n%s\n", len(db.Configuration), db.Configuration)
				} else {
					k := string(l[1:])
					val := db.Get(k)
					fmt.Fprintf(conn, "$%d\n%s\n", len(val), val)
				}
			case '+':
				if len(l) == 1 {
					fmt.Fprintf(conn, "-ERR the key path is required\n")
				} else if nl, err := readline(reader); err != nil {
					break
				} else {
					k := string(l[1:])
					var v any
					if err := json.Unmarshal(nl, &v); err != nil {
						fmt.Fprintf(conn, "-ERR %s\n", err.Error())
					} else if err := db.Set(k, v); err != nil {
						fmt.Fprintf(conn, "-ERR %s\n", err.Error())
					} else {
						fmt.Fprintf(conn, "+OK\n")
					}
				}
			case '-':
				if len(l) == 1 {
					fmt.Fprintf(conn, "-ERR the key path is required\n")
				} else {
					k := string(l[1:])
					db.Del(k)
					fmt.Fprintf(conn, "+OK\n")
				}
			case '<':
				if len(l) == 1 {
					fmt.Fprintf(conn, "-ERR the source key path is required\n")
				} else if nl, err := readline(reader); err != nil {
					break
				} else if len(nl) <= 1 || nl[0] != '>' {
					fmt.Fprintf(conn, "-ERR the target key path is required\n")
				} else {
					fk := string(l[1:])
					tk := string(nl[1:])
					db.Clone(fk, tk)
					fmt.Fprintf(conn, "+OK\n")
				}
			case '*':
				db.Vacuum()
				fmt.Fprintf(conn, "+OK\n")
			case 'p':
				fallthrough
			case 'P':
				if bytes.EqualFold(l, []byte("PING")) {
					fmt.Fprintf(conn, "+PONG\n")
					continue
				}
				fallthrough
			default:
				fmt.Fprintf(conn, "-ERR unknown command\n")
			}
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

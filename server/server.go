package server

import (
	"bufio"
	"bytes"
	"fmt"
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
		if l, err := reader.ReadBytes('\n'); err != nil {
			break
		} else {
			l = bytes.TrimSpace(l)
			if len(l) == 0 {
				continue
			}
			switch l[0] {
			case '=':
				if len(l) == 1 {
					fmt.Fprintf(conn, "%s\n", db.Configuration)
				} else {
					fmt.Fprintf(conn, "%s\n", db.Get(string(l[1:])))
				}
			case '+':
				//
			case '*':
				//
			case '-':
				db.Del(string(l[1:]))
				fmt.Fprintf(conn, "ok\n")
			case '<':
				//
				//VACUUM
			}
		}
	}
}

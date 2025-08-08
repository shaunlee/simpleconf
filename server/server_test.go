package server

import (
	"bufio"
	"fmt"
	"net"
	"sync"
	"testing"
)

var pool = sync.Pool{
	New: func() any {
		nc, _ := net.Dial("tcp4", "127.0.0.1:23466")
		return nc
	},
}

func TestTcpSet(t *testing.T) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpSet(b *testing.B) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "+bench\n\"mark\"\n")
		reader.ReadBytes('\n')
	}
}

func TestTcpGet(t *testing.T) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "=\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
	v, _ = reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpGet(b *testing.B) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "=bench\n")
		reader.ReadBytes('\n')
		reader.ReadBytes('\n')
	}
}

func TestTcpClone(t *testing.T) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "<bench\n>mark\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpClone(b *testing.B) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "<bench\n>mark\n")
		reader.ReadBytes('\n')
	}
}

func TestTcpDel(t *testing.T) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	fmt.Fprintf(conn, "-bench\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpDel(b *testing.B) {
	conn := pool.Get().(net.Conn)
	defer pool.Put(conn)
	reader := bufio.NewReader(conn)
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "-bench\n")
		reader.ReadBytes('\n')
	}
}

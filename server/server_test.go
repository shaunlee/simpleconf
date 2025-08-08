package server

import (
	"bufio"
	"fmt"
	"net"
	"testing"
)

var (
	conn   net.Conn
	reader *bufio.Reader
)

func init() {
	nc, _ := net.Dial("tcp", "127.0.0.1:23466")
	conn = nc
	reader = bufio.NewReader(conn)

	//fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	//v, _ := reader.ReadBytes('\n')
	//fmt.Print(string(v))
	//v, _ = reader.ReadBytes('\n')
	//fmt.Print(string(v))
}

func TestTcpSet(t *testing.T) {
	fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "+bench\n\"mark\"\n")
		reader.ReadBytes('\n')
	}
}

func TestTcpGet(t *testing.T) {
	fmt.Fprintf(conn, "=\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
	v, _ = reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpGet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "=bench\n")
		reader.ReadBytes('\n')
		reader.ReadBytes('\n')
	}
}

func TestTcpClone(t *testing.T) {
	fmt.Fprintf(conn, "<bench\n>mark\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpClone(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "<bench\n>mark\n")
		reader.ReadBytes('\n')
	}
}

func TestTcpDel(t *testing.T) {
	fmt.Fprintf(conn, "-bench\n")
	v, _ := reader.ReadBytes('\n')
	fmt.Print(string(v))
}

func BenchmarkTcpDel(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "-bench\n")
		reader.ReadBytes('\n')
	}
}

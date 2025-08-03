package server

import (
	"bufio"
	"fmt"
	"net"
	"testing"
)

var (
	conn net.Conn
	reader *bufio.Reader
)

func init() {
	nc, _ := net.Dial("tcp", "127.0.0.1:23466")
	conn = nc
	reader = bufio.NewReader(conn)

	fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	reader.ReadBytes('\n')
}

func BenchmarkTcpGet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "=bench\n")
		reader.ReadBytes('\n')
	}
}

func BenchmarkTcpSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "+bench\n\"mark\"\n")
		reader.ReadBytes('\n')
	}
}

func BenchmarkTcpDel(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "-bench\n")
		reader.ReadBytes('\n')
	}
}

func BenchmarkTcpClone(b *testing.B) {
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "<bench\n>mark\n")
		reader.ReadBytes('\n')
	}
}

package server

import (
	"bufio"
	"fmt"
	"net"
	"testing"
)

func BenchmarkGet(b *testing.B) {
	conn, _ := net.Dial("tcp", "127.0.0.1:23466")
	defer conn.Close()
	reader := bufio.NewReader(conn)

	fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	reader.ReadBytes('\n')
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "=bench\n")
		reader.ReadBytes('\n')
	}
}

func BenchmarkSet(b *testing.B) {
	conn, _ := net.Dial("tcp", "127.0.0.1:23466")
	defer conn.Close()
	reader := bufio.NewReader(conn)

	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "+bench\n\"mark\"\n")
		reader.ReadBytes('\n')
	}
}

func BenchmarkDel(b *testing.B) {
	conn, _ := net.Dial("tcp", "127.0.0.1:23466")
	defer conn.Close()
	reader := bufio.NewReader(conn)

	fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	reader.ReadBytes('\n')
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "-bench\n")
		reader.ReadBytes('\n')
	}
}

func BenchmarkClone(b *testing.B) {
	conn, _ := net.Dial("tcp", "127.0.0.1:23466")
	defer conn.Close()
	reader := bufio.NewReader(conn)

	fmt.Fprintf(conn, "+bench\n\"mark\"\n")
	reader.ReadBytes('\n')
	for i := 0; i < b.N; i++ {
		fmt.Fprintf(conn, "<bench\n>mark\n")
		reader.ReadBytes('\n')
	}
}

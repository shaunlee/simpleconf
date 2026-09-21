package tcpapi

import (
	"bufio"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shaunlee/simpleconf/internal/db"
)

var (
	benchOnce sync.Once
	benchAddr string
)

func benchServer(b *testing.B) string {
	b.Helper()
	benchOnce.Do(func() {
		db.DisableAOF()
		db.Init(b.TempDir())

		srv := New()
		go srv.Listen("127.0.0.1:0")

		for i := 0; i < 1000; i++ {
			srv.mu.Lock()
			lc := srv.listener
			srv.mu.Unlock()
			if lc != nil {
				benchAddr = lc.Addr().String()
				return
			}
			time.Sleep(time.Millisecond)
		}
		b.Fatal("server did not start")
	})
	return benchAddr
}

func benchConn(b *testing.B) (net.Conn, *bufio.Reader) {
	b.Helper()
	conn, err := net.Dial("tcp", benchServer(b))
	if err != nil {
		b.Fatalf("dial failed: %v", err)
	}
	return conn, bufio.NewReader(conn)
}

// serial: one connection, strict request -> response ping-pong
func benchSerial(b *testing.B, req string, respLines int) {
	benchServer(b)
	db.Set("bench", "mark")

	conn, r := benchConn(b)
	defer conn.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := conn.Write([]byte(req)); err != nil {
			b.Fatalf("write failed: %v", err)
		}
		for j := 0; j < respLines; j++ {
			if _, err := r.ReadBytes('\n'); err != nil {
				b.Fatalf("read failed: %v", err)
			}
		}
	}
}

// parallel: one connection per goroutine
func benchParallel(b *testing.B, req string, respLines int) {
	benchServer(b)
	db.Set("bench", "mark")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		conn, r := benchConn(b)
		defer conn.Close()
		for pb.Next() {
			if _, err := conn.Write([]byte(req)); err != nil {
				b.Fatalf("write failed: %v", err)
			}
			for j := 0; j < respLines; j++ {
				if _, err := r.ReadBytes('\n'); err != nil {
					b.Fatalf("read failed: %v", err)
				}
			}
		}
	})
}

const (
	getReq   = "=bench\n"
	setReq   = "+bench\n\"mark\"\n"
	delReq   = "-bench\n"
	cloneReq = "<bench\n>mark\n"
)

func BenchmarkTcpGet(b *testing.B)   { benchSerial(b, getReq, 2) }
func BenchmarkTcpSet(b *testing.B)   { benchSerial(b, setReq, 1) }
func BenchmarkTcpDel(b *testing.B)   { benchSerial(b, delReq, 1) }
func BenchmarkTcpClone(b *testing.B) { benchSerial(b, cloneReq, 1) }

func BenchmarkTcpGetParallel(b *testing.B)   { benchParallel(b, getReq, 2) }
func BenchmarkTcpSetParallel(b *testing.B)   { benchParallel(b, setReq, 1) }
func BenchmarkTcpDelParallel(b *testing.B)   { benchParallel(b, delReq, 1) }
func BenchmarkTcpCloneParallel(b *testing.B) { benchParallel(b, cloneReq, 1) }

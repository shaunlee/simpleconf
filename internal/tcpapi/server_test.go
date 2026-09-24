package tcpapi

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/shaunlee/simpleconf/internal/cluster"
)

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("read line failed: %v", err)
	}
	return line
}

func TestReadline(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("  hello world  \n"))
	line, err := readline(r)
	if err != nil {
		t.Fatalf("readline returned error: %v", err)
	}
	if got, want := string(line), "hello world"; got != want {
		t.Fatalf("line mismatch: got %q want %q", got, want)
	}
}

func TestWritelines(t *testing.T) {
	var sb strings.Builder
	w := bufio.NewWriter(&sb)
	if err := writelines(w, "+OK\n", "$4\n", "test\n"); err != nil {
		t.Fatalf("writelines returned error: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush returned error: %v", err)
	}
	if got, want := sb.String(), "+OK\n$4\ntest\n"; got != want {
		t.Fatalf("written content mismatch: got %q want %q", got, want)
	}
}

func TestHandleCommands(t *testing.T) {
	srv := New()
	sc, cc := net.Pipe()
	defer cc.Close()
	srv.wg.Add(1)
	go srv.handle(sc)

	r := bufio.NewReader(cc)

	if _, err := cc.Write([]byte("+\n")); err != nil {
		t.Fatalf("write bad set failed: %v", err)
	}
	if got, want := readLine(t, r), "-ERR the key path is required\n"; got != want {
		t.Fatalf("set missing key response mismatch: got %q want %q", got, want)
	}

	if _, err := cc.Write([]byte("+bench\n{\n")); err != nil {
		t.Fatalf("write invalid set failed: %v", err)
	}
	if got := readLine(t, r); !strings.HasPrefix(got, "-ERR ") {
		t.Fatalf("invalid json should return ERR, got %q", got)
	}

	if _, err := cc.Write([]byte("+bench\n\"mark\"\n")); err != nil {
		t.Fatalf("write set failed: %v", err)
	}
	if got, want := readLine(t, r), "+OK\n"; got != want {
		t.Fatalf("set response mismatch: got %q want %q", got, want)
	}

	if _, err := cc.Write([]byte("=bench\n")); err != nil {
		t.Fatalf("write get failed: %v", err)
	}
	if got, want := readLine(t, r), "$6\n"; got != want {
		t.Fatalf("get length mismatch: got %q want %q", got, want)
	}
	if got, want := readLine(t, r), "\"mark\"\n"; got != want {
		t.Fatalf("get value mismatch: got %q want %q", got, want)
	}

	if _, err := cc.Write([]byte("<bench\n>copy\n")); err != nil {
		t.Fatalf("write clone failed: %v", err)
	}
	if got, want := readLine(t, r), "+OK\n"; got != want {
		t.Fatalf("clone response mismatch: got %q want %q", got, want)
	}

	if _, err := cc.Write([]byte("-bench\n")); err != nil {
		t.Fatalf("write del failed: %v", err)
	}
	if got, want := readLine(t, r), "+OK\n"; got != want {
		t.Fatalf("del response mismatch: got %q want %q", got, want)
	}

	if _, err := cc.Write([]byte("UNKNOWN\n")); err != nil {
		t.Fatalf("write unknown failed: %v", err)
	}
	if got, want := readLine(t, r), "-ERR unknown command\n"; got != want {
		t.Fatalf("unknown response mismatch: got %q want %q", got, want)
	}
}

// pipeServer runs handle on one end of an in-memory pipe and returns the other.
func pipeServer(t *testing.T) (*Server, net.Conn, *bufio.Reader) {
	t.Helper()
	srv := New()
	sc, cc := net.Pipe()
	t.Cleanup(func() { cc.Close() })
	srv.wg.Add(1)
	go srv.handle(sc)
	return srv, cc, bufio.NewReader(cc)
}

func waitGroupDone(t *testing.T, srv *Server) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		srv.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return")
	}
}

func TestHandleMisc(t *testing.T) {
	_, cc, r := pipeServer(t)

	cases := []struct {
		name, req, want string
	}{
		{"empty line then ping", "\nPING\n", "+PONG\n"},
		{"lower-case ping", "ping\n", "+PONG\n"},
		{"p-prefixed unknown", "pong\n", "-ERR unknown command\n"},
		{"del without key", "-\n", "-ERR the key path is required\n"},
		{"clone without source", "<\n", "-ERR the source key path is required\n"},
		{"clone without target marker", "<a\nb\n", "-ERR the target key path is required\n"},
		{"clone with empty target", "<a\n>\n", "-ERR the target key path is required\n"},
		{"vacuum", "*\n", "+OK\n"},
	}
	for _, c := range cases {
		if _, err := cc.Write([]byte(c.req)); err != nil {
			t.Fatalf("%s: write failed: %v", c.name, err)
		}
		if got := readLine(t, r); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestHandleDisconnectMidCommand(t *testing.T) {
	for _, req := range []string{"+key\n", "<key\n"} {
		srv, cc, _ := pipeServer(t)
		if _, err := cc.Write([]byte(req)); err != nil {
			t.Fatalf("%q: write failed: %v", req, err)
		}
		cc.Close()
		waitGroupDone(t, srv)
	}
}

func TestListenAndShutdown(t *testing.T) {
	srv := New()
	errc := make(chan error, 1)
	go func() { errc <- srv.Listen("127.0.0.1:0") }()

	var addr string
	for i := 0; i < 1000 && addr == ""; i++ {
		srv.mu.Lock()
		if srv.listener != nil {
			addr = srv.listener.Addr().String()
		}
		srv.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if addr == "" {
		t.Fatal("server did not start")
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial failed: %v", err)
	}
	if _, err := conn.Write([]byte("PING\n")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if got, want := readLine(t, bufio.NewReader(conn)), "+PONG\n"; got != want {
		t.Fatalf("ping response mismatch: got %q want %q", got, want)
	}

	// Listen waits for open connections, so close ours after shutting down.
	srv.Shutdown()
	conn.Close()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Listen returned error after Shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Listen did not return after Shutdown")
	}
}

func TestListenErrors(t *testing.T) {
	if err := New().Listen("127.0.0.1:notaport"); err == nil {
		t.Fatal("expected error for an unresolvable address")
	}

	lc, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer lc.Close()
	if err := New().Listen(lc.Addr().String()); err == nil {
		t.Fatal("expected error for an address already in use")
	}
}

func TestShutdownBeforeListen(t *testing.T) {
	New().Shutdown()
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestWriteErrors(t *testing.T) {
	w := bufio.NewWriterSize(errWriter{}, 16)
	w.WriteString("pending")
	if _, err := readlineFlush(bufio.NewReader(strings.NewReader("")), w); err == nil {
		t.Fatal("readlineFlush should return the flush error")
	}

	w = bufio.NewWriterSize(errWriter{}, 16)
	if err := writelines(w, strings.Repeat("x", 64)); err == nil {
		t.Fatal("writelines should return the write error")
	}
}

func TestWriteResult(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"ok", nil, "+OK\n"},
		{"not leader", &cluster.NotLeaderError{LeaderHTTPAddr: "10.0.0.1:23456"}, "-ERR not leader 10.0.0.1:23456\n"},
		{"other error", errors.New("boom"), "-ERR boom\n"},
	}
	for _, c := range cases {
		var sb strings.Builder
		w := bufio.NewWriter(&sb)
		if err := writeResult(w, c.err); err != nil {
			t.Fatalf("%s: writeResult returned error: %v", c.name, err)
		}
		w.Flush()
		if got := sb.String(); got != c.want {
			t.Fatalf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

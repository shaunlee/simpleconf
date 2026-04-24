package server

import (
	"bufio"
	"net"
	"strings"
	"testing"
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

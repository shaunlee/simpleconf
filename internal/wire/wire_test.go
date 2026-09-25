package wire

import (
	"bufio"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFrames(t *testing.T) {
	var buf strings.Builder
	w := bufio.NewWriter(&buf)
	for _, s := range []string{"", "a", strings.Repeat("x", 300)} {
		if err := WriteFrame(w, []byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(strings.NewReader(buf.String()))
	for _, want := range []string{"", "a", strings.Repeat("x", 300)} {
		got, err := ReadFrame(r, 1000)
		if err != nil || string(got) != want {
			t.Fatalf("got %q, %v want %q", got, err, want)
		}
	}
	if _, err := ReadFrame(r, 1000); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last frame: %v", err)
	}

	r = bufio.NewReader(strings.NewReader(buf.String()[3:])) // the 300-byte frame, after 1 + 2 bytes
	if _, err := ReadFrame(r, 299); err == nil {
		t.Fatal("a frame over the limit should fail")
	}
}

func dialSend(t *testing.T, addr, msg string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := io.WriteString(c, msg); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestMuxRoutesByFirstByte(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := NewMux(ln, func(first byte) bool { return first == 'X' })
	defer m.Close()

	// Before a handler is set, matching connections are closed.
	c := dialSend(t, ln.Addr().String(), "Xearly")
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read on an unhandled connection: %v", err)
	}

	got := make(chan string, 1)
	m.SetHandler(func(c net.Conn) {
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	})
	dialSend(t, ln.Addr().String(), "Xhandled\n")
	if line := <-got; line != "Xhandled\n" {
		t.Fatalf("handler read %q, want the first byte kept", line)
	}

	dialSend(t, ln.Addr().String(), "GET / HTTP/1.1\r\n")
	other, err := m.Accept()
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(other).ReadString('\n')
	if line != "GET / HTTP/1.1\r\n" {
		t.Fatalf("Accept's connection read %q", line)
	}
	_ = other.Close()
	if m.Addr().String() != ln.Addr().String() {
		t.Fatalf("Addr = %v", m.Addr())
	}
}

func TestMuxClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := NewMux(ln, func(first byte) bool { return first == 'X' })
	entered := make(chan struct{})
	left := make(chan struct{})
	m.SetHandler(func(c net.Conn) {
		close(entered)
		_, _ = c.Read(make([]byte, 16)) // until Close closes it
		close(left)
	})
	dialSend(t, ln.Addr().String(), "X")
	<-entered
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-left:
	case <-time.After(2 * time.Second):
		t.Fatal("Close left the handled connection open")
	}
	if _, err := m.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

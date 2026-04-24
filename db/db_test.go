package db

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func resetConfig() {
	configMu.Lock()
	configuration = "{}"
	configMu.Unlock()
}

func TestSetGetDelClone(t *testing.T) {
	resetConfig()

	if err := setonly("bench", "mark"); err != nil {
		t.Fatalf("setonly failed: %v", err)
	}
	if got, want := Get("bench"), "\"mark\""; got != want {
		t.Fatalf("Get(bench) mismatch: got %q want %q", got, want)
	}
	if got := Get(""); !strings.Contains(got, "\"bench\":\"mark\"") {
		t.Fatalf("Get(\"\") should contain bench field, got %q", got)
	}

	Clone("bench", "bench_copy")
	if got, want := Get("bench_copy"), "\"mark\""; got != want {
		t.Fatalf("Clone result mismatch: got %q want %q", got, want)
	}

	Clone("missing", "ignored")
	if got := Get("ignored"); got != "" {
		t.Fatalf("missing source clone should not write target, got %q", got)
	}

	delonly("bench")
	if got := Get("bench"); got != "" {
		t.Fatalf("delonly should delete value, got %q", got)
	}
}

func TestReadline(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("one\n"))
	if got := readline(r); string(got) != "one" {
		t.Fatalf("readline mismatch: got %q", string(got))
	}

	r = bufio.NewReader(strings.NewReader(""))
	if got := readline(r); got != nil {
		t.Fatalf("readline on EOF should return nil, got %q", string(got))
	}
}

func TestInitReloadAndClose(t *testing.T) {
	resetConfig()
	tmp := t.TempDir()

	Init(tmp)
	defer Close()

	if err := Set("a", "b"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	Del("a")
	if err := Set("x", map[string]any{"y": 1}); err != nil {
		t.Fatalf("Set object failed: %v", err)
	}
	wg.Wait()
	Close()

	resetConfig()
	Init(tmp)
	defer Close()
	if got, want := Get("x.y"), "1"; got != want {
		t.Fatalf("reloaded value mismatch: got %q want %q", got, want)
	}
	if got := Get("a"); got != "" {
		t.Fatalf("deleted key should stay deleted after reload, got %q", got)
	}

	Close(true)
	if _, err := os.Stat(dbfn); err != nil {
		t.Fatalf("db file should exist after Close(true): %v", err)
	}
}

func BenchmarkGet(b *testing.B) {
	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		Get("bench")
	}
}

func BenchmarkSet(b *testing.B) {
	for i := 0; i < b.N; i++ {
		setonly("bench", "mark")
	}
}

func BenchmarkDel(b *testing.B) {
	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		delonly("bench")
	}
}

func BenchmarkClone(b *testing.B) {
	setonly("bench", "mark")
	for i := 0; i < b.N; i++ {
		Clone("bench", "mark")
	}
}

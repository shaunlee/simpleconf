package cluster

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/hashicorp/raft"
	"github.com/shaunlee/simpleconf/internal/db"
)

// useDB points the db package at a fresh directory for one test.
func useDB(t *testing.T) {
	t.Helper()
	db.Init(t.TempDir())
	t.Cleanup(func() { db.Close() })
}

// useDefault installs m as the package default for one test.
func useDefault(t *testing.T, m *Manager) {
	t.Helper()
	prev := getDefault()
	SetDefault(m)
	t.Cleanup(func() { SetDefault(prev) })
}

func TestNormalizeHTTPAddr(t *testing.T) {
	cases := map[string]string{
		"":                  "",
		"   ":               "",
		" 127.0.0.1:8080 ":  "http://127.0.0.1:8080",
		"http://a:1":        "http://a:1",
		"https://a:1":       "https://a:1",
		"example.com:23456": "http://example.com:23456",
	}
	for in, want := range cases {
		if got := normalizeHTTPAddr(in); got != want {
			t.Fatalf("normalizeHTTPAddr(%q) = %q want %q", in, got, want)
		}
	}
}

func TestDecodeErrors(t *testing.T) {
	for _, raw := range []string{"{", "1 2", "1 {"} {
		if _, err := decodeJSON([]byte(raw)); err == nil {
			t.Fatalf("decodeJSON(%q) should fail", raw)
		}
	}
	for _, raw := range []string{"{", `{"op":"del"} {}`, `{"op":"del"} {`} {
		if _, err := decodeCommand([]byte(raw)); err == nil {
			t.Fatalf("decodeCommand(%q) should fail", raw)
		}
	}
}

func TestFSMApplyBadLog(t *testing.T) {
	if _, ok := (&fsm{}).Apply(&raft.Log{Data: []byte("{")}).(error); !ok {
		t.Fatal("Apply should return the decode error")
	}
}

func TestApplyLocal(t *testing.T) {
	useDB(t)

	steps := []struct {
		c    command
		key  string
		want string
	}{
		{command{Op: "set", Key: "a", Value: map[string]any{"b": "x"}}, "a.b", `"x"`},
		{command{Op: "clone", FromKey: "a", ToKey: "c"}, "c.b", `"x"`},
		{command{Op: "del", Key: "a"}, "a", ""},
		{command{Op: "vacuum"}, "c.b", `"x"`},
	}
	for _, s := range steps {
		if err := applyLocal(s.c); err != nil {
			t.Fatalf("%s: %v", s.c.Op, err)
		}
		if got := db.Get(s.key); got != s.want {
			t.Fatalf("after %s, Get(%q) = %q want %q", s.c.Op, s.key, got, s.want)
		}
	}
	if err := applyLocal(command{Op: "bogus"}); err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("unknown op error = %v", err)
	}
}

// With Raft disabled, every package-level write goes straight to the db.
func TestStandaloneApply(t *testing.T) {
	useDB(t)
	m, err := Start(Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	useDefault(t, m)

	if err := ApplySet("s", "v"); err != nil {
		t.Fatal(err)
	}
	if err := ApplySetRaw("r", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyClone("r", "r2"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDelete("s"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyVacuum(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"s": "", "r.n": "1", "r2.n": "1"} {
		if got := db.Get(k); got != want {
			t.Fatalf("Get(%q) = %q want %q", k, got, want)
		}
	}
	var je *db.JSONError
	if err := ApplySetRaw("bad", []byte("{")); !errors.As(err, &je) {
		t.Fatalf("bad JSON error = %v, want *db.JSONError", err)
	}
}

type memSink struct {
	bytes.Buffer
	writeErr  error
	cancelled bool
	closed    bool
}

func (s *memSink) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.Buffer.Write(p)
}
func (s *memSink) Close() error  { s.closed = true; return nil }
func (s *memSink) Cancel() error { s.cancelled = true; return nil }
func (s *memSink) ID() string    { return "mem" }

func TestFSMSnapshotRestore(t *testing.T) {
	useDB(t)
	if err := db.Set("k", "v"); err != nil {
		t.Fatal(err)
	}

	snap, err := (&fsm{}).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Release()
	sink := &memSink{}
	if err := snap.Persist(sink); err != nil {
		t.Fatal(err)
	}
	if !sink.closed || sink.cancelled {
		t.Fatalf("sink closed=%v cancelled=%v, want closed only", sink.closed, sink.cancelled)
	}

	db.Del("k")
	if err := (&fsm{}).Restore(io.NopCloser(bytes.NewReader(sink.Bytes()))); err != nil {
		t.Fatal(err)
	}
	if got := db.Get("k"); got != `"v"` {
		t.Fatalf("restored k = %q", got)
	}

	failing := &memSink{writeErr: errors.New("disk full")}
	if err := snap.Persist(failing); err == nil || !failing.cancelled {
		t.Fatalf("Persist error = %v cancelled=%v, want error and cancel", err, failing.cancelled)
	}
	if err := (&fsm{}).Restore(io.NopCloser(iotest.ErrReader(errors.New("read failed")))); err == nil {
		t.Fatal("Restore should return the read error")
	}
}

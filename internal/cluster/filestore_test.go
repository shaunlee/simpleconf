package cluster

import (
	"bytes"
	"github.com/hashicorp/raft"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileStorePersistAndReload(t *testing.T) {
	dir := t.TempDir()
	s, err := newFileStore(dir)
	if err != nil {
		t.Fatalf("newFileStore failed: %v", err)
	}

	if err := s.Set([]byte("a"), []byte("b")); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	if err := s.SetUint64([]byte("n"), 42); err != nil {
		t.Fatalf("SetUint64 failed: %v", err)
	}
	if err := s.StoreLog(&raft.Log{Index: 1, Term: 1, Type: raft.LogCommand, Data: []byte("one")}); err != nil {
		t.Fatalf("StoreLog #1 failed: %v", err)
	}
	if err := s.StoreLog(&raft.Log{Index: 2, Term: 1, Type: raft.LogCommand, Data: []byte("two")}); err != nil {
		t.Fatalf("StoreLog #2 failed: %v", err)
	}
	if err := s.DeleteRange(2, 2); err != nil {
		t.Fatalf("DeleteRange failed: %v", err)
	}

	s2, err := newFileStore(dir)
	if err != nil {
		t.Fatalf("reload newFileStore failed: %v", err)
	}

	if v, err := s2.Get([]byte("a")); err != nil || string(v) != "b" {
		t.Fatalf("Get mismatch: val=%q err=%v", string(v), err)
	}
	if v, err := s2.GetUint64([]byte("n")); err != nil || v != 42 {
		t.Fatalf("GetUint64 mismatch: val=%d err=%v", v, err)
	}
	first, _ := s2.FirstIndex()
	last, _ := s2.LastIndex()
	if first != 1 || last != 1 {
		t.Fatalf("index bounds mismatch: first=%d last=%d", first, last)
	}
	var out raft.Log
	if err := s2.GetLog(1, &out); err != nil {
		t.Fatalf("GetLog(1) failed: %v", err)
	}
	if got := string(out.Data); got != "one" {
		t.Fatalf("GetLog(1) data mismatch: got=%q", got)
	}
	if err := s2.GetLog(2, &out); err != raft.ErrLogNotFound {
		t.Fatalf("GetLog(2) should be ErrLogNotFound, got=%v", err)
	}
}

// Appending never rewrites the checkpoint, however long the log grows: a
// rewrite copies every log in memory, and between two Raft snapshots that
// can be a million of them. The checkpoint is written when Raft drops logs.
func TestFileStoreCompactsOnlyOnDeleteRange(t *testing.T) {
	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "raft-state.snapshot.json")
	walPath := filepath.Join(dir, "raft-state.wal")

	s, err := newFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.syncEvery(time.Hour) // spare 2000 fsyncs; Close flushes
	if err := s.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	for i := uint64(1); i <= 2000; i++ {
		storeLog(t, s, i)
	}
	if _, err := os.Stat(snapshotPath); !os.IsNotExist(err) {
		t.Fatalf("checkpoint written while appending: %v", err)
	}

	if err := s.DeleteRange(1, 1990); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(snapshotPath); err != nil || st.Size() == 0 {
		t.Fatalf("no checkpoint after DeleteRange: %v", err)
	}
	if st, err := os.Stat(walPath); err != nil || st.Size() != 0 {
		t.Fatalf("WAL not emptied by the checkpoint: %v", err)
	}
	storeLog(t, s, 2001)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := newFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	first, _ := s2.FirstIndex()
	last, _ := s2.LastIndex()
	if first != 1991 || last != 2001 {
		t.Fatalf("reloaded logs %d..%d, want 1991..2001", first, last)
	}
	if v, err := s2.Get([]byte("k1")); err != nil || string(v) != "v1" {
		t.Fatalf("reloaded k1 = %q, %v", v, err)
	}
}

func TestFileStoreLoadErrors(t *testing.T) {
	if _, err := newFileStore(""); err == nil {
		t.Fatal("empty dir should fail")
	}

	cases := map[string]func(dir string) error{
		"corrupt snapshot": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "raft-state.snapshot.json"), []byte("{"), 0o600)
		},
		"unreadable snapshot": func(dir string) error {
			return os.Mkdir(filepath.Join(dir, "raft-state.snapshot.json"), 0o755)
		},
		"corrupt wal record before the last": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "raft-state.wal"), []byte("{\n"+`{"type":"set","key":"k"}`+"\n"), 0o600)
		},
		"unknown wal record": func(dir string) error {
			return os.WriteFile(filepath.Join(dir, "raft-state.wal"), []byte(`{"type":"bogus"}`+"\n"), 0o600)
		},
		"unreadable wal": func(dir string) error {
			return os.Mkdir(filepath.Join(dir, "raft-state.wal"), 0o755)
		},
	}
	for name, setup := range cases {
		dir := t.TempDir()
		if err := setup(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := newFileStore(dir); err == nil {
			t.Fatalf("%s: newFileStore should fail", name)
		}
	}
}

func TestFileStoreEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "raft-state.snapshot.json"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := newFileStore(dir)
	if err != nil {
		t.Fatalf("empty snapshot should load: %v", err)
	}
	if n, err := s.LastIndex(); err != nil || n != 0 {
		t.Fatalf("LastIndex = %d, %v", n, err)
	}
}

// One WAL line holds a whole batch, so it can be far longer than
// bufio.Scanner's 64 KB default.
func TestFileStoreLargeRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := newFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("x"), 1<<20)
	if err := s.StoreLog(&raft.Log{Index: 1, Term: 1, Type: raft.LogCommand, Data: big}); err != nil {
		t.Fatal(err)
	}
	s2, err := newFileStore(dir)
	if err != nil {
		t.Fatalf("reopen after a 1 MB entry: %v", err)
	}
	var got raft.Log
	if err := s2.GetLog(1, &got); err != nil || !bytes.Equal(got.Data, big) {
		t.Fatalf("GetLog = %d bytes, %v", len(got.Data), err)
	}
}

// A crash in the middle of an append leaves a partial last line. It was never
// acknowledged, so the store drops it and carries on.
func TestFileStoreTornTail(t *testing.T) {
	for name, tail := range map[string]string{
		"no newline":      `{"type":"store_logs","logs":[{"index":2,"te`,
		"unparsable line": "{\"type\":\"store_lo\n",
	} {
		dir := t.TempDir()
		s, err := newFileStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.StoreLog(&raft.Log{Index: 1, Term: 1, Type: raft.LogCommand, Data: []byte("a")}); err != nil {
			t.Fatal(err)
		}
		whole, err := os.ReadFile(s.walPath)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(s.walPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(tail)
		f.Close()

		s2, err := newFileStore(dir)
		if err != nil {
			t.Fatalf("%s: reopen with a torn tail: %v", name, err)
		}
		if n, _ := s2.LastIndex(); n != 1 {
			t.Fatalf("%s: LastIndex = %d want 1", name, n)
		}
		if after, _ := os.ReadFile(s2.walPath); !bytes.Equal(after, whole) {
			t.Fatalf("%s: WAL not cut back to its last whole record:\n%q\nwant\n%q", name, after, whole)
		}
		// Appends continue on a clean line.
		if err := s2.StoreLog(&raft.Log{Index: 2, Term: 1, Type: raft.LogCommand, Data: []byte("b")}); err != nil {
			t.Fatal(err)
		}
		s3, err := newFileStore(dir)
		if err != nil {
			t.Fatalf("%s: reopen after appending: %v", name, err)
		}
		if n, _ := s3.LastIndex(); n != 2 {
			t.Fatalf("%s: LastIndex after append = %d want 2", name, n)
		}
	}
}

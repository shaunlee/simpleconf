package cluster

import (
	"github.com/hashicorp/raft"
	"os"
	"path/filepath"
	"testing"
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

func TestFileStoreCompactsWAL(t *testing.T) {
	dir := t.TempDir()
	storeCheckpointEvery = 2
	t.Cleanup(func() { storeCheckpointEvery = 512 })

	s, err := newFileStore(dir)
	if err != nil {
		t.Fatalf("newFileStore failed: %v", err)
	}
	if err := s.Set([]byte("k1"), []byte("v1")); err != nil {
		t.Fatalf("Set #1 failed: %v", err)
	}
	if err := s.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set #2 failed: %v", err)
	}

	snapshotPath := filepath.Join(dir, "raft-state.snapshot.json")
	if st, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("expected snapshot after checkpoint, err=%v", err)
	} else if st.Size() == 0 {
		t.Fatalf("expected non-empty snapshot after checkpoint")
	}

	walPath := filepath.Join(dir, "raft-state.wal")
	if st, err := os.Stat(walPath); err != nil || st.Size() != 0 {
		size := int64(-1)
		if err == nil {
			size = st.Size()
		}
		t.Fatalf("expected empty wal after checkpoint, err=%v size=%d", err, size)
	}

	s2, err := newFileStore(dir)
	if err != nil {
		t.Fatalf("reload newFileStore failed: %v", err)
	}
	if v, err := s2.Get([]byte("k1")); err != nil || string(v) != "v1" {
		t.Fatalf("reloaded k1 mismatch: val=%q err=%v", string(v), err)
	}
	if v, err := s2.Get([]byte("k2")); err != nil || string(v) != "v2" {
		t.Fatalf("reloaded k2 mismatch: val=%q err=%v", string(v), err)
	}
}

package cluster

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
)

func TestParseFsync(t *testing.T) {
	for in, want := range map[string]bool{"": false, "always": false, "everysec": true} {
		if got, err := parseFsync(in); err != nil || got != want {
			t.Fatalf("parseFsync(%q) = %v, %v want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"no", "Always", "every-sec"} {
		if _, err := parseFsync(in); err == nil {
			t.Fatalf("parseFsync(%q) should fail", in)
		}
	}
	if _, err := Start(Config{Enabled: true, NodeID: "n", RaftAddr: "127.0.0.1:1", Dir: t.TempDir(), Fsync: "no"}); err == nil {
		t.Fatal("Start should reject an unknown raft.fsync")
	}
}

func storeLog(t *testing.T, s *fileStore, index uint64) {
	t.Helper()
	if err := s.StoreLog(&raft.Log{Index: index, Term: 1, Type: raft.LogCommand, Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
}

func syncState(s *fileStore) (syncs int, dirty bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncs, s.dirty
}

func TestAlwaysFsyncsLogAppends(t *testing.T) {
	s, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	storeLog(t, s, 1)
	if syncs, dirty := syncState(s); syncs != 1 || dirty {
		t.Fatalf("after a log append: syncs=%d dirty=%v, want 1 false", syncs, dirty)
	}
}

// Under everysec, log appends and deletes skip the fsync; term and vote do not.
func TestEverysecDefersOnlyLogWrites(t *testing.T) {
	s, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.syncEvery(time.Hour) // no tick during the test

	storeLog(t, s, 1)
	storeLog(t, s, 2)
	if err := s.DeleteRange(2, 2); err != nil {
		t.Fatal(err)
	}
	if syncs, dirty := syncState(s); syncs != 0 || !dirty {
		t.Fatalf("after log writes: syncs=%d dirty=%v, want 0 true", syncs, dirty)
	}

	if err := s.SetUint64([]byte("CurrentTerm"), 2); err != nil {
		t.Fatal(err)
	}
	if syncs, dirty := syncState(s); syncs != 1 || dirty {
		t.Fatalf("after the term: syncs=%d dirty=%v, want 1 false", syncs, dirty)
	}
	if err := s.Set([]byte("LastVoteCand"), []byte("n2")); err != nil {
		t.Fatal(err)
	}
	if syncs, _ := syncState(s); syncs != 2 {
		t.Fatalf("after the vote: syncs=%d want 2", syncs)
	}
}

func TestEverysecBackgroundFsync(t *testing.T) {
	s, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.syncEvery(5 * time.Millisecond)

	storeLog(t, s, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if syncs, dirty := syncState(s); syncs >= 1 && !dirty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the background loop did not fsync the log append")
		}
		time.Sleep(time.Millisecond)
	}
	// Nothing new to write, so no further fsyncs.
	syncs, _ := syncState(s)
	time.Sleep(30 * time.Millisecond)
	if again, _ := syncState(s); again != syncs {
		t.Fatalf("idle loop fsynced %d more times", again-syncs)
	}
}

func TestCloseFsyncsPendingWrites(t *testing.T) {
	dir := t.TempDir()
	s, err := newFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.syncEvery(time.Hour)
	storeLog(t, s, 1)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if syncs, dirty := syncState(s); syncs != 1 || dirty {
		t.Fatalf("after Close: syncs=%d dirty=%v, want 1 false", syncs, dirty)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	s2, err := newFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if n, _ := s2.LastIndex(); n != 1 {
		t.Fatalf("LastIndex after reopen = %d want 1", n)
	}
}

// A checkpoint persists every log in memory, so it leaves nothing pending.
func TestCheckpointClearsPending(t *testing.T) {
	prev := storeCheckpointEvery
	storeCheckpointEvery = 3
	defer func() { storeCheckpointEvery = prev }()

	s, err := newFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.syncEvery(time.Hour)
	storeLog(t, s, 1)
	storeLog(t, s, 2)
	storeLog(t, s, 3) // third WAL write: checkpoint
	if _, dirty := syncState(s); dirty {
		t.Fatal("checkpoint left the WAL marked dirty")
	}
}

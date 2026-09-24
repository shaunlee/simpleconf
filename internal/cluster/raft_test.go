package cluster

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/shaunlee/simpleconf/internal/db"
)

func freeAddr(t testing.TB) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startSingle(t *testing.T, dir string) *Manager {
	t.Helper()
	addr := freeAddr(t)
	m, err := Start(Config{
		Enabled:   true,
		NodeID:    "n1",
		RaftAddr:  addr,
		HTTPAddr:  "127.0.0.1:8080",
		Dir:       dir,
		Bootstrap: true,
		Peers:     []Peer{{ID: "n1", RaftAddr: addr, HTTPAddr: "127.0.0.1:8080"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return m
}

func waitLeader(t testing.TB, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for m.raft.State() != raft.Leader {
		if time.Now().After(deadline) {
			t.Fatal("node did not become leader")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestStartConfigErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := t.TempDir()
	if err := os.WriteFile(filepath.Join(corrupt, "raft-state.snapshot.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := map[string]Config{
		"missing node id":    {Enabled: true, RaftAddr: "127.0.0.1:1", Dir: t.TempDir()},
		"missing raft addr":  {Enabled: true, NodeID: "n", Dir: t.TempDir()},
		"missing dir":        {Enabled: true, NodeID: "n", RaftAddr: "127.0.0.1:1"},
		"dir is a file":      {Enabled: true, NodeID: "n", RaftAddr: "127.0.0.1:1", Dir: filepath.Join(file, "sub")},
		"bad raft addr":      {Enabled: true, NodeID: "n", RaftAddr: "127.0.0.1:notaport", Dir: t.TempDir()},
		"unadvertisable":     {Enabled: true, NodeID: "n", RaftAddr: "0.0.0.0:0", Dir: t.TempDir()},
		"corrupt raft state": {Enabled: true, NodeID: "n", RaftAddr: freeAddr(t), Dir: corrupt},
	}
	for name, cfg := range cases {
		if _, err := Start(cfg); err == nil {
			t.Fatalf("%s: Start should fail", name)
		}
	}
}

func TestSingleNodeRaft(t *testing.T) {
	useDB(t)
	dir := t.TempDir()
	m := startSingle(t, dir)
	waitLeader(t, m)
	useDefault(t, m)
	// Raft replicates on its own; the peers hook must stay out of it.
	writes := recordWrites(t)
	defer func() {
		if got := writes(); len(got) != 0 {
			t.Fatalf("hook called with Raft enabled: %+v", got)
		}
	}()

	if got, want := m.leaderHTTPAddr(), "http://127.0.0.1:8080"; got != want {
		t.Fatalf("leaderHTTPAddr = %q want %q", got, want)
	}

	if err := ApplySetRaw("a", []byte(`{"b":9007199254740993}`)); err != nil {
		t.Fatal(err)
	}
	if err := ApplySet("s", "v"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyClone("a", "c"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDelete("s"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyVacuum(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"a.b": "9007199254740993", "c.b": "9007199254740993", "s": ""} {
		if got := db.Get(k); got != want {
			t.Fatalf("Get(%q) = %q want %q", k, got, want)
		}
	}
	var je *db.JSONError
	if err := ApplySetRaw("bad", []byte("{")); !errors.As(err, &je) {
		t.Fatalf("bad JSON error = %v, want *db.JSONError", err)
	}
	m.Shutdown()

	// Bootstrapping a directory that already holds state is not an error.
	m = startSingle(t, dir)
	defer m.Shutdown()
	waitLeader(t, m)
}

func TestNoLeader(t *testing.T) {
	addr := freeAddr(t)
	m, err := Start(Config{Enabled: true, Forward: true, NodeID: "n1", RaftAddr: addr, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()

	if got := m.leaderHTTPAddr(); got != "" {
		t.Fatalf("leaderHTTPAddr = %q, want empty", got)
	}
	nl, ok := AsNotLeader(m.ApplyDelete("k"))
	if !ok || nl.LeaderHTTPAddr != "" {
		t.Fatalf("got %+v, %v; want NotLeaderError without a leader", nl, ok)
	}
}

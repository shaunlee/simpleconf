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
	if got := Role(); got != "leader" {
		t.Fatalf("Role = %q want leader", got)
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

// The transport advertises the resolved address, 127.0.0.1:port, while the
// config says localhost:port; the leader must still be found by its ID.
func TestLeaderHTTPAddrHostname(t *testing.T) {
	useDB(t)
	_, port, err := net.SplitHostPort(freeAddr(t))
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("localhost", port)
	m, err := Start(Config{
		Enabled:   true,
		NodeID:    "n1",
		RaftAddr:  addr,
		HTTPAddr:  "127.0.0.1:8080",
		Dir:       t.TempDir(),
		Bootstrap: true,
		Peers:     []Peer{{ID: "n1", RaftAddr: addr, HTTPAddr: "127.0.0.1:8080"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	waitLeader(t, m)

	if got, want := m.leaderHTTPAddr(), "http://127.0.0.1:8080"; got != want {
		t.Fatalf("leaderHTTPAddr = %q want %q", got, want)
	}
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
	if got := m.role(); got != "follower" {
		t.Fatalf("role = %q want follower", got)
	}
	nl, ok := AsNotLeader(m.ApplyDelete("k"))
	if !ok || nl.LeaderHTTPAddr != "" {
		t.Fatalf("got %+v, %v; want NotLeaderError without a leader", nl, ok)
	}
}

func TestSingleNodeRaftEverysec(t *testing.T) {
	useDB(t)
	dir := t.TempDir()
	start := func() *Manager {
		addr := freeAddr(t)
		m, err := Start(Config{Enabled: true, NodeID: "n1", RaftAddr: addr, Dir: dir, Bootstrap: true,
			Peers: []Peer{{ID: "n1", RaftAddr: addr}}, Fsync: "everysec"})
		if err != nil {
			t.Fatal(err)
		}
		waitLeader(t, m)
		return m
	}

	m := start()
	useDefault(t, m)
	if err := ApplySetRaw("k", []byte(`"v"`)); err != nil {
		t.Fatal(err)
	}
	m.Shutdown() // fsyncs what the background loop had not

	db.Close()
	db.Init(t.TempDir()) // an empty document; Raft replays into it
	m = start()
	defer m.Shutdown()
	deadline := time.Now().Add(5 * time.Second)
	for db.Get("k") != `"v"` {
		if time.Now().After(deadline) {
			t.Fatalf("write not replayed after restart: %s", db.Get(""))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

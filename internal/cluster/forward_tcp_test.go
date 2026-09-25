package cluster

import (
	"bufio"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/shaunlee/simpleconf/internal/db"
)

// startCluster starts n nodes that know each other only by Raft address:
// none has an HTTP address. The first bootstraps the cluster.
func startCluster(t *testing.T, n int) []*Manager {
	t.Helper()
	addrs := make([]string, n)
	peers := make([]Peer, n)
	for i := range n {
		addrs[i] = freeAddr(t)
		peers[i] = Peer{ID: "n" + string(rune('1'+i)), RaftAddr: addrs[i]}
	}
	ms := make([]*Manager, n)
	for i := range n {
		m, err := Start(Config{
			Enabled:   true,
			Forward:   true,
			NodeID:    peers[i].ID,
			RaftAddr:  addrs[i],
			Dir:       t.TempDir(),
			Bootstrap: i == 0,
			Peers:     peers,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(m.Shutdown)
		ms[i] = m
	}
	return ms
}

// leaderAndFollower waits until every node knows the leader.
func leaderAndFollower(t *testing.T, ms []*Manager) (leader, follower *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		leader, follower = nil, nil
		known := 0
		for _, m := range ms {
			if a, _ := m.raft.LeaderWithID(); len(a) > 0 {
				known++
			}
			if m.raft.State() == raft.Leader {
				leader = m
			} else {
				follower = m
			}
		}
		if leader != nil && follower != nil && known == len(ms) {
			return leader, follower
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no leader known to every node")
	return nil, nil
}

// A follower forwards over the leader's Raft port, so the cluster needs no
// HTTP addresses at all. Before, it answered "not leader" with no leader.
func TestForwardOverRaftPort(t *testing.T) {
	useDB(t)
	ms := startCluster(t, 3)
	leader, follower := leaderAndFollower(t, ms)

	if err := follower.apply(command{Op: "set", Key: "fwd", Value: "v"}); err != nil {
		t.Fatalf("forwarded set: %v", err)
	}
	if got := db.Get("fwd"); got != `"v"` {
		t.Fatalf("fwd = %s", got)
	}
	if err := follower.apply(command{Op: "clone", FromKey: "fwd", ToKey: "copy"}); err != nil {
		t.Fatal(err)
	}
	if err := follower.apply(command{Op: "del", Key: "fwd"}); err != nil {
		t.Fatal(err)
	}
	if db.Get("fwd") != "" || db.Get("copy") != `"v"` {
		t.Fatalf("document = %s", db.Get(""))
	}

	// One connection carried all three writes.
	addr, _ := follower.raft.LeaderWithID()
	if n := len(follower.fwd.idle[string(addr)]); n != 1 {
		t.Fatalf("%d pooled connections, want 1", n)
	}

	// The leader closes the pooled connection; the next write dials again.
	leader.layer.closeForwardConns()
	if err := follower.apply(command{Op: "set", Key: "again", Value: 1}); err != nil {
		t.Fatalf("set after the leader closed the connection: %v", err)
	}

	follower.forward = false
	if _, ok := AsNotLeader(follower.apply(command{Op: "del", Key: "x"})); !ok {
		t.Fatal("with forward off a follower should refuse")
	}
}

// A node that is not the leader answers a forwarded write with "not leader"
// and does not pass it on again.
func TestForwardToFollowerIsRefused(t *testing.T) {
	useDB(t)
	ms := startCluster(t, 3)
	_, follower := leaderAndFollower(t, ms)
	var other *Manager
	for _, m := range ms {
		if m != follower && m.raft.State() != raft.Leader {
			other = m
		}
	}
	err := other.forwardTCP(follower.layer.Addr().String(), []byte(`{"op":"del","key":"k"}`))
	if _, ok := AsNotLeader(err); !ok {
		t.Fatalf("forward to a follower = %v, want not leader", err)
	}
}

func TestForwardRejectsBadRequests(t *testing.T) {
	useDB(t)
	leader := startCluster(t, 1)[0]
	waitLeader(t, leader)
	addr := leader.layer.Addr().String()
	for req, want := range map[string]string{
		`{"op":"bogus"}`: "unknown op",
		`not json`:       "invalid",
	} {
		err := leader.forwardTCP(addr, []byte(req))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: error = %v, want %q", req, err, want)
		}
	}
}

// A v0.6 leader's Raft transport reads 'F' as an unknown RPC type and closes
// the connection. The follower then uses the leader's HTTP address.
func TestForwardFallsBackToHTTPForOldLeaders(t *testing.T) {
	old, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var tries atomic.Int32
	go func() {
		for {
			c, err := old.Accept()
			if err != nil {
				return
			}
			tries.Add(1)
			_, _ = bufio.NewReader(c).ReadByte()
			_ = c.Close()
		}
	}()
	srv, got := fakeLeader(t, http.StatusAccepted, "")

	m := &Manager{forward: true}
	c := command{Op: "del", Key: "k"}
	if err := m.forwardTo(c, []byte(`{"op":"del","key":"k"}`), old.Addr().String(), func() string { return srv.URL }); err != nil {
		t.Fatalf("fallback: %v", err)
	}
	if got.method != http.MethodDelete || got.path != "/db/k" {
		t.Fatalf("HTTP leader got %+v", *got)
	}
	// The next write goes straight to HTTP, without trying the Raft port.
	if err := m.forwardTo(c, []byte(`{"op":"del","key":"k"}`), old.Addr().String(), func() string { return srv.URL }); err != nil {
		t.Fatal(err)
	}
	if n := tries.Load(); n != 1 {
		t.Fatalf("%d connections to the old leader's Raft port, want 1", n)
	}
	// Without an HTTP address there is nowhere to fall back to.
	if err := m.forwardTo(c, nil, old.Addr().String(), func() string { return "" }); err != errForwardUnsupported {
		t.Fatalf("no HTTP address: %v", err)
	}
}

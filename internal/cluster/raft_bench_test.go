package cluster

import (
	"io"
	"log"
	"os"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/shaunlee/simpleconf/internal/db"
)

// benchLeader starts a single-node cluster and makes it the default manager.
// As in main, the AOF is off under Raft; benchmarks run after the tests, so
// turning it off for the rest of the process does not affect them.
func benchLeader(b *testing.B, fsync string) {
	b.Helper()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	db.DisableAOF()
	db.Init(b.TempDir())
	b.Cleanup(func() { db.Close() })

	addr := freeAddr(b)
	m, err := Start(Config{
		Enabled:   true,
		NodeID:    "n1",
		RaftAddr:  addr,
		Dir:       b.TempDir(),
		Bootstrap: true,
		Peers:     []Peer{{ID: "n1", RaftAddr: addr}},
		Fsync:     fsync,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(m.Shutdown)
	waitLeader(b, m)
	prev := getDefault()
	SetDefault(m)
	b.Cleanup(func() { SetDefault(prev) })
}

// Every write is one Raft log entry, stored before it is applied.
func BenchmarkRaftApply(b *testing.B)         { benchApply(b, "always") }
func BenchmarkRaftApplyEverysec(b *testing.B) { benchApply(b, "everysec") }

// Concurrent writers let Raft store several entries per log write.
func BenchmarkRaftApplyParallel(b *testing.B)         { benchApplyParallel(b, "always") }
func BenchmarkRaftApplyEverysecParallel(b *testing.B) { benchApplyParallel(b, "everysec") }

func benchApply(b *testing.B, fsync string) {
	benchLeader(b, fsync)
	raw := []byte(`"v"`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := ApplySetRaw("k"+strconv.Itoa(i%1024), raw); err != nil {
			b.Fatal(err)
		}
	}
}

func benchApplyParallel(b *testing.B, fsync string) {
	benchLeader(b, fsync)
	raw := []byte(`"v"`)
	var n atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := ApplySetRaw("k"+strconv.Itoa(int(n.Add(1)%1024)), raw); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

package cluster

import (
	"fmt"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaunlee/simpleconf/internal/db"
)

// TestSoak writes to a single Raft node without pause and prints, every 10
// seconds, the writes per second, the latency percentiles, the logs kept in
// memory and the checkpoint's size; then it reopens the log store. It found
// the log store rewriting every kept log each 512 appends, which only shows
// after minutes. It is skipped unless SOAK is set; run it on its own, in a
// Linux container, since it turns the AOF off for the rest of the process:
//
//	SOAK=1 go test ./internal/cluster -run TestSoak -v -timeout 30m
//
// SOAK_DUR (default 5m), SOAK_SIZE in bytes (16), SOAK_WRITERS (64) and
// SOAK_FSYNC (always) change the run. Throughput and the logs kept should
// level off; on a VM disk, the maximum latency also shows fsync spikes that
// have nothing to do with the code.
func TestSoak(t *testing.T) {
	if os.Getenv("SOAK") == "" {
		t.Skip("set SOAK=1 to run")
	}
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	dur, err := time.ParseDuration(env("SOAK_DUR", "5m"))
	if err != nil {
		t.Fatal(err)
	}
	size, _ := strconv.Atoi(env("SOAK_SIZE", "16"))
	writers, _ := strconv.Atoi(env("SOAK_WRITERS", "64"))
	fsync := env("SOAK_FSYNC", "always")

	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	db.DisableAOF()
	db.Init(t.TempDir())
	dir := t.TempDir()
	addr := freeAddr(t)
	m, err := Start(Config{Enabled: true, NodeID: "n1", RaftAddr: addr, Dir: dir, Bootstrap: true,
		Peers: []Peer{{ID: "n1", RaftAddr: addr}}, Fsync: fsync})
	if err != nil {
		t.Fatal(err)
	}
	waitLeader(t, m)
	useDefault(t, m)

	raw := []byte(`"` + strings.Repeat("x", size) + `"`)
	var (
		mu   sync.Mutex
		lats []time.Duration
		stop atomic.Bool
		n    atomic.Int64
		wg   sync.WaitGroup
	)
	for range writers {
		wg.Go(func() {
			for !stop.Load() {
				k := "k" + strconv.Itoa(int(n.Add(1)%1024))
				t0 := time.Now()
				if err := ApplySetRaw(k, raw); err != nil {
					t.Error(err)
					return
				}
				d := time.Since(t0)
				mu.Lock()
				lats = append(lats, d)
				mu.Unlock()
			}
		})
	}
	fmt.Printf("size=%dB writers=%d fsync=%s\n", size, writers, fsync)
	fmt.Printf("%6s %8s %9s %9s %9s %8s %10s\n", "t", "writes/s", "p50", "p99", "max", "logs", "ckpt")
	start := time.Now()
	for time.Since(start) < dur {
		time.Sleep(10 * time.Second)
		mu.Lock()
		w := lats
		lats = nil
		mu.Unlock()
		slices.Sort(w)
		m.store.mu.RLock()
		nlogs := len(m.store.logs)
		m.store.mu.RUnlock()
		var ck int64
		if fi, err := os.Stat(m.store.snapshotPath); err == nil {
			ck = fi.Size()
		}
		p := func(q float64) time.Duration {
			if len(w) == 0 {
				return 0
			}
			return w[int(float64(len(w)-1)*q)].Round(time.Microsecond)
		}
		fmt.Printf("%5.0fs %8d %9v %9v %9v %8d %9.1fM\n", time.Since(start).Seconds(), len(w)/10, p(0.5), p(0.99), p(1), nlogs, float64(ck)/1e6)
	}
	stop.Store(true)
	wg.Wait()

	last, _ := m.store.LastIndex()
	m.store.mu.RLock()
	nlogs := len(m.store.logs)
	m.store.mu.RUnlock()
	wal, err := os.Stat(m.store.walPath)
	if err != nil {
		t.Fatal(err)
	}
	m.Shutdown()
	t0 := time.Now()
	s2, err := newFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	last2, _ := s2.LastIndex()
	fmt.Printf("reopen: %d logs, WAL %.1fM, loaded in %v\n", nlogs, float64(wal.Size())/1e6, time.Since(t0).Round(time.Millisecond))
	if last2 != last {
		t.Fatalf("reopened LastIndex %d, was %d", last2, last)
	}
}

package peers

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaunlee/simpleconf/internal/db"
)

// failWALWrites makes the next n WAL writes store the given share of their
// bytes and fail, as a full disk would.
func failWALWrites(t *testing.T, n int, keep func([]byte) []byte) {
	t.Helper()
	orig := writeWAL
	t.Cleanup(func() { writeWAL = orig })
	writeWAL = func(f *os.File, b []byte) (int, error) {
		if n == 0 {
			return f.Write(b)
		}
		n--
		k, _ := f.Write(keep(b))
		return k, errors.New("no space left on device")
	}
}

func half(b []byte) []byte    { return b[:len(b)/2] }
func nothing(b []byte) []byte { return nil }

// countWALSyncs counts fsyncs of WAL files, failing the first fail of them.
func countWALSyncs(t *testing.T, fail int) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	var mu sync.Mutex
	orig := syncWAL
	t.Cleanup(func() { syncWAL = orig })
	syncWAL = func(f *os.File) error {
		n.Add(1)
		mu.Lock()
		defer mu.Unlock()
		if fail > 0 {
			fail--
			return errors.New("input/output error")
		}
		return f.Sync()
	}
	return &n
}

func usePolicy(t *testing.T, p db.FsyncPolicy) {
	t.Helper()
	orig := currentPolicy()
	SetFsyncPolicy(p)
	syncLoopPaused.Store(true)
	t.Cleanup(func() {
		SetFsyncPolicy(orig)
		syncLoopPaused.Store(false)
	})
}

func pendingPaths(w *workerState) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var paths []string
	for _, op := range w.pending {
		paths = append(paths, op.Path)
	}
	return paths
}

// walPaths returns the ops a restart would load from w's WAL.
func walPaths(t *testing.T, w *workerState) []string {
	t.Helper()
	r := newTestWorker(t, w.addr)
	if err := r.loadWAL(); err != nil {
		t.Fatal(err)
	}
	return pendingPaths(r)
}

func put(path string) syncOp {
	return syncOp{Method: http.MethodPut, Path: path, Body: []byte("1")}
}

func TestWALWriteFailureKeepsQueueInStep(t *testing.T) {
	for name, keep := range map[string]func([]byte) []byte{"partial record": half, "nothing written": nothing} {
		t.Run(name, func(t *testing.T) {
			resetSyncState(t)
			w := newTestWorker(t, "http://step")
			if err := w.loadWAL(); err != nil {
				t.Fatal(err)
			}

			failWALWrites(t, 1, keep)
			if _, err := w.enqueue(put("/db/x")); err == nil {
				t.Fatal("enqueue should report the failed WAL write")
			}
			if _, err := w.enqueue(put("/db/z")); err != nil {
				t.Fatal(err)
			}
			// The worker still sends x, then acks it.
			if got := pendingPaths(w); strings.Join(got, ",") != "/db/x,/db/z" {
				t.Fatalf("pending = %v, want x then z", got)
			}
			if err := w.ack(); err != nil {
				t.Fatal(err)
			}

			// After a restart z, which the peer has not had, is still queued.
			if got := walPaths(t, w); strings.Join(got, ",") != "/db/z" {
				t.Fatalf("pending after restart = %v, want [/db/z]", got)
			}
		})
	}
}

func TestSyncAllRewritesWALAfterFailedWrite(t *testing.T) {
	resetSyncState(t)
	usePolicy(t, db.FsyncNo)
	w := workerFor("http://127.0.0.1:1")

	failWALWrites(t, 1, half)
	if _, err := w.enqueue(put("/db/x")); err == nil {
		t.Fatal("enqueue should report the failed WAL write")
	}
	if got := walPaths(t, w); len(got) != 0 {
		t.Fatalf("WAL before the retry = %v, want nothing", got)
	}
	// With no further writes, the background loop rewrites the WAL.
	syncAll()
	if got := walPaths(t, w); strings.Join(got, ",") != "/db/x" {
		t.Fatalf("WAL after the retry = %v, want [/db/x]", got)
	}
	if _, err := w.enqueue(put("/db/y")); err != nil {
		t.Fatal(err)
	}
	if got := walPaths(t, w); strings.Join(got, ",") != "/db/x,/db/y" {
		t.Fatalf("WAL = %v, want x then y", got)
	}
}

func TestWALFsyncFollowsPolicy(t *testing.T) {
	cases := []struct {
		policy         db.FsyncPolicy
		dispatch, loop int64 // fsyncs for 3 writes, then for one tick
	}{
		{db.FsyncAlways, 3, 0},
		{db.FsyncEverysec, 0, 1},
		{db.FsyncNo, 0, 0},
	}
	for _, c := range cases {
		resetSyncState(t)
		usePolicy(t, c.policy)
		syncs := countWALSyncs(t, 0)
		addr := "http://127.0.0.1:1"
		Configure([]string{addr})
		w := workerFor(addr)

		for range 3 {
			SyncDelete("k")
		}
		if got := syncs.Load(); got != c.dispatch {
			t.Fatalf("policy %d: %d fsyncs for 3 writes, want %d", c.policy, got, c.dispatch)
		}
		// Acks are not fsynced.
		for range 3 {
			if err := w.ack(); err != nil {
				t.Fatal(err)
			}
		}
		syncAll()
		syncAll()
		if got := syncs.Load() - c.dispatch; got != c.loop {
			t.Fatalf("policy %d: %d fsyncs from the loop, want %d", c.policy, got, c.loop)
		}
	}
}

func TestWALFsyncFailureRewritesWAL(t *testing.T) {
	resetSyncState(t)
	usePolicy(t, db.FsyncAlways)
	syncs := countWALSyncs(t, 1)
	w := newTestWorker(t, "http://fsync")
	if err := w.loadWAL(); err != nil {
		t.Fatal(err)
	}

	seq, err := w.enqueue(put("/db/x"))
	if err != nil {
		t.Fatal(err)
	}
	// The failed fsync is not retried on the same file: the queue is written
	// to a new one, which is fsynced.
	if err := w.syncTo(seq); err != nil {
		t.Fatal(err)
	}
	if got := syncs.Load(); got != 2 {
		t.Fatalf("%d fsyncs, want the failed one and the checkpoint's", got)
	}
	if got := walPaths(t, w); strings.Join(got, ",") != "/db/x" {
		t.Fatalf("WAL = %v, want [/db/x]", got)
	}
}

func TestWALGroupCommit(t *testing.T) {
	resetSyncState(t)
	usePolicy(t, db.FsyncAlways)
	syncs := countWALSyncs(t, 0)
	orig := syncWAL
	syncWAL = func(f *os.File) error {
		time.Sleep(5 * time.Millisecond)
		return orig(f)
	}
	w := newTestWorker(t, "http://group")
	if err := w.loadWAL(); err != nil {
		t.Fatal(err)
	}

	const writers = 20
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			seq, err := w.enqueue(put("/db/k"))
			if err == nil {
				err = w.syncTo(seq)
			}
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := syncs.Load(); got >= writers/2 {
		t.Fatalf("%d fsyncs for %d concurrent writers, want them shared", got, writers)
	}
	if got := w.synced.Load(); got != writers {
		t.Fatalf("synced = %d want %d", got, writers)
	}
}

// A long queue is not a reason to rewrite the WAL: only acked records can be
// dropped from it, and a rewrite waits until they are at least as many as
// the ops still queued, so a peer catching up does not rewrite the queue
// over and over.
func TestCheckpointOnlyWhenHalfTheWALIsAcked(t *testing.T) {
	resetSyncState(t)
	usePolicy(t, db.FsyncNo)
	atomic.StoreInt64(&walCheckpointEvery, 4)
	syncs := countWALSyncs(t, 0)
	w := newTestWorker(t, "http://slow")
	if err := w.loadWAL(); err != nil {
		t.Fatal(err)
	}

	for range 10 {
		if _, err := w.enqueue(put("/db/k")); err != nil {
			t.Fatal(err)
		}
	}
	if got := syncs.Load(); got != 0 {
		t.Fatalf("%d checkpoints while nothing was acked, want 0", got)
	}
	// Three acks leave six records to drop and seven ops queued.
	for range 3 {
		if err := w.ack(); err != nil {
			t.Fatal(err)
		}
	}
	if got := syncs.Load(); got != 0 {
		t.Fatalf("%d checkpoints after three acks, want 0", got)
	}
	// The fourth makes it eight and six.
	if err := w.ack(); err != nil {
		t.Fatal(err)
	}
	if got := syncs.Load(); got != 1 {
		t.Fatalf("%d checkpoints after four acks, want 1", got)
	}
	if got := walPaths(t, w); len(got) != 6 {
		t.Fatalf("WAL holds %d ops, want 6", len(got))
	}
}

// A peer that is down gets every write when it returns, however many.
func TestQueueHasNoLimit(t *testing.T) {
	resetSyncState(t)
	usePolicy(t, db.FsyncNo)
	w := newTestWorker(t, "http://down")
	if err := w.loadWAL(); err != nil {
		t.Fatal(err)
	}
	const n = 5000
	for range n {
		if _, err := w.enqueue(put("/db/k")); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(walPaths(t, w)); got != n {
		t.Fatalf("WAL holds %d ops, want %d", got, n)
	}
}

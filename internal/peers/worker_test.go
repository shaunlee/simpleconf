package peers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"github.com/shaunlee/simpleconf/internal/db"
)

func eventually(t *testing.T, within time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fn()
}

// recorder is a fake peer that answers with fail(path) and logs each request.
type recorder struct {
	mu   sync.Mutex
	reqs []string
	fail func(path string, n int) bool
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req.Method+" "+req.URL.Path)
	n := 0
	for _, s := range r.reqs {
		if strings.HasSuffix(s, " "+req.URL.Path) {
			n++
		}
	}
	r.mu.Unlock()
	if r.fail != nil && r.fail(req.URL.Path, n) {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (r *recorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reqs...)
}

func (r *recorder) count(req string) int {
	n := 0
	for _, s := range r.requests() {
		if s == req {
			n++
		}
	}
	return n
}

func TestRestore(t *testing.T) {
	resetSyncState(t)
	useDB(t)

	Restore(nil)

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/db" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		io.WriteString(w, `{"k":"v"}`)
	}))
	defer good.Close()

	Restore([]string{"http://127.0.0.1:1", bad.URL, good.URL})
	if got := db.Get("k"); got != `"v"` {
		t.Fatalf("restored k = %q", got)
	}
}

func TestSyncCloneAndVacuum(t *testing.T) {
	resetSyncState(t)
	rec := &recorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	Configure([]string{srv.URL})

	SyncClone("a", "b")
	SyncVacuum()

	want := []string{"POST /clone/a/b", "POST /vacuum"}
	if !eventually(t, 2*time.Second, func() bool { return len(rec.requests()) == len(want) }) {
		t.Fatalf("peer got %q want %q", rec.requests(), want)
	}
	for i, got := range rec.requests() {
		if got != want[i] {
			t.Fatalf("request %d = %q want %q", i, got, want[i])
		}
	}
}

func TestSyncRetriesThenSucceeds(t *testing.T) {
	resetSyncState(t)
	rec := &recorder{fail: func(path string, n int) bool { return n <= 2 }}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	Configure([]string{srv.URL})

	SyncVacuum()

	w := workerFor(srv.URL)
	if !eventually(t, 2*time.Second, func() bool {
		_, pending := w.peek()
		return rec.count("POST /vacuum") == 3 && !pending
	}) {
		t.Fatalf("peer got %q, want three attempts and an empty queue", rec.requests())
	}
}

func TestSyncDropsAfterMaxRetries(t *testing.T) {
	resetSyncState(t)
	rec := &recorder{fail: func(path string, n int) bool { return path == "/db/drop" }}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	Configure([]string{srv.URL})

	SyncDelete("drop")
	SyncVacuum()

	// Five attempts sleep 100+200+400+800ms before the op is dropped.
	if !eventually(t, 5*time.Second, func() bool { return rec.count("POST /vacuum") == 1 }) {
		t.Fatalf("queue stayed blocked: peer got %q", rec.requests())
	}
	if got := rec.count("DELETE /db/drop"); got != maxRetries {
		t.Fatalf("dropped op attempts = %d want %d", got, maxRetries)
	}
}

func TestSyncUpdateUnencodable(t *testing.T) {
	resetSyncState(t)
	addr := "http://127.0.0.1:1"
	Configure([]string{addr})

	SyncUpdate("k", make(chan int))
	if _, pending := workerFor(addr).peek(); pending {
		t.Fatal("an unencodable value should not be queued")
	}
}

func newTestWorker(t *testing.T, addr string) *workerState {
	t.Helper()
	return &workerState{addr: addr, ch: make(chan struct{}, 1), walPath: walPathForAddr(addr)}
}

func TestEnqueueQueueFull(t *testing.T) {
	resetSyncState(t)
	w := newTestWorker(t, "http://full")
	w.pending = make([]syncOp, queueSize)
	if err := w.enqueue(syncOp{Method: http.MethodPost, Path: "/vacuum"}); err == nil {
		t.Fatal("enqueue on a full queue should fail")
	}
}

func TestDispatchUnwritableWAL(t *testing.T) {
	resetSyncState(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	SetWALDir(filepath.Join(file, "wal"))

	addr := "http://127.0.0.1:1"
	Configure([]string{addr})
	SyncDelete("k") // logs the WAL error rather than panicking
	if _, err := os.Stat(walPathForAddr(addr)); err == nil {
		t.Fatal("WAL file should not exist under a regular file")
	}
}

func walLine(t *testing.T, op syncOp) string {
	t.Helper()
	b, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	return "E " + string(b) + "\n"
}

func TestLoadWALReplay(t *testing.T) {
	resetSyncState(t)
	op1 := syncOp{Method: http.MethodPut, Path: "/db/1", Body: []byte("1"), ContentType: "application/json"}
	op2 := syncOp{Method: http.MethodDelete, Path: "/db/2"}
	op3 := syncOp{Method: http.MethodPost, Path: "/vacuum"}
	wal := "A\n" + walLine(t, op1) + "\nA\n" + walLine(t, op2) + walLine(t, op3) + "A\n"

	w := newTestWorker(t, "http://replay")
	if err := os.WriteFile(w.walPath, []byte(wal), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := w.loadWAL(); err != nil {
		t.Fatal(err)
	}
	if len(w.pending) != 1 || w.pending[0].Path != op3.Path {
		t.Fatalf("pending = %+v, want only %s", w.pending, op3.Path)
	}
	if w.writes != 6 {
		t.Fatalf("writes = %d want 6", w.writes)
	}

	// Past the checkpoint threshold, loading rewrites the WAL to the pending ops.
	atomic.StoreInt64(&walCheckpointEvery, 3)
	w = newTestWorker(t, "http://replay")
	if err := w.loadWAL(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(w.walPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), walLine(t, op3); got != want {
		t.Fatalf("checkpointed WAL = %q want %q", got, want)
	}
}

func TestLoadWALErrors(t *testing.T) {
	cases := map[string]string{
		"unknown record": "X\n",
		"corrupt op":     "E {\n",
	}
	for name, wal := range cases {
		resetSyncState(t)
		w := newTestWorker(t, "http://bad")
		if err := os.WriteFile(w.walPath, []byte(wal), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := w.loadWAL(); err == nil {
			t.Fatalf("%s: loadWAL should fail", name)
		}
	}

	resetSyncState(t)
	w := newTestWorker(t, "http://dir")
	if err := os.Mkdir(w.walPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.loadWAL(); err == nil {
		t.Fatal("a directory in place of the WAL should fail")
	}
}

func TestDoRequestBadURL(t *testing.T) {
	if _, err := doRequest(syncOp{Method: http.MethodGet, Path: "/db"}, "http://[::1"); err == nil {
		t.Fatal("malformed peer URL should fail")
	}
}

func TestReadBody(t *testing.T) {
	if body, err := readBody(&http.Response{}); body != "" || err != nil {
		t.Fatalf("nil body = %q, %v", body, err)
	}
	resp := &http.Response{Body: io.NopCloser(iotest.ErrReader(errors.New("read failed")))}
	if _, err := readBody(resp); err == nil {
		t.Fatal("readBody should return the read error")
	}
}

func TestBackoff(t *testing.T) {
	cases := map[int]time.Duration{
		-1: 100 * time.Millisecond,
		0:  100 * time.Millisecond,
		1:  200 * time.Millisecond,
		3:  800 * time.Millisecond,
		4:  1500 * time.Millisecond,
		10: 1500 * time.Millisecond,
	}
	for attempt, want := range cases {
		if got := backoff(attempt); got != want {
			t.Fatalf("backoff(%d) = %v want %v", attempt, got, want)
		}
	}
}

func TestSetWALDirIgnoresEmpty(t *testing.T) {
	resetSyncState(t)
	dir := currentWALDir()
	SetWALDir("")
	if got := currentWALDir(); got != dir {
		t.Fatalf("WAL dir = %q want %q", got, dir)
	}
}

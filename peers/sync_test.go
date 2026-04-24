package peers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSyncUpdateAndDelete(t *testing.T) {
	resetSyncState(t)
	origPeers := peerAddresses()
	defer Configure(origPeers)

	var (
		mu           sync.Mutex
		updateMethod string
		updatePath   string
		updateBody   string
		deleteMethod string
		deletePath   string
		updateCalled bool
		deleteCalled bool
	)

	var srv *httptest.Server
	func() {
		defer func() {
			if r := recover(); r != nil {
				msg := strings.ToLower(fmt.Sprint(r))
				if strings.Contains(msg, "operation not permitted") || strings.Contains(msg, "failed to listen on a port") {
					t.Skipf("skipping network integration test in restricted environment: %v", r)
				}
				panic(r)
			}
		}()
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasPrefix(r.URL.Path, "/db/bench.flag"):
				b, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				mu.Lock()
				updateMethod = r.Method
				updatePath = r.URL.Path
				updateBody = strings.TrimSpace(string(b))
				updateCalled = true
				mu.Unlock()
				w.WriteHeader(http.StatusAccepted)
			case strings.HasPrefix(r.URL.Path, "/db/bench.old"):
				mu.Lock()
				deleteMethod = r.Method
				deletePath = r.URL.Path
				deleteCalled = true
				mu.Unlock()
				w.WriteHeader(http.StatusAccepted)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
	}()
	defer srv.Close()

	Configure([]string{srv.URL})

	SyncUpdate("bench.flag", true)
	SyncDelete("bench.old")

	if !waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return updateCalled && deleteCalled
	}) {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("expected sync calls to complete, update=%v delete=%v", updateCalled, deleteCalled)
	}

	mu.Lock()
	defer mu.Unlock()
	if !updateCalled {
		t.Fatalf("SyncUpdate did not call peer endpoint")
	}
	if updateMethod != http.MethodPut {
		t.Fatalf("SyncUpdate method mismatch: got %q want %q", updateMethod, http.MethodPut)
	}
	if updatePath != "/db/bench.flag" {
		t.Fatalf("SyncUpdate path mismatch: got %q", updatePath)
	}
	if updateBody != "true" {
		t.Fatalf("SyncUpdate body mismatch: got %q want %q", updateBody, "true")
	}

	if !deleteCalled {
		t.Fatalf("SyncDelete did not call peer endpoint")
	}
	if deleteMethod != http.MethodDelete {
		t.Fatalf("SyncDelete method mismatch: got %q want %q", deleteMethod, http.MethodDelete)
	}
	if deletePath != "/db/bench.old" {
		t.Fatalf("SyncDelete path mismatch: got %q", deletePath)
	}
}

func TestSyncUpdatePersistsWALWhenPeerUnavailable(t *testing.T) {
	resetSyncState(t)
	addr := "http://127.0.0.1:1"
	Configure([]string{addr})

	SyncUpdate("bench.offline", true)

	walPath := walPathForAddr(addr)
	if !waitFor(t, func() bool {
		b, err := os.ReadFile(walPath)
		return err == nil && len(strings.TrimSpace(string(b))) > 0
	}) {
		b, _ := os.ReadFile(walPath)
		t.Fatalf("expected wal file to contain pending ops, got: %q", string(b))
	}
}

func resetSyncState(t *testing.T) {
	t.Helper()
	SetWALDir(t.TempDir())
	walCheckpointEvery = 512

	workersMu.Lock()
	workers = map[string]*workerState{}
	workersMu.Unlock()

	peersMu.Lock()
	peers = nil
	peersMu.Unlock()
}

func TestWALAppendAndCheckpoint(t *testing.T) {
	resetSyncState(t)
	walCheckpointEvery = 3

	addr := "http://example-peer"
	w := &workerState{
		addr:    addr,
		ch:      make(chan struct{}, 1),
		walPath: walPathForAddr(addr),
	}
	if err := w.loadWAL(); err != nil {
		t.Fatalf("loadWAL failed: %v", err)
	}

	if err := w.enqueue(syncOp{Method: http.MethodPut, Path: "/db/a", Body: []byte("1")}); err != nil {
		t.Fatalf("enqueue #1 failed: %v", err)
	}
	if err := w.enqueue(syncOp{Method: http.MethodDelete, Path: "/db/b"}); err != nil {
		t.Fatalf("enqueue #2 failed: %v", err)
	}

	before, err := os.ReadFile(w.walPath)
	if err != nil {
		t.Fatalf("read wal before ack failed: %v", err)
	}
	s := string(before)
	if !strings.Contains(s, "E ") {
		t.Fatalf("wal should contain enqueue records before checkpoint, got %q", s)
	}

	if err := w.ack(); err != nil {
		t.Fatalf("ack failed: %v", err)
	}

	after, err := os.ReadFile(w.walPath)
	if err != nil {
		t.Fatalf("read wal after checkpoint failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(after)), "\n")
	if len(lines) != 1 {
		t.Fatalf("checkpoint should compact wal to one pending record, got %d lines: %q", len(lines), string(after))
	}
	if !strings.HasPrefix(lines[0], "E ") {
		t.Fatalf("checkpointed record should be enqueue record, got %q", lines[0])
	}
	if strings.Contains(string(after), "\nA\n") || strings.HasPrefix(strings.TrimSpace(string(after)), "A") {
		t.Fatalf("checkpointed wal should not contain ack markers, got %q", string(after))
	}
}

func waitFor(t *testing.T, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fn()
}

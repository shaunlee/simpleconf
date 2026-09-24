package peers

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goccy/go-json"
	"github.com/shaunlee/simpleconf/internal/cluster"
	"github.com/shaunlee/simpleconf/internal/db"
)

const (
	requestTimeout = 2 * time.Second
	// After this many failed attempts at one op the worker logs that the peer
	// is unreachable. It keeps retrying regardless.
	warnAfter = 5
)

var (
	walCheckpointEvery int64 = 512

	// backoff starts at retryBase and doubles up to retryCap. Workers read
	// them while running, hence atomics.
	retryBase = newDuration(100 * time.Millisecond)
	retryCap  = newDuration(1500 * time.Millisecond)

	// Replaced in tests to simulate a failing disk.
	writeWAL = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	syncWAL  = func(f *os.File) error { return f.Sync() }

	walPolicy    atomic.Int32 // a db.FsyncPolicy; the zero value is everysec
	syncLoopOnce sync.Once
	// Set by tests that count fsyncs, so a background tick does not add to
	// them. Workers created after it is set are never seen by the loop.
	syncLoopPaused atomic.Bool
)

// SetFsyncPolicy makes the WAL follow db.fsync, so the queue to the peers is
// kept as safely as the local data: always fsyncs before the write is
// answered, everysec once a second in the background, no leaves it to the OS.
func SetFsyncPolicy(p db.FsyncPolicy) {
	walPolicy.Store(int32(p))
}

func currentPolicy() db.FsyncPolicy {
	return db.FsyncPolicy(walPolicy.Load())
}

func newDuration(d time.Duration) *atomic.Int64 {
	v := new(atomic.Int64)
	v.Store(int64(d))
	return v
}

type syncOp struct {
	Method      string `json:"method"`
	Path        string `json:"path"`
	Body        []byte `json:"body,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type workerState struct {
	addr string
	ch   chan struct{}

	mu      sync.Mutex
	walPath string
	pending []syncOp
	walFile *os.File
	writes  int    // records in the WAL file, for checkpointEvery
	seq     uint64 // E records appended so far, for syncTo
	// needCheckpoint is set when a write or fsync to the WAL failed. The file
	// may then lack records that pending holds, so nothing more is appended
	// to it until checkpointLocked rewrites it from pending.
	needCheckpoint bool

	syncMu sync.Mutex    // one fsync at a time; later callers share the next
	synced atomic.Uint64 // highest seq known to be on disk
}

var (
	httpClient = &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        128,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		},
	}

	workersMu sync.Mutex
	workers   = map[string]*workerState{}

	walDirMu sync.RWMutex
	walDir   = ".peers-wal"
)

func SetWALDir(dir string) {
	if len(dir) == 0 {
		return
	}
	walDirMu.Lock()
	walDir = dir
	walDirMu.Unlock()
}

func currentWALDir() string {
	walDirMu.RLock()
	defer walDirMu.RUnlock()
	return walDir
}

func Restore(peerAddrs []string) {
	Configure(peerAddrs)
	if len(peerAddrs) == 0 {
		return
	}

	for _, addr := range peerAddrs {
		url := addr + "/db"
		log.Println("trying to restore from", url)
		body, err := doRequest(syncOp{
			Method: http.MethodGet,
			Path:   "/db",
		}, addr)
		if err != nil {
			log.Println("failed to restore", err)
			continue
		}

		if err := db.Replace(body); err != nil {
			log.Println("failed to restore", err)
			continue
		}
		db.Vacuum()
		break
	}
}

func SyncUpdate(key string, value any) {
	v, err := json.Marshal(value)
	if err != nil {
		log.Println("failed to marshal sync payload", key, err)
		return
	}
	dispatch(syncOp{
		Method:      http.MethodPut,
		Path:        "/db/" + key,
		Body:        v,
		ContentType: "application/json",
	})
}

// SyncSetRaw replicates a set whose value is already JSON text, as a client
// sent it, so the peer stores the same text.
func SyncSetRaw(key string, raw []byte) {
	dispatch(syncOp{
		Method:      http.MethodPut,
		Path:        "/db/" + key,
		Body:        raw,
		ContentType: "application/json",
	})
}

// SyncWrite replicates one local write to every peer. It is installed with
// cluster.SetLocalWriteHook in peers mode.
func SyncWrite(w cluster.Write) {
	switch w.Op {
	case "set":
		SyncSetRaw(w.Key, w.Raw)
	case "del":
		SyncDelete(w.Key)
	case "clone":
		SyncClone(w.FromKey, w.ToKey)
	}
}

func SyncDelete(key string) {
	dispatch(syncOp{
		Method: http.MethodDelete,
		Path:   "/db/" + key,
	})
}

func SyncClone(fromKey, toKey string) {
	dispatch(syncOp{
		Method: http.MethodPost,
		Path:   "/clone/" + fromKey + "/" + toKey,
	})
}

func SyncVacuum() {
	dispatch(syncOp{
		Method: http.MethodPost,
		Path:   "/vacuum",
	})
}

func dispatch(op syncOp) {
	type queued struct {
		w   *workerState
		seq uint64
	}
	var written []queued
	for _, addr := range peerAddresses() {
		w := workerFor(addr)
		seq, err := w.enqueue(op)
		if err != nil {
			log.Println("failed to persist sync op", addr, err)
			continue
		}
		written = append(written, queued{w, seq})
	}
	if currentPolicy() != db.FsyncAlways {
		return
	}
	for _, q := range written {
		if err := q.w.syncTo(q.seq); err != nil {
			log.Println("failed to fsync sync op", q.w.addr, err)
		}
	}
}

func startSyncLoop() {
	syncLoopOnce.Do(func() {
		go func() {
			for range time.Tick(time.Second) {
				if !syncLoopPaused.Load() {
					syncAll()
				}
			}
		}()
	})
}

// syncAll fsyncs each WAL written since the last call under everysec, and
// retries the checkpoint of any WAL that a failed write left behind.
func syncAll() {
	workersMu.Lock()
	ws := make([]*workerState, 0, len(workers))
	for _, w := range workers {
		ws = append(ws, w)
	}
	workersMu.Unlock()

	everysec := currentPolicy() == db.FsyncEverysec
	for _, w := range ws {
		w.mu.Lock()
		seq, broken := w.seq, w.needCheckpoint
		var err error
		if broken {
			err = w.checkpointLocked()
		}
		w.mu.Unlock()
		if !broken && everysec {
			err = w.syncTo(seq)
		}
		if err != nil {
			log.Println("failed to persist sync wal", w.addr, err)
		}
	}
}

func ensureWorkers(peerAddrs []string) {
	for _, addr := range peerAddrs {
		_ = workerFor(addr)
	}
}

func workerFor(addr string) *workerState {
	workersMu.Lock()
	defer workersMu.Unlock()

	if w, ok := workers[addr]; ok {
		return w
	}

	w := &workerState{
		addr:    addr,
		ch:      make(chan struct{}, 1),
		walPath: walPathForAddr(addr),
	}
	if err := w.loadWAL(); err != nil {
		log.Println("failed to load wal", addr, err)
	}
	workers[addr] = w
	startSyncLoop()
	go w.run()
	w.signal()
	return w
}

func walPathForAddr(addr string) string {
	sum := sha1.Sum([]byte(addr))
	name := hex.EncodeToString(sum[:]) + ".jsonl"
	return filepath.Join(currentWALDir(), name)
}

func (w *workerState) loadWAL() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	data, err := os.ReadFile(w.walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return w.ensureWALFileLocked()
		}
		return err
	}
	w.pending = w.pending[:0]
	writes := 0
	reader := bufio.NewReader(bytes.NewReader(data))
	for {
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		writes++
		switch {
		case strings.HasPrefix(line, "E "):
			var op syncOp
			if err := json.Unmarshal([]byte(line[2:]), &op); err != nil {
				return err
			}
			w.pending = append(w.pending, op)
		case line == "A":
			if len(w.pending) > 0 {
				w.pending = w.pending[1:]
			}
		default:
			return fmt.Errorf("invalid wal record: %s", line)
		}
	}
	w.writes = writes
	if err := w.ensureWALFileLocked(); err != nil {
		return err
	}
	if w.reclaimableLocked() {
		return w.checkpointLocked()
	}
	return nil
}

// enqueue queues op for the peer and appends it to the WAL, returning the
// record's number for syncTo. If the WAL cannot take it, op stays queued, so
// the peer still gets it while the process runs, and the error is returned.
func (w *workerState) enqueue(op syncOp) (uint64, error) {
	b, err := json.Marshal(op)
	if err != nil {
		return 0, err
	}
	rec := make([]byte, 0, len(b)+3)
	rec = append(append(append(rec, "E "...), b...), '\n')

	w.mu.Lock()
	defer w.mu.Unlock()

	w.pending = append(w.pending, op)
	w.seq++
	w.signal()
	if err := w.appendLocked(rec); err != nil {
		return 0, err
	}
	return w.seq, nil
}

func (w *workerState) signal() {
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

// run sends queued ops to the peer in order. An op the peer cannot be reached
// for is retried until it goes through, so a peer that is down for a while
// catches up when it returns. An op the peer rejects with a 4xx is dropped,
// since sending it again would not change the answer.
func (w *workerState) run() {
	for range w.ch {
		for {
			op, ok := w.peek()
			if !ok {
				break
			}

			for attempt := 0; ; attempt++ {
				_, err := doRequest(op, w.addr)
				if err == nil {
					break
				}
				if isRejected(err) {
					log.Printf("dropping sync op rejected by %s: %s %s: %v", w.addr, op.Method, op.Path, err)
					break
				}
				if attempt+1 == warnAfter {
					log.Printf("peer %s unreachable, still retrying %s %s: %v", w.addr, op.Method, op.Path, err)
				}
				time.Sleep(backoff(attempt))
			}

			if err := w.ack(); err != nil {
				log.Println("failed to ack wal", w.addr, err)
				time.Sleep(backoff(0))
				break
			}
		}
	}
}

func (w *workerState) peek() (syncOp, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.pending) == 0 {
		return syncOp{}, false
	}
	return w.pending[0], true
}

func (w *workerState) ack() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.pending) == 0 {
		return nil
	}
	w.pending = w.pending[1:]
	// An ack is never fsynced on its own. If a crash loses it, the op and
	// the ones after it are sent again in order, and the peer ends the same.
	return w.appendLocked([]byte("A\n"))
}

// syncTo returns once the WAL is on disk up to record seq. Callers that
// arrive during an fsync wait for it and then share the next one.
func (w *workerState) syncTo(seq uint64) error {
	if w.synced.Load() >= seq {
		return nil
	}
	w.syncMu.Lock()
	defer w.syncMu.Unlock()
	if w.synced.Load() >= seq {
		return nil
	}

	w.mu.Lock()
	f, target := w.walFile, w.seq
	if w.needCheckpoint || f == nil {
		err := w.checkpointLocked()
		w.mu.Unlock()
		return err
	}
	w.mu.Unlock()

	if err := syncWAL(f); err != nil {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.synced.Load() >= seq || w.walFile != f {
			return nil // a checkpoint has written and fsynced it since
		}
		// After a failed fsync the kernel may have dropped the unwritten
		// pages, and fsyncing again can report success without them.
		// Write the whole queue to a new file instead.
		log.Println("failed to fsync sync wal, rewriting it", w.addr, err)
		return w.checkpointLocked()
	}
	w.markSynced(target)
	return nil
}

func (w *workerState) markSynced(seq uint64) {
	for {
		cur := w.synced.Load()
		if cur >= seq || w.synced.CompareAndSwap(cur, seq) {
			return
		}
	}
}

func (w *workerState) ensureWALFileLocked() error {
	if w.walFile != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(w.walPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(w.walPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w.walFile = f
	return nil
}

// appendLocked writes one record. A failed write can leave a partial record
// at the end of the file, which loadWAL skips; nothing is appended after it,
// since the next call rewrites the file from pending instead.
func (w *workerState) appendLocked(rec []byte) error {
	if w.needCheckpoint {
		return w.checkpointLocked()
	}
	if err := w.ensureWALFileLocked(); err != nil {
		w.needCheckpoint = true
		return err
	}
	if _, err := writeWAL(w.walFile, rec); err != nil {
		w.needCheckpoint = true
		return err
	}
	w.writes++
	if w.reclaimableLocked() {
		return w.checkpointLocked()
	}
	return nil
}

// checkpointLocked rewrites the WAL to hold only the pending ops. The new
// file is fsynced and renamed into place, so everything queued is on disk
// when it returns nil.
func (w *workerState) checkpointLocked() (err error) {
	defer func() { w.needCheckpoint = err != nil }()

	if w.walFile != nil {
		_ = w.walFile.Close()
		w.walFile = nil
	}
	dir := filepath.Dir(w.walPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var buf []byte
	for _, op := range w.pending {
		b, err := json.Marshal(op)
		if err != nil {
			return err
		}
		buf = append(append(append(buf, "E "...), b...), '\n')
	}
	tmp := w.walPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := writeWAL(f, buf); err != nil {
		_ = f.Close()
		return err
	}
	if err := syncWAL(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.walPath); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	w.writes = len(w.pending)
	w.markSynced(w.seq)
	return w.ensureWALFileLocked()
}

// reclaimableLocked reports whether a checkpoint would drop enough records:
// the acks and the ops they acked, at least walCheckpointEvery of them and
// no fewer than the ops it would write back. The queue has no limit, so the
// second rule keeps a peer catching up on a long queue from rewriting it
// every few acks.
func (w *workerState) reclaimableLocked() bool {
	dead := int64(w.writes - len(w.pending))
	return dead >= atomic.LoadInt64(&walCheckpointEvery) && dead >= int64(len(w.pending))
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func doRequest(op syncOp, addr string) (string, error) {
	url := addr + op.Path

	req, err := http.NewRequest(op.Method, url, bytes.NewReader(op.Body))
	if err != nil {
		return "", err
	}
	if len(op.ContentType) > 0 {
		req.Header.Set("Content-Type", op.ContentType)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := readBody(resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, &statusError{code: resp.StatusCode, status: resp.Status, body: body}
	}
	return body, nil
}

type statusError struct {
	code         int
	status, body string
}

func (e *statusError) Error() string { return fmt.Sprintf("status=%s body=%s", e.status, e.body) }

// isRejected reports a 4xx other than a timeout or rate limit: the peer
// understood the op and refused it.
func isRejected(err error) bool {
	var se *statusError
	if !errors.As(err, &se) {
		return false
	}
	return se.code >= 400 && se.code < 500 &&
		se.code != http.StatusRequestTimeout && se.code != http.StatusTooManyRequests
}

func readBody(resp *http.Response) (string, error) {
	if resp.Body == nil {
		return "", nil
	}
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func backoff(attempt int) time.Duration {
	base := time.Duration(retryBase.Load())
	limit := time.Duration(retryCap.Load())
	if attempt <= 0 {
		return base
	}
	if attempt > 16 {
		return limit
	}
	if d := base << attempt; d < limit {
		return d
	}
	return limit
}

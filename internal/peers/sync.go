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
	"github.com/shaunlee/simpleconf/internal/db"
)

const (
	requestTimeout = 2 * time.Second
	queueSize      = 1024
	maxRetries     = 5
)

var walCheckpointEvery int64 = 512

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
	writes  int
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
	for _, addr := range peerAddresses() {
		w := workerFor(addr)
		if err := w.enqueue(op); err != nil {
			log.Println("failed to persist sync op", addr, err)
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
	if int64(w.writes) >= atomic.LoadInt64(&walCheckpointEvery) {
		return w.checkpointLocked()
	}
	return nil
}

func (w *workerState) enqueue(op syncOp) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.pending) >= queueSize {
		return fmt.Errorf("queue is full: %d", len(w.pending))
	}

	w.pending = append(w.pending, op)
	if err := w.appendEnqueueLocked(op); err != nil {
		return err
	}
	w.signal()
	return nil
}

func (w *workerState) signal() {
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

func (w *workerState) run() {
	for range w.ch {
		for {
			op, ok := w.peek()
			if !ok {
				break
			}

			ok = false
			for i := 0; i < maxRetries; i++ {
				if _, err := doRequest(op, w.addr); err != nil {
					if i == maxRetries-1 {
						log.Println("failed to sync", w.addr+op.Path, err)
						break
					}
					time.Sleep(backoff(i))
					continue
				}
				ok = true
				break
			}

			if !ok {
				log.Printf("dropping sync op after max retries for %s: %s", w.addr, op.Path)
				// fallthrough and ack it to unblock the rest of the queue
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
	if err := w.appendAckLocked(); err != nil {
		return err
	}
	if int64(w.writes) >= atomic.LoadInt64(&walCheckpointEvery) {
		return w.checkpointLocked()
	}
	return nil
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

func (w *workerState) appendEnqueueLocked(op syncOp) error {
	if err := w.ensureWALFileLocked(); err != nil {
		return err
	}
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if _, err := w.walFile.Write([]byte("E ")); err != nil {
		return err
	}
	if _, err := w.walFile.Write(b); err != nil {
		return err
	}
	if _, err := w.walFile.Write([]byte{'\n'}); err != nil {
		return err
	}
	if err := w.walFile.Sync(); err != nil {
		return err
	}
	w.writes++
	return nil
}

func (w *workerState) appendAckLocked() error {
	if err := w.ensureWALFileLocked(); err != nil {
		return err
	}
	if _, err := w.walFile.Write([]byte("A\n")); err != nil {
		return err
	}
	if err := w.walFile.Sync(); err != nil {
		return err
	}
	w.writes++
	return nil
}

func (w *workerState) checkpointLocked() error {
	if w.walFile != nil {
		_ = w.walFile.Close()
		w.walFile = nil
	}
	if err := os.MkdirAll(filepath.Dir(w.walPath), 0o755); err != nil {
		return err
	}

	tmp := w.walPath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, op := range w.pending {
		b, err := json.Marshal(op)
		if err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write([]byte("E ")); err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write(b); err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write([]byte{'\n'}); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, w.walPath); err != nil {
		return err
	}
	w.writes = len(w.pending)
	return w.ensureWALFileLocked()
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
		return body, fmt.Errorf("status=%s body=%s", resp.Status, body)
	}
	return body, nil
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
	if attempt < 0 {
		return 100 * time.Millisecond
	}
	d := 100 * time.Millisecond * time.Duration(1<<attempt)
	if d > 1500*time.Millisecond {
		return 1500 * time.Millisecond
	}
	return d
}

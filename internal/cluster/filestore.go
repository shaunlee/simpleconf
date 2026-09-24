package cluster

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/hashicorp/raft"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

var storeCheckpointEvery = 512

type fileStore struct {
	mu sync.RWMutex

	dir          string
	snapshotPath string
	walPath      string

	lowIndex  uint64
	highIndex uint64
	logs      map[uint64]*raft.Log
	kv        map[string][]byte
	kvInt     map[string]uint64

	walFile *os.File
	walOps  int

	// With lazySync set (raft.fsync: everysec), log appends return before
	// they are fsynced and a background loop fsyncs them within a second.
	// Term, vote, checkpoints and WAL truncation are always fsynced.
	lazySync   bool
	dirty      bool // the WAL holds writes that are not fsynced yet
	syncFailed bool // the background fsync has failed and said so
	syncs      int  // WAL fsyncs, for tests
	stopSync   chan struct{}
	syncDone   chan struct{}
}

type persistedState struct {
	LowIndex  uint64            `json:"low_index"`
	HighIndex uint64            `json:"high_index"`
	Logs      []persistedLog    `json:"logs"`
	KV        map[string][]byte `json:"kv"`
	KVInt     map[string]uint64 `json:"kv_int"`
}

type persistedLog struct {
	Index      uint64       `json:"index"`
	Term       uint64       `json:"term"`
	Type       raft.LogType `json:"type"`
	Data       []byte       `json:"data,omitempty"`
	Extensions []byte       `json:"extensions,omitempty"`
	AppendedAt time.Time    `json:"appended_at"`
}

type walRecord struct {
	Type string `json:"type"`

	Logs []persistedLog `json:"logs,omitempty"`

	Min uint64 `json:"min,omitempty"`
	Max uint64 `json:"max,omitempty"`

	Key string `json:"key,omitempty"`
	Val []byte `json:"val,omitempty"`

	ValUint64 uint64 `json:"val_u64,omitempty"`
}

func newFileStore(dir string) (*fileStore, error) {
	if len(dir) == 0 {
		return nil, fmt.Errorf("raft dir is required for file store")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &fileStore{
		dir:          dir,
		snapshotPath: filepath.Join(dir, "raft-state.snapshot.json"),
		walPath:      filepath.Join(dir, "raft-state.wal"),
		logs:         make(map[uint64]*raft.Log),
		kv:           make(map[string][]byte),
		kvInt:        make(map[string]uint64),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *fileStore) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.loadSnapshotLocked(); err != nil {
		return err
	}
	if err := s.replayWALLocked(); err != nil {
		return err
	}
	s.recomputeBoundsLocked()
	return nil
}

func (s *fileStore) loadSnapshotLocked() error {
	b, err := os.ReadFile(s.snapshotPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(b) == 0 {
		return nil
	}

	var state persistedState
	if err := json.Unmarshal(b, &state); err != nil {
		return err
	}
	s.logs = make(map[uint64]*raft.Log, len(state.Logs))
	for _, pl := range state.Logs {
		l := &raft.Log{
			Index:      pl.Index,
			Term:       pl.Term,
			Type:       pl.Type,
			Data:       append([]byte(nil), pl.Data...),
			Extensions: append([]byte(nil), pl.Extensions...),
			AppendedAt: pl.AppendedAt,
		}
		s.logs[l.Index] = l
	}
	if state.KV != nil {
		s.kv = state.KV
	} else {
		s.kv = make(map[string][]byte)
	}
	if state.KVInt != nil {
		s.kvInt = state.KVInt
	} else {
		s.kvInt = make(map[string]uint64)
	}
	s.lowIndex = state.LowIndex
	s.highIndex = state.HighIndex
	return nil
}

// replayWALLocked applies the WAL on top of the checkpoint. Records are read
// with no length limit: one line holds a whole batch of log entries. A last
// record cut short, by a crash in the middle of a write, is dropped and the
// file truncated after the last whole record; it was never acknowledged. A bad
// record anywhere else means the file is damaged, and is an error.
func (s *fileStore) replayWALLocked() error {
	f, err := os.Open(s.walPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	reader := bufio.NewReaderSize(f, 64*1024)
	var good int64 // offset just past the last whole record
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			if len(line) > 0 {
				return s.dropTornTailLocked(good, len(line))
			}
			return nil
		}
		if err != nil {
			return err
		}
		body := bytes.TrimSpace(line)
		if len(body) > 0 {
			var rec walRecord
			if err := json.Unmarshal(body, &rec); err != nil {
				if _, perr := reader.Peek(1); perr == io.EOF {
					return s.dropTornTailLocked(good, len(line))
				}
				return fmt.Errorf("raft wal: bad record at byte %d: %w", good, err)
			}
			if err := s.applyWALRecordLocked(rec); err != nil {
				return err
			}
			s.walOps++
		}
		good += int64(len(line))
	}
}

// dropTornTailLocked cuts the WAL back to size bytes, removing a last record
// that was only partly written.
func (s *fileStore) dropTornTailLocked(size int64, torn int) error {
	log.Printf("raft wal: dropping an incomplete last record (%d bytes)", torn)
	f, err := os.OpenFile(s.walPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(size); err != nil {
		return err
	}
	return f.Sync()
}

func (s *fileStore) applyWALRecordLocked(rec walRecord) error {
	switch rec.Type {
	case "store_logs":
		for _, pl := range rec.Logs {
			l := &raft.Log{
				Index:      pl.Index,
				Term:       pl.Term,
				Type:       pl.Type,
				Data:       append([]byte(nil), pl.Data...),
				Extensions: append([]byte(nil), pl.Extensions...),
				AppendedAt: pl.AppendedAt,
			}
			s.logs[l.Index] = l
		}
	case "delete_range":
		for i := rec.Min; i <= rec.Max; i++ {
			delete(s.logs, i)
			if i == rec.Max {
				break
			}
		}
	case "set":
		s.kv[rec.Key] = append([]byte(nil), rec.Val...)
	case "set_u64":
		s.kvInt[rec.Key] = rec.ValUint64
	default:
		return fmt.Errorf("unknown wal record type: %s", rec.Type)
	}
	return nil
}

// appendWALLocked appends rec. A durable record is fsynced before this
// returns; a log append may be left to the background loop under lazySync.
func (s *fileStore) appendWALLocked(rec walRecord, durable bool) error {
	if s.walFile == nil {
		if err := os.MkdirAll(filepath.Dir(s.walPath), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(s.walPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		// The file may have just been created.
		if err := syncDir(filepath.Dir(s.walPath)); err != nil {
			f.Close()
			return err
		}
		s.walFile = f
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.walFile.Write(append(b, '\n')); err != nil {
		return err
	}
	// Raft treats a stored term or vote as durable once this returns: a
	// node that forgets its vote can vote twice in one term. Log entries
	// get the same treatment unless raft.fsync is everysec. The WAL is one
	// file, so this also covers any log entries still waiting for the loop.
	if durable || !s.lazySync {
		if err := s.syncWALLocked(); err != nil {
			return err
		}
	} else {
		s.dirty = true
	}
	s.walOps++
	if s.walOps >= storeCheckpointEvery {
		return s.compactLocked()
	}
	return nil
}

func (s *fileStore) compactLocked() error {
	if s.walFile != nil {
		_ = s.walFile.Close()
		s.walFile = nil
	}
	if err := s.persistSnapshotLocked(); err != nil {
		return err
	}
	tmp := s.walPath + ".tmp"
	if err := os.WriteFile(tmp, nil, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.walPath); err != nil {
		return err
	}
	if err := syncDir(s.dir); err != nil {
		return err
	}
	// Everything the old WAL held is in the fsynced checkpoint.
	s.dirty = false
	s.walOps = 0
	return nil
}

func (s *fileStore) syncWALLocked() error {
	if err := s.walFile.Sync(); err != nil {
		return err
	}
	s.syncs++
	s.dirty = false
	return nil
}

// syncEvery switches log appends to lazy fsync and starts the loop that
// fsyncs them every d.
func (s *fileStore) syncEvery(d time.Duration) {
	s.mu.Lock()
	s.lazySync = true
	s.stopSync = make(chan struct{})
	s.syncDone = make(chan struct{})
	stop, done := s.stopSync, s.syncDone
	s.mu.Unlock()

	go func() {
		defer close(done)
		t := time.NewTicker(d)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.syncIfDirty()
			case <-stop:
				return
			}
		}
	}()
}

func (s *fileStore) syncIfDirty() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty || s.walFile == nil {
		return
	}
	if err := s.syncWALLocked(); err != nil {
		if !s.syncFailed {
			s.syncFailed = true
			log.Printf("raft wal: background fsync failed, will keep trying: %v", err)
		}
		return
	}
	s.syncFailed = false
}

// Close stops the background loop, fsyncs what it had not, and closes the WAL.
func (s *fileStore) Close() error {
	s.mu.Lock()
	stop, done := s.stopSync, s.syncDone
	s.stopSync = nil
	s.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.walFile == nil {
		return nil
	}
	var err error
	if s.dirty {
		err = s.syncWALLocked()
	}
	if cerr := s.walFile.Close(); err == nil {
		err = cerr
	}
	s.walFile = nil
	return err
}

func (s *fileStore) persistSnapshotLocked() error {
	indices := make([]uint64, 0, len(s.logs))
	for idx := range s.logs {
		indices = append(indices, idx)
	}
	slices.Sort(indices)
	logs := make([]persistedLog, 0, len(indices))
	for _, idx := range indices {
		l := s.logs[idx]
		logs = append(logs, persistedLog{
			Index:      l.Index,
			Term:       l.Term,
			Type:       l.Type,
			Data:       append([]byte(nil), l.Data...),
			Extensions: append([]byte(nil), l.Extensions...),
			AppendedAt: l.AppendedAt,
		})
	}
	payload := persistedState{
		LowIndex:  s.lowIndex,
		HighIndex: s.highIndex,
		Logs:      logs,
		KV:        s.kv,
		KVInt:     s.kvInt,
	}
	return atomicWriteJSON(s.snapshotPath, payload)
}

// atomicWriteJSON replaces path with v's JSON. The data is fsynced before the
// rename and the rename before returning, so the WAL can be truncated after it.
func atomicWriteJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes renames and file creations in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *fileStore) recomputeBoundsLocked() {
	if len(s.logs) == 0 {
		s.lowIndex = 0
		s.highIndex = 0
		return
	}
	var low uint64
	var high uint64
	for idx := range s.logs {
		if low == 0 || idx < low {
			low = idx
		}
		if idx > high {
			high = idx
		}
	}
	s.lowIndex = low
	s.highIndex = high
}

func (s *fileStore) FirstIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lowIndex, nil
}

func (s *fileStore) LastIndex() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.highIndex, nil
}

func (s *fileStore) GetLog(index uint64, logOut *raft.Log) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.logs[index]
	if !ok {
		return raft.ErrLogNotFound
	}
	*logOut = *l
	logOut.Data = append([]byte(nil), l.Data...)
	logOut.Extensions = append([]byte(nil), l.Extensions...)
	return nil
}

func (s *fileStore) StoreLog(logIn *raft.Log) error {
	return s.StoreLogs([]*raft.Log{logIn})
}

func (s *fileStore) StoreLogs(logsIn []*raft.Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	persisted := make([]persistedLog, 0, len(logsIn))
	for _, l := range logsIn {
		cp := *l
		cp.Data = append([]byte(nil), l.Data...)
		cp.Extensions = append([]byte(nil), l.Extensions...)
		s.logs[cp.Index] = &cp
		persisted = append(persisted, persistedLog{
			Index:      cp.Index,
			Term:       cp.Term,
			Type:       cp.Type,
			Data:       append([]byte(nil), cp.Data...),
			Extensions: append([]byte(nil), cp.Extensions...),
			AppendedAt: cp.AppendedAt,
		})
	}
	s.recomputeBoundsLocked()
	return s.appendWALLocked(walRecord{
		Type: "store_logs",
		Logs: persisted,
	}, false)
}

func (s *fileStore) DeleteRange(min, max uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := min; i <= max; i++ {
		delete(s.logs, i)
		if i == max {
			break
		}
	}
	s.recomputeBoundsLocked()
	return s.appendWALLocked(walRecord{
		Type: "delete_range",
		Min:  min,
		Max:  max,
	}, false)
}

func (s *fileStore) Set(key []byte, val []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := append([]byte(nil), val...)
	k := string(key)
	s.kv[k] = cp
	return s.appendWALLocked(walRecord{
		Type: "set",
		Key:  k,
		Val:  cp,
	}, true)
}

func (s *fileStore) Get(key []byte) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	val := s.kv[string(key)]
	if val == nil {
		return nil, errors.New("not found")
	}
	return append([]byte(nil), val...), nil
}

func (s *fileStore) SetUint64(key []byte, val uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := string(key)
	s.kvInt[k] = val
	return s.appendWALLocked(walRecord{
		Type:      "set_u64",
		Key:       k,
		ValUint64: val,
	}, true)
}

func (s *fileStore) GetUint64(key []byte) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.kvInt[string(key)]
	if !ok {
		return 0, errors.New("not found")
	}
	return v, nil
}

package db

import (
	"bufio"
	stdjson "encoding/json"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type persistable struct {
	command cmd
	key     string
	value   any
	// done is closed once the record is durable on disk. Only set under
	// FsyncAlways, where the caller waits for it before reporting success.
	done chan struct{}
}

type cmd uint8

const (
	setCmd = cmd(iota)
	delCmd
	dumpCmd
	setRawCmd
	closeCmd
)

// FsyncPolicy controls when the append-only file is fsynced, following the
// same three-way choice Redis offers.
type FsyncPolicy uint8

const (
	// FsyncEverysec fsyncs at most once per second in the background. A crash
	// or power loss can lose up to one second of acknowledged writes.
	FsyncEverysec FsyncPolicy = iota
	// FsyncAlways fsyncs before a write is acknowledged. No acknowledged write
	// is lost, at a significant cost in throughput.
	FsyncAlways
	// FsyncNo never fsyncs explicitly and leaves it to the operating system.
	// Acknowledged writes survive kill -9 but not power loss.
	FsyncNo
)

// ParseFsyncPolicy maps a config string onto a policy.
func ParseFsyncPolicy(v string) (FsyncPolicy, error) {
	switch v {
	case "", "everysec":
		return FsyncEverysec, nil
	case "always":
		return FsyncAlways, nil
	case "no":
		return FsyncNo, nil
	}
	return FsyncEverysec, fmt.Errorf("unknown fsync policy %q (want always, everysec or no)", v)
}

// SetFsyncPolicy must be called before Init.
func SetFsyncPolicy(p FsyncPolicy) {
	fsyncPolicy = p
}

var (
	fsyncPolicy = FsyncEverysec

	dbfn string
	db   *os.File
	// configRoot is the document, held as a tree so that a keyed read or
	// write costs O(depth) rather than O(offset). configSnapshot is its
	// serialized form, rebuilt lazily after a write; it serves whole-document
	// reads and the gjson query paths, and because it is an immutable string a
	// value a reader already holds can never change underneath it.
	configRoot     = newTreeObj()
	configSnapshot = "{}"
	configStale    bool
	configMu       sync.RWMutex

	wg          sync.WaitGroup
	persists    = make(chan persistable, 1024)
	persistExit chan struct{}

	aofEnabled = true
)

// DisableAOF disables Append-Only File persistence (useful when Raft manages state)
func DisableAOF() {
	aofEnabled = false
}

// rawJSON renders v exactly as sjson.SetBytesOptions would. The document is no
// longer held as a string, but the rules are kept because they decide the
// stored text of every value, and changing them would change what the API
// returns.
func rawJSON(v any) ([]byte, error) {
	switch v := v.(type) {
	case nil:
		return []byte("null"), nil
	case string:
		return stringifyJSON(v), nil
	case []byte:
		return stringifyJSON(string(v)), nil
	case bool:
		if v {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case int:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int8:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int16:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int32:
		return strconv.AppendInt(nil, int64(v), 10), nil
	case int64:
		return strconv.AppendInt(nil, v, 10), nil
	case uint:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint8:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint16:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint32:
		return strconv.AppendUint(nil, uint64(v), 10), nil
	case uint64:
		return strconv.AppendUint(nil, v, 10), nil
	case float32:
		return strconv.AppendFloat(nil, float64(v), 'f', -1, 64), nil
	case float64:
		return strconv.AppendFloat(nil, v, 'f', -1, 64), nil
	}
	return stdjson.Marshal(v)
}

// stringifyJSON mirrors sjson's appendStringify.
func stringifyJSON(s string) []byte {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > 0x7f || s[i] == '"' || s[i] == '\\' {
			b, _ := stdjson.Marshal(s)
			return b
		}
	}
	b := make([]byte, 0, len(s)+2)
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"')
}

func setonly(k string, v any) error {
	raw, err := rawJSON(v)
	if err != nil {
		return err
	}
	return setrawonly(k, raw)
}

func setrawonly(k string, raw []byte) error {
	val := parseTreeRaw(raw)
	configMu.Lock()
	defer configMu.Unlock()
	n, err := treeSet(configRoot, splitTreePath(k), val)
	if err != nil {
		return err
	}
	configRoot = n.(*treeObj)
	configStale = true
	return nil
}

func Set(k string, v any) error {
	if err := setonly(k, v); err != nil {
		return err
	}

	appendAOF(persistable{command: setCmd, key: k, value: v})
	return nil
}

func delonly(k string) {
	configMu.Lock()
	defer configMu.Unlock()
	configRoot = treeDel(configRoot, splitTreePath(k)).(*treeObj)
	configStale = true
}

func Del(k string) {
	delonly(k)
	appendAOF(persistable{command: delCmd, key: k})
}

func Get(k string) string {
	// A plain key path is answered from the tree without touching the
	// snapshot, so its cost does not grow with the document.
	if len(k) > 0 && isPlainPath(k) {
		configMu.RLock()
		defer configMu.RUnlock()
		n, ok := treeLookup(configRoot, splitTreePath(k))
		if !ok {
			return ""
		}
		if l, isLeaf := n.(treeLeaf); isLeaf {
			return string(l)
		}
		return string(appendTree(nil, n))
	}
	// The whole document, and gjson's query syntax, are served from the
	// snapshot. Delegating queries to gjson keeps their semantics exact
	// rather than reimplemented.
	snapshot := snapshot()
	if len(k) == 0 {
		return snapshot
	}
	return gjson.Get(snapshot, k).Raw
}

// snapshot returns the serialized document, rebuilding it if a write has
// happened since it was last taken.
func snapshot() string {
	configMu.RLock()
	if !configStale {
		s := configSnapshot
		configMu.RUnlock()
		return s
	}
	configMu.RUnlock()

	configMu.Lock()
	defer configMu.Unlock()
	if configStale {
		configSnapshot = string(appendTree(make([]byte, 0, len(configSnapshot)+64), configRoot))
		configStale = false
	}
	return configSnapshot
}

func Replace(raw string) error {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return err
	}

	root, ok := parseTreeRaw([]byte(raw)).(*treeObj)
	if !ok {
		root = newTreeObj()
	}
	configMu.Lock()
	configRoot = root
	configSnapshot = raw
	configStale = false
	configMu.Unlock()
	return nil
}

// cloneonly copies a key path in memory and reports the raw value it copied,
// which is empty when the source does not exist.
func cloneonly(fk, tk string) string {
	configMu.Lock()
	src, ok := treeLookup(configRoot, splitTreePath(fk))
	if !ok {
		configMu.Unlock()
		return ""
	}
	raw := appendTree(nil, src)
	configMu.Unlock()
	if len(raw) == 0 {
		return ""
	}
	if err := setrawonly(tk, raw); err != nil {
		return ""
	}
	return string(raw)
}

func Clone(fk, tk string) {
	if v := cloneonly(fk, tk); len(v) > 0 {
		appendAOF(persistable{command: setRawCmd, key: tk, value: v})
	}
}

func Vacuum() {
	appendAOF(persistable{command: dumpCmd})
}

// appendAOF queues a record for the persist goroutine. Under FsyncAlways it
// blocks until the record is on disk, so a successful Set/Del/Clone means the
// data survives both kill -9 and power loss.
func appendAOF(row persistable) {
	if !aofEnabled {
		return
	}
	if fsyncPolicy == FsyncAlways {
		row.done = make(chan struct{})
	}
	wg.Add(1)
	persists <- row
	if row.done != nil {
		<-row.done
	}
}

func Init(dir string) {
	log.Println("init db ...")
	dbfn = filepath.Join(dir, "data.aof")

	if aofEnabled {
		if err := reopen(); err != nil {
			log.Fatalf("failed to open db: %v", err)
		}

		reader := bufio.NewReader(db)
	loop:
		for {
			kl := readline(reader)
			if kl == nil {
				break
			}

			switch kl[0] {
			case '+':
				if vl := readline(reader); vl == nil {
					break loop
				} else {
					if err := setrawonly(string(kl[1:]), vl); err != nil {
						log.Printf("skipping bad aof record for %q: %v", kl[1:], err)
					}
				}
			case '*':
				if vl := readline(reader); vl == nil {
					break loop
				} else {
					root, ok := parseTreeRaw(vl).(*treeObj)
					if !ok {
						root = newTreeObj()
					}
					configMu.Lock()
					configRoot = root
					configStale = true
					configMu.Unlock()
				}
			case '-':
				delonly(string(kl[1:]))
			}
		}
	}

	// A previous Init may have left a persist goroutine running (Close without
	// exit=true does not stop it). Two consumers on the same channel race for
	// the close command, and the loser's Close blocks on a persistExit that is
	// never closed, so retire the old one first.
	stopPersist()

	persistExit = make(chan struct{})
	go persist()
	log.Println("db loaded")
}

func stopPersist() {
	if persistExit == nil {
		return
	}
	select {
	case <-persistExit:
		return
	default:
	}
	wg.Add(1)
	persists <- persistable{command: closeCmd}
	<-persistExit
}

func reopen() error {
	if db != nil {
		db.Sync()
		db.Close()
		db = nil
	}
	var err error
	db, err = os.OpenFile(dbfn, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
	if err != nil {
		log.Printf("failed to open db file: %v", err)
		db = nil
		return err
	}
	return nil
}

func doEraseAndDump() {
	if db != nil {
		db.Sync()
		db.Close()
		db = nil
	}

	// Rename the old AOF for backup
	os.Rename(dbfn, dbfn+"."+time.Now().Format("060102150405"))

	if err := reopen(); err != nil {
		log.Printf("failed to reopen db after vacuum: %v", err)
		return
	}

	fmt.Fprintf(db, "*\n%s\n", snapshot())
	db.Sync()
}

func Close(exit ...bool) {
	if len(exit) > 0 && exit[0] {
		log.Println("closing db ...")
		if aofEnabled {
			wg.Wait()
			Vacuum()
			wg.Wait()
		}

		wg.Add(1)
		persists <- persistable{command: closeCmd}
		<-persistExit
	}

	if db != nil {
		db.Sync()
		db.Close()
		db = nil
	}
}

func persist() {
	defer close(persistExit)

	var w *bufio.Writer
	if db != nil {
		w = bufio.NewWriterSize(db, 64*1024)
	}
	// Writers waiting on the current batch, and whether anything has been
	// written since the last fsync.
	var waiters []chan struct{}
	unsynced := false

	flush := func() {
		if w != nil {
			w.Flush()
		}
	}
	syncNow := func() {
		flush()
		if unsynced && db != nil {
			if err := db.Sync(); err != nil {
				log.Printf("failed to fsync db: %v", err)
			}
			unsynced = false
		}
	}
	release := func() {
		for _, c := range waiters {
			close(c)
		}
		waiters = waiters[:0]
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	write := func(row persistable) (stop bool) {
		switch row.command {
		case setCmd:
			if w != nil {
				pv, err := json.Marshal(row.value)
				if err != nil {
					log.Printf("failed to marshal value for key %s: %v", row.key, err)
				} else {
					fmt.Fprintf(w, "+%s\n%s\n", row.key, pv)
					unsynced = true
				}
			}
		case setRawCmd:
			if w != nil {
				fmt.Fprintf(w, "+%s\n%s\n", row.key, row.value)
				unsynced = true
			}
		case delCmd:
			if w != nil {
				fmt.Fprintf(w, "-%s\n", row.key)
				unsynced = true
			}
		case dumpCmd:
			flush()
			doEraseAndDump() // fsyncs the rewritten file itself
			if db != nil {
				w = bufio.NewWriterSize(db, 64*1024)
			} else {
				w = nil
			}
			unsynced = false
		case closeCmd:
			syncNow()
			wg.Done()
			release()
			return true
		}
		if row.done != nil {
			waiters = append(waiters, row.done)
		}
		wg.Done()
		return false
	}

	for {
		select {
		case row := <-persists:
			// Drain whatever is already queued, then pay for one flush (and,
			// under FsyncAlways, one fsync) for the whole batch. Group commit:
			// concurrent writers share the cost of a single fsync.
			for {
				if write(row) {
					return
				}
				select {
				case next := <-persists:
					row = next
					continue
				default:
				}
				break
			}
			flush()
			if fsyncPolicy == FsyncAlways {
				syncNow()
			}
			release()
		case <-ticker.C:
			if fsyncPolicy == FsyncEverysec {
				syncNow()
			}
		}
	}
}

func readline(reader *bufio.Reader) []byte {
	if line, err := reader.ReadBytes('\n'); err != nil {
		return nil
	} else {
		return line[:len(line)-1]
	}
}

package db

import (
	"bufio"
	"bytes"
	stdjson "encoding/json"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	b := make([]byte, len(s)+2)
	b[0] = '"'
	copy(b[1:], s)
	b[len(b)-1] = '"'
	return b
}

// quoteJSONString is stringifyJSON for the common case of a plain string,
// producing the canonical JSON text in one allocation.
func quoteJSONString(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] < ' ' || s[i] > 0x7f || s[i] == '"' || s[i] == '\\' {
			b, _ := stdjson.Marshal(s)
			return string(b)
		}
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	b.WriteString(s)
	b.WriteByte('"')
	return b.String()
}

// JSONError is bad JSON from a client write. Callers map it to 422.
type JSONError struct{ Err error }

func (e *JSONError) Error() string { return e.Err.Error() }
func (e *JSONError) Unwrap() error { return e.Err }

func valueToNode(v any) (any, error) {
	switch v := v.(type) {
	case nil:
		return treeLeaf("null"), nil
	case string:
		return treeLeaf(quoteJSONString(v)), nil
	case bool:
		if v {
			return treeLeaf("true"), nil
		}
		return treeLeaf("false"), nil
	case int:
		return treeLeaf(strconv.FormatInt(int64(v), 10)), nil
	case int8:
		return treeLeaf(strconv.FormatInt(int64(v), 10)), nil
	case int16:
		return treeLeaf(strconv.FormatInt(int64(v), 10)), nil
	case int32:
		return treeLeaf(strconv.FormatInt(int64(v), 10)), nil
	case int64:
		return treeLeaf(strconv.FormatInt(v, 10)), nil
	case uint:
		return treeLeaf(strconv.FormatUint(uint64(v), 10)), nil
	case uint8:
		return treeLeaf(strconv.FormatUint(uint64(v), 10)), nil
	case uint16:
		return treeLeaf(strconv.FormatUint(uint64(v), 10)), nil
	case uint32:
		return treeLeaf(strconv.FormatUint(uint64(v), 10)), nil
	case uint64:
		return treeLeaf(strconv.FormatUint(v, 10)), nil
	case float32:
		return treeLeaf(string(strconv.AppendFloat(nil, float64(v), 'f', -1, 64))), nil
	case float64:
		return treeLeaf(string(strconv.AppendFloat(nil, v, 'f', -1, 64))), nil
	case json.Number:
		if c, ok := canonicalNumberText(v.String()); ok {
			return treeLeaf(c), nil
		}
		return nil, &JSONError{Err: fmt.Errorf("invalid number %s", v.String())}
	default:
		raw, err := rawJSON(v)
		if err != nil {
			return nil, err
		}
		return nodeFromRaw(raw), nil
	}
}

// nodeFromRaw stores a scalar's text as a leaf. Objects and arrays are parsed
// into the tree. The bytes are the canonical form produced by rawJSON, so the
// leaf text matches what gjson would have kept.
func nodeFromRaw(raw []byte) any {
	if isJSONScalar(raw) {
		if c, ok := canonicalNumberText(string(raw)); ok {
			return treeLeaf(c)
		}
		return treeLeaf(string(raw))
	}
	return parseTreeRaw(raw)
}

func isJSONScalar(raw []byte) bool {
	for _, c := range raw {
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue
		}
		return c != '{' && c != '['
	}
	return false
}

// canonicalScalar reports client JSON that is already in the form rawJSON
// would emit: true, false, null, or a plain quoted string.
func canonicalScalar(raw []byte) (treeLeaf, bool) {
	switch {
	case bytes.Equal(raw, []byte("true")):
		return "true", true
	case bytes.Equal(raw, []byte("false")):
		return "false", true
	case bytes.Equal(raw, []byte("null")):
		return "null", true
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", false
	}
	for i := 1; i < len(raw)-1; i++ {
		c := raw[i]
		if c == '\\' || c == '"' || c < ' ' || c > 0x7f {
			return "", false
		}
	}
	return treeLeaf(string(raw)), true
}

func setNode(k string, val any) error {
	configMu.Lock()
	defer configMu.Unlock()
	n, err := treeSetPath(configRoot, k, val)
	if err != nil {
		return err
	}
	configRoot = n.(*treeObj)
	configStale = true
	return nil
}

func setonly(k string, v any) error {
	val, err := valueToNode(v)
	if err != nil {
		return err
	}
	return setNode(k, val)
}

func setrawonly(k string, raw []byte) error {
	return setNode(k, nodeFromRaw(raw))
}

// SetRaw writes client JSON. Plain scalars are stored in one copy; anything
// else is decoded with json.Number so an integer is not rounded through float64.
func SetRaw(k string, raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if leaf, ok := canonicalScalar(raw); ok {
		if err := setNode(k, leaf); err != nil {
			return err
		}
		appendAOF(persistable{command: setRawCmd, key: k, value: string(leaf)})
		return nil
	}
	v, err := decodeJSON(raw)
	if err != nil {
		return &JSONError{Err: err}
	}
	return Set(k, v)
}

// decodeJSON parses one JSON value. Numbers stay json.Number instead of float64.
func decodeJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("extra data after JSON value")
		}
		return nil, err
	}
	return v, nil
}

func Set(k string, v any) error {
	val, err := valueToNode(v)
	if err != nil {
		return err
	}
	// Render before setNode publishes the node. After that, another Set can
	// change a child while this walk still reads the same object.
	// Get(k) is also the wrong text for an append path such as arr.-1.
	var text string
	if aofEnabled {
		text = storedText(val)
	}
	if err := setNode(k, val); err != nil {
		return err
	}
	if aofEnabled {
		appendAOF(persistable{command: setRawCmd, key: k, value: text})
	}
	return nil
}

func storedText(val any) string {
	if leaf, ok := val.(treeLeaf); ok {
		return string(leaf)
	}
	return string(appendTree(nil, val))
}

func delonly(k string) {
	configMu.Lock()
	defer configMu.Unlock()
	configRoot = treeDelPath(configRoot, k).(*treeObj)
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
		n, ok := treeLookupPath(configRoot, k)
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
	src, ok := treeLookupPath(configRoot, fk)
	if !ok {
		configMu.Unlock()
		return ""
	}
	if leaf, ok := src.(treeLeaf); ok {
		configMu.Unlock()
		// The leaf text is immutable, so the copy shares it.
		if err := setNode(tk, leaf); err != nil {
			return ""
		}
		return string(leaf)
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
	// The writer runs after this call returns. Copy anything that might
	// still point at a request buffer.
	row.key = strings.Clone(row.key)
	if s, ok := row.value.(string); ok {
		row.value = strings.Clone(s)
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
	// A second Init replaces the file. Stop the writer first so it is not
	// still reading that file.
	stopPersist()
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
	}
	// The writer goroutine reads db from its own loop. Stop it before closing
	// the file; otherwise the two race, and a concurrent Set's log race is
	// hidden behind this one.
	stopPersist()

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

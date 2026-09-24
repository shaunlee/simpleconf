package db

import (
	"bufio"
	"bytes"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	delCmd = cmd(iota)
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

// SetBackups sets how many previous AOFs a vacuum keeps; a negative n keeps
// them all. It must be called before Init.
func SetBackups(n int) {
	backups = n
}

var (
	fsyncPolicy = FsyncEverysec
	backups     = 3
	now         = time.Now // names vacuum backups; tests replace it

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

// ErrWritesRefused is returned for a write made while the AOF cannot be
// written, under db.fsync everysec or no. Reads still work, and writes are
// accepted again once the pending records reach the file.
var ErrWritesRefused = errors.New("writes refused: the append-only file cannot be written")

var (
	writesRefused atomic.Bool

	// Replaced by tests to simulate a failing disk and to observe an exit.
	writeFile = func(f *os.File, b []byte) (int, error) { return f.Write(b) }
	syncFile  = func(f *os.File) error { return f.Sync() }
	fatalf    = log.Fatalf
	tickEvery = time.Second
)

func checkWritable() error {
	if aofEnabled && writesRefused.Load() {
		return ErrWritesRefused
	}
	return nil
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
		return floatNode(float64(v))
	case float64:
		return floatNode(v)
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

// floatNode rejects NaN and ±Inf, which JSON cannot represent, as rawJSON
// does when they are nested.
func floatNode(f float64) (any, error) {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, &JSONError{Err: fmt.Errorf("unsupported number %v", f)}
	}
	return treeLeaf(string(strconv.AppendFloat(nil, f, 'f', -1, 64))), nil
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
	if err := checkWritable(); err != nil {
		return err
	}
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
	if err := checkWritable(); err != nil {
		return err
	}
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

func Del(k string) error {
	if err := checkWritable(); err != nil {
		return err
	}
	delonly(k)
	appendAOF(persistable{command: delCmd, key: k})
	return nil
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

func Clone(fk, tk string) error {
	if err := checkWritable(); err != nil {
		return err
	}
	if v := cloneonly(fk, tk); len(v) > 0 {
		appendAOF(persistable{command: setRawCmd, key: tk, value: v})
	}
	return nil
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
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("failed to create db dir: %v", err)
		}
		if err := reopen(); err != nil {
			log.Fatalf("failed to open db: %v", err)
		}

		if err := replayAOF(); err != nil {
			log.Fatalf("failed to load db: %v", err)
		}
	}

	persistExit = make(chan struct{})
	go persist()
	log.Println("db loaded")
}

// replayAOF loads the AOF into memory. A record the file ends in the middle
// of, left by a crash during a write, is cut off, so the next append starts
// on a line of its own instead of running on from the partial one.
func replayAOF() error {
	reader := bufio.NewReader(db)
	var good int64 // offset just past the last whole record
loop:
	for {
		kl, ok := readRecordLine(reader)
		if !ok {
			break
		}
		size := int64(len(kl)) + 1
		if len(kl) == 0 {
			good += size
			continue
		}

		switch kl[0] {
		case '+':
			vl, ok := readRecordLine(reader)
			if !ok {
				break loop
			}
			size += int64(len(vl)) + 1
			if err := setrawonly(string(kl[1:]), vl); err != nil {
				log.Printf("skipping bad aof record for %q: %v", kl[1:], err)
			}
		case '*':
			vl, ok := readRecordLine(reader)
			if !ok {
				break loop
			}
			size += int64(len(vl)) + 1
			root, ok := parseTreeRaw(vl).(*treeObj)
			if !ok {
				root = newTreeObj()
			}
			configMu.Lock()
			configRoot = root
			configStale = true
			configMu.Unlock()
		case '-':
			delonly(string(kl[1:]))
		}
		good += size
	}

	info, err := db.Stat()
	if err != nil {
		return err
	}
	if info.Size() > good {
		log.Printf("dropping an incomplete last aof record (%d bytes)", info.Size()-good)
		if err := db.Truncate(good); err != nil {
			return err
		}
		return db.Sync()
	}
	return nil
}

// readRecordLine reads one line without its newline. ok is false at the end
// of the file, including a last line with no newline, which is incomplete.
func readRecordLine(reader *bufio.Reader) (line []byte, ok bool) {
	b, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, false
	}
	return b[:len(b)-1], true
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
		if err := db.Sync(); err != nil {
			log.Printf("failed to fsync db: %v", err)
		}
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

// doEraseAndDump rewrites the AOF as a single snapshot record. The snapshot is
// written and fsynced to a temp file first and then renamed over the AOF, so a
// crash or a failed write at any point leaves a complete AOF, old or new.
func doEraseAndDump() error {
	tmp := dbfn + ".tmp"
	if err := writeSnapshotFile(tmp); err != nil {
		log.Printf("vacuum failed, keeping the current aof: %v", err)
		os.Remove(tmp)
		return err
	}

	if db != nil {
		if err := db.Sync(); err != nil {
			log.Printf("failed to fsync db before vacuum: %v", err)
		}
		db.Close()
		db = nil
	}

	// Keep the old AOF as a backup. A backup from the same second is replaced,
	// as the rename this used to be did.
	backup := dbfn + "." + now().Format(backupTimeLayout)
	os.Remove(backup)
	if err := os.Link(dbfn, backup); err != nil && !os.IsNotExist(err) {
		log.Printf("failed to keep aof backup: %v", err)
	}
	renameErr := os.Rename(tmp, dbfn)
	if renameErr != nil {
		log.Printf("failed to replace aof after vacuum: %v", renameErr)
	}
	syncDir(filepath.Dir(dbfn))
	pruneBackups()

	if err := reopen(); err != nil {
		log.Printf("failed to reopen db after vacuum: %v", err)
		return err
	}
	return renameErr
}

const backupTimeLayout = "060102150405"

// pruneBackups removes the oldest vacuum backups beyond the number to keep.
// Only files named like one, data.aof. and twelve digits, are touched.
func pruneBackups() {
	if backups < 0 {
		return
	}
	dir, base := filepath.Dir(dbfn), filepath.Base(dbfn)
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Printf("failed to list aof backups: %v", err)
		return
	}
	var names []string
	for _, e := range entries {
		if isBackupName(base, e.Name()) {
			names = append(names, e.Name())
		}
	}
	// The timestamp sorts in time order.
	sort.Strings(names)
	for len(names) > backups {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			log.Printf("failed to remove old aof backup: %v", err)
		}
		names = names[1:]
	}
}

func isBackupName(base, name string) bool {
	stamp, ok := strings.CutPrefix(name, base+".")
	if !ok || len(stamp) != len(backupTimeLayout) {
		return false
	}
	for i := 0; i < len(stamp); i++ {
		if stamp[i] < '0' || stamp[i] > '9' {
			return false
		}
	}
	return true
}

func writeSnapshotFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "*\n%s\n", snapshot()); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// syncDir makes a rename in dir durable.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		log.Printf("failed to open data dir for fsync: %v", err)
		return
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		log.Printf("failed to fsync data dir: %v", err)
	}
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
		if err := db.Sync(); err != nil {
			log.Printf("failed to fsync db: %v", err)
		}
		db.Close()
		db = nil
	}
}

func persist() {
	defer close(persistExit)

	var (
		buf      []byte // records not yet written to the file
		size     int64  // the file's size after its last whole record
		waiters  []chan struct{}
		unsynced bool // written since the last fsync
	)
	statSize := func() {
		size = 0
		if db != nil {
			if info, err := db.Stat(); err == nil {
				size = info.Size()
			}
		}
	}
	statSize()

	// flush writes buf to the file. A failed or short write is cut back off
	// the file, so a retry starts on a record boundary; buf is kept for it.
	flush := func() error {
		if len(buf) == 0 || db == nil {
			return nil
		}
		n, err := writeFile(db, buf)
		if err == nil && n < len(buf) {
			err = io.ErrShortWrite
		}
		if err != nil {
			if n > 0 {
				if terr := db.Truncate(size); terr != nil {
					fatalf("aof write failed (%v) and the partial record could not be removed: %v", err, terr)
				}
			}
			return err
		}
		size += int64(n)
		buf = buf[:0]
		unsynced = true
		if writesRefused.Swap(false) {
			log.Println("aof writes recovered, accepting writes again")
		}
		return nil
	}
	// failed handles a write that did not reach the file. Under always the
	// writers are waiting for an acknowledgement that must not come, and
	// their changes are already in memory, so the process exits and the
	// restart reloads the file, as Redis does. Otherwise writes are refused
	// until a retry gets the pending records out. It reports whether to stop.
	failed := func(err error) bool {
		if fsyncPolicy == FsyncAlways {
			fatalf("aof write failed under db.fsync always, exiting: %v", err)
			return true
		}
		if !writesRefused.Swap(true) {
			log.Printf("aof write failed, refusing writes until it recovers: %v", err)
		}
		return false
	}
	// syncNow fsyncs. After a failed fsync the kernel may already have
	// dropped the unwritten pages, so a retry that succeeds proves nothing:
	// exit and reload the file, as PostgreSQL does. It reports whether to stop.
	syncNow := func() bool {
		if !unsynced || db == nil {
			return false
		}
		if err := syncFile(db); err != nil {
			fatalf("aof fsync failed, exiting: %v", err)
			return true
		}
		unsynced = false
		return false
	}
	release := func() {
		for _, c := range waiters {
			close(c)
		}
		waiters = waiters[:0]
	}

	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()

	write := func(row persistable) (stop bool) {
		switch row.command {
		case setRawCmd:
			if db != nil {
				buf = fmt.Appendf(buf, "+%s\n%s\n", row.key, row.value)
			}
		case delCmd:
			if db != nil {
				buf = fmt.Appendf(buf, "-%s\n", row.key)
			}
		case dumpCmd:
			if err := flush(); err != nil && failed(err) {
				return true
			}
			// The snapshot is the whole document, so it also covers records
			// a failed write left pending: a vacuum that works recovers.
			if err := doEraseAndDump(); err == nil {
				buf = buf[:0]
				unsynced = false
				if writesRefused.Swap(false) {
					log.Println("aof rewritten by vacuum, accepting writes again")
				}
			}
			statSize()
		case closeCmd:
			if err := flush(); err != nil {
				if failed(err) {
					return true
				}
				log.Printf("aof write failed at shutdown, %d bytes of records lost: %v", len(buf), err)
			} else if syncNow() {
				return true
			}
			wg.Done()
			release()
			return true
		}
		if row.done != nil {
			waiters = append(waiters, row.done)
		}
		wg.Done()
		// Bound the buffer during a long batch.
		if len(buf) >= 64*1024 {
			if err := flush(); err != nil && failed(err) {
				return true
			}
		}
		return false
	}

	for {
		select {
		case row := <-persists:
			// Drain whatever is already queued, then pay for one write (and,
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
			if err := flush(); err != nil {
				if failed(err) {
					return
				}
			} else if fsyncPolicy == FsyncAlways && syncNow() {
				return
			}
			release()
		case <-ticker.C:
			// Retries a failed write; under everysec, also the fsync.
			if err := flush(); err != nil {
				if failed(err) {
					return
				}
			} else if fsyncPolicy == FsyncEverysec && syncNow() {
				return
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

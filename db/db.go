package db

import (
	"bufio"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"log"
	"os"
	"path/filepath"
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

	dbfn          string
	db            *os.File
	configuration = "{}"
	configMu      sync.RWMutex

	wg          sync.WaitGroup
	persists    = make(chan persistable, 1024)
	persistExit chan struct{}

	aofEnabled = true
)

// DisableAOF disables Append-Only File persistence (useful when Raft manages state)
func DisableAOF() {
	aofEnabled = false
}

func setonly(k string, v any) (err error) {
	configMu.Lock()
	defer configMu.Unlock()
	configuration, err = sjson.Set(configuration, k, v)
	return
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
	configuration, _ = sjson.Delete(configuration, k)
}

func Del(k string) {
	delonly(k)
	appendAOF(persistable{command: delCmd, key: k})
}

func Get(k string) string {
	configMu.RLock()
	defer configMu.RUnlock()

	if len(k) == 0 {
		return configuration
	}
	return gjson.Get(configuration, k).Raw
}

func Replace(raw string) error {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return err
	}

	configMu.Lock()
	configuration = raw
	configMu.Unlock()
	return nil
}

func Clone(fk, tk string) {
	var v string
	func() {
		configMu.Lock()
		defer configMu.Unlock()
		v = gjson.Get(configuration, fk).Raw
		if len(v) > 0 {
			configuration, _ = sjson.SetRaw(configuration, tk, v)
		}
	}()
	if len(v) > 0 {
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
					configMu.Lock()
					configuration, _ = sjson.SetRaw(configuration, string(kl[1:]), string(vl))
					configMu.Unlock()
				}
			case '*':
				if vl := readline(reader); vl == nil {
					break loop
				} else {
					configMu.Lock()
					configuration = string(vl)
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

	configMu.RLock()
	snapshot := configuration
	configMu.RUnlock()

	fmt.Fprintf(db, "*\n%s\n", snapshot)
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

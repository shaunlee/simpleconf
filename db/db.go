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
}

type cmd uint8

const (
	setCmd = cmd(iota)
	delCmd
	dumpCmd
	setRawCmd
	closeCmd
)

var (
	dbfn          string
	db            *os.File
	configuration = "{}"
	configMu      sync.RWMutex

	wg          sync.WaitGroup
	persists    = make(chan *persistable, 1024)
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

	if aofEnabled {
		wg.Add(1)
		persists <- &persistable{setCmd, k, v}
	}
	return nil
}

func delonly(k string) {
	configMu.Lock()
	defer configMu.Unlock()
	configuration, _ = sjson.Delete(configuration, k)
}

func Del(k string) {
	delonly(k)
	if aofEnabled {
		wg.Add(1)
		persists <- &persistable{delCmd, k, nil}
	}
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
	if len(v) > 0 && aofEnabled {
		wg.Add(1)
		persists <- &persistable{setRawCmd, tk, v}
	}
}

func Vacuum() {
	if aofEnabled {
		wg.Add(1)
		persists <- &persistable{dumpCmd, "", nil}
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

	persistExit = make(chan struct{})
	go persist()
	log.Println("db loaded")
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
		persists <- &persistable{closeCmd, "", nil}
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
	for row := range persists {
		switch row.command {
		case setCmd:
			if db != nil {
				pv, err := json.Marshal(row.value)
				if err != nil {
					log.Printf("failed to marshal value for key %s: %v", row.key, err)
				} else {
					fmt.Fprintf(db, "+%s\n%s\n", row.key, pv)
				}
			}
		case setRawCmd:
			if db != nil {
				fmt.Fprintf(db, "+%s\n%s\n", row.key, row.value)
			}
		case delCmd:
			if db != nil {
				fmt.Fprintf(db, "-%s\n", row.key)
			}
		case dumpCmd:
			doEraseAndDump()
		case closeCmd:
			wg.Done()
			return
		}
		wg.Done()
	}
}

func readline(reader *bufio.Reader) []byte {
	if line, err := reader.ReadBytes('\n'); err != nil {
		return nil
	} else {
		return line[:len(line)-1]
	}
}

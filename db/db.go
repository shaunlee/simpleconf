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
)

var (
	dbfn          string
	db            *os.File
	configuration = "{}"
	configMu      sync.RWMutex

	wg       sync.WaitGroup
	suspend  = make(chan struct{})
	resume   = make(chan struct{})
	persists = make(chan *persistable, 100)
)

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

	wg.Add(1)
	persists <- &persistable{setCmd, k, v}
	return nil
}

func delonly(k string) {
	configMu.Lock()
	defer configMu.Unlock()
	configuration, _ = sjson.Delete(configuration, k)
}

func Del(k string) {
	delonly(k)

	wg.Add(1)
	persists <- &persistable{delCmd, k, nil}
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
	configMu.Lock()
	v := gjson.Get(configuration, fk).Raw
	if len(v) > 0 {
		configuration, _ = sjson.SetRaw(configuration, tk, v)
		configMu.Unlock()

		wg.Add(1)
		persists <- &persistable{setRawCmd, tk, v}
		return
	}
	configMu.Unlock()
}

func Vacuum() {
	suspend <- struct{}{}
	erase()
	resume <- struct{}{}

	wg.Add(1)
	persists <- &persistable{dumpCmd, "", nil}
}

func Init(dir string) {
	log.Println("init db ...")
	dbfn = filepath.Join(dir, "data.aof")

	reopen()

	reader := bufio.NewReader(db)
	for {
		kl := readline(reader)
		if kl == nil {
			break
		}

		switch kl[0] {
		case '+':
			if vl := readline(reader); vl == nil {
				break
			} else {
				configMu.Lock()
				configuration, _ = sjson.SetRaw(configuration, string(kl[1:]), string(vl))
				configMu.Unlock()
			}
		case '*':
			if vl := readline(reader); vl == nil {
				break
			} else {
				configMu.Lock()
				configuration = string(vl)
				configMu.Unlock()
			}
		case '-':
			delonly(string(kl[1:]))
		}
	}

	go persist()
	log.Println("db loaded")
}

func reopen() {
	Close()

	db, _ = os.OpenFile(dbfn, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
}

func erase() {
	Close()

	os.Rename(dbfn, dbfn+"."+time.Now().Format("060102150405"))

	reopen()
}

func Close(exit ...bool) {
	if db != nil {
		if len(exit) > 0 && exit[0] {
			log.Println("closing db ...")
			wg.Wait()
			Vacuum()
			wg.Wait()
		}
		db.Close()
		db = nil
	}
}

func persist() {
	for {
		select {
		case <-suspend:
			<-resume
		case row := <-persists:
			switch row.command {
			case setCmd:
				pv, _ := json.Marshal(row.value)
				fmt.Fprintf(db, "+%s\n%s\n", row.key, pv)
			case setRawCmd:
				fmt.Fprintf(db, "+%s\n%s\n", row.key, row.value)
			case delCmd:
				fmt.Fprintf(db, "-%s\n", row.key)
			case dumpCmd:
				configMu.RLock()
				fmt.Fprintf(db, "*\n%s\n", configuration)
				configMu.RUnlock()
			}
			wg.Done()
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

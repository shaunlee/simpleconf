package db

import (
	"bufio"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/shaunlee/simpleconf/utils"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"log"
	"os"
	"path/filepath"
	"sync"
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
)

var (
	dbfn          string
	db            *os.File
	Configuration = "{}"

	wg       sync.WaitGroup
	suspend  = make(chan struct{})
	resume   = make(chan struct{})
	persists = make(chan *persistable, 100)
)

func setonly(k string, v any) {
	Configuration, _ = sjson.Set(Configuration, k, v)
}

func Set(k string, v any) {
	setonly(k, v)

	wg.Add(1)
	persists <- &persistable{setCmd, k, v}
}

func delonly(k string) {
	Configuration, _ = sjson.Delete(Configuration, k)
}

func Del(k string) {
	delonly(k)

	wg.Add(1)
	persists <- &persistable{delCmd, k, nil}
}

func Get(k string) string {
	return gjson.Get(Configuration, k).Raw
}

func Clone(fk, tk string) {
	v := gjson.Get(Configuration, fk).Raw
	Configuration, _ = sjson.SetRaw(Configuration, tk, v)
}

func RewriteAof() {
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
		kl := utils.Readline(reader)
		if kl == nil {
			break
		}

		switch kl[0] {
		case '+':
			vl := utils.Readline(reader)
			if vl == nil {
				break
			}

			setonly(string(kl[1:]), utils.Bytes2Any(vl))
		case '*':
			vl := utils.Readline(reader)
			if vl == nil {
				break
			}

			Configuration = string(vl)
		case '-':
			delonly(string(kl[1:]))
		}
	}

	go persist()
}

func reopen() {
	Close()

	db, _ = os.OpenFile(dbfn, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
}

func erase() {
	Close()

	os.Rename(dbfn, dbfn+".bak")

	reopen()
}

func Close(exit ...bool) {
	if db != nil {
		if len(exit) > 0 && exit[0] {
			log.Println("closing db ...")
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
				wg.Done()
			case delCmd:
				fmt.Fprintf(db, "-%s\n", row.key)
				wg.Done()
			case dumpCmd:
				fmt.Fprintf(db, "*\n%s\n", Configuration)
				wg.Done()
			}
		}
	}
}

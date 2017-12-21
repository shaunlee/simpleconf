package models

import (
	"bufio"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/shaunlee/simpleconf/helpers"
	"os"
)

var (
	dbfilename    string
	db            *os.File
	Configuration = "{}"
)

func setonly(k string, v interface{}) {
	Configuration, _ = sjson.Set(Configuration, k, v)
}

func Set(k string, v interface{}) {
	setonly(k, v)

	persists <- &persistable{SetCmd, k, v}
}

func delonly(k string) {
	Configuration, _ = sjson.Delete(Configuration, k)
}

func Del(k string) {
	delonly(k)

	persists <- &persistable{DelCmd, k, nil}
}

func Get(k string) string {
	return gjson.Get(Configuration, k).Raw
}

func cponly(fk, tk string) {
	v := Get(fk)
	setonly(tk, helpers.Bytes2Obj([]byte(v)))
}

func Clone(fk, tk string) {
	v := Get(fk)
	Set(tk, helpers.Bytes2Obj([]byte(v)))
}

func RewriteAof() {
	suspend <- true
	erase()
	<-resume
	persists <- &persistable{DumpCmd, "", nil}
}

func InitDb(dbfile string) {
	dbfilename = dbfile

	reopen()

	reader := bufio.NewReader(db)
	for {
		kl := helpers.Readline(reader)
		if kl == nil {
			break
		}

		switch kl[0] {
		case '+':
			vl := helpers.Readline(reader)
			if vl == nil {
				break
			}

			setonly(string(kl[1:]), helpers.Bytes2Obj(vl))
		case '*':
			vl := helpers.Readline(reader)
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
	FreeDb()

	db, _ = os.OpenFile(dbfilename, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)
}

func erase() {
	FreeDb()

	os.Rename(dbfilename, dbfilename+".bak")

	reopen()
}

func FreeDb() {
	if db != nil {
		db.Close()
		db = nil
	}
}

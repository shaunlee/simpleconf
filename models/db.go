package models

import (
	"bufio"
	"github.com/json-iterator/go"
	"github.com/shaunlee/simpleconf/helpers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"io/ioutil"
	"os"
)

var (
	json          = jsoniter.ConfigCompatibleWithStandardLibrary
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

func dump() {
	ioutil.WriteFile("dump.json", []byte(Configuration), 0644)
}

func InitDb(dbfile string) {
	db, _ = os.OpenFile(dbfile, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0600)

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
		case '-':
			delonly(string(kl[1:]))
		}
	}

	go persist()
}

func FreeDb() {
	db.Close()
}

package db

import (
	"os"
	"fmt"
	"bufio"
	"io/ioutil"
	"github.com/json-iterator/go"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/shaunlee/simpleconf/helpers"
)

var (
	json = jsoniter.ConfigCompatibleWithStandardLibrary
	db *os.File
	Configuration = "{}"
)

func setonly(k string, v interface{}) {
	Configuration, _ = sjson.Set(Configuration, k, v)
}

func Set(k string, v interface{}) {
	setonly(k, v)

	fmt.Fprintf(db, "+%v\n", k)
	pv, _ := json.Marshal(v)
	fmt.Fprintf(db, "%s\n", string(pv))
}

func delonly(k string) {
	Configuration, _ = sjson.Delete(Configuration, k)
}

func Del(k string) {
	delonly(k)

	fmt.Fprintf(db, "-%v\n", k)
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
	defer func() {
		if err := recover(); err != nil {
		}
	}()

	db, _ = os.OpenFile(dbfile, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)

	reader := bufio.NewReader(db)
	for {
		kl := helpers.Readline(reader)
		if kl == nil {
			panic("end")
		}

		switch kl[0] {
		case '+':
			vl := helpers.Readline(reader)
			if vl == nil {
				panic("end")
			}

			setonly(string(kl[1:]), helpers.Bytes2Obj(vl))
		case '-':
			delonly(string(kl[1:]))
		}
	}
}

func FreeDb() {
	db.Close()
}

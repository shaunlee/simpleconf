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
)

var (
	dbfn          string
	db            *os.File
	Configuration = "{}"
)

func setonly(k string, v any) (err error) {
	Configuration, err = sjson.Set(Configuration, k, v)
	return
}

func Set(k string, v any) error {
	if err := setonly(k, v); err != nil {
		return err
	}

	pv, _ := json.Marshal(v)
	fmt.Fprintf(db, "+%s\n%s\n", k, pv)
	return nil
}

func delonly(k string) {
	Configuration, _ = sjson.Delete(Configuration, k)
}

func Del(k string) {
	delonly(k)

	fmt.Fprintf(db, "-%s\n", k)
}

func Get(k string) string {
	return gjson.Get(Configuration, k).Raw
}

func Clone(fk, tk string) {
	v := gjson.Get(Configuration, fk).Raw
	if len(v) > 0 {
		Configuration, _ = sjson.SetRaw(Configuration, tk, v)

		fmt.Fprintf(db, "+%s\n%s\n", tk, v)
	}
}

func Vacuum() {
	erase()

	fmt.Fprintf(db, "*\n%s\n", Configuration)
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
				Configuration, _ = sjson.SetRaw(Configuration, string(kl[1:]), string(vl))
			}
		case '*':
			if vl := readline(reader); vl == nil {
				break
			} else {
				Configuration = string(vl)
			}
		case '-':
			delonly(string(kl[1:]))
		}
	}
	log.Println("db loaded")
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
		db.Close()
		db = nil
	}
}

func readline(reader *bufio.Reader) []byte {
	if line, err := reader.ReadBytes('\n'); err != nil {
		return nil
	} else {
		return line[:len(line)-1]
	}
}

package models

import (
	"fmt"
	"github.com/goccy/go-json"
)

type Cmd uint8

const (
	SetCmd = Cmd(iota)
	DelCmd
	DumpCmd
)

type persistable struct {
	command Cmd
	key     string
	value   any
}

var (
	suspend  = make(chan bool)
	resume   = make(chan bool)
	persists = make(chan *persistable, 100)
)

func persist() {
	for {
		select {
		case <-suspend:
			resume <- true
		case row := <-persists:
			switch row.command {
			case SetCmd:
				pv, _ := json.Marshal(row.value)
				fmt.Fprintf(db, "+%v\n%s\n", row.key, pv)
			case DumpCmd:
				fmt.Fprintf(db, "*\n%s\n", Configuration)
			case DelCmd:
				fmt.Fprintf(db, "-%v\n", row.key)
			}
		}
	}
}

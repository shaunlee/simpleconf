package models

import (
	"fmt"
)

type Cmd uint8

const (
	SetCmd = Cmd(iota)
	DelCmd
)

type persistable struct {
	command Cmd
	key string
	value interface{}
}

var persists = make(chan *persistable, 100)

func persist() {
	for {
		row := <-persists

		switch row.command {
		case SetCmd:
			fmt.Fprintf(db, "+%v\n", row.key)
			pv, _ := json.Marshal(row.value)
			fmt.Fprintf(db, "%s\n", pv)
		case DelCmd:
			fmt.Fprintf(db, "-%v\n", row.key)
		}
	}
}

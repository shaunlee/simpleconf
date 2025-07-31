package helpers

import (
	"bufio"
	"github.com/goccy/go-json"
)

func Readline(reader *bufio.Reader) []byte {
	if line, err := reader.ReadBytes('\n'); err != nil {
		return nil
	} else {
		return line[:len(line)-1]
	}
}

func Bytes2Obj(s []byte) interface{} {
	var v interface{}
	if err := json.Unmarshal(s, &v); err != nil {
		return nil
	} else {
		return v
	}
}

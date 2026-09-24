package db

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func buildTreeDoc(keys int) *treeObj {
	root := newTreeObj()
	set := func(k, raw string) {
		n, _ := treeSet(root, splitTreePath(k), parseTreeRaw([]byte(raw)))
		root = n.(*treeObj)
	}
	set("first", `"mark"`)
	for i := 0; i < keys; i++ {
		set(fmt.Sprintf("svc%04d", i),
			`{"name":"`+strings.Repeat("x", 40)+`","port":8000,"tags":["a","b"]}`)
	}
	set("last", `"mark"`)
	return root
}

func TestFootprint(t *testing.T) {
	const reps = 30
	for _, n := range []struct {
		name string
		keys int
	}{{"600B", 6}, {"6KB", 70}, {"60KB", 750}} {
		docSize := len(appendTree(nil, buildTreeDoc(n.keys)))

		var m0, m1, s0, s1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		trees := make([]*treeObj, reps)
		for i := range trees {
			trees[i] = buildTreeDoc(n.keys)
		}
		runtime.GC()
		runtime.ReadMemStats(&m1)
		treeBytes := (m1.HeapAlloc - m0.HeapAlloc) / reps
		runtime.KeepAlive(trees)

		// the representation this replaced: one JSON string, plus the
		// snapshot readers were handed
		runtime.GC()
		runtime.ReadMemStats(&s0)
		strs := make([][2]string, reps)
		for i := range strs {
			raw := string(appendTree(nil, buildTreeDoc(n.keys)))
			strs[i] = [2]string{raw, string([]byte(raw))}
		}
		runtime.GC()
		runtime.ReadMemStats(&s1)
		strBytes := (s1.HeapAlloc - s0.HeapAlloc) / reps
		runtime.KeepAlive(strs)

		t.Logf("%-6s doc=%6dB | tree=%6dB (%.1fx doc) | string+snapshot=%6dB (%.1fx doc) | tree/string=%.1fx",
			n.name, docSize, treeBytes, float64(treeBytes)/float64(docSize),
			strBytes, float64(strBytes)/float64(docSize),
			float64(treeBytes)/float64(strBytes))
	}
}

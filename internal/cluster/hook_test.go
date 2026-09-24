package cluster

import (
	"reflect"
	"sync"
	"testing"

	"github.com/shaunlee/simpleconf/internal/db"
)

// recordWrites installs a hook that collects every reported write.
func recordWrites(t *testing.T) func() []Write {
	t.Helper()
	var (
		mu     sync.Mutex
		writes []Write
	)
	SetLocalWriteHook(func(w Write) {
		mu.Lock()
		writes = append(writes, w)
		mu.Unlock()
	})
	t.Cleanup(func() { SetLocalWriteHook(nil) })
	return func() []Write {
		mu.Lock()
		defer mu.Unlock()
		return append([]Write(nil), writes...)
	}
}

func TestLocalWriteHook(t *testing.T) {
	useDB(t)
	m, err := Start(Config{})
	if err != nil {
		t.Fatal(err)
	}
	useDefault(t, m)
	writes := recordWrites(t)

	body := []byte(" 9007199254740993 ")
	if err := ApplySetRaw("n", body); err != nil {
		t.Fatal(err)
	}
	copy(body, "xxxxxxxxxxxxxxxxxx") // the server reuses its request buffer
	if err := ApplySet("s", "v"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyClone("s", "t"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyDelete("n"); err != nil {
		t.Fatal(err)
	}
	if err := ApplyVacuum(); err != nil {
		t.Fatal(err)
	}
	// Failed writes are not reported.
	if err := ApplySetRaw("bad", []byte("{")); err == nil {
		t.Fatal("bad JSON should fail")
	}
	if err := ApplySetRaw("s.x", []byte("1")); err != nil {
		t.Fatal(err) // s is a string, so this replaces it with an object
	}
	if err := ApplySetRaw("arr", []byte("[1]")); err != nil {
		t.Fatal(err)
	}
	if err := ApplySetRaw("arr.x", []byte("1")); err == nil {
		t.Fatal("a non-numeric array index should fail")
	}

	want := []Write{
		{Op: "set", Key: "n", Raw: []byte("9007199254740993")},
		{Op: "set", Key: "s", Raw: []byte(`"v"`)},
		{Op: "clone", FromKey: "s", ToKey: "t"},
		{Op: "del", Key: "n"},
		{Op: "set", Key: "s.x", Raw: []byte("1")},
		{Op: "set", Key: "arr", Raw: []byte("[1]")},
	}
	if got := writes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("hook saw\n  %+v\nwant\n  %+v", got, want)
	}

	SetLocalWriteHook(nil)
	if err := ApplyDelete("t"); err != nil {
		t.Fatal(err)
	}
	if got := writes(); len(got) != len(want) {
		t.Fatalf("a removed hook was still called: %+v", got[len(want):])
	}
	if db.Get("t") != "" {
		t.Fatalf("delete without a hook did not apply: %s", db.Get(""))
	}
}

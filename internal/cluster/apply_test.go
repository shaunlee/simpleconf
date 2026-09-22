package cluster

import (
	"bytes"
	"testing"

	json "github.com/goccy/go-json"
	"github.com/hashicorp/raft"
	"github.com/shaunlee/simpleconf/internal/db"
)

func TestRaftReplayKeepsBigInteger(t *testing.T) {
	const n = "9007199254740993"
	db.Init(t.TempDir())
	defer db.Close()

	for _, raw := range []string{n, `{"n":` + n + `}`} {
		v, err := decodeJSON([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		key := "n"
		if raw[0] == '{' {
			key = "o"
		}
		b, err := json.Marshal(command{Op: "set", Key: key, Value: v})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(b, []byte(n)) {
			t.Fatalf("log lost integer text: %s", b)
		}
		if err, ok := (&fsm{}).Apply(&raft.Log{Data: b}).(error); ok {
			t.Fatal(err)
		}
		gotKey := key
		if key == "o" {
			gotKey = "o.n"
		}
		if got := db.Get(gotKey); got != n {
			t.Fatalf("replay %s = %q", gotKey, got)
		}
	}

	// A log line that already contains the digits must replay as those digits.
	body := []byte(`{"op":"set","key":"direct","value":9007199254740993}`)
	if err, ok := (&fsm{}).Apply(&raft.Log{Data: body}).(error); ok {
		t.Fatal(err)
	}
	if got := db.Get("direct"); got != n {
		t.Fatalf("direct replay = %q", got)
	}
}

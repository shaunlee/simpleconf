package db

import (
	"errors"
	"testing"

	"github.com/goccy/go-json"
)

// useAOF gives one test a fresh document and AOF directory.
func useAOF(t *testing.T) string {
	t.Helper()
	resetConfig()
	dir := t.TempDir()
	Init(dir)
	t.Cleanup(func() { Close() })
	return dir
}

func TestValueConversions(t *testing.T) {
	useAOF(t)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, "null"},
		{"string", "s", `"s"`},
		{"escaped string", `a"b`, `"a\"b"`},
		{"true", true, "true"},
		{"false", false, "false"},
		{"int", int(-1), "-1"},
		{"int8", int8(-8), "-8"},
		{"int16", int16(-16), "-16"},
		{"int32", int32(-32), "-32"},
		{"int64", int64(-64), "-64"},
		{"uint", uint(1), "1"},
		{"uint8", uint8(8), "8"},
		{"uint16", uint16(16), "16"},
		{"uint32", uint32(32), "32"},
		{"uint64", uint64(18446744073709551615), "18446744073709551615"},
		{"float32", float32(1.5), "1.5"},
		{"float64", 0.25, "0.25"},
		{"bytes", []byte("hi"), `"hi"`},
		{"map", map[string]any{"k": 1}, `{"k":1}`},
	}
	for _, c := range cases {
		raw, err := rawJSON(c.v)
		if err != nil || string(raw) != c.want {
			t.Fatalf("%s: rawJSON = %q, %v want %q", c.name, raw, err, c.want)
		}
		if err := Set("v", c.v); err != nil {
			t.Fatalf("%s: Set: %v", c.name, err)
		}
		if got := Get("v"); got != c.want {
			t.Fatalf("%s: stored %q want %q", c.name, got, c.want)
		}
	}
}

func TestValueConversionErrors(t *testing.T) {
	useAOF(t)
	if err := Set("n", json.Number("12")); err != nil || Get("n") != "12" {
		t.Fatalf("valid json.Number: err=%v stored %q", err, Get("n"))
	}
	var je *JSONError
	if err := Set("n", json.Number("abc")); !errors.As(err, &je) {
		t.Fatalf("invalid json.Number error = %v, want *JSONError", err)
	}
	if err := Set("c", make(chan int)); err == nil {
		t.Fatal("an unencodable value should fail")
	}
	if Get("n") != "12" || Get("c") != "" {
		t.Fatalf("failed writes changed the document: %s", Get(""))
	}
}

func TestSetRaw(t *testing.T) {
	useAOF(t)
	cases := []struct{ raw, want string }{
		{`false`, "false"},
		{`null`, "null"},
		{` "x" `, `"x"`},
		{`"a\"b"`, `"a\"b"`},
		{`"中"`, `"中"`},
		{`{"a":[1,2]}`, `{"a":[1,2]}`},
	}
	for _, c := range cases {
		if err := SetRaw("r", []byte(c.raw)); err != nil {
			t.Fatalf("SetRaw(%s): %v", c.raw, err)
		}
		if got := Get("r"); got != c.want {
			t.Fatalf("SetRaw(%s) stored %q want %q", c.raw, got, c.want)
		}
	}

	for _, raw := range []string{`{`, `"`, `1 2`} {
		err := SetRaw("bad", []byte(raw))
		var je *JSONError
		if !errors.As(err, &je) {
			t.Fatalf("SetRaw(%s) error = %v, want *JSONError", raw, err)
		}
		if je.Error() != errors.Unwrap(je).Error() {
			t.Fatalf("JSONError text %q differs from its cause %q", je.Error(), errors.Unwrap(je))
		}
	}
}

func TestReplace(t *testing.T) {
	useAOF(t)
	if err := Replace(`{"a":1,"b":{"c":true}}`); err != nil {
		t.Fatal(err)
	}
	if Get("") != `{"a":1,"b":{"c":true}}` || Get("b.c") != "true" {
		t.Fatalf("after Replace: %s", Get(""))
	}

	if err := Replace("{"); err == nil {
		t.Fatal("Replace with invalid JSON should fail")
	}
	if Get("a") != "1" {
		t.Fatalf("a failed Replace changed the document: %s", Get(""))
	}

	// A non-object replaces the tree with an empty object.
	if err := Replace("[1]"); err != nil {
		t.Fatal(err)
	}
	if err := Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if got, want := Get(""), `{"k":"v"}`; got != want {
		t.Fatalf("after Replace([1]) and Set: %s want %s", got, want)
	}
}

func TestCloneToInvalidPath(t *testing.T) {
	useAOF(t)
	for k, v := range map[string]any{"arr": []any{1}, "s": "x", "o": map[string]any{"a": 1}} {
		if err := Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	Clone("s", "arr.x")
	Clone("o", "arr.y")
	if got := Get("arr"); got != "[1]" {
		t.Fatalf("clone into an invalid path changed arr: %s", got)
	}
}

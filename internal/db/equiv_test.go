package db

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// refDoc is the representation this package used before the tree: one JSON
// byte slice mutated in place by sjson. It is kept here, unchanged, as the
// oracle the tree is checked against. Every step of the script below must
// produce byte-identical output from both.
type refDoc struct{ buf []byte }

var refOpts = &sjson.Options{Optimistic: true, ReplaceInPlace: true}

func newRefDoc() *refDoc { return &refDoc{buf: []byte("{}")} }

func (d *refDoc) whole() string { return string(d.buf) }

func (d *refDoc) get(k string) string {
	if k == "" {
		return d.whole()
	}
	return gjson.Get(d.whole(), k).Raw
}

func (d *refDoc) setRaw(k string, raw []byte) error {
	b, err := sjson.SetRawBytesOptions(d.buf, k, raw, refOpts)
	if err != nil {
		return err
	}
	d.buf = b
	return nil
}

func (d *refDoc) del(k string) { d.buf, _ = sjson.DeleteBytes(d.buf, k) }

func (d *refDoc) clone(fk, tk string) string {
	v := gjson.GetBytes(d.buf, fk).Raw
	if len(v) == 0 {
		return ""
	}
	d.buf, _ = sjson.SetRawBytesOptions(d.buf, tk, []byte(v), refOpts)
	return v
}

type equivOp struct{ kind, a, b string }

var equivScript = []equivOp{
	{"get", "", ""},
	{"set", "a", `"hello"`}, {"get", "a", ""},
	{"set", "a", `"q\"uote"`}, {"get", "a", ""},
	{"set", "u", `"中文字"`}, {"get", "u", ""},
	{"set", "h", `"a<b>c&d"`}, {"get", "h", ""},
	{"set", "ctl", `"a\tb\nc"`}, {"get", "ctl", ""},
	{"set", "n1", `0.1`}, {"set", "n2", `1e21`}, {"set", "n3", `-0`},
	{"set", "n4", `123456789012345678901234567890`}, {"set", "n5", `1.0`},
	{"get", "n2", ""}, {"get", "n4", ""}, {"get", "n5", ""},
	{"set", "b1", `true`}, {"set", "b2", `null`}, {"set", "b3", `""`},
	{"set", "o", `{"k":[1,{"z":null}],"e":{}}`}, {"get", "o", ""}, {"get", "o.k.1.z", ""},
	{"set", "deep.a.b.c.d", `"v"`}, {"get", "deep", ""},
	{"set", "deep.a.b.c.d", `"w"`}, {"get", "deep.a.b.c.d", ""},
	{"set", "arr", `[80,443,8080]`}, {"get", "arr", ""},
	{"set", "arr.1", `9999`}, {"get", "arr", ""}, {"get", "arr.1", ""},
	{"set", "arr.6", `7`}, {"get", "arr", ""},
	{"set", "arr.-1", `5`}, {"get", "arr", ""},
	{"seterr", "arr.x", `1`}, {"get", "arr", ""},
	{"del", "arr.0", ""}, {"get", "arr", ""},
	{"set", `m.x\.y`, `9`}, {"get", "m", ""}, {"get", `m.x\.y`, ""},
	{"set", "a.sub", `1`}, {"get", "a", ""},
	{"set", "arr.2.deep", `"x"`}, {"get", "arr", ""},
	{"clone", "o", "ocopy"}, {"get", "ocopy", ""},
	{"clone", "nosuch", "dst"}, {"get", "dst", ""},
	{"clone", "o", "p.q.r"}, {"get", "p", ""},
	{"del", "b1", ""}, {"get", "b1", ""},
	{"del", "nosuchkey", ""},
	{"del", "deep.a.b.c", ""}, {"get", "deep", ""},
	// plain-path edges
	{"set", "arr.-1.k", `1`}, {"get", "arr", ""},
	{"del", "arr.99", ""}, {"del", "arr.x", ""}, {"get", "arr", ""},
	{"del", "arr.1.deep", ""}, {"get", "arr", ""},
	{"del", "a.sub.z", ""}, {"get", "a", ""},
	{"get", "arr.99", ""}, {"get", "arr.x", ""}, {"get", "a.sub.z", ""},
	// escaped paths take the segment-slice code path
	{"set", `esc\.arr`, `[1,2]`}, {"get", `esc\.arr`, ""},
	{"set", `esc\.arr.-1`, `3`}, {"get", `esc\.arr`, ""},
	{"set", `esc\.arr.5`, `6`}, {"get", `esc\.arr`, ""},
	{"set", `esc\.arr.1`, `20`}, {"get", `esc\.arr`, ""},
	{"set", `esc\.arr.0.x`, `"y"`}, {"get", `esc\.arr`, ""},
	{"set", `esc\.arr.0.z`, `"w"`}, {"get", `esc\.arr`, ""},
	{"set", `esc\.arr.-1.k`, `true`}, {"get", `esc\.arr`, ""},
	{"seterr", `esc\.arr.x`, `1`}, {"get", `esc\.arr`, ""},
	{"get", `esc\.arr.0.x`, ""}, {"get", `esc\.arr.99`, ""}, {"get", `esc\.arr.x`, ""},
	{"set", `e\.1.a.b`, `1`}, {"set", `e\.1.a.c`, `2`}, {"get", `e\.1`, ""},
	{"set", `e\.1.a.b`, `10`}, {"get", `e\.1.a.b`, ""},
	{"get", `e\.1.nosuch`, ""}, {"get", `e\.1.a.b.c`, ""},
	{"set", `e\.1.a.c.z`, `3`}, {"get", `e\.1`, ""},
	{"del", `esc\.arr.0`, ""}, {"get", `esc\.arr`, ""},
	{"del", `esc\.arr.99`, ""}, {"del", `esc\.arr.x`, ""}, {"get", `esc\.arr`, ""},
	{"del", `esc\.arr.0.x`, ""}, {"get", `esc\.arr`, ""},
	{"del", `e\.1.a.b`, ""}, {"get", `e\.1`, ""},
	{"del", `e\.1.nosuch.x`, ""}, {"del", `e\.1.a.c.z.q`, ""}, {"get", `e\.1`, ""},
	{"del", `m.x\.y`, ""}, {"get", "m", ""},
	{"clone", `e\.1`, `f\.2`}, {"get", `f\.2`, ""},
	// gjson query syntax, which the tree delegates rather than reimplements
	{"set", "friends", `[{"first":"Dale","age":44},{"first":"Roger","age":68},{"first":"Jane","age":47}]`},
	{"get", "friends.#", ""},
	{"get", "friends.#.first", ""},
	{"get", `friends.#(first=="Dale")`, ""},
	{"get", `friends.#(first=="Dale").age`, ""},
	{"get", "friends.#(age>45)#", ""},
	{"get", "friends.#(age>45)#.first", ""},
	{"get", `friends.#(first%"?a*")#.first`, ""},
	{"get", "friends.#(age>100)#", ""},
	{"get", "friends.#.nosuch", ""},
	{"get", "fr*nds.0.first", ""},
	{"get", "n?me", ""},
	{"get", "", ""},
}

func TestTreeMatchesStringRepresentation(t *testing.T) {
	resetConfig()
	ref := newRefDoc()

	for i, op := range equivScript {
		var gotRef, gotTree string
		switch op.kind {
		case "get":
			gotRef, gotTree = ref.get(op.a), Get(op.a)
		case "set", "seterr":
			var v any
			if err := json.Unmarshal([]byte(op.b), &v); err != nil {
				t.Fatalf("step %d: bad literal: %v", i, err)
			}
			raw, err := rawJSON(v)
			if err != nil {
				t.Fatalf("step %d: rawJSON: %v", i, err)
			}
			refErr := ref.setRaw(op.a, raw)
			treeErr := setrawonly(op.a, raw)
			gotRef, gotTree = errText(refErr), errText(treeErr)
			if op.kind == "seterr" && refErr == nil {
				t.Fatalf("step %d: expected the reference to reject %q", i, op.a)
			}
		case "del":
			ref.del(op.a)
			delonly(op.a)
		case "clone":
			gotRef, gotTree = ref.clone(op.a, op.b), cloneonly(op.a, op.b)
		}
		if gotRef != gotTree {
			t.Fatalf("step %d (%s %s): result differs\n  string: %q\n  tree  : %q",
				i, op.kind, op.a, gotRef, gotTree)
		}
		if w1, w2 := ref.whole(), Get(""); w1 != w2 {
			t.Fatalf("step %d (%s %s): document differs\n  string: %s\n  tree  : %s",
				i, op.kind, op.a, w1, w2)
		}
	}
}

func errText(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

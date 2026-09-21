package db

// The document is held as a parsed tree rather than as one JSON string. A
// keyed read or write costs O(depth) instead of O(offset), so the cost of an
// operation no longer depends on where in the document the key happens to sit.
//
// Two properties of the gjson/sjson representation this replaces have to be
// preserved exactly, because they are visible through the API:
//
//   - Document order. gjson/sjson keep keys in insertion order, so objects
//     here are ordered, and a key's original JSON text is kept alongside it.
//   - Scalar text. gjson hands back the source bytes of a value, so leaves are
//     stored as raw JSON rather than parsed, and are copied out untouched.
//
// Query paths (`#`, `#(...)`, wildcards) are not reimplemented. They are
// delegated to gjson against the snapshot the tree already maintains; see
// Get in db.go.

import (
	"strconv"

	"github.com/tidwall/gjson"
)

// treeObj is an insertion-ordered object.
type treeObj struct {
	keys []string // unescaped, for lookup
	raws []string // the key's JSON text, including quotes
	vals []any
	idx  map[string]int
}

func newTreeObj() *treeObj { return &treeObj{idx: map[string]int{}} }

func (o *treeObj) get(k string) (any, bool) {
	i, ok := o.idx[k]
	if !ok {
		return nil, false
	}
	return o.vals[i], true
}

func (o *treeObj) set(k, raw string, v any) {
	if i, ok := o.idx[k]; ok {
		o.vals[i] = v
		return
	}
	o.idx[k] = len(o.keys)
	o.keys = append(o.keys, k)
	o.raws = append(o.raws, raw)
	o.vals = append(o.vals, v)
}

func (o *treeObj) del(k string) {
	i, ok := o.idx[k]
	if !ok {
		return
	}
	o.keys = append(o.keys[:i], o.keys[i+1:]...)
	o.raws = append(o.raws[:i], o.raws[i+1:]...)
	o.vals = append(o.vals[:i], o.vals[i+1:]...)
	delete(o.idx, k)
	for j := i; j < len(o.keys); j++ {
		o.idx[o.keys[j]] = j
	}
}

// treeLeaf is the raw JSON text of a scalar.
type treeLeaf []byte

// ---- serialization ----

func appendTree(b []byte, n any) []byte {
	switch v := n.(type) {
	case treeLeaf:
		return append(b, v...)
	case *treeObj:
		b = append(b, '{')
		for i := range v.keys {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, v.raws[i]...)
			b = append(b, ':')
			b = appendTree(b, v.vals[i])
		}
		return append(b, '}')
	case []any:
		b = append(b, '[')
		for i, e := range v {
			if i > 0 {
				b = append(b, ',')
			}
			if e == nil {
				b = append(b, "null"...)
			} else {
				b = appendTree(b, e)
			}
		}
		return append(b, ']')
	case nil:
		return append(b, "null"...)
	}
	return b
}

// ---- parsing ----

func parseTree(r gjson.Result) any {
	switch {
	case r.IsObject():
		o := newTreeObj()
		r.ForEach(func(k, v gjson.Result) bool {
			o.set(k.String(), k.Raw, parseTree(v))
			return true
		})
		return o
	case r.IsArray():
		var a []any
		r.ForEach(func(_, v gjson.Result) bool {
			a = append(a, parseTree(v))
			return true
		})
		return a
	default:
		return treeLeaf(r.Raw)
	}
}

func parseTreeRaw(raw []byte) any { return parseTree(gjson.ParseBytes(raw)) }

// ---- paths ----

// isPlainPath reports whether the path is a plain dot path, so it can take the
// tree walk. Anything else is gjson query syntax and goes to gjson.
func isPlainPath(p string) bool {
	for i := 0; i < len(p); i++ {
		switch p[i] {
		case '#', '*', '?':
			return false
		}
	}
	return true
}

// splitTreePath splits on unescaped dots, with `\` escaping the next
// character, matching sjson.
func splitTreePath(p string) []string {
	out := make([]string, 0, 4)
	cur := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		switch {
		case p[i] == '\\' && i+1 < len(p):
			cur = append(cur, p[i+1])
			i++
		case p[i] == '.':
			out = append(out, string(cur))
			cur = cur[:0]
		default:
			cur = append(cur, p[i])
		}
	}
	return append(out, string(cur))
}

// ---- read ----

func treeLookup(n any, segs []string) (any, bool) {
	for _, s := range segs {
		switch c := n.(type) {
		case *treeObj:
			v, ok := c.get(s)
			if !ok {
				return nil, false
			}
			n = v
		case []any:
			i, err := strconv.Atoi(s)
			if err != nil || i < 0 || i >= len(c) {
				return nil, false
			}
			n = c[i]
		default:
			return nil, false
		}
	}
	return n, true
}

// ---- write ----

type pathError struct{ msg string }

func (e *pathError) Error() string { return e.msg }

func treeSet(n any, segs []string, val any) (any, error) {
	s := segs[0]
	last := len(segs) == 1
	switch c := n.(type) {
	case *treeObj:
		if last {
			c.set(s, string(stringifyJSON(s)), val)
			return c, nil
		}
		child, ok := c.get(s)
		if !ok || !isTreeContainer(child) {
			child = newTreeObj()
		}
		nc, err := treeSet(child, segs[1:], val)
		if err != nil {
			return nil, err
		}
		c.set(s, string(stringifyJSON(s)), nc)
		return c, nil
	case []any:
		if s == "-1" {
			if last {
				return append(c, val), nil
			}
			nc, err := treeSet(newTreeObj(), segs[1:], val)
			if err != nil {
				return nil, err
			}
			return append(c, nc), nil
		}
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 {
			return nil, &pathError{"cannot set array element for non-numeric key '" + s + "'"}
		}
		for len(c) <= i {
			c = append(c, nil)
		}
		if last {
			c[i] = val
			return c, nil
		}
		child := c[i]
		if !isTreeContainer(child) {
			child = newTreeObj()
		}
		nc, err := treeSet(child, segs[1:], val)
		if err != nil {
			return nil, err
		}
		c[i] = nc
		return c, nil
	default:
		return treeSet(newTreeObj(), segs, val)
	}
}

func isTreeContainer(n any) bool {
	switch n.(type) {
	case *treeObj, []any:
		return true
	}
	return false
}

func treeDel(n any, segs []string) any {
	s := segs[0]
	last := len(segs) == 1
	switch c := n.(type) {
	case *treeObj:
		if last {
			c.del(s)
			return c
		}
		if child, ok := c.get(s); ok {
			c.set(s, string(stringifyJSON(s)), treeDel(child, segs[1:]))
		}
		return c
	case []any:
		i, err := strconv.Atoi(s)
		if err != nil || i < 0 || i >= len(c) {
			return c
		}
		if last {
			// sjson shifts the remaining elements down
			return append(c[:i], c[i+1:]...)
		}
		c[i] = treeDel(c[i], segs[1:])
		return c
	}
	return n
}

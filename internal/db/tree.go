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
	"strings"

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
	// Callers such as Fiber route params and gjson hand over strings that
	// alias a buffer the next request will reuse. A key already in the tree
	// has to own its bytes.
	k = strings.Clone(k)
	raw = strings.Clone(raw)
	o.idx[k] = len(o.keys)
	o.keys = append(o.keys, k)
	o.raws = append(o.raws, raw)
	o.vals = append(o.vals, v)
}

// put stores v at k. An existing key keeps its JSON text; quoting happens
// only when the key is first inserted.
func (o *treeObj) put(k string, v any) {
	if i, ok := o.idx[k]; ok {
		o.vals[i] = v
		return
	}
	o.set(k, string(stringifyJSON(k)), v)
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

// treeLeaf is the raw JSON text of a scalar. It is a string so a keyed read
// can hand the text back without copying it.
type treeLeaf string

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
		// Raw aliases the input buffer. A float is rewritten to the same
		// decimal text Set stores, so an old log line like 1e+21 reloads as
		// the value a live write would have kept. An integer is kept verbatim:
		// float64 cannot hold 9007199254740993. Anything else is copied
		// unchanged, including a string's own escape sequences.
		if c, ok := canonicalNumberText(r.Raw); ok {
			return treeLeaf(c)
		}
		return treeLeaf(strings.Clone(r.Raw))
	}
}

// canonicalNumberText accepts a JSON number. Integers are returned unchanged.
// Other numbers are rendered the way valueToNode renders a float64.
func canonicalNumberText(raw string) (string, bool) {
	if len(raw) == 0 || raw[0] == '"' {
		return "", false
	}
	i := 0
	if raw[0] == '-' {
		i++
		if i == len(raw) {
			return "", false
		}
	}
	if raw[i] < '0' || raw[i] > '9' {
		return "", false
	}
	if raw[i] == '0' {
		i++
	} else {
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
	}
	frac := false
	if i < len(raw) && raw[i] == '.' {
		frac = true
		i++
		start := i
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		if i == start {
			return "", false
		}
	}
	exp := false
	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		exp = true
		i++
		if i < len(raw) && (raw[i] == '+' || raw[i] == '-') {
			i++
		}
		start := i
		for i < len(raw) && raw[i] >= '0' && raw[i] <= '9' {
			i++
		}
		if i == start {
			return "", false
		}
	}
	if i != len(raw) {
		return "", false
	}
	if !frac && !exp {
		return strings.Clone(raw), true
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return "", false
	}
	return string(strconv.AppendFloat(nil, f, 'f', -1, 64)), true
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
	// Without escapes the segments can share the path's memory.
	if strings.IndexByte(p, '\\') < 0 {
		return strings.Split(p, ".")
	}
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
			c.put(s, val)
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
		c.put(s, nc)
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

// cutSeg splits one unescaped segment off path. The returned strings share
// path's backing array, so a lookup does not allocate.
func cutSeg(path string) (seg, rest string, last bool) {
	i := strings.IndexByte(path, '.')
	if i < 0 {
		return path, "", true
	}
	return path[:i], path[i+1:], false
}

func treeLookupPath(n any, path string) (any, bool) {
	if strings.IndexByte(path, '\\') >= 0 {
		return treeLookup(n, splitTreePath(path))
	}
	for {
		seg, rest, last := cutSeg(path)
		switch c := n.(type) {
		case *treeObj:
			v, ok := c.get(seg)
			if !ok {
				return nil, false
			}
			n = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(c) {
				return nil, false
			}
			n = c[i]
		default:
			return nil, false
		}
		if last {
			return n, true
		}
		path = rest
	}
}

func treeSetPath(n any, path string, val any) (any, error) {
	if strings.IndexByte(path, '\\') >= 0 {
		return treeSet(n, splitTreePath(path), val)
	}
	return treeSetPlain(n, path, val)
}

func treeSetPlain(n any, path string, val any) (any, error) {
	seg, rest, last := cutSeg(path)
	switch c := n.(type) {
	case *treeObj:
		if last {
			c.put(seg, val)
			return c, nil
		}
		child, ok := c.get(seg)
		if !ok || !isTreeContainer(child) {
			child = newTreeObj()
		}
		nc, err := treeSetPlain(child, rest, val)
		if err != nil {
			return nil, err
		}
		c.put(seg, nc)
		return c, nil
	case []any:
		if seg == "-1" {
			if last {
				return append(c, val), nil
			}
			nc, err := treeSetPlain(newTreeObj(), rest, val)
			if err != nil {
				return nil, err
			}
			return append(c, nc), nil
		}
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 {
			return nil, &pathError{"cannot set array element for non-numeric key '" + seg + "'"}
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
		nc, err := treeSetPlain(child, rest, val)
		if err != nil {
			return nil, err
		}
		c[i] = nc
		return c, nil
	default:
		return treeSetPlain(newTreeObj(), path, val)
	}
}

func treeDelPath(n any, path string) any {
	if strings.IndexByte(path, '\\') >= 0 {
		return treeDel(n, splitTreePath(path))
	}
	return treeDelPlain(n, path)
}

func treeDelPlain(n any, path string) any {
	seg, rest, last := cutSeg(path)
	switch c := n.(type) {
	case *treeObj:
		if last {
			c.del(seg)
			return c
		}
		if child, ok := c.get(seg); ok {
			c.put(seg, treeDelPlain(child, rest))
		}
		return c
	case []any:
		i, err := strconv.Atoi(seg)
		if err != nil || i < 0 || i >= len(c) {
			return c
		}
		if last {
			return append(c[:i], c[i+1:]...)
		}
		c[i] = treeDelPlain(c[i], rest)
		return c
	}
	return n
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
			c.put(s, treeDel(child, segs[1:]))
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

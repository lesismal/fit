package assistant

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// A tree is JSON decoded with its objects' members in the order they were
// written, so that a config a proposal changes keeps the order its person
// wrote it in: *jobject, []any, string, json.Number, bool or nil.
type jobject struct {
	keys []string
	vals map[string]any
}

func newObject() *jobject { return &jobject{vals: map[string]any{}} }

func (o *jobject) get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

// set sets member k, at the end when it is new.
func (o *jobject) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *jobject) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i:i], o.keys[i+1:]...)
			return
		}
	}
}

// maxTreeDepth bounds how deeply a tree nests.
const maxTreeDepth = 512

// decodeTree decodes data, one JSON value, into a tree.
func decodeTree(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, errNotJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errNotJSON
	}
	return v, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxTreeDepth {
		return nil, errNotJSON
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		o := newObject()
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			v, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			o.set(k.(string), v)
		}
		_, err = dec.Token()
		return o, err
	default: // '['
		arr := []any{}
		for dec.More() {
			v, err := decodeValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		_, err = dec.Token()
		return arr, err
	}
}

// encodeTree returns v as compact JSON, its members in their order.
func encodeTree(v any) []byte {
	var b bytes.Buffer
	writeTree(&b, v)
	return b.Bytes()
}

func writeTree(b *bytes.Buffer, v any) {
	switch t := v.(type) {
	case *jobject:
		b.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			writeTree(b, t.vals[k])
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, x := range t {
			if i > 0 {
				b.WriteByte(',')
			}
			writeTree(b, x)
		}
		b.WriteByte(']')
	case string:
		writeString(b, t)
	case json.Number:
		b.WriteString(t.String())
	case bool:
		b.WriteString(strconv.FormatBool(t))
	default:
		b.WriteString("null")
	}
}

// writeString writes s as a JSON string, without escaping <, > and &.
func writeString(b *bytes.Buffer, s string) {
	var sb bytes.Buffer
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	b.Write(bytes.TrimSuffix(sb.Bytes(), []byte("\n")))
}

// cloneTree returns a deep copy of v.
func cloneTree(v any) any {
	switch t := v.(type) {
	case *jobject:
		o := &jobject{keys: append([]string(nil), t.keys...), vals: make(map[string]any, len(t.vals))}
		for k, x := range t.vals {
			o.vals[k] = cloneTree(x)
		}
		return o
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = cloneTree(x)
		}
		return out
	}
	return v
}

// equalTree reports whether a and b are the same JSON value; members'
// order does not count, and numbers are compared as written.
func equalTree(a, b any) bool {
	switch x := a.(type) {
	case *jobject:
		y, ok := b.(*jobject)
		if !ok || len(x.vals) != len(y.vals) {
			return false
		}
		for k, v := range x.vals {
			w, ok := y.vals[k]
			if !ok || !equalTree(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalTree(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return a == b
}

// parsePointer splits a JSON pointer (RFC 6901), as /routes/0/handle, into
// its tokens; "" is the whole value.
func parsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if p[0] != '/' {
		return nil, fmt.Errorf("%q is not a JSON pointer: it starts with /", p)
	}
	parts := strings.Split(p[1:], "/")
	for i, s := range parts {
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// pointerOf joins tokens into a JSON pointer.
func pointerOf(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(t, "~", "~0"), "/", "~1"))
	}
	return b.String()
}

// index parses an array's index token for an array of n elements; end
// allows n itself, as an insertion at the end.
func index(tok string, n int, end bool) (int, error) {
	i, err := strconv.Atoi(tok)
	switch {
	case err != nil || i < 0 || tok != strconv.Itoa(i):
		return 0, fmt.Errorf("%q is not an array's index", tok)
	case i > n || i == n && !end:
		return 0, fmt.Errorf("index %d is past the array's %d elements", i, n)
	}
	return i, nil
}

// lookupTree returns the value at tokens in root.
func lookupTree(root any, tokens []string) (any, bool) {
	cur := root
	for _, t := range tokens {
		switch c := cur.(type) {
		case *jobject:
			v, ok := c.get(t)
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := index(t, len(c), false)
			if err != nil {
				return nil, false
			}
			cur = c[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// The operations of a patch: add sets a member or inserts an element (at
// "-", at the end), replace sets what is there, remove removes it.
const (
	opAdd     = "add"
	opReplace = "replace"
	opRemove  = "remove"
)

// patchTree applies op at tokens, not empty, to root, and returns root.
// With create, the objects and the array an add's path goes through are
// made when the config leaves them out, as a reconcile's fix expects.
func patchTree(root any, op string, tokens []string, v any, create bool) (any, error) {
	if len(tokens) == 0 {
		return nil, fmt.Errorf("%s needs a path below the config's top", op)
	}
	parent, ok := lookupTree(root, tokens[:len(tokens)-1])
	if !ok && create && op == opAdd {
		var err error
		if parent, err = makePath(root, tokens[:len(tokens)-1], tokens[len(tokens)-1] == "-"); err != nil {
			return nil, err
		}
		ok = true
	}
	if !ok {
		return nil, fmt.Errorf("the config has nothing at %s", pointerOf(tokens[:len(tokens)-1]))
	}
	last := tokens[len(tokens)-1]
	switch p := parent.(type) {
	case *jobject:
		_, has := p.get(last)
		switch {
		case op == opAdd:
			p.set(last, v)
		case !has:
			return nil, fmt.Errorf("the config has nothing at %s to %s", pointerOf(tokens), op)
		case op == opReplace:
			p.vals[last] = v
		default:
			p.del(last)
		}
		return root, nil
	case []any:
		var arr []any
		switch {
		case op == opAdd && last == "-":
			arr = append(p, v)
		case op == opAdd:
			i, err := index(last, len(p), true)
			if err != nil {
				return nil, err
			}
			arr = append(append(append([]any(nil), p[:i]...), v), p[i:]...)
		default:
			i, err := index(last, len(p), false)
			if err != nil {
				return nil, err
			}
			arr = append([]any(nil), p...)
			if op == opReplace {
				arr[i] = v
			} else {
				arr = append(arr[:i], arr[i+1:]...)
			}
		}
		return setTree(root, tokens[:len(tokens)-1], arr), nil
	}
	return nil, fmt.Errorf("%s is neither an object nor an array", pointerOf(tokens[:len(tokens)-1]))
}

// setTree puts v at tokens, which lead to a value root has, and returns root.
func setTree(root any, tokens []string, v any) any {
	if len(tokens) == 0 {
		return v
	}
	parent, _ := lookupTree(root, tokens[:len(tokens)-1])
	last := tokens[len(tokens)-1]
	switch p := parent.(type) {
	case *jobject:
		p.vals[last] = v
	case []any:
		i, _ := strconv.Atoi(last)
		p[i] = v
	}
	return root
}

// makePath makes the objects along tokens in root that are missing, the
// last an array when array, and returns the value at tokens.
func makePath(root any, tokens []string, array bool) (any, error) {
	cur := root
	for i, t := range tokens {
		o, ok := cur.(*jobject)
		if !ok {
			return nil, fmt.Errorf("%s is not an object", pointerOf(tokens[:i]))
		}
		next, has := o.get(t)
		if !has {
			if i == len(tokens)-1 && array {
				next = []any{}
			} else {
				next = newObject()
			}
			o.set(t, next)
		}
		cur = next
	}
	return cur, nil
}

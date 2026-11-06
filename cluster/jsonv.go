package cluster

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// jv is a JSON value that keeps its objects' members in the order they were
// written, so that a config the manager changes reads as the member wrote
// it, but for what changed.
type jv struct {
	kind  byte // 'o' object, 'a' array, 's' string, 'r' another scalar
	keys  []string
	vals  []*jv
	items []*jv
	str   string
	raw   json.RawMessage
}

// maxDepth bounds how deeply a value nests.
const maxDepth = 512

var errDepth = errors.New("the JSON nests too deeply")

// parseJV reads data, one JSON value.
func parseJV(data []byte) (*jv, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := readJV(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("more than one JSON value")
	}
	return v, nil
}

func readJV(dec *json.Decoder, depth int) (*jv, error) {
	if depth > maxDepth {
		return nil, errDepth
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			v := &jv{kind: 'o'}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string) // a member's name is a string
				val, err := readJV(dec, depth+1)
				if err != nil {
					return nil, err
				}
				v.set(k, val)
			}
			_, err := dec.Token()
			return v, err
		case '[':
			v := &jv{kind: 'a', items: []*jv{}}
			for dec.More() {
				it, err := readJV(dec, depth+1)
				if err != nil {
					return nil, err
				}
				v.items = append(v.items, it)
			}
			_, err := dec.Token()
			return v, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	case string:
		return &jv{kind: 's', str: t}, nil
	case json.Number:
		return &jv{kind: 'r', raw: json.RawMessage(t.String())}, nil
	case bool:
		if t {
			return &jv{kind: 'r', raw: json.RawMessage("true")}, nil
		}
		return &jv{kind: 'r', raw: json.RawMessage("false")}, nil
	default: // nil
		return &jv{kind: 'r', raw: json.RawMessage("null")}, nil
	}
}

// get returns the object's member k, or nil.
func (v *jv) get(k string) *jv {
	if v == nil || v.kind != 'o' {
		return nil
	}
	for i, key := range v.keys {
		if key == k {
			return v.vals[i]
		}
	}
	return nil
}

// set sets the object's member k, in its place when it has one, last when
// not.
func (v *jv) set(k string, val *jv) {
	for i, key := range v.keys {
		if key == k {
			v.vals[i] = val
			return
		}
	}
	v.keys = append(v.keys, k)
	v.vals = append(v.vals, val)
}

// name returns an object's "name" when it is a string, or "".
func (v *jv) name() string {
	if n := v.get("name"); n != nil && n.kind == 's' {
		return n.str
	}
	return ""
}

// clone returns a copy of v that shares nothing with it.
func (v *jv) clone() *jv {
	if v == nil {
		return nil
	}
	c := &jv{kind: v.kind, str: v.str, raw: v.raw}
	if v.kind == 'o' {
		c.keys = append([]string{}, v.keys...)
		c.vals = make([]*jv, len(v.vals))
		for i, x := range v.vals {
			c.vals[i] = x.clone()
		}
	}
	if v.kind == 'a' {
		c.items = make([]*jv, len(v.items))
		for i, x := range v.items {
			c.items[i] = x.clone()
		}
	}
	return c
}

// encode writes v compactly, its members in their order.
func (v *jv) encode(b *bytes.Buffer) {
	switch v.kind {
	case 'o':
		b.WriteByte('{')
		for i, k := range v.keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k)
			b.WriteByte(':')
			v.vals[i].encode(b)
		}
		b.WriteByte('}')
	case 'a':
		b.WriteByte('[')
		for i, it := range v.items {
			if i > 0 {
				b.WriteByte(',')
			}
			it.encode(b)
		}
		b.WriteByte(']')
	case 's':
		writeString(b, v.str)
	default:
		b.Write(v.raw)
	}
}

func writeString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string
	b.Truncate(b.Len() - 1)
}

// bytes returns v as compact JSON.
func (v *jv) bytes() []byte {
	var b bytes.Buffer
	v.encode(&b)
	return b.Bytes()
}

// pretty returns v as JSON indented by two spaces, with a newline.
func (v *jv) pretty() []byte {
	var b bytes.Buffer
	_ = json.Indent(&b, v.bytes(), "", "  ") // valid JSON
	b.WriteByte('\n')
	return b.Bytes()
}

// walkStrings calls fn on every string of v, with the names of the object
// members it is under, from the root; fn returns the string to put in its
// place, or an error that ends the walk.
func (v *jv) walkStrings(path []string, fn func(path []string, s string) (string, error)) error {
	switch v.kind {
	case 'o':
		for i, k := range v.keys {
			if err := v.vals[i].walkStrings(append(path, k), fn); err != nil {
				return err
			}
		}
	case 'a':
		for _, it := range v.items {
			if err := it.walkStrings(path, fn); err != nil {
				return err
			}
		}
	case 's':
		s, err := fn(path, v.str)
		if err != nil {
			return err
		}
		v.str = s
	}
	return nil
}

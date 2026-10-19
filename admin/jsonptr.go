package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// A JSON document is edited as a tree of these: nil, bool, json.Number,
// string, []any and *object. Objects keep their members in the order
// written, and numbers their text, so that a config edited in one place is
// written back unchanged everywhere else, but for its layout.
type object struct {
	keys []string
	vals map[string]any
}

func (o *object) get(k string) (any, bool) {
	v, ok := o.vals[k]
	return v, ok
}

func (o *object) set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *object) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, key := range o.keys {
		if key == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// RequestError is an edit the API refuses, with the status it answers.
type RequestError struct {
	Status int
	Msg    string
}

func (e *RequestError) Error() string { return e.Msg }

func badRequest(format string, args ...any) error {
	return &RequestError{Status: 400, Msg: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &RequestError{Status: 404, Msg: fmt.Sprintf(format, args...)}
}

// decodeJSON parses data, which must hold one JSON value, into a tree.
func decodeJSON(data []byte) (any, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, err := decodeValue(d)
	if err != nil {
		return nil, badRequest("the body is not JSON: %v", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, badRequest("the body holds more than one JSON value")
	}
	return v, nil
}

func decodeValue(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := t.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := &object{vals: map[string]any{}}
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				if _, dup := o.vals[k]; dup {
					return nil, fmt.Errorf("the key %q is given twice", k)
				}
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				o.set(k, v)
			}
			_, err := d.Token()
			return o, err
		case '[':
			a := []any{}
			for d.More() {
				v, err := decodeValue(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token()
			return a, err
		}
		return nil, fmt.Errorf("unexpected %v", t)
	default:
		return t, nil
	}
}

// encodeJSON writes v indented by two spaces, ending in a newline.
func encodeJSON(v any) []byte {
	var b bytes.Buffer
	writeValue(&b, v, "")
	b.WriteByte('\n')
	return b.Bytes()
}

func writeValue(b *bytes.Buffer, v any, indent string) {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case json.Number:
		b.WriteString(v.String())
	case string:
		writeString(b, v)
	case []any:
		if len(v) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, e := range v {
			b.WriteString(indent + "  ")
			writeValue(b, e, indent+"  ")
			if i < len(v)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "]")
	case *object:
		if len(v.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, k := range v.keys {
			b.WriteString(indent + "  ")
			writeString(b, k)
			b.WriteString(": ")
			writeValue(b, v.vals[k], indent+"  ")
			if i < len(v.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(indent + "}")
	}
}

func writeString(b *bytes.Buffer, s string) {
	e := json.NewEncoder(b)
	e.SetEscapeHTML(false)
	_ = e.Encode(s)
	b.Truncate(b.Len() - 1) // Encode's newline
}

// parsePointer splits a JSON pointer (RFC 6901), "" or starting with "/",
// into its reference tokens.
func parsePointer(p string) ([]string, error) {
	if p == "" {
		return nil, nil
	}
	if !strings.HasPrefix(p, "/") {
		return nil, badRequest("%q is not a JSON pointer: it must start with '/'", p)
	}
	tokens := strings.Split(p[1:], "/")
	for i, t := range tokens {
		for j := 0; j < len(t); j++ {
			if t[j] == '~' && (j+1 == len(t) || (t[j+1] != '0' && t[j+1] != '1')) {
				return nil, badRequest("%q is not a JSON pointer: '~' must be followed by 0 or 1", p)
			}
		}
		tokens[i] = unescapeToken.Replace(t)
	}
	return tokens, nil
}

var (
	unescapeToken = strings.NewReplacer("~1", "/", "~0", "~")
	escapeToken   = strings.NewReplacer("~", "~0", "/", "~1")
)

// pointerString writes tokens back as a JSON pointer, for messages.
func pointerString(tokens []string) string {
	var b strings.Builder
	for _, t := range tokens {
		b.WriteByte('/')
		b.WriteString(escapeToken.Replace(t))
	}
	return b.String()
}

// index reads an array index token: decimal, without leading zeros.
func index(t string, n int, tokens []string) (int, error) {
	i, err := strconv.Atoi(t)
	if err != nil || i < 0 || (len(t) > 1 && t[0] == '0') || t[0] == '+' {
		return 0, notFound("%s: %q is not an array index", pointerString(tokens), t)
	}
	if i >= n {
		return 0, notFound("%s: index %d is past the end of an array of %d", pointerString(tokens), i, n)
	}
	return i, nil
}

// lookup returns the value tokens point to in root.
func lookup(root any, tokens []string) (any, error) {
	v := root
	for i, t := range tokens {
		switch c := v.(type) {
		case *object:
			var ok bool
			if v, ok = c.get(t); !ok {
				return nil, notFound("%s: there is no member %q", pointerString(tokens[:i+1]), t)
			}
		case []any:
			j, err := index(t, len(c), tokens[:i+1])
			if err != nil {
				return nil, err
			}
			v = c[j]
		default:
			return nil, notFound("%s: %s is neither an object nor an array", pointerString(tokens[:i+1]), pointerString(tokens[:i]))
		}
	}
	return v, nil
}

// put sets the value tokens point to: it replaces a member or an element,
// adds a member to an object, or, for the token "-", appends to an array.
// It returns the new root.
func put(root any, tokens []string, v any) (any, error) {
	if len(tokens) == 0 {
		return v, nil
	}
	parent, err := lookup(root, tokens[:len(tokens)-1])
	if err != nil {
		return nil, err
	}
	last := tokens[len(tokens)-1]
	switch p := parent.(type) {
	case *object:
		p.set(last, v)
	case []any:
		if last == "-" {
			p = append(p, v)
			return put(root, tokens[:len(tokens)-1], p)
		}
		i, err := index(last, len(p), tokens)
		if err != nil {
			return nil, err
		}
		p[i] = v
	default:
		return nil, notFound("%s: %s is neither an object nor an array", pointerString(tokens), pointerString(tokens[:len(tokens)-1]))
	}
	return root, nil
}

// remove deletes the member or element tokens point to, and returns the new
// root.
func remove(root any, tokens []string) (any, error) {
	if len(tokens) == 0 {
		return nil, badRequest("the config itself cannot be deleted")
	}
	parent, err := lookup(root, tokens[:len(tokens)-1])
	if err != nil {
		return nil, err
	}
	last := tokens[len(tokens)-1]
	switch p := parent.(type) {
	case *object:
		if _, ok := p.get(last); !ok {
			return nil, notFound("%s: there is no member %q", pointerString(tokens), last)
		}
		p.del(last)
	case []any:
		i, err := index(last, len(p), tokens)
		if err != nil {
			return nil, err
		}
		return put(root, tokens[:len(tokens)-1], append(p[:i:i], p[i+1:]...))
	default:
		return nil, notFound("%s: %s is neither an object nor an array", pointerString(tokens), pointerString(tokens[:len(tokens)-1]))
	}
	return root, nil
}

// mergePatch applies patch to target as RFC 7396's JSON merge patch does:
// an object's members are merged in, a null removes one, and anything else
// replaces the target whole.
func mergePatch(target, patch any) any {
	p, ok := patch.(*object)
	if !ok {
		return patch
	}
	t, ok := target.(*object)
	if !ok {
		t = &object{vals: map[string]any{}}
	}
	for _, k := range p.keys {
		v := p.vals[k]
		if v == nil {
			t.del(k)
			continue
		}
		old, _ := t.get(k)
		t.set(k, mergePatch(old, v))
	}
	return t
}

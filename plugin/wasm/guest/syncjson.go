//go:build wasip1

package guest

import (
	"encoding/json"
	"errors"
	"strings"
)

// A batch is read and records are written by hand rather than by
// encoding/json, whose reflection is slow in WebAssembly: a record's
// values stay the JSON they were, sliced out of the input.

var errSyntax = errors.New("a batch that is not an array of records")

// parseBatch reads a JSON array of records.
func parseBatch(b []byte) ([]Record, error) {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '[' {
		return nil, errSyntax
	}
	var recs []Record
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == ']' {
		return recs, nil
	}
	for {
		end, err := skipValue(b, i)
		if err != nil {
			return nil, err
		}
		var r Record
		if err := r.parse(b[i:end]); err != nil {
			return nil, err
		}
		recs = append(recs, r)
		i = skipSpace(b, end)
		if i >= len(b) {
			return nil, errSyntax
		}
		if b[i] == ']' {
			return recs, nil
		}
		if b[i] != ',' {
			return nil, errSyntax
		}
		i = skipSpace(b, i+1)
	}
}

// parse reads r from an object of its parts.
func (r *Record) parse(b []byte) error {
	return members(b, func(name string, val []byte) error {
		v := json.RawMessage(val)
		switch name {
		case "source":
			r.Source = unquote(v)
		case "namespace":
			r.Namespace = unquote(v)
		case "op":
			r.Op = unquote(v)
		case "commit_time":
			r.CommitTime = unquote(v)
		case "key":
			r.Key = v
		case "before":
			r.Before = v
		case "after":
			r.After = v
		case "position":
			r.Position = v
		case "tx":
			r.Tx = new(Tx)
			return json.Unmarshal(v, r.Tx)
		}
		return nil
	})
}

// members calls fn with each member of the JSON object b, its value a
// slice of b.
func members(b []byte, fn func(name string, v []byte) error) error {
	i := skipSpace(b, 0)
	if i >= len(b) || b[i] != '{' {
		return errSyntax
	}
	i = skipSpace(b, i+1)
	if i < len(b) && b[i] == '}' {
		return nil
	}
	for {
		kend, err := skipValue(b, i)
		if err != nil || b[i] != '"' {
			return errSyntax
		}
		name := unquote(b[i:kend])
		i = skipSpace(b, kend)
		if i >= len(b) || b[i] != ':' {
			return errSyntax
		}
		i = skipSpace(b, i+1)
		end, err := skipValue(b, i)
		if err != nil {
			return err
		}
		if err := fn(name, b[i:end]); err != nil {
			return err
		}
		i = skipSpace(b, end)
		if i >= len(b) {
			return errSyntax
		}
		if b[i] == '}' {
			return nil
		}
		if b[i] != ',' {
			return errSyntax
		}
		i = skipSpace(b, i+1)
	}
}

// appendJSON appends r's canonical JSON.
func (r *Record) appendJSON(b []byte) []byte {
	b = append(b, '{')
	field := func(name string) {
		if b[len(b)-1] != '{' {
			b = append(b, ',')
		}
		b = append(append(append(b, '"'), name...), `":`...)
	}
	for _, s := range []struct{ name, v string }{{"source", r.Source}, {"namespace", r.Namespace}, {"op", r.Op}} {
		if s.v != "" {
			field(s.name)
			b = appendQuote(b, s.v)
		}
	}
	for _, v := range []struct {
		name string
		v    json.RawMessage
	}{{"key", r.Key}, {"before", r.Before}, {"after", r.After}} {
		if len(v.v) > 0 && string(v.v) != "null" {
			field(v.name)
			b = append(b, v.v...)
		}
	}
	if r.CommitTime != "" {
		field("commit_time")
		b = appendQuote(b, r.CommitTime)
	}
	if r.Tx != nil && *r.Tx != (Tx{}) {
		field("tx")
		t, _ := json.Marshal(r.Tx)
		b = append(b, t...)
	}
	if len(r.Position) > 0 {
		field("position")
		b = append(b, r.Position...)
	}
	return append(b, '}')
}

func skipSpace(b []byte, i int) int {
	for i < len(b) && (b[i] == ' ' || b[i] == '\t' || b[i] == '\n' || b[i] == '\r') {
		i++
	}
	return i
}

// skipValue returns where the JSON value at b[i] ends.
func skipValue(b []byte, i int) (int, error) {
	if i >= len(b) {
		return 0, errSyntax
	}
	switch b[i] {
	case '"':
		for i++; i < len(b); i++ {
			switch b[i] {
			case '\\':
				i++
			case '"':
				return i + 1, nil
			}
		}
		return 0, errSyntax
	case '{', '[':
		depth := 0
		for ; i < len(b); i++ {
			switch b[i] {
			case '{', '[':
				depth++
			case '}', ']':
				if depth--; depth == 0 {
					return i + 1, nil
				}
			case '"':
				end, err := skipValue(b, i)
				if err != nil {
					return 0, err
				}
				i = end - 1
			}
		}
		return 0, errSyntax
	}
	for i < len(b) && !strings.ContainsRune(" \t\r\n,]}:", rune(b[i])) {
		i++
	}
	return i, nil
}

// unquote returns the string a JSON string holds.
func unquote(b []byte) string {
	if len(b) >= 2 && b[0] == '"' && !strings.ContainsRune(string(b), '\\') {
		return string(b[1 : len(b)-1])
	}
	var s string
	_ = json.Unmarshal(b, &s)
	return s
}

// appendQuote appends s as a JSON string.
func appendQuote(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\':
			b = append(b, '\\', c)
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&15])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}

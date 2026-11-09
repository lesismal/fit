// Package vecutil is what sync's vector database sinks (qdrant, milvus)
// share: a batch's records cut to the last operation on each ID, and the
// key's text.
package vecutil

import (
	"encoding/base64"
	"strings"

	fitsync "github.com/lesismal/fit/sync"
)

// Last returns the indices of the last of each ID among ids, in order: the
// state a batch leaves each ID in is its last record's, and the IDs left
// are each written once, so that a request holds no ID twice and the
// order of different IDs' writes does not matter.
func Last(ids []string) []int {
	last := make(map[string]int, len(ids))
	for i, id := range ids {
		last[id] = i
	}
	out := make([]int, 0, len(last))
	for i, id := range ids {
		if last[id] == i {
			out = append(out, i)
		}
	}
	return out
}

// KeyText returns a key's text: a single value's text, or a map's values
// in order, each with '\' and ':' escaped by a '\', joined by ':', as the
// elasticsearch sink makes an _id. Bytes are in base64, arrays and maps
// plain JSON. A null key has none.
func KeyText(key fitsync.Value) (string, bool) {
	switch key.Kind() {
	case fitsync.KindNull:
		return "", false
	case fitsync.KindMap:
		fs := key.Fields()
		if len(fs) == 1 {
			return text(fs[0].Value), true
		}
		var b strings.Builder
		for i, f := range fs {
			if i > 0 {
				b.WriteByte(':')
			}
			b.WriteString(strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(text(f.Value)))
		}
		return b.String(), true
	}
	return text(key), true
}

// Single returns a key's one value: the value itself, or a map's only
// field's; false for a null key or a map of several fields.
func Single(key fitsync.Value) (fitsync.Value, bool) {
	switch key.Kind() {
	case fitsync.KindNull:
		return key, false
	case fitsync.KindMap:
		if fs := key.Fields(); len(fs) == 1 {
			return fs[0].Value, true
		}
		return key, false
	}
	return key, true
}

func text(v fitsync.Value) string {
	switch v.Kind() {
	case fitsync.KindBytes:
		return base64.StdEncoding.EncodeToString([]byte(v.Text()))
	case fitsync.KindArray, fitsync.KindMap:
		if b, err := fitsync.AppendPlain(nil, v); err == nil {
			return string(b)
		}
	}
	return fitsync.TextOf(v)
}

// RowOf returns the row a record writes: after, or before when after is
// null.
func RowOf(r *fitsync.Record) fitsync.Value {
	if r.After.IsNull() {
		return r.Before
	}
	return r.After
}

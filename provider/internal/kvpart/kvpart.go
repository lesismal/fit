// Package kvpart makes a part from the keys of a key-value store, as the
// Consul KV and etcd providers read them: each key under a prefix holds a
// part of its own, a JSON object of listeners, routes, upstreams and
// services, and the part provided is theirs together, in the order of
// their keys.
package kvpart

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/lesismal/fit/provider"
)

// An Entry is a key and its value.
type Entry struct {
	Key   string
	Value []byte
}

// Part joins the parts entries hold, in the order of their keys, and says
// why it left out the entries it did: those whose value is not a part, and
// those past the first max with a value. Keys ending in '/', which name
// folders, and empty values are passed over. The part is the same for the
// same entries, so that an unchanged one is seen to be.
func Part(entries []Entry, max int) (part []byte, problems []string) {
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	var kinds [4][]json.RawMessage // by the index of their kind in provider.Kinds
	taken := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Key, "/") || len(bytes.TrimSpace(e.Value)) == 0 {
			continue
		}
		if taken == max {
			problems = append(problems, fmt.Sprintf("key %s: left out: max_keys, %d, are taken", e.Key, max))
			continue
		}
		if _, err := provider.Parse("", e.Value); err != nil {
			problems = append(problems, fmt.Sprintf("key %s: %v", e.Key, strings.ReplaceAll(err.Error(), "\n", "; ")))
			continue
		}
		var m map[string][]json.RawMessage
		if err := json.Unmarshal(e.Value, &m); err != nil { // Parse took it, so this cannot fail
			problems = append(problems, fmt.Sprintf("key %s: %v", e.Key, err))
			continue
		}
		for i, kind := range provider.Kinds {
			for _, v := range m[kind] {
				var b bytes.Buffer
				if json.Compact(&b, v) == nil {
					v = b.Bytes()
				}
				kinds[i] = append(kinds[i], v)
			}
		}
		taken++
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kind := range provider.Kinds {
		if len(kinds[i]) == 0 {
			continue
		}
		if b.Len() > 1 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:[", kind)
		for j, v := range kinds[i] {
			if j > 0 {
				b.WriteByte(',')
			}
			b.Write(v)
		}
		b.WriteByte(']')
	}
	b.WriteByte('}')
	return b.Bytes(), problems
}

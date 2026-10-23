package yaml

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzAdapt checks that no YAML makes the adapter panic, that what it
// makes is JSON, and that the JSON written back as YAML adapts to the same.
func FuzzAdapt(f *testing.F) {
	f.Add("listeners:\n  - name: web\n    address: \":8080\"\n")
	f.Add("a: &a [1, 2]\nb: *a\nc: {<<: {x: 1}, y: 2}\n")
	f.Add("x: !!binary aGk=\ny: 0x1F\nz: .nan\n")
	f.Fuzz(func(t *testing.T, src string) {
		r, err := Adapt([]byte(src))
		if err != nil {
			return
		}
		if !json.Valid(r.JSON) {
			t.Fatalf("not JSON: %s", r.JSON)
		}
		y, err := FromJSON(r.JSON)
		if err != nil {
			t.Fatalf("%v: %s", err, r.JSON)
		}
		again, err := Adapt(y)
		if err != nil {
			t.Fatalf("%v:\n%s", err, y)
		}
		var a, b any
		da, db := json.NewDecoder(bytes.NewReader(r.JSON)), json.NewDecoder(bytes.NewReader(again.JSON))
		da.UseNumber()
		db.UseNumber()
		_, _ = da.Decode(&a), db.Decode(&b)
		if ja, _ := json.Marshal(a); !bytes.Equal(ja, func() []byte { jb, _ := json.Marshal(b); return jb }()) {
			t.Fatalf("the round trip changed it:\n%s\n%s", r.JSON, again.JSON)
		}
	})
}

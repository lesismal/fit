package fitconf

import (
	"bytes"
	"encoding/json"
	"testing"
)

// FuzzAdapt checks that no .fit file makes the adapter panic, that what it
// makes is JSON, and that the JSON, when it can be written back as a .fit
// file, adapts to the same.
func FuzzAdapt(f *testing.F) {
	f.Add("listener web {\n\taddress :8080\n}\nroute { handler respond { body \"hi\\n\" } }\n")
	f.Add("site a.example b.example {\n\tmiddleware compress\n\troute /x/* { handler proxy { upstream app } }\n}\n")
	f.Add("route {\n\tmatch { header { X-A 1 2 }; query { q } }\n\thandler respond\n}\nadmin {}\n")
	f.Add("x `raw {$A}` \"q \\{$B} {$C:d}\"; y {}\n# comment\n")
	f.Fuzz(func(t *testing.T, src string) {
		r, err := Adapt([]byte(src))
		if err != nil {
			return
		}
		if !json.Valid(r.JSON) {
			t.Fatalf("not JSON: %s", r.JSON)
		}
		out, err := FromJSON(r.JSON)
		if err != nil {
			return // a field of no known type that a .fit file cannot hold as it is
		}
		again, err := Adapt(out)
		if err != nil {
			t.Fatalf("%v:\n%s", err, out)
		}
		var a, b any
		da, db := json.NewDecoder(bytes.NewReader(r.JSON)), json.NewDecoder(bytes.NewReader(again.JSON))
		da.UseNumber()
		db.UseNumber()
		_, _ = da.Decode(&a), db.Decode(&b)
		ja, _ := json.Marshal(a)
		jb, _ := json.Marshal(b)
		if !bytes.Equal(ja, jb) {
			t.Fatalf("the round trip changed it:\n%s\n%s\n%s", r.JSON, out, again.JSON)
		}
	})
}

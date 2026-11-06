package managed

import (
	"strings"
	"testing"
)

func TestRecordCheck(t *testing.T) {
	ok := Record{Manager: "m", Paths: []Path{{Member: "routes", Name: "a", Hash: strings.Repeat("a", 64)}, {Member: "log"}}}
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", MaxNameBytes+1)
	for name, r := range map[string]Record{
		"manager":   {Manager: long},
		"host":      {ManagerHost: long},
		"too many":  {Paths: make([]Path, MaxPaths+1)},
		"member":    {Paths: []Path{{Member: "Bad"}}},
		"admin":     {Paths: []Path{{Member: "admin"}}},
		"no name":   {Paths: []Path{{Member: "routes"}}},
		"a name":    {Paths: []Path{{Member: "log", Name: "x"}}},
		"long name": {Paths: []Path{{Member: "routes", Name: long}}},
		"template":  {Paths: []Path{{Member: "log", Template: long}}},
		"hash":      {Paths: []Path{{Member: "log", Hash: "xyz"}}},
		"twice":     {Paths: []Path{{Member: "log"}, {Member: "log"}}},
	} {
		if err := r.Check(); err == nil {
			t.Errorf("%s: taken", name)
		}
	}
	a := Record{Manager: "m", Paths: []Path{{Member: "routes", Name: "b"}, {Member: "log"}, {Member: "routes", Name: "a"}}}
	a.Normalize()
	if a.Paths[0].String() != "log" || a.Paths[1].String() != "routes[a]" || !a.Paths[1].Same(Path{Member: "routes", Name: "a", Hash: "z"}) {
		t.Errorf("%+v", a.Paths)
	}
	b := a
	b.Paths = append([]Path{}, a.Paths...)
	if !a.Equal(&b) || a.ETag() != b.ETag() {
		t.Error("equal")
	}
	b.Paths[0].Hash = "x"
	if a.Equal(&b) || a.ETag() == b.ETag() {
		t.Error("not equal")
	}
	var none Record
	none.Normalize()
	if none.Paths == nil || !IsList("routes") || IsList("log") {
		t.Error("normalize")
	}
}

func TestHashLookup(t *testing.T) {
	if Hash([]byte(`{"b":1, "a":[1.0,"<x>"]}`)) != Hash([]byte(`{"a":[1.0,"<x>"],"b":1}`)) || Hash([]byte(`{`)) != "" || Hash([]byte(`1 2`)) != "" {
		t.Error("hash")
	}
	if Hash([]byte(`1.0`)) == Hash([]byte(`1`)) {
		t.Error("numbers are kept as written")
	}
	cfg := []byte(`{"log":{"level":"info"},"routes":[{"name":"a","x":1},{"x":2},{"name":3}]}`)
	if v, ok, err := Lookup(cfg, "routes", "a"); !ok || err != nil || string(v) != `{"name":"a","x":1}` {
		t.Errorf("%s %v %v", v, ok, err)
	}
	if v, ok, _ := Lookup(cfg, "log", ""); !ok || string(v) != `{"level":"info"}` {
		t.Errorf("%s", v)
	}
	if _, ok, err := Lookup(cfg, "routes", "b"); ok || err != nil {
		t.Error("b")
	}
	if _, ok, err := Lookup(cfg, "upstreams", "a"); ok || err != nil {
		t.Error("upstreams")
	}
	if _, _, err := Lookup([]byte(`[`), "log", ""); err == nil {
		t.Error("not an object")
	}
	if _, _, err := Lookup(cfg, "log", "x"); err == nil {
		t.Error("not a list")
	}
	if NameOf([]byte(`[`)) != "" {
		t.Error("name")
	}
	if Display("routes", "a") != "routes[a]" || Display("log", "") != "log" {
		t.Error("display")
	}
}

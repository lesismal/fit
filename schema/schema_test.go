package schema

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	_ "github.com/lesismal/fit/modules/standard"
)

var update = flag.Bool("update", false, "rewrite fit.schema.json")

const file = "fit.schema.json"

// TestSchemaUpToDate fails when a config type, a struct tag or a standard
// module changed without fit.schema.json being regenerated.
func TestSchemaUpToDate(t *testing.T) {
	got, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; run: go test ./schema -update", file)
	}
}

func TestSchemaShape(t *testing.T) {
	out, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Required []string `json:"required"`
		Defs     map[string]struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
			AllOf      []json.RawMessage          `json:"allOf"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(out, &s); err != nil {
		t.Fatal(err)
	}
	// listeners may be left out, for HTTPS by default to make them.
	if len(s.Required) != 0 {
		t.Fatalf("root required = %v", s.Required)
	}
	listener := s.Defs["config.Listener"]
	if _, ok := listener.Properties["At"]; ok || len(listener.Properties) != 6 {
		t.Fatalf("listener properties = %v", listener.Properties)
	}
	if string(listener.Properties["protocols"]) == "" {
		t.Fatal("listener has no protocols")
	}
	handlers := s.Defs["http.handlers"]
	if len(handlers.AllOf) == 0 || string(handlers.Properties["module"]) == "" {
		t.Fatalf("http.handlers = %+v", handlers)
	}
	if _, ok := s.Defs["http.handlers.respond"]; !ok {
		t.Fatal("respond's params are not defined")
	}
	if _, ok := s.Defs["http.middleware"]; !ok {
		t.Fatal("the middleware ref is not defined")
	}
}

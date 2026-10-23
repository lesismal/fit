package schema

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
)

const referenceFile = "../docs/reference/config.md"

// TestReferenceUpToDate fails when a config type, a struct tag or a
// standard module changed without docs/reference/config.md being
// regenerated, and when a field or a module has no description.
func TestReferenceUpToDate(t *testing.T) {
	got, err := Reference()
	if err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.WriteFile(referenceFile, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(referenceFile)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is out of date; run: go test ./schema -update", referenceFile)
	}
}

type undocumented struct {
	Named int `json:"named" doc:"Has one."`
	Bare  int `json:"bare"`
}

// TestReferenceNeedsDescriptions checks that a field without a doc tag is
// reported, and that the reference shows renamed fields and links nested
// objects.
func TestReferenceNeedsDescriptions(t *testing.T) {
	r := &reference{anchors: map[reflect.Type]*section{}}
	r.walk(reflect.TypeFor[undocumented](), "", "x")
	if len(r.missing) != 1 || r.missing[0] != "schema.undocumented.bare" {
		t.Fatalf("missing = %v", r.missing)
	}

	out, err := Reference()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"| `schema_version` | integer, 1 |",
		"| `max_size` |  |  | Deprecated: renamed `max_bytes`",
		"| `tls` | [object](#config-tls) |",
		"<a id=\"config-tls\"></a>\n\n### `listeners[].tls`\n",
		"### `cache`\n",
		"### `disk` in `cache`\n",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the reference lacks %q", want)
		}
	}
}

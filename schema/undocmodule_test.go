package schema

import (
	"slices"
	"strings"
	"testing"

	"github.com/lesismal/fit/module"
)

// bareModule is a module of a namespace of the tests' own, registered
// without a Doc.
type bareModule struct {
	Size int `json:"size" doc:"Has one."`
}

func init() {
	module.Register(module.Info{ID: "schematest.things.bare", New: func() any { return new(bareModule) }})
}

// TestReferenceUndocumentedModule checks a module without a Doc fails the
// reference.
func TestReferenceUndocumentedModule(t *testing.T) {
	saved := namespaces
	defer func() { namespaces = saved }()
	namespaces = append(slices.Clone(saved), struct{ ns, title, kind, file string }{"schematest.things", "Things", "thing", "handlers.md"})
	if _, err := Reference(); err == nil || !strings.Contains(err.Error(), "module schematest.things.bare") {
		t.Errorf("%v", err)
	}
}

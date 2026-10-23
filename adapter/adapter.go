// Package adapter turns configs written in other formats into fit's native
// JSON: YAML, and subsets of nginx's configs and Caddy's Caddyfile. An
// adapter registers itself under a name and the file extensions (or whole
// file names) it takes, and fit run, fit validate and fit adapt pick one by
// a file's extension or name, or by its own name.
package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/lesismal/fit/config"
)

// Result is a config adapted to native JSON.
type Result struct {
	// JSON is the config in fit's native format.
	JSON []byte
	// Warnings say what the adapter could not carry over, or carried over
	// with doubts.
	Warnings []string
	// Position returns where in the source the value at a config path
	// (as config errors name them, listeners[0].address) was written, or
	// false when it does not know.
	Position func(path string) (line, col int, ok bool)
}

// Adapter adapts a config in its format.
type Adapter func(data []byte) (*Result, error)

type entry struct {
	adapt Adapter
	exts  []string
}

var (
	mu       sync.RWMutex
	adapters = map[string]entry{}
)

// Register makes an adapter known by name, for files with exts, as
// ".yaml", or with a whole name, as "Caddyfile", which has no dot. It panics if name is taken, a mistake in the adapter's code.
func Register(name string, a Adapter, exts ...string) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := adapters[name]; ok {
		panic("adapter: " + name + " is registered twice")
	}
	adapters[name] = entry{a, exts}
}

// Lookup returns the adapter registered as name.
func Lookup(name string) (Adapter, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := adapters[name]
	return e.adapt, ok
}

// Names returns the adapters registered, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(adapters))
	for n := range adapters {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// ForPath returns the name of the adapter a file at path takes by its
// extension or its name, or "" for native JSON, as a .json file or one no
// adapter claims.
func ForPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)
	mu.RLock()
	defer mu.RUnlock()
	for name, e := range adapters {
		if ext != "" && slices.Contains(e.exts, ext) {
			return name
		}
		if slices.ContainsFunc(e.exts, func(x string) bool { return !strings.HasPrefix(x, ".") && strings.EqualFold(x, base) }) {
			return name
		}
	}
	return ""
}

// Adapt adapts data with the adapter named name, or, for "" or "json",
// returns it as standard JSON, its comments and trailing commas blanked out
// (config.Standardize), every position kept.
func Adapt(name string, data []byte) (*Result, error) {
	if name == "" || name == "json" {
		std, err := config.Standardize(data)
		if err != nil {
			return nil, config.Errors{err.(*config.Error)}
		}
		return &Result{JSON: std}, nil
	}
	a, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("there is no adapter %q (there are %s)", name, strings.Join(Names(), ", "))
	}
	return a(data)
}

// ReadFile reads the config at path and adapts it by its extension. The
// Result's JSON is what config.Load takes.
func ReadFile(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Adapt(ForPath(path), data)
}

// MapErrors returns err with the positions of its config errors, which are
// in r's JSON, moved to where the source wrote what they are about: the
// nearest value on their path the adapter knows the place of, or none.
func (r *Result) MapErrors(err error) error {
	if r == nil || r.Position == nil || err == nil {
		return err
	}
	errs := config.AsErrors(err)
	if len(errs) == 0 {
		return err
	}
	out := make(config.Errors, len(errs))
	for i, e := range errs {
		c := *e
		c.Line, c.Col = 0, 0
		for p := c.Path; ; p = parent(p) {
			if line, col, ok := r.Position(p); ok {
				c.Line, c.Col = line, col
				break
			}
			if p == "" {
				break
			}
		}
		out[i] = &c
	}
	return out
}

// parent returns the path of what holds the value at path: a.b[2] for
// a.b[2].c, a.b for a.b[2], a for a["k"], "" for a.
func parent(path string) string {
	switch {
	case strings.HasSuffix(path, `"]`):
		if i := strings.LastIndex(path, `["`); i >= 0 {
			return path[:i]
		}
	case strings.HasSuffix(path, "]"):
		if i := strings.LastIndexByte(path, '['); i >= 0 {
			return path[:i]
		}
	}
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[:i]
	}
	return ""
}

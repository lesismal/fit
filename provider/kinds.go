package provider

import (
	"fmt"
	"slices"
	"sync"

	"github.com/lesismal/fit/config"
)

// A Kind makes a provider of one kind, as docker, from pc, its config,
// with name, the name it goes by, and logf to log through; and says what it
// watches, for the log.
type Kind func(name string, pc *config.Provider, logf func(format string, args ...any)) (p Provider, watching string)

var (
	kindsMu sync.RWMutex
	kinds   = map[string]Kind{}
)

// RegisterKind makes kind, as config.Provider.Kind names it, known. A
// provider's package registers its kind in an init function, and a binary
// has the providers whose packages it imports: fit's own imports
// provider/standard, which imports them all. It panics if kind is
// registered twice, a mistake in the provider's code.
func RegisterKind(kind string, k Kind) {
	kindsMu.Lock()
	defer kindsMu.Unlock()
	if _, ok := kinds[kind]; ok {
		panic(fmt.Sprintf("provider: kind %s is registered twice", kind))
	}
	kinds[kind] = k
}

// LookupKind returns kind's maker, if this binary has it.
func LookupKind(kind string) (Kind, bool) {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	k, ok := kinds[kind]
	return k, ok
}

// RegisteredKinds returns the provider kinds this binary has, sorted.
func RegisteredKinds() []string {
	kindsMu.RLock()
	defer kindsMu.RUnlock()
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

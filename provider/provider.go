// Package provider is how config reaches fit from sources other than its
// config file and the Admin API: Docker, Kubernetes, Consul and the like.
//
// A provider watches its source and sends what it finds there as a part: a
// JSON object with listeners, routes, upstreams and services, and nothing
// else. Parts are merged into the base config, the one the file and the
// Admin API write, by rules written down in docs/decisions/009-provider-merging.md:
//
//   - The base config holds everything else (access_log, limits, admin and
//     the rest), which no provider may set.
//   - A part's elements are added after the base config's, provider by
//     provider in their order of precedence, each part's in its own order.
//   - A named element (a listener, upstream or service, or a route with a
//     name) whose name the base config, or a provider before it, already
//     uses is left out, and recorded as shadowed: the base config wins over
//     every provider, and a provider over those after it.
//   - The merged config is checked as a whole, as any other config is, and
//     applied as a whole; a part that would leave it invalid is refused, and
//     the provider's part before it stays.
//
// Where each element of the merged config came from is recorded, for the
// Admin API's GET /config/effective. Providers run in the control plane, on
// goroutines of their own; nothing here runs on a fib worker.
package provider

import (
	"context"
	"sync"
)

// A Provider watches a source of config.
type Provider interface {
	// Name names the provider in the merged config's sources. It is
	// unique among the providers of a server.
	Name() string
	// Run watches the source until ctx ends, and calls update with the
	// whole of what it provides each time that changes: a part, as
	// Parse reads it. update keeps data, which the provider must not
	// change afterwards, never blocks, and may be called from any one
	// goroutine at a time.
	Run(ctx context.Context, update func(data []byte))
}

// Run runs providers, each on a goroutine of its own, until ctx ends, and
// passes their parts to apply, one at a time and on a goroutine of Run's.
// Only a provider's latest part waits to be applied: one sent while an
// earlier one waits replaces it, so a provider that changes faster than
// configs are applied skips what is already out of date, and what waits is
// bounded by one part per provider. report, which may be nil, hears what
// apply gives back. Run returns once every provider and apply have
// returned.
func Run(ctx context.Context, providers []Provider, apply func(name string, data []byte) error, report func(name string, err error)) {
	if report == nil {
		report = func(string, error) {}
	}
	var (
		mu      sync.Mutex
		pending = make(map[string][]byte, len(providers))
		order   []string // the providers with a part pending, in the order they sent it
	)
	wake := make(chan struct{}, 1)
	var wg sync.WaitGroup
	for _, p := range providers {
		name := p.Name()
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Run(ctx, func(data []byte) {
				mu.Lock()
				if _, ok := pending[name]; !ok {
					order = append(order, name)
				}
				pending[name] = data
				mu.Unlock()
				select {
				case wake <- struct{}{}:
				default: // a wake-up is pending already
				}
			})
		}()
	}
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-wake:
		}
		for {
			mu.Lock()
			if len(order) == 0 {
				mu.Unlock()
				break
			}
			name := order[0]
			order = order[1:]
			data := pending[name]
			delete(pending, name)
			mu.Unlock()
			if ctx.Err() != nil {
				break
			}
			if err := apply(name, data); err != nil {
				report(name, err)
			}
		}
	}
}

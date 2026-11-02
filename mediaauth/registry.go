package mediaauth

import (
	"context"
	"errors"
	"fmt"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// ID is the role's, and its member's path in the config.
const ID = "media_auth"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if len(c.MediaAuth) == 0 {
				return nil
			}
			return c.MediaAuth
		},
		New: func(h *app.Host) (app.Role, error) { return &role{key: h.Upstream}, nil },
	})
}

// role moves the policies of its engine to each config applied.
type role struct{ key *fib.Engine }

func (r *role) Apply(member any) error {
	list, ok := member.([]config.MediaAuth)
	if !ok {
		return fmt.Errorf("media_auth: a member of type %T", member)
	}
	return Apply(r.key, list)
}

func (r *role) Stop(context.Context) { Remove(r.key) }

// Lookup returns the policy named name of the modules and roles of engine,
// or nil when the running config has none of that name. The policy
// returned changes in place with each config applied.
func Lookup(engine *fib.Engine, name string) *Policy {
	regMu.Lock()
	defer regMu.Unlock()
	if p := registry[engine][name]; p != nil && p.state() != nil {
		return p
	}
	return nil
}

// Apply moves engine's policies to list: each is changed in place, or made,
// and those list leaves out are removed, so that whoever still holds one is
// refused with ErrUnavailable. An error, a policy that does not compile,
// changes nothing.
func Apply(engine *fib.Engine, list []config.MediaAuth) error {
	regMu.Lock()
	defer regMu.Unlock()
	states := make(map[string]*state, len(list))
	pols := make(map[string]*Policy, len(list))
	var errs []error
	for i := range list {
		cfg := &list[i]
		if pols[cfg.Name] != nil {
			errs = append(errs, fmt.Errorf("%q is listed twice", cfg.Name))
			continue
		}
		p := registry[engine][cfg.Name]
		var old *state
		if p == nil {
			p = newPolicy(cfg.Name)
		} else {
			old = p.st.Load()
		}
		st, err := p.compile(cfg, old)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", cfg.Name, err))
			continue
		}
		states[cfg.Name], pols[cfg.Name] = st, p
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	for name, p := range registry[engine] {
		if pols[name] == nil {
			p.st.Store(&state{removed: true})
		}
	}
	for name, p := range pols {
		p.st.Store(states[name])
		p.applied = true
	}
	if len(pols) == 0 {
		delete(registry, engine)
	} else {
		registry[engine] = pols
	}
	return nil
}

// Remove removes every policy of engine.
func Remove(engine *fib.Engine) {
	regMu.Lock()
	defer regMu.Unlock()
	for _, p := range registry[engine] {
		p.st.Store(&state{removed: true})
	}
	delete(registry, engine)
}

// Provision returns the policy a module's param field names, as auth: nil
// for an empty name, which is no policy, and an error for a name the
// module's config has no media_auth policy of. A policy new to the engine
// is made from the config being compiled, so that the module can serve
// before the role media_auth applies it; one the role has applied is left
// to the role, which applies the config once it has been compiled.
func Provision(ctx *module.Context, field, name string) (*Policy, error) {
	if name == "" {
		return nil, nil
	}
	cfg := ctx.MediaAuth(name)
	if cfg == nil {
		return nil, ctx.Errorf(field, "there is no media_auth policy named %q", name)
	}
	regMu.Lock()
	defer regMu.Unlock()
	p := get(ctx.Engine(), name)
	st, err := p.compile(cfg, p.st.Load())
	if err != nil {
		return nil, ctx.Errorf(field, "media_auth %q: %v", name, err)
	}
	if !p.applied || p.state() == nil {
		p.st.Store(st)
	}
	return p, nil
}

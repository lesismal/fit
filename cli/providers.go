package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
)

// kindList lists the provider kinds this fit has, for an error.
func kindList() string {
	kinds := provider.RegisteredKinds()
	if len(kinds) == 0 {
		return "none"
	}
	return strings.Join(kinds, ", ")
}

// startProviders runs the providers c lists, in their order of precedence,
// applying what they provide through configs, until the function it
// returns is called; that waits for them to stop. A part refused is
// reported once, until the provider's part applies again.
func startProviders(c *config.Config, configs *admin.Configs, stdout, stderr io.Writer) (stop func(), err error) {
	var ps []provider.Provider
	for i := range c.Providers {
		pc := &c.Providers[i]
		name := pc.ID()
		logf := func(format string, args ...any) { fmt.Fprintf(stderr, "fit: "+format+"\n", args...) }
		kind := pc.Kind()
		if kind == "" {
			return nil, fmt.Errorf("provider %s: no kind is set", name)
		}
		newProvider, ok := provider.LookupKind(kind)
		if !ok {
			return nil, fmt.Errorf("provider %s: this fit was built without the %s provider (it has %s)", name, kind, kindList())
		}
		p, watching := newProvider(name, pc, logf)
		ps = append(ps, p)
		fmt.Fprintf(stdout, "fit: provider %s %s\n", name, watching)
		if err := configs.AddProvider(name); err != nil {
			return nil, err
		}
	}
	if len(ps) == 0 {
		return func() {}, nil
	}
	refused := map[string]string{} // the error last reported for each provider; used by Run's one goroutine
	apply := func(name string, data []byte) error {
		before := configs.Current().Number
		r, err := configs.Provide(name, data)
		if err != nil {
			return err
		}
		delete(refused, name)
		if r.Version != before {
			printApplied(stdout, stderr, r)
			fmt.Fprintf(stdout, "fit: applied what provider %s provides (config version %d)\n", name, r.Version)
		}
		return nil
	}
	report := func(name string, err error) {
		if refused[name] == err.Error() {
			return
		}
		refused[name] = err.Error()
		fmt.Fprintf(stderr, "fit: what provider %s provides was refused; the running config stays\n", name)
		printConfigError(stderr, "fit run", "provider "+name, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		provider.Run(ctx, ps, apply, report)
	}()
	return func() {
		cancel()
		<-done
	}, nil
}

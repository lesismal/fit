// Package roleconsole runs a role's console, its web pages behind logins,
// moved from config to config as the role's member changes: started anew
// when its settings change, its access set by every config applied. The
// tunnel's roles and the BitTorrent client share it.
package roleconsole

import (
	"context"
	"reflect"
	"strings"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/app/workbench"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// Console is a role's console, moved from config to config.
type Console struct {
	host   *app.Host
	engine *fib.Engine
	cookie string
	log    *logs.Logger
	routes func(*console.API)
	module *console.Module

	cfg *config.RoleConsole
	c   *console.Console
	// gate is the client addresses the console serves, which every config
	// applied sets, whether or not the console starts anew.
	gate access.Gate
}

// New returns the console of the role id, as tunnel.hub, on h's
// engine, logging to h's log, not started: Apply starts it. routes adds the
// role's API, to its console and to its module, which any console may show.
func New(h *app.Host, id string, routes func(*console.API)) *Console {
	return &Console{host: h, engine: h.Engine, cookie: "fit_" + strings.ReplaceAll(id, ".", "_"), log: h.Log, routes: routes,
		module: &console.Module{ID: id, Routes: routes}}
}

// Module returns the role's module.
func (c *Console) Module() *console.Module { return c.module }

// options are the console's options for cfg.
func (c *Console) options(cfg *config.RoleConsole) console.Options {
	return console.Options{Config: cfg, Cookie: c.cookie, Pages: workbench.Pages(), Log: c.log, Routes: c.routes,
		Modules: func() []*console.Module { return []*console.Module{c.module} }, Access: &c.gate}
}

// Apply moves the console to cfg, which may be nil for none: it is started
// anew when cfg differs from what it runs, but for its access, and left as
// it is otherwise. A console that does not start leaves the one before
// running, and returns why. Its access, cfg's then the global one, is set
// every time, before the console starts; one that does not compile leaves
// the policy before in force, and is returned.
func (c *Console) Apply(cfg *config.RoleConsole) error {
	if cfg != nil {
		p, err := c.host.Access(cfg.Access)
		if err != nil {
			return err
		}
		c.gate.Set(p)
	}
	if sameConsole(c.cfg, cfg) {
		c.cfg = cfg
		return nil
	}
	old := c.c
	if old != nil {
		// The new one may listen where the old one does.
		old.Stop(context.Background())
		c.c = nil
	}
	if cfg == nil {
		c.cfg = nil
		return nil
	}
	n, err := console.Start(c.engine, c.options(cfg))
	if err != nil {
		if c.cfg != nil {
			c.c, _ = console.Start(c.engine, c.options(c.cfg))
		}
		return err
	}
	c.c, c.cfg = n, cfg
	if c.log != nil {
		c.log.Infof("console on %s", n.URL())
	}
	return nil
}

// sameConsole reports whether a and b are the same console's config.
func sameConsole(a, b *config.RoleConsole) bool {
	if a == nil || b == nil {
		return a == b
	}
	x, y := *a, *b
	x.At, y.At = config.Value{}, config.Value{}
	x.Users, y.Users = nil, nil
	// The access is set on the running console's gate.
	x.Access, y.Access = nil, nil
	if len(a.Users) != len(b.Users) {
		return false
	}
	for i := range a.Users {
		if a.Users[i].Username != b.Users[i].Username || a.Users[i].PasswordHash != b.Users[i].PasswordHash {
			return false
		}
	}
	return reflect.DeepEqual(x, y)
}

// Addr returns where the console listens, or nil without one.
func (c *Console) Addr() string {
	if c.c == nil || c.c.Addr() == nil {
		return ""
	}
	return c.c.Addr().String()
}

// URL returns the console's address as a URL, or "".
func (c *Console) URL() string {
	if c.c == nil {
		return ""
	}
	return c.c.URL()
}

// Stop stops the console.
func (c *Console) Stop(ctx context.Context) {
	if c.c != nil {
		c.c.Stop(ctx)
		c.c = nil
	}
}

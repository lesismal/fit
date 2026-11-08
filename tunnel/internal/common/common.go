// Package common holds what the tunnel's three roles share around their
// console pages and their config: the role's module of the workbench, the
// starting and moving of a role's console, the write-back of what a page
// changes into the role's member of the config, and the relay of two
// connections with backpressure both ways.
package common

import (
	"context"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"reflect"
	"strings"
	"time"

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

// NewConsole returns the console of the role id, as tunnel.hub, on h's
// engine, logging to h's log, not started: Apply starts it. routes adds the
// role's API, to its console and to its module, which any console may show.
func NewConsole(h *app.Host, id string, routes func(*console.API)) *Console {
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

// Member is a role's member of the config, as JSON, for an edit to change:
// its members by name, as they are written, so that what an edit leaves
// alone stays as it was.
type Member map[string]json.RawMessage

// Get decodes the member named name into v, leaving v as it is when the
// member is left out.
func (m Member) Get(name string, v any) error {
	raw, ok := m[name]
	if !ok {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// Set sets the member named name to v, or leaves it out when v is its type's
// zero value.
func (m Member) Set(name string, v any) error {
	if rv := reflect.ValueOf(v); !rv.IsValid() || rv.IsZero() || rv.Kind() == reflect.Slice && rv.Len() == 0 || rv.Kind() == reflect.Map && rv.Len() == 0 {
		delete(m, name)
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	m[name] = data
	return nil
}

// EditJSON runs change on cur, a role's member as JSON, or nil when the
// config leaves it out, and returns the member changed.
func EditJSON(cur []byte, change func(Member) error) ([]byte, error) {
	m := Member{}
	if len(cur) > 0 && string(cur) != "null" {
		if err := json.Unmarshal(cur, &m); err != nil {
			return nil, err
		}
	}
	if err := change(m); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

// ErrBadRequest marks an edit refused for what the page asked, answered 400.
var ErrBadRequest = errors.New("bad request")

// BadRequest is an error of what the page asked.
func BadRequest(msg string) error { return &badRequest{msg} }

type badRequest struct{ msg string }

func (e *badRequest) Error() string        { return e.msg }
func (e *badRequest) Is(target error) bool { return target == ErrBadRequest }

// Edit changes the role's member of the config with change, which is given
// it as a Member, and answers r once the config with the change has been
// applied, the role's Apply among it: with then's answer, or {"ok": true,
// "warnings": [...]} when then is nil. The edit runs off the worker, on the
// app's manager; r is held until it answers. A change that fails, or a
// config that does not load, is answered 400 with why.
func Edit(h *app.Host, r *console.Request, change func(m Member) error, then func(warnings []string)) {
	r.Context.Retain()
	h.Edit(func(cur []byte) ([]byte, error) { return EditJSON(cur, change) }, func(warnings []string, err error) {
		defer r.Context.Release()
		switch {
		case errors.Is(err, app.ErrBusy), errors.Is(err, app.ErrStopped):
			_ = r.JSON(stdhttp.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		case err != nil:
			_ = r.JSON(stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		case then != nil:
			then(warnings)
		default:
			if warnings == nil {
				warnings = []string{}
			}
			_ = r.JSON(stdhttp.StatusOK, map[string]any{"ok": true, "warnings": warnings})
		}
	})
}

// SweepEvery is how often tunnels are looked at for idle_timeout idle: a
// quarter of it, between a second and a minute, so that one is closed at
// most a quarter late.
func SweepEvery(idle time.Duration) time.Duration {
	return min(max(idle/4, time.Second), time.Minute)
}

// OK answers r with {"ok": true}.
func OK(r *console.Request) { _ = r.JSON(stdhttp.StatusOK, map[string]bool{"ok": true}) }

// Fail answers r with status and {"error": msg}.
func Fail(r *console.Request, status int, msg string) {
	_ = r.JSON(status, map[string]string{"error": msg})
}

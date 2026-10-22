// Package hello is a module built into fit with fit build, as a template for
// modules of your own: a middleware, hello, that adds a field to every
// response of its routes.
//
//	fit build -with github.com/lesismal/fit/examples/module/hello=./examples/module/hello
//
// and in a route's middleware:
//
//	{"module": "hello", "params": {"greeting": "world"}}
package hello

import (
	stdhttp "net/http"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

// The module registers itself when its package is imported, under the
// namespace of what it is, http.middleware here, and the name configs know
// it by.
func init() {
	module.Register(module.Info{ID: module.Middlewares + ".hello", New: func() any { return new(Hello) }})
}

// Hello adds X-Hello to every response. Its exported fields are its params,
// decoded from the config as strictly as the config itself, with their
// docs and defaults in the schema.
type Hello struct {
	Greeting string `json:"greeting,omitempty" default:"\"world\"" doc:"What X-Hello says; world when left out."`

	value []string
}

// Provision checks the params and builds what serving takes, once, when
// the config is loaded: requests only read it.
func (h *Hello) Provision(ctx *module.Context) error {
	if h.Greeting == "" {
		h.Greeting = "world"
	}
	if len(h.Greeting) > 256 {
		return ctx.Errorf("greeting", "is longer than 256 bytes")
	}
	h.value = []string{h.Greeting}
	return nil
}

// Wrap returns next behind the middleware. It runs on a fib worker for
// every request, so it must not block: no I/O, no waiting, no locks held
// for long.
func (h *Hello) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.OnHeader(func(_ int, header stdhttp.Header) {
			header["X-Hello"] = h.value
		})
		next.ServeHTTP(c)
	})
}

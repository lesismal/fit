// Package config defines fit's native config, and loads it strictly: an
// unknown field, a value of the wrong type, a missing required field or a
// duplicate key is an error, reported with its path and its line and column.
//
// The native format is JSON. A Config holds what was written, without
// defaults filled in, so that it can be written back out unchanged; package
// runtime applies the defaults when it compiles one.
package config

import (
	"os"
	"reflect"
)

// Config is a whole fit config.
type Config struct {
	Listeners []Listener `json:"listeners" fit:"required" doc:"The addresses fit listens on, and the protocols each one serves."`
	Routes    []Route    `json:"routes,omitempty" doc:"How requests are matched, and what handles them."`
}

// Protocols a listener can serve.
const (
	ProtocolHTTP1 = "http/1.1"
	ProtocolH2C   = "h2c"
)

// DefaultProtocols are what a listener that names none serves.
var DefaultProtocols = []string{ProtocolHTTP1}

// Listener is an address fit listens on.
type Listener struct {
	Name      string   `json:"name" fit:"required" doc:"A name unique among the listeners, by which routes refer to this one."`
	Address   string   `json:"address" fit:"required" doc:"The host and port to listen on, as in \":8080\" or \"127.0.0.1:8080\"."`
	Protocols []string `json:"protocols,omitempty" enum:"http/1.1,h2c" doc:"The protocols served; http/1.1 alone when left out."`

	At Value `json:"-" fit:"self"`
}

// Route matches requests and says what handles them: its middleware in
// order, then its handler.
type Route struct {
	Name       string      `json:"name,omitempty" doc:"A name unique among the routes, for logs and metrics."`
	Listeners  []string    `json:"listeners,omitempty" doc:"The names of the listeners the route serves; every listener when left out."`
	Priority   int         `json:"priority,omitempty" doc:"Routes with a higher priority are tried first; equal ones in the order written."`
	Match      *Match      `json:"match,omitempty" doc:"What a request must match; every request when left out."`
	Middleware []ModuleRef `json:"middleware,omitempty" module:"http.middleware" doc:"Middleware a request passes through, in order, before the handler."`
	Handler    ModuleRef   `json:"handler" fit:"required" module:"http.handlers" doc:"What answers the request."`

	At Value `json:"-" fit:"self"`
}

// Match is what a request must match for a route to take it. Each field
// that is set must match; within a field, any one entry may.
type Match struct {
	Host []string `json:"host,omitempty" doc:"Host names, exact or with a leading \"*.\" wildcard."`
	Path []string `json:"path,omitempty" doc:"Paths, exact or ending in \"*\" to match a prefix."`

	At Value `json:"-" fit:"self"`
}

// ModuleRef names a module and holds its params, which the module decodes
// itself.
type ModuleRef struct {
	Module string `json:"module" fit:"required" doc:"The module's name within its namespace."`
	Params Value  `json:"params,omitzero" doc:"The module's params."`

	At Value `json:"-" fit:"self"`
}

// Load parses and checks a config. Its error, when there is one, is Errors.
func Load(data []byte) (*Config, error) {
	root, err := parse(data)
	if err != nil {
		return nil, Errors{err.(*Error)}
	}
	var c Config
	var d decoder
	d.decode(root, "", reflect.ValueOf(&c).Elem(), nil)
	if len(d.errs) > 0 {
		return nil, d.errs
	}
	if errs := c.Validate(); len(errs) > 0 {
		return nil, errs
	}
	return &c, nil
}

// LoadFile loads the config in the file at path.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(data)
}

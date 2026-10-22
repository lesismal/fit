// Package wasm has the wasm middleware, which runs a WebAssembly plugin for
// each request of its routes: the plugin reads the request, changes its
// fields and the response's, or answers it itself.
//
// Plugins run in wazero, a WebAssembly runtime in pure Go (decision 017),
// sandboxed: a plugin reaches nothing but what the ABI below hands it, and
// its memory is bounded. Calls run on a pool of goroutines of the module's
// own, each with an instance of the plugin, never on a fib worker: a request
// is retained and handed to the pool, and goes on from there. A call that
// runs past its timeout is stopped, and its instance replaced.
//
// # The ABI
//
// A plugin is a WASI reactor, as Go builds one with GOOS=wasip1 GOARCH=wasm
// and -buildmode=c-shared, that exports fit_on_request() i32: 0 for the
// request to go on, 1 when the plugin answered it. Package
// plugin/wasm/guest is the Go side of it. The host functions it imports,
// from the module "fit", take strings as a pointer and a length into the
// plugin's memory:
//
//	header_get(kind, name, name_len, buf, buf_len) i32  the field's value
//	    written to buf, up to buf_len bytes, and its length, or -1 when
//	    there is none; kind 0 is the request's fields, 1 the response's
//	header_set(kind, name, name_len, value, value_len)
//	header_add(kind, name, name_len, value, value_len)
//	header_del(kind, name, name_len)
//	request_get(field, buf, buf_len) i32  as header_get, for field 0 the
//	    method, 1 the path, 2 the query, 3 the host, 4 the client's
//	    address, 5 the plugin's config
//	respond(status, body, body_len)  answer the request with status, body
//	    and the response fields the plugin set
//	log(level, msg, msg_len)
//
// The response's fields a plugin sets or deletes are applied as the
// response's header is settled, whoever writes it.
//
// # Proxy-Wasm
//
// A plugin that exports proxy_abi_version_0_2_0 or proxy_abi_version_0_2_1
// is a Proxy-Wasm filter, as Envoy runs them, and gets Proxy-Wasm's request
// phase, as proxywasm.go describes: filters written for Envoy that act on
// requests run unchanged. Its response callbacks are not made yet
// (decision 018).
package wasm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	stdhttp "net/http"
	"os"
	goruntime "runtime"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/tetratelabs/wazero"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".wasm", New: func() any { return new(WASM) }})
}

// Defaults of WASM's params.
const (
	DefaultQueue       = 1024
	DefaultTimeout     = 100 * time.Millisecond
	DefaultWaitTimeout = time.Second
	DefaultMaxMemory   = 128 << 20
	maxInstances       = 16
)

// WASM runs a WebAssembly plugin for each request.
type WASM struct {
	Path        string          `json:"path" fit:"required" doc:"The plugin: a WebAssembly module that exports fit_on_request, as a WASI reactor; package plugin/wasm/guest builds one from Go with GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared."`
	Config      string          `json:"config,omitempty" doc:"A string the plugin reads, for settings of its own."`
	Instances   int             `json:"instances,omitempty" min:"0" doc:"How many instances of the plugin run calls at once, each with its own memory: the CPUs fit has, at most 16, when left out or 0."`
	Queue       int             `json:"queue,omitempty" min:"0" default:"1024" doc:"How many requests may wait for an instance; past it a request is answered 503. 1024 when left out or 0."`
	Timeout     config.Duration `json:"timeout,omitempty" default:"\"100ms\"" doc:"How long a call may run; past it the call is stopped and its instance replaced, and the request goes as a failure does (on_error). 100ms when left out."`
	WaitTimeout config.Duration `json:"wait_timeout,omitempty" default:"\"1s\"" doc:"How long a request may wait for an instance; past it it is answered 503. 1s when left out."`
	MaxMemory   int64           `json:"max_memory,omitempty" min:"0" default:"134217728" doc:"The most memory an instance may have; one that asks for more fails its call. 128 MiB when left out or 0."`
	VMConfig    string          `json:"vm_config,omitempty" doc:"For a Proxy-Wasm plugin, its VM configuration, which it reads as it starts; config is its plugin configuration."`
	Properties  []Property      `json:"properties,omitempty" doc:"For a Proxy-Wasm plugin, properties it reads by their path (proxy_get_property), beside the request's own: request.path, request.url_path, request.query, request.host, request.method, request.scheme, request.protocol, source.address and plugin_name."`
	OnError     string          `json:"on_error,omitempty" enum:"fail,skip" default:"\"fail\"" doc:"What a call that fails, traps or runs past its timeout leads to: fail answers 500; skip lets the request go on as if the plugin were not there. fail when left out."`

	plugin *plugin
	pool   *pool
	graph  module.Holder
}

// Provision compiles the plugin, or takes it compiled for an earlier
// config, and starts its instances.
func (m *WASM) Provision(ctx *module.Context) error {
	if m.Instances == 0 {
		m.Instances = min(goruntime.GOMAXPROCS(0), maxInstances)
	}
	if m.Queue == 0 {
		m.Queue = DefaultQueue
	}
	if m.Timeout <= 0 {
		m.Timeout = config.Duration(DefaultTimeout)
	}
	if m.WaitTimeout <= 0 {
		m.WaitTimeout = config.Duration(DefaultWaitTimeout)
	}
	if m.MaxMemory == 0 {
		m.MaxMemory = DefaultMaxMemory
	}
	code, err := os.ReadFile(m.Path)
	if err != nil {
		return ctx.Errorf("path", "%v", err)
	}
	if m.plugin, err = loadPlugin(code, m.MaxMemory); err != nil {
		return ctx.Errorf("path", "%v", err)
	}
	m.graph = ctx.Graph()
	if m.pool, err = newPool(m); err != nil {
		m.plugin.close()
		return ctx.Errorf("path", "%v", err)
	}
	return nil
}

// Cleanup stops the instances and gives the compiled plugin back.
func (m *WASM) Cleanup() {
	if m.pool != nil {
		m.pool.close()
	}
	if m.plugin != nil {
		m.plugin.close()
	}
}

// Wrap returns next behind the plugin.
func (m *WASM) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		cl := &call{m: m, c: c, next: next, queued: time.Now()}
		c.Retain()
		m.graph.Retain()
		if !m.pool.submit(cl) {
			_ = c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("503 Service Unavailable\n"))
			cl.release()
		}
	})
}

// Property is a property a Proxy-Wasm plugin reads.
type Property struct {
	Path  []string `json:"path" fit:"required" doc:"The property's path, by segment, as [\"route_metadata\", \"filter_metadata\", \"envoy.filters.http.wasm\", \"auth\"]."`
	Value string   `json:"value" doc:"Its value."`
}

// plugin is a compiled plugin and the runtime it runs in, shared by the
// configs that load the same code with the same memory bound, so that a
// reload does not compile it again.
type plugin struct {
	key      [sha256.Size]byte
	maxPages uint32
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	// proxyWasm says the plugin speaks Proxy-Wasm, rather than fit's ABI.
	proxyWasm bool
	refs      int // under plugins.mu
}

var plugins = struct {
	sync.Mutex
	m map[pluginKey]*plugin
}{m: map[pluginKey]*plugin{}}

// compiled keeps what wazero compiles, in memory, so that a plugin loaded
// again, by a config reloaded after its last one was cleaned up, is not
// compiled again.
var compiled = wazero.NewCompilationCache()

type pluginKey struct {
	sum      [sha256.Size]byte
	maxPages uint32
}

// loadPlugin returns code compiled, in a runtime whose instances may have up
// to maxMemory bytes.
func loadPlugin(code []byte, maxMemory int64) (*plugin, error) {
	pages := uint32(min(max(maxMemory>>16, 1), 65536))
	key := pluginKey{sha256.Sum256(code), pages}
	plugins.Lock()
	defer plugins.Unlock()
	if p := plugins.m[key]; p != nil {
		p.refs++
		return p, nil
	}
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true).
		WithCompilationCache(compiled))
	if err := instantiateHost(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, err
	}
	mod, err := rt.CompileModule(ctx, code)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("compiling the plugin: %w", err)
	}
	pw := isProxyWasm(mod)
	if _, ok := mod.ExportedFunctions()[exportOnRequest]; !ok && !pw {
		_ = rt.Close(ctx)
		return nil, errors.New("the plugin exports neither " + exportOnRequest + " nor Proxy-Wasm's ABI version")
	}
	p := &plugin{key: key.sum, maxPages: pages, runtime: rt, compiled: mod, proxyWasm: pw, refs: 1}
	plugins.m[key] = p
	return p, nil
}

// close gives back a hold loadPlugin took; the runtime goes with the last.
func (p *plugin) close() {
	plugins.Lock()
	defer plugins.Unlock()
	if p.refs--; p.refs > 0 {
		return
	}
	delete(plugins.m, pluginKey{p.key, p.maxPages})
	_ = p.runtime.Close(context.Background())
}

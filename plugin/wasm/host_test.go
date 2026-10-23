package wasm

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/lesismal/fit/internal/fibtest"
)

// hostEnv runs the host functions, called directly as a plugin's call would
// make them, on an instance of a module of the tests' own: a page of memory
// and a proxy_on_memory_allocate that returns the global "global", or traps
// when it is 0.
type hostEnv struct {
	t   *testing.T
	w   *worker
	cl  *call
	mod api.Module
	// next is where put writes the next string.
	next uint32
}

// The places in the instance's memory the tests read results from.
const (
	envAlloc   = 16384
	envBuf     = 20000
	envPtr     = 12000
	envSize    = 12004
	envBad     = 0xffffff00
	envStrings = 16
)

func newHostEnv(t *testing.T) *hostEnv {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	g := int32(envAlloc)
	code := wasmModule{pages: 1, global: &g, funcs: []wasmFunc{{
		export: pwAllocate, params: 1, results: 1,
		body: []byte{opGlobalGet, 0, opI32Eqz, opIf, blockEmpty, opUnreachable, opEnd, opGlobalGet, 0},
	}}}.encode()
	mod, err := rt.InstantiateWithConfig(ctx, code, wazero.NewModuleConfig().WithName("guest"))
	if err != nil {
		t.Fatal(err)
	}
	m := &WASM{Path: "test.wasm", Config: "the config", VMConfig: "the vm config",
		Properties: []Property{{Path: []string{"a", "b"}, Value: "ab"}}}
	m.pool = &pool{m: m}
	r := httptest.NewRequest("GET", "http://example.com/p/q?x=1", nil)
	r.Header.Set("X-A", "1")
	r.RemoteAddr = "10.0.0.1:5"
	cl := &call{m: m, c: fibhttp.NewStreamContext(nil, r, &fibtest.Recorder{})}
	w := &worker{p: m.pool, mod: mod, current: cl, pw: &proxyWasm{
		allocate: mod.ExportedFunction(pwAllocate), metricNames: map[string]uint32{},
	}}
	w.ctx = context.WithValue(ctx, workerKey{}, w)
	return &hostEnv{t: t, w: w, cl: cl, mod: mod, next: envStrings}
}

// put writes s into the instance's memory, and returns its address and
// length, as a plugin passes a string.
func (e *hostEnv) put(s string) (uint64, uint64) {
	p := e.next
	if !e.mod.Memory().WriteString(p, s) {
		e.t.Fatal("put")
	}
	e.next += uint32(len(s))
	if e.next >= envPtr {
		e.t.Fatal("out of room for strings")
	}
	return uint64(p), uint64(len(s))
}

// str reads n bytes at p.
func (e *hostEnv) str(p, n uint32) string {
	b, ok := e.mod.Memory().Read(p, n)
	if !ok {
		e.t.Fatal("str")
	}
	return string(b)
}

// given reads what give handed over to envPtr and envSize.
func (e *hostEnv) given() string {
	p, _ := e.mod.Memory().ReadUint32Le(envPtr)
	n, _ := e.mod.Memory().ReadUint32Le(envSize)
	if n == 0 {
		return ""
	}
	return e.str(p, n)
}

func (e *hostEnv) setAlloc(v int32) {
	e.mod.ExportedGlobal("global").(api.MutableGlobal).Set(uint64(uint32(v)))
}

// call calls fn with args, and returns what it left in stack[0], or how it
// panicked.
func (e *hostEnv) call(fn api.GoModuleFunc, args ...uint64) (uint64, any) {
	return e.callIn(e.w.ctx, fn, args...)
}

func (e *hostEnv) callIn(ctx context.Context, fn api.GoModuleFunc, args ...uint64) (result uint64, panicked any) {
	stack := append(make([]uint64, 0, max(len(args), 1)), args...)
	stack = stack[:max(len(args), 1)]
	defer func() { panicked = recover() }()
	fn(ctx, e.mod, stack)
	return stack[0], nil
}

// mustPanic checks that fn panicked with a message holding want.
func (e *hostEnv) mustPanic(what string, panicked any, want string) {
	e.t.Helper()
	if panicked == nil || !strings.Contains(fmt.Sprint(panicked), want) {
		e.t.Errorf("%s: panicked with %v, want %q", what, panicked, want)
	}
}

func (e *hostEnv) mustReturn(what string, got uint64, panicked any, want uint64) {
	e.t.Helper()
	if panicked != nil || got != want {
		e.t.Errorf("%s: %d (panic %v), want %d", what, got, panicked, want)
	}
}

func i32ret(v uint64) int32 { return int32(uint32(v)) }

func TestHostABI(t *testing.T) {
	e := newHostEnv(t)
	req := e.cl.c.Request

	_, p := e.callIn(context.Background(), headerGet, 0, 0, 0, 0, 0)
	e.mustPanic("outside a request", p, "outside a request")
	if err, ok := p.(error); !ok || !strings.HasPrefix(err.Error(), "wasm: ") {
		t.Errorf("the panic %v is no error of the ABI", p)
	}

	xa, xaLen := e.put("x-a")
	r, p := e.call(headerGet, kindRequest, xa, xaLen, envBuf, 64)
	if p != nil || r != 1 || e.str(envBuf, 1) != "1" {
		t.Errorf("header_get: %d %v", r, p)
	}
	none, noneLen := e.put("x-none")
	if r, p := e.call(headerGet, kindRequest, none, noneLen, envBuf, 64); p != nil || i32ret(r) != -1 {
		t.Errorf("header_get of none: %d %v", r, p)
	}
	_, p = e.call(headerGet, kindRequest, xa, xaLen, envBad, 10)
	e.mustPanic("a buffer outside memory", p, "a buffer outside")
	_, p = e.call(headerGet, kindRequest, 0, maxField+1, envBuf, 64)
	e.mustPanic("a long name", p, "longer than 64 KiB")
	_, p = e.call(headerGet, kindRequest, envBad, 4, envBuf, 64)
	e.mustPanic("a name outside memory", p, "outside the plugin's memory")

	// The response's fields, as the plugin sets them before there is one.
	xr, xrLen := e.put("x-r")
	v1, v1Len := e.put("v1")
	v2, v2Len := e.put("v2")
	e.ok("header_set", headerChange(opSet), kindResponse, xr, xrLen, v1, v1Len)
	e.ok("header_add", headerChange(opAdd), kindResponse, xr, xrLen, v2, v2Len)
	if r, p := e.call(headerGet, kindResponse, xr, xrLen, envBuf, 64); p != nil || r != 2 || e.str(envBuf, 2) != "v1" {
		t.Errorf("the response's field: %d %v", r, p)
	}
	e.ok("header_del", headerDel, kindResponse, xr, xrLen)
	if r, p := e.call(headerGet, kindResponse, xr, xrLen, envBuf, 64); p != nil || i32ret(r) != -1 {
		t.Errorf("a deleted response field: %d %v", r, p)
	}
	xn, xnLen := e.put("x-n")
	e.ok("header_add", headerChange(opAdd), kindResponse, xn, xnLen, v1, v1Len)
	e.ok("header_add", headerChange(opAdd), kindResponse, xn, xnLen, v2, v2Len)
	if v, ok := e.cl.response("X-N"); !ok || v != "v1" {
		t.Errorf("added twice: %q %v", v, ok)
	}
	h := map[string][]string{"X-R": {"old"}}
	e.cl.apply(h)
	if _, ok := h["X-R"]; ok || !slices.Equal(h["X-N"], []string{"v1", "v2"}) {
		t.Errorf("applied: %v", h)
	}

	// The request's fields, changed at once.
	e.ok("header_add", headerChange(opAdd), kindRequest, xa, xaLen, v2, v2Len)
	if got := req.Header["X-A"]; !slices.Equal(got, []string{"1", "v2"}) {
		t.Errorf("added to the request: %q", got)
	}
	e.ok("header_del", headerDel, kindRequest, xa, xaLen)
	if _, ok := req.Header["X-A"]; ok {
		t.Error("not deleted from the request")
	}

	for len(e.cl.ops) < maxOps {
		e.cl.change(kindResponse, headerOp{opAdd, "X-Many", "v"})
	}
	_, p = e.call(headerChange(opAdd), kindResponse, xn, xnLen, v1, v1Len)
	e.mustPanic("too many changes", p, "more than 256")
	e.cl.ops = nil

	for field, want := range map[uint64]string{
		fieldMethod: "GET", fieldPath: "/p/q", fieldQuery: "x=1", fieldHost: "example.com",
		fieldRemote: "10.0.0.1:5", fieldConfig: "the config",
	} {
		if r, p := e.call(requestGet, field, envBuf, 64); p != nil || int(r) != len(want) || e.str(envBuf, uint32(len(want))) != want {
			t.Errorf("request_get %d: %d %v, want %q", field, r, p, want)
		}
	}
	if r, p := e.call(requestGet, 99, envBuf, 64); p != nil || i32ret(r) != -1 {
		t.Errorf("request_get of an unknown field: %d %v", r, p)
	}

	body, bodyLen := e.put("the body")
	for _, status := range []uint64{199, 600} {
		_, p = e.call(respond, status, body, bodyLen)
		e.mustPanic("respond", p, "status out of range")
	}
	_, p = e.call(respond, 200, 0, maxBody+1)
	e.mustPanic("respond", p, "longer than 1 MiB")
	e.ok("respond", respond, 201, body, bodyLen)
	if e.cl.status != 201 || string(e.cl.body) != "the body" {
		t.Errorf("responded %d %q", e.cl.status, e.cl.body)
	}

	msg, msgLen := e.put("a line")
	e.ok("log", logMessage, 0, msg, msgLen)
	e.w.p.logs.Store(maxLogs)
	e.ok("log", logMessage, 0, msg, msgLen)
	if n := e.w.p.logs.Load(); n != maxLogs+1 {
		t.Errorf("logs counted %d", n)
	}
}

// ok calls fn, a function that returns nothing, and checks that it did not
// panic.
func (e *hostEnv) ok(what string, fn api.GoModuleFunc, args ...uint64) {
	e.t.Helper()
	if _, p := e.call(fn, args...); p != nil {
		e.t.Errorf("%s: panicked with %v", what, p)
	}
}

// TestHostModules checks the host modules a runtime is given: their
// functions that answer Unimplemented, and that they are given once.
func TestHostModules(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)
	if err := instantiateHost(ctx, rt); err != nil {
		t.Fatal(err)
	}
	// A plugin that calls them.
	var tenZeros []byte
	for range 10 {
		tenZeros = append(tenZeros, i32(0)...)
	}
	code := wasmModule{
		imports: []wasmImport{{"env", "proxy_done", 0, 1}, {"env", "proxy_http_call", 10, 1}},
		funcs: []wasmFunc{
			{export: "done", results: 1, body: []byte{opCall, 0}},
			{export: "http_call", results: 1, body: cat(tenZeros, []byte{opCall, 1})},
		},
	}.encode()
	mod, err := rt.InstantiateWithConfig(ctx, code, wazero.NewModuleConfig().WithName("guest"))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := mod.ExportedFunction("done").Call(ctx); err != nil || r[0] != pwOK {
		t.Errorf("proxy_done: %v %v", r, err)
	}
	if r, err := mod.ExportedFunction("http_call").Call(ctx); err != nil || r[0] != pwUnimplemented {
		t.Errorf("proxy_http_call: %v %v", r, err)
	}
	if err := instantiateHost(ctx, rt); err == nil {
		t.Error("WASI instantiated twice")
	}
	rt2 := wazero.NewRuntime(ctx)
	defer rt2.Close(ctx)
	if _, err := rt2.NewHostModuleBuilder(hostModule).Instantiate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := instantiateHost(ctx, rt2); err == nil {
		t.Error("the host module instantiated twice")
	}
}

func TestHostProxyWasm(t *testing.T) {
	e := newHostEnv(t)
	req := e.cl.c.Request
	str := func(s string) []uint64 { p, n := e.put(s); return []uint64{p, n} }
	args := func(parts ...any) []uint64 {
		var a []uint64
		for _, p := range parts {
			switch p := p.(type) {
			case []uint64:
				a = append(a, p...)
			case int:
				a = append(a, uint64(uint32(p)))
			case uint64:
				a = append(a, p)
			}
		}
		return a
	}

	_, p := e.callIn(context.Background(), pwLog, 0, 0, 0)
	e.mustPanic("outside an instance", p, "outside an instance")
	line := str("a line")
	for _, level := range []int{1, pwLogInfo} {
		r, p := e.call(pwLog, args(level, line)...)
		e.mustReturn("proxy_log", r, p, pwOK)
	}
	e.w.p.logs.Store(maxLogs)
	r, p := e.call(pwLog, args(pwLogInfo, line)...)
	e.mustReturn("proxy_log past the bound", r, p, pwOK)

	// proxy_get_buffer_bytes, and give, which hands its data over.
	for _, tt := range []struct {
		buf, start, size int
		status           uint64
		want             string
	}{
		{pwPluginConfiguration, 0, 100, pwOK, "the config"},
		{pwPluginConfiguration, 4, 3, pwOK, "con"},
		{pwPluginConfiguration, 10, 5, pwOK, ""},
		{pwVMConfiguration, 0, 100, pwOK, "the vm config"},
		{pwPluginConfiguration, 11, 1, pwBadArgument, ""},
		{9, 0, 1, pwNotFound, ""},
	} {
		_ = e.mod.Memory().WriteUint64Le(envPtr, 0)
		r, p := e.call(pwGetBufferBytes, args(tt.buf, tt.start, tt.size, envPtr, envSize)...)
		e.mustReturn(fmt.Sprintf("buffer %d %d", tt.buf, tt.start), r, p, tt.status)
		if tt.status == pwOK && e.given() != tt.want {
			t.Errorf("buffer %d %d: %q, want %q", tt.buf, tt.start, e.given(), tt.want)
		}
	}
	r, p = e.call(pwGetBufferBytes, args(pwPluginConfiguration, 0, 100, envBad, envSize)...)
	e.mustReturn("a pointer outside memory", r, p, pwBadArgument)
	e.setAlloc(-256)
	r, p = e.call(pwGetBufferBytes, args(pwPluginConfiguration, 0, 100, envPtr, envSize)...)
	e.mustReturn("memory allocated outside memory", r, p, pwBadArgument)
	e.setAlloc(0)
	_, p = e.call(pwGetBufferBytes, args(pwPluginConfiguration, 0, 100, envPtr, envSize)...)
	e.mustPanic("an allocation that traps", p, "unreachable")
	e.setAlloc(envAlloc)

	// proxy_send_local_response
	body := str("no")
	pairs := serializePairs([][2]string{{"x-l", "1"}})
	if !e.mod.Memory().Write(envBuf, pairs) {
		t.Fatal("pairs")
	}
	pairsArg := []uint64{envBuf, uint64(len(pairs))}
	bad := str("\x01\x00\x00")
	for _, tt := range []struct {
		name   string
		args   []uint64
		status uint64
	}{
		{"a status out of range", args(99, 0, 0, body, pairsArg, -1), pwBadArgument},
		{"a body too long", args(200, 0, 0, 0, maxBody+1, pairsArg, -1), pwBadArgument},
		{"bad pairs", args(200, 0, 0, body, bad, -1), pwBadArgument},
		{"no gRPC status", args(403, 0, 0, body, pairsArg, -1), pwOK},
	} {
		e.cl.ops = nil
		r, p := e.call(pwSendLocalResponse, tt.args...)
		e.mustReturn(tt.name, r, p, tt.status)
	}
	if _, ok := e.cl.response("Grpc-Status"); ok || e.cl.status != 403 || string(e.cl.body) != "no" {
		t.Errorf("local response: %d %q %v", e.cl.status, e.cl.body, e.cl.ops)
	}
	e.cl.ops = nil
	r, p = e.call(pwSendLocalResponse, args(200, 0, 0, body, pairsArg, 7)...)
	e.mustReturn("a gRPC status", r, p, pwOK)
	if v, _ := e.cl.response("Grpc-Status"); v != "7" {
		t.Errorf("Grpc-Status %q", v)
	}
	if v, _ := e.cl.response("X-L"); v != "1" {
		t.Errorf("X-L %q", v)
	}

	// proxy_get_header_map_value
	get := func(mapType int, name string) (string, uint64) {
		t.Helper()
		_ = e.mod.Memory().WriteUint64Le(envPtr, 0)
		r, p := e.call(pwGetHeaderMapValue, args(mapType, str(name), envPtr, envSize)...)
		if p != nil {
			t.Fatalf("%s: %v", name, p)
		}
		if r != pwOK {
			return "", r
		}
		return e.given(), r
	}
	for _, tt := range []struct {
		mapType    int
		name, want string
		status     uint64
	}{
		{pwRequestHeaders, ":path", "/p/q?x=1", pwOK},
		{pwRequestHeaders, ":scheme", "http", pwOK},
		{pwRequestHeaders, ":nope", "", pwNotFound},
		{pwRequestHeaders, "x-a", "1", pwOK},
		{pwRequestHeaders, "x-none", "", pwNotFound},
		{pwResponseHeaders, "x-l", "1", pwOK},
		{5, "x-a", "", pwNotFound},
	} {
		if v, s := get(tt.mapType, tt.name); v != tt.want || s != tt.status {
			t.Errorf("%d %s: %q %d, want %q %d", tt.mapType, tt.name, v, s, tt.want, tt.status)
		}
	}
	req.TLS = &tls.ConnectionState{}
	if v, _ := get(pwRequestHeaders, ":scheme"); v != "https" {
		t.Errorf(":scheme over TLS %q", v)
	}
	req.TLS = nil

	// proxy_replace_header_map_value, proxy_add_header_map_value and
	// proxy_remove_header_map_value
	for _, tt := range []struct {
		op          int
		mapType     int
		name, value string
		status      uint64
	}{
		{opSet, pwRequestHeaders, ":method", "POST", pwOK},
		{opSet, pwRequestHeaders, ":authority", "other.example", pwOK},
		{opSet, pwRequestHeaders, ":path", "/n?y=2", pwOK},
		{opSet, pwRequestHeaders, ":path", "not a path", pwBadArgument},
		{opSet, pwRequestHeaders, ":scheme", "https", pwBadArgument},
		{opSet, pwRequestHeaders, "x-b", "3", pwOK},
		{opAdd, pwRequestHeaders, "x-b", "4", pwOK},
		{opSet, pwResponseHeaders, "x-c", "5", pwOK},
		{opAdd, 5, "x-c", "5", pwNotFound},
	} {
		r, p := e.call(pwChangeHeaderMap(tt.op), args(tt.mapType, str(tt.name), str(tt.value))...)
		e.mustReturn(tt.name+" "+tt.value, r, p, tt.status)
	}
	if req.Method != "POST" || req.Host != "other.example" || req.URL.Path != "/n" || req.URL.RawQuery != "y=2" ||
		!slices.Equal(req.Header["X-B"], []string{"3", "4"}) {
		t.Errorf("the request: %s %s %s %v", req.Method, req.Host, req.URL, req.Header)
	}
	r, p = e.call(pwRemoveHeaderMapValue, args(pwRequestHeaders, str(":method"))...)
	e.mustReturn("removing :method", r, p, pwBadArgument)
	r, p = e.call(pwRemoveHeaderMapValue, args(pwRequestHeaders, str("x-b"))...)
	e.mustReturn("removing x-b", r, p, pwOK)
	if _, ok := req.Header["X-B"]; ok {
		t.Error("x-b not removed")
	}

	// proxy_get_header_map_pairs
	getPairs := func(mapType int) ([][2]string, uint64) {
		t.Helper()
		r, p := e.call(pwGetHeaderMapPairs, args(mapType, envPtr, envSize)...)
		if p != nil {
			t.Fatal(p)
		}
		if r != pwOK {
			return nil, r
		}
		pairs, ok := deserializePairs([]byte(e.given()))
		if !ok {
			t.Fatal("pairs given badly")
		}
		return pairs, r
	}
	if pairs, _ := getPairs(pwRequestHeaders); !slices.Contains(pairs, [2]string{":method", "POST"}) || !slices.Contains(pairs, [2]string{"x-a", "1"}) {
		t.Errorf("the request's pairs: %v", pairs)
	}
	if pairs, _ := getPairs(pwResponseHeaders); !slices.Contains(pairs, [2]string{"x-c", "5"}) {
		t.Errorf("the response's pairs: %v", pairs)
	}
	if _, s := getPairs(5); s != pwNotFound {
		t.Errorf("pairs of map 5: %d", s)
	}

	// proxy_set_header_map_pairs
	setPairs := func(mapType int, pairs [][2]string) (uint64, any) {
		b := serializePairs(pairs)
		_ = e.mod.Memory().Write(envBuf, b)
		return e.call(pwSetHeaderMapPairs, args(mapType, envBuf, len(b))...)
	}
	r, p = e.call(pwSetHeaderMapPairs, args(pwRequestHeaders, bad)...)
	e.mustReturn("bad pairs", r, p, pwBadArgument)
	_, p = e.call(pwSetHeaderMapPairs, args(pwRequestHeaders, 0, maxBody+1)...)
	e.mustPanic("pairs too long", p, "longer than allowed")
	r, p = setPairs(pwRequestHeaders, [][2]string{{":path", "/s"}, {"x-d", "4"}})
	e.mustReturn("the request's pairs", r, p, pwOK)
	if req.URL.Path != "/s" || len(req.Header) != 1 || req.Header.Get("X-D") != "4" {
		t.Errorf("the request: %s %v", req.URL, req.Header)
	}
	r, p = setPairs(pwResponseHeaders, [][2]string{{"x-e", "5"}})
	e.mustReturn("the response's pairs", r, p, pwOK)
	if len(e.cl.ops) != 1 || e.cl.ops[0].name != "X-E" {
		t.Errorf("the response's changes: %v", e.cl.ops)
	}
	r, p = setPairs(5, [][2]string{{"x-e", "5"}})
	e.mustReturn("pairs of map 5", r, p, pwNotFound)
	r, p = setPairs(pwRequestHeaders, [][2]string{{":scheme", "https"}})
	e.mustReturn("a pseudo-header it cannot set", r, p, pwBadArgument)

	// proxy_get_property
	req.URL.RawQuery = "z=1"
	property := func(path string) (string, uint64) {
		t.Helper()
		r, p := e.call(pwGetProperty, args(str(path), envPtr, envSize)...)
		if p != nil {
			t.Fatal(p)
		}
		if r != pwOK {
			return "", r
		}
		return e.given(), r
	}
	for path, want := range map[string]string{
		"a\x00b\x00":           "ab",
		"plugin_name":          "test.wasm",
		"request\x00path":      "/s?z=1",
		"request\x00url_path":  "/s",
		"request\x00query":     "z=1",
		"request\x00host":      "other.example",
		"request\x00method":    "POST",
		"request\x00scheme":    "http",
		"request\x00protocol":  "HTTP/1.1",
		"source\x00address":    "10.0.0.1:5",
		"request\x00nope":      "",
		"request":              "",
		"request\x00path\x00x": "",
	} {
		v, s := property(path)
		wantStatus := uint64(pwOK)
		if want == "" {
			wantStatus = pwNotFound
		}
		if s != wantStatus || v != want {
			t.Errorf("property %q: %q %d, want %q", path, v, s, want)
		}
	}
	req.TLS = &tls.ConnectionState{}
	if v, _ := property("request\x00scheme"); v != "https" {
		t.Errorf("request.scheme over TLS: %q", v)
	}
	req.TLS = nil
	e.w.current = nil
	if _, s := property("request\x00path"); s != pwNotFound {
		t.Errorf("request.path outside a request: %d", s)
	}
	e.w.current = e.cl
	_, p = e.call(pwGetProperty, args(0, maxBody+1, envPtr, envSize)...)
	e.mustPanic("a path too long", p, "longer than allowed")

	// Metrics
	define := func(name string, idPtr uint64) (uint32, uint64) {
		t.Helper()
		r, p := e.call(pwDefineMetric, args(0, str(name), idPtr)...)
		if p != nil {
			t.Fatal(p)
		}
		id, _ := e.mod.Memory().ReadUint32Le(envPtr)
		return id, r
	}
	if id, s := define("m1", envPtr); s != pwOK || id != 0 {
		t.Errorf("m1: %d %d", id, s)
	}
	if id, s := define("m2", envPtr); s != pwOK || id != 1 {
		t.Errorf("m2: %d %d", id, s)
	}
	if id, s := define("m1", envPtr); s != pwOK || id != 0 {
		t.Errorf("m1 again: %d %d", id, s)
	}
	if _, s := define("m3", envBad); s != pwBadArgument {
		t.Errorf("an id outside memory: %d", s)
	}
	for _, tt := range []struct {
		fn    api.GoModuleFunc
		id    uint64
		value uint64
		want  int64
	}{
		{pwChangeMetric(true), 0, 5, 5},
		{pwChangeMetric(true), 0, 5, 10},
		{pwChangeMetric(false), 0, 3, 3},
	} {
		r, p := e.call(tt.fn, tt.id, tt.value)
		e.mustReturn("a metric's change", r, p, pwOK)
		if e.w.pw.metrics[0] != tt.want {
			t.Errorf("metric %d, want %d", e.w.pw.metrics[0], tt.want)
		}
	}
	r, p = e.call(pwChangeMetric(true), 999, 1)
	e.mustReturn("an unknown metric", r, p, pwNotFound)
	r, p = e.call(pwGetMetric, 0, envBuf)
	e.mustReturn("proxy_get_metric", r, p, pwOK)
	if v, _ := e.mod.Memory().ReadUint64Le(envBuf); v != 3 {
		t.Errorf("metric read %d", v)
	}
	r, p = e.call(pwGetMetric, 999, envBuf)
	e.mustReturn("an unknown metric read", r, p, pwNotFound)
	r, p = e.call(pwGetMetric, 0, envBad)
	e.mustReturn("a metric read outside memory", r, p, pwBadArgument)
	for len(e.w.pw.metrics) < maxMetrics {
		e.w.pw.metrics = append(e.w.pw.metrics, 0)
	}
	e.next = envStrings // room for more names
	if _, s := define("one too many", envPtr); s != pwBadArgument {
		t.Errorf("past the metrics' bound: %d", s)
	}

	if pairs, ok := deserializePairs(nil); !ok || pairs != nil {
		t.Errorf("no pairs: %v %v", pairs, ok)
	}
}

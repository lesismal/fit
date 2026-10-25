package wasm

import (
	"context"
	"encoding/binary"
	"errors"
	"log"
	stdhttp "net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Proxy-Wasm: the ABI Envoy and others run HTTP filters through, v0.2.x,
// as far as a filter's request headers go. A plugin is one when it exports
// proxy_abi_version_0_2_0 or proxy_abi_version_0_2_1. Each instance starts
// its root context (id 1) with the vm_config and the config when it is
// made, and fails to load if the plugin refuses either. Each request is a
// context of its own: created, its request headers handed to the plugin,
// then done, logged and deleted, so that the plugin forgets it, unless the
// plugin follows the response (response.go). What the plugin decides then
// goes: a local response it sent, or the request on, with the changes it
// made to the request's headers (pseudo-headers :method, :path, :authority
// included) and to the response's, which apply when its header is settled.
// The response's callbacks read and change the response's header, body
// and trailer. Calls out (proxy_http_call), shared data and queues, ticks,
// foreign functions, the request's body and trailer, and setting properties
// answer Unimplemented or NotFound.

// The exports a Proxy-Wasm plugin has, and the ones it may have.
var proxyWasmVersions = []string{"proxy_abi_version_0_2_1", "proxy_abi_version_0_2_0"}

const (
	pwOnContextCreate  = "proxy_on_context_create"
	pwOnVMStart        = "proxy_on_vm_start"
	pwOnConfigure      = "proxy_on_configure"
	pwOnRequestHeaders = "proxy_on_request_headers"
	pwOnDone           = "proxy_on_done"
	pwOnLog            = "proxy_on_log"
	pwOnDelete         = "proxy_on_delete"
	pwAllocate         = "proxy_on_memory_allocate"
)

// Proxy-Wasm's statuses, map types, buffer types and actions.
const (
	pwOK            = 0
	pwNotFound      = 1
	pwBadArgument   = 2
	pwUnimplemented = 12

	pwRequestHeaders  = 0
	pwResponseHeaders = 2

	pwVMConfiguration     = 6
	pwPluginConfiguration = 7

	pwActionPause = 1

	pwRootContext = 1
	maxMetrics    = 256
)

// isProxyWasm reports whether a compiled plugin speaks Proxy-Wasm.
func isProxyWasm(m wazero.CompiledModule) bool {
	exports := m.ExportedFunctions()
	for _, v := range proxyWasmVersions {
		if _, ok := exports[v]; ok {
			return true
		}
	}
	return false
}

// proxyWasm is a Proxy-Wasm instance's own: the functions the host calls,
// the next request's context id, and the plugin's metrics.
type proxyWasm struct {
	contextCreate, requestHeaders, done, logFn, deleteFn, allocate api.Function
	// The response's callbacks, those the plugin exports; response says it
	// exports one.
	responseHeaders, responseBody, responseTrailers api.Function
	response                                        bool
	stack                                           [3]uint64
	allocStack                                      [1]uint64
	nextID                                          uint32
	metrics                                         []int64
	metricNames                                     map[string]uint32
}

// startProxyWasm finds the plugin's functions and starts its root context.
func (w *worker) startProxyWasm() error {
	pw := &proxyWasm{nextID: pwRootContext + 1, metricNames: map[string]uint32{}}
	for name, fn := range map[string]*api.Function{
		pwOnContextCreate: &pw.contextCreate, pwOnRequestHeaders: &pw.requestHeaders,
		pwOnDone: &pw.done, pwOnLog: &pw.logFn, pwOnDelete: &pw.deleteFn, pwAllocate: &pw.allocate,
	} {
		if *fn = w.mod.ExportedFunction(name); *fn == nil {
			return errors.New("the Proxy-Wasm plugin does not export " + name)
		}
	}
	pw.responseHeaders = w.mod.ExportedFunction(pwOnResponseHeaders)
	pw.responseBody = w.mod.ExportedFunction(pwOnResponseBody)
	pw.responseTrailers = w.mod.ExportedFunction(pwOnResponseTrailers)
	pw.response = pw.responseHeaders != nil || pw.responseBody != nil || pw.responseTrailers != nil
	w.pw = pw
	m := w.p.m
	if _, err := pw.contextCreate.Call(w.ctx, pwRootContext, 0); err != nil {
		return err
	}
	if fn := w.mod.ExportedFunction(pwOnVMStart); fn != nil {
		r, err := fn.Call(w.ctx, pwRootContext, uint64(len(m.VMConfig)))
		if err != nil {
			return err
		}
		if r[0] == 0 {
			return errors.New("the plugin refused its vm_config")
		}
	}
	if fn := w.mod.ExportedFunction(pwOnConfigure); fn != nil {
		r, err := fn.Call(w.ctx, pwRootContext, uint64(len(m.Config)))
		if err != nil {
			return err
		}
		if r[0] == 0 {
			return errors.New("the plugin refused its config")
		}
	}
	return nil
}

// servePW runs the plugin's request callbacks for cl, in a context of their
// own, and returns whether the plugin paused the request. A plugin that
// follows the response keeps the context open for it, unless it answered
// the request itself.
func (w *worker) servePW(cl *call) (paused bool, err error) {
	pw := w.pw
	id := pw.nextID
	if pw.nextID++; pw.nextID == 0 {
		pw.nextID = pwRootContext + 1
	}
	call := func(fn api.Function, params ...uint64) (uint64, error) {
		copy(pw.stack[:], params)
		err := fn.CallWithStack(w.ctx, pw.stack[:])
		return pw.stack[0], err
	}
	if _, err := call(pw.contextCreate, uint64(id), pwRootContext); err != nil {
		return false, err
	}
	r := cl.c.Request
	end := uint64(0)
	if r.ContentLength == 0 && len(r.TransferEncoding) == 0 {
		end = 1
	}
	action, err := call(pw.requestHeaders, uint64(id), uint64(len(r.Header)+4), end)
	if err != nil {
		return false, err
	}
	paused = uint32(action) == pwActionPause
	if !paused && cl.status == 0 && w.follows(cl) {
		cl.w, cl.id, cl.gen, cl.followed = w, id, w.gen, true
		w.open++
		return false, nil
	}
	// The stream is over, as far as the plugin is to know.
	for _, fn := range []api.Function{pw.done, pw.logFn, pw.deleteFn} {
		if _, err := call(fn, uint64(id)); err != nil {
			return false, err
		}
	}
	return paused, nil
}

// workerOf returns the worker running a host function.
func workerOf(ctx context.Context) *worker {
	w, _ := ctx.Value(workerKey{}).(*worker)
	if w == nil {
		panic(errABI("a host function called outside an instance"))
	}
	return w
}

// give hands data to the plugin: memory it allocates, whose address and
// length go to the pointers at ptrPtr and sizePtr.
func (w *worker) give(mod api.Module, data []byte, ptrPtr, sizePtr uint32) uint32 {
	var p uint32
	if len(data) > 0 {
		w.pw.allocStack[0] = uint64(len(data))
		if err := w.pw.allocate.CallWithStack(w.ctx, w.pw.allocStack[:]); err != nil {
			panic(err)
		}
		p = uint32(w.pw.allocStack[0])
		if !mod.Memory().Write(p, data) {
			return pwBadArgument
		}
	}
	if !mod.Memory().WriteUint32Le(ptrPtr, p) || !mod.Memory().WriteUint32Le(sizePtr, uint32(len(data))) {
		return pwBadArgument
	}
	return pwOK
}

// instantiateProxyWasmHost adds the module env, Proxy-Wasm's host
// functions, to rt.
func instantiateProxyWasmHost(ctx context.Context, rt wazero.Runtime) error {
	b := rt.NewHostModuleBuilder("env")
	fn := func(name string, f api.GoModuleFunc, params ...api.ValueType) {
		b.NewFunctionBuilder().WithGoModuleFunction(f, params, []api.ValueType{api.ValueTypeI32}).Export(name)
	}
	i32s := func(n int) []api.ValueType {
		t := make([]api.ValueType, n)
		for i := range t {
			t[i] = api.ValueTypeI32
		}
		return t
	}
	status := func(s uint32) api.GoModuleFunc {
		return func(_ context.Context, _ api.Module, stack []uint64) { stack[0] = uint64(s) }
	}
	fn("proxy_log", pwLog, i32s(3)...)
	fn("proxy_send_local_response", pwSendLocalResponse, i32s(8)...)
	fn("proxy_get_header_map_value", pwGetHeaderMapValue, i32s(5)...)
	fn("proxy_add_header_map_value", pwChangeHeaderMap(opAdd), i32s(5)...)
	fn("proxy_replace_header_map_value", pwChangeHeaderMap(opSet), i32s(5)...)
	fn("proxy_remove_header_map_value", pwRemoveHeaderMapValue, i32s(3)...)
	fn("proxy_get_header_map_pairs", pwGetHeaderMapPairs, i32s(3)...)
	fn("proxy_set_header_map_pairs", pwSetHeaderMapPairs, i32s(3)...)
	fn("proxy_get_buffer_bytes", pwGetBufferBytes, i32s(5)...)
	fn("proxy_set_buffer_bytes", pwSetBufferBytes, i32s(5)...)
	fn("proxy_get_property", pwGetProperty, i32s(4)...)
	fn("proxy_set_effective_context", status(pwOK), i32s(1)...)
	fn("proxy_done", status(pwOK))
	fn("proxy_define_metric", pwDefineMetric, i32s(4)...)
	fn("proxy_increment_metric", pwChangeMetric(true), api.ValueTypeI32, api.ValueTypeI64)
	fn("proxy_record_metric", pwChangeMetric(false), api.ValueTypeI32, api.ValueTypeI64)
	fn("proxy_get_metric", pwGetMetric, i32s(2)...)
	for name, n := range map[string]int{
		"proxy_set_property": 4, "proxy_continue_stream": 1,
		"proxy_close_stream": 1, "proxy_http_call": 10, "proxy_call_foreign_function": 6,
		"proxy_set_tick_period_milliseconds": 1, "proxy_get_shared_data": 5, "proxy_set_shared_data": 5,
		"proxy_register_shared_queue": 3, "proxy_resolve_shared_queue": 5,
		"proxy_dequeue_shared_queue": 3, "proxy_enqueue_shared_queue": 3,
	} {
		fn(name, status(pwUnimplemented), i32s(n)...)
	}
	_, err := b.Instantiate(ctx)
	return err
}

// pwLogInfo is Proxy-Wasm's info level; lines below it, trace and debug,
// are dropped.
const pwLogInfo = 2

func pwLog(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	msg := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
	if api.DecodeU32(stack[0]) >= pwLogInfo && w.p.logs.Add(1) <= maxLogs {
		log.Printf("fit: wasm plugin %s: %s", w.p.m.Path, msg)
	}
	stack[0] = pwOK
}

func pwSendLocalResponse(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	status := int(api.DecodeU32(stack[0]))
	if status < 200 || status > 599 {
		stack[0] = pwBadArgument
		return
	}
	n := api.DecodeU32(stack[4])
	if n > maxBody {
		stack[0] = pwBadArgument
		return
	}
	cl.status, cl.body = status, append([]byte(nil), read(mod, api.DecodeU32(stack[3]), n)...)
	pairs, ok := deserializePairs(read(mod, api.DecodeU32(stack[5]), api.DecodeU32(stack[6])))
	if !ok {
		stack[0] = pwBadArgument
		return
	}
	for _, kv := range pairs {
		cl.change(kindResponse, headerOp{opAdd, textproto.CanonicalMIMEHeaderKey(kv[0]), kv[1]})
	}
	if grpc := int32(api.DecodeU32(stack[7])); grpc >= 0 {
		cl.change(kindResponse, headerOp{opSet, "Grpc-Status", strconv.Itoa(int(grpc))})
	}
	stack[0] = pwOK
}

// requestPairs returns the request's headers as Proxy-Wasm has them: its
// pseudo-headers, then its fields, in lower case.
func requestPairs(r *stdhttp.Request) [][2]string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	pairs := [][2]string{{":method", r.Method}, {":path", r.URL.RequestURI()}, {":authority", r.Host}, {":scheme", scheme}}
	for k, vs := range r.Header {
		for _, v := range vs {
			pairs = append(pairs, [2]string{strings.ToLower(k), v})
		}
	}
	return pairs
}

func pwGetHeaderMapValue(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	cl := callOf(ctx)
	name := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
	var v string
	ok := false
	switch api.DecodeU32(stack[0]) {
	case pwRequestHeaders:
		if strings.HasPrefix(name, ":") {
			for _, kv := range requestPairs(cl.c.Request)[:4] {
				if kv[0] == name {
					v, ok = kv[1], true
				}
			}
		} else if vs := cl.c.Request.Header[textproto.CanonicalMIMEHeaderKey(name)]; len(vs) > 0 {
			v, ok = vs[0], true
		}
	case pwResponseHeaders:
		switch h := cl.respHeader; {
		case name == ":status" && cl.respStatus != 0:
			v, ok = strconv.Itoa(cl.respStatus), true
		case h != nil:
			if vs := h[textproto.CanonicalMIMEHeaderKey(name)]; len(vs) > 0 {
				v, ok = vs[0], true
			}
		case cl.respStatus == 0:
			v, ok = cl.response(textproto.CanonicalMIMEHeaderKey(name))
		}
	case pwResponseTrailers:
		if vs := cl.trailer[textproto.CanonicalMIMEHeaderKey(name)]; len(vs) > 0 {
			v, ok = vs[0], true
		}
	}
	if !ok {
		stack[0] = pwNotFound
		return
	}
	stack[0] = uint64(w.give(mod, []byte(v), api.DecodeU32(stack[3]), api.DecodeU32(stack[4])))
}

// setPseudo sets one of the request's pseudo-headers, and reports whether
// name is one it can set.
func setPseudo(r *stdhttp.Request, name, value string) bool {
	switch name {
	case ":method":
		r.Method = value
	case ":authority":
		r.Host = value
	case ":path":
		u, err := url.ParseRequestURI(value)
		if err != nil {
			return false
		}
		r.URL.Path, r.URL.RawPath, r.URL.RawQuery = u.Path, u.RawPath, u.RawQuery
		r.RequestURI = value
	default:
		return false
	}
	return true
}

func pwChangeHeaderMap(op int) api.GoModuleFunc {
	return func(ctx context.Context, mod api.Module, stack []uint64) {
		cl := callOf(ctx)
		name := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
		value := readField(mod, api.DecodeU32(stack[3]), api.DecodeU32(stack[4]))
		stack[0] = uint64(pwChange(cl, api.DecodeU32(stack[0]), op, name, value))
	}
}

func pwRemoveHeaderMapValue(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	name := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
	stack[0] = uint64(pwChange(cl, api.DecodeU32(stack[0]), opDel, name, ""))
}

// pwChange makes a change to the request's headers or the response's.
func pwChange(cl *call, mapType uint32, op int, name, value string) uint32 {
	switch mapType {
	case pwRequestHeaders:
		if strings.HasPrefix(name, ":") {
			if op == opDel || !setPseudo(cl.c.Request, name, value) {
				return pwBadArgument
			}
			return pwOK
		}
		cl.change(kindRequest, headerOp{op, textproto.CanonicalMIMEHeaderKey(name), value})
	case pwResponseHeaders:
		switch {
		case cl.respHeader != nil:
			if strings.HasPrefix(name, ":") {
				// Neither the status nor anything else of fib's can change.
				return pwBadArgument
			}
			changeHeader(cl.respHeader, headerOp{op, textproto.CanonicalMIMEHeaderKey(name), value})
		case cl.respStatus != 0:
			// The header has gone.
			return pwBadArgument
		default:
			cl.change(kindResponse, headerOp{op, textproto.CanonicalMIMEHeaderKey(name), value})
		}
	case pwResponseTrailers:
		if cl.trailer == nil || strings.HasPrefix(name, ":") {
			return pwBadArgument
		}
		changeHeader(cl.trailer, headerOp{op, textproto.CanonicalMIMEHeaderKey(name), value})
	default:
		return pwNotFound
	}
	return pwOK
}

// changeHeader makes op on h.
func changeHeader(h stdhttp.Header, op headerOp) {
	switch op.op {
	case opSet:
		h[op.name] = []string{op.value}
	case opAdd:
		h[op.name] = append(h[op.name], op.value)
	case opDel:
		delete(h, op.name)
	}
}

// fieldPairs returns h's fields as Proxy-Wasm has them, in lower case,
// after pairs.
func fieldPairs(pairs [][2]string, h stdhttp.Header) [][2]string {
	for k, vs := range h {
		for _, v := range vs {
			pairs = append(pairs, [2]string{strings.ToLower(k), v})
		}
	}
	return pairs
}

func pwGetHeaderMapPairs(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	cl := callOf(ctx)
	var pairs [][2]string
	switch api.DecodeU32(stack[0]) {
	case pwRequestHeaders:
		pairs = requestPairs(cl.c.Request)
	case pwResponseHeaders:
		switch {
		case cl.respHeader != nil:
			pairs = fieldPairs([][2]string{{":status", strconv.Itoa(cl.respStatus)}}, cl.respHeader)
		case cl.respStatus != 0:
			pairs = [][2]string{{":status", strconv.Itoa(cl.respStatus)}}
		default:
			h := stdhttp.Header{}
			cl.apply(h)
			pairs = fieldPairs(nil, h)
		}
	case pwResponseTrailers:
		pairs = fieldPairs(nil, cl.trailer)
	default:
		stack[0] = pwNotFound
		return
	}
	stack[0] = uint64(w.give(mod, serializePairs(pairs), api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
}

func pwSetHeaderMapPairs(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	mapType := api.DecodeU32(stack[0])
	pairs, ok := deserializePairs(read(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	if !ok {
		stack[0] = pwBadArgument
		return
	}
	switch mapType {
	case pwRequestHeaders:
		clear(cl.c.Request.Header)
	case pwResponseHeaders:
		switch {
		case cl.respHeader != nil:
			clear(cl.respHeader)
		case cl.respStatus != 0:
			stack[0] = pwBadArgument
			return
		default:
			cl.ops = cl.ops[:0]
		}
	case pwResponseTrailers:
		if cl.trailer == nil {
			stack[0] = pwBadArgument
			return
		}
		clear(cl.trailer)
	default:
		stack[0] = pwNotFound
		return
	}
	for _, kv := range pairs {
		op := opAdd
		if strings.HasPrefix(kv[0], ":") {
			if mapType != pwRequestHeaders {
				// The status stays as it is.
				continue
			}
			op = opSet
		}
		if s := pwChange(cl, mapType, op, kv[0], kv[1]); s != pwOK {
			stack[0] = uint64(s)
			return
		}
	}
	stack[0] = pwOK
}

func pwGetBufferBytes(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	var data string
	switch api.DecodeU32(stack[0]) {
	case pwVMConfiguration:
		data = w.p.m.VMConfig
	case pwPluginConfiguration:
		data = w.p.m.Config
	case pwResponseBody:
		cl := w.current
		if cl == nil || !cl.inBody {
			stack[0] = pwNotFound
			return
		}
		start, size := int(api.DecodeU32(stack[1])), int(api.DecodeU32(stack[2]))
		if start > len(cl.buf) {
			stack[0] = pwBadArgument
			return
		}
		body := cl.buf[start : start+min(len(cl.buf)-start, size)]
		stack[0] = uint64(w.give(mod, body, api.DecodeU32(stack[3]), api.DecodeU32(stack[4])))
		return
	default:
		stack[0] = pwNotFound
		return
	}
	start, size := int(api.DecodeU32(stack[1])), int(api.DecodeU32(stack[2]))
	if start > len(data) {
		stack[0] = pwBadArgument
		return
	}
	data = data[start:min(len(data), start+size)]
	stack[0] = uint64(w.give(mod, []byte(data), api.DecodeU32(stack[3]), api.DecodeU32(stack[4])))
}

func pwSetBufferBytes(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	cl := w.current
	if api.DecodeU32(stack[0]) != pwResponseBody || cl == nil || !cl.inBody {
		stack[0] = pwNotFound
		return
	}
	data := read(mod, api.DecodeU32(stack[3]), api.DecodeU32(stack[4]))
	stack[0] = uint64(cl.setBody(api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), data))
}

func pwGetProperty(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	path := strings.Split(strings.TrimSuffix(string(read(mod, api.DecodeU32(stack[0]), api.DecodeU32(stack[1]))), "\x00"), "\x00")
	v, ok := w.property(path)
	if !ok {
		stack[0] = pwNotFound
		return
	}
	stack[0] = uint64(w.give(mod, []byte(v), api.DecodeU32(stack[2]), api.DecodeU32(stack[3])))
}

// property returns the value of the property at path: one the config
// sets, or one of the request's.
func (w *worker) property(path []string) (string, bool) {
	joined := strings.Join(path, "\x00")
	for _, p := range w.p.m.Properties {
		if strings.Join(p.Path, "\x00") == joined {
			return p.Value, true
		}
	}
	if joined == "plugin_name" {
		return w.p.m.Path, true
	}
	cl := w.current
	if cl == nil || len(path) != 2 {
		return "", false
	}
	r := cl.c.Request
	switch path[0] + "." + path[1] {
	case "request.path":
		return r.URL.RequestURI(), true
	case "request.url_path":
		return r.URL.Path, true
	case "request.query":
		return r.URL.RawQuery, true
	case "request.host":
		return r.Host, true
	case "request.method":
		return r.Method, true
	case "request.scheme":
		if r.TLS != nil {
			return "https", true
		}
		return "http", true
	case "request.protocol":
		return r.Proto, true
	case "source.address":
		return r.RemoteAddr, true
	case "response.code":
		if cl.respStatus != 0 {
			return strconv.Itoa(cl.respStatus), true
		}
	}
	return "", false
}

func pwDefineMetric(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	name := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
	id, ok := w.pw.metricNames[name]
	if !ok {
		if len(w.pw.metrics) == maxMetrics {
			stack[0] = pwBadArgument
			return
		}
		id = uint32(len(w.pw.metrics))
		w.pw.metrics = append(w.pw.metrics, 0)
		w.pw.metricNames[name] = id
	}
	if !mod.Memory().WriteUint32Le(api.DecodeU32(stack[3]), id) {
		stack[0] = pwBadArgument
		return
	}
	stack[0] = pwOK
}

func pwChangeMetric(increment bool) api.GoModuleFunc {
	return func(ctx context.Context, _ api.Module, stack []uint64) {
		w := workerOf(ctx)
		id := api.DecodeU32(stack[0])
		if int(id) >= len(w.pw.metrics) {
			stack[0] = pwNotFound
			return
		}
		if increment {
			w.pw.metrics[id] += int64(stack[1])
		} else {
			w.pw.metrics[id] = int64(stack[1])
		}
		stack[0] = pwOK
	}
}

func pwGetMetric(ctx context.Context, mod api.Module, stack []uint64) {
	w := workerOf(ctx)
	id := api.DecodeU32(stack[0])
	if int(id) >= len(w.pw.metrics) {
		stack[0] = pwNotFound
		return
	}
	if !mod.Memory().WriteUint64Le(api.DecodeU32(stack[1]), uint64(w.pw.metrics[id])) {
		stack[0] = pwBadArgument
		return
	}
	stack[0] = pwOK
}

// serializePairs encodes pairs as Proxy-Wasm does: their count, each one's
// key and value lengths, then each key and value with a NUL after it, the
// numbers little-endian.
func serializePairs(pairs [][2]string) []byte {
	size := 4
	for _, kv := range pairs {
		size += 8 + len(kv[0]) + len(kv[1]) + 2
	}
	b := make([]byte, 4, size)
	binary.LittleEndian.PutUint32(b, uint32(len(pairs)))
	for _, kv := range pairs {
		b = binary.LittleEndian.AppendUint32(b, uint32(len(kv[0])))
		b = binary.LittleEndian.AppendUint32(b, uint32(len(kv[1])))
	}
	for _, kv := range pairs {
		b = append(append(append(append(b, kv[0]...), 0), kv[1]...), 0)
	}
	return b
}

// deserializePairs decodes what serializePairs encodes, copying it.
func deserializePairs(b []byte) ([][2]string, bool) {
	if len(b) == 0 {
		return nil, true
	}
	if len(b) < 4 {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint32(b))
	if n > maxOps || 4+8*n > len(b) {
		return nil, false
	}
	pairs := make([][2]string, n)
	data := 4 + 8*n
	for i := range n {
		kl := int(binary.LittleEndian.Uint32(b[4+8*i:]))
		vl := int(binary.LittleEndian.Uint32(b[8+8*i:]))
		if data+kl+vl+2 > len(b) {
			return nil, false
		}
		pairs[i][0] = string(b[data : data+kl])
		data += kl + 1
		pairs[i][1] = string(b[data : data+vl])
		data += vl + 1
	}
	return pairs, true
}

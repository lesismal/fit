package wasm

import (
	"context"
	"log"
	stdhttp "net/http"
	"net/textproto"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// The names the ABI uses.
const (
	hostModule      = "fit"
	exportOnRequest = "fit_on_request"
	// actionStop is what fit_on_request returns once the plugin answered.
	actionStop = 1
)

// The kinds of field header_* takes, and the fields request_get does.
const (
	kindRequest  = 0
	kindResponse = 1

	fieldMethod = 0
	fieldPath   = 1
	fieldQuery  = 2
	fieldHost   = 3
	fieldRemote = 4
	fieldConfig = 5
)

// The bounds on what a call may ask of the host: a name or value longer
// than maxField, more than maxOps changes to the response's fields, or a
// body past maxBody fails the call.
const (
	maxField = 64 << 10
	maxOps   = 256
	maxBody  = 1 << 20
)

// errABI is how a host function fails the call that made it: wazero turns
// the panic into the call's error.
type errABI string

func (e errABI) Error() string { return "wasm: " + string(e) }

// instantiateHost adds the host module, and WASI, to rt.
func instantiateHost(ctx context.Context, rt wazero.Runtime) error {
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return err
	}
	i32 := api.ValueTypeI32
	b := rt.NewHostModuleBuilder(hostModule)
	export := func(name string, fn api.GoModuleFunc, params, results []api.ValueType) {
		b.NewFunctionBuilder().WithGoModuleFunction(fn, params, results).Export(name)
	}
	export("header_get", headerGet, []api.ValueType{i32, i32, i32, i32, i32}, []api.ValueType{i32})
	export("header_set", headerChange(opSet), []api.ValueType{i32, i32, i32, i32, i32}, nil)
	export("header_add", headerChange(opAdd), []api.ValueType{i32, i32, i32, i32, i32}, nil)
	export("header_del", headerDel, []api.ValueType{i32, i32, i32}, nil)
	export("request_get", requestGet, []api.ValueType{i32, i32, i32}, []api.ValueType{i32})
	export("respond", respond, []api.ValueType{i32, i32, i32}, nil)
	export("log", logMessage, []api.ValueType{i32, i32, i32}, nil)
	_, err := b.Instantiate(ctx)
	return err
}

// callOf returns the call the instance running a host function serves.
func callOf(ctx context.Context) *call {
	w, _ := ctx.Value(workerKey{}).(*worker)
	if w == nil || w.current == nil {
		panic(errABI("a host function called outside a request"))
	}
	return w.current
}

// read returns n bytes of mod's memory at ptr, without copying them.
func read(mod api.Module, ptr, n uint32) []byte {
	if n > maxBody {
		panic(errABI("a string longer than allowed"))
	}
	b, ok := mod.Memory().Read(ptr, n)
	if !ok {
		panic(errABI("a string outside the plugin's memory"))
	}
	return b
}

// readField returns a field's name or value, copied.
func readField(mod api.Module, ptr, n uint32) string {
	if n > maxField {
		panic(errABI("a field longer than 64 KiB"))
	}
	return string(read(mod, ptr, n))
}

// write writes s into mod's memory at buf, up to n bytes, and returns s's
// length, for the plugin to call again with room for it.
func write(mod api.Module, buf, n uint32, s string) int32 {
	if len(s) > 0 && n > 0 {
		if !mod.Memory().WriteString(buf, s[:min(len(s), int(n))]) {
			panic(errABI("a buffer outside the plugin's memory"))
		}
	}
	return int32(len(s))
}

func headerGet(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	kind := api.DecodeU32(stack[0])
	name := textproto.CanonicalMIMEHeaderKey(readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	result := int32(-1)
	if kind == kindRequest {
		if v, ok := cl.c.Request.Header[name]; ok && len(v) > 0 {
			result = write(mod, api.DecodeU32(stack[3]), api.DecodeU32(stack[4]), v[0])
		}
	} else if v, ok := cl.response(name); ok {
		result = write(mod, api.DecodeU32(stack[3]), api.DecodeU32(stack[4]), v)
	}
	stack[0] = api.EncodeI32(result)
}

// The changes header_set, header_add and header_del make.
const (
	opSet = iota
	opAdd
	opDel
)

// headerOp is a change to the response's fields, made once its header is
// settled.
type headerOp struct {
	op          int
	name, value string
}

func headerChange(op int) api.GoModuleFunc {
	return func(ctx context.Context, mod api.Module, stack []uint64) {
		cl := callOf(ctx)
		name := textproto.CanonicalMIMEHeaderKey(readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
		value := readField(mod, api.DecodeU32(stack[3]), api.DecodeU32(stack[4]))
		cl.change(api.DecodeU32(stack[0]), headerOp{op, name, value})
	}
}

func headerDel(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	name := textproto.CanonicalMIMEHeaderKey(readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2])))
	cl.change(api.DecodeU32(stack[0]), headerOp{op: opDel, name: name})
}

func requestGet(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	r := cl.c.Request
	var v string
	switch api.DecodeU32(stack[0]) {
	case fieldMethod:
		v = r.Method
	case fieldPath:
		v = r.URL.Path
	case fieldQuery:
		v = r.URL.RawQuery
	case fieldHost:
		v = r.Host
	case fieldRemote:
		v = r.RemoteAddr
	case fieldConfig:
		v = cl.m.Config
	default:
		stack[0] = api.EncodeI32(-1)
		return
	}
	stack[0] = api.EncodeI32(write(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]), v))
}

func respond(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	status := int(api.DecodeU32(stack[0]))
	if status < 200 || status > 599 {
		panic(errABI("a status out of range"))
	}
	n := api.DecodeU32(stack[2])
	if n > maxBody {
		panic(errABI("a body longer than 1 MiB"))
	}
	cl.status, cl.body = status, append([]byte(nil), read(mod, api.DecodeU32(stack[1]), n)...)
}

// maxLogs bounds a plugin's log lines a second; past it they are dropped.
const maxLogs = 100

func logMessage(ctx context.Context, mod api.Module, stack []uint64) {
	cl := callOf(ctx)
	msg := readField(mod, api.DecodeU32(stack[1]), api.DecodeU32(stack[2]))
	if cl.m.pool.logs.Add(1) <= maxLogs {
		log.Printf("fit: wasm plugin %s: %s", cl.m.Path, msg)
	}
}

// change records op on the fields of kind: the request's at once, the
// response's for when its header is settled.
func (cl *call) change(kind uint32, op headerOp) {
	if kind == kindRequest {
		h := cl.c.Request.Header
		switch op.op {
		case opSet:
			h[op.name] = []string{op.value}
		case opAdd:
			h[op.name] = append(h[op.name], op.value)
		case opDel:
			delete(h, op.name)
		}
		return
	}
	if len(cl.ops) == maxOps {
		panic(errABI("more than 256 changes to the response's fields"))
	}
	cl.ops = append(cl.ops, op)
}

// response returns the value the response's field name has so far: as the
// plugin set it, since there is no response yet.
func (cl *call) response(name string) (string, bool) {
	v, ok := "", false
	for _, op := range cl.ops {
		if op.name != name {
			continue
		}
		switch op.op {
		case opSet:
			v, ok = op.value, true
		case opAdd:
			if !ok {
				v, ok = op.value, true
			}
		case opDel:
			v, ok = "", false
		}
	}
	return v, ok
}

// apply makes the plugin's changes to the response's fields h.
func (cl *call) apply(h stdhttp.Header) {
	for _, op := range cl.ops {
		switch op.op {
		case opSet:
			h[op.name] = []string{op.value}
		case opAdd:
			h[op.name] = append(h[op.name], op.value)
		case opDel:
			delete(h, op.name)
		}
	}
}

//go:build wasip1

// Package guest is the Go side of fit's WASM plugin ABI (see package
// plugin/wasm): a plugin registers what runs for each request with
// OnRequest, in an init function, and is built as a WASI reactor:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
//
// A plugin's main is never called. Calls come one at a time, so a plugin
// may keep state in package variables, its own for each instance fit
// runs.
package guest

import "unsafe"

// Action is what a plugin decides for a request.
type Action uint32

const (
	// Continue sends the request on, to the handler after the plugin.
	Continue Action = 0
	// Stop says the plugin answered the request, with Respond.
	Stop Action = 1
)

var onRequest func() Action

// OnRequest sets fn to run for each request.
func OnRequest(fn func() Action) { onRequest = fn }

//go:wasmexport fit_on_request
func fitOnRequest() uint32 {
	if onRequest == nil {
		return uint32(Continue)
	}
	return uint32(onRequest())
}

//go:wasmimport fit header_get
func headerGet(kind, name, nameLen, buf, bufLen uint32) int32

//go:wasmimport fit header_set
func headerSet(kind, name, nameLen, value, valueLen uint32)

//go:wasmimport fit header_add
func headerAdd(kind, name, nameLen, value, valueLen uint32)

//go:wasmimport fit header_del
func headerDel(kind, name, nameLen uint32)

//go:wasmimport fit request_get
func requestGet(field, buf, bufLen uint32) int32

//go:wasmimport fit respond
func respond(status, body, bodyLen uint32)

//go:wasmimport fit log
func logMessage(level, msg, msgLen uint32)

const (
	request  = 0
	response = 1
)

func str(s string) (uint32, uint32) {
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s)))), uint32(len(s))
}

// buf is where the host writes what the plugin reads; it grows as needed.
var buf = make([]byte, 1024)

// get calls fn, which writes what is asked for into buf, again with a
// larger buf when it did not fit.
func get(fn func(p, n uint32) int32) (string, bool) {
	for {
		n := fn(uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)))
		if n < 0 {
			return "", false
		}
		if int(n) <= len(buf) {
			return string(buf[:n]), true
		}
		buf = make([]byte, n)
	}
}

func header(kind uint32, name string) (string, bool) {
	p, n := str(name)
	return get(func(b, bn uint32) int32 { return headerGet(kind, p, n, b, bn) })
}

func field(f uint32) string {
	v, _ := get(func(b, bn uint32) int32 { return requestGet(f, b, bn) })
	return v
}

// RequestHeader returns the request's field name, and whether it has it.
func RequestHeader(name string) (string, bool) { return header(request, name) }

// SetRequestHeader sets the request's field name to value.
func SetRequestHeader(name, value string) {
	np, nn := str(name)
	vp, vn := str(value)
	headerSet(request, np, nn, vp, vn)
}

// AddRequestHeader adds value to the request's field name.
func AddRequestHeader(name, value string) {
	np, nn := str(name)
	vp, vn := str(value)
	headerAdd(request, np, nn, vp, vn)
}

// DeleteRequestHeader deletes the request's field name.
func DeleteRequestHeader(name string) {
	np, nn := str(name)
	headerDel(request, np, nn)
}

// ResponseHeader returns the response's field name as the plugin set it so
// far: the response has not begun.
func ResponseHeader(name string) (string, bool) { return header(response, name) }

// SetResponseHeader sets the response's field name to value, once its
// header is settled.
func SetResponseHeader(name, value string) {
	np, nn := str(name)
	vp, vn := str(value)
	headerSet(response, np, nn, vp, vn)
}

// AddResponseHeader adds value to the response's field name.
func AddResponseHeader(name, value string) {
	np, nn := str(name)
	vp, vn := str(value)
	headerAdd(response, np, nn, vp, vn)
}

// DeleteResponseHeader deletes the response's field name.
func DeleteResponseHeader(name string) {
	np, nn := str(name)
	headerDel(response, np, nn)
}

// Method returns the request's method.
func Method() string { return field(0) }

// Path returns the request's path.
func Path() string { return field(1) }

// Query returns the request's query, without its "?".
func Query() string { return field(2) }

// Host returns the host the request names.
func Host() string { return field(3) }

// RemoteAddr returns the client's address.
func RemoteAddr() string { return field(4) }

// Config returns the plugin's config, as the route's params set it.
func Config() string { return field(5) }

// Respond answers the request with status, body and the response fields
// the plugin set, and returns Stop, for the plugin to return.
func Respond(status int, body string) Action {
	p, n := str(body)
	respond(uint32(status), p, n)
	return Stop
}

// Log writes msg to fit's log at info; a plugin's lines past 100 a second
// are dropped.
func Log(msg string) { LogAt(LevelInfo, msg) }

// The levels of LogAt, as Go's log/slog numbers them.
const (
	LevelDebug int32 = -4
	LevelInfo  int32 = 0
	LevelWarn  int32 = 4
	LevelError int32 = 8
)

// LogAt writes msg to fit's log at level, if the plugin logger's level lets
// it; a plugin's lines past 100 a second are dropped.
func LogAt(level int32, msg string) {
	p, n := str(msg)
	logMessage(uint32(level), p, n)
}

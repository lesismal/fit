package wasm

import (
	stdhttp "net/http"
	"slices"
	"testing"

	"github.com/tetratelabs/wazero/api"
)

// TestHostProxyWasmResponse runs the host functions as the response's
// callbacks call them: on the header while proxy_on_response_headers runs,
// on the status once the header has gone, on the trailer and on the body.
func TestHostProxyWasmResponse(t *testing.T) {
	e := newHostEnv(t)
	cl := e.cl
	str := func(s string) (uint64, uint64) { return e.put(s) }
	get := func(mapType uint64, name string) (string, uint64) {
		t.Helper()
		p, n := str(name)
		r, panicked := e.call(pwGetHeaderMapValue, mapType, p, n, envPtr, envSize)
		if panicked != nil {
			t.Fatal(panicked)
		}
		if r != pwOK {
			return "", r
		}
		return e.given(), r
	}
	change := func(mapType uint64, op int, name, value string) uint64 {
		t.Helper()
		np, nn := str(name)
		vp, vn := str(value)
		r, panicked := e.call(pwChangeHeaderMap(op), mapType, np, nn, vp, vn)
		if panicked != nil {
			t.Fatal(panicked)
		}
		return r
	}
	getPairs := func(mapType uint64) [][2]string {
		t.Helper()
		r, panicked := e.call(pwGetHeaderMapPairs, mapType, envPtr, envSize)
		if panicked != nil || r != pwOK {
			t.Fatalf("pairs of %d: %d %v", mapType, r, panicked)
		}
		pairs, _ := deserializePairs([]byte(e.given()))
		return pairs
	}
	setPairs := func(mapType uint64, pairs [][2]string) uint64 {
		t.Helper()
		b := serializePairs(pairs)
		_ = e.mod.Memory().Write(envBuf, b)
		r, panicked := e.call(pwSetHeaderMapPairs, mapType, envBuf, uint64(len(b)))
		if panicked != nil {
			t.Fatal(panicked)
		}
		return r
	}

	// While proxy_on_response_headers runs, the header is the response's own.
	cl.respStatus, cl.respHeader = 201, stdhttp.Header{"X-R": {"r"}}
	if v, s := get(pwResponseHeaders, ":status"); v != "201" || s != pwOK {
		t.Errorf(":status %q %d", v, s)
	}
	if v, _ := get(pwResponseHeaders, "x-r"); v != "r" {
		t.Errorf("x-r %q", v)
	}
	if _, s := get(pwResponseHeaders, "x-none"); s != pwNotFound {
		t.Errorf("x-none %d", s)
	}
	if s := change(pwResponseHeaders, opSet, ":status", "500"); s != pwBadArgument {
		t.Errorf("setting :status: %d", s)
	}
	if s := change(pwResponseHeaders, opSet, "x-s", "1"); s != pwOK || cl.respHeader.Get("X-S") != "1" {
		t.Errorf("setting x-s: %d %v", s, cl.respHeader)
	}
	if v, ok := e.w.property([]string{"response", "code"}); !ok || v != "201" {
		t.Errorf("response.code %q %v", v, ok)
	}
	if pairs := getPairs(pwResponseHeaders); !slices.Contains(pairs, [2]string{":status", "201"}) || !slices.Contains(pairs, [2]string{"x-s", "1"}) {
		t.Errorf("the header's pairs %v", pairs)
	}
	if s := setPairs(pwResponseHeaders, [][2]string{{":status", "500"}, {"x-t", "2"}}); s != pwOK ||
		len(cl.respHeader) != 1 || cl.respHeader.Get("X-T") != "2" || cl.respStatus != 201 {
		t.Errorf("setting the header's pairs: %d %v %d", s, cl.respHeader, cl.respStatus)
	}

	// Once the header has gone only its status is left, which cannot change.
	cl.respHeader = nil
	if pairs := getPairs(pwResponseHeaders); !slices.Equal(pairs, [][2]string{{":status", "201"}}) {
		t.Errorf("the status's pairs %v", pairs)
	}
	if s := change(pwResponseHeaders, opAdd, "x-u", "3"); s != pwBadArgument {
		t.Errorf("a field past the header: %d", s)
	}
	if s := setPairs(pwResponseHeaders, [][2]string{{"x-u", "3"}}); s != pwBadArgument {
		t.Errorf("pairs past the header: %d", s)
	}

	// The trailer is there only at the end of the body.
	if s := change(pwResponseTrailers, opSet, "x-v", "4"); s != pwBadArgument {
		t.Errorf("a trailer before the end: %d", s)
	}
	if s := setPairs(pwResponseTrailers, [][2]string{{"x-v", "4"}}); s != pwBadArgument {
		t.Errorf("trailer pairs before the end: %d", s)
	}
	cl.trailer = stdhttp.Header{"X-Old": {"o"}}
	if s := change(pwResponseTrailers, opSet, ":x", "4"); s != pwBadArgument {
		t.Errorf("a pseudo-trailer: %d", s)
	}
	if s := change(pwResponseTrailers, opDel, "x-old", ""); s != pwOK || len(cl.trailer) != 0 {
		t.Errorf("removing a trailer: %d %v", s, cl.trailer)
	}
	if s := setPairs(pwResponseTrailers, [][2]string{{"x-w", "5"}}); s != pwOK || cl.trailer.Get("X-W") != "5" {
		t.Errorf("the trailer's pairs: %d %v", s, cl.trailer)
	}
	if v, _ := get(pwResponseTrailers, "x-w"); v != "5" {
		t.Errorf("x-w %q", v)
	}
	if pairs := getPairs(pwResponseTrailers); !slices.Equal(pairs, [][2]string{{"x-w", "5"}}) {
		t.Errorf("the trailer's pairs %v", pairs)
	}

	// The body is the plugin's to read and change only in its callback.
	cl.buf = []byte("body")
	for _, current := range []*call{nil, cl} {
		e.w.current = current
		if r, _ := e.call(pwGetBufferBytes, pwResponseBody, 0, 4, envPtr, envSize); r != pwNotFound {
			t.Errorf("reading the body outside its callback: %d", r)
		}
		if r, _ := e.call(pwSetBufferBytes, pwResponseBody, 0, 0, envBuf, 1); r != pwNotFound {
			t.Errorf("changing the body outside its callback: %d", r)
		}
	}
	cl.inBody = true
	if r, _ := e.call(pwGetBufferBytes, pwResponseBody, 5, 1, envPtr, envSize); r != pwBadArgument {
		t.Errorf("reading past the body: %d", r)
	}
	if r, _ := e.call(pwGetBufferBytes, pwResponseBody, 1, 100, envPtr, envSize); r != pwOK || e.given() != "ody" {
		t.Errorf("reading the body: %d %q", r, e.given())
	}
	if r, _ := e.call(pwSetBufferBytes, pwPluginConfiguration, 0, 0, envBuf, 1); r != pwNotFound {
		t.Errorf("changing the config: %d", r)
	}
}

// TestLogDisabled checks a line below the plugin logger's level is dropped
// before it is read.
func TestLogDisabled(t *testing.T) {
	e := newHostEnv(t)
	// The address is outside memory: a line read would panic.
	if _, p := e.call(logMessage, api.EncodeI32(-100), envBad, 10); p != nil {
		t.Errorf("a line below the level: %v", p)
	}
}

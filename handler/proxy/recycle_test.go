package proxy

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	fibhttp "github.com/lesismal/fib/http"
)

// TestExchangeStateIsAllItHolds checks that an exchange keeps nothing of a
// request outside exchangeState, which recycle clears in one assignment: a
// field added beside gen and the locks would carry over to the next request.
func TestExchangeStateIsAllItHolds(t *testing.T) {
	want := map[string]bool{"exchangeState": true, "gen": true, "callMu": true, "mu": true}
	typ := reflect.TypeFor[exchange]()
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; !want[name] {
			t.Errorf("exchange has %s outside exchangeState, which recycle would not clear", name)
		}
	}
}

// TestExchangeRecycleTurnsLateCallbacksAway checks that a reset exchange is
// cleared and serves a new gen, that a cancel made for the request it served
// does nothing to the next, and that one for the next does. The tests reset
// rather than recycle, which would hand the exchange they go on using to
// the requests of other tests.
func TestExchangeRecycleTurnsLateCallbacksAway(t *testing.T) {
	x := &exchange{recyclable: true, attempt: 2}
	gen := x.gen.Load()
	if !x.reset() {
		t.Fatal("reset refused a recyclable exchange")
	}
	if x.gen.Load() == gen {
		t.Fatal("reset left the gen as it was")
	}
	if x.attempt != 0 || x.recyclable {
		t.Errorf("reset left attempt %d, recyclable %v", x.attempt, x.recyclable)
	}
	x.cancel(gen, errClientGone)
	if cause := x.causeErr(); cause != nil {
		t.Errorf("a cancel for the request before set the next's cause to %v", cause)
	}
	x.cancel(x.gen.Load(), errRouteTimeout)
	if cause := x.causeErr(); !errors.Is(cause, errRouteTimeout) {
		t.Errorf("a cancel for the request served set its cause to %v", cause)
	}
}

// TestExchangeRecycleWaitsForNothing checks that reset, and so recycle,
// leaves an exchange to the collector when callMu is held, by a send still
// storing its Call, and one that is not recyclable at all, rather than
// waiting or handing on an exchange something still uses.
func TestExchangeRecycleWaitsForNothing(t *testing.T) {
	x := &exchange{recyclable: true}
	gen := x.gen.Load()
	x.callMu.Lock()
	ok := x.reset()
	x.callMu.Unlock()
	if ok || x.gen.Load() != gen || !x.recyclable {
		t.Error("reset cleared an exchange whose callMu was held")
	}
	y := &exchange{attempt: 1}
	if y.reset() || y.gen.Load() != 0 || y.attempt != 1 {
		t.Error("reset cleared an exchange that was not recyclable")
	}
}

// TestOutgoingFieldsOutliveTheExchange checks that the values outgoing puts
// in the client's header are the request's own: an exchange recycled and used
// for the next request leaves the first request's header as it was, since
// fib reads it once more as that request finishes.
func TestOutgoingFieldsOutliveTheExchange(t *testing.T) {
	p := &Proxy{}
	request := func(remote string) *fibhttp.Context {
		r := httptest.NewRequest(stdhttp.MethodGet, "http://example.com/x", nil)
		r.RemoteAddr = remote
		return fibhttp.NewStreamContext(nil, r, nil)
	}
	first := request("192.0.2.10:1000")
	x := new(exchange)
	if !p.outgoing(first, x) {
		t.Fatal("refused")
	}
	header := first.Request.Header
	forwardedFor, forwarded := header.Get("X-Forwarded-For"), header.Get("Forwarded")
	x.recyclable = true
	x.reset()
	if !p.outgoing(request("198.51.100.7:2000"), x) {
		t.Fatal("refused")
	}
	if got := header.Get("X-Forwarded-For"); got != forwardedFor || got != "192.0.2.10" {
		t.Errorf("the first request's X-Forwarded-For is %q, want %q", got, forwardedFor)
	}
	if got := header.Get("Forwarded"); got != forwarded {
		t.Errorf("the first request's Forwarded is %q, want %q", got, forwarded)
	}
}

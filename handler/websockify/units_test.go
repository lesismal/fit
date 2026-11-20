package websockify

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
)

// TestDecode decodes base64 split across frames anywhere, and refuses a
// message that does not end at a group of four or is not base64.
func TestDecode(t *testing.T) {
	want := []byte("hello, websockify")
	enc := base64.StdEncoding.EncodeToString(want)
	for _, cuts := range [][]int{{len(enc)}, {1, 2, 3, len(enc)}, {2, 6, 7, len(enc)}, {5, len(enc)}, {0, 4, len(enc)}} {
		b := &bridge{}
		var got []byte
		prev := 0
		for i, cut := range cuts {
			out, err := b.decode([]byte(enc[prev:cut]), i == len(cuts)-1)
			if err != nil {
				t.Fatalf("cuts %v: %v", cuts, err)
			}
			got = append(got, out...)
			prev = cut
		}
		if string(got) != string(want) || b.ncarry != 0 {
			t.Errorf("cuts %v: %q, carry %d", cuts, got, b.ncarry)
		}
	}
	b := &bridge{}
	if _, err := b.decode([]byte("aGV"), true); err == nil {
		t.Error("a message of 3 characters decoded")
	}
	b = &bridge{}
	if _, err := b.decode([]byte("a"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.decode([]byte("!!!"), false); err == nil {
		t.Error("a group that is not base64 decoded")
	}
	if out, err := (&bridge{}).decode(nil, true); out != nil || err != nil {
		t.Errorf("an empty last frame: %q, %v", out, err)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestDialStatus(t *testing.T) {
	for err, want := range map[error]int{
		os.ErrDeadlineExceeded:                       stdhttp.StatusGatewayTimeout,
		context.DeadlineExceeded:                     stdhttp.StatusGatewayTimeout,
		&net.OpError{Op: "dial", Err: timeoutErr{}}:  stdhttp.StatusGatewayTimeout,
		&net.OpError{Op: "dial", Err: syscall.EPIPE}: stdhttp.StatusBadGateway,
		errors.New("no"):                             stdhttp.StatusBadGateway,
	} {
		if got := dialStatus(err); got != want {
			t.Errorf("%v: %d, want %d", err, got, want)
		}
	}
}

// TestProvisionErrors refuses params that name no target, more than one,
// or one that is not a host and a port, and origins that are not origins.
func TestProvisionErrors(t *testing.T) {
	for params, want := range map[string]string{
		`{}`: "set one of address, upstream and tokens",
		`{"address": "a:1", "tokens": {"x": "b:2"}}`:     "set one of address, upstream and tokens",
		`{"address": "nohost"}`:                          "missing port",
		`{"address": "a:x"}`:                             "not a number",
		`{"address": "a:0"}`:                             "not a number",
		`{"address": ":80"}`:                             "has no host",
		`{"tokens": {"": "a:1"}}`:                        "a token is empty",
		`{"tokens": {"x": "a"}}`:                         "missing port",
		`{"upstream": "nope"}`:                           `there is no upstream named "nope"`,
		`{"address": "a:1", "origins": ["example.com"]}`: "is not an origin",
	} {
		c, err := config.Load([]byte(routeDoc(params, "")))
		if err == nil {
			_, err = runtime.Compile(c, runtime.Env{})
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", params, err, want)
		}
	}
}

// TestNoEngine answers 503 from a config compiled without an engine, as
// one only checked is.
func TestNoEngine(t *testing.T) {
	c, err := config.Load([]byte(routeDoc(`{"tokens": {"a": "127.0.0.1:1"}}`, "")))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	for _, m := range g.Modules() {
		if w, ok := m.(*Websockify); ok && (w.engine != nil || w.st.key.name != "tokens") {
			t.Fatalf("engine %v, name %q", w.engine, w.st.key.name)
		}
	}
}

func TestIsHandshake(t *testing.T) {
	for _, tc := range []struct {
		r    *stdhttp.Request
		want bool
	}{
		{&stdhttp.Request{ProtoMajor: 2, Method: "CONNECT", Header: stdhttp.Header{":protocol": {"websocket"}}}, true},
		{&stdhttp.Request{ProtoMajor: 2, Method: "GET", Header: stdhttp.Header{":protocol": {"websocket"}}}, false},
		{&stdhttp.Request{ProtoMajor: 3, Method: "CONNECT", Header: stdhttp.Header{":protocol": {"webtransport"}}}, false},
		{&stdhttp.Request{ProtoMajor: 1, Method: "GET", Header: stdhttp.Header{"Upgrade": {"h2c, WebSocket"}, "Connection": {"keep-alive, Upgrade"}}}, true},
		{&stdhttp.Request{ProtoMajor: 1, Method: "POST", Header: stdhttp.Header{"Upgrade": {"websocket"}, "Connection": {"upgrade"}}}, false},
		{&stdhttp.Request{ProtoMajor: 1, Method: "GET", Header: stdhttp.Header{"Upgrade": {"websocket"}}}, false},
	} {
		if got := isHandshake(tc.r); got != tc.want {
			t.Errorf("%s %v: %v", tc.r.Method, tc.r.Header, got)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	w := &Websockify{origins: map[string]struct{}{"https://a.example": {}}}
	if w.originAllowed(&stdhttp.Request{Header: stdhttp.Header{"Origin": {"https://a.example", "https://a.example"}}}) {
		t.Error("two origins allowed")
	}
	if !w.originAllowed(&stdhttp.Request{Header: stdhttp.Header{"Origin": {"HTTPS://A.example"}}}) {
		t.Error("an origin listed, in other case, refused")
	}
}

func TestQueryValue(t *testing.T) {
	for raw, want := range map[string]string{
		"token=a":           "a",
		"x=1&token=a%2Fb&y": "a/b",
		"tokens=a":          "",
		"token":             "",
		"token=%zz":         "",
		"":                  "",
		"token=a&token=b":   "a",
	} {
		if got := queryValue(raw, "token"); got != want {
			t.Errorf("%q: %q, want %q", raw, got, want)
		}
	}
}

// TestBridgeBeforeOpen keeps what the target sends before the WebSocket
// opens, and gives up a bridge abandoned while it dials.
func TestBridgeBeforeOpen(t *testing.T) {
	b := &bridge{}
	b.fromTarget(nil, []byte("RFB 003.008\n"))
	if string(b.pending) != "RFB 003.008\n" {
		t.Errorf("pending %q", b.pending)
	}
	b.targetClosed()
	if !b.targetEnded {
		t.Error("the target's end before the WebSocket opened was not kept")
	}
	b.abandon(errDrained)
	if !b.gone || b.cause != errDrained {
		t.Error("a bridge abandoned while it dials was not marked gone")
	}
	var h wsHandler
	h.OnMessage(nil, websocket.Binary, nil)
	(*target)(b).OnPriorityData(nil, nil)
}

// TestStatsShared keeps a name's counters while a handler has it, apart
// from another engine's.
func TestStatsShared(t *testing.T) {
	a := takeStats(statsKey{name: "x"})
	b := takeStats(statsKey{name: "x"})
	z := takeStats(statsKey{name: "a"})
	if got := AllStats(nil); len(got) != 2 || got[0].Name != "a" {
		t.Errorf("stats %+v, want a first", got)
	}
	giveStats(z)
	other := takeStats(statsKey{name: "y", engine: clientEngine(t)})
	if a != b {
		t.Fatal("one name, two counters")
	}
	a.bridged.Add(3)
	giveStats(a)
	if got := AllStats(nil); len(got) != 1 || got[0].Bridged != 3 {
		t.Errorf("stats %+v", got)
	}
	giveStats(b)
	giveStats(other)
	if got := AllStats(nil); len(got) != 0 {
		t.Errorf("stats kept after the last handler: %+v", got)
	}
}

// TestClientGoneWhileDialing gives up a bridge whose client leaves while
// the target is dialed, here one that never answers: the request is let go
// once the dial ends, and nothing is bridged. Where the network refuses the
// address at once, the dial fails first, which is a 502 nobody hears.
func TestClientGoneWhileDialing(t *testing.T) {
	f := startFit(t, routeDoc(`{"address": "192.0.2.1:9", "connect_timeout": "500ms", "name": "blackhole"}`, ""))
	c, err := net.DialTimeout("tcp", f.addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(c, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	time.Sleep(50 * time.Millisecond)
	c.Close()
	eventually(t, 5*time.Second, "the bridge to be given up", func() bool { return f.handler().conns.Load() == 0 })
	if s := f.stats("blackhole"); s.Bridged != 0 {
		t.Errorf("stats %+v", s)
	}
	// A client that waits hears 504, or 502 where the network refuses.
	_, resp, err := dialWS(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	if err == nil || resp == nil || resp.StatusCode != stdhttp.StatusGatewayTimeout && resp.StatusCode != stdhttp.StatusBadGateway {
		t.Errorf("%v, want 504", err)
	}
}

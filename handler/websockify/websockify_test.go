package websockify

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/observe"
)

// TestEcho bridges WebSockets to an echo server: a small message, a large
// one of 4 MiB, many at once, with the binary subprotocol and with none.
func TestEcho(t *testing.T) {
	target := backend(t, echo)
	f := startFit(t, routeDoc(`{"address": `+q(target)+`, "max_frame_bytes": 8388608}`, ""))
	engine := clientEngine(t)
	url := "ws://" + f.addr + "/websockify"

	c := mustDial(t, engine, url, nil, "binary")
	if p := c.conn.Subprotocol(); p != "binary" {
		t.Errorf("subprotocol %q, want binary", p)
	}
	if err := c.conn.WriteMessage(websocket.Binary, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got := c.read(5, false); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
	large := pattern(4<<20, 1)
	if err := c.conn.WriteMessage(websocket.Binary, large); err != nil {
		t.Fatal(err)
	}
	if got := c.read(len(large), false); sha(got) != sha(large) {
		t.Fatal("the large message came back otherwise")
	}

	// No subprotocol offered: binary, without one chosen.
	plain := mustDial(t, engine, url, nil)
	if p := plain.conn.Subprotocol(); p != "" {
		t.Errorf("subprotocol %q, want none", p)
	}
	errs := make(chan error, 20)
	for i := range 20 {
		go func() {
			c, _, err := dialWS(t, engine, url, nil)
			if err != nil {
				errs <- err
				return
			}
			want := pattern(10000+i, byte(i))
			if err := c.conn.WriteMessage(websocket.Binary, want); err != nil {
				errs <- err
				return
			}
			var got []byte
			for len(got) < len(want) {
				select {
				case m := <-c.msgs:
					got = append(got, m.data...)
				case <-time.After(10 * time.Second):
					errs <- fmt.Errorf("connection %d: no echo", i)
					return
				}
			}
			if !bytes.Equal(got, want) {
				errs <- fmt.Errorf("connection %d: echoed otherwise", i)
				return
			}
			errs <- nil
		}()
	}
	for range 20 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
	s := f.stats(target)
	if s.Bridged != 22 || s.ToTarget < uint64(len(large)) || s.ToClient < uint64(len(large)) {
		t.Errorf("stats %+v", s)
	}
}

// TestFrameTooLarge closes a WebSocket whose frame passes max_frame_bytes
// with 1009.
func TestFrameTooLarge(t *testing.T) {
	f := startFit(t, routeDoc(`{"address": `+q(backend(t, echo))+`, "max_frame_bytes": 1024}`, ""))
	c := mustDial(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	_ = c.conn.WriteMessage(websocket.Binary, make([]byte, 2048))
	if cl := c.waitClosed(10 * time.Second); cl.code != websocket.CloseMessageTooBig {
		t.Fatalf("closed with %d, want 1009", cl.code)
	}
}

// TestBase64 speaks base64 both ways: text messages of base64 go to the
// target as bytes, and the target's bytes come back as text of base64. A
// message that is not base64 closes the WebSocket with 1007.
func TestBase64(t *testing.T) {
	f := startFit(t, routeDoc(`{"address": `+q(backend(t, echo))+`}`, ""))
	engine := clientEngine(t)
	c := mustDial(t, engine, "ws://"+f.addr+"/", nil, "base64", "binary")
	if p := c.conn.Subprotocol(); p != "base64" {
		t.Fatalf("subprotocol %q, want base64", p)
	}
	for _, want := range [][]byte{[]byte("hello"), pattern(300<<10, 2), {0}} {
		if err := c.conn.WriteMessage(websocket.Text, []byte(base64.StdEncoding.EncodeToString(want))); err != nil {
			t.Fatal(err)
		}
		if got := c.read(len(want), true); !bytes.Equal(got, want) {
			t.Fatalf("%d bytes came back otherwise", len(want))
		}
	}
	if err := c.conn.WriteMessage(websocket.Text, []byte("not base64!")); err != nil {
		t.Fatal(err)
	}
	if cl := c.waitClosed(10 * time.Second); cl.code != websocket.CloseInvalidPayload {
		t.Fatalf("closed with %d, want 1007", cl.code)
	}
}

// TestTokens picks the target by a token in the query: one listed is
// bridged, one missing or not listed is refused 403, and one whose target
// is down is answered 502.
func TestTokens(t *testing.T) {
	good := backend(t, func(c net.Conn) { _, _ = c.Write([]byte("good")) })
	other := backend(t, func(c net.Conn) { _, _ = c.Write([]byte("other")) })
	f := startFit(t, routeDoc(`{"tokens": {"a": `+q(good)+`, "b c": `+q(other)+`, "down": `+q(refused(t))+`},
		"token_param": "t", "name": "desks"}`, ""))
	engine := clientEngine(t)
	base := "ws://" + f.addr + "/websockify?x=1&t="
	if got := mustDial(t, engine, base+"a", nil).read(4, false); string(got) != "good" {
		t.Errorf("token a: %q", got)
	}
	if got := mustDial(t, engine, base+"b%20c", nil).read(5, false); string(got) != "other" {
		t.Errorf("token b c: %q", got)
	}
	for _, url := range []string{base + "zzz", "ws://" + f.addr + "/websockify", base + "%zz", base} {
		_, resp, err := dialWS(t, engine, url, nil)
		if err == nil || resp == nil || resp.StatusCode != stdhttp.StatusForbidden {
			t.Errorf("%s: %v, want 403", url, err)
		}
	}
	_, resp, err := dialWS(t, engine, base+"down", nil)
	if err == nil || resp == nil || resp.StatusCode != stdhttp.StatusBadGateway {
		t.Errorf("token down: %v, want 502", err)
	}
	s := f.stats("desks")
	if s.Bridged != 2 || s.Refused != 4 || s.Failed != 1 {
		t.Errorf("stats %+v", s)
	}
}

// TestTargetDown answers 502 when the target refuses the connection; an
// upstream's next address is tried, and 502 only when all are down.
func TestTargetDown(t *testing.T) {
	engine := clientEngine(t)
	t.Run("address", func(t *testing.T) {
		f := startFit(t, routeDoc(`{"address": `+q(refused(t))+`}`, ""))
		_, resp, err := dialWS(t, engine, "ws://"+f.addr+"/", nil)
		if err == nil || resp == nil || resp.StatusCode != stdhttp.StatusBadGateway {
			t.Fatalf("%v, want 502", err)
		}
	})
	t.Run("upstream", func(t *testing.T) {
		up := backend(t, func(c net.Conn) { _, _ = c.Write([]byte("up")) })
		f := startFit(t, routeDoc(`{"upstream": "vnc"}`,
			`[{"name": "vnc", "addresses": [`+q(refused(t))+`, `+q(up)+`], "max_fails": 0}]`))
		for range 4 {
			if got := mustDial(t, engine, "ws://"+f.addr+"/", nil).read(2, false); string(got) != "up" {
				t.Fatalf("got %q", got)
			}
		}
	})
	t.Run("upstream down", func(t *testing.T) {
		f := startFit(t, routeDoc(`{"upstream": "vnc"}`,
			`[{"name": "vnc", "addresses": [`+q(refused(t))+`, `+q(refused(t))+`]}]`))
		_, resp, err := dialWS(t, engine, "ws://"+f.addr+"/", nil)
		if err == nil || resp == nil || resp.StatusCode != stdhttp.StatusBadGateway {
			t.Fatalf("%v, want 502", err)
		}
		if s := f.stats("vnc"); s.Failed != 1 {
			t.Errorf("stats %+v", s)
		}
	})
}

// TestSlowClient has the client stop reading while the target sends: fit
// holds the target back rather than taking what it sends, and the client
// then gets all of it.
func TestSlowClient(t *testing.T) {
	const size = 64 << 20
	data := pattern(size, 3)
	var sent atomic.Int64
	target := backend(t, func(c net.Conn) {
		if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
			return
		}
		for rest := data; len(rest) > 0; {
			n, err := c.Write(rest[:min(len(rest), 64<<10)])
			sent.Add(int64(n))
			if err != nil {
				return
			}
			rest = rest[n:]
		}
	})
	f := startFit(t, routeDoc(`{"address": `+q(target)+`}`, ""))
	c := mustDial(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	c.conn.HoldReads(true)
	if err := c.conn.WriteMessage(websocket.Binary, []byte("go")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n := sent.Load(); n >= size/2 {
		t.Fatalf("the target sent %d bytes to a client that read none; want them held back", n)
	}
	c.conn.HoldReads(false)
	h := sha256.New()
	for got := 0; got < size; {
		b := c.read(min(size-got, 1<<20), false)
		h.Write(b)
		got += len(b)
	}
	if !bytes.Equal(h.Sum(nil), func() []byte { s := sha(data); return s[:] }()) {
		t.Fatal("the client got other bytes than the target sent")
	}
}

// TestSlowTarget has the target stop reading while the client sends: fit
// holds the client back, and the target then gets all of it.
func TestSlowTarget(t *testing.T) {
	const size = 64 << 20
	data := pattern(size, 5)
	read := make(chan error, 1)
	start := make(chan struct{})
	target := backend(t, func(c net.Conn) {
		<-start
		h := sha256.New()
		n, err := io.CopyN(h, c, size)
		if s := sha(data); err == nil && !bytes.Equal(h.Sum(nil), s[:]) {
			err = fmt.Errorf("the target got %d bytes, not those sent", n)
		}
		read <- err
	})
	f := startFit(t, routeDoc(`{"address": `+q(target)+`}`, ""))
	c := mustDial(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	var sent atomic.Int64
	go func() {
		// Paced by what waits to go to fit, as a browser's bufferedAmount
		// paces a careful client.
		writable := make(chan struct{}, 1)
		for rest := data; len(rest) > 0; {
			n := min(len(rest), 32<<10)
			above, err := c.conn.WriteMessageNoWait(websocket.Binary, rest[:n])
			if err != nil {
				return
			}
			sent.Add(int64(n))
			rest = rest[n:]
			if above && c.conn.OnWritable(func(error) { writable <- struct{}{} }) {
				<-writable
			}
		}
	}()
	time.Sleep(time.Second)
	if n := sent.Load(); n >= size/2 {
		t.Fatalf("the client sent %d bytes to a target that read none; want them held back", n)
	}
	close(start)
	select {
	case err := <-read:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the target did not get it all")
	}
}

// TestHangUp ends one side when the other does: the client gets what the
// target sent before it closed and a close 1000, and the target hears the
// client close.
func TestHangUp(t *testing.T) {
	engine := clientEngine(t)
	t.Run("target", func(t *testing.T) {
		f := startFit(t, routeDoc(`{"address": `+q(backend(t, func(c net.Conn) {
			_, _ = c.Write(pattern(1<<20, 9))
		}))+`}`, ""))
		c := mustDial(t, engine, "ws://"+f.addr+"/", nil)
		if got := c.read(1<<20, false); !bytes.Equal(got, pattern(1<<20, 9)) {
			t.Fatal("the target's bytes came otherwise")
		}
		if cl := c.waitClosed(10 * time.Second); cl.code != websocket.CloseNormal {
			t.Fatalf("closed with %d, want 1000", cl.code)
		}
		eventually(t, 5*time.Second, "the bridge to end", func() bool { return f.handler().conns.Load() == 0 })
	})
	t.Run("client", func(t *testing.T) {
		gone := make(chan error, 1)
		f := startFit(t, routeDoc(`{"address": `+q(backend(t, func(c net.Conn) {
			_, err := io.Copy(io.Discard, c)
			gone <- err
		}))+`}`, ""))
		c := mustDial(t, engine, "ws://"+f.addr+"/", nil)
		_ = c.conn.WriteMessage(websocket.Binary, []byte("bye"))
		_ = c.conn.Close(websocket.CloseNormal, "")
		select {
		case <-gone:
		case <-time.After(10 * time.Second):
			t.Fatal("the target did not hear the client close")
		}
		eventually(t, 5*time.Second, "the bridge to end", func() bool { return f.handler().conns.Load() == 0 })
		if s := f.stats(f.handler().Address); s.Open != 0 || s.Bridged != 1 {
			t.Errorf("stats %+v", s)
		}
	})
}

// TestIdleTimeout closes a bridge that carried nothing for idle_timeout with
// 1001, and one whose client does not take the close at once the next time.
func TestIdleTimeout(t *testing.T) {
	f := startFit(t, routeDoc(`{"address": `+q(backend(t, echo))+`, "idle_timeout": "300ms"}`, ""))
	engine := clientEngine(t)
	c := mustDial(t, engine, "ws://"+f.addr+"/", nil)
	// Kept busy past the timeout first.
	for range 12 {
		_ = c.conn.WriteMessage(websocket.Binary, []byte("x"))
		c.read(1, false)
		time.Sleep(50 * time.Millisecond)
	}
	if cl := c.waitClosed(5 * time.Second); cl.code != websocket.CloseGoingAway {
		t.Fatalf("closed with %d, want 1001", cl.code)
	}
	deaf := mustDial(t, engine, "ws://"+f.addr+"/", nil)
	deaf.conn.HoldReads(true)
	eventually(t, 5*time.Second, "the deaf bridge to end", func() bool { return f.handler().conns.Load() == 0 })
}

// TestIdleTimeoutStuck aborts, the second time the idle timeout passes, a
// bridge whose close frame cannot go: its client stopped reading behind
// more than the sockets hold.
func TestIdleTimeoutStuck(t *testing.T) {
	flood := backend(t, func(c net.Conn) { _, _ = c.Write(make([]byte, 16<<20)) })
	f := startFit(t, routeDoc(`{"address": `+q(flood)+`, "idle_timeout": "300ms"}`, ""))
	c := mustDial(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	c.conn.HoldReads(true)
	eventually(t, 10*time.Second, "the stuck bridge to end", func() bool { return f.handler().conns.Load() == 0 })
}

// TestMaxConns answers 503 past max_conns, and bridges again once one has
// ended.
func TestMaxConns(t *testing.T) {
	f := startFit(t, routeDoc(`{"address": `+q(backend(t, echo))+`, "max_conns": 1, "name": "one"}`, ""))
	engine := clientEngine(t)
	c := mustDial(t, engine, "ws://"+f.addr+"/", nil)
	_, resp, err := dialWS(t, engine, "ws://"+f.addr+"/", nil)
	if err == nil || resp == nil || resp.StatusCode != stdhttp.StatusServiceUnavailable {
		t.Fatalf("%v, want 503", err)
	}
	_ = c.conn.Close(websocket.CloseNormal, "")
	eventually(t, 5*time.Second, "the bridge to end", func() bool { return f.handler().conns.Load() == 0 })
	mustDial(t, engine, "ws://"+f.addr+"/", nil)
	if s := f.stats("one"); s.Busy != 1 || s.Bridged != 2 {
		t.Errorf("stats %+v", s)
	}
}

// TestOrigins lets pages of the origins listed open the WebSocket, of the
// request's host when none are, and of any with "*"; a request without an
// Origin always.
func TestOrigins(t *testing.T) {
	target := backend(t, echo)
	engine := clientEngine(t)
	try := func(t *testing.T, f *fit, origin string, want bool) {
		t.Helper()
		var h stdhttp.Header
		if origin != "" {
			h = stdhttp.Header{"Origin": {origin}}
		}
		_, resp, err := dialWS(t, engine, "ws://"+f.addr+"/", h)
		switch {
		case want && err != nil:
			t.Errorf("origin %q refused: %v", origin, err)
		case !want && (err == nil || resp == nil || resp.StatusCode != stdhttp.StatusForbidden):
			t.Errorf("origin %q: %v, want 403", origin, err)
		}
	}
	t.Run("listed", func(t *testing.T) {
		f := startFit(t, routeDoc(`{"address": `+q(target)+`, "origins": ["https://Good.example/", "http://other.example"]}`, ""))
		try(t, f, "https://good.example", true)
		try(t, f, "http://other.example", true)
		try(t, f, "https://evil.example", false)
		try(t, f, "", true)
	})
	t.Run("same", func(t *testing.T) {
		f := startFit(t, routeDoc(`{"address": `+q(target)+`}`, ""))
		try(t, f, "http://"+f.addr, true)
		try(t, f, "https://evil.example", false)
	})
	t.Run("any", func(t *testing.T) {
		f := startFit(t, routeDoc(`{"address": `+q(target)+`, "origins": ["*"]}`, ""))
		try(t, f, "https://evil.example", true)
	})
}

// TestNotAHandshake answers a request that does not ask for a WebSocket 426,
// and one that asks for it amiss, found only once the target is dialed, as
// fib's handshake checks find it.
func TestNotAHandshake(t *testing.T) {
	gone := make(chan struct{}, 1)
	f := startFit(t, routeDoc(`{"address": `+q(backend(t, func(c net.Conn) {
		_, _ = io.Copy(io.Discard, c)
		gone <- struct{}{}
	}))+`, "name": "nh"}`, ""))
	head := rawRequest(t, f.addr, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	if !strings.HasPrefix(head, "HTTP/1.1 426") || !strings.Contains(head, "Upgrade: websocket") {
		t.Errorf("a plain GET: %q, want 426", head)
	}
	head = rawRequest(t, f.addr, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 8\r\n\r\n")
	if !strings.HasPrefix(head, "HTTP/1.1 400") {
		t.Errorf("a handshake without a key: %q, want 400", head)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the target's connection of a refused handshake was kept")
	}
	eventually(t, 5*time.Second, "the bridge to end", func() bool { return f.handler().conns.Load() == 0 })
	if s := f.stats("nh"); s.Refused != 2 || s.Bridged != 0 {
		t.Errorf("stats %+v", s)
	}
}

// TestDrain closes the bridges of a config swapped out once its drain
// timeout passes, and the new config's handler keeps the counters.
func TestDrain(t *testing.T) {
	doc := routeDoc(`{"address": `+q(backend(t, echo))+`, "name": "d"}`, "")
	f := startFit(t, doc)
	f.rt.SetDrainTimeout(100 * time.Millisecond)
	c := mustDial(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	_ = c.conn.WriteMessage(websocket.Binary, []byte("x"))
	c.read(1, false)
	f.reload(doc)
	c.waitClosed(5 * time.Second)
	eventually(t, 5*time.Second, "the drained bridge to end", func() bool { return f.stats("d").Open == 0 })
	if s := f.stats("d"); s.Bridged != 1 {
		t.Errorf("stats after the reload %+v", s)
	}
}

// TestMetrics writes the handlers' metrics.
func TestMetrics(t *testing.T) {
	f := startFit(t, routeDoc(`{"address": `+q(backend(t, echo))+`, "name": "m"}`, ""))
	c := mustDial(t, clientEngine(t), "ws://"+f.addr+"/", nil)
	_ = c.conn.WriteMessage(websocket.Binary, []byte("hello"))
	c.read(5, false)
	var w observe.TextWriter
	WriteMetrics(&w, f.engine)
	out := string(w.B)
	for _, want := range []string{
		`fit_websockify_connections_open{bridge="m"} 1`,
		`fit_websockify_requests_total{bridge="m",result="bridged"} 1`,
		`fit_websockify_bytes_total{bridge="m",direction="to_target"} 5`,
		`fit_websockify_bytes_total{bridge="m",direction="to_client"} 5`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the metrics lack %s:\n%s", want, out)
		}
	}
	var none observe.TextWriter
	WriteMetrics(&none, nil)
	if len(none.B) != 0 {
		t.Errorf("metrics of an engine without handlers: %s", none.B)
	}
}

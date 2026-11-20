package websockify

import (
	stdhttp "net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
	"unsafe"

	fib "github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"
)

// fibEcho is a fib engine echoing what each connection sends, so that a
// target costs no goroutine; it returns the engine's address.
func fibEcho(t testing.TB) string {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig(), fib.Binding{Network: "tcp", Addr: "127.0.0.1:0", Handler: echoHandler{}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.Run() }()
	t.Cleanup(func() {
		engine.Stop()
		<-done
		_ = engine.Close()
	})
	addrs, err := engine.BindingAddrs(0)
	if err != nil {
		t.Fatal(err)
	}
	return addrs[0].String()
}

type echoHandler struct{}

func (echoHandler) OnOpen(*fib.Connection)                 {}
func (echoHandler) OnData(c *fib.Connection, data []byte)  { _ = c.Send(data) }
func (echoHandler) OnPriorityData(*fib.Connection, []byte) {}
func (echoHandler) OnClose(*fib.Connection, error)         {}

// heap is the Go heap in use and the goroutine stacks, once collected.
func heap() int64 {
	var m runtime.MemStats
	for range 3 {
		runtime.GC()
	}
	runtime.ReadMemStats(&m)
	return int64(m.HeapAlloc + m.StackInuse)
}

// TestIdleMemory measures what an idle bridged WebSocket costs: n bridges,
// each a client's WebSocket through fit to a fib echo target, every
// endpoint in this process, against n plain pairs of the same endpoints
// without fit between them, a WebSocket of fib's own server and a TCP
// connection dialed through fib to the target. Run with
// FIT_WEBSOCKIFY_MEM=1 and -v; it measures, it does not check.
func TestIdleMemory(t *testing.T) {
	if os.Getenv("FIT_WEBSOCKIFY_MEM") == "" {
		t.Skip("set FIT_WEBSOCKIFY_MEM=1 to measure")
	}
	const n = 1000
	if os.Getenv("FIT_WEBSOCKIFY_MEMPROFILE") != "" {
		runtime.MemProfileRate = 1
	}
	target := fibEcho(t)
	f := startFit(t, routeDoc(`{"address": `+q(target)+`, "max_conns": 4000}`, ""))
	engine := clientEngine(t)
	url := "ws://" + f.addr + "/"
	open := func(url string) []*websocket.Connection {
		d := websocket.NewDialer(engine, websocket.DefaultDialerConfig())
		conns := make([]*websocket.Connection, 0, n)
		for range n {
			c, _, err := d.Go(url, nil, websocket.HandlerFuncs{}).Wait()
			if err != nil {
				t.Fatal(err)
			}
			if err := c.WriteMessage(websocket.Binary, []byte("x")); err != nil {
				t.Fatal(err)
			}
			conns = append(conns, c)
		}
		time.Sleep(500 * time.Millisecond)
		return conns
	}
	closeAll := func(conns []*websocket.Connection) {
		for _, c := range conns {
			c.Abort(nil)
		}
	}

	// Warm the pools and the engines' tables first.
	closeAll(open(url))
	eventually(t, 10*time.Second, "the bridges to end", func() bool { return f.handler().conns.Load() == 0 })
	before := heap()
	bridges := open(url)
	bridged := heap() - before
	if p := os.Getenv("FIT_WEBSOCKIFY_MEMPROFILE"); p != "" {
		if out, err := os.Create(p); err == nil {
			_ = pprof.WriteHeapProfile(out)
			out.Close()
		}
	}
	closeAll(bridges)
	eventually(t, 10*time.Second, "the bridges to end", func() bool { return f.handler().conns.Load() == 0 })

	// The same endpoints without fit: a WebSocket to fib's own server, and
	// a TCP connection dialed to the target.
	ws, err := fib.New(fib.DefaultConfig(), fib.Binding{Network: "tcp", Addr: "127.0.0.1:0",
		Handler: websocket.NewHandlerWithConfig(websocket.Config{CheckOrigin: func(*stdhttp.Request) bool { return true }}, websocket.HandlerFuncs{})})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = ws.Run() }()
	defer func() { ws.Stop(); _ = ws.Close() }()
	addrs, _ := ws.BindingAddrs(0)
	closeAll(open("ws://" + addrs[0].String() + "/"))
	before = heap()
	plain := open("ws://" + addrs[0].String() + "/")
	tcp := make([]*fib.Connection, 0, n)
	opened := make(chan *fib.Connection, n)
	for range n {
		if err := f.engine.DialWithHandler("tcp", target, 5*time.Second, echoHandler{}, func(c *fib.Connection, err error) {
			if err != nil {
				t.Error(err)
			}
			opened <- c
		}); err != nil {
			t.Fatal(err)
		}
	}
	for range n {
		tcp = append(tcp, <-opened)
	}
	time.Sleep(500 * time.Millisecond)
	pairs := heap() - before
	closeAll(plain)
	for _, c := range tcp {
		_ = c.Close()
	}
	t.Logf("%d idle bridges: %d bytes each, every endpoint in this process; plain pairs %d bytes each; the bridge adds %d bytes each",
		n, bridged/n, pairs/n, (bridged-pairs)/n)
	t.Logf("sizeof bridge %d", bridgeSize)
}

var bridgeSize = unsafe.Sizeof(bridge{})

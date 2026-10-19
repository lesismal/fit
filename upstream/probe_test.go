package upstream

import (
	"encoding/pem"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// healthy is a backend whose /healthz answers 200 or, once failing is set,
// 503, and counts the probes it gets.
type healthy struct {
	*backend
	failing atomic.Bool
	probes  atomic.Int64
	host    atomic.Value // the Host field of the last probe
}

func newHealthy(t *testing.T, name string) *healthy {
	t.Helper()
	h := &healthy{}
	h.backend = newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if r.URL.Path != "/healthz" {
			hello(name)(w, r)
			return
		}
		h.probes.Add(1)
		h.host.Store(r.Host)
		if r.Header.Get("User-Agent") != "fit-health-check" {
			w.WriteHeader(400)
			return
		}
		if h.failing.Load() {
			w.WriteHeader(503)
		}
	})
	return h
}

// The probes in these tests: every 50ms, plus up to 10ms of jitter, each
// with 40ms to answer; two in a row to leave rotation or rejoin it.
const (
	probeCheck = `"health_check": {"path": "/healthz", "interval": "50ms", "timeout": "40ms", "jitter": "10ms", "fails": 2, "passes": 2}`
	// probeWindow is how long leaving or rejoining may take: two probes'
	// waits and the second's timeout, with room for a loaded test host.
	probeWindow = 2 * (2*(50+10) + 40) * time.Millisecond
)

// waitDown waits until b's probes have it down, or up, and returns how
// long that took.
func waitDown(t *testing.T, b *Backend, down bool) time.Duration {
	t.Helper()
	began := time.Now()
	for b.ProbedDown() != down {
		if time.Since(began) > 5*time.Second {
			t.Fatalf("%s: ProbedDown stayed %v", b.Address, !down)
		}
		time.Sleep(5 * time.Millisecond)
	}
	return time.Since(began)
}

func TestProbeLeaveAndRejoin(t *testing.T) {
	good, flaky := newHealthy(t, "g"), newHealthy(t, "f")
	p := newPool(t, `{"addresses": [%s], `+probeCheck+`}`, good.backend, flaky.backend)
	fb := p.Backends()[1]
	seen := func(n int) map[string]int {
		got := map[string]int{}
		for range n {
			r := do(t, p, "/")
			if r.err != nil {
				t.Fatal(r.err)
			}
			got[r.body[:1]]++
		}
		return got
	}

	// Up at the start, and kept up by its probes.
	time.Sleep(150 * time.Millisecond)
	if fb.ProbedDown() || flaky.probes.Load() == 0 {
		t.Fatalf("down %v after %d probes", fb.ProbedDown(), flaky.probes.Load())
	}
	if got := seen(10); got["g"] != 5 || got["f"] != 5 {
		t.Fatalf("both up: %v", got)
	}

	flaky.failing.Store(true)
	if took := waitDown(t, fb, true); took > probeWindow {
		t.Errorf("left rotation after %v, past %v", took, probeWindow)
	}
	if !fb.Down() {
		t.Fatal("probed down, but Down is false")
	}
	if got := seen(10); got["g"] != 10 {
		t.Fatalf("flaky probed down: %v", got)
	}

	flaky.failing.Store(false)
	if took := waitDown(t, fb, false); took > probeWindow {
		t.Errorf("rejoined after %v, past %v", took, probeWindow)
	}
	if got := seen(10); got["g"] != 5 || got["f"] != 5 {
		t.Fatalf("after rejoining: %v", got)
	}
	if good.Backend(p).ProbedDown() {
		t.Fatal("the good backend was probed down")
	}
}

// Backend returns h's backend in p.
func (h *healthy) Backend(p *Pool) *Backend {
	for _, b := range p.Backends() {
		if b.Address == h.addr {
			return b
		}
	}
	return nil
}

// TestProbeOneFailure: a single failed probe, under fails 2, keeps the
// backend in rotation.
func TestProbeOneFailure(t *testing.T) {
	var n atomic.Int64
	b := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		if n.Add(1) == 2 {
			w.WriteHeader(500)
		}
	})
	p := newPool(t, `{"addresses": [%s], `+probeCheck+`}`, b)
	time.Sleep(400 * time.Millisecond)
	if n.Load() < 4 || p.Backends()[0].ProbedDown() {
		t.Fatalf("after %d probes, one failed: down %v", n.Load(), p.Backends()[0].ProbedDown())
	}
}

func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	srv := httptest.NewUnstartedServer(hello("t"))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	c, err := decode(t, `{"addresses": ["`+addr+`"], "health_check": {"type": "tcp", "interval": "50ms", "timeout": "40ms", "jitter": "10ms"}}`)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, startEngine(t), c)
	defer p.Close()
	b := p.Backends()[0]
	time.Sleep(150 * time.Millisecond)
	if b.ProbedDown() {
		t.Fatal("down while listening")
	}

	srv.Close() // the port now refuses connections
	if took := waitDown(t, b, true); took > probeWindow {
		t.Errorf("left rotation after %v", took)
	}
	ln, err = net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("the port did not come back: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if took := waitDown(t, b, false); took > probeWindow {
		t.Errorf("rejoined after %v", took)
	}
}

// TestProbeSlowAndCut: a backend that answers its probes too late, one that
// takes the connection and never answers, and one that cuts the connection
// midway are all taken out of rotation; one that answers with a large body
// is kept, its body not waited for.
func TestProbeSlowAndCut(t *testing.T) {
	slow := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		select {
		case <-time.After(200 * time.Millisecond):
		case <-r.Context().Done():
		}
	})
	cut := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		conn, buf, err := stdhttp.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100\r\n")
		_ = buf.Flush()
		conn.Close()
	})
	large := newBackend(t, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Set("Content-Length", "1073741824") // 1 GiB, of which little is sent
		chunk := []byte(strings.Repeat("x", 64<<10))
		for range 16 << 10 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	go func() {
		var held []net.Conn
		defer func() {
			for _, c := range held {
				c.Close()
			}
		}()
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			held = append(held, c) // read nothing, answer nothing
		}
	}()

	c, err := decode(t, `{"addresses": ["`+slow.addr+`", "`+cut.addr+`", "`+large.addr+`", "`+silent.Addr().String()+`"], `+probeCheck+`}`)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, startEngine(t), c)
	defer p.Close()
	bs := p.Backends()
	for _, b := range []*Backend{bs[0], bs[1], bs[3]} {
		if took := waitDown(t, b, true); took > probeWindow {
			t.Errorf("%s left rotation after %v", b.Address, took)
		}
	}
	time.Sleep(300 * time.Millisecond)
	if bs[2].ProbedDown() {
		t.Fatal("the backend with a large body was probed down")
	}
}

func TestProbeExpectStatusHostPort(t *testing.T) {
	served := newHealthy(t, "s")
	probed := newHealthy(t, "p")
	_, port, _ := net.SplitHostPort(probed.addr)
	p := newPool(t, `{"addresses": [%s], "health_check": {"path": "/healthz", "host": "health.example.com", "port": `+port+
		`, "expect_status": [204], "interval": "50ms", "timeout": "40ms", "jitter": "10ms", "fails": 1}}`, served.backend)
	// The probes go to the check's port, with its Host field, and 200 is
	// not what it expects.
	waitDown(t, p.Backends()[0], true)
	if served.probes.Load() != 0 || probed.probes.Load() == 0 || probed.host.Load() != "health.example.com" {
		t.Fatalf("probes: %d to the address, %d to the port, Host %v", served.probes.Load(), probed.probes.Load(), probed.host.Load())
	}
}

// TestProbesStop: a closed pool sends no more probes.
func TestProbesStop(t *testing.T) {
	h := newHealthy(t, "h")
	c, err := decode(t, `{"addresses": ["`+h.addr+`"], `+probeCheck+`}`)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, startEngine(t), c)
	time.Sleep(150 * time.Millisecond)
	p.Close()
	p.Close()
	time.Sleep(20 * time.Millisecond) // a probe under way finishes
	n := h.probes.Load()
	time.Sleep(200 * time.Millisecond)
	if n == 0 || h.probes.Load() != n {
		t.Fatalf("%d probes before Close, %d after", n, h.probes.Load()-n)
	}
}

// TestProbedAndPassive: probes and max_fails take a backend out each on
// their own account.
func TestProbedAndPassive(t *testing.T) {
	b := backends(1)[0]
	b.setProbedDown(true)
	b.succeeded()
	if !b.Down() {
		t.Fatal("a request's success put a probed-down backend back")
	}
	b.failed(1, time.Hour)
	b.setProbedDown(false)
	if !b.Down() {
		t.Fatal("passing probes put back a backend max_fails took out")
	}
	b.succeeded()
	if b.Down() {
		t.Fatal("down after a success with probes passing")
	}
}

// TestProbeConnectionsNotKept: each probe opens a connection of its own and
// closes it.
func TestProbeConnectionsNotKept(t *testing.T) {
	h := newHealthy(t, "h")
	newPool(t, `{"addresses": [%s], `+probeCheck+`}`, h.backend)
	time.Sleep(300 * time.Millisecond)
	if h.open.Load() > 2 || h.opened.Load() < 3 {
		t.Fatalf("%d connections open, %d opened", h.open.Load(), h.opened.Load())
	}
}

// TestProbeProtocols: http probes speak the upstream's protocol, h2c and h2
// (with its CA file) alike.
func TestProbeProtocols(t *testing.T) {
	for _, protocol := range []string{"h2c", "h2"} {
		t.Run(protocol, func(t *testing.T) {
			var proto atomic.Value
			srv := httptest.NewUnstartedServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				proto.Store(r.Proto)
			}))
			extra := ""
			if protocol == "h2c" {
				srv.Config.Protocols = new(stdhttp.Protocols)
				srv.Config.Protocols.SetUnencryptedHTTP2(true)
				srv.Start()
			} else {
				srv.EnableHTTP2 = true
				srv.StartTLS()
				ca := filepath.Join(t.TempDir(), "ca.pem")
				if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o644); err != nil {
					t.Fatal(err)
				}
				extra = `, "tls_ca_file": "` + ca + `", "tls_server_name": "example.com"`
			}
			defer srv.Close()
			c, err := decode(t, `{"addresses": ["`+srv.Listener.Addr().String()+`"], "protocol": "`+protocol+`"`+extra+`, `+probeCheck+`}`)
			if err != nil {
				t.Fatal(err)
			}
			p := mustNew(t, startEngine(t), c)
			defer p.Close()
			time.Sleep(200 * time.Millisecond)
			if p.Backends()[0].ProbedDown() || proto.Load() != "HTTP/2.0" {
				t.Fatalf("down %v, probes over %v", p.Backends()[0].ProbedDown(), proto.Load())
			}
		})
	}
}

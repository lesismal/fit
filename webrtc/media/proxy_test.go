package media

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/webrtc"
	"github.com/lesismal/fib/webrtc/stun"

	"github.com/lesismal/fit/observe"
)

// proxyDoc is a config of fit proxying /whip/* and /whep/* to backends,
// with webrtc.proxy's JSON proxy.
func proxyDoc(proxy string, backends ...*fit) string {
	addrs := make([]string, len(backends))
	for i, b := range backends {
		addrs[i] = `"` + strings.TrimPrefix(b.base, "http://") + `"`
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "upstreams": [{"name": "media", "addresses": [` + strings.Join(addrs, ", ") + `]}],
  "routes": [
    {"match": {"path": ["/whip/*", "/whep/*"]}, "handler": {"module": "webrtc_proxy", "params": {"upstream": "media", "timeout": "1s"}}}
  ],
  "webrtc": {"proxy": ` + proxy + `}
}`
}

// loopbackProxy's idle_timeout outlasts ICE's consent checks, every 4 to 6
// seconds.
const loopbackProxy = `{"listen": "127.0.0.1:0", "idle_timeout": "8s", "session_timeout": "2s"}`

func (f *fit) proxy() *Proxy { return LookupProxy(f.up) }

// The proxy's port, as its answers name it.
func (f *fit) proxyPort(t *testing.T) netip.AddrPort {
	t.Helper()
	a := f.proxy().Addrs()
	if len(a) != 1 {
		t.Fatalf("addrs %v", a)
	}
	return a[0]
}

// Three media servers behind one fit: each publisher's stream plays
// through fit to its viewer, the sessions spread over the media servers
// and stay on them, every candidate fit answers with is its own port, and
// DELETE frees the ufrags.
func TestProxyPublishAndView(t *testing.T) {
	backends := []*fit{startFit(t, doc(loopbackMedia)), startFit(t, doc(loopbackMedia)), startFit(t, doc(loopbackMedia))}
	f := startFit(t, proxyDoc(loopbackProxy, backends...))
	port := f.proxyPort(t)
	clients := startEngine(t)

	type pair struct {
		pub *publisher
		v   *viewer
	}
	var pairs []pair
	for i := range 3 {
		name := fmt.Sprintf("cam%d", i)
		pub := newPublisher(t, clients)
		resp, body := pub.signal(t, f.base+"/whip/"+name)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("publish %s: %d %s", name, resp.StatusCode, body)
		}
		if !strings.HasPrefix(pub.location, "/whip/"+name+"/") {
			t.Fatalf("Location %q", pub.location)
		}
		checkCandidates(t, body, port)
		pub.waitConnected(t)
		pub.send()
		pairs = append(pairs, pair{pub: pub})
	}
	// Each media server has one publisher: round robin spread them.
	for i, b := range backends {
		if p, _ := b.media().Sessions(); p != 1 {
			t.Fatalf("backend %d has %d publishers", i, p)
		}
	}
	// A viewer of each stream reaches the media server its publisher is on:
	// WHEP to the other two would be 404, which round robin would hit.
	for i := range pairs {
		v := newViewer(t, clients)
		for {
			resp, body := v.signal(t, f.base+fmt.Sprintf("/whep/cam%d", i))
			if resp.StatusCode == http.StatusCreated {
				checkCandidates(t, body, port)
				break
			}
			if resp.StatusCode != http.StatusNotFound {
				t.Fatalf("view cam%d: %d %s", i, resp.StatusCode, body)
			}
		}
		pairs[i].v = v
	}
	for _, p := range pairs {
		p.v.waitConnected(t)
		p.v.waitPackets(t, 30)
	}
	if s, r, u := f.proxy().Stats(); s != 6 || r < 6 || u != 6 {
		t.Fatalf("stats: %d sessions, %d relays, %d ufrags", s, r, u)
	}
	// The sessions' URLs reach the media servers that answered them.
	for _, p := range pairs {
		if code := request(t, "DELETE", f.base+p.v.location); code != http.StatusOK {
			t.Fatalf("DELETE viewer: %d", code)
		}
		if code := request(t, "DELETE", f.base+p.v.location); code != http.StatusNotFound {
			t.Fatalf("DELETE again: %d", code)
		}
		p.v.waitEnded(t)
		// Closed, by the media server's close_notify, which the relay
		// carries past the DELETE's answer, rather than failed when ICE
		// consent runs out.
		if s := p.v.pc.ConnectionState(); s != webrtc.PeerConnectionStateClosed {
			t.Fatalf("the viewer ended %s, not closed by the media server", s)
		}
	}
	for _, p := range pairs {
		if code := request(t, "DELETE", f.base+p.pub.location); code != http.StatusOK {
			t.Fatalf("DELETE publisher: %d", code)
		}
	}
	eventually(t, "the ufrags and relays freed", func() bool { s, r, u := f.proxy().Stats(); return s == 0 && r == 0 && u == 0 })
	for i, b := range backends {
		if p, v := b.media().Sessions(); p != 0 || v != 0 {
			t.Fatalf("backend %d still has %d publishers, %d viewers", i, p, v)
		}
	}
	m := proxyMetrics(f.proxy())
	for _, want := range []string{
		`fit_webrtc_proxy_sessions_total 6`,
		`fit_webrtc_proxy_sessions_ended_total{reason="delete"} 6`,
		`fit_webrtc_proxy_relays 0`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s:\n%s", want, m)
		}
	}
}

// checkCandidates checks that an answer's only candidate is fit's port.
func checkCandidates(t *testing.T, sdp string, port netip.AddrPort) {
	t.Helper()
	n := 0
	for _, l := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(l, "a=candidate:") {
			n++
			if !strings.Contains(l, " "+port.Addr().String()+" "+fmt.Sprint(port.Port())+" typ host") {
				t.Fatalf("candidate %q is not fit's %s", l, port)
			}
		}
	}
	if n == 0 {
		t.Fatalf("no candidate in\n%s", sdp)
	}
}

func proxyMetrics(p *Proxy) string {
	w := &observe.TextWriter{}
	p.WriteMetrics(w)
	return string(w.B)
}

// A datagram that is not a STUN Binding request, or is one for a ufrag no
// session has or that its ice-pwd does not sign, opens nothing.
func TestProxyDropsStrangers(t *testing.T) {
	backend := startFit(t, doc(loopbackMedia))
	f := startFit(t, proxyDoc(loopbackProxy, backend))
	port := f.proxyPort(t)
	clients := startEngine(t)
	pub := newPublisher(t, clients)
	if resp, body := pub.signal(t, f.base+"/whip/cam1"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	ufrag := ""
	for _, l := range strings.Split(pub.pc.RemoteDescription().SDP, "\r\n") {
		if u, ok := strings.CutPrefix(l, "a=ice-ufrag:"); ok {
			ufrag = u
		}
	}
	binding := func(user string, key []byte) []byte {
		var m stun.Message
		m.Build(stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}, stun.NewTransactionID())
		m.Add(stun.AttrUsername, []byte(user))
		m.AddIntegrity(key)
		m.AddFingerprint()
		return m.Raw
	}
	for _, d := range [][]byte{
		{0x16, 0xfe, 0xfd, 0, 0, 0, 0}, // DTLS, first
		binding("nosuchufrag:x", []byte("pwd")),
		binding(ufrag+":x", []byte("not the ice-pwd")),
	} {
		conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(port))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_, _ = conn.Write(d)
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if n, err := conn.Read(make([]byte, 1500)); err == nil {
			t.Fatalf("a stranger was answered with %d bytes", n)
		}
	}
	m := proxyMetrics(f.proxy())
	for _, want := range []string{`{reason="not_stun"} 1`, `{reason="unknown_ufrag"} 1`, `{reason="integrity"} 1`} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics lack %s:\n%s", want, m)
		}
	}
	// The publisher itself gets through.
	pub.waitConnected(t)
}

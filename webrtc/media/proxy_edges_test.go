package media

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A media server gone: its peer's relay ends, a DELETE of its session frees
// the ufrag though the media server cannot be told, and new sessions go to
// the media servers left once it is out of rotation.
func TestProxyBackendDown(t *testing.T) {
	a, b := startFit(t, doc(loopbackMedia)), startFit(t, doc(loopbackMedia))
	f := startFit(t, proxyDoc(loopbackProxy, a, b))
	clients := startEngine(t)
	pubs := []*publisher{newPublisher(t, clients), newPublisher(t, clients)}
	for i, p := range pubs {
		if resp, body := p.signal(t, f.base+"/whip/s"+string(rune('0'+i))); resp.StatusCode != http.StatusCreated {
			t.Fatalf("%d %s", resp.StatusCode, body)
		}
		p.waitConnected(t)
	}
	if pa, _ := a.media().Sessions(); pa != 1 {
		t.Fatal("the publishers are not one on each media server")
	}
	// Which of the two is on b.
	onB := pubs[0]
	for _, p := range pubs {
		if f.proxy().Session(p.location[strings.LastIndexByte(p.location, '/')+1:]).Backend == strings.TrimPrefix(b.base, "http://") {
			onB = p
		}
	}
	b.stop()
	onB.waitEnded(t)
	eventually(t, "the relay to b closed", func() bool { _, r, _ := f.proxy().Stats(); return r <= 1 })
	if code := request(t, "DELETE", f.base+onB.location); code != http.StatusBadGateway {
		t.Fatalf("DELETE of a session on a media server gone: %d", code)
	}
	if s, _, _ := f.proxy().Stats(); s != 1 {
		t.Fatalf("%d sessions", s)
	}
	// b fails once, and is out of rotation: every publisher after goes to a.
	failed := 0
	for i := range 4 {
		p := newPublisher(t, clients)
		resp, _ := p.signal(t, f.base+"/whip/n"+string(rune('0'+i)))
		switch resp.StatusCode {
		case http.StatusCreated:
			p.waitConnected(t)
		case http.StatusBadGateway:
			failed++
		default:
			t.Fatalf("publish: %d", resp.StatusCode)
		}
	}
	if failed > 1 {
		t.Fatalf("%d offers failed", failed)
	}
}

// A client cut, its peer silent, loses its relay at idle_timeout and its
// session session_timeout after, at fit and at the media server.
func TestProxyClientCut(t *testing.T) {
	backend := startFit(t, doc(loopbackMedia))
	f := startFit(t, proxyDoc(`{"listen": "127.0.0.1:0", "idle_timeout": "1s", "session_timeout": "1s"}`, backend))
	clients, cut := startEngine(t), startEngine(t)
	pub := newPublisher(t, clients)
	if resp, body := pub.signal(t, f.base+"/whip/s"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	pub.send()
	v := newViewer(t, cut)
	if resp, body := v.signal(t, f.base+"/whep/s"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	v.waitPackets(t, 20)
	stopEngine(cut)
	eventually(t, "the cut viewer's session gone", func() bool { s, _, _ := f.proxy().Stats(); return s == 1 })
	eventually(t, "the media server's viewer gone", func() bool { _, n := backend.media().Sessions(); return n == 0 })
	if m := proxyMetrics(f.proxy()); !strings.Contains(m, `reason="expired"} 1`) {
		t.Fatalf("metrics:\n%s", m)
	}
}

func freeUDPPort(t *testing.T) string {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return strconv.Itoa(pc.LocalAddr().(*net.UDPAddr).Port)
}

// fakeServer is a media server that answers as told.
type fakeServer struct {
	*httptest.Server
	patches, deletes atomic.Int64
	// posted is the escaped path and query of the last POST.
	posted atomic.Value
}

const fakeAnswer = "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\n" +
	"m=video 9 UDP/TLS/RTP/SAVPF 96\r\nc=IN IP4 10.9.9.9\r\na=mid:0\r\na=ice-ufrag:%s\r\na=ice-pwd:pwdpwdpwdpwdpwdpwdpwdpwd\r\n" +
	"a=candidate:1 1 udp 2130706431 fe80::1 5000 typ host\r\n" +
	"a=candidate:2 1 udp 2130706431 127.0.0.1 5001 typ host\r\n" +
	"a=candidate:3 1 udp 1694498815 203.0.113.9 5002 typ srflx raddr 0.0.0.0 rport 0\r\n" +
	"a=candidate:4 1 tcp 1518280447 127.0.0.1 5003 typ host tcptype passive\r\n" +
	"a=end-of-candidates\r\n"

func startFake(t *testing.T) *fakeServer {
	fs := &fakeServer{}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == "POST" {
			fs.posted.Store(r.URL.RequestURI())
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/slow"):
			time.Sleep(3 * time.Second)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/big"):
			w.Header().Set("Location", "/res/big")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(strings.Repeat("a=x\r\n", 20000)))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/noloc"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/badloc"):
			w.Header().Set("Location", ":bad")
			w.WriteHeader(http.StatusCreated)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/cut"):
			// The answer is cut short.
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("v=0\r\n"))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		case r.Method == "OPTIONS":
			w.Header().Set("Accept-Post", "application/sdp")
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET":
			w.Header().Add("Link", `<`+r.URL.Path+`/more>; rel="more"`)
			_, _ = w.Write([]byte("got " + r.URL.Path))
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/nocand"):
			w.Header().Set("Location", "/res/nocand")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(strings.Split(strings.Replace(fakeAnswer, "%s", "nc", 1), "a=candidate:")[0]))
		case r.Method == "POST":
			if strings.Contains(string(body), "a=candidate:") {
				t.Errorf("the offer reached the media server with its candidates")
			}
			w.Header().Set("Location", "http://"+r.Host+"/res/"+r.URL.Query().Get("u"))
			w.Header().Add("Link", `<stun:stun.example.com>; rel="ice-server"`)
			w.Header().Add("Link", `</res/`+r.URL.Query().Get("u")+`/sse>; rel="urn:ietf:params:whep:ext:core:server-sent-events", <http://elsewhere.example/x>; rel="other"`)
			w.Header().Set("Content-Type", "application/sdp")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(strings.Replace(fakeAnswer, "%s", r.URL.Query().Get("u"), 1)))
		case r.Method == "PATCH":
			fs.patches.Add(1)
			w.Header().Set("Content-Type", "application/trickle-ice-sdpfrag")
			_, _ = w.Write([]byte("a=ice-ufrag:restarted\r\na=ice-pwd:pwdpwdpwdpwdpwdpwdpwdpwd\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=mid:0\r\n"))
		case r.Method == "DELETE":
			fs.deletes.Add(1)
		}
	}))
	t.Cleanup(fs.Close)
	return fs
}

// The answer's candidates, Location and Links as fit rewrites them; a
// restart's ufrag routed with the first's and a trickle answered by fit; an
// offer or an answer past max_sdp_bytes, an answer with no candidate fit
// can relay to, and a media server that does not answer in time; and the
// sessions that reserved room and made none give it back.
func TestProxySignaling(t *testing.T) {
	fs := startFake(t)
	f := startFit(t, proxyDoc(loopbackProxy, &fit{base: fs.URL}))
	port := f.proxyPort(t)

	resp, body := post(t, f.base+"/whip/cam?u=u1", "application/sdp", "v=0\r\na=candidate:1 1 udp 1 192.0.2.1 9 typ host\r\n")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	checkCandidates(t, body, port)
	if !strings.Contains(body, "c=IN IP4 127.0.0.1\r\n") || strings.Contains(body, "10.9.9.9") {
		t.Fatalf("c= not rewritten:\n%s", body)
	}
	loc := resp.Header.Get("Location")
	id := loc[strings.LastIndexByte(loc, '/')+1:]
	if !strings.HasPrefix(loc, "/whip/cam/") || f.proxy().Session(id) == nil {
		t.Fatalf("Location %q", loc)
	}
	f.proxy().mu.Lock()
	rt := f.proxy().ufrags["u1"]
	f.proxy().mu.Unlock()
	if rt == nil || rt.media.String() != "127.0.0.1:5001" {
		t.Fatalf("route %+v", rt)
	}
	links := resp.Header.Values("Link")
	if len(links) != 2 || links[0] != `<stun:stun.example.com>; rel="ice-server"` || !strings.HasPrefix(links[1], "<"+loc+"/sse>;") {
		t.Fatalf("Link %q", links)
	}

	// A trickle is answered here; a restart goes to the media server, and
	// its new ufrag routes to the same address.
	patch := func(frag string) int {
		req, _ := http.NewRequest("PATCH", f.base+loc, strings.NewReader(frag))
		req.Header.Set("Content-Type", "application/trickle-ice-sdpfrag")
		r, _ := do(t, req)
		return r.StatusCode
	}
	if code := patch("a=candidate:1 1 udp 1 192.0.2.1 9 typ host\r\n"); code != http.StatusNoContent || fs.patches.Load() != 0 {
		t.Fatalf("trickle: %d, %d at the media server", code, fs.patches.Load())
	}
	if code := patch("a=ice-ufrag:new\r\na=ice-pwd:newnewnewnewnewnewnewnew\r\n"); code != http.StatusOK || fs.patches.Load() != 1 {
		t.Fatalf("restart: %d", code)
	}
	if _, _, u := f.proxy().Stats(); u != 2 {
		t.Fatalf("%d ufrags", u)
	}

	// The limits and the failures.
	if resp, _ := post(t, f.base+"/whip/cam?u=u2", "application/sdp", strings.Repeat("a=x\r\n", 10000)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a large offer: %d", resp.StatusCode)
	}
	if resp, _ := post(t, f.base+"/whip/big", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("a large answer: %d", resp.StatusCode)
	}
	if resp, _ := post(t, f.base+"/whip/nocand", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("an answer with no candidate: %d", resp.StatusCode)
	}
	eventually(t, "the session without candidates deleted", func() bool { return fs.deletes.Load() == 1 })
	start := time.Now()
	if resp, _ := post(t, f.base+"/whip/slow", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusGatewayTimeout || time.Since(start) > 2*time.Second {
		t.Fatalf("a slow media server: %d after %s", resp.StatusCode, time.Since(start))
	}
	// A client that leaves while the media server is slow.
	c := &http.Client{Timeout: 200 * time.Millisecond}
	if _, err := c.Post(f.base+"/whip/slow", "application/sdp", strings.NewReader("v=0\r\n")); err == nil {
		t.Fatal("the slow post was answered")
	}
	if resp, _ := post(t, f.base+"/whip/cam", "text/plain", "v=0\r\n"); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain: %d", resp.StatusCode)
	}

	if code := request(t, "DELETE", f.base+loc); code != http.StatusOK || fs.deletes.Load() != 2 {
		t.Fatalf("DELETE: %d", code)
	}
	eventually(t, "every reservation given back", func() bool {
		p := f.proxy()
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.reserved == 0 && len(p.sessions) == 0 && len(p.ufrags) == 0
	})
}

// max_sessions refuses the offer before it reaches a media server.
func TestProxyMaxSessions(t *testing.T) {
	fs := startFake(t)
	f := startFit(t, proxyDoc(`{"listen": "127.0.0.1:0", "max_sessions": 1}`, &fit{base: fs.URL}))
	if resp, body := post(t, f.base+"/whip/a?u=a", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if resp, _ := post(t, f.base+"/whip/b?u=b", "application/sdp", "v=0\r\n"); resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("past max_sessions: %d", resp.StatusCode)
	}
}

// A reload that keeps listen keeps the sessions and their media; one that
// changes it ends them; one without webrtc.proxy stops it, and the routes
// answer 503.
func TestProxyReload(t *testing.T) {
	backend := startFit(t, doc(loopbackMedia))
	f := startFit(t, proxyDoc(loopbackProxy, backend))
	clients := startEngine(t)
	pub := newPublisher(t, clients)
	if resp, body := pub.signal(t, f.base+"/whip/s"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	pub.waitConnected(t)
	pub.send()
	v := newViewer(t, clients)
	if resp, body := v.signal(t, f.base+"/whep/s"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	v.waitPackets(t, 20)

	f.reload(t, proxyDoc(`{"listen": "127.0.0.1:0", "idle_timeout": "7s", "session_timeout": "3s", "max_sessions": 10}`, backend))
	v.waitPackets(t, 50)
	if s, _, _ := f.proxy().Stats(); s != 2 {
		t.Fatalf("%d sessions after a reload", s)
	}
	f.reload(t, proxyDoc(`{"listen": "127.0.0.1:0", "public_ips": ["127.0.0.2"]}`, backend))
	if s, _, _ := f.proxy().Stats(); s != 2 {
		t.Fatalf("%d sessions after a change of public_ips", s)
	}
	f.reload(t, proxyDoc(`{"listen": "127.0.0.1:`+freeUDPPort(t)+`"}`, backend))
	if s, r, _ := f.proxy().Stats(); s != 0 || r != 0 {
		t.Fatalf("%d sessions, %d relays after a change of listen", s, r)
	}
	if m := proxyMetrics(f.proxy()); !strings.Contains(m, "fit_webrtc_proxy_restarts_total 1") || !strings.Contains(m, `reason="reload"} 2`) {
		t.Fatalf("metrics:\n%s", m)
	}
	f.reload(t, strings.Replace(proxyDoc(loopbackProxy, backend), `"webrtc": {"proxy": `+loopbackProxy+`}`, `"webrtc": {}`, 1))
	if resp, _ := newPublisher(t, clients).signal(t, f.base+"/whip/again"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("without webrtc.proxy: %d", resp.StatusCode)
	}
}

// rewriteSDP relays to a UDP candidate of the RTP component, though a TCP
// candidate or an RTCP one would rank above it.
func TestRewriteSDPOnlyUDPRTP(t *testing.T) {
	answer := "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=ice-ufrag:uf\r\na=ice-pwd:pw\r\n" +
		"a=candidate:1 1 tcp 1518280447 127.0.0.1 5003 typ host tcptype passive\r\n" +
		"a=candidate:2 2 udp 2130706430 127.0.0.1 5004 typ host\r\n" +
		"a=candidate:3 1 udp 1694498815 203.0.113.9 5002 typ srflx raddr 0.0.0.0 rport 0\r\n"
	_, creds, err := rewriteSDP([]byte(answer), []netip.AddrPort{netip.MustParseAddrPort("192.0.2.1:8189")}, netip.AddrPort{})
	if err != nil || len(creds) != 1 {
		t.Fatal(creds, err)
	}
	if want := netip.MustParseAddrPort("203.0.113.9:5002"); creds[0].Media != want {
		t.Fatalf("relayed to %v, want %v", creds[0].Media, want)
	}
}

// An offer's path goes on escaped as it came: an escaped '?' or '#' in the
// stream's name does not cut the path short. With auth, a stream's name
// that no media server of fit's takes is refused before it is checked, so
// that secret%23-guest, which *-guest matches, does not publish secret.
func TestProxyEscapedPath(t *testing.T) {
	fs := startFake(t)
	f := startFit(t, proxyDoc(loopbackProxy, &fit{base: fs.URL}))
	for i, path := range []string{"/whip/a%3Fb", "/whip/a%23b", "/whip/a%2Fb"} {
		query := "?u=e" + strconv.Itoa(i)
		resp, body := post(t, f.base+path+query, "application/sdp", "v=0\r\n")
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, body)
		}
		if got, _ := fs.posted.Load().(string); got != path+query {
			t.Fatalf("%s reached the media server as %q", path, got)
		}
		if loc := resp.Header.Get("Location"); !strings.HasPrefix(loc, path+"/") {
			t.Fatalf("%s: Location %q", path, loc)
		}
	}

	const guest = "guest-token-0123456789"
	g := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": [{"name": "g", "tokens": [{"token": "`+guest+`", "publish": ["*-guest"]}]}],
  "upstreams": [{"name": "media", "addresses": ["`+strings.TrimPrefix(fs.URL, "http://")+`"]}],
  "routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "webrtc_proxy", "params": {"upstream": "media", "timeout": "1s", "auth": "g", "action": "publish"}}}],
  "webrtc": {"proxy": `+loopbackProxy+`}
}`)
	fs.posted.Store("")
	for _, path := range []string{"/whip/secret%23-guest", "/whip/secret%3F-guest", "/whip/a%2Fb-guest", "/whip/s%20p-guest"} {
		resp, _ := postSDP(t, g.base+path+"?u=g1", "v=0\r\n", bearer(guest))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if got, _ := fs.posted.Load().(string); got != "" {
			t.Fatalf("%s reached the media server as %q", path, got)
		}
	}
	if resp, body := postSDP(t, g.base+"/whip/cam-guest?u=g2", "v=0\r\n", bearer(guest)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("cam-guest: %d %s", resp.StatusCode, body)
	}
	if got, _ := fs.posted.Load().(string); got != "/whip/cam-guest?u=g2" {
		t.Fatalf("cam-guest reached the media server as %q", got)
	}
}

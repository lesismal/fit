package media

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/lesismal/fib/webrtc"
	"github.com/lesismal/fib/webrtc/stun"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// host is an app.Host on an engine of the test's, as fit gives a role.
func host(t *testing.T) *app.Host {
	t.Helper()
	root, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	e := startEngine(t)
	return &app.Host{ID: "webrtc", Engine: e, Upstream: e, Log: root.Logger("webrtc")}
}

// busyUDP holds a UDP port, as another process would, and returns its
// address.
func busyUDP(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	return pc.LocalAddr().String()
}

// The proxy role refuses a member that is not one, a bad public IP and a
// port another socket holds, before and after it runs; a move to a held
// port ends the sessions and leaves it without a port, which Add then
// refuses; a public IP or a wildcard listen is what Addrs names; and once
// stopped it does not start again.
func TestProxyRoleApply(t *testing.T) {
	p := NewProxy(host(t))
	if err := p.Apply(nil); err == nil {
		t.Fatal("applied no member")
	}
	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0", PublicIPs: []string{"not-an-ip"}}); err == nil {
		t.Fatal("applied a bad public IP")
	}
	held := busyUDP(t)
	if err := p.Apply(&config.WebRTCProxy{Listen: held}); err == nil {
		t.Fatal("bound a port another socket holds")
	}
	if p.Addrs() != nil || p.Reserve() {
		t.Fatal("a proxy with no port takes offers")
	}
	if _, err := p.Add("b", "/s", nil); err == nil {
		t.Fatal("added a session with no port")
	}

	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0", PublicIPs: []string{"192.0.2.7", "::ffff:192.0.2.7"}}); err != nil {
		t.Fatal(err)
	}
	if a := p.Addrs(); len(a) != 1 || a[0].Addr() != netip.MustParseAddr("192.0.2.7") {
		t.Fatalf("addrs with public_ips: %v", a)
	}
	if !p.Reserve() {
		t.Fatal("no room")
	}
	id, err := p.Add("b", "/s", []Cred{{Ufrag: "u", Pwd: "p", Media: netip.MustParseAddrPort("127.0.0.1:9")}})
	if err != nil {
		t.Fatal(err)
	}

	// A wildcard listen names the host's addresses of its family.
	if err := p.Apply(&config.WebRTCProxy{Listen: "0.0.0.0:0"}); err != nil {
		t.Fatal(err)
	}
	for _, a := range p.Addrs() {
		if !a.Addr().Is4() || a.Addr().IsUnspecified() || a.Port() == 0 {
			t.Fatalf("addrs of a wildcard listen: %v", p.Addrs())
		}
	}
	if p.Session(id) != nil {
		t.Fatal("the session outlived a change of listen")
	}

	// A move to a port held elsewhere: the old port goes first, and the
	// role is left without one.
	if err := p.Apply(&config.WebRTCProxy{Listen: held}); err == nil {
		t.Fatal("moved to a port another socket holds")
	}
	if p.Addrs() != nil {
		t.Fatalf("addrs after a failed move: %v", p.Addrs())
	}
	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	p.Stop(t.Context())
	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0"}); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("applied after Stop: %v", err)
	}
}

// A ufrag routes to one session: another session's answer or restart that
// names it is refused; a restart for a session gone is refused; and a
// session keeps its last 4 ufrags, a restart without a candidate keeping
// the media address.
func TestProxyUfrags(t *testing.T) {
	p := NewProxy(host(t))
	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(t.Context()) })
	media := netip.MustParseAddrPort("127.0.0.1:5000")
	add := func(ufrag string) (string, error) {
		if !p.Reserve() {
			t.Fatal("no room")
		}
		return p.Add("b", "/s/"+ufrag, []Cred{{Ufrag: ufrag, Pwd: "pwd", Media: media}})
	}
	a, err := add("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := add("b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := add("a"); err == nil {
		t.Fatal("a second session took the ufrag a")
	}
	if err := p.Restart(b, []Cred{{Ufrag: "a", Pwd: "pwd"}}); err == nil {
		t.Fatal("a restart took another session's ufrag")
	}
	if err := p.Restart("nope", []Cred{{Ufrag: "x", Pwd: "pwd"}}); err == nil {
		t.Fatal("restarted a session that is not")
	}
	for _, u := range []string{"a1", "a2", "a3", "a4"} {
		if err := p.Restart(a, []Cred{{Ufrag: u, Pwd: "pwd"}}); err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	first, last := p.ufrags["a"], p.ufrags["a4"]
	kept := len(p.sessions[a].ufrags)
	p.mu.Unlock()
	if first != nil || last == nil || kept != maxUfrags || last.media != media {
		t.Fatalf("after 4 restarts: the first ufrag %v, the last %+v, %d kept", first, last, kept)
	}
	p.End(a)
	p.End(a)
	if s, _, u := p.Stats(); s != 1 || u != 1 {
		t.Fatalf("%d sessions, %d ufrags", s, u)
	}
}

// stunRequest is a STUN message of type typ with attrs, each a type and a
// value, its length field length when not negative.
func stunRequest(typ uint16, length int, attrs ...[]byte) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b, typ)
	binary.BigEndian.PutUint32(b[4:], stun.MagicCookie)
	for _, a := range attrs {
		b = append(b, a...)
	}
	if length < 0 {
		length = len(b) - 20
	}
	binary.BigEndian.PutUint16(b[2:], uint16(length))
	return b
}

func attr(typ uint16, v string, declared int) []byte {
	b := make([]byte, 4, 4+len(v)+3)
	binary.BigEndian.PutUint16(b, typ)
	binary.BigEndian.PutUint16(b[2:], uint16(declared))
	b = append(b, v...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// requestUfrag reads a Binding request's ufrag past other attributes, and
// nothing from what is not one, or is cut short.
func TestRequestUfrag(t *testing.T) {
	user := uint16(stun.AttrUsername)
	for _, c := range []struct {
		name string
		b    []byte
		want string
	}{
		{"after another attribute", stunRequest(1, -1, attr(0x8022, "agent", 5), attr(user, "srv:peer", 8)), "srv"},
		{"short", []byte{0, 1, 0, 0}, ""},
		{"a response", stunRequest(0x0101, -1, attr(user, "srv:peer", 8)), ""},
		{"a length past the datagram", stunRequest(1, 64, attr(user, "srv:peer", 8)), ""},
		{"an attribute past the message", stunRequest(1, -1, attr(user, "srv:peer", 40)), ""},
		{"a USERNAME with no colon", stunRequest(1, -1, attr(user, "srvpeer", 7)), ""},
		{"no USERNAME", stunRequest(1, -1, attr(0x8022, "agent", 5)), ""},
	} {
		if got := requestUfrag(c.b); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// rewriteSDP refuses with no address of fit's; it relays to media_addrs'
// address, or its IP with the best candidate's port; it ranks a host
// candidate over a server-reflexive one and IPv4 first, and skips a relay
// one; an IPv6 address of fit's makes c= IP6; and a fragment with no media
// section gets the candidates at its end.
func TestRewriteSDPEdges(t *testing.T) {
	fits := []netip.AddrPort{netip.MustParseAddrPort("[2001:db8::1]:8189")}
	answer := "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 10.0.0.9\r\na=ice-ufrag:uf\r\na=ice-pwd:pw\r\n" +
		"a=candidate:1 1 udp 16777215 198.51.100.1 7000 typ relay raddr 0.0.0.0 rport 0\r\n" +
		"a=candidate:2 1 udp 2130706431 2001:db8::9 7001 typ host\r\n" +
		"a=candidate:3 1 udp 1694498815 203.0.113.9 7002 typ srflx raddr 0.0.0.0 rport 0\r\n" +
		"m=video 0 UDP/TLS/RTP/SAVPF 96\r\n"
	if _, _, err := rewriteSDP([]byte(answer), nil, netip.AddrPort{}); err == nil {
		t.Fatal("rewrote with no address of fit's")
	}
	out, creds, err := rewriteSDP([]byte(answer), fits, netip.AddrPort{})
	if err != nil || len(creds) != 1 || creds[0].Media != netip.MustParseAddrPort("[2001:db8::9]:7001") {
		t.Fatalf("%v %v", creds, err)
	}
	if s := string(out); !strings.Contains(s, "c=IN IP6 2001:db8::1\r\n") || strings.Count(s, "a=end-of-candidates") != 1 {
		t.Fatalf("rewritten:\n%s", s)
	}
	_, creds, _ = rewriteSDP([]byte(answer), fits, netip.MustParseAddrPort("10.1.1.1:0"))
	if creds[0].Media != netip.MustParseAddrPort("10.1.1.1:7001") {
		t.Fatalf("media_addrs' IP: %v", creds[0].Media)
	}
	_, creds, _ = rewriteSDP([]byte(answer), fits, netip.MustParseAddrPort("10.1.1.1:9999"))
	if creds[0].Media != netip.MustParseAddrPort("10.1.1.1:9999") {
		t.Fatalf("media_addrs' address: %v", creds[0].Media)
	}
	out, creds, _ = rewriteSDP([]byte("a=ice-ufrag:r\r\na=ice-pwd:pw\r\n"), fits, netip.AddrPort{})
	if len(creds) != 1 || creds[0].Media.IsValid() || !strings.Contains(string(out), "a=end-of-candidates\r\n") {
		t.Fatalf("a fragment: %v\n%s", creds, out)
	}
}

// setLinks drops a field value that is not a link, and upstreamPath a
// Location that is not a URL.
func TestLinksAndLocations(t *testing.T) {
	req, _ := http.NewRequest("POST", "http://media.example/whip/a", nil)
	hdr := http.Header{}
	setLinks(hdr, []string{`not a link, </whip/a/x/sse>; rel="sse"`}, req.URL, "/whip/a/x", "/fit/s")
	if got := hdr.Values("Link"); len(got) != 1 || got[0] != `</fit/s/sse>; rel="sse"` {
		t.Fatalf("Link %q", got)
	}
	if p := upstreamPath(req.URL, ":bad"); p != "" {
		t.Fatalf("a Location that is not a URL: %q", p)
	}
	if p := upstreamPath(req.URL, "x?a=1"); p != "/whip/x?a=1" {
		t.Fatalf("a relative Location: %q", p)
	}
	if e := (statusError{http.StatusTeapot, "tea"}); e.Error() != "tea" {
		t.Fatal(e.Error())
	}
}

// The media server refuses a member that is not one, a port another socket
// holds and a bad public IP; a change of public_ips that cannot start on
// the same port starts the server as it was again; a publisher's offer
// that is SDP but no session's is refused 400, its stream given back;
// a viewer's offer that is not SDP is refused 400; a DELETE of a session
// of the other kind finds none; and once stopped it neither starts again
// nor takes offers.
func TestServerRoleApply(t *testing.T) {
	s, err := New(host(t))
	if err != nil {
		t.Fatal(err)
	}
	member := func(listen string, public ...string) *config.WebRTC {
		return &config.WebRTC{Media: &config.WebRTCMedia{Listen: listen, PublicIPs: public}}
	}
	if err := s.Apply(&config.WebRTC{}); err == nil {
		t.Fatal("applied no member")
	}
	if err := s.Apply(member(busyUDP(t))); err == nil {
		t.Fatal("bound a port another socket holds")
	}
	if err := s.Apply(member("127.0.0.1:0", "not-an-ip")); err == nil {
		t.Fatal("applied a bad public IP")
	}
	if _, e := s.View("s", "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\n"); e == nil || e.Status != http.StatusServiceUnavailable || e.RetryAfter == 0 {
		t.Fatalf("an offer to a server not running: %v", e)
	}
	if err := s.Apply(member(freeUDPAddr(t))); err != nil {
		t.Fatal(err)
	}
	listen := s.cur.cfg.Listen
	if err := s.Apply(member(listen, "not-an-ip")); err == nil {
		t.Fatal("applied a bad public IP")
	}
	if a := s.Addrs(); len(a) != 1 || a[0].String() != listen {
		t.Fatalf("the server as it was did not start again: %v", a)
	}

	// SDP whose media section has no ICE credentials: parsed, picked, and
	// refused by the PeerConnection.
	noICE := "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=mid:0\r\na=sendonly\r\na=rtpmap:111 opus/48000/2\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 111\r\nc=IN IP4 0.0.0.0\r\na=mid:1\r\na=sendonly\r\na=rtpmap:111 opus/48000/2\r\n"
	if _, e := s.Publish("s", noICE); e == nil || e.Status != http.StatusBadRequest || e.Error() != e.Msg {
		t.Fatalf("an offer without ICE: %v", e)
	}
	if _, e := s.View("s", "not sdp"); e == nil || e.Status != http.StatusBadRequest {
		t.Fatalf("a viewer's offer that is not SDP: %v", e)
	}
	s.mu.Lock()
	streams, pcs := len(s.streams), s.pcs
	s.mu.Unlock()
	if streams != 0 || pcs != 0 {
		t.Fatalf("%d streams, %d PeerConnections reserved after refusals", streams, pcs)
	}

	clients := startEngine(t)
	pub := newPublisher(t, clients)
	offer := offerOf(t, pub.peer)
	a, e := s.Publish("s", offer)
	if e != nil {
		t.Fatal(e)
	}
	if s.End(Viewer, a.ID) || !s.has(Publisher, a.ID) {
		t.Fatal("a publisher's session ended as a viewer's")
	}
	if Publisher.String() != "whip" || Viewer.String() != "whep" {
		t.Fatal(Publisher.String(), Viewer.String())
	}

	s.Stop(t.Context())
	s.Stop(t.Context())
	if s.has(Publisher, a.ID) {
		t.Fatal("the session outlived Stop")
	}
	if err := s.Apply(member("127.0.0.1:0")); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("applied after Stop: %v", err)
	}
}

// freeUDPAddr is a UDP address on the loopback no socket holds.
func freeUDPAddr(t *testing.T) string { return "127.0.0.1:" + freeUDPPort(t) }

// offerOf is p's offer, with its candidates.
func offerOf(t *testing.T, p *peer) string {
	t.Helper()
	o, err := webrtc.Offer(t.Context(), p.pc, nil)
	if err != nil {
		t.Fatal(err)
	}
	return o.SDP
}

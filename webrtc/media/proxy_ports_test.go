package media

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/webrtc/stun"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// The relays' sockets toward the media servers leave from relay_ip and
// the port range; a peer address that finds the range full is dropped and
// counted, and gets through once a port is free again.
func TestProxyRelayPorts(t *testing.T) {
	e := startEngine(t)
	media, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	port, _ := strconv.Atoi(freeUDPPort(t))
	p := NewProxy(&app.Host{ID: ProxyID, Engine: e, Upstream: e})
	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0", RelayIP: "127.0.0.1", PortMin: port, PortMax: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Stop(context.Background()) })
	if !p.Reserve() {
		t.Fatal("no room")
	}
	const pwd = "pwdpwdpwdpwdpwdpwdpwdpwd"
	if _, err := p.Add("backend", "/res/1", []Cred{{Ufrag: "u1", Pwd: pwd, Media: media.LocalAddr().(*net.UDPAddr).AddrPort()}}); err != nil {
		t.Fatal(err)
	}
	proxyAddr := p.Addrs()[0]
	binding := func() []byte {
		var m stun.Message
		m.Build(stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}, stun.NewTransactionID())
		m.Add(stun.AttrUsername, []byte("u1:peer"))
		m.AddIntegrity([]byte(pwd))
		m.AddFingerprint()
		return m.Raw
	}
	peer := func() *net.UDPConn {
		c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(proxyAddr))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	buf := make([]byte, 1500)
	// readFrom waits for the media server to read a datagram, and returns
	// where it came from.
	readFrom := func(within time.Duration) (netip.AddrPort, bool) {
		_ = media.SetReadDeadline(time.Now().Add(within))
		_, from, err := media.ReadFromUDPAddrPort(buf)
		return from, err == nil
	}

	first := peer()
	if _, err := first.Write(binding()); err != nil {
		t.Fatal(err)
	}
	from, ok := readFrom(5 * time.Second)
	if !ok || from != netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)) {
		t.Fatalf("the media server read from %v (%v), want 127.0.0.1:%d", from, ok, port)
	}
	if _, err := media.WriteToUDPAddrPort([]byte("answer"), from); err != nil {
		t.Fatal(err)
	}
	_ = first.SetReadDeadline(time.Now().Add(5 * time.Second))
	if n, err := first.Read(buf); err != nil || string(buf[:n]) != "answer" {
		t.Fatalf("the peer read %q: %v", buf[:n], err)
	}

	// The one port is the first relay's: a second peer address is dropped.
	second := peer()
	if _, err := second.Write(binding()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second peer dropped for no port", func() bool {
		return strings.Contains(proxyMetrics(p), `fit_webrtc_proxy_dropped_total{reason="no_port"} 1`)
	})
	if from, ok := readFrom(200 * time.Millisecond); ok {
		t.Fatalf("the media server read from %v", from)
	}
	if _, relays, _ := p.Stats(); relays != 1 {
		t.Fatalf("%d relays", relays)
	}

	// Once the first relay ends, the second peer's next request gets the
	// port.
	_ = first.Close()
	p.mu.Lock()
	var r *relay
	for _, s := range p.sessions {
		for rr := range s.relays {
			r = rr
		}
	}
	p.mu.Unlock()
	r.close()
	eventually(t, "the port freed and taken again", func() bool {
		_, _ = second.Write(binding())
		from, ok := readFrom(100 * time.Millisecond)
		return ok && from.Port() == uint16(port)
	})
}

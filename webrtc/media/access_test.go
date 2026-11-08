package media

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/lesismal/fib/webrtc/stun"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// webrtc.proxy's access decides first, then the global one: the datagrams
// of a peer address it refuses are dropped, and never reach the media
// server; a reload that changes the access alone, or the global access
// alone, keeps the port and its sessions.
func TestProxyAccess(t *testing.T) {
	e := startEngine(t)
	mgr := app.NewManager(e, e, nil, nil)
	t.Cleanup(func() { mgr.Stop(context.Background()) })
	global := func(a *config.Access) {
		t.Helper()
		if err := mgr.Apply(&config.Config{Access: a}); err != nil {
			t.Fatal(err)
		}
	}
	media, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer media.Close()
	p := NewProxy(mgr.Host(ProxyID))
	t.Cleanup(func() { p.Stop(context.Background()) })
	apply := func(a *config.Access) {
		t.Helper()
		if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0", Access: a}); err != nil {
			t.Fatal(err)
		}
	}
	apply(&config.Access{Deny: []string{"127.0.0.1"}})
	if !p.Reserve() {
		t.Fatal("no room")
	}
	const pwd = "pwdpwdpwdpwdpwdpwdpwdpwd"
	if _, err := p.Add("backend", "/res/1", []Cred{{Ufrag: "u1", Pwd: pwd, Media: media.LocalAddr().(*net.UDPAddr).AddrPort()}}); err != nil {
		t.Fatal(err)
	}
	port := p.Addrs()[0]
	buf := make([]byte, 1500)
	// relayed reports whether a new peer address's Binding request reaches
	// the media server.
	relayed := func() bool {
		t.Helper()
		c, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(port))
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		var m stun.Message
		m.Build(stun.MessageType{Method: stun.MethodBinding, Class: stun.ClassRequest}, stun.NewTransactionID())
		m.Add(stun.AttrUsername, []byte("u1:peer"))
		m.AddIntegrity([]byte(pwd))
		m.AddFingerprint()
		if _, err := c.Write(m.Raw); err != nil {
			t.Fatal(err)
		}
		_ = media.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		_, err = media.Read(buf)
		return err == nil
	}
	want := func(what string, want bool) {
		t.Helper()
		if got := relayed(); got != want {
			t.Fatalf("%s: relayed %v, want %v", what, got, want)
		}
	}

	want("the member's deny", false)
	global(&config.Access{Deny: []string{"127.0.0.0/8"}})
	apply(&config.Access{Allow: []string{"127.0.0.1"}})
	want("the member's allow over a global deny", true)
	apply(nil)
	want("the global deny", false)
	// The global access changed, the member not.
	global(nil)
	apply(nil)
	want("no access", true)
	if p.Addrs()[0] != port || p.m.restarts.Load() != 0 {
		t.Fatalf("the port moved to %v from %v, %d restarts", p.Addrs()[0], port, p.m.restarts.Load())
	}
	if sessions, _, _ := p.Stats(); sessions != 1 {
		t.Fatalf("%d sessions", sessions)
	}
	if err := p.Apply(&config.WebRTCProxy{Listen: "127.0.0.1:0", Access: &config.Access{AllowFiles: []string{"/nonexistent/fit-access-list"}}}); err == nil {
		t.Fatal("an access list file that is not there")
	}
	want("after a refused Apply", true)

	// The access's list files are watched, as certificate files are.
	c := &config.Config{WebRTC: &config.WebRTC{Proxy: &config.WebRTCProxy{Listen: "127.0.0.1:0",
		Access: &config.Access{AllowFiles: []string{"a.txt"}, DenyFiles: []string{"d.txt"}}}}}
	if got := app.CertFiles(c); !slices.Equal(got, []string{"a.txt", "d.txt"}) {
		t.Fatalf("the files watched: %q", got)
	}
}

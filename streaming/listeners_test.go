package streaming

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// restart keeps a listener of the same key; starts a new one on another
// address before closing the old, which stays when the new cannot start;
// and on the same address closes the old first, starting it again when the
// new cannot start.
func TestRestart(t *testing.T) {
	var stopped []string
	stop := func(l string) { stopped = append(stopped, l) }
	ok := func(l string) func() (string, error) { return func() (string, error) { return l, nil } }
	fail := func() (string, error) { return "", errors.New("busy") }

	if l, err := restart("", "", nil, "k", ":1", true, ok("new"), stop, nil); l != "new" || err != nil {
		t.Fatalf("the first: %q %v", l, err)
	}
	if l, err := restart("old", "k", []string{":1"}, "k", ":1", false, fail, stop, fail); l != "old" || err != nil || stopped != nil {
		t.Fatalf("the same key: %q %v %v", l, err, stopped)
	}

	l, err := restart("old", "k", []string{":1"}, "k2", ":2", false, fail, stop, fail)
	if l != "old" || err == nil || stopped != nil {
		t.Fatalf("another address that cannot start: %q %v %v", l, err, stopped)
	}
	l, err = restart("old", "k", []string{":1"}, "k2", ":2", false, ok("new"), stop, fail)
	if l != "new" || err != nil || len(stopped) != 1 {
		t.Fatalf("another address: %q %v %v", l, err, stopped)
	}

	stopped = nil
	l, err = restart("old", "k", []string{":1"}, "k2", ":1", false, fail, stop, ok("again"))
	if l != "again" || err == nil || len(stopped) != 1 {
		t.Fatalf("the same address that cannot start: %q %v %v", l, err, stopped)
	}
	l, err = restart("old", "k", []string{"127.0.0.1:1"}, "k2", "0.0.0.0:1", false, fail, stop, fail)
	if l != "" || err == nil || !strings.Contains(err.Error(), "could not start again") {
		t.Fatalf("neither starts: %q %v", l, err)
	}
	if addrString(nil) != "" {
		t.Fatal("the address of no listener")
	}
}

// role is the streaming role on an engine of the test's, as fit gives it.
func role(t *testing.T) *Server {
	t.Helper()
	e := startEngine(t)
	s := New(&app.Host{Engine: e, Upstream: e})
	t.Cleanup(func() { s.Stop(t.Context()) })
	return s
}

// busy holds a port of network, as another process would, and returns its
// address.
func busy(t *testing.T, network string) string {
	t.Helper()
	if network == "udp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		return pc.LocalAddr().String()
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().String()
}

// The role refuses a member that is not one; each listener refuses a port
// another socket holds, which fib's SO_REUSEPORT bind alone would share,
// and the role runs on without it; a stopped role takes no member.
func TestListenerRefusals(t *testing.T) {
	s := role(t)
	if s.Apply(nil) == nil || s.Apply(&config.StreamRTMP{}) == nil {
		t.Fatal("a member that is not the role's")
	}
	if s.Hub() != nil || s.RTC() != nil || s.Addr("rtmp") != nil {
		t.Fatal("a role not yet started")
	}

	tcp, udp := busy(t, "tcp"), busy(t, "udp")
	err := s.Apply(&config.Streaming{
		RTMP:   &config.StreamRTMP{Listen: tcp},
		RTSP:   &config.StreamRTSP{Listen: tcp},
		SRT:    &config.StreamSRT{Listen: udp},
		WebRTC: &config.StreamWebRTC{Listen: udp},
	})
	for _, want := range []string{"rtmp: TCP " + tcp + " is taken", "rtsp: TCP " + tcp + " is taken", "srt: UDP " + udp + " is taken", "webrtc: UDP " + udp + " is taken"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q: %v", want, err)
		}
	}
	for _, proto := range []string{"rtmp", "rtsp", "srt", "webrtc", "other"} {
		if a := s.Addr(proto); a != nil {
			t.Fatalf("%s is bound to %s", proto, a)
		}
	}
	if s.Hub() == nil || s.RTC() != nil {
		t.Fatal("the hub runs without the listeners")
	}

	s.Stop(t.Context())
	s.Stop(t.Context())
	if err := s.Apply(&config.Streaming{}); !errors.Is(err, app.ErrStopped) {
		t.Fatalf("a stopped role: %v", err)
	}
}

// A listener moved to a port another socket holds keeps its old one; one
// whose new config cannot start on its own port is started again as it
// was.
func TestListenerKeptOnError(t *testing.T) {
	s := role(t)
	if err := s.Apply(&config.Streaming{
		RTMP:   &config.StreamRTMP{Listen: "127.0.0.1:0"},
		WebRTC: &config.StreamWebRTC{Listen: "127.0.0.1:0"},
	}); err != nil {
		t.Fatal(err)
	}
	rtmpAddr, rtc := s.Addr("rtmp"), s.RTC()
	if rtmpAddr == nil || rtc == nil {
		t.Fatal("the listeners do not run")
	}

	tcp := busy(t, "tcp")
	err := s.Apply(&config.Streaming{
		RTMP:   &config.StreamRTMP{Listen: tcp},
		WebRTC: &config.StreamWebRTC{Listen: s.Addr("webrtc").String(), PublicIPs: []string{"not-an-ip"}},
	})
	if err == nil || !strings.Contains(err.Error(), "rtmp: TCP "+tcp+" is taken") || !strings.Contains(err.Error(), "webrtc: public_ips") {
		t.Fatalf("the errors: %v", err)
	}
	if a := s.Addr("rtmp"); a == nil || a.String() != rtmpAddr.String() {
		t.Fatalf("rtmp moved: %v, was %v", a, rtmpAddr)
	}
	if s.RTC() == nil || s.RTC() == rtc || s.Addr("webrtc") == nil {
		t.Fatal("the WebRTC port was not started again as it was")
	}
}

// Each listener starts again on a change of its fields, and closes when
// the config leaves it out.
func TestListenersMove(t *testing.T) {
	s := role(t)
	all := func(idle, latency time.Duration, rtc *config.StreamWebRTC) *config.Streaming {
		return &config.Streaming{
			RTMP:   &config.StreamRTMP{Listen: "127.0.0.1:0", IdleTimeout: config.Duration(idle)},
			RTSP:   &config.StreamRTSP{Listen: "127.0.0.1:0", IdleTimeout: config.Duration(idle)},
			SRT:    &config.StreamSRT{Listen: "127.0.0.1:0", Latency: config.Duration(latency), Passphrase: "a-passphrase-of-srt"},
			WebRTC: rtc,
		}
	}
	if err := s.Apply(all(5*time.Second, 100*time.Millisecond, &config.StreamWebRTC{Listen: "127.0.0.1:0"})); err != nil {
		t.Fatal(err)
	}
	was := map[string]string{}
	for _, proto := range []string{"rtmp", "rtsp", "srt", "webrtc"} {
		a := s.Addr(proto)
		if a == nil {
			t.Fatalf("no %s listener", proto)
		}
		was[proto] = a.String()
	}
	rtc := s.RTC()
	if err := s.Apply(all(6*time.Second, 200*time.Millisecond, &config.StreamWebRTC{Listen: "127.0.0.1:0", PublicIPs: []string{"127.0.0.1"},
		ICEServers: []config.ICEServer{{URLs: []string{"stun:stun.example.com"}}}})); err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{"rtmp", "rtsp", "srt", "webrtc"} {
		if a := s.Addr(proto); a == nil || a.String() == was[proto] {
			t.Fatalf("%s did not start again: %v", proto, a)
		}
	}
	if s.RTC() == rtc {
		t.Fatal("the WebRTC server was kept")
	}

	if err := s.Apply(&config.Streaming{}); err != nil {
		t.Fatal(err)
	}
	for _, proto := range []string{"rtmp", "rtsp", "srt", "webrtc"} {
		if a := s.Addr(proto); a != nil {
			t.Fatalf("%s still runs on %s", proto, a)
		}
	}
	if s.Hub() == nil || s.RTC() != nil {
		t.Fatal("the hub stopped with its listeners")
	}
}

// shutdowns is a handler that is told of a shutdown and of the engine's
// close.
type shutdowns struct {
	fib.Handler
	shutdown, engineClose int
}

func (h *shutdowns) OnShutdown(*fib.Connection)    { h.shutdown++ }
func (h *shutdowns) OnEngineClose(*fib.Connection) { h.engineClose++ }

// connLimit passes a shutdown and the engine's close to the protocol's
// handler when it takes them, and drops them when it does not.
func TestConnLimitForwards(t *testing.T) {
	h := &shutdowns{}
	l := newConnLimit(h, 1)
	l.OnShutdown(nil)
	l.OnEngineClose(nil)
	if h.shutdown != 1 || h.engineClose != 1 {
		t.Fatalf("forwarded: %+v", h)
	}
	plain := newConnLimit(struct{ fib.Handler }{}, 1)
	plain.OnShutdown(nil)
	plain.OnEngineClose(nil)
}

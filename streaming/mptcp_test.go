package streaming

import (
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/mptcptest"
)

// TestStreamingMPTCP: streaming.rtmp.mptcp and streaming.rtsp.mptcp listen
// their TCP connections with Multipath TCP, and a client asking for it gets
// it. Only a kernel that offers Multipath TCP can tell, so the test skips
// elsewhere: it checks the kernel's handshake, not a field.
func TestStreamingMPTCP(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	f := startFit(t, doc(t, `{"rtmp": {"listen": "127.0.0.1:0", "mptcp": true},
  "rtsp": {"listen": "127.0.0.1:0", "mptcp": true}}`))
	for _, proto := range []string{"rtmp", "rtsp"} {
		t.Run(proto+" MPTCP on", func(t *testing.T) {
			c, err := mptcptest.Dial(f.addr(t, proto))
			if err != nil {
				t.Fatalf("dial %s: %v", proto, err)
			}
			defer c.Close()
			if !mptcptest.InUse(c) {
				t.Fatalf("%s listens without Multipath TCP under mptcp", proto)
			}
		})
	}
}

// TestStreamingMPTCPOff: without mptcp the listeners are plain TCP even
// when the client asks for Multipath TCP, which it then falls back from:
// a listener never offers more than its member asks.
func TestStreamingMPTCPOff(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	f := startFit(t, doc(t, `{"rtmp": {"listen": "127.0.0.1:0"},
  "rtsp": {"listen": "127.0.0.1:0"}}`))
	for _, proto := range []string{"rtmp", "rtsp"} {
		t.Run(proto+" MPTCP off", func(t *testing.T) {
			c, err := mptcptest.Dial(f.addr(t, proto))
			if err != nil {
				t.Fatalf("dial %s: %v", proto, err)
			}
			defer c.Close()
			if mptcptest.InUse(c) {
				t.Fatalf("%s uses Multipath TCP without mptcp", proto)
			}
		})
	}
}

// TestStreamingMPTCPRestart: mptcp is part of a listener's key, so a change
// of only mptcp starts the RTMP or RTSP listener again, as a change of any
// other field but auth does, on the address it held. This needs no kernel
// support: the listener's own working is what is checked.
func TestStreamingMPTCPRestart(t *testing.T) {
	t.Run("RTMP MPTCP restart", func(t *testing.T) {
		s := role(t)
		addr := freePort(t)
		if err := s.Apply(&config.Streaming{RTMP: &config.StreamRTMP{Listen: addr}}); err != nil {
			t.Fatal(err)
		}
		before := s.rtmp
		if err := s.Apply(&config.Streaming{RTMP: &config.StreamRTMP{Listen: addr, MPTCP: true}}); err != nil {
			t.Fatal(err)
		}
		if s.rtmp == before {
			t.Fatal("a change of mptcp kept the RTMP listener")
		}
		if got := s.Addr("rtmp"); got == nil || got.String() != addr {
			t.Fatalf("the restarted RTMP listener is at %v, want %s", got, addr)
		}
	})
	t.Run("RTSP MPTCP restart", func(t *testing.T) {
		s := role(t)
		addr := freePort(t)
		if err := s.Apply(&config.Streaming{RTSP: &config.StreamRTSP{Listen: addr}}); err != nil {
			t.Fatal(err)
		}
		before := s.rtsp
		if err := s.Apply(&config.Streaming{RTSP: &config.StreamRTSP{Listen: addr, MPTCP: true}}); err != nil {
			t.Fatal(err)
		}
		if s.rtsp == before {
			t.Fatal("a change of mptcp kept the RTSP listener")
		}
		if got := s.Addr("rtsp"); got == nil || got.String() != addr {
			t.Fatalf("the restarted RTSP listener is at %v, want %s", got, addr)
		}
	})
}

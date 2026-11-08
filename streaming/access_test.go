package streaming

import (
	"io"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/lesismal/fib/media/srt"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// accessDoc is a config with the RTMP, RTSP and SRT listeners, with global
// as the global access and own as the role's, JSON or "null".
func accessDoc(global, own string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "access": ` + global + `,
  "streaming": {
    "rtmp": {"listen": "127.0.0.1:0"},
    "rtsp": {"listen": "127.0.0.1:0"},
    "srt": {"listen": "127.0.0.1:0"},
    "access": ` + own + `
  }
}`
}

// rtmpServed reports whether the RTMP listener answers a new connection's
// C0 and C1 with its S0; a refused one is closed.
func rtmpServed(t *testing.T, addr string) bool {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, waitTimeout)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(waitTimeout))
	if _, err := c.Write(append([]byte{3}, make([]byte, 1536)...)); err != nil {
		return false
	}
	b := make([]byte, 1)
	_, err = io.ReadFull(c, b)
	return err == nil && b[0] == 3
}

// rtspServed reports whether c, an RTSP connection, is answered OPTIONS.
func rtspServed(c net.Conn) bool {
	_ = c.SetDeadline(time.Now().Add(waitTimeout))
	if _, err := io.WriteString(c, "OPTIONS * RTSP/1.0\r\nCSeq: 1\r\n\r\n"); err != nil {
		return false
	}
	b := make([]byte, 9)
	_, err := io.ReadFull(c, b)
	return err == nil && string(b) == "RTSP/1.0 "
}

func dialTCP(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, waitTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// srtServed reports whether a new SRT caller's handshake is answered: it
// connects; a refused one's handshake times out.
func srtServed(t *testing.T, addr string) bool {
	t.Helper()
	e := startEngine(t)
	done := make(chan error, 1)
	s, err := srt.Dial(e, addr, srt.ClientConfig{
		StreamID:         "#!::r=live/access,m=publish",
		HandshakeTimeout: 500 * time.Millisecond,
		OnConnect:        func(*srt.Session) { done <- nil },
		OnClose: func(err error) {
			select {
			case done <- err:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(waitTimeout):
		t.Fatal("the SRT handshake did not end")
		return false
	}
}

// The role's access decides first, then the global one: a refused client's
// RTMP and RTSP connections are closed as accepted, and its SRT handshake
// goes unanswered. A reload that changes the access alone, or the global
// access alone, starts no listener again, and keeps the connections open.
func TestAccess(t *testing.T) {
	f := startFit(t, accessDoc("null", `{"deny": ["127.0.0.1"]}`))
	addrs := map[string]string{}
	for _, proto := range []string{"rtmp", "rtsp", "srt"} {
		addrs[proto] = f.addr(t, proto)
	}
	want := func(what string, want bool) {
		t.Helper()
		rtmp, rtsp, srt := rtmpServed(t, addrs["rtmp"]), rtspServed(dialTCP(t, addrs["rtsp"])), srtServed(t, addrs["srt"])
		if rtmp != want || rtsp != want || srt != want {
			t.Fatalf("%s: rtmp %v, rtsp %v, srt %v served, want %v", what, rtmp, rtsp, srt, want)
		}
		for proto, a := range addrs {
			if got := f.addr(t, proto); got != a {
				t.Fatalf("%s: %s moved from %s to %s", what, proto, a, got)
			}
		}
	}
	want("the role's deny", false)

	f.reload(t, accessDoc(`{"deny": ["127.0.0.0/8"]}`, `{"allow": ["127.0.0.1"]}`))
	want("the role's allow over a global deny", true)
	kept := dialRTSP(t, addrs["rtsp"])
	if code, _ := kept.do("OPTIONS", "rtsp://"+addrs["rtsp"]+"/"); code != 200 {
		t.Fatalf("OPTIONS: %d", code)
	}

	f.reload(t, accessDoc(`{"deny": ["127.0.0.0/8"]}`, "null"))
	want("the global deny", false)
	// A connection served before the access changed is kept.
	if code, _ := kept.do("OPTIONS", "rtsp://"+addrs["rtsp"]+"/"); code != 200 {
		t.Fatalf("OPTIONS on the connection kept: %d", code)
	}

	// The global access changed, the role's member not.
	f.reload(t, accessDoc("null", "null"))
	want("no access", true)
	if f.role().m.restarts.Load() != 0 {
		t.Fatal("the hub started again")
	}

	// The access's list files are watched, as certificate files are.
	c := &config.Config{Streaming: &config.Streaming{Access: &config.Access{AllowFiles: []string{"a.txt"}, DenyFiles: []string{"d.txt"}}}}
	if got := app.CertFiles(c); !slices.Equal(got, []string{"a.txt", "d.txt"}) {
		t.Fatalf("the files watched: %q", got)
	}
}

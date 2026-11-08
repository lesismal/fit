package media

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// mediaAccessDoc is a config with the media server and its routes, with
// global as the global access and own as webrtc.media's, JSON or "null".
// The web listener, which takes the offers, serves loopback whatever the
// global access says.
func mediaAccessDoc(global, own string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "access": {"allow": ["127.0.0.1"]}}],
  "access": ` + global + `,
  "routes": [{"match": {"path": ["/whip/*"]}, "handler": {"module": "whip"}}],
  "webrtc": {"media": {"listen": "127.0.0.1:0", "connect_timeout": "3s", "access": ` + own + `}}
}`
}

// webrtc.media's access decides first, then the global one: a publisher on
// a refused address is answered, but its ICE checks go unanswered and it
// never connects; a reload that changes the access alone, or the global
// access alone, keeps the port and the sessions connected.
func TestMediaAccess(t *testing.T) {
	f := startFit(t, mediaAccessDoc("null", `{"deny": ["127.0.0.1"]}`))
	clients := startEngine(t)
	addrs := f.media().Addrs()
	n := 0
	var kept *publisher
	want := func(what string, want bool) {
		t.Helper()
		n++
		p := newPublisher(t, clients)
		if resp, body := p.signal(t, f.base+"/whip/cam"+string(rune('0'+n))); resp.StatusCode != http.StatusCreated {
			t.Fatalf("%s: %d %s", what, resp.StatusCode, body)
		}
		var got bool
		select {
		case <-p.conn:
			got = true
		case <-time.After(2 * time.Second):
		}
		if got != want {
			t.Fatalf("%s: connected %v, want %v", what, got, want)
		}
		if got && kept == nil {
			kept = p
		}
		if a := f.media().Addrs(); !slices.Equal(a, addrs) {
			t.Fatalf("%s: the port moved from %v to %v", what, addrs, a)
		}
	}
	want("the member's deny", false)

	f.reload(t, mediaAccessDoc(`{"deny": ["127.0.0.0/8"]}`, `{"allow": ["127.0.0.1"]}`))
	want("the member's allow over a global deny", true)
	kept.send()

	f.reload(t, mediaAccessDoc(`{"deny": ["127.0.0.0/8"]}`, "null"))
	want("the global deny", false)

	// The global access changed, the member not.
	f.reload(t, mediaAccessDoc("null", "null"))
	want("no access", true)
	if f.media().m.restarts.Load() != 0 {
		t.Fatal("the media server started again")
	}
	// The publisher connected before the access changed is still there.
	if p, _ := f.media().Sessions(); p < 2 || kept.pc.ConnectionState().String() != "connected" {
		t.Fatalf("%d publishers, the first %s", p, kept.pc.ConnectionState())
	}

	// The access's list files are watched, as certificate files are.
	c := &config.Config{WebRTC: &config.WebRTC{Media: &config.WebRTCMedia{Listen: "127.0.0.1:0",
		Access: &config.Access{AllowFiles: []string{"a.txt"}, DenyFiles: []string{"d.txt"}}}}}
	if got := app.CertFiles(c); !slices.Equal(got, []string{"a.txt", "d.txt"}) {
		t.Fatalf("the files watched: %q", got)
	}
}

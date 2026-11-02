package streaming

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/flv"
	"github.com/lesismal/fib/media/rtmp"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

const waitTimeout = 15 * time.Second

// The credentials of the test policy, live.
const (
	pubToken  = "publisher-token-0123456789"
	viewToken = "viewer-token-0123456789"
)

// fit is fit running a config with the streaming role, as fit runs it: the
// config compiled and served, its roles moved by an app.Manager.
type fit struct {
	configs *admin.Configs
	apps    *app.Manager
	server  *listener.Server
	up      *fib.Engine
	rt      *runtime.Runtime
	stopUp  func()
	base    string // http://127.0.0.1:port
	stopped bool
}

func startFit(t *testing.T, doc string) *fit {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{}
	if f.up, f.stopUp, err = upstream.StartEngine(); err != nil {
		t.Fatal(err)
	}
	env := runtime.Env{Metrics: observe.NewMetrics(), Engine: f.up}
	g, err := runtime.Compile(c, env)
	if err != nil {
		f.stopUp()
		t.Fatal(err)
	}
	f.rt = new(runtime.Runtime)
	f.rt.Swap(g)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	f.configs = admin.NewConfigs(f.rt, f.server, env, c, []byte(doc), admin.SourceFile, 10)
	f.apps = app.NewManager(f.server.Engine(), f.up, nil, f.configs.EditMember)
	if err := f.apps.Apply(c); err != nil {
		f.stop()
		t.Fatal(err)
	}
	f.configs.UseApps(f.apps)
	t.Cleanup(f.stop)
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	return f
}

func (f *fit) stop() {
	if f.stopped {
		return
	}
	f.stopped = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	f.apps.Stop(ctx)
	_ = f.server.Close()
	f.rt.Close()
	f.stopUp()
}

func (f *fit) reload(t *testing.T, doc string) {
	t.Helper()
	r, err := f.configs.Apply([]byte(doc), "test")
	if err != nil {
		t.Fatalf("reload: %v %+v", err, r)
	}
}

func (f *fit) role() *Server { return Lookup(f.up) }

// addr returns the address of the role's listener of proto.
func (f *fit) addr(t *testing.T, proto string) string {
	t.Helper()
	s := f.role()
	if s == nil {
		t.Fatal("the role does not run")
	}
	a := s.Addr(proto)
	if a == nil {
		t.Fatalf("no %s listener", proto)
	}
	return a.String()
}

// livePolicy is the media_auth policy live: a publishing and a viewing
// token, alice with a plaintext password and bob with a bcrypt hash.
func livePolicy(t *testing.T) string {
	h, err := bcrypt.GenerateFromPassword([]byte("bob-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return `{"name": "live", "secret": "a-secret-of-sixteen-bytes",
	  "tokens": [{"name": "pub", "token": "` + pubToken + `", "publish": ["live/*"]},
	             {"name": "view", "token": "` + viewToken + `", "play": ["live/*"]}],
	  "users": [{"name": "alice", "password": "alice-pw", "publish": ["live/alice"], "play": ["*"]},
	            {"name": "bob", "password_hash": "` + string(h) + `", "publish": ["live/bob"], "play": ["*"]}]}`
}

// doc is a config with the streaming role whose JSON is streaming, the
// policy live, and routes of every handler behind it.
func doc(t *testing.T, streaming string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": [` + livePolicy(t) + `],
  "streaming": ` + streaming + `,
  "routes": [
    {"match": {"path": ["/flv/*"]}, "handler": {"module": "httpflv", "params": {"prefix": "/flv/", "auth": "live"}}},
    {"match": {"path": ["/hls/*"]}, "handler": {"module": "hls", "params": {"prefix": "/hls/", "auth": "live", "target_duration": "1s"}}},
    {"match": {"path": ["/whip/*"]}, "handler": {"module": "stream_whip", "params": {"auth": "live"}}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "stream_whep", "params": {"auth": "live"}}}
  ]
}`
}

const allListeners = `{
  "rtmp": {"listen": "127.0.0.1:0", "auth": "live"},
  "rtsp": {"listen": "127.0.0.1:0", "auth": "live", "auth_methods": ["digest", "basic"]},
  "srt": {"listen": "127.0.0.1:0", "auth": "live"},
  "webrtc": {"listen": "127.0.0.1:0"}
}`

// startEngine starts an engine for clients.
func startEngine(t *testing.T) *fib.Engine {
	t.Helper()
	cfg := fib.DefaultConfig()
	cfg.LogSettings = false
	e, err := fib.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.Run() }()
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			e.Stop()
			<-done
			_ = e.Close()
		})
	})
	return e
}

// fixture is the tracks and packets of an H.264 and AAC stream, repeated
// to last about want.
func fixture(t testing.TB, want time.Duration) ([]*av.Track, []*av.Packet) {
	t.Helper()
	b, err := os.ReadFile("testdata/h264_aac.flv")
	if err != nil {
		t.Fatal(err)
	}
	var tracks []*av.Track
	var once []*av.Packet
	dec := flv.NewDecoder(flv.DecoderConfig{
		OnTracks: func(tr []*av.Track) { tracks = tr },
		OnPacket: func(p *av.Packet) { once = append(once, p) },
	})
	if err := flv.NewDemuxer(flv.DemuxerConfig{}).Feed(b, func(tag flv.Tag) error {
		return dec.WriteTag(tag.Type, tag.Timestamp, tag.Data)
	}); err != nil {
		t.Fatal(err)
	}
	dec.Flush()
	if len(once) == 0 {
		t.Fatal("the fixture has no packets")
	}
	span := once[len(once)-1].DTS + 40*time.Millisecond
	var packets []*av.Packet
	for off := time.Duration(0); off < want; off += span {
		for _, p := range once {
			q := *p
			q.DTS += off
			q.PTS += off
			packets = append(packets, &q)
		}
	}
	return tracks, packets
}

// publishRTMP publishes the fixture to url, returning the client, which
// keeps the stream until closed, or the refusal.
func publishRTMP(t *testing.T, e *fib.Engine, url string, length time.Duration) (*rtmp.ClientConn, error) {
	t.Helper()
	return publishRTMPWith(t, e, url, length, nil)
}

// publishRTMPWith is publishRTMP with the fixture's tracks changed by
// change, when not nil.
func publishRTMPWith(t *testing.T, e *fib.Engine, url string, length time.Duration, change func([]*av.Track) []*av.Track) (*rtmp.ClientConn, error) {
	t.Helper()
	ready := make(chan error, 1)
	cc, err := rtmp.Publish(e, url, rtmp.ClientConfig{}, func(_ *rtmp.ClientConn, err error) { ready <- err })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
	case <-time.After(waitTimeout):
		t.Fatal("the publish was not answered")
	}
	tracks, packets := fixture(t, length)
	if change != nil {
		tracks = change(tracks)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		<-done
		_ = cc.Close()
	})
	cc.OnTracks(tracks)
	go feed(packets, cc.OnPacket, stop, done)
	return cc, nil
}

// feed sends packets in real time, the first second's at once, until
// stop.
func feed(packets []*av.Packet, send func(*av.Packet), stop, done chan struct{}) {
	defer close(done)
	start := time.Now().Add(-time.Second)
	for _, p := range packets {
		if wait := time.Until(start.Add(p.DTS)); wait > 0 {
			select {
			case <-stop:
				return
			case <-time.After(wait):
			}
		}
		select {
		case <-stop:
			return
		default:
		}
		send(p)
	}
}

// rtmpCode returns the status code of an RTMP client's refusal.
func rtmpCode(err error) string {
	var se *rtmp.StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	if err == nil {
		return "no error"
	}
	return "not a status: " + err.Error()
}

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// get sends a GET to url with what set adds, and returns the response
// with up to max bytes of its body.
func get(t *testing.T, url string, max int64, set func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	if set != nil {
		set(req)
	}
	client := &http.Client{Timeout: waitTimeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, max))
	return resp, b
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

var _ = strings.Contains

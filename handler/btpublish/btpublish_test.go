package btpublish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"

	_ "github.com/lesismal/fit/handler/static"
)

// hang bounds every wait: a guard against a hang, not a measure of speed.
const hang = 2 * time.Minute

var client = &http.Client{Timeout: hang}

// serve serves routes on loopback, on the engine it returns, until the
// test ends.
func serve(t *testing.T, dir string) (string, *fib.Engine) {
	t.Helper()
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stopUp)
	c, err := config.Load(fmt.Appendf(nil, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [
    {"match": {"path": ["/files/*"]}, "handler": {"module": "static", "params": {"root": %q, "strip_prefix": "/files"}}},
    {"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_publish"}},
    {"match": {"path": ["/only/*"]}, "handler": {"module": "bt_publish", "params": {"publish": ["pub", "none"]}}}
  ]
}`, dir))
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	g, err := runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics(), Engine: up})
	if err != nil {
		t.Fatal(err)
	}
	rt.Swap(g)
	srv, err := listener.Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = srv.Close()
		rt.Close()
	})
	return "http://" + srv.Addrs()[0].Addr.String(), up
}

// role starts a bittorrent.client on e with the member js.
func role(t *testing.T, e *fib.Engine, js string) *bittorrent.Role {
	t.Helper()
	c, err := config.Load([]byte(`{"bittorrent": {"client": ` + js + `}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := bittorrent.New(&app.Host{ID: bittorrent.ID, Engine: e, Upstream: e})
	if err := r.Apply(c.BitTorrent.Client); err != nil {
		r.Stop(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Stop(context.Background()) })
	return r
}

// member is a client on loopback without the DHT, keeping its state in
// dataDir, with extra fields.
func member(dataDir, extra string) string {
	s := `{"listen": "127.0.0.1:0", "data_dir": ` + strconv.Quote(dataDir) + `, "dht": false, "save_interval": "50ms"`
	if extra != "" {
		s += ", " + extra
	}
	return s + "}"
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func get(t *testing.T, method, url string) reply {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, b}
}

// waitFor waits for cond, polling, failing the test after hang.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestRoutesAndWebSeed publishes a directory whose web seed is fit's own
// static route, serves its index, .torrent file and magnet link, and
// downloads the file from the web seed alone, once the role is stopped.
func TestRoutesAndWebSeed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "share")
	data := make([]byte, 700<<10+11)
	rnd := rand.New(rand.NewPCG(1, 2))
	for i := range data {
		data[i] = byte(rnd.Uint32())
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub dir", "big file.bin"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	base, up := serve(t, dir)

	// No client: 503, saying so.
	if r := get(t, "GET", base+"/bt/"); r.status != http.StatusServiceUnavailable || !bytes.Contains(r.body, []byte("bittorrent.client")) {
		t.Fatalf("%d %s", r.status, r.body)
	}

	pub := role(t, up, member(t.TempDir(), `"publish": [{"name": "pub", "dir": `+strconv.Quote(dir)+
		`, "check_interval": "50ms", "web_seed": `+strconv.Quote(base+"/files/")+`}]`))
	var idx struct {
		Publish []bittorrent.PublishStatus `json:"publish"`
	}
	waitFor(t, "the torrent seeding in the index", func() bool {
		r := get(t, "GET", base+"/bt/index.json")
		if r.status != http.StatusOK || r.header.Get("Content-Type") != "application/json" {
			return false
		}
		if err := json.Unmarshal(r.body, &idx); err != nil {
			t.Fatal(err)
		}
		return len(idx.Publish) == 1 && len(idx.Publish[0].Torrents) == 1 && idx.Publish[0].Torrents[0].State == "seeding"
	})
	tr := idx.Publish[0].Torrents[0]
	if tr.Path != "sub dir/big file.bin" || tr.TorrentURL != tr.ID+".torrent" || tr.MagnetURL != tr.ID+".magnet" {
		t.Fatalf("%+v", tr)
	}

	r := get(t, "GET", base+"/bt/"+tr.TorrentURL)
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "application/x-bittorrent" ||
		!strings.Contains(r.header.Get("Content-Disposition"), "big file.bin.torrent") {
		t.Fatalf("%d %v", r.status, r.header)
	}
	raw := r.body
	mi, err := metainfo.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if want := base + "/files/sub%20dir/big%20file.bin"; len(mi.URLList) != 1 || mi.URLList[0] != want {
		t.Fatalf("web seeds %v, want %s", mi.URLList, want)
	}
	if r := get(t, "GET", base+"/bt/"+tr.MagnetURL); r.status != http.StatusOK || strings.TrimSpace(string(r.body)) != tr.Magnet {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if r := get(t, "HEAD", base+"/bt/"+tr.TorrentURL); r.status != http.StatusOK {
		t.Fatalf("HEAD %d", r.status)
	}
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/bt/" + strings.Repeat("0", 40) + ".torrent", http.StatusNotFound},
		{"GET", "/bt/" + strings.Repeat("0", 40) + ".magnet", http.StatusNotFound},
		{"GET", "/bt/other", http.StatusNotFound},
		{"POST", "/bt/", http.StatusMethodNotAllowed},
	} {
		if r := get(t, c.method, base+c.path); r.status != c.status {
			t.Errorf("%s %s: %d, want %d", c.method, c.path, r.status, c.status)
		}
	}
	// A handler of named entries lists one it lacks with its error.
	r = get(t, "GET", base+"/only/")
	if err := json.Unmarshal(r.body, &idx); err != nil || len(idx.Publish) != 2 || idx.Publish[1].Name != "none" ||
		idx.Publish[1].Error == "" || len(idx.Publish[0].Torrents) != 1 {
		t.Fatalf("%s %v", r.body, err)
	}

	// The web seed alone: the role stops, and a client on an engine of its
	// own, with no peers, downloads from the static route.
	pub.Stop(context.Background())
	if r := get(t, "GET", base+"/bt/"); r.status != http.StatusServiceUnavailable {
		t.Fatalf("a stopped role: %d", r.status)
	}
	other, stopOther, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stopOther()
	dl := t.TempDir()
	l := role(t, other, member(t.TempDir(), `"download_dir": `+strconv.Quote(dl)))
	s, err := l.Add(bittorrent.AddRequest{Torrent: raw})
	if err != nil {
		t.Fatal(err)
	}
	tor, err := l.Torrent(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-tor.Done():
	case <-time.After(hang):
		st, _ := l.Get(s.ID)
		t.Fatalf("the download did not finish: %+v", st)
	}
	got, err := os.ReadFile(filepath.Join(dl, "big file.bin"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded %d bytes, want %d: %v", len(got), len(data), err)
	}
	if st := tor.Stats(); st.Peers != 0 {
		t.Fatalf("%d peers", st.Peers)
	}
}

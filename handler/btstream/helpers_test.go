package btstream

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/bencode"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// hang bounds every wait of these tests: a guard against a hang, never a
// measure of speed, so it is generous for slow CI runners.
const hang = 90 * time.Second

const (
	// playToken may play every torrent; pubToken may also add them.
	playToken = "a-player-token-0123456789"
	pubToken  = "a-publisher-token-0123456789"
	authConf  = `[{"name": "site", "tokens": [{"name": "player", "token": "` + playToken + `", "play": ["bt/*"]},
		{"name": "pub", "token": "` + pubToken + `", "play": ["bt/*"], "publish": ["bt/*"]}]}]`
)

// fit is fit serving a bt_stream route on /bt/, with a bittorrent.client
// role on its engine, as fit runs them.
type fit struct {
	rt     *runtime.Runtime
	server *listener.Server
	base   string
	role   *bittorrent.Role
	engine *fib.Engine
	dl     string
}

// startFit serves a bt_stream route of params, with the media_auth
// policies auth (a JSON list, or ""), and a role downloading into a
// directory of the test's.
func startFit(t *testing.T, params, auth string) *fit {
	t.Helper()
	if auth == "" {
		auth = "[]"
	}
	doc := `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": ` + auth + `,
  "routes": [{"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_stream", "params": ` + params + `}}]
}`
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{rt: new(runtime.Runtime), engine: up, dl: t.TempDir()}
	c, err := config.Load([]byte(doc))
	if err != nil {
		stopUp()
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics(), Engine: up})
	if err != nil {
		stopUp()
		t.Fatal(err)
	}
	f.rt.Swap(g)
	if f.server, err = listener.Start(f.rt); err != nil {
		stopUp()
		t.Fatal(err)
	}
	f.role = startRole(t, up, clientJSON(t.TempDir(), f.dl))
	t.Cleanup(func() {
		_ = f.server.Close()
		f.rt.Close()
		stopUp()
	})
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	return f
}

// handler returns the bt_stream handler of the running config.
func (f *fit) handler(t *testing.T) *BTStream {
	t.Helper()
	g := f.rt.Current()
	defer g.Release()
	for _, m := range g.Modules() {
		if h, ok := m.(*BTStream); ok {
			return h
		}
	}
	t.Fatal("no bt_stream handler")
	return nil
}

// startEngine starts an engine of its own, for a seeding role.
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
	t.Cleanup(func() {
		e.Stop()
		<-done
		_ = e.Close()
	})
	return e
}

// clientJSON is a member on loopback, without the DHT or LSD.
func clientJSON(dataDir, downloadDir string) string {
	return `{"listen": "127.0.0.1:0", "data_dir": ` + strconv.Quote(dataDir) + `, "download_dir": ` +
		strconv.Quote(downloadDir) + `, "dht": false, "save_interval": "50ms"}`
}

// startRole starts a role on e and stops it when the test ends.
func startRole(t *testing.T, e *fib.Engine, js string) *bittorrent.Role {
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

// content is a torrent of the tests: a directory show with a video and a
// text file.
type content struct {
	torrent []byte
	mi      *metainfo.MetaInfo
	id      string
	video   []byte
	notes   []byte
	src     string
}

// makeContent writes show/ep1.mp4 of n random bytes and show/notes.txt in
// a new directory, and builds their torrent with pieces of pieceLen.
func makeContent(t *testing.T, n int, pieceLen int64) *content {
	t.Helper()
	src := t.TempDir()
	dir := filepath.Join(src, "show")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	video := make([]byte, n)
	rnd := rand.New(rand.NewPCG(uint64(n), 11))
	for i := range video {
		video[i] = byte(rnd.Uint32())
	}
	notes := []byte("notes of the show\n")
	if err := os.WriteFile(filepath.Join(dir, "ep1.mp4"), video, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), notes, 0o644); err != nil {
		t.Fatal(err)
	}
	b := metainfo.Builder{Version: metainfo.Hybrid, PieceLength: pieceLen}
	mi, err := b.BuildPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bencode.Marshal(mi)
	if err != nil {
		t.Fatal(err)
	}
	return &content{torrent: raw, mi: mi, id: mi.Info.InfoHashV1().String(), video: video, notes: notes, src: src}
}

// seed starts a role on an engine of its own seeding c, and returns its
// peer address.
func seed(t *testing.T, c *content) string {
	t.Helper()
	r := startRole(t, startEngine(t), clientJSON(t.TempDir(), c.src))
	s, err := r.Add(bittorrent.AddRequest{Torrent: c.torrent})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the seeder to have every piece", func() bool {
		tor, err := r.Torrent(s.ID)
		return err == nil && tor.HaveAll()
	})
	return "127.0.0.1:" + strconv.Itoa(int(r.Addr().Port()))
}

// waitFor polls cond, failing the test after hang.
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

// result is a response, or the error of its request.
type result struct {
	resp *http.Response
	body []byte
	err  error
}

// fetch sends a request, the header fields hdr in pairs.
func fetch(method, url string, hdr ...string) result {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return result{err: err}
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	client := &http.Client{Timeout: hang}
	resp, err := client.Do(req)
	if err != nil {
		return result{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return result{resp: resp, body: body, err: err}
}

// get fetches path, failing the test on an error.
func (f *fit) get(t *testing.T, method, path string, hdr ...string) result {
	t.Helper()
	r := fetch(method, f.base+path, hdr...)
	if r.err != nil {
		t.Fatalf("%s %s: %v", method, path, r.err)
	}
	return r
}

// must fetches path and fails unless it is answered want.
func (f *fit) must(t *testing.T, method, path string, want int, hdr ...string) result {
	t.Helper()
	r := f.get(t, method, path, hdr...)
	if r.resp.StatusCode != want {
		t.Fatalf("%s %s: %d %q, want %d", method, path, r.resp.StatusCode, r.body, want)
	}
	return r
}

// rawGet opens a connection to f and sends a GET of path on it, with the
// receive buffer set to rcvbuf when not 0, and returns the connection with
// the response's header read.
func (f *fit) rawGet(t *testing.T, path string, rcvbuf int, hdr ...string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if rcvbuf > 0 {
		_ = conn.(*net.TCPConn).SetReadBuffer(rcvbuf)
	}
	req := "GET " + path + " HTTP/1.1\r\nHost: fit\r\n"
	for i := 0; i+1 < len(hdr); i += 2 {
		req += hdr[i] + ": " + hdr[i+1] + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	return conn, bufio.NewReaderSize(conn, 4096)
}

// readHead reads a response's status line and header from br; its body
// reads on from br.
func readHead(t *testing.T, conn net.Conn, br *bufio.Reader) *http.Response {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(hang))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// fileIndex returns the index of the file of c whose path ends in name.
func (f *fit) fileIndex(t *testing.T, id, name string) int {
	t.Helper()
	s, err := f.role.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range s.Files {
		if strings.HasSuffix(x.Path, "/"+name) {
			return x.Index
		}
	}
	t.Fatalf("no file %s in %+v", name, s.Files)
	return -1
}

func rangeHdr(from, to int) string { return fmt.Sprintf("bytes=%d-%d", from, to) }

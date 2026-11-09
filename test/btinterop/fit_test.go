// Package btinterop checks fit's BitTorrent roles against other clients:
// anacrolix/torrent in the test's process, and libtorrent-rasterbar through
// libtorrent_peer.py and its Python binding. fit is built from this
// checkout and run as a user runs it, steered through its Admin API.
//
// The anacrolix cases run wherever go test does (uTP against anacrolix
// needs cgo, for go-libutp); the libtorrent cases need python3-libtorrent
// and run only with FIT_LIBTORRENT=1, as run.sh runs them in a container
// and CI's bittorrent-interop job runs them on its runner.
package btinterop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// hang bounds every wait: a guard against a hang, never a measure of
// speed, so it is generous for slow CI runners.
const hang = 120 * time.Second

// fitBin is the fit binary TestMain builds.
var fitBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fit-btinterop")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fitBin = filepath.Join(dir, "fit")
	// fit's own module, two directories up, built as fit builds.
	build := exec.Command("go", "build", "-C", "../..", "-o", fitBin, "./cmd/fit")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building fit:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// freePort returns a loopback port that TCP and UDP are both free on, as
// far as can be told.
func freePort(t *testing.T) int {
	t.Helper()
	for range 50 {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(port))
		_ = l.Close()
		if err == nil {
			_ = u.Close()
			return port
		}
	}
	t.Fatal("no free port")
	return 0
}

// syncBuffer keeps what a process writes, for a failing test to show.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fitProc is a running fit.
type fitProc struct {
	cmd   *exec.Cmd
	out   *syncBuffer
	admin string // the Admin API's base URL
	web   string // the web listener's base URL
	bt    int    // the client's port, TCP and UDP, if it runs
	udp   int    // the tracker's UDP port, if it runs
}

// fitConfig is what a test asks of fit: the bittorrent.client member
// (JSON, without listen, which startFit picks), the tracker member (with
// "listen": "UDP" for a port startFit picks), routes, and media_auth.
type fitConfig struct {
	client    string
	tracker   string
	routes    string
	mediaAuth string
	webPort   int // the web listener's port; one free when 0
}

// startFit runs fit on c, waiting until its Admin API answers for the
// roles c sets; fit is killed when the test ends, its output logged if the
// test failed.
func startFit(t *testing.T, c fitConfig) *fitProc {
	t.Helper()
	f := &fitProc{out: new(syncBuffer)}
	adminAddr := "127.0.0.1:" + strconv.Itoa(freePort(t))
	f.admin = "http://" + adminAddr
	var parts []string
	parts = append(parts, `"admin": {"address": "`+adminAddr+`"}`)
	routes := c.routes
	if routes == "" {
		routes = "[]"
	}
	webPort := c.webPort
	if webPort == 0 {
		webPort = freePort(t)
	}
	webAddr := "127.0.0.1:" + strconv.Itoa(webPort)
	f.web = "http://" + webAddr
	parts = append(parts, `"listeners": [{"name": "web", "address": "`+webAddr+`"}]`, `"routes": `+routes)
	if c.mediaAuth != "" {
		parts = append(parts, `"media_auth": `+c.mediaAuth)
	}
	var bt []string
	if c.client != "" {
		f.bt = freePort(t)
		member := strings.TrimSpace(c.client)
		member = `{"listen": "127.0.0.1:` + strconv.Itoa(f.bt) + `", ` + member[1:]
		bt = append(bt, `"client": `+member)
	}
	if c.tracker != "" {
		member := c.tracker
		if strings.Contains(member, `"UDP"`) {
			f.udp = freePort(t)
			member = strings.Replace(member, `"UDP"`, `"127.0.0.1:`+strconv.Itoa(f.udp)+`"`, 1)
		}
		bt = append(bt, `"tracker": `+member)
	}
	if len(bt) > 0 {
		parts = append(parts, `"bittorrent": {`+strings.Join(bt, ", ")+`}`)
	}
	doc := "{\n" + strings.Join(parts, ",\n") + "\n}\n"
	cfg := filepath.Join(t.TempDir(), "fit.json")
	if err := os.WriteFile(cfg, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	f.cmd = exec.Command(fitBin, "run", "-c", cfg)
	f.cmd.Stdout, f.cmd.Stderr = f.out, f.out
	if err := f.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- f.cmd.Wait() }()
	t.Cleanup(func() {
		_ = f.cmd.Process.Kill()
		<-exited
		if t.Failed() {
			t.Logf("fit's config:\n%s\nfit's output:\n%s", doc, f.out.String())
		}
	})
	ready := "/bittorrent/torrents"
	if c.client == "" {
		ready = "/bittorrent/tracker/stats"
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-exited:
			exited <- err
			t.Fatalf("fit exited: %v", err)
		default:
		}
		if resp, err := http.Get(f.admin + ready); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return f
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("fit's BitTorrent roles did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// call sends a request to fit's Admin API and decodes its JSON answer
// into out, failing the test unless the status is want.
func (f *fitProc) call(t *testing.T, method, path string, body any, want int, out any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.admin+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, data, want)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, data)
		}
	}
}

// status is the part of a torrent's status the tests read.
type status struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	State      string  `json:"state"`
	Error      string  `json:"error"`
	Complete   bool    `json:"complete"`
	Size       int64   `json:"size"`
	Progress   float64 `json:"progress"`
	Pieces     int     `json:"pieces"`
	PiecesHave int     `json:"pieces_have"`
	Downloaded int64   `json:"downloaded"`
	Uploaded   int64   `json:"uploaded"`
	Peers      int     `json:"peers"`
}

type peerStatus struct {
	Addr       string `json:"addr"`
	Client     string `json:"client"`
	UTP        bool   `json:"utp"`
	Encrypted  bool   `json:"encrypted"`
	Downloaded int64  `json:"downloaded"`
	Uploaded   int64  `json:"uploaded"`
}

// add adds a torrent through the Admin API: body is its JSON.
func (f *fitProc) add(t *testing.T, body map[string]any) status {
	t.Helper()
	var st status
	f.call(t, http.MethodPost, "/bittorrent/torrents", body, http.StatusCreated, &st)
	return st
}

func (f *fitProc) get(t *testing.T, id string) status {
	t.Helper()
	var st status
	f.call(t, http.MethodGet, "/bittorrent/torrents/"+id, nil, http.StatusOK, &st)
	return st
}

// peerWatch records the peers a torrent of fit has had connected, as its
// Admin API lists them, until stopped.
type peerWatch struct {
	mu    sync.Mutex
	seen  map[string]peerStatus
	stop  chan struct{}
	ended chan struct{}
}

func (f *fitProc) watchPeers(t *testing.T, id string) *peerWatch {
	t.Helper()
	w := &peerWatch{seen: map[string]peerStatus{}, stop: make(chan struct{}), ended: make(chan struct{})}
	go func() {
		defer close(w.ended)
		for {
			resp, err := http.Get(f.admin + "/bittorrent/torrents/" + id + "/peers")
			if err == nil {
				var out struct {
					Peers []peerStatus `json:"peers"`
				}
				if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&out) == nil {
					w.mu.Lock()
					for _, p := range out.Peers {
						w.seen[p.Addr] = p
					}
					w.mu.Unlock()
				}
				resp.Body.Close()
			}
			select {
			case <-w.stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	t.Cleanup(w.done)
	return w
}

func (w *peerWatch) done() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	<-w.ended
}

func (w *peerWatch) peers() []peerStatus {
	w.done()
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []peerStatus
	for _, p := range w.seen {
		out = append(out, p)
	}
	return out
}

// waitComplete waits for fit's torrent id to have every piece it wants.
func (f *fitProc) waitComplete(t *testing.T, id string) status {
	t.Helper()
	deadline := time.Now().Add(hang)
	for {
		st := f.get(t, id)
		if st.Complete && st.State != "checking" {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("fit's torrent did not complete: %+v", st)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

package btinterop

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
)

// The stream case plays a file through bt_stream while anacrolix seeds
// it, slowly, so that the range asked for arrives before the rest; the
// web seed cases have libtorrent and anacrolix download a file fit
// publishes from fit's own static route, the publish entry's web seed,
// with no BitTorrent peer to find.

// httpGet fetches u with the headers given, failing the test unless the
// status is want; it returns the body.
func httpGet(t *testing.T, u string, want int, headers ...string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	c := &http.Client{Timeout: hang}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("GET %s: %d %q, want %d", u, resp.StatusCode, body[:min(len(body), 200)], want)
	}
	return body
}

const (
	streamToken = "stream-token-0123456789abcdef"
	streamAuth  = `[{"name": "site", "tokens": [{"name": "viewer", "token": "` + streamToken + `", "play": ["bt/*"], "publish": ["bt/*"]}]}]`
)

func TestStreamFromAnacrolix(t *testing.T) {
	c := clientCase{name: "stream", version: metainfo.Hybrid, enc: encPrefer}
	sdir := t.TempDir()
	files := clientFiles(t, sdir, "content")
	mi := buildTorrent(t, filepath.Join(sdir, "content"), c.version, false)
	// A film of 24 MiB, which fit downloads at its 2 MiB/s: seconds, the
	// range in the middle far less, since bt_stream reads it first.
	film := make([]byte, 24<<20)
	rnd := rand.New(rand.NewPCG(3, 5))
	for i := range film {
		film[i] = byte(rnd.Uint32())
	}
	if err := os.WriteFile(filepath.Join(sdir, "film.mkv"), film, 0o644); err != nil {
		t.Fatal(err)
	}
	// v1: anacrolix looks for the single file of a hybrid torrent below
	// its name, a directory (see TestWebSeed).
	filmMI := buildTorrent(t, filepath.Join(sdir, "film.mkv"), metainfo.V1, false)
	seeder := newAnacrolix(t, sdir, c, anacrolixOpts{seed: true})
	anacrolixSeed(t, seeder, mi)
	anacrolixSeed(t, seeder, filmMI)
	addr := anacrolixAddr(seeder)
	small := filepath.Join("content", "sub1", "f1.bin")
	id := torrentID(mi)

	t.Run("added", func(t *testing.T) {
		f := startFit(t, fitConfig{client: fitClient(t, c, t.TempDir()),
			routes: `[{"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_stream"}}]`})
		fid := torrentID(filmMI)
		f.add(t, map[string]any{"torrent": marshal(t, filmMI), "peers": []string{addr}})
		u := f.web + "/bt/" + fid + "/film.mkv"
		from, to := 12_000_000, 12_500_000
		got := httpGet(t, u, http.StatusPartialContent, "Range", fmt.Sprintf("bytes=%d-%d", from, to))
		if !bytes.Equal(got, film[from:to+1]) {
			t.Fatalf("the range differs: %d bytes", len(got))
		}
		st := f.get(t, fid)
		t.Logf("range in the middle answered at %.0f%% of the film", st.Progress*100)
		if st.Complete {
			t.Fatalf("the film completed before its range was answered: %+v", st)
		}
		if got := httpGet(t, f.web+"/bt/"+fid+"/", http.StatusOK); !bytes.Equal(got, film) {
			t.Fatalf("the film differs: %d bytes", len(got))
		}
	})

	t.Run("magnet-on-the-fly", func(t *testing.T) {
		f := startFit(t, fitConfig{client: fitClient(t, c, t.TempDir()), mediaAuth: streamAuth,
			routes: `[{"match": {"path": ["/bt/*"]}, "handler": {"module": "bt_stream", "params": {"auth": "site", "magnet": true}}}]`})
		q := url.Values{"magnet": {magnetOf(mi, addr)}, "token": {streamToken}}
		u := f.web + "/bt/" + id + "/" + filepath.ToSlash(small) + "?" + q.Encode()
		if got := httpGet(t, u, http.StatusOK); !bytes.Equal(got, files[small]) {
			t.Fatalf("the file differs: %d bytes", len(got))
		}
		var st struct {
			Temporary bool `json:"temporary"`
		}
		f.call(t, http.MethodGet, "/bittorrent/torrents/"+id, nil, http.StatusOK, &st)
		if !st.Temporary {
			t.Fatal("a torrent added on the fly is not temporary")
		}
	})
}

// publishIndex is the part of bt_publish's index the tests read.
type publishIndex struct {
	Publish []struct {
		Name     string `json:"name"`
		Pending  int    `json:"pending"`
		Torrents []struct {
			ID         string `json:"id"`
			Path       string `json:"path"`
			State      string `json:"state"`
			TorrentURL string `json:"torrent_url"`
		} `json:"torrents"`
	} `json:"publish"`
}

// startPublisher runs fit with two publish entries of root/pub, each
// torrent's web seed fit's own static route of root on /files/: "files",
// a torrent of each file, and "dir", one of the whole directory. It
// returns fit, and the .torrent files by entry and path, once they seed.
func startPublisher(t *testing.T, root string, files int) (*fitProc, map[string]map[string][]byte) {
	t.Helper()
	port := freePort(t)
	web := fmt.Sprintf("http://127.0.0.1:%d/files/pub/", port)
	pub := filepath.Join(root, "pub")
	entries := fmt.Sprintf(`"publish": [{"name": "files", "dir": %q, "web_seed": %q, "check_interval": "200ms"},
	{"name": "dir", "dir": %q, "mode": "whole_dir", "web_seed": %q, "check_interval": "200ms"}]`, pub, web, pub, web)
	member := fitClient(t, clientCase{enc: encPrefer}, t.TempDir())
	member = strings.TrimSuffix(strings.TrimSpace(member), "}") + ", " + entries + "}"
	routes := fmt.Sprintf(`[{"match": {"path": ["/files/*"]}, "handler": {"module": "static", "params": {"root": %q, "strip_prefix": "/files"}}},
	{"match": {"path": ["/torrents/*"]}, "handler": {"module": "bt_publish"}}]`, root)
	f := startFit(t, fitConfig{client: member, routes: routes, webPort: port})
	deadline := time.Now().Add(hang)
	for {
		var idx publishIndex
		if err := json.Unmarshal(httpGet(t, f.web+"/torrents/", http.StatusOK), &idx); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string][]byte{}
		n := 0
		for _, e := range idx.Publish {
			out[e.Name] = map[string][]byte{}
			for _, tor := range e.Torrents {
				if tor.State == "seeding" && e.Pending == 0 {
					out[e.Name][tor.Path] = httpGet(t, f.web+"/torrents/"+tor.TorrentURL, http.StatusOK)
					n++
				}
			}
		}
		if n == files+1 {
			return f, out
		}
		if time.Now().After(deadline) {
			t.Fatalf("not published: %+v", idx)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestWebSeed: libtorrent downloads both entries' torrents, anacrolix the
// whole directory's, from fit's static route alone: nothing tells them of
// a BitTorrent peer. anacrolix takes the single-file torrent of a hybrid
// (or v2) torrent for a directory, as its file tree holds the file's name,
// and so refuses a web seed URL not ending in "/"; it is left out of the
// per-file torrents. Nor does anacrolix's web seed fetch a torrent's last
// piece when it is short, so the directory's last file fills its last
// piece here (both anacrolix bugs, upstream; libtorrent has neither, and
// file0's per-file torrent ends with a short piece).
func TestWebSeed(t *testing.T) {
	root := t.TempDir()
	want := map[string][]byte{}
	sizes := []int{2<<20 + 99, 20 * 16 << 10}
	for i, n := range sizes {
		b := bytes.Repeat([]byte{byte(i + 1), 2, 3, byte(i * 7)}, n/4+1)[:n]
		name := fmt.Sprintf("file%d.bin", i)
		p := filepath.Join(root, "pub", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		want[name] = b
	}
	_, torrents := startPublisher(t, root, len(sizes))
	dirWant := map[string][]byte{}
	for name, b := range want {
		dirWant[filepath.Join("pub", name)] = b
	}
	if len(torrents["dir"]) != 1 {
		t.Fatalf("whole_dir published %d torrents", len(torrents["dir"]))
	}

	t.Run("anacrolix-whole-dir", func(t *testing.T) {
		cl := newAnacrolix(t, t.TempDir(), clientCase{enc: encPrefer}, anacrolixOpts{webSeeds: true})
		for _, raw := range torrents["dir"] {
			mi, err := metainfo.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			at, err := cl.AddTorrent(anacrolixTorrent(t, mi))
			if err != nil {
				t.Fatal(err)
			}
			at.DownloadAll()
			select {
			case <-at.Complete().On():
			case <-time.After(hang):
				t.Fatalf("anacrolix incomplete from the web seed: %d bytes", at.BytesCompleted())
			}
			anacrolixCheck(t, at, dirWant, "pub")
			if n := len(at.PeerConns()); n != 0 {
				t.Fatalf("anacrolix had %d peers", n)
			}
		}
	})

	t.Run("libtorrent", func(t *testing.T) {
		needLibtorrent(t)
		for entry, ts := range torrents {
			for path, raw := range ts {
				p := filepath.Join(t.TempDir(), "t.torrent")
				if err := os.WriteFile(p, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				ldir := t.TempDir()
				lt := startLibtorrent(t, "leech", p, ldir)
				lt.wait(t, "the download of "+entry+"/"+path+" from the web seed")
				if entry == "dir" {
					sameFiles(t, ldir, dirWant)
				} else {
					sameFiles(t, ldir, map[string][]byte{path: want[path]})
				}
			}
		}
	})
}

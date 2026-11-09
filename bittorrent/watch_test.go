package bittorrent

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mustWrite writes content as path, making its directory.
func mustWrite(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// exists reports whether path is there.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// TestWatchDir drops .torrent and .magnet files into watch_dir and a
// category's subdirectory: those added, or held already, move into .done,
// those that cannot be into .failed beside why, the rest stay.
func TestWatchDir(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	a, _ := payload(t, src, "wa.bin", 64<<10)
	b, _ := payload(t, src, "wb.bin", 64<<10+4096)
	c, _ := payload(t, src, "wc.bin", 64<<10+8192)
	watch, ldl := t.TempDir(), t.TempDir()
	mustWrite(t, filepath.Join(watch, "a.torrent"), a)
	mustWrite(t, filepath.Join(watch, "b.MAGNET"), []byte("\n  "+magnetOf(t, b)+"  \n# more\n"))
	mustWrite(t, filepath.Join(watch, "films", "c.torrent"), c)
	mustWrite(t, filepath.Join(watch, "bad.torrent"), []byte("not bencoded"))
	mustWrite(t, filepath.Join(watch, "empty.magnet"), []byte("\n\n"))
	mustWrite(t, filepath.Join(watch, "notes.txt"), []byte("left alone"))
	mustWrite(t, filepath.Join(watch, ".hidden.torrent"), a)
	mustWrite(t, filepath.Join(watch, "other", "x.torrent"), a) // not a category
	js := clientJSON(t.TempDir(), ldl, `"watch_dir": `+strconv.Quote(watch)+`, "watch_interval": "20ms", "categories": [{"name": "films"}]`)
	r := startRoleWith(t, e, member(t, js), nil)
	waitFor(t, "the files to be handled", func() bool {
		return exists(filepath.Join(watch, watchDone, "a.torrent")) && exists(filepath.Join(watch, watchDone, "b.MAGNET")) &&
			exists(filepath.Join(watch, "films", watchDone, "c.torrent")) && exists(filepath.Join(watch, watchFailed, "bad.torrent")) &&
			exists(filepath.Join(watch, watchFailed, "empty.magnet"))
	})
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("%d torrents", len(list))
	}
	cats := map[string]string{}
	for _, s := range list {
		cats[s.Name] = s.Category
	}
	if cats["wa.bin"] != "" || cats["wb.bin"] != "" || cats["wc.bin"] != "films" {
		t.Fatalf("categories %v", cats)
	}
	why, err := os.ReadFile(filepath.Join(watch, watchFailed, "bad.torrent.error"))
	if err != nil || !strings.Contains(string(why), "bad request") {
		t.Fatalf("the .error file: %q %v", why, err)
	}
	for _, p := range []string{"notes.txt", ".hidden.torrent", filepath.Join("other", "x.torrent")} {
		if !exists(filepath.Join(watch, p)) {
			t.Errorf("%s was taken", p)
		}
	}
	// One already held moves into .done too, beside the first of its name.
	mustWrite(t, filepath.Join(watch, "a.torrent"), a)
	waitFor(t, "the second a.torrent", func() bool { return exists(filepath.Join(watch, watchDone, "a-1.torrent")) })
	if n := len(r.List()); n != 3 {
		t.Fatalf("%d torrents", n)
	}
}

// TestWatchBounds takes at most watch_max_files files a look, only files
// that kept their size and time since the last look, leaves those that may
// be added later, and fails one too large.
func TestWatchBounds(t *testing.T) {
	e := startEngine(t)
	src, watch := t.TempDir(), t.TempDir()
	r := startRoleWith(t, e, member(t, clientJSON(t.TempDir(), t.TempDir(), `"limits": {"max_torrents": 1}`)), nil)
	job := &watchJob{dir: watch, max: 2}
	for i := range 3 {
		torrent, _ := payload(t, src, fmt.Sprintf("b%d.bin", i), 32<<10+i*4096)
		mustWrite(t, filepath.Join(watch, fmt.Sprintf("b%d.torrent", i)), torrent)
	}
	count := func(dir string) int {
		ents, _ := os.ReadDir(dir)
		n := 0
		for _, e := range ents {
			if !e.IsDir() {
				n++
			}
		}
		return n
	}
	r.runWatch(job) // first sight: nothing taken
	if count(filepath.Join(watch, watchDone)) != 0 || len(r.List()) != 0 {
		t.Fatal("a file taken at first sight")
	}
	r.runWatch(job)
	r.runWatch(job)
	r.runWatch(job)
	if n := count(filepath.Join(watch, watchDone)); n != 1 || len(r.List()) != 1 || count(watch) != 2 {
		t.Fatalf("done %d, torrents %d, left %d", n, len(r.List()), count(watch))
	}
	if len(r.watchLast) == 0 {
		t.Fatal("the files left are not remembered as tried")
	}

	// A file being written is taken once it stopped changing.
	torrent, _ := payload(t, src, "w.bin", 32<<10)
	path := filepath.Join(watch, "w.torrent")
	mustWrite(t, path, torrent[:len(torrent)/2])
	job2 := &watchJob{dir: watch, max: 10}
	r.runWatch(job2)
	mustWrite(t, path, torrent)
	r.runWatch(job2)
	if !exists(path) {
		t.Fatal("a file taken while it changed")
	}
	// Room for it now.
	if err := r.Apply(member(t, clientJSON(r.dataDir, "", `"limits": {"max_torrents": 10}`))); err != nil {
		t.Fatal(err)
	}
	r.runWatch(job2)
	if exists(path) || !exists(filepath.Join(watch, watchDone, "w.torrent")) {
		t.Fatal("the finished file was not taken")
	}

	// A file too large fails.
	big := filepath.Join(watch, "big.magnet")
	mustWrite(t, big, []byte("magnet:?xt=urn:btih:"+strings.Repeat("a", MaxMagnetBytes)))
	r.runWatch(job2)
	r.runWatch(job2)
	if !exists(filepath.Join(watch, watchFailed, "big.magnet")) {
		t.Fatal("the large file did not fail")
	}

	// A watch_dir that is not there is reported, not fatal.
	r.runWatch(&watchJob{dir: filepath.Join(watch, "none"), max: 1})
	if r.watchErr == "" {
		t.Fatal("no error for a missing watch_dir")
	}
}

// TestAddURL adds torrents by URL: directly, through redirects, one to a
// magnet link, and refuses bad URLs, bad answers, a slow server, a cut
// connection, a body too large and too many downloads at once.
func TestAddURL(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	a, _ := payload(t, src, "ua.bin", 64<<10)
	b, _ := payload(t, src, "ub.bin", 64<<10+4096)
	m, _ := payload(t, src, "um.bin", 64<<10+8192)
	release := make(chan struct{})
	var blocked atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/a.torrent", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(a) })
	mux.HandleFunc("/b.torrent", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(b) })
	mux.HandleFunc("/redir", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/r2", http.StatusFound) })
	mux.HandleFunc("/r2", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/b.torrent", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/magnet", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", magnetOf(t, m))
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) })
	mux.HandleFunc("/ftp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "ftp://example.com/x.torrent")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(hang):
		case <-r.Context().Done():
		case <-release:
		}
	})
	mux.HandleFunc("/block", func(w http.ResponseWriter, r *http.Request) {
		blocked.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		_, _ = w.Write(a)
	})
	mux.HandleFunc("/cut", func(w http.ResponseWriter, _ *http.Request) {
		conn, bw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_, _ = bw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 100000\r\n\r\npartial")
		_ = bw.Flush()
		_ = conn.Close()
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(MaxURLTorrentBytes+1))
		chunk := make([]byte, 64<<10)
		for sent := 0; sent <= MaxURLTorrentBytes; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	defer close(release)

	r := startRoleWith(t, e, member(t, clientJSON(t.TempDir(), t.TempDir(), `"url_timeout": "1s", "categories": [{"name": "c"}]`)), nil)
	st, err := r.AddURL(srv.URL+"/a.torrent", AddRequest{Category: "c", Paused: true})
	if err != nil || st.Name != "ua.bin" || st.Category != "c" || !st.Paused {
		t.Fatalf("%+v %v", st, err)
	}
	if st, err := r.AddURL(srv.URL+"/redir", AddRequest{Paused: true}); err != nil || st.Name != "ub.bin" {
		t.Fatalf("through redirects: %+v %v", st, err)
	}
	if st, err := r.AddURL(srv.URL+"/magnet", AddRequest{Paused: true}); err != nil || st.Magnet == "" || st.Name != "um.bin" {
		t.Fatalf("to a magnet link: %+v %v", st, err)
	}
	if _, err := r.AddURL(srv.URL+"/a.torrent", AddRequest{}); !errors.Is(err, ErrExists) {
		t.Fatalf("twice: %v", err)
	}
	for url, want := range map[string]error{
		"ftp://example.com/x.torrent": ErrBadRequest,
		"magnet:?xt=urn:btih:00":      ErrBadRequest,
		"http://":                     ErrBadRequest,
		srv.URL + "/text":             ErrBadRequest,
		srv.URL + "/missing":          ErrFetch,
		srv.URL + "/loop":             ErrFetch,
		srv.URL + "/ftp":              ErrFetch,
		srv.URL + "/slow":             ErrFetch,
		srv.URL + "/cut":              ErrFetch,
		srv.URL + "/big":              ErrFetch,
		"http://" + strings.Repeat("a", maxURLBytes): ErrBadRequest,
	} {
		if _, err := r.AddURL(url, AddRequest{}); !errors.Is(err, want) {
			t.Errorf("%.60s: %v, want %v", url, err, want)
		}
	}
	if _, err := r.AddURL(srv.URL+"/a.torrent", AddRequest{Magnet: "magnet:?x"}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("a URL and a magnet link: %v", err)
	}
	if n := len(r.List()); n != 3 {
		t.Fatalf("%d torrents", n)
	}

	// MaxURLFetches at once; one more is refused.
	var wg sync.WaitGroup
	errs := make([]error, MaxURLFetches)
	for i := range MaxURLFetches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.AddURL(srv.URL+"/block", AddRequest{})
		}()
	}
	waitFor(t, "the downloads to be under way", func() bool { return len(r.fetchSem) == MaxURLFetches })
	if _, err := r.AddURL(srv.URL+"/a.torrent", AddRequest{}); !errors.Is(err, ErrBusy) {
		t.Fatalf("one more: %v", err)
	}
	wg.Wait() // they end by url_timeout, or by the answer, as the server goes
	for _, err := range errs {
		if err != nil && !errors.Is(err, ErrFetch) && !errors.Is(err, ErrExists) {
			t.Errorf("a blocked download: %v", err)
		}
	}
	if len(r.fetchSem) != 0 {
		t.Fatal("a download slot not given back")
	}
}

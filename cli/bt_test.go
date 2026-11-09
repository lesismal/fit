package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const (
	btID1 = "aaaa111122223333444455556666777788889999"
	btID2 = "aaaa2222bbbbccccddddeeeeffff000011112222"
	btID3 = "cccc333344445555666677778888999900001111"
)

// btFake is an Admin API's /bittorrent endpoints, answering canned bodies
// and keeping each request.
type btFake struct {
	srv *httptest.Server
	mu  sync.Mutex
	got []btRequest
	// code answers "METHOD path" with a status other than 200.
	code map[string]int
}

type btRequest struct {
	method, uri, contentType, auth string
	body                           []byte
}

func btStatusJSON(id, name, state string) string {
	return `{"id": "` + id + `", "name": "` + name + `", "state": "` + state + `", "dir": "/dl", "progress": 0.5, "size": 2097152,
		"wanted": 2097152, "completed": 1048576, "pieces": 8, "pieces_have": 4, "download_rate": 2048, "upload_rate": 0.5, "peers": 3, "seeders": 1,
		"category": "films", "queue_position": 1, "held": "queued", "error": "slow", "info_hash": "` + id + `", "info_hash_v2": "` + id + id[:24] + `",
		"sequential": true, "paused": true, "downloaded": 10, "uploaded": 20,
		"effective_limits": {"upload_rate": 1024, "seed_time": 3600000000000},
		"files": [{"index": 0, "path": "` + name + `/a.mkv", "length": 1048576, "completed": 524288, "priority": 4}, {"index": 1, "path": "` + name + `/empty", "length": 0, "completed": 0, "priority": 0}]}`
}

func newBTFake(t *testing.T) *btFake {
	f := &btFake{code: map[string]int{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.got = append(f.got, btRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), body})
		code := f.code[r.Method+" "+r.URL.Path]
		f.mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error": "refused for the test"}`))
			return
		}
		switch {
		case r.URL.Path == "/bittorrent/torrents" && r.Method == "GET":
			_, _ = w.Write([]byte(`{"torrents": [` + btStatusJSON(btID1, "Big Buck Bunny", "downloading") + `, ` +
				btStatusJSON(btID2, "Sintel", "seeding") + `, ` + btStatusJSON(btID3, strings.Repeat("long name ", 8), "paused") + `], "total": 5}`))
		case r.URL.Path == "/bittorrent/torrents" && r.Method == "POST":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(btStatusJSON(btID1, "Big Buck Bunny", "downloading")))
		case strings.HasPrefix(r.URL.Path, "/bittorrent/torrents/") && r.Method == "DELETE":
			_, _ = w.Write([]byte(`{"removed": "x", "data_deleted": true}`))
		case strings.HasPrefix(r.URL.Path, "/bittorrent/torrents/"):
			id := strings.TrimPrefix(r.URL.Path, "/bittorrent/torrents/")
			_, _ = w.Write([]byte(btStatusJSON(id, "Big Buck Bunny", "paused")))
		case r.URL.Path == "/bittorrent/alt_speed":
			_, _ = w.Write([]byte(`{"mode": "on", "configured": false, "active": true, "upload_rate": 1024, "download_rate": 0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// last returns the last request.
func (f *btFake) last() btRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got[len(f.got)-1]
}

func (f *btFake) requests() []btRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]btRequest(nil), f.got...)
}

func runFitBT(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(append([]string{"bt"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestBTCommands(t *testing.T) {
	f := newBTFake(t)
	u, _ := url.Parse(f.srv.URL)
	dir := t.TempDir()
	tok := filepath.Join(dir, "token")
	writeFile(t, tok, "s3cret\n")
	boot := writeCheckBoot(t, `{"address": `+js(u.Host)+`, "token_file": `+js(tok)+`}`)
	torrent := filepath.Join(dir, "x.torrent")
	writeFile(t, torrent, "d4:infod4:name1:xee")

	for _, tc := range []struct {
		args          []string
		code          int
		out, err      []string
		method, uri   string
		contentType   string
		body          string
		bodyHas       []string
		skipRequestOK bool
	}{
		{args: []string{"ls"}, out: []string{"ID", "aaaa11112222", "Big Buck Bunny", "downloading", "50.0%", "2.0 MiB", "2.0 KiB/s", "films", "long name long name", "...", "3 of 5 torrents"},
			method: "GET", uri: "/bittorrent/torrents"},
		{args: []string{"ls", "-state", "seeding,paused", "-category", "films", "-q", "bunny"}, method: "GET",
			uri: "/bittorrent/torrents?category=films&q=bunny&state=seeding%2Cpaused"},
		{args: []string{"ls", "-json"}, out: []string{`"total": 5`}, method: "GET", uri: "/bittorrent/torrents"},
		{args: []string{"add", "-category", "films", "-paused", "-sequential", "magnet:?xt=urn:btih:" + btID1}, out: []string{btID1 + " Big Buck Bunny: added, downloading"},
			method: "POST", uri: "/bittorrent/torrents", contentType: "application/json",
			bodyHas: []string{`"magnet":"magnet:?xt=urn:btih:` + btID1 + `"`, `"category":"films"`, `"paused":true`, `"sequential":true`}},
		{args: []string{"add", "https://example.com/x.torrent", "-down", "1.5M"}, method: "POST", uri: "/bittorrent/torrents", contentType: "application/json",
			bodyHas: []string{`"url":"https://example.com/x.torrent"`, `"download_rate":1572864`}},
		{args: []string{"add", "-category", "films", "-paused", "-sequential", torrent}, method: "POST",
			uri: "/bittorrent/torrents?category=films&paused=1&sequential=1", contentType: "application/x-bittorrent", body: "d4:infod4:name1:xee"},
		{args: []string{"add", "-up", "512K", torrent}, method: "POST", uri: "/bittorrent/torrents", contentType: "application/json",
			bodyHas: []string{`"torrent":"` + base64.StdEncoding.EncodeToString([]byte("d4:infod4:name1:xee")) + `"`, `"upload_rate":524288`}},
		{args: []string{"add", filepath.Join(dir, "missing.torrent")}, code: 1, err: []string{"missing.torrent"}, skipRequestOK: true},
		{args: []string{"info", btID1}, out: []string{"id         " + btID1, "state      paused (held: queued)", "error      slow", "v2 hash", "category   films",
			"progress   50.0%, 1.0 MiB of 2.0 MiB wanted, 4 of 8 pieces", "peers      3, 1 of them seeds", "queue      1, sequential, paused",
			"limits     up 1.0 KiB/s, down unlimited", "seed time 1h0m0s", "Big Buck Bunny/a.mkv", "50.0%", "100.0%"},
			method: "GET", uri: "/bittorrent/torrents/" + btID1},
		{args: []string{"info", "aaaa2222"}, method: "GET", uri: "/bittorrent/torrents/" + btID2},
		{args: []string{"info", "Sintel"}, method: "GET", uri: "/bittorrent/torrents/" + btID2},
		{args: []string{"info", strings.ToUpper(btID3)}, method: "GET", uri: "/bittorrent/torrents/" + btID3},
		{args: []string{"info", "aaaa"}, code: 1, err: []string{`"aaaa" names 2 torrents`}, method: "GET", uri: "/bittorrent/torrents?limit=10000"},
		{args: []string{"info", "nothing"}, code: 1, err: []string{`no torrent is "nothing"`}, method: "GET", uri: "/bittorrent/torrents?limit=10000"},
		{args: []string{"pause", btID1, btID2}, out: []string{btID1 + " Big Buck Bunny: paused", btID2}, method: "POST", uri: "/bittorrent/torrents/" + btID2,
			contentType: "application/json", body: `{"action": "pause"}`},
		{args: []string{"resume", btID1}, method: "POST", uri: "/bittorrent/torrents/" + btID1, body: `{"action": "resume"}`},
		{args: []string{"rm", "-delete-data", btID1}, out: []string{btID1 + " removed"}, method: "DELETE", uri: "/bittorrent/torrents/" + btID1 + "?delete_data=1"},
		{args: []string{"rm", btID1}, method: "DELETE", uri: "/bittorrent/torrents/" + btID1},
		{args: []string{"limit", "-up", "1M", "-ratio", "2", "-seed-time", "12h", "-conns", "-1", "-slots", "3", btID1}, method: "PATCH", uri: "/bittorrent/torrents/" + btID1,
			bodyHas: []string{`"upload_rate":1048576`, `"seed_ratio":2`, `"seed_time":"12h"`, `"max_conns":-1`, `"upload_slots":3`, `"download_rate":0`}},
		{args: []string{"limit", "-clear", btID1}, method: "PATCH", uri: "/bittorrent/torrents/" + btID1, body: `{"limits": null}`},
		{args: []string{"alt"}, out: []string{"mode    on", "the alternative rates hold", "upload 1.0 KiB/s, download unlimited", "never hold"}, method: "GET", uri: "/bittorrent/alt_speed"},
		{args: []string{"alt", "off"}, method: "PUT", uri: "/bittorrent/alt_speed", body: `{"mode": "off"}`},
		{args: []string{"alt", "-json"}, out: []string{`"mode": "on"`}, method: "GET", uri: "/bittorrent/alt_speed"},
	} {
		before := len(f.requests())
		args := append([]string{tc.args[0], "-c", boot}, tc.args[1:]...)
		code, out, errOut := runFitBT(args...)
		if code != tc.code {
			t.Errorf("%v: exit %d, want %d; %s %s", tc.args, code, tc.code, out, errOut)
			continue
		}
		for _, w := range tc.out {
			if !strings.Contains(out, w) {
				t.Errorf("%v: no %q in\n%s", tc.args, w, out)
			}
		}
		for _, w := range tc.err {
			if !strings.Contains(errOut, w) {
				t.Errorf("%v: no %q in stderr %s", tc.args, w, errOut)
			}
		}
		if tc.skipRequestOK {
			if len(f.requests()) != before {
				t.Errorf("%v: sent a request", tc.args)
			}
			continue
		}
		r := f.last()
		if r.method != tc.method || r.uri != tc.uri || r.auth != "Bearer s3cret" {
			t.Errorf("%v: sent %s %s (%s), want %s %s", tc.args, r.method, r.uri, r.auth, tc.method, tc.uri)
		}
		if tc.contentType != "" && r.contentType != tc.contentType {
			t.Errorf("%v: content type %q", tc.args, r.contentType)
		}
		if tc.body != "" && string(r.body) != tc.body {
			t.Errorf("%v: body %s, want %s", tc.args, r.body, tc.body)
		}
		for _, w := range tc.bodyHas {
			if !bytes.Contains(r.body, []byte(w)) {
				t.Errorf("%v: body %s has no %s", tc.args, r.body, w)
			}
		}
	}

	// Refusals exit 1 with the API's error; one torrent refused, the
	// others go on.
	f.code["POST /bittorrent/torrents/"+btID1] = http.StatusConflict
	code, out, errOut := runFitBT("pause", "-c", boot, btID1, btID2)
	if code != 1 || !strings.Contains(errOut, "the Admin API answered 409: refused for the test") || !strings.Contains(out, btID2) {
		t.Fatalf("refused: %d %s %s", code, out, errOut)
	}
	f.code["POST /bittorrent/torrents"] = http.StatusTooManyRequests
	if code, _, errOut := runFitBT("add", "-c", boot, "magnet:?xt=urn:btih:"+btID1); code != 1 || !strings.Contains(errOut, "429") {
		t.Fatalf("add refused: %d %s", code, errOut)
	}
	f.code["GET /bittorrent/torrents"] = http.StatusNotFound
	if code, _, errOut := runFitBT("ls", "-c", boot); code != 1 || !strings.Contains(errOut, "404") {
		t.Fatalf("ls refused: %d %s", code, errOut)
	}
	if code, _, _ := runFitBT("info", "-c", boot, "Sintel"); code != 1 {
		t.Fatalf("resolving with the list refused: %d", code)
	}
}

func TestBTUsage(t *testing.T) {
	boot := writeCheckBoot(t, `{"address": "127.0.0.1:1"}`)
	for _, tc := range []struct {
		args []string
		code int
		err  string
	}{
		{nil, 2, "Usage: fit bt"},
		{[]string{"dance"}, 2, `unknown command "dance"`},
		{[]string{"ls", "-c", boot, "x"}, 2, "takes no arguments"},
		{[]string{"alt", "-c", boot, "on", "off"}, 2, "give auto, on or off"},
		{[]string{"alt", "-c", boot, "fast"}, 2, `not "fast"`},
		{[]string{"info", "-c", boot}, 2, "name one torrent"},
		{[]string{"limit", "-c", boot, "a", "b"}, 2, "name one torrent"},
		{[]string{"pause", "-c", boot}, 2, "name a torrent"},
		{[]string{"limit", "-c", boot, "-clear", "-up", "1", "a"}, 2, "-clear goes alone"},
		{[]string{"limit", "-c", boot, "a"}, 2, "give a limit"},
		{[]string{"limit", "-c", boot, "-seed-time", "soon", "a"}, 2, "not a duration"},
		{[]string{"add", "-c", boot, "-up", "fast", "x"}, 2, "not a rate"},
		{[]string{"ls", "-nope"}, 2, "flag provided but not defined"},
		{[]string{"ls", "-c", filepath.Join(t.TempDir(), "missing.json")}, 2, "fit bt"},
		{[]string{"ls", "-c", boot}, 1, "fit bt ls"},
	} {
		code, _, errOut := runFitBT(tc.args...)
		if code != tc.code || !strings.Contains(errOut, tc.err) {
			t.Errorf("%v: %d %q, want %d %q", tc.args, code, errOut, tc.code, tc.err)
		}
	}
	if code, out, _ := runFitBT("help"); code != 0 || !strings.Contains(out, "fit bt add") {
		t.Fatalf("help: %d %s", code, out)
	}
	var out bytes.Buffer
	if run([]string{"help"}, &out, io.Discard); !strings.Contains(out.String(), "bt         a running fit's BitTorrent client") {
		t.Fatal("fit help does not list bt")
	}
	// A file past the bound is refused before a request.
	big := filepath.Join(t.TempDir(), "big.torrent")
	fh, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := fh.Truncate(32<<20 + 1); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
	if _, err := readTorrentFile(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("a large file: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.torrent")
	writeFile(t, empty, "")
	if _, err := readTorrentFile(empty); err == nil {
		t.Fatal("an empty file read")
	}
}

func TestParseRate(t *testing.T) {
	for in, want := range map[string]int{"0": 0, "1024": 1024, "512K": 512 << 10, "512k": 512 << 10, "1.5M": 3 << 19, "2G": 2 << 30,
		"64KiB": 64 << 10, "3 MiB": 3 << 20, "1GiB": 1 << 30, "-1": -1} {
		got, err := parseRate(in)
		if err != nil || got != want {
			t.Errorf("%q: %d %v, want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "fast", "1T", "9999999G"} {
		if _, err := parseRate(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	var r rateFlag
	if err := r.Set("2K"); err != nil || !r.set || r.String() != "2048" {
		t.Fatal(r, err)
	}
	for v, want := range map[float64]string{0: "-", 0.5: "-", 100: "100 B/s", 1536: "1.5 KiB/s", 5 << 40: "5.0 TiB/s"} {
		if got := speedText(v); got != want {
			t.Errorf("speed %v: %q", v, got)
		}
	}
	if rateText(0) != "unlimited" || rateText(-1) != "unlimited" || rateText(2048) != "2.0 KiB/s" {
		t.Fatal(rateText(0), rateText(2048))
	}
}

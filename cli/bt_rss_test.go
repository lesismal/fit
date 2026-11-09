package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestBTRSS runs fit bt rss's commands against a fake Admin API: the
// requests they send and what they print, refusals and usage errors.
func TestBTRSS(t *testing.T) {
	var mu sync.Mutex
	var got []btRequest
	refuse := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, btRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), body})
		code := refuse[r.Method+" "+r.URL.Path]
		mu.Unlock()
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error": "refused for the test"}`))
			return
		}
		switch {
		case r.URL.Path == "/bittorrent/rss":
			_, _ = w.Write([]byte(`{"configured": true, "feeds": [{"name": "shows", "url": "https://example.com/rss", "enabled": true, "toggled": true,
				"interval": 900000000000, "last_poll": "2026-11-09T10:00:00Z", "next_poll": "2026-11-09T10:15:00Z", "last_result": "error",
				"last_error": "answered 500", "seen": 12, "added": 3, "failed": 1}],
				"rules": [{"name": "s", "enabled": true, "feeds": ["shows"], "category": "tv", "episodes": true, "episodes_seen": 4, "added": 3}]}`))
		case r.URL.Path == "/bittorrent/rss/items":
			_, _ = w.Write([]byte(`{"items": [{"feed": "shows", "key": "1", "title": "Show S01E01", "seen": "2026-11-09T10:00:00Z", "outcome": "added",
				"rule": "s", "torrent": "aaaa111122223333444455556666777788889999"}, {"feed": "shows", "key": "2", "title": "Bad", "seen": "2026-11-09T10:00:00Z",
				"outcome": "failed", "error": "404"}], "total": 9}`))
		case strings.HasSuffix(r.URL.Path, "/poll") && strings.Contains(r.URL.Path, "/broken/"):
			_, _ = w.Write([]byte(`{"feed": "broken", "result": "error", "error": "answered 500"}`))
		case strings.HasSuffix(r.URL.Path, "/poll"):
			_, _ = w.Write([]byte(`{"feed": "shows", "result": "ok", "items": 10, "new": 2, "added": 1, "failed": 0, "deferred": 1}`))
		case strings.HasPrefix(r.URL.Path, "/bittorrent/rss/"):
			_, _ = w.Write([]byte(`{"name": "x", "enabled": false, "toggled": true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	tok := filepath.Join(t.TempDir(), "token")
	writeFile(t, tok, "s3cret\n")
	boot := writeCheckBoot(t, `{"address": `+js(u.Host)+`, "token_file": `+js(tok)+`}`)
	last := func() btRequest {
		mu.Lock()
		defer mu.Unlock()
		return got[len(got)-1]
	}
	for _, tc := range []struct {
		args        []string
		code        int
		out         []string
		method, uri string
		body        string
	}{
		{args: []string{"rss"}, out: []string{"FEED", "shows", "on*", "15m0s", "error", "12", "https://example.com/rss", "feed shows: answered 500",
			"RULE", "[shows]", "tv", "4"}, method: "GET", uri: "/bittorrent/rss"},
		{args: []string{"rss", "ls", "-json"}, out: []string{`"configured": true`}, method: "GET", uri: "/bittorrent/rss"},
		{args: []string{"rss", "items", "-feed", "shows", "-outcome", "added", "-limit", "5"}, out: []string{"SEEN", "Show S01E01", "aaaa11112222",
			"failed (404)", "2 of 9 items"}, method: "GET", uri: "/bittorrent/rss/items?feed=shows&limit=5&outcome=added"},
		{args: []string{"rss", "poll", "shows"}, out: []string{"feed shows: 10 items, 2 new, 1 added, 0 failed, 1 left"}, method: "POST", uri: "/bittorrent/rss/feeds/shows/poll"},
		{args: []string{"rss", "poll", "broken"}, code: 1, out: []string{"feed broken: error: answered 500"}, method: "POST", uri: "/bittorrent/rss/feeds/broken/poll"},
		{args: []string{"rss", "disable", "shows"}, out: []string{"feed shows: off, whatever the config says"}, method: "PUT", uri: "/bittorrent/rss/feeds/shows", body: `{"enabled": false}`},
		{args: []string{"rss", "enable", "-rule", "s"}, method: "PUT", uri: "/bittorrent/rss/rules/s", body: `{"enabled": true}`},
		{args: []string{"rss", "reset", "s"}, method: "PUT", uri: "/bittorrent/rss/feeds/s", body: `{"enabled": null}`},
	} {
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
		r := last()
		if r.method != tc.method || r.uri != tc.uri || r.auth != "Bearer s3cret" || tc.body != "" && string(r.body) != tc.body {
			t.Errorf("%v: sent %s %s %s", tc.args, r.method, r.uri, r.body)
		}
	}
	refuse["POST /bittorrent/rss/feeds/a/poll"] = http.StatusConflict
	if code, out, errOut := runFitBT("rss", "-c", boot, "poll", "a", "shows"); code != 1 || !strings.Contains(errOut, "409") || !strings.Contains(out, "feed shows") {
		t.Fatalf("refused: %d %s %s", code, out, errOut)
	}
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"rss", "-c", boot, "dance"}, `unknown command "dance"`},
		{[]string{"rss", "-c", boot, "ls", "x"}, "takes no arguments"},
		{[]string{"rss", "-c", boot, "poll"}, "name a feed"},
		{[]string{"rss", "-c", boot, "enable"}, "or, with -rule, a rule"},
		{[]string{"rss", "-c", boot, "poll", "-rule", "x"}, "-rule goes with"},
		{[]string{"rss", "-c", boot, "-feed", "x"}, "go with items"},
		{[]string{"rss", "-c", boot, "items", "-limit", "5000"}, "-limit is 1 to 1000"},
	} {
		if code, _, errOut := runFitBT(tc.args...); code != 2 || !strings.Contains(errOut, tc.err) {
			t.Errorf("%v: %d %s", tc.args, code, errOut)
		}
	}
}

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/config"
)

// TestBitTorrentRSS drives the RSS endpoints: the feeds and rules, a poll
// adding a torrent from a magnet item, the items filtered, the toggles and
// their errors.
func TestBitTorrentRSS(t *testing.T) {
	src := t.TempDir()
	torrent := btTorrentFile(t, src, "rss.bin", 100000)
	mi, err := metainfo.Parse(torrent)
	if err != nil {
		t.Fatal(err)
	}
	magnet := mi.Magnet().String()
	feed := `<rss><channel><title>T</title><item><title>Wanted S01E01</title><guid>1</guid><link><![CDATA[` + magnet + `]]></link></item>` +
		`<item><title>Other</title><guid>2</guid></item></channel></rss>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(feed)) }))
	defer srv.Close()
	f := startBT(t, &config.Admin{}, `, "rss": {"feeds": [{"name": "f", "url": "`+srv.URL+`/rss?passkey=secret", "enabled": false}],
		"rules": [{"name": "wanted", "patterns": ["wanted*"], "category": "films", "episodes": true}]}`)

	v := f.json(t, "GET", "/bittorrent/rss", "", http.StatusOK)
	feeds := v["feeds"].([]any)
	if v["configured"] != true || len(feeds) != 1 || len(v["rules"].([]any)) != 1 {
		t.Fatalf("%v", v)
	}
	if u := feeds[0].(map[string]any)["url"].(string); strings.Contains(u, "secret") || !strings.HasSuffix(u, "/rss") {
		t.Fatalf("the feed's URL %q", u)
	}
	v = f.json(t, "POST", "/bittorrent/rss/feeds/f/poll", "", http.StatusOK)
	if v["result"] != "ok" || v["added"] != 1.0 || v["items"] != 2.0 {
		t.Fatalf("%v", v)
	}
	v = f.json(t, "GET", "/bittorrent/rss/items?outcome=added", "", http.StatusOK)
	items := v["items"].([]any)
	if v["total"] != 1.0 || len(items) != 1 || items[0].(map[string]any)["feed"] != "f" || items[0].(map[string]any)["torrent"] == "" {
		t.Fatalf("%v", v)
	}
	v = f.json(t, "GET", "/bittorrent/rss/items?feed=f&limit=1", "", http.StatusOK)
	if v["total"] != 2.0 || len(v["items"].([]any)) != 1 {
		t.Fatalf("%v", v)
	}
	if list := f.role.List(); len(list) != 1 || list[0].Category != "films" {
		t.Fatalf("%+v", list)
	}
	v = f.json(t, "PUT", "/bittorrent/rss/feeds/f", `{"enabled": true}`, http.StatusOK)
	if v["enabled"] != true || v["toggled"] != true {
		t.Fatalf("%v", v)
	}
	v = f.json(t, "PUT", "/bittorrent/rss/rules/wanted", `{"enabled": false}`, http.StatusOK)
	if v["enabled"] != false || v["added"] != 1.0 {
		t.Fatalf("%v", v)
	}
	v = f.json(t, "PUT", "/bittorrent/rss/rules/wanted", `{"enabled": null}`, http.StatusOK)
	if v["enabled"] != true || v["toggled"] != nil {
		t.Fatalf("%v", v)
	}
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/bittorrent/rss/feeds/nope/poll", "", 404},
		{"PUT", "/bittorrent/rss/feeds/nope", `{"enabled": true}`, 404},
		{"PUT", "/bittorrent/rss/rules/nope", `{"enabled": true}`, 404},
		{"PUT", "/bittorrent/rss/feeds/f", `{}`, 400},
		{"PUT", "/bittorrent/rss/feeds/f", `{"enabled": "yes"}`, 400},
		{"PUT", "/bittorrent/rss/feeds/f", `{"enabled": true, "x": 1}`, 400},
		{"GET", "/bittorrent/rss/items?outcome=nope", "", 400},
		{"GET", "/bittorrent/rss/items?limit=0", "", 400},
		{"GET", "/bittorrent/rss/items?feed=nope", "", 404},
	} {
		f.json(t, c.method, c.path, c.body, c.want)
	}
}

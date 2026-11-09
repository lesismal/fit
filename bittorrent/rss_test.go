package bittorrent

import (
	"context"
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

	"github.com/lesismal/fib"
	"github.com/lesismal/fit/app"
)

// feedServer serves feeds and .torrent files for the RSS tests.
type feedServer struct {
	*httptest.Server
	mu       sync.Mutex
	feeds    map[string]string // path -> body
	etags    map[string]string // path -> ETag
	torrents map[string][]byte // path -> .torrent file
	hits     map[string]int
	ifNone   map[string][]string // path -> If-None-Match each request sent
	headers  []http.Header
	handler  map[string]http.HandlerFunc
}

func newFeedServer(t *testing.T) *feedServer {
	fs := &feedServer{feeds: map[string]string{}, etags: map[string]string{}, torrents: map[string][]byte{},
		hits: map[string]int{}, ifNone: map[string][]string{}, handler: map[string]http.HandlerFunc{}}
	fs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.hits[r.URL.Path]++
		fs.ifNone[r.URL.Path] = append(fs.ifNone[r.URL.Path], r.Header.Get("If-None-Match"))
		fs.headers = append(fs.headers, r.Header.Clone())
		h, body, isFeed, etag := fs.handler[r.URL.Path], fs.feeds[r.URL.Path], false, fs.etags[r.URL.Path]
		_, isFeed = fs.feeds[r.URL.Path]
		torrent, isTorrent := fs.torrents[r.URL.Path]
		fs.mu.Unlock()
		switch {
		case h != nil:
			h(w, r)
		case isFeed:
			if etag != "" {
				if r.Header.Get("If-None-Match") == etag {
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("ETag", etag)
			}
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(body))
		case isTorrent:
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write(torrent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fs.Close)
	return fs
}

func (fs *feedServer) set(path, body, etag string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.feeds[path], fs.etags[path] = body, etag
}

func (fs *feedServer) hit(path string) int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.hits[path]
}

// rssItemXML is an item of an RSS 2.0 feed.
func rssItemXML(guid, title, magnet, enclosure string, size int64) string {
	s := "<item><title>" + title + "</title><guid>" + guid + "</guid>"
	if magnet != "" {
		s += "<torrent xmlns=\"http://xmlns.ezrss.it/0.1/\"><magnetURI><![CDATA[" + magnet + "]]></magnetURI>"
		if size > 0 {
			s += "<contentLength>" + strconv.FormatInt(size, 10) + "</contentLength>"
		}
		s += "</torrent>"
	}
	if enclosure != "" {
		s += `<enclosure url="` + enclosure + `" length="` + strconv.FormatInt(size, 10) + `" type="application/x-bittorrent"/>`
	}
	return s + "</item>"
}

func rssDocXML(items ...string) string {
	return `<?xml version="1.0"?><rss version="2.0"><channel><title>Test feed</title>` + strings.Join(items, "") + `</channel></rss>`
}

// rssClientJSON is a client member with rss; the feeds are off so that
// only PollFeed polls them, unless extra says otherwise.
func rssClientJSON(dataDir, rss string) string {
	return clientJSON(dataDir, filepath.Join(dataDir, "dl"), `"categories": [{"name": "tv"}], "rss": `+rss)
}

// startRSSRole starts a role whose clock is clock.
func startRSSRole(t *testing.T, e *fib.Engine, js string, clock *fakeClock) *Role {
	t.Helper()
	return startRoleWith(t, e, member(t, js), func(r *Role) {
		if clock != nil {
			r.now = clock.now
		}
	})
}

func itemsOf(t *testing.T, r *Role, feed string) map[string]RSSItem {
	t.Helper()
	list, _, err := r.RSSItems(feed, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]RSSItem{}
	for _, it := range list {
		out[it.Key] = it
	}
	return out
}

// TestRSSPoll polls an RSS feed: a magnet item and a .torrent enclosure
// added in the rule's category, an item no rule picks, a second quality of
// an episode left out, an item past the size bound; the events, the
// metrics, and a 304 at the next poll.
func TestRSSPoll(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	ta, _ := payload(t, src, "ra.bin", 40<<10)
	tb, _ := payload(t, src, "rb.bin", 40<<10+4096)
	tc, _ := payload(t, src, "rc.bin", 40<<10+8192)
	fs := newFeedServer(t)
	fs.torrents["/b.torrent"] = tb
	fs.set("/feed", rssDocXML(
		rssItemXML("1", "Show S01E01 1080p", magnetOf(t, ta), "", 1000),
		rssItemXML("2", "Show S01E01 720p", magnetOf(t, tc), "", 1000),
		rssItemXML("3", "Film 2026 1080p", "", fs.URL+"/b.torrent", 2000),
		rssItemXML("4", "Unwanted thing", magnetOf(t, tc), "", 1000),
		rssItemXML("5", "Huge Show S01E02 1080p", magnetOf(t, tc), "", 1<<40),
	), `"v1"`)
	dir := t.TempDir()
	js := rssClientJSON(dir, `{"feeds": [{"name": "f", "url": "`+fs.URL+`/feed", "enabled": false, "headers": {"Cookie": "a=b"}, "headers_env": {"X-Feed-Auth": "FIT_TEST_RSS_AUTH"}}],
		"rules": [{"name": "shows", "patterns": ["*S01E*", "*Huge*"], "episodes": true, "category": "tv", "max_size": 1000000},
		          {"name": "films", "patterns": ["re:film \\d{4}"], "paused": true}]}`)
	t.Setenv("FIT_TEST_RSS_AUTH", "secret")
	r := startRSSRole(t, e, js, nil)
	sub, _, _, err := r.Subscribe(0)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	w := &eventWaiter{t: t, sub: sub}

	res, err := r.PollFeed("f")
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != RSSResultOK || res.Items != 5 || res.New != 5 || res.Added != 2 || res.Failed != 0 || res.Deferred != 0 || res.Status != 200 {
		t.Fatalf("%+v", res)
	}
	items := itemsOf(t, r, "f")
	want := map[string]string{"1": RSSAdded, "2": RSSDuplicate, "3": RSSAdded, "4": RSSUnmatched, "5": RSSUnmatched}
	for k, o := range want {
		if items[k].Outcome != o {
			t.Errorf("item %s: %+v, want %s", k, items[k], o)
		}
	}
	if items["1"].Rule != "shows" || items["1"].Source != "magnet" || items["3"].Source != "url" || items["3"].Rule != "films" || items["1"].Torrent == "" {
		t.Errorf("items %+v", items)
	}
	byName := map[string]Status{}
	for _, s := range r.List() {
		byName[s.Name] = s
	}
	if len(byName) != 2 || byName["ra.bin"].Category != "tv" || byName["rb.bin"].Category != "" || !byName["rb.bin"].Paused {
		t.Fatalf("torrents %+v", byName)
	}
	ev := w.wait("rss_added", func(e Event) bool { return e.Type == EventRSSAdded })
	if ev.Feed != "f" || ev.Torrent == "" || ev.Rule == "" || ev.Title == "" {
		t.Fatalf("event %+v", ev)
	}
	fs.mu.Lock()
	h := fs.headers[0]
	fs.mu.Unlock()
	if h.Get("Cookie") != "a=b" || h.Get("X-Feed-Auth") != "secret" || !strings.Contains(h.Get("Accept"), "rss+xml") {
		t.Fatalf("headers %v", h)
	}
	st := r.RSS()
	if !st.Configured || len(st.Feeds) != 1 || len(st.Rules) != 2 {
		t.Fatalf("%+v", st)
	}
	if f := st.Feeds[0]; f.Title != "Test feed" || f.Enabled || f.Seen != 5 || f.Added != 2 || f.Polls != 1 || f.LastResult != RSSResultOK || f.Last == nil || f.URL != fs.URL+"/feed" {
		t.Fatalf("feed %+v", f)
	}
	if rl := st.Rules[0]; rl.Name != "shows" || rl.Added != 1 || rl.EpisodesSeen != 1 || rl.Category != "tv" {
		t.Fatalf("rule %+v", rl)
	}

	// Unchanged: 304, nothing new.
	res, _ = r.PollFeed("f")
	if res.Result != RSSResultNotModified || res.Status != 304 {
		t.Fatalf("%+v", res)
	}
	fs.mu.Lock()
	sent := fs.ifNone["/feed"]
	fs.mu.Unlock()
	if len(sent) != 2 || sent[0] != "" || sent[1] != `"v1"` {
		t.Fatalf("If-None-Match %q", sent)
	}
	// Changed: a new episode, and the old ones not again.
	fs.set("/feed", rssDocXML(
		rssItemXML("6", "Show S01E02 1080p", magnetOf(t, tc), "", 1000),
		rssItemXML("1", "Show S01E01 1080p", magnetOf(t, ta), "", 1000),
	), `"v2"`)
	res, _ = r.PollFeed("f")
	if res.Result != RSSResultOK || res.New != 1 || res.Added != 1 {
		t.Fatalf("%+v", res)
	}
	text := scrape(r)
	if metric(text, "fit_bittorrent_rss_polls_total", `{feed="f",result="ok"}`) != 2 ||
		metric(text, "fit_bittorrent_rss_polls_total", `{feed="f",result="not_modified"}`) != 1 ||
		metric(text, "fit_bittorrent_rss_items_added_total", `{feed="f"}`) != 3 {
		t.Fatalf("metrics:\n%s", text)
	}
	if _, err := r.PollFeed("nope"); err != ErrRSSNoFeed {
		t.Fatal(err)
	}
	list, total, _ := r.RSSItems("", RSSAdded, 2)
	if total != 3 || len(list) != 2 || list[0].Feed != "f" {
		t.Fatalf("%d %+v", total, list)
	}
	if _, _, err := r.RSSItems("nope", "", 0); err != ErrRSSNoFeed {
		t.Fatal(err)
	}
}

// TestRSSMaxPerPoll adds at most max_per_poll torrents a poll, leaving the
// rest for the next, whose request is not conditional; one held already
// counts as done; max_torrents leaves an item for later.
func TestRSSMaxPerPoll(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	fs := newFeedServer(t)
	var items []string
	for i := range 5 {
		tor, _ := payload(t, src, fmt.Sprintf("m%d.bin", i), 32<<10+i*4096)
		items = append(items, rssItemXML(strconv.Itoa(i), fmt.Sprintf("Item %d", i), magnetOf(t, tor), "", 0))
	}
	fs.set("/feed", rssDocXML(items...), `"e"`)
	dir := t.TempDir()
	js := clientJSON(dir, "", `"limits": {"max_torrents": 4}, "rss": {"feeds": [{"name": "f", "url": "`+fs.URL+`/feed", "enabled": false}],
		"rules": [{"name": "all", "max_per_poll": 2}]}`)
	r := startRSSRole(t, e, js, nil)
	res, _ := r.PollFeed("f")
	if res.Added != 2 || res.Deferred != 3 {
		t.Fatalf("%+v", res)
	}
	res, _ = r.PollFeed("f")
	if res.Added != 2 || res.Deferred != 1 || res.New != 3 {
		t.Fatalf("%+v", res)
	}
	// max_torrents (4) reached: the last waits.
	res, _ = r.PollFeed("f")
	if res.Added != 0 || res.Deferred != 1 || !strings.Contains(res.Error, "max_torrents") {
		t.Fatalf("%+v", res)
	}
	fs.mu.Lock()
	sent := fs.ifNone["/feed"]
	fs.mu.Unlock()
	for i, s := range sent {
		if s != "" {
			t.Fatalf("request %d sent If-None-Match %q with items left", i, s)
		}
	}
	if n := len(r.List()); n != 4 {
		t.Fatalf("%d torrents", n)
	}
	if n := len(itemsOf(t, r, "f")); n != 4 {
		t.Fatalf("%d items seen", n)
	}
}

// TestRSSHistory keeps what was seen, the episodes, the toggles and the
// ETag across a restart, and forgets the oldest items past history_size.
func TestRSSHistory(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	ta, _ := payload(t, src, "h.bin", 40<<10)
	fs := newFeedServer(t)
	var items []string
	items = append(items, rssItemXML("ep", "Show S02E05 1080p", magnetOf(t, ta), "", 0))
	for i := range 4 {
		items = append(items, rssItemXML(fmt.Sprint("x", i), fmt.Sprint("Other ", i), "", "", 0))
	}
	fs.set("/feed", rssDocXML(items...), "")
	dir := t.TempDir()
	js := clientJSON(dir, "", `"rss": {"history_size": 5, "feeds": [{"name": "f", "url": "`+fs.URL+`/feed", "enabled": false, "max_items": 5}],
		"rules": [{"name": "shows", "patterns": ["show*"], "episodes": true}]}`)
	r := startRSSRole(t, e, js, nil)
	if res, _ := r.PollFeed("f"); res.Added != 1 || res.New != 5 {
		t.Fatalf("%+v", res)
	}
	off := false
	if err := r.SetRuleEnabled("shows", &off); err != nil {
		t.Fatal(err)
	}
	if err := r.SetFeedEnabled("f", &off); err != nil {
		t.Fatal(err)
	}
	if err := r.SetRuleEnabled("nope", &off); err != ErrRSSNoRule {
		t.Fatal(err)
	}
	if err := r.SetFeedEnabled("nope", nil); err != ErrRSSNoFeed {
		t.Fatal(err)
	}
	r.Stop(context.Background())
	if !exists(filepath.Join(dir, rssStateFile)) {
		t.Fatal("no rss.json")
	}

	// Another quality of the episode, under another ID, after a restart.
	tb, _ := payload(t, src, "h2.bin", 40<<10+4096)
	fs.set("/feed", rssDocXML(append(items, rssItemXML("ep2", "Show S02E05 720p", magnetOf(t, tb), "", 0))...), "")
	r2 := startRSSRole(t, e, js, nil)
	st := r2.RSS()
	if st.Feeds[0].Seen != 5 || !st.Feeds[0].Toggled || st.Feeds[0].Enabled || !st.Rules[0].Toggled || st.Rules[0].Enabled || st.Rules[0].EpisodesSeen != 1 || st.Rules[0].Added != 1 {
		t.Fatalf("%+v", st)
	}
	if err := r2.SetRuleEnabled("shows", nil); err != nil {
		t.Fatal(err)
	}
	res, _ := r2.PollFeed("f")
	// max_items 5: the document's first five, one already seen... all
	// seen, so nothing is new; the sixth is past max_items.
	if res.New != 0 || !res.Truncated {
		t.Fatalf("%+v", res)
	}
	fs.set("/feed", rssDocXML(rssItemXML("ep2", "Show S02E05 720p", magnetOf(t, tb), "", 0)), "")
	res, _ = r2.PollFeed("f")
	if res.New != 1 || res.Added != 0 {
		t.Fatalf("%+v", res)
	}
	got := itemsOf(t, r2, "f")
	if got["ep2"].Outcome != RSSDuplicate || len(got) != 5 {
		t.Fatalf("%d items: %+v", len(got), got)
	}
	if _, ok := got["x0"]; ok {
		t.Fatal("the oldest item was not forgotten past history_size")
	}
	if n := len(r2.List()); n != 1 {
		t.Fatalf("%d torrents", n)
	}

	// A state that does not read is moved aside.
	r2.Stop(context.Background())
	mustWrite(t, filepath.Join(dir, rssStateFile), []byte("{not json"))
	r3 := startRSSRole(t, e, js, nil)
	if st := r3.RSS(); st.Feeds[0].Seen != 0 {
		t.Fatalf("%+v", st.Feeds[0])
	}
	if !exists(filepath.Join(dir, rssStateFile+".bad")) {
		t.Fatal("the bad rss.json was not moved aside")
	}
}

// TestRSSBadFeeds: a feed past max_feed_bytes, malformed, not a feed, a
// server error, a redirect loop, a server that never answers and one that
// cuts the body each fail the poll; a redirect to the feed is followed;
// the role goes on.
func TestRSSBadFeeds(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	fs.set("/big", rssDocXML(strings.Repeat(rssItemXML("x", "y", "", "", 0), 400)), "")
	fs.set("/malformed", "<rss><channel><item><title>a</title><guid>1</guid></item><item>", "")
	fs.set("/html", "<html><body>login</body></html>", "")
	fs.set("/ok", rssDocXML(rssItemXML("1", "one", "", "", 0)), "")
	fs.handler["/500"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }
	fs.handler["/loop"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/loop", http.StatusFound) }
	fs.handler["/moved"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ok", http.StatusMovedPermanently) }
	fs.handler["/ftp"] = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "ftp://x/feed", http.StatusFound) }
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fs.handler["/slow"] = func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	fs.handler["/cut"] = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100000")
		_, _ = w.Write([]byte("<rss><channel><item><title>a</title>"))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}
	names := []string{"big", "malformed", "html", "500", "loop", "moved", "ftp", "slow", "cut"}
	var feeds []string
	for _, n := range names {
		feeds = append(feeds, `{"name": "`+n+`", "url": "`+fs.URL+`/`+n+`", "enabled": false}`)
	}
	js := clientJSON(t.TempDir(), "", `"rss": {"max_feed_bytes": 4096, "timeout": "300ms", "feeds": [`+strings.Join(feeds, ",")+`]}`)
	r := startRSSRole(t, e, js, nil)
	for _, n := range names {
		res, err := r.PollFeed(n)
		if err != nil {
			t.Fatal(err)
		}
		if n == "moved" {
			if res.Result != RSSResultOK || res.Items != 1 {
				t.Errorf("moved: %+v", res)
			}
			continue
		}
		if res.Result != RSSResultError || res.Error == "" {
			t.Errorf("%s: %+v", n, res)
		}
	}
	st := r.RSS()
	for _, f := range st.Feeds {
		if f.Name != "moved" && (f.Errors != 1 || f.LastResult != RSSResultError || f.LastError == "") {
			t.Errorf("%+v", f)
		}
	}
	if v := metric(scrape(r), "fit_bittorrent_rss_polls_total", `{feed="cut",result="error"}`); v != 1 {
		t.Errorf("errors of cut: %v", v)
	}
}

// TestRSSFailedFetch tries an item whose .torrent file cannot be downloaded
// at three polls, then gives it up; one that is not a torrent fails at
// once.
func TestRSSFailedFetch(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	fs.torrents["/junk.torrent"] = []byte("not bencoded")
	fs.set("/feed", rssDocXML(
		rssItemXML("gone", "Gone", "", fs.URL+"/missing.torrent", 0),
		rssItemXML("junk", "Junk", "", fs.URL+"/junk.torrent", 0),
	), "")
	js := clientJSON(t.TempDir(), "", `"rss": {"feeds": [{"name": "f", "url": "`+fs.URL+`/feed", "enabled": false}], "rules": [{"name": "all"}]}`)
	r := startRSSRole(t, e, js, nil)
	for i := 1; i <= 4; i++ {
		res, _ := r.PollFeed("f")
		got := itemsOf(t, r, "f")
		want := min(i, rssMaxAttempts)
		if got["gone"].Outcome != RSSFailed || got["gone"].Attempts != want || !strings.Contains(got["gone"].Error, "404") {
			t.Fatalf("poll %d: %+v", i, got["gone"])
		}
		if got["junk"].Outcome != RSSFailed || got["junk"].Attempts != rssMaxAttempts {
			t.Fatalf("poll %d: %+v", i, got["junk"])
		}
		if i == 1 && res.Failed != 2 || i > 1 && i <= rssMaxAttempts && res.Failed != 1 || i > rssMaxAttempts && res.New != 0 {
			t.Fatalf("poll %d: %+v", i, res)
		}
	}
	if fs.hit("/missing.torrent") != rssMaxAttempts || fs.hit("/junk.torrent") != 1 {
		t.Fatalf("hits %d %d", fs.hit("/missing.torrent"), fs.hit("/junk.torrent"))
	}
}

// TestRSSSchedule polls an enabled feed at once, then not before its
// interval has passed on the role's clock; a feed turned off is not
// polled, and polls again when turned back.
func TestRSSSchedule(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	fs.set("/feed", rssDocXML(rssItemXML("1", "one", "", "", 0)), "")
	clock := &fakeClock{}
	start := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	clock.set(start)
	js := clientJSON(t.TempDir(), "", `"rss": {"feeds": [{"name": "f", "url": "`+fs.URL+`/feed", "interval": "1m"}]}`)
	r := startRSSRole(t, e, js, clock)
	waitFor(t, "the first poll", func() bool { return r.RSS().Feeds[0].Polls == 1 })
	st := r.RSS().Feeds[0]
	if !st.LastPoll.Equal(start) || !st.NextPoll.Equal(start.Add(time.Minute)) || !st.Enabled {
		t.Fatalf("%+v", st)
	}
	clock.set(start.Add(59 * time.Second))
	time.Sleep(100 * time.Millisecond) // ten ticks
	if n := fs.hit("/feed"); n != 1 {
		t.Fatalf("%d polls before the interval", n)
	}
	clock.set(start.Add(time.Minute))
	waitFor(t, "the second poll", func() bool { return fs.hit("/feed") == 2 })
	off := false
	if err := r.SetFeedEnabled("f", &off); err != nil {
		t.Fatal(err)
	}
	clock.set(start.Add(10 * time.Minute))
	time.Sleep(100 * time.Millisecond)
	if n := fs.hit("/feed"); n != 2 {
		t.Fatalf("%d polls while off", n)
	}
	if st := r.RSS().Feeds[0]; st.Enabled || !st.NextPoll.IsZero() {
		t.Fatalf("%+v", st)
	}
	if err := r.SetFeedEnabled("f", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a poll once back on", func() bool { return fs.hit("/feed") == 3 })
}

// TestRSSReload: a reload drops the feeds and rules no longer in the
// config, with what was seen of them, and takes new ones in place; a poll
// being made is refused another.
func TestRSSReload(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	fs.set("/a", rssDocXML(rssItemXML("1", "one", "", "", 0)), "")
	fs.set("/b", rssDocXML(rssItemXML("2", "two", "", "", 0)), "")
	gate := make(chan struct{})
	var entered atomic.Bool
	fs.handler["/slow"] = func(w http.ResponseWriter, r *http.Request) {
		entered.Store(true)
		select {
		case <-gate:
		case <-r.Context().Done():
		}
		_, _ = w.Write([]byte(rssDocXML()))
	}
	dir := t.TempDir()
	feed := func(n string) string {
		return `{"name": "` + n + `", "url": "` + fs.URL + `/` + n + `", "enabled": false}`
	}
	r := startRSSRole(t, e, clientJSON(dir, "", `"rss": {"feeds": [`+feed("a")+`,`+feed("slow")+`], "rules": [{"name": "r1"}]}`), nil)
	if res, _ := r.PollFeed("a"); res.New != 1 {
		t.Fatalf("%+v", res)
	}
	done := make(chan RSSPollResult, 1)
	go func() { res, _ := r.PollFeed("slow"); done <- res }()
	waitFor(t, "the slow poll to start", entered.Load)
	if _, err := r.PollFeed("slow"); err != ErrRSSBusy {
		t.Fatalf("a second poll: %v", err)
	}
	close(gate)
	if res := <-done; res.Result != RSSResultOK {
		t.Fatalf("%+v", res)
	}
	if err := r.Apply(member(t, clientJSON(dir, "", `"rss": {"feeds": [`+feed("b")+`], "rules": [{"name": "r2"}]}`))); err != nil {
		t.Fatal(err)
	}
	st := r.RSS()
	if len(st.Feeds) != 1 || st.Feeds[0].Name != "b" || len(st.Rules) != 1 || st.Rules[0].Name != "r2" {
		t.Fatalf("%+v", st)
	}
	if _, err := r.PollFeed("a"); err != ErrRSSNoFeed {
		t.Fatal(err)
	}
	if res, _ := r.PollFeed("b"); res.New != 1 {
		t.Fatalf("%+v", res)
	}
	r.Stop(context.Background())
	data, _ := os.ReadFile(filepath.Join(dir, rssStateFile))
	if strings.Contains(string(data), `"a"`) || !strings.Contains(string(data), `"b"`) {
		t.Fatalf("rss.json %s", data)
	}
	// Without rss, the API answers none.
	r2 := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	if st := r2.RSS(); st.Configured || len(st.Feeds) != 0 {
		t.Fatalf("%+v", st)
	}
}

// TestRSSStopDuringPoll: stopping the role ends a poll waiting for a server
// that never answers.
func TestRSSStopDuringPoll(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	var entered atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	fs.handler["/hang"] = func(w http.ResponseWriter, r *http.Request) {
		entered.Store(true)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	r := startRSSRole(t, e, clientJSON(t.TempDir(), "", `"rss": {"timeout": "1h", "feeds": [{"name": "h", "url": "`+fs.URL+`/hang", "enabled": false}]}`), nil)
	done := make(chan RSSPollResult, 1)
	go func() { res, _ := r.PollFeed("h"); done <- res }()
	waitFor(t, "the poll to start", entered.Load)
	stopped := make(chan struct{})
	go func() { r.Stop(context.Background()); close(stopped) }()
	select {
	case res := <-done:
		if res.Result != RSSResultError {
			t.Fatalf("%+v", res)
		}
	case <-time.After(hang):
		t.Fatal("the poll did not end at stop")
	}
	<-stopped
	if _, err := r.PollFeed("h"); err != ErrNotRunning {
		t.Fatal(err)
	}
}

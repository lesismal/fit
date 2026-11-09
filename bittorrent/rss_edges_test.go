package bittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// reLogged starts a logger writing to a buffer, for the tests that read
// what the role logs.
func reLogged(t *testing.T) (*logs.Logger, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	root, err := logs.New(nil, buf, buf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root.Logger("bittorrent"), buf
}

// TestParseSizeUnits reads the sizes sites write, and refuses the others.
func TestParseSizeUnits(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"  ", 0},
		{"1234", 1234},
		{"2 KB", 2 << 10},
		{"2k", 2 << 10},
		{"1.5 GiB", 3 << 29},
		{"700 MB", 700 << 20},
		{"3 TiB", 3 << 40},
		{"5 PB", 0},           // no such unit
		{"abc", 0},            // no number
		{"1.2.3 MB", 0},       // not a number
		{"0 MB", 0},           // nothing
		{"9e9 TiB", 0},        // "e9 tib" is no unit
		{"9000000000 TiB", 0}, // past what an int64 holds
	} {
		if got := parseSize(tt.in); got != tt.want {
			t.Errorf("parseSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// TestParseFeedTimeLayouts reads the times feeds write, and none from junk.
func TestParseFeedTimeLayouts(t *testing.T) {
	want := time.Date(2026, 11, 9, 12, 30, 0, 0, time.UTC)
	for _, s := range []string{"2026-11-09T12:30:00Z", "Mon, 09 Nov 2026 12:30:00 +0000", "Mon, 9 Nov 2026 12:30:00 +0000",
		"9 Nov 2026 12:30:00 +0000", "2026-11-09T12:30:00"} {
		if got, ok := parseFeedTime(s); !ok || !got.Equal(want) {
			t.Errorf("%q: %v %v", s, got, ok)
		}
	}
	if got, ok := parseFeedTime("2026-11-09"); !ok || !got.Equal(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("a date: %v %v", got, ok)
	}
	for _, s := range []string{"", "yesterday", "13/40/2026"} {
		if _, ok := parseFeedTime(s); ok {
			t.Errorf("%q read as a time", s)
		}
	}
}

// TestClipRune cuts on a character's boundary, never inside one.
func TestClipRune(t *testing.T) {
	if got := clip("héllo", 2); got != "h" {
		t.Errorf("%q", got)
	}
	if got := clip("héllo", 3); got != "hé" {
		t.Errorf("%q", got)
	}
	if got := clip("abc", 10); got != "abc" {
		t.Errorf("%q", got)
	}
}

type reFailReader struct{}

func (reFailReader) Read([]byte) (int, error) { return 0, errors.New("broken") }

// TestCharsetReader reads UTF-8 and ASCII as they are, ISO-8859-1 decoded,
// and refuses the others.
func TestCharsetReader(t *testing.T) {
	for _, cs := range []string{"", "UTF-8", "us-ascii", " ascii "} {
		r, err := charsetReader(cs, strings.NewReader("abc"))
		if err != nil {
			t.Fatal(cs, err)
		}
		if b, _ := io.ReadAll(r); string(b) != "abc" {
			t.Errorf("%q: %q", cs, b)
		}
	}
	r, err := charsetReader("Latin1", strings.NewReader("caf\xe9"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(r); string(b) != "café" {
		t.Errorf("latin1: %q", b)
	}
	if _, err := charsetReader("iso-8859-1", reFailReader{}); err == nil {
		t.Error("a broken reader read")
	}
	if _, err := charsetReader("shift_jis", strings.NewReader("x")); err == nil || !strings.Contains(err.Error(), "shift_jis") {
		t.Errorf("shift_jis: %v", err)
	}

	// A feed declared ASCII reads; one in an unknown charset does not.
	f, err := parseFeed([]byte(`<?xml version="1.0" encoding="US-ASCII"?><rss><channel><title>T</title><item><guid>1</guid></item></channel></rss>`), 10)
	if err != nil || f.Title != "T" || len(f.Items) != 1 {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := parseFeed([]byte(`<?xml version="1.0" encoding="koi8-r"?><rss></rss>`), 10); !errors.Is(err, ErrFeed) {
		t.Fatalf("koi8-r: %v", err)
	}
}

// TestParseFeedBroken refuses a document cut inside an item left out past
// max, and inside the channel's title.
func TestParseFeedBroken(t *testing.T) {
	for _, doc := range []string{
		`<rss><channel><item><guid>1</guid></item><item><guid>2</guid>`,
		`<rss><channel><title>cut`,
	} {
		if f, err := parseFeed([]byte(doc), 1); !errors.Is(err, ErrFeed) {
			t.Errorf("%q: %+v %v", doc, f, err)
		}
	}
}

// TestParseItemSources: a magnet link as an enclosure, URLs past their
// bound left out, an item known only by its title, and one with nothing to
// tell it by left out.
func TestParseItemSources(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	long := "http://example.com/" + strings.Repeat("a", maxURLBytes)
	doc := `<rss><channel>` +
		`<item><title>enc</title><guid>1</guid><enclosure url="` + magnet + `"/></item>` +
		`<item><title>long</title><guid>2</guid><enclosure url="` + long + `" length="5"/><link>` + long + `</link></item>` +
		`<item><title>only a title</title></item>` +
		`<item><description>nothing</description></item>` +
		`<item><title>sized</title><guid>3</guid><size>2 MiB</size><enclosure url="http://example.com/a.torrent" length="7"/></item>` +
		`<item><title>enc length</title><guid>4</guid><enclosure url="http://example.com/b.torrent" length="7"/></item>` +
		`</channel></rss>`
	f, err := parseFeed([]byte(doc), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Items) != 5 {
		t.Fatalf("%d items: %+v", len(f.Items), f.Items)
	}
	if it := f.Items[0]; it.Magnet != magnet || it.TorrentURL != "" {
		t.Errorf("enclosure magnet: %+v", it)
	}
	if it := f.Items[1]; it.TorrentURL != "" || it.Link != "" || it.Size != 5 {
		t.Errorf("long URLs: %+v", it)
	}
	if _, _, ok := f.Items[1].source(); ok {
		t.Error("an item whose URLs are too long has a source")
	}
	if it := f.Items[2]; it.Key != "title:only a title" {
		t.Errorf("title key: %+v", it)
	}
	if it := f.Items[3]; it.Size != 2<<20 {
		t.Errorf("site size over the enclosure's: %+v", it)
	}
	if it := f.Items[4]; it.Size != 7 || it.TorrentURL != "http://example.com/b.torrent" {
		t.Errorf("enclosure length: %+v", it)
	}
}

// TestCompileRulesBadPattern refuses a rule whose pattern does not
// compile, naming the rule.
func TestCompileRulesBadPattern(t *testing.T) {
	_, err := compileRules([]config.BitTorrentRSSRule{{Name: "bad", Exclude: []string{"re:("}}})
	if err == nil || !strings.Contains(err.Error(), "rss rule bad") {
		t.Fatalf("%v", err)
	}
}

// TestRSSStateLaterVersion moves aside a state of a later version, and
// starts with none.
func TestRSSStateLaterVersion(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, rssStateFile), []byte(`{"version": `+strconv.Itoa(RSSStateVersion+1)+`}`))
	doc, err := loadRSSState(dir)
	if err == nil || !strings.Contains(err.Error(), "later than") || doc == nil || len(doc.Feeds) != 0 {
		t.Fatalf("%+v %v", doc, err)
	}
	if !exists(filepath.Join(dir, rssStateFile+".bad")) || exists(filepath.Join(dir, rssStateFile)) {
		t.Fatal("not moved aside")
	}
}

// TestRSSStateNotMovable reports a state that neither reads nor can be
// moved aside, and starts with none.
func TestRSSStateNotMovable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write")
	}
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, rssStateFile), []byte(`{`))
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	doc, err := loadRSSState(dir)
	if err == nil || !strings.Contains(err.Error(), "nor can it be moved aside") || doc == nil {
		t.Fatalf("%+v %v", doc, err)
	}
}

// TestPlainURLBad is empty for a URL that does not parse.
func TestPlainURLBad(t *testing.T) {
	if got := plainURL("http://[::1"); got != "" {
		t.Fatalf("%q", got)
	}
	if got := plainURL("https://user:pw@example.com/rss?passkey=x#f"); got != "https://example.com/rss" {
		t.Fatalf("%q", got)
	}
}

// TestRSSWithoutConfig: a role without rss lists no items, polls no feed,
// toggles no rule.
func TestRSSWithoutConfig(t *testing.T) {
	e := startEngine(t)
	r := startRole(t, e, clientJSON(t.TempDir(), "", ""), nil)
	if items, n, err := r.RSSItems("", "", 0); err != nil || n != 0 || items == nil || len(items) != 0 {
		t.Fatalf("%v %d %v", items, n, err)
	}
	if _, err := r.PollFeed("x"); !errors.Is(err, ErrRSSNoFeed) {
		t.Fatal(err)
	}
	if err := r.SetFeedEnabled("x", nil); !errors.Is(err, ErrRSSNoFeed) {
		t.Fatal(err)
	}
	if err := r.SetRuleEnabled("x", nil); !errors.Is(err, ErrRSSNoRule) {
		t.Fatal(err)
	}
	if st := r.RSS(); st.Configured || len(st.Feeds) != 0 {
		t.Fatalf("%+v", st)
	}
	// A poll of a feed the config lacks, as the loop would start it.
	if res := r.rss.poll(context.Background(), "x"); res.Result != RSSResultError || res.Error != ErrRSSNoFeed.Error() {
		t.Fatalf("%+v", res)
	}
	r.rss.startDue() // nothing configured: nothing starts
}

// TestRSSPollEdges: an item twice in a document counted once; two items of
// one magnet link, the second held already; the items filtered by feed
// and outcome, those of two feeds seen at once in order of their feeds;
// Last-Modified sent back; the log of the polls.
func TestRSSPollEdges(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	const magnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=x"
	fs.set("/a", rssDocXML(
		rssItemXML("1", "one", magnet, "", 0),
		rssItemXML("1", "one again", magnet, "", 0),
		rssItemXML("2", "two", magnet, "", 0),
	), "")
	var since []string
	fs.handler["/b"] = func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		since = append(since, r.Header.Get("If-Modified-Since"))
		fs.mu.Unlock()
		w.Header().Set("Last-Modified", "Mon, 09 Nov 2026 12:00:00 GMT")
		_, _ = w.Write([]byte(rssDocXML(rssItemXML("b1", "bee", "", "", 0))))
	}
	fs.handler["/err"] = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }
	clock := &fakeClock{}
	clock.set(time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC))
	js := clientJSON(t.TempDir(), "", `"rss": {"feeds": [`+
		`{"name": "a", "url": "`+fs.URL+`/a", "enabled": false},`+
		`{"name": "b", "url": "`+fs.URL+`/b", "enabled": false},`+
		`{"name": "err", "url": "`+fs.URL+`/err", "enabled": false}],`+
		`"rules": [{"name": "all", "feeds": ["a"], "paused": true}]}`)
	l, logged := reLogged(t)
	r := startRoleWith(t, e, member(t, js), func(r *Role) { r.now, r.log = clock.now, l })

	res, err := r.PollFeed("a")
	if err != nil {
		t.Fatal(err)
	}
	if res.Items != 3 || res.New != 2 || res.Added != 1 {
		t.Fatalf("%+v", res)
	}
	got := itemsOf(t, r, "a")
	if got["1"].Outcome != RSSAdded || got["2"].Outcome != RSSExists || len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	for range 2 {
		if res, err := r.PollFeed("b"); err != nil || res.Result != RSSResultOK || res.New > 1 {
			t.Fatalf("%+v %v", res, err)
		}
	}
	fs.mu.Lock()
	if len(since) != 2 || since[0] != "" || since[1] != "Mon, 09 Nov 2026 12:00:00 GMT" {
		t.Errorf("If-Modified-Since %q", since)
	}
	fs.mu.Unlock()
	if res, _ := r.PollFeed("err"); res.Result != RSSResultError {
		t.Fatalf("%+v", res)
	}

	// All seen at one time: a's before b's, the newest first otherwise.
	all, n, err := r.RSSItems("", "", 0)
	if err != nil || n != 3 || len(all) != 3 || all[0].Feed != "a" || all[2].Feed != "b" {
		t.Fatalf("%+v %d %v", all, n, err)
	}
	if b, n, _ := r.RSSItems("b", "", 0); n != 1 || b[0].Key != "b1" {
		t.Fatalf("%+v", b)
	}
	if none, n, err := r.RSSItems("a", RSSFailed, 0); err != nil || n != 0 || none == nil {
		t.Fatalf("%v %d %v", none, n, err)
	}
	if two, n, _ := r.RSSItems("", "", 2); n != 3 || len(two) != 2 {
		t.Fatalf("a page of 2: %d of %d", len(two), n)
	}
	if _, _, err := r.RSSItems("nope", "", 0); !errors.Is(err, ErrRSSNoFeed) {
		t.Fatal(err)
	}
	for _, want := range []string{"rss feed a: 3 items, 2 new, 1 added", "rss feed err:"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log has no %q:\n%s", want, logged.String())
		}
	}
}

// TestRSSReloadTrimsAndRemakes: a reload with a smaller history forgets the
// oldest items at once, a new timeout makes a new HTTP client, and a bad
// rule is refused; after the role stops, a config changes nothing and no
// poll starts.
func TestRSSReloadTrimsAndRemakes(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	var items []string
	for i := range 5 {
		items = append(items, rssItemXML(strconv.Itoa(i), "t"+strconv.Itoa(i), "", "", 0))
	}
	fs.set("/a", rssDocXML(items...), "")
	dataDir := t.TempDir()
	cfg := func(history int, timeout string) string {
		return clientJSON(dataDir, "", `"rss": {"history_size": `+strconv.Itoa(history)+`, "timeout": "`+timeout+`", "feeds": [`+
			`{"name": "a", "url": "`+fs.URL+`/a", "enabled": false, "max_items": `+strconv.Itoa(min(history, 5))+`}]}`)
	}
	r := startRSSRole(t, e, cfg(100, "10s"), nil)
	if _, err := r.PollFeed("a"); err != nil {
		t.Fatal(err)
	}
	if len(itemsOf(t, r, "a")) != 5 {
		t.Fatal("not all seen")
	}
	r.rss.mu.Lock()
	before := r.rss.httpc
	r.rss.mu.Unlock()
	if before == nil {
		t.Fatal("no client after a poll")
	}
	if err := r.Apply(member(t, cfg(2, "20s"))); err != nil {
		t.Fatal(err)
	}
	got := itemsOf(t, r, "a")
	if len(got) != 2 || got["3"].Key == "" || got["4"].Key == "" {
		t.Fatalf("after the trim %+v", got)
	}
	r.rss.mu.Lock()
	after := r.rss.httpc
	r.rss.mu.Unlock()
	if after != nil {
		t.Fatal("the old client is kept for a new timeout")
	}
	if _, err := r.PollFeed("a"); err != nil {
		t.Fatal(err)
	}

	bad := &config.BitTorrentClient{RSS: &config.BitTorrentRSS{Rules: []config.BitTorrentRSSRule{{Name: "x", Patterns: []string{"re:["}}}}}
	if err := r.rss.apply(bad, dataDir); err == nil {
		t.Fatal("a bad rule applied")
	}

	r.Stop(context.Background())
	if err := r.rss.apply(member(t, cfg(1, "10s")), dataDir); err != nil {
		t.Fatal(err)
	}
	r.rss.startDue()
	if _, err := r.PollFeed("a"); !errors.Is(err, ErrNotRunning) {
		t.Fatal(err)
	}
	f := r.rss.feeds["a"]
	f.polling = true
	if res := r.rss.poll(context.Background(), "a"); res.Result != RSSResultError || f.polling {
		t.Fatalf("a poll after the stop: %+v, polling %v", res, f.polling)
	}
}

// TestRSSStateBounds reads a state that does not read, logged and moved
// aside, and a rule's episodes past MaxRSSEpisodes kept to the newest; an
// episode added past them forgets the oldest.
func TestRSSStateBounds(t *testing.T) {
	e := startEngine(t)
	dataDir := t.TempDir()
	mustWrite(t, filepath.Join(dataDir, rssStateFile), []byte("{"))
	fs := newFeedServer(t)
	const magnet = "magnet:?xt=urn:btih:1123456789abcdef0123456789abcdef01234567"
	fs.set("/a", rssDocXML(rssItemXML("1", "Show S01E99 1080p", magnet, "", 0)), "")
	js := clientJSON(dataDir, "", `"rss": {"feeds": [{"name": "a", "url": "`+fs.URL+`/a", "enabled": false}],`+
		`"rules": [{"name": "tv", "episodes": true, "paused": true}]}`)
	l, logged := reLogged(t)
	r := startRoleWith(t, e, member(t, js), func(r *Role) { r.log = l })
	if !strings.Contains(logged.String(), "rss: ") || !exists(filepath.Join(dataDir, rssStateFile+".bad")) {
		t.Fatalf("not logged nor moved aside:\n%s", logged.String())
	}
	r.Stop(context.Background())

	// A state with one episode more than kept.
	eps := make([]string, MaxRSSEpisodes+1)
	for i := range eps {
		eps[i] = "other\x00" + strconv.Itoa(i)
	}
	data, err := json.Marshal(rssDoc{Version: RSSStateVersion, Rules: map[string]*rssRuleDoc{"tv": {Episodes: eps}}})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dataDir, rssStateFile), data)
	r = startRSSRole(t, e, js, nil)
	if st := r.RSS().Rules[0]; st.EpisodesSeen != MaxRSSEpisodes {
		t.Fatalf("%+v", st)
	}
	res, err := r.PollFeed("a")
	if err != nil || res.Added != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	r.rss.mu.Lock()
	st := r.rss.ruleSts["tv"]
	n, first, hasOld := len(st.doc.Episodes), st.doc.Episodes[0], st.eps["other\x001"]
	r.rss.mu.Unlock()
	if n != MaxRSSEpisodes || first != "other\x002" || hasOld {
		t.Fatalf("%d episodes, the first %q, the oldest kept %v", n, first, hasOld)
	}
}

// TestRSSSaveFails reports a state that cannot be saved, and saves it once
// it can again, logging both.
func TestRSSSaveFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a directory the test cannot write")
	}
	e := startEngine(t)
	fs := newFeedServer(t)
	dataDir := t.TempDir()
	js := clientJSON(dataDir, "", `"rss": {"feeds": [{"name": "a", "url": "`+fs.URL+`/a", "enabled": false}]}`)
	l, logged := reLogged(t)
	r := startRoleWith(t, e, member(t, js), func(r *Role) { r.log = l })
	if err := os.Chmod(dataDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o700) })
	off := false
	if err := r.SetFeedEnabled("a", &off); err != nil {
		t.Fatal(err)
	}
	if st := r.RSS(); st.Error == "" || st.Feeds[0].Enabled || !st.Feeds[0].Toggled {
		t.Fatalf("%+v", st)
	}
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := r.SetRuleEnabled("nope", nil); !errors.Is(err, ErrRSSNoRule) {
		t.Fatal(err)
	}
	if err := r.SetFeedEnabled("a", nil); err != nil {
		t.Fatal(err)
	}
	if st := r.RSS(); st.Error != "" {
		t.Fatalf("%+v", st)
	}
	for _, want := range []string{"rss: saving", "is saved again"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log has no %q:\n%s", want, logged.String())
		}
	}
}

// TestRSSFetchBadURL refuses a URL that does not parse, before any request.
func TestRSSFetchBadURL(t *testing.T) {
	s := &rssRunner{}
	if _, err := s.fetchFeed(context.Background(), nil, "http://[::1", time.Second, http.Header{}, "", ""); !errors.Is(err, ErrFetch) {
		t.Fatal(err)
	}
}

// TestRSSPollsBounded starts no poll while MaxRSSPolls run.
func TestRSSPollsBounded(t *testing.T) {
	e := startEngine(t)
	fs := newFeedServer(t)
	fs.set("/a", rssDocXML(rssItemXML("1", "one", "", "", 0)), "")
	js := clientJSON(t.TempDir(), "", `"rss": {"feeds": [{"name": "a", "url": "`+fs.URL+`/a", "enabled": false}]}`)
	r := startRSSRole(t, e, js, nil)
	for range MaxRSSPolls {
		r.rss.sem <- struct{}{}
	}
	if err := r.SetFeedEnabled("a", new(true)); err != nil {
		t.Fatal(err)
	}
	r.rss.startDue()
	time.Sleep(50 * time.Millisecond)
	if fs.hit("/a") != 0 {
		t.Fatal("a poll past MaxRSSPolls")
	}
	for range MaxRSSPolls {
		<-r.rss.sem
	}
	waitFor(t, "the poll once a slot frees", func() bool { return fs.hit("/a") >= 1 })
}

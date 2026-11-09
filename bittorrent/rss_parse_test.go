package bittorrent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestParseRSS reads an RSS 2.0 feed: magnet links in the torrent
// namespace, nested and flat, in an enclosure and as the link; a .torrent
// enclosure with its length; a site's own size; an info hash alone; an
// item with nothing to add; IDs from guid, else the link, else the title.
func TestParseRSS(t *testing.T) {
	hash := strings.Repeat("ab", 20)
	doc := `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torrent="http://xmlns.ezrss.it/0.1/" xmlns:nyaa="https://nyaa.si/xmlns/nyaa">
<channel><title>Shows &amp; more</title><link>https://example.com/</link>
<item><title>Show S01E02 1080p</title><guid isPermaLink="false">g-1</guid>
  <torrent><magnetURI><![CDATA[magnet:?xt=urn:btih:` + hash + `&dn=a]]></magnetURI><contentLength>1048576</contentLength></torrent>
  <pubDate>Mon, 09 Nov 2026 10:00:00 +0000</pubDate></item>
<item><title>Film&nbsp;2026</title><link>https://example.com/film</link>
  <enclosure url="https://example.com/film.torrent" length="2048" type="application/x-bittorrent"/></item>
<item><title>Flat</title><guid>g-3</guid><torrent:magnetURI>magnet:?xt=urn:btih:` + hash + `</torrent:magnetURI><nyaa:size>1.5 GiB</nyaa:size></item>
<item><title>Linked</title><link>magnet:?xt=urn:btih:` + hash + `</link></item>
<item><title>Hash only</title><guid>g-5</guid><nyaa:infoHash>` + strings.ToUpper(hash) + `</nyaa:infoHash></item>
<item><title>Page only</title><guid>g-6</guid><link>https://example.com/page</link></item>
<item><description>nothing</description></item>
</channel></rss>`
	f, err := parseFeed([]byte(doc), 100)
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Shows & more" || f.Truncated {
		t.Fatalf("title %q truncated %v", f.Title, f.Truncated)
	}
	if len(f.Items) != 6 {
		t.Fatalf("%d items: %+v", len(f.Items), f.Items)
	}
	it := f.Items
	if it[0].Key != "g-1" || it[0].Magnet == "" || it[0].Size != 1<<20 || it[0].Published.IsZero() {
		t.Errorf("item 1: %+v", it[0])
	}
	if it[1].Key != "https://example.com/film" || it[1].Title != "Film 2026" || it[1].TorrentURL != "https://example.com/film.torrent" || it[1].Size != 2048 || it[1].Magnet != "" {
		t.Errorf("item 2: %+v", it[1])
	}
	if m, l, ok := it[1].source(); !ok || m != "" || l != "https://example.com/film.torrent" {
		t.Errorf("item 2's source: %q %q", m, l)
	}
	if it[2].Magnet == "" || it[2].Size != 3<<29 {
		t.Errorf("item 3: %+v", it[2])
	}
	if it[3].Magnet == "" || it[3].Link != "" || it[3].Key != it[3].Magnet {
		t.Errorf("item 4: %+v", it[3])
	}
	if it[4].Magnet != "magnet:?xt=urn:btih:"+hash+"&dn=Hash+only" {
		t.Errorf("item 5: %+v", it[4])
	}
	if m, l, ok := it[5].source(); !ok || m != "" || l != "https://example.com/page" {
		t.Errorf("item 6: %+v", it[5])
	}
}

// TestParseAtom reads an Atom feed: the enclosure link with its length, the
// alternate link, a magnet link as the link, the id.
func TestParseAtom(t *testing.T) {
	doc := `<feed xmlns="http://www.w3.org/2005/Atom"><title>Atom feed</title>
<entry><title>A S02E03</title><id>urn:a</id><updated>2026-11-09T10:00:00Z</updated>
  <link rel="alternate" href="https://example.com/a"/>
  <link rel="enclosure" href="https://example.com/a.torrent" length="777" type="application/x-bittorrent"/></entry>
<entry><title>B</title><id>urn:b</id><link href="magnet:?xt=urn:btih:` + strings.Repeat("cd", 20) + `"/></entry>
<entry><title>C</title><link href="https://example.com/c"/></entry>
</feed>`
	f, err := parseFeed([]byte(doc), 100)
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Atom feed" || len(f.Items) != 3 {
		t.Fatalf("%q %+v", f.Title, f.Items)
	}
	a, b, c := f.Items[0], f.Items[1], f.Items[2]
	if a.Key != "urn:a" || a.TorrentURL != "https://example.com/a.torrent" || a.Link != "https://example.com/a" || a.Size != 777 || a.Published.IsZero() {
		t.Errorf("a: %+v", a)
	}
	if b.Magnet == "" || b.Key != "urn:b" {
		t.Errorf("b: %+v", b)
	}
	if c.Key != "https://example.com/c" || c.Link != "https://example.com/c" {
		t.Errorf("c: %+v", c)
	}
}

// TestParseBounds: items past max are left out, a long title is cut, a
// long URL left out, a long ID hashed; RSS 1.0; ISO-8859-1.
func TestParseBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString(`<rss><channel>`)
	for i := range 10 {
		fmt.Fprintf(&b, `<item><title>t%d</title><guid>%d</guid></item>`, i, i)
	}
	long := strings.Repeat("é", maxRSSTitle)
	fmt.Fprintf(&b, `<item><title>%s</title><guid>%s</guid><link>https://x/%s</link></item>`, long, strings.Repeat("k", maxRSSKey+1), strings.Repeat("u", maxURLBytes))
	b.WriteString(`</channel></rss>`)
	f, err := parseFeed([]byte(b.String()), 3)
	if err != nil || len(f.Items) != 3 || !f.Truncated || f.Items[2].Title != "t2" {
		t.Fatalf("%v %+v", err, f)
	}
	f, err = parseFeed([]byte(b.String()), 100)
	if err != nil || len(f.Items) != 11 {
		t.Fatalf("%v %d", err, len(f.Items))
	}
	last := f.Items[10]
	if len(last.Title) > maxRSSTitle || !strings.HasPrefix(long, last.Title) || last.Link != "" || !strings.HasPrefix(last.Key, "sha256:") {
		t.Fatalf("the long item: %d %q %q", len(last.Title), last.Link, last.Key)
	}

	rdf := `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/">
<channel><title>R</title></channel><item><title>r1</title><link>https://example.com/r1.torrent</link></item></rdf:RDF>`
	if f, err := parseFeed([]byte(rdf), 10); err != nil || len(f.Items) != 1 || f.Items[0].Link == "" {
		t.Fatalf("RDF: %v %+v", err, f)
	}
	latin := append([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?><rss><channel><item><title>caf`), 0xe9)
	latin = append(latin, []byte(`</title><guid>1</guid></item></channel></rss>`)...)
	if f, err := parseFeed(latin, 10); err != nil || f.Items[0].Title != "café" {
		t.Fatalf("ISO-8859-1: %v %+v", err, f)
	}
}

// TestParseErrors: not XML, not a feed, cut short, an unknown charset.
func TestParseErrors(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":   ``,
		"text":    `just text`,
		"html":    `<html><body>no</body></html>`,
		"cut":     `<rss><channel><item><title>a</title><guid>1</guid></item><item><title>b`,
		"charset": `<?xml version="1.0" encoding="KOI8-R"?><rss><channel></channel></rss>`,
		"bad":     `<rss><channel><item><title>a</ti tle></item></channel></rss>`,
	} {
		if _, err := parseFeed([]byte(doc), 10); !errors.Is(err, ErrFeed) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestEpisodeKey(t *testing.T) {
	for title, want := range map[string]string{
		"Show Name S01E02 1080p WEB":           "show name|s01e02",
		"Show.Name.S01E02.720p.HDTV":           "show name|s01e02",
		"[Group] Show Name - S1E2 [1080p]":     "show name|s01e02",
		"Show Name S01E02E03 1080p":            "show name|s01e02-e03",
		"Show Name S01E02-E03":                 "show name|s01e02-e03",
		"Show Name S01E02-1080p":               "show name|s01e02",
		"Show Name 1x02 HDTV":                  "show name|s01e02",
		"Daily Show 2026.11.09 Guest 720p":     "daily show|2026-11-09",
		"Daily Show 2026-11-09":                "daily show|2026-11-09",
		"(Grp)【Raws】Another Show S10E100 x265": "another show|s10e100",
		"Film 2026 1080p":                      "",
		"Show Name Season 1 Complete":          "",
	} {
		got, ok := episodeKey(title)
		if want == "" {
			if ok {
				t.Errorf("%q: %q, want none", title, got)
			}
			continue
		}
		if got != want {
			t.Errorf("%q: %q, want %q", title, got, want)
		}
	}
}

func TestRuleMatches(t *testing.T) {
	rules, err := compileRules([]config.BitTorrentRSSRule{
		{Name: "glob", Patterns: []string{"*show name*1080p*"}, Exclude: []string{"*hevc*"}, MinSize: 100, MaxSize: 1000},
		{Name: "re", Feeds: []string{"b"}, Patterns: []string{`re:S0[1-3]E\d+`}},
		{Name: "all"},
	})
	if err != nil {
		t.Fatal(err)
	}
	glob, re, all := rules[0], rules[1], rules[2]
	for _, c := range []struct {
		r    *rssRule
		feed string
		it   feedItem
		want bool
	}{
		{glob, "a", feedItem{Title: "Show Name S01E01 1080p"}, true},
		{glob, "a", feedItem{Title: "SHOW NAME S01E01 1080P", Size: 500}, true},
		{glob, "a", feedItem{Title: "Show Name S01E01 720p"}, false},
		{glob, "a", feedItem{Title: "Show Name S01E01 1080p HEVC"}, false},
		{glob, "a", feedItem{Title: "Show Name S01E01 1080p", Size: 99}, false},
		{glob, "a", feedItem{Title: "Show Name S01E01 1080p", Size: 1001}, false},
		{glob, "a", feedItem{Title: "Other Show Name 1080p"}, true},
		{re, "b", feedItem{Title: "x s02e10 y"}, true},
		{re, "a", feedItem{Title: "x s02e10 y"}, false},
		{re, "b", feedItem{Title: "x s04e10 y"}, false},
		{all, "z", feedItem{Title: "anything"}, true},
	} {
		if got := c.r.matches(c.feed, &c.it); got != c.want {
			t.Errorf("%s %s %+v: %v", c.r.name, c.feed, c.it, got)
		}
	}
	if p, err := config.TitlePattern("a?c[1]"); err != nil || !p.MatchString("ABC[1]") || p.MatchString("abc1") {
		t.Errorf("a glob's other characters are literal: %v", err)
	}
	if _, err := config.TitlePattern("re:("); err == nil {
		t.Error("a bad regular expression compiled")
	}
}

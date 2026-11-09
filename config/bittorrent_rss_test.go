package config

import (
	"strings"
	"testing"
	"time"
)

func TestBitTorrentRSS(t *testing.T) {
	head := `{"bittorrent": {"client": {"listen": ":6881", "data_dir": "/var/lib/bt", "categories": [{"name": "tv"}], "rss": `
	c, err := Load([]byte(head + `{"history_size": 500, "max_feed_bytes": 1048576, "timeout": "10s",
		"feeds": [{"name": "a", "url": "https://example.com/rss?passkey=x", "interval": "5m", "enabled": false, "max_items": 50,
			"headers": {"Cookie": "k=v"}, "headers_env": {"Authorization": "FEED_AUTH"}}, {"name": "b", "url": "http://example.com/atom"}],
		"rules": [{"name": "shows", "feeds": ["a"], "patterns": ["*Show*", "re:S0\\dE\\d+"], "exclude": ["*720p*"], "category": "tv",
			"paused": true, "min_size": 1, "max_size": 2, "episodes": true, "max_per_poll": 3, "enabled": false}, {"name": "all"}]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.BitTorrent.Client.RSS
	if r.History() != 500 || r.FeedBytes() != 1<<20 || r.FetchTimeout() != 10*time.Second || len(r.Feeds) != 2 || len(r.Rules) != 2 {
		t.Fatalf("%+v", r)
	}
	a, b := &r.Feeds[0], &r.Feeds[1]
	if a.On() || a.Every() != 5*time.Minute || a.Items() != 50 || !b.On() || b.Every() != DefaultBTRSSInterval || b.Items() != DefaultBTRSSMaxItems {
		t.Fatalf("%+v %+v", a, b)
	}
	if r.Rules[0].On() || r.Rules[0].PerPoll() != 3 || !r.Rules[1].On() || r.Rules[1].PerPoll() != DefaultBTRSSMaxPerPoll || r.Feed("b") != b || r.Feed("c") != nil {
		t.Fatalf("%+v", r.Rules)
	}
	d := &BitTorrentRSS{}
	if d.History() != DefaultBTRSSHistory || d.FeedBytes() != DefaultBTRSSMaxFeedBytes || d.FetchTimeout() != DefaultBTRSSTimeout {
		t.Errorf("defaults %+v", d)
	}
	feed := func(extra string) string {
		return `{"feeds": [{"name": "a", "url": "https://example.com/rss"` + extra + `}]`
	}
	for _, bad := range []struct{ doc, want string }{
		{`{"feeds": [{"name": "-a", "url": "https://x/"}]}`, "not a valid name"},
		{`{"feeds": [{"name": "a", "url": "https://x/"}, {"name": "a", "url": "https://y/"}]}`, `another feed is already named "a"`},
		{`{"feeds": [{"name": "a", "url": "ftp://x/"}]}`, "not an http or https URL"},
		{`{"feeds": [{"name": "a"}]}`, "url"},
		{feed(`, "interval": "30s"`) + `}`, "interval is at least 1m"},
		{feed(`, "max_items": 2000`) + `}`, "greater than the maximum"},
		{`{"history_size": 10, ` + feed(``)[1:] + `}`, "more than history_size"},
		{`{"history_size": 20000}`, "greater than the maximum"},
		{`{"max_feed_bytes": 67108864}`, "greater than the maximum"},
		{`{"timeout": "-1s"}`, "negative"},
		{feed(`, "headers": {"Bad Name": "x"}`) + `}`, "not a header's name"},
		{feed(`, "headers": {"Host": "x"}`) + `}`, "the request's own"},
		{feed(`, "headers": {"X-A": "a\u0001"}`) + `}`, "control character"},
		{feed(`, "headers_env": {"X-A": "1BAD"}`) + `}`, "not an environment variable's name"},
		{feed(`, "headers": {"X-A": "a"}, "headers_env": {"X-A": "ENV"}`) + `}`, "in headers too"},
		{`{"rules": [{"name": "a"}, {"name": "a"}]}`, `another rule is already named "a"`},
		{`{"rules": [{"name": "a", "feeds": ["nope"]}]}`, `no feed is named "nope"`},
		{`{"rules": [{"name": "a", "patterns": ["re:("]}]}`, "is not a pattern"},
		{`{"rules": [{"name": "a", "exclude": [""]}]}`, "a pattern is 1 to 512 bytes"},
		{`{"rules": [{"name": "a", "category": "films"}]}`, `no category is named "films"`},
		{`{"rules": [{"name": "a", "min_size": 5, "max_size": 4}]}`, "min_size is more than max_size"},
		{`{"rules": [{"name": "a", "min_size": -1}]}`, "less than the minimum"},
		{`{"rules": [{"name": "a", "max_per_poll": 101}]}`, "greater than the maximum"},
	} {
		_, err := Load([]byte(head + bad.doc + `}}}`))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	var many strings.Builder
	many.WriteString(`{"rules": [`)
	for i := range MaxBTRSSRules + 1 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"name": "r` + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `"}`)
	}
	many.WriteString(`]}`)
	if _, err := Load([]byte(head + many.String() + `}}}`)); err == nil || !strings.Contains(err.Error(), "more than 200") {
		t.Errorf("%d rules: %v", MaxBTRSSRules+1, err)
	}
}

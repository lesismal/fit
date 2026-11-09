package config

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The defaults and bounds of BitTorrentRSS (I208).
const (
	// MaxBTRSSFeeds and MaxBTRSSRules bound the feeds and the rules.
	MaxBTRSSFeeds = 100
	MaxBTRSSRules = 200
	// DefaultBTRSSInterval is a feed's interval when left out, and
	// MinBTRSSInterval the least it may be.
	DefaultBTRSSInterval = 15 * time.Minute
	MinBTRSSInterval     = time.Minute
	// DefaultBTRSSMaxItems is a feed's max_items when left out, and
	// MaxBTRSSMaxItems the most it may be.
	DefaultBTRSSMaxItems = 200
	MaxBTRSSMaxItems     = 1000
	// DefaultBTRSSHistory is history_size when left out, and MaxBTRSSHistory
	// the most it may be.
	DefaultBTRSSHistory = 1000
	MaxBTRSSHistory     = 10000
	// DefaultBTRSSMaxFeedBytes is max_feed_bytes when left out, and
	// MaxBTRSSMaxFeedBytes the most it may be.
	DefaultBTRSSMaxFeedBytes = 4 << 20
	MaxBTRSSMaxFeedBytes     = 32 << 20
	// DefaultBTRSSTimeout bounds a feed's download when timeout is left
	// out.
	DefaultBTRSSTimeout = 30 * time.Second
	// DefaultBTRSSMaxPerPoll is a rule's max_per_poll when left out, and
	// MaxBTRSSMaxPerPoll the most it may be.
	DefaultBTRSSMaxPerPoll = 5
	MaxBTRSSMaxPerPoll     = 100
	// MaxBTRSSPatterns bounds a rule's patterns and exclude lists, and
	// MaxBTRSSPatternBytes a pattern.
	MaxBTRSSPatterns     = 32
	MaxBTRSSPatternBytes = 512
	// MaxBTRSSHeaders bounds a feed's headers and headers_env together.
	MaxBTRSSHeaders = 16
)

// BitTorrentRSS is the BitTorrent client's RSS auto-download: feeds polled
// on their intervals and rules that add the torrents of their items.
type BitTorrentRSS struct {
	Feeds        []BitTorrentRSSFeed `json:"feeds,omitempty" doc:"The feeds polled, RSS 2.0 (or 1.0) and Atom, each on its own interval. Their URLs come from the config alone. At most 100."`
	Rules        []BitTorrentRSSRule `json:"rules,omitempty" doc:"The rules that pick the items whose torrents are added, tried in order: an item is added by the first rule whose feeds, patterns and size bounds it meets; an item no rule picks is only remembered as seen. At most 200."`
	HistorySize  int                 `json:"history_size,omitempty" min:"0" max:"10000" default:"1000" doc:"How many items of each feed are remembered as seen, in data_dir/rss.json, so that an item is handled once, across restarts; the oldest are forgotten first. At least each feed's max_items. 1000 when left out or 0."`
	MaxFeedBytes int64               `json:"max_feed_bytes,omitempty" min:"0" max:"33554432" default:"4194304" doc:"The most bytes of a feed downloaded: a larger one fails its poll. 4 MiB when left out or 0, at most 32 MiB."`
	Timeout      Duration            `json:"timeout,omitempty" default:"\"30s\"" doc:"How long a feed's download may take in all: connecting, redirects (at most 5) and the body. 30s when left out."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentRSSFeed is a feed the client polls.
type BitTorrentRSSFeed struct {
	Name       string            `json:"name" fit:"required" doc:"The feed's name, which the rules, the Admin API and data_dir/rss.json know it by. Letters, digits, '_', '.' and '-'."`
	URL        string            `json:"url" fit:"required" doc:"The feed's http or https URL. Redirects are followed, at most 5. A private tracker's passkey may be in it: the Admin API and the pages show it without its user and query."`
	Interval   Duration          `json:"interval,omitempty" default:"\"15m\"" doc:"How often the feed is polled, at least 1m; the server's ETag and Last-Modified are sent back, so that an unchanged feed answers 304 and costs nothing more. 15m when left out."`
	Enabled    *bool             `json:"enabled,omitempty" default:"true" doc:"Poll the feed. On when left out. The Admin API may turn it off or on, which data_dir keeps until it is turned back."`
	MaxItems   int               `json:"max_items,omitempty" min:"0" max:"1000" default:"200" doc:"The most items of the feed read at a poll, the first in the document; the rest are left out. 200 when left out or 0."`
	Headers    map[string]string `json:"headers,omitempty" doc:"Headers sent with the feed's requests, as a cookie a site wants. Not Host or the body's framing. A secret is better named by headers_env."`
	HeadersEnv map[string]string `json:"headers_env,omitempty" doc:"Headers sent with the feed's requests whose values are read from environment variables at each poll, by the header's name: {\"Authorization\": \"FEED_AUTH\"}. A variable not set sends no header."`

	At Value `json:"-" fit:"self"`
}

// BitTorrentRSSRule is a rule that adds the torrents of some items.
type BitTorrentRSSRule struct {
	Name       string   `json:"name" fit:"required" doc:"The rule's name, which the Admin API and data_dir/rss.json know it by. Letters, digits, '_', '.' and '-'."`
	Feeds      []string `json:"feeds,omitempty" doc:"The feeds whose items the rule looks at, by name. Every feed when left out."`
	Patterns   []string `json:"patterns,omitempty" doc:"Patterns an item's title must match one of, in any case: a glob matching the whole title, where * matches any run of characters and ? one, as *Show Name*1080p*; or, after re:, a regular expression (Go's syntax) found anywhere in it, as re:S0[1-3]E\\d+. Every title when left out. At most 32."`
	Exclude    []string `json:"exclude,omitempty" doc:"Patterns, as patterns writes them, of titles the rule leaves out, as *720p* or re:(?:cam|ts)\\b. None when left out. At most 32."`
	Category   string   `json:"category,omitempty" doc:"The category the torrents are added in, one of categories. None, download_dir, when left out."`
	Paused     bool     `json:"paused,omitempty" doc:"Add the torrents paused."`
	MinSize    int64    `json:"min_size,omitempty" min:"0" doc:"The least size of an item's content, in bytes, from its torrent:contentLength, or its enclosure's length. An item whose size the feed does not give passes. No bound when left out or 0."`
	MaxSize    int64    `json:"max_size,omitempty" min:"0" doc:"The most size of an item's content, in bytes, as min_size reads it. No bound when left out or 0."`
	Episodes   bool     `json:"episodes,omitempty" doc:"Add each episode once: an item whose title names an episode (S01E02, 1x02, or a date, as 2026-11-09) the rule has added already, of a show of the same name (the title before the episode), is left out, as another quality or a repack of it. Without an episode in the title, an item is told only by its own ID."`
	MaxPerPoll int      `json:"max_per_poll,omitempty" min:"0" max:"100" default:"5" doc:"The most torrents the rule adds at one poll of a feed; the items past it are left for the next poll. 5 when left out or 0."`
	Enabled    *bool    `json:"enabled,omitempty" default:"true" doc:"Use the rule. On when left out. The Admin API may turn it off or on, which data_dir keeps until it is turned back."`

	At Value `json:"-" fit:"self"`
}

// On reports whether the feed is enabled by the config.
func (f *BitTorrentRSSFeed) On() bool { return f.Enabled == nil || *f.Enabled }

// Every returns interval, or its default.
func (f *BitTorrentRSSFeed) Every() time.Duration {
	return durationOr(f.Interval, DefaultBTRSSInterval)
}

// Items returns max_items, or its default.
func (f *BitTorrentRSSFeed) Items() int {
	return int(orDefault(int64(f.MaxItems), DefaultBTRSSMaxItems))
}

// On reports whether the rule is enabled by the config.
func (r *BitTorrentRSSRule) On() bool { return r.Enabled == nil || *r.Enabled }

// PerPoll returns max_per_poll, or its default.
func (r *BitTorrentRSSRule) PerPoll() int {
	return int(orDefault(int64(r.MaxPerPoll), DefaultBTRSSMaxPerPoll))
}

// History returns history_size, or its default.
func (r *BitTorrentRSS) History() int {
	return int(orDefault(int64(r.HistorySize), DefaultBTRSSHistory))
}

// FeedBytes returns max_feed_bytes, or its default.
func (r *BitTorrentRSS) FeedBytes() int64 { return orDefault(r.MaxFeedBytes, DefaultBTRSSMaxFeedBytes) }

// FetchTimeout returns timeout, or its default.
func (r *BitTorrentRSS) FetchTimeout() time.Duration {
	return durationOr(r.Timeout, DefaultBTRSSTimeout)
}

// Feed returns the feed named name, or nil.
func (r *BitTorrentRSS) Feed(name string) *BitTorrentRSSFeed {
	for i := range r.Feeds {
		if r.Feeds[i].Name == name {
			return &r.Feeds[i]
		}
	}
	return nil
}

// TitlePattern compiles a pattern of a rule's patterns or exclude: a glob
// matching the whole title, * any run and ? one character, or after "re:"
// a regular expression found anywhere in it; both in any case.
func TitlePattern(p string) (*regexp.Regexp, error) {
	if re, ok := strings.CutPrefix(p, "re:"); ok {
		return regexp.Compile("(?i)" + re)
	}
	var b strings.Builder
	b.WriteString(`(?is)^`)
	for _, c := range p {
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString(`$`)
	return regexp.Compile(b.String())
}

// rssHeadersRefused are the headers a feed may not set.
var rssHeadersRefused = []string{"host", "content-length", "transfer-encoding", "connection", "te", "upgrade",
	"if-none-match", "if-modified-since"}

func checkBitTorrentRSS(b *BitTorrentClient, add func(Value, string, ...any)) {
	s := b.RSS
	if s == nil {
		return
	}
	if len(s.Feeds) > MaxBTRSSFeeds {
		add(s.At.Field("feeds"), "%d feeds, more than %d", len(s.Feeds), MaxBTRSSFeeds)
	}
	if len(s.Rules) > MaxBTRSSRules {
		add(s.At.Field("rules"), "%d rules, more than %d", len(s.Rules), MaxBTRSSRules)
	}
	for i := range s.Feeds {
		f := &s.Feeds[i]
		switch {
		case !namePattern.MatchString(f.Name):
			add(f.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", f.Name)
		case slices.IndexFunc(s.Feeds, func(o BitTorrentRSSFeed) bool { return o.Name == f.Name }) < i:
			add(f.At.Field("name"), "another feed is already named %q", f.Name)
		}
		if u, err := url.Parse(f.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(f.URL) > 8<<10 {
			add(f.At.Field("url"), "%q is not an http or https URL of at most 8 KiB", f.URL)
		}
		if f.Interval.Std() != 0 && f.Interval.Std() < MinBTRSSInterval {
			add(f.At.Field("interval"), "interval is at least %s", MinBTRSSInterval)
		}
		if f.MaxItems >= 0 && f.MaxItems <= MaxBTRSSMaxItems && s.HistorySize >= 0 && s.HistorySize <= MaxBTRSSHistory && f.Items() > s.History() {
			add(f.At.Field("max_items"), "max_items (%d) is more than history_size (%d): items read would be forgotten as seen at once", f.Items(), s.History())
		}
		if len(f.Headers)+len(f.HeadersEnv) > MaxBTRSSHeaders {
			add(f.At.Field("headers"), "%d headers, more than %d", len(f.Headers)+len(f.HeadersEnv), MaxBTRSSHeaders)
		}
		for _, h := range []struct {
			field string
			m     map[string]string
		}{{"headers", f.Headers}, {"headers_env", f.HeadersEnv}} {
			for k, v := range h.m {
				at := f.At.Field(h.field).Field(k)
				switch {
				case !isToken(k):
					add(at, "%q is not a header's name", k)
				case slices.Contains(rssHeadersRefused, strings.ToLower(k)):
					add(at, "%s is the request's own", k)
				case h.field == "headers_env" && !envNamePattern.MatchString(v):
					add(at, "%q is not an environment variable's name", v)
				case strings.ContainsFunc(v, func(r rune) bool { return r < ' ' && r != '\t' || r == 0x7f }):
					add(at, "the value holds a control character")
				}
				if h.field == "headers_env" {
					if _, dup := f.Headers[k]; dup {
						add(at, "%s is in headers too", k)
					}
				}
			}
		}
	}
	for i := range s.Rules {
		r := &s.Rules[i]
		switch {
		case !namePattern.MatchString(r.Name):
			add(r.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", r.Name)
		case slices.IndexFunc(s.Rules, func(o BitTorrentRSSRule) bool { return o.Name == r.Name }) < i:
			add(r.At.Field("name"), "another rule is already named %q", r.Name)
		}
		for j, name := range r.Feeds {
			if s.Feed(name) == nil {
				add(r.At.Field("feeds").Index(j), "no feed is named %q", name)
			}
		}
		for _, l := range []struct {
			field string
			list  []string
		}{{"patterns", r.Patterns}, {"exclude", r.Exclude}} {
			if len(l.list) > MaxBTRSSPatterns {
				add(r.At.Field(l.field), "%d patterns, more than %d", len(l.list), MaxBTRSSPatterns)
			}
			for j, p := range l.list {
				if p == "" || len(p) > MaxBTRSSPatternBytes {
					add(r.At.Field(l.field).Index(j), "a pattern is 1 to %d bytes", MaxBTRSSPatternBytes)
				} else if _, err := TitlePattern(p); err != nil {
					add(r.At.Field(l.field).Index(j), "%q is not a pattern: %v", p, err)
				}
			}
		}
		if r.Category != "" && b.Category(r.Category) == nil {
			add(r.At.Field("category"), "no category is named %q", r.Category)
		}
		if r.MaxSize > 0 && r.MinSize > r.MaxSize {
			add(r.At.Field("min_size"), "min_size is more than max_size")
		}
	}
}

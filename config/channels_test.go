package config

import (
	"strings"
	"testing"
	"time"
)

func TestChannels(t *testing.T) {
	head := `{"listeners": [{"name": "web", "address": ":8080"}], "media_library": [{"name": "films", "roots": ["/srv/films"]}], "streaming": `
	c, err := Load([]byte(head + `{"channels": [
	  {"name": "tv/films", "library": "films", "items": ["films/bbb", "sintel"], "order": "shuffle", "loop": false,
	   "schedule": [{"at": "20:00", "item": "sintel"}, {"at": "mon,wed-fri 09:30:15", "item": "films/bbb"}, {"at": "2026-12-31 23:00", "item": "sintel"}],
	   "time_zone": "Asia/Shanghai", "rendition": "720p", "read_ahead": "5s", "max_buffer_bytes": 2097152,
	   "push": [{"url": "rtmp://cdn.example.com/live/key"}, {"name": "srt-out", "url": "srt://203.0.113.5:9000?streamid=%23!::r=live/tv,m=publish&passphrase=0123456789&pbkeylen=32&latency=200&mode=caller"}]},
	  {"name": "tv/news", "library": "films", "schedule": [{"at": "12:00", "item": "news"}]}],
	  "push": [{"stream": "live/cam", "url": "rtmps://ingest.example.com/app/key", "retry_min": "2s", "retry_max": "1m", "connect_timeout": "5s"},
	           {"stream": "tv/films", "url": "rtmp://a.example.com:1936/app/k2"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Streaming
	ch := &s.Channels[0]
	loc, err := ch.Location()
	if err != nil || loc.String() != "Asia/Shanghai" || ch.Looped() || !ch.Shuffled() || ch.ReadAheadOr() != 5*time.Second || ch.BufferBytes() != 2<<20 {
		t.Fatalf("%+v %v", ch, err)
	}
	news := &s.Channels[1]
	if loc, _ := news.Location(); loc != time.UTC || !news.Looped() || news.Shuffled() || news.ReadAheadOr() != DefaultChannelReadAhead ||
		news.BufferBytes() != DefaultChannelMaxBufferBytes {
		t.Errorf("defaults: %+v", news)
	}
	targets := s.PushTargets()
	names := []string{}
	for _, p := range targets {
		names = append(names, p.Name+"@"+p.Stream+"@"+p.Channel)
	}
	if got := strings.Join(names, " "); got != "live/cam#1@live/cam@ tv/films#1@tv/films@ tv/films#2@tv/films@tv/films srt-out@tv/films@tv/films" {
		t.Errorf("push targets: %s", got)
	}
	p := targets[0]
	if p.RetryMinOr() != 2*time.Second || p.RetryMaxOr() != time.Minute || p.Connect() != 5*time.Second {
		t.Errorf("%+v", p)
	}
	if q := targets[1]; q.RetryMinOr() != DefaultPushRetryMin || q.RetryMaxOr() != DefaultPushRetryMax || q.Connect() != DefaultPushConnectTimeout {
		t.Errorf("defaults: %+v", q)
	}
	if (&StreamPush{RetryMin: Duration(time.Minute), RetryMax: Duration(time.Second)}).RetryMaxOr() != time.Minute {
		t.Error("retry_max below retry_min")
	}

	for _, bad := range []struct{ doc, want string }{
		{`{"channels": [{"name": "/x", "library": "films", "items": ["a"]}]}`, "not a stream's name"},
		{`{"channels": [{"name": "x/../y", "library": "films", "items": ["a"]}]}`, "not a stream's name"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["a"]}, {"name": "x", "library": "films", "items": ["a"]}]}`, "already named"},
		{`{"channels": [{"name": "x", "library": "none", "items": ["a"]}]}`, `no media_library named "none"`},
		{`{"channels": [{"name": "x", "library": "films"}]}`, "neither is set"},
		{`{"channels": [{"name": "x", "library": "films", "items": [""]}]}`, "may not be empty"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["/a"]}]}`, "not an item's ID"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["a b"]}]}`, "not an item's ID"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["` + strings.Repeat("a", 300) + `"]}]}`, "at most 256"},
		{`{"channels": [{"name": "x", "library": "films", "order": "random", "items": ["a"]}]}`, "random"},
		{`{"channels": [{"name": "x", "library": "films", "schedule": [{"at": "25:00", "item": "a"}]}]}`, "is not a time"},
		{`{"channels": [{"name": "x", "library": "films", "schedule": [{"at": "someday 10:00", "item": "a"}]}]}`, "not a day of the week"},
		{`{"channels": [{"name": "x", "library": "films", "schedule": [{"at": "mon-xyz 10:00", "item": "a"}]}]}`, "not a day of the week"},
		{`{"channels": [{"name": "x", "library": "films", "schedule": [{"at": "10:00", "item": ""}]}]}`, "may not be empty"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["a"], "time_zone": "Mars/Base"}]}`, "not a time zone"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["a"], "read_ahead": "100ms"}]}`, "500ms to 1m"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["a"], "max_buffer_bytes": 1000}]}`, "at least 1048576"},
		{`{"channels": [{"name": "x", "library": "films", "items": ["a"], "push": [{"stream": "y", "url": "rtmp://h/a/b"}]}]}`, "leave stream out"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"url": "rtmp://h/a/b"}]}`, "names the stream"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a b", "url": "rtmp://h/a/b"}]}`, "not a stream's name"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"name": "p", "stream": "a", "url": "rtmp://h/a/b"}, {"name": "p", "stream": "b", "url": "rtmp://h/a/b"}]}`, "already named"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "http://h/a/b"}]}`, "rtmp://, rtmps:// or srt://"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "rtmp:///a/b"}]}`, "needs a host"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "rtmp://h/a"}]}`, "names an app and a stream"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h"}]}`, "a host and a port"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h:1/x"}]}`, "has no path"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h:1?foo=1"}]}`, `not "foo"`},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h:1?mode=listener"}]}`, "mode may only be caller"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h:1?passphrase=short"}]}`, "10 to 79"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h:1?pbkeylen=20"}]}`, "16, 24 or 32"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "srt://h:1?latency=5"}]}`, "20 to 10000"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "rtmp://h/a/b", "retry_min": "1m", "retry_max": "1s"}]}`, "more than retry_max"},
		{`{"rtmp": {"listen": ":1935"}, "push": [{"stream": "a", "url": "%%"}]}`, "does not parse"},
		{`{}`, "nothing takes streams in"},
	} {
		_, err := Load([]byte(head + bad.doc + `}`))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
		if err != nil && strings.Contains(err.Error(), "/key") {
			t.Errorf("an error holds the url: %v", err)
		}
	}
	many := make([]string, MaxChannels+1)
	for i := range many {
		many[i] = `{"name": "c` + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + `", "library": "films", "items": ["a"]}`
	}
	if _, err := Load([]byte(head + `{"channels": [` + strings.Join(many, ",") + `]}}`)); err == nil || !strings.Contains(err.Error(), "at most 256") {
		t.Errorf("too many channels: %v", err)
	}
}

func TestPushURL(t *testing.T) {
	for raw, want := range map[string]string{
		"rtmp://h/app/key?x=1":    "rtmp://h:1935/app/…",
		"rtmps://h/app/key":       "rtmps://h:443/app/…",
		"RTMP://h:1936/app/a/b":   "rtmp://h:1936/app/…",
		"srt://h:9000?streamid=x": "srt://h:9000",
	} {
		u, err := ParsePushURL(raw)
		if err != nil || u.Redacted() != want {
			t.Errorf("%s: %v %v", raw, u, err)
		}
	}
	u, _ := ParsePushURL("srt://h:9000?streamid=s&passphrase=0123456789&pbkeylen=16&latency=300")
	if u.StreamID != "s" || u.Passphrase != "0123456789" || u.PBKeyLen != 16 || u.Latency != 300*time.Millisecond {
		t.Errorf("%+v", u)
	}
	if u, _ := ParsePushURL("srt://h:9000"); u.Latency != DefaultPushSRTLatency {
		t.Errorf("%+v", u)
	}
}

func TestChannelAt(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	ny, _ := time.LoadLocation("America/New_York")
	// A Friday, 10:00 in Shanghai.
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, sh)
	for _, c := range []struct {
		at         string
		loc        *time.Location
		prev, next time.Time
		noPrev     bool
		noNext     bool
	}{
		{at: "09:30", loc: sh, prev: time.Date(2026, 11, 6, 9, 30, 0, 0, sh), next: time.Date(2026, 11, 7, 9, 30, 0, 0, sh)},
		{at: "10:00:00", loc: sh, prev: now, next: now.AddDate(0, 0, 1)},
		{at: "mon 08:00", loc: sh, prev: time.Date(2026, 11, 2, 8, 0, 0, 0, sh), next: time.Date(2026, 11, 9, 8, 0, 0, 0, sh)},
		{at: "sat-mon 12:00", loc: sh, prev: time.Date(2026, 11, 2, 12, 0, 0, 0, sh), next: time.Date(2026, 11, 7, 12, 0, 0, 0, sh)},
		{at: "FRI,sun 11:00", loc: sh, prev: time.Date(2026, 11, 1, 11, 0, 0, 0, sh), next: time.Date(2026, 11, 6, 11, 0, 0, 0, sh)},
		{at: "2026-11-06 09:00", loc: sh, prev: time.Date(2026, 11, 6, 9, 0, 0, 0, sh), noNext: true},
		{at: "2026-11-07T09:00:30", loc: sh, noPrev: true, next: time.Date(2026, 11, 7, 9, 0, 30, 0, sh)},
		// New York leaves summer time on 2026-11-01 at 02:00: 01:30 is the
		// time of day as written, its first.
		{at: "01:30", loc: ny, prev: time.Date(2026, 11, 5, 1, 30, 0, 0, ny), next: time.Date(2026, 11, 6, 1, 30, 0, 0, ny)},
	} {
		a, err := ParseChannelAt(c.at)
		if err != nil {
			t.Fatalf("%s: %v", c.at, err)
		}
		prev, ok := a.Prev(now, c.loc)
		if ok == c.noPrev || ok && !prev.Equal(c.prev) {
			t.Errorf("%s: prev %v %v, want %v", c.at, prev, ok, c.prev)
		}
		next, ok := a.Next(now, c.loc)
		if ok == c.noNext || ok && !next.Equal(c.next) {
			t.Errorf("%s: next %v %v, want %v", c.at, next, ok, c.next)
		}
	}
	if _, ok := (ChannelAt{}).Prev(now, sh); ok {
		t.Error("a slot on no day")
	}
	if _, ok := (ChannelAt{}).Next(now, sh); ok {
		t.Error("a slot on no day")
	}
	if a, _ := ParseChannelAt("2026-11-06 09:00"); !a.Once() {
		t.Error("Once")
	}
	if !ValidStreamPath("a/b.c/_d~e-f") || ValidStreamPath("") || ValidStreamPath("a//b") || ValidStreamPath(".a") || ValidStreamPath(strings.Repeat("a", 300)) {
		t.Error("ValidStreamPath")
	}
}

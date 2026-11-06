package config

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The defaults and bounds of the streaming role's channels and push targets.
const (
	DefaultChannelReadAhead       = 3 * time.Second
	MinChannelReadAhead           = 500 * time.Millisecond
	MaxChannelReadAhead           = time.Minute
	DefaultChannelMaxBufferBytes  = 16 << 20
	MinChannelMaxBufferBytes      = 1 << 20
	MaxChannelItems               = 10000
	MaxChannelSlots               = 1000
	MaxChannels                   = 256
	DefaultChannelKeyRotation     = 10
	MaxStreamPushTargets          = 256
	DefaultPushRetryMin           = time.Second
	DefaultPushRetryMax           = 30 * time.Second
	DefaultPushConnectTimeout     = 10 * time.Second
	DefaultPushSRTLatency         = 120 * time.Millisecond
	maxChannelItemLength          = 256
	ChannelOrderSequence          = "sequence"
	ChannelOrderShuffle           = "shuffle"
	PushProtocolRTMP              = "rtmp"
	PushProtocolRTMPS             = "rtmps"
	PushProtocolSRT               = "srt"
	defaultChannelTimeZone        = "UTC"
	channelTimeLayoutWithSeconds  = "15:04:05"
	channelTimeLayout             = "15:04"
	channelDateLayoutWithSeconds  = "2006-01-02 15:04:05"
	channelDateLayout             = "2006-01-02 15:04"
	channelDateLayoutT            = "2006-01-02T15:04"
	channelDateLayoutTWithSeconds = "2006-01-02T15:04:05"
)

// StreamChannel is a channel of the streaming role: items of a media
// library played into the hub, under the channel's name, as a live stream,
// in order, shuffled, looped or on a schedule. See docs/channels.md.
type StreamChannel struct {
	Name           string        `json:"name" fit:"required" doc:"The stream the channel publishes into the hub, as tv/films: every egress of the streaming role plays it by that path, under its auth, as rtmp://host/tv/films or /hls/tv/films/index.m3u8. Parts of letters, digits, '.', '_', '~' and '-', split at '/'. A channel renamed is another channel: the old one ends."`
	Library        string        `json:"library" fit:"required" doc:"The media_library the channel's items are of."`
	Items          []string      `json:"items,omitempty" doc:"The IDs of the library's items the channel plays, in this order unless order is shuffle, and again from the first when loop is on; between the schedule's slots too. An item the library does not hold, or cannot serve, is skipped. At most 10000."`
	Order          string        `json:"order,omitempty" enum:"sequence,shuffle" default:"\"sequence\"" doc:"The order items are played in: sequence, as listed, or shuffle, every item once in a random order each time round. sequence when left out."`
	Loop           *bool         `json:"loop,omitempty" default:"true" doc:"Play items again from the first once the last has played. With loop off, the channel goes quiet after the last, staying published, until its next slot, or until the Admin API stops and starts it, which plays the list again from the first. On when left out."`
	Schedule       []ChannelSlot `json:"schedule,omitempty" doc:"Items started at set times, in time_zone, cutting what plays: daily, on days of the week, or once. A slot whose time has passed while its item would still be playing, when the channel starts, is joined where it would be. Between slots the channel plays items, or goes quiet without them. At most 1000."`
	TimeZone       string        `json:"time_zone,omitempty" default:"\"UTC\"" doc:"The IANA time zone the schedule's times are in, as Asia/Shanghai or Local for the host's. UTC when left out."`
	Rendition      string        `json:"rendition,omitempty" doc:"The rendition played of each item, by its name, as 720p; one an item lacks plays its best instead. The rendition with the most pixels, the highest bit rate among equals, when left out."`
	ReadAhead      Duration      `json:"read_ahead,omitempty" default:"\"3s\"" doc:"How much of the items is read ahead of what is sent, on the library's file pool, so that a slow disk does not stall the channel and the next item follows the last without a gap. 500ms to 1m. 3s when left out."`
	MaxBufferBytes int64         `json:"max_buffer_bytes,omitempty" min:"0" default:"16777216" doc:"The most bytes read ahead and not yet sent, whatever read_ahead says, at least 1 MiB; a sample larger than it ends its item. 16 MiB when left out or 0."`
	Push           []StreamPush  `json:"push,omitempty" doc:"Outside servers the channel is relayed to, as streaming's push names them, their stream being the channel's."`
	Encrypt        bool          `json:"encrypt,omitempty" doc:"Encrypts the channel's HLS segments with AES-128 (METHOD=AES-128), fMP4 and MPEG-TS alike, under a key derived from the library's secret, which the hls handler serves only to a request its auth lets play the channel; so that segments copied, as a CDN keeps them, do not play without it. Needs the library's secret or secret_file. Low-Latency HLS parts are not served for it. The other egresses play the channel as before, under their auth. Off when left out."`
	KeyRotation    int           `json:"key_rotation,omitempty" min:"0" max:"100000" default:"10" doc:"With encrypt, how many segments one key encrypts before the next takes over, so that a key handed out opens a few segments only. 10 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// ChannelSlot is an item of a channel's schedule.
type ChannelSlot struct {
	At   string `json:"at" fit:"required" doc:"When the slot starts, in the channel's time_zone: HH:MM or HH:MM:SS every day; days of the week before it, as mon,wed,fri 20:00 or sat-sun 09:30; or a date, as 2026-12-31 23:00, once."`
	Item string `json:"item" fit:"required" doc:"The ID of the library's item the slot plays, from its start."`

	Self Value `json:"-" fit:"self"`
}

// StreamPush is an outside server a stream of the hub is relayed to.
type StreamPush struct {
	Name           string   `json:"name,omitempty" doc:"The target's name in the Admin API and the metrics, unique among the push targets. The stream's name and the target's place among the stream's when left out, as tv/films#1."`
	Stream         string   `json:"stream,omitempty" doc:"The stream of the hub relayed, as live/cam or a channel's name. Required in streaming's push; a channel's push targets relay the channel."`
	URL            string   `json:"url" fit:"required" doc:"Where the stream is sent: rtmp://host[:port]/app/stream, or rtmps://, publishing to an RTMP server as a CDN's ingest takes it; or srt://host:port?streamid=...&passphrase=...&latency=ms, calling an SRT listener with MPEG-TS. The URL is a secret, its stream key with it: the Admin API and the logs show its scheme, host and app alone."`
	RetryMin       Duration `json:"retry_min,omitempty" default:"\"1s\"" doc:"How long the first retry waits, after a target refuses, drops or cannot be reached, or the stream is not published; each retry waits twice the last, up to retry_max, and a push that has stayed up retry_max starts again from retry_min. 1s when left out."`
	RetryMax       Duration `json:"retry_max,omitempty" default:"\"30s\"" doc:"The longest a retry waits. 30s when left out."`
	ConnectTimeout Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long connecting may take, the RTMP handshake and publish or the SRT handshake. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// ReadAheadOr returns read_ahead, or its default.
func (c *StreamChannel) ReadAheadOr() time.Duration {
	return durationOr(c.ReadAhead, DefaultChannelReadAhead)
}

// BufferBytes returns max_buffer_bytes, or its default.
func (c *StreamChannel) BufferBytes() int64 {
	return orDefault(c.MaxBufferBytes, DefaultChannelMaxBufferBytes)
}

// Rotation returns key_rotation, or its default.
func (c *StreamChannel) Rotation() int {
	return int(orDefault(int64(c.KeyRotation), DefaultChannelKeyRotation))
}

// Looped reports whether loop is on, as it is when left out.
func (c *StreamChannel) Looped() bool { return c.Loop == nil || *c.Loop }

// Shuffled reports whether order is shuffle.
func (c *StreamChannel) Shuffled() bool { return c.Order == ChannelOrderShuffle }

// Location returns time_zone's location, UTC when left out, or the error
// of a time zone that is not known.
func (c *StreamChannel) Location() (*time.Location, error) {
	tz := c.TimeZone
	if tz == "" {
		tz = defaultChannelTimeZone
	}
	return time.LoadLocation(tz)
}

// RetryMinOr returns retry_min, or its default.
func (p *StreamPush) RetryMinOr() time.Duration { return durationOr(p.RetryMin, DefaultPushRetryMin) }

// RetryMaxOr returns retry_max, or its default; never less than retry_min.
func (p *StreamPush) RetryMaxOr() time.Duration {
	return max(durationOr(p.RetryMax, DefaultPushRetryMax), p.RetryMinOr())
}

// Connect returns connect_timeout, or its default.
func (p *StreamPush) Connect() time.Duration {
	return durationOr(p.ConnectTimeout, DefaultPushConnectTimeout)
}

// PushTarget is a push target as the role runs it: from streaming's push or
// a channel's, its name and stream filled in.
type PushTarget struct {
	StreamPush
	// Channel is the channel whose push it is, "" for streaming's.
	Channel string
}

// PushTargets returns every push target of s, streaming's and then each
// channel's, with its name and stream filled in: a target without a name
// is named after its stream and its place among the stream's targets, from
// 1, as tv/films#1.
func (s *Streaming) PushTargets() []PushTarget {
	var out []PushTarget
	n := map[string]int{}
	add := func(p StreamPush, channel string) {
		if channel != "" {
			p.Stream = channel
		}
		n[p.Stream]++
		if p.Name == "" {
			p.Name = p.Stream + "#" + strconv.Itoa(n[p.Stream])
		}
		out = append(out, PushTarget{StreamPush: p, Channel: channel})
	}
	for _, p := range s.Push {
		add(p, "")
	}
	for _, c := range s.Channels {
		for _, p := range c.Push {
			add(p, c.Name)
		}
	}
	return out
}

// PushURL is a push target's URL taken apart.
type PushURL struct {
	// Protocol is rtmp, rtmps or srt.
	Protocol string
	// Host is host:port, the default port filled in for RTMP.
	Host string
	// App is an RTMP URL's first path element.
	App string
	// SRT's options: the stream ID, passphrase, its key length and the
	// latency asked for.
	StreamID   string
	Passphrase string
	PBKeyLen   int
	Latency    time.Duration
}

// Redacted returns the URL as the Admin API and the logs show it: its
// scheme, host and, for RTMP, app; never the stream key, nor SRT's stream
// ID and passphrase.
func (u *PushURL) Redacted() string {
	if u.App != "" {
		return u.Protocol + "://" + u.Host + "/" + u.App + "/…"
	}
	return u.Protocol + "://" + u.Host
}

// ParsePushURL takes a push target's url apart, or says what is wrong with
// it; the error never holds the URL, which is a secret.
func ParsePushURL(raw string) (*PushURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("the url does not parse")
	}
	p := &PushURL{Protocol: strings.ToLower(u.Scheme)}
	switch p.Protocol {
	case PushProtocolRTMP, PushProtocolRTMPS:
		if u.Host == "" {
			return nil, fmt.Errorf("an %s url needs a host", p.Protocol)
		}
		p.Host = u.Host
		if u.Port() == "" {
			port := "1935"
			if p.Protocol == PushProtocolRTMPS {
				port = "443"
			}
			p.Host = net.JoinHostPort(u.Hostname(), port)
		}
		app, stream, _ := strings.Cut(strings.TrimPrefix(u.Path, "/"), "/")
		if app == "" || stream == "" {
			return nil, fmt.Errorf("an %s url names an app and a stream: %s://host/app/stream", p.Protocol, p.Protocol)
		}
		p.App = app
	case PushProtocolSRT:
		if u.Hostname() == "" || u.Port() == "" {
			return nil, fmt.Errorf("an srt url needs a host and a port, as srt://host:9000")
		}
		if u.Path != "" && u.Path != "/" {
			return nil, fmt.Errorf("an srt url has no path: the stream goes in streamid")
		}
		p.Host = u.Host
		q := u.Query()
		for k := range q {
			switch k {
			case "streamid", "passphrase", "pbkeylen", "latency", "mode":
			default:
				return nil, fmt.Errorf("an srt url takes streamid, passphrase, pbkeylen, latency and mode=caller, not %q", k)
			}
		}
		if m := q.Get("mode"); m != "" && m != "caller" {
			return nil, fmt.Errorf("an srt push calls its target: mode may only be caller")
		}
		p.StreamID, p.Passphrase = q.Get("streamid"), q.Get("passphrase")
		if n := len(p.Passphrase); n != 0 && (n < 10 || n > 79) {
			return nil, fmt.Errorf("the passphrase is %d characters: SRT takes 10 to 79", n)
		}
		if v := q.Get("pbkeylen"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n != 16 && n != 24 && n != 32 {
				return nil, fmt.Errorf("pbkeylen is 16, 24 or 32")
			}
			p.PBKeyLen = n
		}
		p.Latency = DefaultPushSRTLatency
		if v := q.Get("latency"); v != "" {
			ms, err := strconv.Atoi(v)
			if err != nil || ms < 20 || ms > 10000 {
				return nil, fmt.Errorf("latency is in milliseconds, 20 to 10000")
			}
			p.Latency = time.Duration(ms) * time.Millisecond
		}
	default:
		return nil, fmt.Errorf("a push url is rtmp://, rtmps:// or srt://")
	}
	return p, nil
}

// ChannelAt is a schedule slot's at, parsed: a time of day, every day or on
// some days of the week, or a date and time, once.
type ChannelAt struct {
	// Days has bit d set for each time.Weekday d the slot is on; all seven
	// for every day, none for a date.
	Days uint8
	// Clock is the time of day, from midnight.
	Clock time.Duration
	// Year, Month and Day are a date's; Year is 0 for a time of day.
	Year  int
	Month time.Month
	Day   int
}

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// ParseChannelAt parses a schedule slot's at.
func ParseChannelAt(s string) (ChannelAt, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{channelDateLayoutWithSeconds, channelDateLayout, channelDateLayoutTWithSeconds, channelDateLayoutT} {
		if t, err := time.Parse(layout, s); err == nil {
			return ChannelAt{Year: t.Year(), Month: t.Month(), Day: t.Day(), Clock: clockOf(t)}, nil
		}
	}
	at := ChannelAt{Days: 0x7f}
	clock := s
	if days, rest, ok := strings.Cut(s, " "); ok {
		clock = strings.TrimSpace(rest)
		at.Days = 0
		for part := range strings.SplitSeq(strings.ToLower(days), ",") {
			from, to, isRange := strings.Cut(part, "-")
			a, ok1 := weekdays[from]
			b, ok2 := weekdays[to]
			if !ok1 || isRange && !ok2 {
				return ChannelAt{}, fmt.Errorf("%q is not a day of the week: mon, tue, wed, thu, fri, sat, sun, or a range of them as mon-fri", part)
			}
			if !isRange {
				b = a
			}
			for d := a; ; d = (d + 1) % 7 {
				at.Days |= 1 << d
				if d == b {
					break
				}
			}
		}
	}
	for _, layout := range []string{channelTimeLayoutWithSeconds, channelTimeLayout} {
		if t, err := time.Parse(layout, clock); err == nil {
			at.Clock = clockOf(t)
			return at, nil
		}
	}
	return ChannelAt{}, fmt.Errorf("%q is not a time: HH:MM or HH:MM:SS, days of the week before it, or a date as 2026-12-31 23:00", s)
}

func clockOf(t time.Time) time.Duration {
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute + time.Duration(t.Second())*time.Second
}

// Once reports whether the slot is on a date, once.
func (a ChannelAt) Once() bool { return a.Year != 0 }

// on returns the time of day of a slot that recurs on the day of t, in
// loc, and whether the slot is on that day.
func (a ChannelAt) on(t time.Time, loc *time.Location) (time.Time, bool) {
	y, m, d := t.In(loc).Date()
	at := a.date(y, m, d, loc)
	return at, a.Days&(1<<at.Weekday()) != 0
}

// date returns the slot's time of day on a date, in loc: across a change of
// the clocks, the time of day as written.
func (a ChannelAt) date(y int, m time.Month, d int, loc *time.Location) time.Time {
	return time.Date(y, m, d, int(a.Clock/time.Hour), int(a.Clock/time.Minute%60), int(a.Clock/time.Second%60), 0, loc)
}

// Prev returns the slot's latest start at or before t, in loc, and false
// when it has none: a date after t, or none within the last week.
func (a ChannelAt) Prev(t time.Time, loc *time.Location) (time.Time, bool) {
	if a.Once() {
		at := a.date(a.Year, a.Month, a.Day, loc)
		return at, !at.After(t)
	}
	for i := range 8 {
		if at, ok := a.on(t.AddDate(0, 0, -i), loc); ok && !at.After(t) {
			return at, true
		}
	}
	return time.Time{}, false
}

// Next returns the slot's first start after t, in loc, and false when it
// has none: a date at or before t.
func (a ChannelAt) Next(t time.Time, loc *time.Location) (time.Time, bool) {
	if a.Once() {
		at := a.date(a.Year, a.Month, a.Day, loc)
		return at, at.After(t)
	}
	for i := range 9 {
		if at, ok := a.on(t.AddDate(0, 0, i), loc); ok && at.After(t) {
			return at, true
		}
	}
	return time.Time{}, false
}

// streamPathPart is what a part of a channel's name may hold.
var streamPathPart = regexp.MustCompile(`^[A-Za-z0-9_~-][A-Za-z0-9._~-]{0,127}$`)

// ValidStreamPath reports whether p is a channel's name: parts of letters,
// digits, '.', '_', '~' and '-', not starting with '.', split at '/', 256
// bytes at most.
func ValidStreamPath(p string) bool {
	if p == "" || len(p) > 256 {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if !streamPathPart.MatchString(part) {
			return false
		}
	}
	return true
}

func (c *Config) checkChannels(add func(Value, string, ...any)) {
	s := c.Streaming
	if s == nil {
		return
	}
	at := s.At.Field("channels")
	if len(s.Channels) > MaxChannels {
		add(at, "%d channels: at most %d", len(s.Channels), MaxChannels)
	}
	for i := range s.Channels {
		ch := &s.Channels[i]
		switch {
		case !ValidStreamPath(ch.Name):
			add(ch.At.Field("name"), "%q is not a stream's name: parts of letters, digits, '.', '_', '~' and '-', split at '/'", ch.Name)
		case slices.IndexFunc(s.Channels, func(o StreamChannel) bool { return o.Name == ch.Name }) < i:
			add(ch.At.Field("name"), "another channel is already named %q", ch.Name)
		}
		if lib := c.MediaLibraryNamed(ch.Library); ch.Library != "" && lib == nil {
			add(ch.At.Field("library"), "there is no media_library named %q", ch.Library)
		} else if ch.Encrypt && lib != nil && !lib.Encryptable() {
			add(ch.At.Field("encrypt"), "encrypt needs a key: set media_library %q's secret or secret_file", ch.Library)
		}
		if len(ch.Items) == 0 && len(ch.Schedule) == 0 {
			add(ch.At, "a channel plays items, a schedule or both: neither is set")
		}
		if len(ch.Items) > MaxChannelItems {
			add(ch.At.Field("items"), "%d items: at most %d", len(ch.Items), MaxChannelItems)
		}
		for j, it := range ch.Items {
			if msg := checkItemID(it); msg != "" {
				add(ch.At.Field("items").Index(j), "%s", msg)
			}
		}
		if len(ch.Schedule) > MaxChannelSlots {
			add(ch.At.Field("schedule"), "%d slots: at most %d", len(ch.Schedule), MaxChannelSlots)
		}
		for j := range ch.Schedule {
			sl := &ch.Schedule[j]
			if _, err := ParseChannelAt(sl.At); err != nil {
				add(sl.Self.Field("at"), "%s", err.Error())
			}
			if msg := checkItemID(sl.Item); msg != "" {
				add(sl.Self.Field("item"), "%s", msg)
			}
		}
		if _, err := ch.Location(); err != nil {
			add(ch.At.Field("time_zone"), "%q is not a time zone: an IANA name, as Asia/Shanghai, or Local", ch.TimeZone)
		}
		if d := ch.ReadAhead.Std(); d != 0 && (d < MinChannelReadAhead || d > MaxChannelReadAhead) {
			add(ch.At.Field("read_ahead"), "read_ahead is %s: %s to %s", d, MinChannelReadAhead, MaxChannelReadAhead)
		}
		if b := ch.MaxBufferBytes; b != 0 && b < MinChannelMaxBufferBytes {
			add(ch.At.Field("max_buffer_bytes"), "max_buffer_bytes is %d: at least %d", b, MinChannelMaxBufferBytes)
		}
		for j := range ch.Push {
			if ch.Push[j].Stream != "" && ch.Push[j].Stream != ch.Name {
				add(ch.Push[j].At.Field("stream"), "a channel's push relays the channel: leave stream out")
			}
		}
	}
	targets := s.PushTargets()
	if len(targets) > MaxStreamPushTargets {
		add(s.At.Field("push"), "%d push targets: at most %d", len(targets), MaxStreamPushTargets)
	}
	for i, t := range targets {
		p := &t.StreamPush
		switch {
		case t.Channel == "" && p.Stream == "":
			add(p.At, "a push target names the stream it relays")
		case t.Channel == "" && !ValidStreamPath(p.Stream):
			add(p.At.Field("stream"), "%q is not a stream's name", p.Stream)
		}
		if slices.IndexFunc(targets, func(o PushTarget) bool { return o.Name == t.Name }) < i {
			add(p.At.Field("name"), "another push target is already named %q", t.Name)
		}
		if _, err := ParsePushURL(p.URL); err != nil {
			add(p.At.Field("url"), "%s", err.Error())
		}
		if lo, hi := p.RetryMin.Std(), p.RetryMax.Std(); lo != 0 && hi != 0 && lo > hi {
			add(p.At.Field("retry_min"), "retry_min is %s, more than retry_max's %s", lo, hi)
		}
	}
}

// checkItemID says what is wrong with a channel's item ID, "" when nothing.
// The library's catalog has the last word: an ID it does not hold is
// skipped when the channel comes to it.
func checkItemID(id string) string {
	switch {
	case id == "":
		return "an item's ID may not be empty"
	case len(id) > maxChannelItemLength:
		return fmt.Sprintf("an item's ID is %d bytes: at most %d", len(id), maxChannelItemLength)
	case strings.HasPrefix(id, "/") || strings.ContainsFunc(id, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		return fmt.Sprintf("%q is not an item's ID: the library's IDs are paths without spaces, not starting with '/'", id)
	}
	return ""
}

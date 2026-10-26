// Package observe is fit's view of the traffic it serves: access logs,
// metrics, and traces exported over OTLP (trace.go).
//
// An access log line is one JSON object per request, written when the
// response has been handed to the connection:
//
//	{"time":"2026-10-19T07:39:56.123Z","listener":"web","route":"api","client":"192.0.2.1",
//	 "proto":"HTTP/1.1","method":"GET","host":"example.com","uri":"/api/x?q=1","status":200,
//	 "bytes_in":0,"bytes_out":512,"duration":0.000412,"upstream":"10.0.0.1:8080",
//	 "upstream_duration":0.000351,"user_agent":"curl/8.9.1","referer":""}
//
// Durations are in seconds. upstream and upstream_duration are there for a
// request that was proxied, and route for one a route took. A request whose
// response is never written, because its connection went first, has no line.
//
// A line is formatted on the goroutine that finished the response and put
// in a buffer; a goroutine of the log's own writes the buffer out, so a slow
// disk or a blocked pipe never holds up a fib worker. When the buffer is full,
// lines are dropped and counted rather than waited for.
//
// The buffer is split in shards, shardsPerProc for each of GOMAXPROCS up to
// 64, each with its own lock, so that workers finishing responses at once
// seldom wait for one another: a request's shard is its client's, not its
// worker's, and with no more shards than workers two of them pick the same
// one often. buffer_bytes bounds what the writer has to write, with what the
// shards hold, though each shard holds up to 64KB of its own past it. A shard
// hands a buffer to the writer as soon as it holds 64KB. A request's line goes to the shard
// of its client's address, so the lines of one connection keep their order;
// lines of different clients may be written out of time order by up to the
// flush interval, as the lines of nginx's workers are.
package observe

import (
	"hash/maphash"
	"io"
	stdhttp "net/http"
	"os"
	goruntime "runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// The defaults config.AccessLog's fields stand for when left out.
const (
	DefaultFlushInterval = time.Second
	DefaultBufferBytes   = 4 << 20
	// flushBytes is how full a shard's buffer gets before it is written
	// without waiting for the interval.
	flushBytes = 64 << 10
)

// AccessLog writes access log lines, buffered. It is safe for concurrent
// use.
type AccessLog struct {
	out      io.Writer
	file     *os.File // the file out is, when the log owns it
	interval time.Duration
	maxBytes int

	shards []shard
	seed   maphash.Seed
	// stamp is the time of the last line, to the second, formatted: lines
	// logged within the same second share it.
	stamp atomic.Pointer[stamp]
	// queued counts the bytes handed to the writer and not yet written.
	// With what each shard holds, it is bounded by maxBytes; a shard adds
	// its bytes when it hands them over, once a buffer rather than once a
	// line, so that loggers on different shards share no counter.
	queued atomic.Int64
	// ready holds the shards' buffers for the writer, in the order each
	// shard filled them: full ones, which hold flushBytes or more of queued
	// each, so that no more than maxBytes/flushBytes are ever there, and at
	// each flush one from each shard. Its capacity leaves room for them all.
	// free holds buffers written out, to reuse.
	ready chan []byte
	free  chan []byte
	// closing orders Close; closed is set once every shard is.
	closing sync.Mutex
	closed  bool
	dropped atomic.Int64

	stop chan struct{}
	done chan struct{}
	// log is where dropped lines are reported.
	log *logs.Logger
	// minStatus is the least status of the responses whose requests are
	// written, by the config's level; a reload may change it.
	minStatus atomic.Int32
}

// shard is a part of the buffer, with a lock of its own. Shards are padded
// apart, so that two shards' locks never share a cache line.
type shard struct {
	mu     sync.Mutex
	buf    []byte
	closed bool
	_      [64]byte
}

// shardsPerProc is how many shards there are for each of GOMAXPROCS, and
// maxShards bounds them, whatever GOMAXPROCS is.
const (
	shardsPerProc = 4
	maxShards     = 64
)

// NewAccessLog opens the log c describes and starts its writer.
func NewAccessLog(c *config.AccessLog) (*AccessLog, error) {
	l := &AccessLog{interval: c.FlushInterval.Std(), maxBytes: c.BufferBytes, log: logs.Named(config.LoggerAccessLog)}
	l.SetLevel(c)
	switch c.Output {
	case "", "stdout":
		l.out = os.Stdout
	case "stderr":
		l.out = os.Stderr
	default:
		f, err := os.OpenFile(c.Output, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
		if err != nil {
			return nil, err
		}
		l.out, l.file = f, f
	}
	l.start()
	return l, nil
}

// NewAccessLogTo returns a log writing to w, for tests and embedders.
func NewAccessLogTo(w io.Writer, interval time.Duration, maxBytes int) *AccessLog {
	l := &AccessLog{out: w, interval: interval, maxBytes: maxBytes, log: logs.Named(config.LoggerAccessLog)}
	l.start()
	return l
}

func (l *AccessLog) start() {
	if l.interval <= 0 {
		l.interval = DefaultFlushInterval
	}
	if l.maxBytes <= 0 {
		l.maxBytes = DefaultBufferBytes
	}
	// A power of two, so that a shard is picked with a mask.
	n := 1
	for n < min(shardsPerProc*goruntime.GOMAXPROCS(0), maxShards) {
		n *= 2
	}
	l.shards = make([]shard, n)
	l.seed = maphash.MakeSeed()
	l.ready = make(chan []byte, l.maxBytes/flushBytes+1+n)
	l.free = make(chan []byte, l.maxBytes/flushBytes+1)
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go l.run()
}

// buffer returns a buffer to fill: one written out before, or a new one.
func (l *AccessLog) buffer() []byte {
	select {
	case b := <-l.free:
		return b
	default:
		return make([]byte, 0, flushBytes+4<<10)
	}
}

// run writes the full buffers as they come, and every interval what the
// shards hold.
func (l *AccessLog) run() {
	defer close(l.done)
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case b := <-l.ready:
			l.write(b)
		case <-t.C:
			l.flush()
		case <-l.stop:
			l.flush()
			return
		}
	}
}

// write writes a buffer out and keeps it to fill again.
func (l *AccessLog) write(b []byte) {
	_, _ = l.out.Write(b)
	l.queued.Add(-int64(len(b)))
	if cap(b) <= 2*flushBytes {
		select {
		case l.free <- b[:0]:
		default:
		}
	}
}

// flush writes what the shards hold, and reports the lines dropped since
// the last flush. Each shard's buffer goes to ready under its lock, as a full
// one does, so that ready has every shard's buffers in the order they were
// filled.
func (l *AccessLog) flush() {
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		if len(sh.buf) > 0 {
			select {
			case l.ready <- sh.buf:
				l.queued.Add(int64(len(sh.buf)))
				sh.buf = nil
			default:
				// ready is full of what came first; the rest waits for the
				// next flush.
			}
		}
		sh.mu.Unlock()
	}
	for {
		select {
		case b := <-l.ready:
			l.write(b)
			continue
		default:
		}
		break
	}
	if n := l.dropped.Swap(0); n > 0 {
		l.log.Warnf("access log: %d lines dropped, the output was too slow", n)
	}
}

// SetLevel has the log write the requests c's level says from now on, as a
// reload does; a nil c changes nothing.
func (l *AccessLog) SetLevel(c *config.AccessLog) {
	if c != nil {
		l.minStatus.Store(int32(c.MinStatus()))
	}
}

// Dropped returns how many lines have been dropped since it was last
// reported.
func (l *AccessLog) Dropped() int64 { return l.dropped.Load() }

// Close writes out what is buffered, stops the writer and closes the file
// the log opened. Lines logged after Close are dropped.
func (l *AccessLog) Close() error {
	l.closing.Lock()
	defer l.closing.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	for i := range l.shards {
		sh := &l.shards[i]
		sh.mu.Lock()
		sh.closed = true
		sh.mu.Unlock()
	}
	close(l.stop)
	<-l.done
	if l.file != nil {
		return l.file.Close()
	}
	return nil
}

// Entry is what is known of a request beyond the request itself: where it
// came in, which route took it, and what the proxy did with it. A handler
// that knows more fills it in before it writes the response.
type Entry struct {
	Start            time.Time
	Listener         string
	Route            string
	Upstream         string
	UpstreamDuration time.Duration
	BytesIn          int64
	// User is who the request was authenticated as, by Basic auth or a
	// client certificate's subject; empty when it was not.
	User string
	// more is what few requests have, kept apart so that the entry of
	// every other stays small: a token's claims, a trace.
	more *entryMore
}

type entryMore struct {
	claims map[string]any
	trace  Trace
	traced bool
}

// Claims are those of the JSON Web Token the request was let in with, for
// later middleware, or nil; the access log does not write them.
func (e *Entry) Claims() map[string]any {
	if e.more == nil {
		return nil
	}
	return e.more.claims
}

// SetClaims keeps a token's claims.
func (e *Entry) SetClaims(claims map[string]any) {
	if e.more == nil {
		e.more = &entryMore{}
	}
	e.more.claims = claims
}

// Trace is the request's place in a trace when the tracer sampled it, and
// nil when it did not; the proxy sends it on to the upstream.
func (e *Entry) Trace() *Trace {
	if e.more == nil || !e.more.traced {
		return nil
	}
	return &e.more.trace
}

// Track gives the request c serves a new entry, for EntryOf to find, and
// returns it for the caller to fill in. The entry is what fit keeps in the
// Context's value slot, which nothing else of fit's uses.
func Track(c *fibhttp.Context) *Entry {
	e := &Entry{}
	c.SetValue(e)
	return e
}

// TrackTraced is Track for a request the tracer sampled: the entry and its
// trace are one allocation.
func TrackTraced(c *fibhttp.Context, tc Trace) *Entry {
	te := &struct {
		Entry
		more entryMore
	}{more: entryMore{trace: tc, traced: true}}
	te.Entry.more = &te.more
	c.SetValue(&te.Entry)
	return &te.Entry
}

// EntryOf returns the entry of the request c serves, or nil when the request
// is not logged.
func EntryOf(c *fibhttp.Context) *Entry {
	e, _ := c.Value().(*Entry)
	return e
}

// Log writes the line for r, which e describes, answered with status and a
// body of size bytes. It never waits for the output: a line that does not
// fit in the buffer is dropped.
func (l *AccessLog) Log(e *Entry, r *stdhttp.Request, status int, size int64) {
	if int32(status) < l.minStatus.Load() {
		return
	}
	end := time.Now()
	var stack [512]byte
	b := append(stack[:0], `{"time":"`...)
	b = l.appendTime(b, end)
	line := appendFields(b, e, r, status, size, end)

	sh := &l.shards[0]
	if len(l.shards) > 1 {
		sh = &l.shards[maphash.String(l.seed, r.RemoteAddr)&uint64(len(l.shards)-1)]
	}
	sh.mu.Lock()
	if sh.closed || l.queued.Load()+int64(len(sh.buf)+len(line)) > int64(l.maxBytes) {
		sh.mu.Unlock()
		l.dropped.Add(1)
		return
	}
	if sh.buf == nil {
		sh.buf = l.buffer()
	}
	sh.buf = append(sh.buf, line...)
	if len(sh.buf) >= flushBytes {
		// Handed over under the lock, so that the writer cannot take what
		// the shard holds next before it; ready has room for it, as queued
		// says, and the select only keeps that from ever waiting.
		select {
		case l.ready <- sh.buf:
			l.queued.Add(int64(len(sh.buf)))
			sh.buf = nil
		default:
		}
	}
	sh.mu.Unlock()
}

// appendLine appends the JSON line for a request to b.
func appendLine(b []byte, e *Entry, r *stdhttp.Request, status int, size int64, end time.Time) []byte {
	b = append(b, `{"time":"`...)
	b = appendTime(b, end)
	return appendFields(b, e, r, status, size, end)
}

// appendFields appends what follows the time in a request's line, which b
// ends with.
func appendFields(b []byte, e *Entry, r *stdhttp.Request, status int, size int64, end time.Time) []byte {
	b = append(b, `","listener":`...)
	b = appendString(b, e.Listener)
	if e.Route != "" {
		b = append(b, `,"route":`...)
		b = appendString(b, e.Route)
	}
	b = append(b, `,"client":`...)
	b = appendString(b, clientIP(r.RemoteAddr))
	if e.User != "" {
		b = append(b, `,"user":`...)
		b = appendString(b, e.User)
	}
	b = append(b, `,"proto":`...)
	b = appendString(b, r.Proto)
	b = append(b, `,"method":`...)
	b = appendString(b, r.Method)
	b = append(b, `,"host":`...)
	b = appendString(b, r.Host)
	b = append(b, `,"uri":`...)
	b = appendString(b, r.RequestURI)
	b = append(b, `,"status":`...)
	b = strconv.AppendInt(b, int64(status), 10)
	b = append(b, `,"bytes_in":`...)
	b = strconv.AppendInt(b, e.BytesIn, 10)
	b = append(b, `,"bytes_out":`...)
	b = strconv.AppendInt(b, size, 10)
	b = append(b, `,"duration":`...)
	b = appendSeconds(b, end.Sub(e.Start))
	if e.Upstream != "" {
		b = append(b, `,"upstream":`...)
		b = appendString(b, e.Upstream)
		b = append(b, `,"upstream_duration":`...)
		b = appendSeconds(b, e.UpstreamDuration)
	}
	b = append(b, `,"user_agent":`...)
	b = appendString(b, headerValue(r.Header, "User-Agent"))
	b = append(b, `,"referer":`...)
	b = appendString(b, headerValue(r.Header, "Referer"))
	return append(b, "}\n"...)
}

// headerValue is h.Get(key) for a key already in canonical form, as the
// keys of a parsed request's header are, without canonicalizing it again.
func headerValue(h stdhttp.Header, key string) string {
	if v := h[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// appendSeconds appends d in seconds with six decimals, as
// strconv.AppendFloat(b, d.Seconds(), 'f', 6, 64) would, rounding to the
// nearest microsecond, without formatting a float.
func appendSeconds(b []byte, d time.Duration) []byte {
	neg := d < 0
	if neg {
		d = -d
	}
	us := uint64((d + time.Microsecond/2) / time.Microsecond)
	if neg && us > 0 {
		b = append(b, '-')
	}
	b = strconv.AppendUint(b, us/1e6, 10)
	frac := us % 1e6
	return append(b, '.',
		byte('0'+frac/100000), byte('0'+frac/10000%10), byte('0'+frac/1000%10),
		byte('0'+frac/100%10), byte('0'+frac/10%10), byte('0'+frac%10))
}

// stamp is a second, and its time formatted up to its fraction:
// "2006-01-02T15:04:05.".
type stamp struct {
	sec  int64
	text [20]byte
}

// appendTime is appendTime(b, t) for a time near the last line's: the date
// and time of day are formatted once a second, and only the milliseconds per
// line.
func (l *AccessLog) appendTime(b []byte, t time.Time) []byte {
	sec := t.Unix()
	s := l.stamp.Load()
	if s == nil || s.sec != sec {
		formatted := appendTime(nil, t)
		if len(formatted) != len("2006-01-02T15:04:05.000Z") {
			return append(b, formatted...)
		}
		s = &stamp{sec: sec}
		copy(s.text[:], formatted)
		l.stamp.Store(s)
	}
	ms := t.Nanosecond() / 1e6
	b = append(b, s.text[:]...)
	return append(b, byte('0'+ms/100), byte('0'+ms/10%10), byte('0'+ms%10), 'Z')
}

// clientIP is the host part of a RemoteAddr.
func clientIP(remote string) string {
	for i := len(remote) - 1; i >= 0; i-- {
		switch remote[i] {
		case ':':
			host := remote[:i]
			if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
				host = host[1 : len(host)-1]
			}
			return host
		case ']':
			return remote
		}
	}
	return remote
}

// appendTime appends t in UTC as "2006-01-02T15:04:05.000Z", as
// Time.AppendFormat would, without parsing a layout.
func appendTime(b []byte, t time.Time) []byte {
	t = t.UTC()
	year, month, day := t.Date()
	hour, minute, sec := t.Clock()
	if year < 0 || year > 9999 {
		return t.AppendFormat(b, "2006-01-02T15:04:05.000Z07:00")
	}
	ms := t.Nanosecond() / 1e6
	return append(b,
		byte('0'+year/1000), byte('0'+year/100%10), byte('0'+year/10%10), byte('0'+year%10), '-',
		byte('0'+int(month)/10), byte('0'+int(month)%10), '-', byte('0'+day/10), byte('0'+day%10), 'T',
		byte('0'+hour/10), byte('0'+hour%10), ':', byte('0'+minute/10), byte('0'+minute%10), ':',
		byte('0'+sec/10), byte('0'+sec%10), '.', byte('0'+ms/100), byte('0'+ms/10%10), byte('0'+ms%10), 'Z')
}

const hex = "0123456789abcdef"

// plain says which bytes go into a JSON string as they are: printable ASCII
// but the quote and the backslash.
var plain = func() (t [256]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		t[c] = c != '"' && c != '\\'
	}
	return t
}()

// The constants plainWord tests eight bytes at once with.
const (
	ones  = 0x0101010101010101
	highs = 0x8080808080808080
)

// plainWord reports whether every byte of x, eight of them, is plain: none is
// a control character, the quote, the backslash or past ASCII. It is what
// eight lookups in plain would tell, a word at a time.
func plainWord(x uint64) bool {
	// A byte's high bit is set below where a byte of x is under 0x20, or is
	// the quote or the backslash, unless x's own is; and x's own are set
	// past ASCII.
	below := x - 0x20*ones
	quote := (x ^ '"'*ones) - ones
	slash := (x ^ '\\'*ones) - ones
	return ((below|quote|slash)&^x|x)&highs == 0
}

// plainLen returns how many of s's first bytes are plain, eight at a time
// while it can.
func plainLen(s string) int {
	i := 0
	for ; i+8 <= len(s); i += 8 {
		w := s[i : i+8]
		x := uint64(w[0]) | uint64(w[1])<<8 | uint64(w[2])<<16 | uint64(w[3])<<24 |
			uint64(w[4])<<32 | uint64(w[5])<<40 | uint64(w[6])<<48 | uint64(w[7])<<56
		if !plainWord(x) {
			break
		}
	}
	for i < len(s) && plain[s[i]] {
		i++
	}
	return i
}

// appendString appends s as a JSON string. Bytes that are not valid UTF-8
// become U+FFFD, so that every line is valid JSON whatever a client sent.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for {
		n := plainLen(s)
		b = append(b, s[:n]...)
		s = s[n:]
		if len(s) == 0 {
			return append(b, '"')
		}
		if c := s[0]; c < utf8.RuneSelf {
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
			}
			s = s[1:]
			continue
		}
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			b = append(b, `�`...)
		} else {
			b = append(b, s[:size]...)
		}
		s = s[size:]
	}
}

// Package observe is fit's view of the traffic it serves: access logs for
// now, metrics (I34) and tracing (I65) later.
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
package observe

import (
	"context"
	"fmt"
	"io"
	stdhttp "net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
)

// The defaults config.AccessLog's fields stand for when left out.
const (
	DefaultFlushInterval = time.Second
	DefaultBufferBytes   = 4 << 20
	// flushBytes is how full the buffer gets before it is written without
	// waiting for the interval.
	flushBytes = 64 << 10
)

// AccessLog writes access log lines, buffered. It is safe for concurrent
// use.
type AccessLog struct {
	out      io.Writer
	file     *os.File // the file out is, when the log owns it
	interval time.Duration
	maxBytes int

	mu      sync.Mutex
	buf     []byte
	spare   []byte
	closed  bool
	dropped atomic.Int64

	kick chan struct{} // asks the writer to flush now
	stop chan struct{}
	done chan struct{}
	// warn is where dropped lines are reported.
	warn io.Writer
}

// NewAccessLog opens the log c describes and starts its writer.
func NewAccessLog(c *config.AccessLog) (*AccessLog, error) {
	l := &AccessLog{interval: c.FlushInterval.Std(), maxBytes: c.BufferBytes, warn: os.Stderr}
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
	l := &AccessLog{out: w, interval: interval, maxBytes: maxBytes, warn: os.Stderr}
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
	l.buf = make([]byte, 0, min(flushBytes*2, l.maxBytes))
	l.kick = make(chan struct{}, 1)
	l.stop = make(chan struct{})
	l.done = make(chan struct{})
	go l.run()
}

// run writes the buffer out every interval, or sooner when asked.
func (l *AccessLog) run() {
	defer close(l.done)
	t := time.NewTicker(l.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-l.kick:
		case <-l.stop:
			l.flush()
			return
		}
		l.flush()
	}
}

// flush writes what the buffer holds. Only the writer goroutine calls it, so
// the spare buffer is its own while the lock is not held.
func (l *AccessLog) flush() {
	l.mu.Lock()
	data := l.buf
	l.buf, l.spare = l.spare[:0], nil
	l.mu.Unlock()
	if len(data) > 0 {
		_, _ = l.out.Write(data)
	}
	if n := l.dropped.Swap(0); n > 0 {
		fmt.Fprintf(l.warn, "fit: access log: %d lines dropped, the output was too slow\n", n)
	}
	l.mu.Lock()
	if cap(data) <= l.maxBytes {
		l.spare = data[:0]
	}
	l.mu.Unlock()
}

// Dropped returns how many lines have been dropped since it was last
// reported.
func (l *AccessLog) Dropped() int64 { return l.dropped.Load() }

// Close writes out what is buffered, stops the writer and closes the file
// the log opened. Lines logged after Close are dropped.
func (l *AccessLog) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
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
}

type entryKey struct{}

// Attach makes e the entry of r, for EntryOf to find. It replaces r's
// context in place, so that r stays the same request.
func Attach(r *stdhttp.Request, e *Entry) {
	*r = *r.WithContext(context.WithValue(r.Context(), entryKey{}, e))
}

// EntryOf returns r's entry, or nil when the request is not logged.
func EntryOf(r *stdhttp.Request) *Entry {
	e, _ := r.Context().Value(entryKey{}).(*Entry)
	return e
}

// Log writes the line for r, which e describes, answered with status and a
// body of size bytes. It never waits for the output: a line that does not
// fit in the buffer is dropped.
func (l *AccessLog) Log(e *Entry, r *stdhttp.Request, status int, size int64) {
	end := time.Now()
	var stack [512]byte
	line := appendLine(stack[:0], e, r, status, size, end)

	l.mu.Lock()
	if l.closed || len(l.buf)+len(line) > l.maxBytes {
		l.mu.Unlock()
		l.dropped.Add(1)
		return
	}
	l.buf = append(l.buf, line...)
	full := len(l.buf) >= flushBytes
	l.mu.Unlock()
	if full {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
}

// appendLine appends the JSON line for a request to b.
func appendLine(b []byte, e *Entry, r *stdhttp.Request, status int, size int64, end time.Time) []byte {
	b = append(b, `{"time":"`...)
	b = end.UTC().AppendFormat(b, "2006-01-02T15:04:05.000Z07:00")
	b = append(b, `","listener":`...)
	b = appendString(b, e.Listener)
	if e.Route != "" {
		b = append(b, `,"route":`...)
		b = appendString(b, e.Route)
	}
	b = append(b, `,"client":`...)
	b = appendString(b, clientIP(r.RemoteAddr))
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
	b = strconv.AppendFloat(b, end.Sub(e.Start).Seconds(), 'f', 6, 64)
	if e.Upstream != "" {
		b = append(b, `,"upstream":`...)
		b = appendString(b, e.Upstream)
		b = append(b, `,"upstream_duration":`...)
		b = strconv.AppendFloat(b, e.UpstreamDuration.Seconds(), 'f', 6, 64)
	}
	b = append(b, `,"user_agent":`...)
	b = appendString(b, r.Header.Get("User-Agent"))
	b = append(b, `,"referer":`...)
	b = appendString(b, r.Header.Get("Referer"))
	return append(b, "}\n"...)
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

const hex = "0123456789abcdef"

// appendString appends s as a JSON string. Bytes that are not valid UTF-8
// become U+FFFD, so that every line is valid JSON whatever a client sent.
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
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
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i++
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}

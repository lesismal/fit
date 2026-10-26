package observe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// syncBuffer is a bytes.Buffer safe for the writer goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLine(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x?q=1&s=%22", nil)
	r.RemoteAddr = "[2001:db8::1]:5555"
	r.Host = "example.com"
	r.Header.Set("User-Agent", "agent \"quoted\"\n\x01\xff")
	r.Header.Set("Referer", "https://ref.example/")
	start := time.Now().Add(-1500 * time.Millisecond)
	e := &Entry{Start: start, Listener: "web", Route: "api", Upstream: "10.0.0.1:8080", UpstreamDuration: 1200 * time.Millisecond, BytesIn: 4, User: "alice"}
	line := appendLine(nil, e, r, 201, 512, start.Add(1500*time.Millisecond))
	if !bytes.HasSuffix(line, []byte("}\n")) || bytes.Count(line, []byte("\n")) != 1 {
		t.Fatalf("not one line: %q", line)
	}
	var got map[string]any
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	want := map[string]any{
		"listener": "web", "route": "api", "client": "2001:db8::1", "user": "alice", "proto": "HTTP/1.1", "method": "POST",
		"host": "example.com", "uri": "/api/x?q=1&s=%22", "status": 201.0, "bytes_in": 4.0, "bytes_out": 512.0,
		"duration": 1.5, "upstream": "10.0.0.1:8080", "upstream_duration": 1.2,
		"user_agent": "agent \"quoted\"\n\x01�", "referer": "https://ref.example/",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %#v, want %#v", k, got[k], v)
		}
	}
	if ts, _ := got["time"].(string); !strings.HasSuffix(ts, "Z") || len(ts) != len("2006-01-02T15:04:05.000Z") {
		t.Errorf("time = %q", got["time"])
	}
	if len(got) != len(want)+1 {
		t.Errorf("fields: %v", got)
	}

	// A request no route took, nobody authenticated and nothing proxied
	// leaves those fields out.
	r = httptest.NewRequest("GET", "/", nil)
	got = nil
	if err := json.Unmarshal(appendLine(nil, &Entry{Start: time.Now(), Listener: "web"}, r, 404, 19, time.Now()), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"route", "user", "upstream", "upstream_duration"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s is there: %v", k, got)
		}
	}
	if got["client"] != "192.0.2.1" {
		t.Errorf("client = %v", got["client"])
	}
}

func TestAppendString(t *testing.T) {
	for _, s := range []string{"", "plain", `"\`, "tab\tcr\rnl\n", "\x00\x1f\x7f", "héllo 世界", "bad \xc3\x28 utf8", "\xff"} {
		var back string
		out := appendString(nil, s)
		if err := json.Unmarshal(out, &back); err != nil {
			t.Errorf("%q: %s: %v", s, out, err)
			continue
		}
		if want := strings.ToValidUTF8(s, "�"); back != want {
			t.Errorf("%q came back as %q", s, back)
		}
	}
}

func TestClientIP(t *testing.T) {
	for in, want := range map[string]string{"192.0.2.1:80": "192.0.2.1", "[::1]:80": "::1", "[::1]": "[::1]", "host": "host", "": ""} {
		if got := clientIP(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func logN(l *AccessLog, n int) {
	r := httptest.NewRequest("GET", "/x", nil)
	for range n {
		l.Log(&Entry{Start: time.Now(), Listener: "web"}, r, 200, 1)
	}
}

func TestFlushInterval(t *testing.T) {
	var out syncBuffer
	l := NewAccessLogTo(&out, 50*time.Millisecond, 0)
	defer l.Close()
	logN(l, 3)
	if out.String() != "" {
		t.Fatal("lines were written before the interval")
	}
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(out.String(), "\n") != 3 {
		if time.Now().After(deadline) {
			t.Fatalf("after the interval: %q", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFlushWhenFull(t *testing.T) {
	var out syncBuffer
	l := NewAccessLogTo(&out, time.Hour, 0)
	defer l.Close()
	line := appendLine(nil, &Entry{Start: time.Now(), Listener: "web"}, httptest.NewRequest("GET", "/x", nil), 200, 1, time.Now())
	n := flushBytes/len(line) + 1
	logN(l, n)
	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(out.String(), "\n") < n {
		if time.Now().After(deadline) {
			t.Fatalf("a full buffer was not written: %d of %d lines", strings.Count(out.String(), "\n"), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// blockingWriter blocks every Write until released.
type blockingWriter struct {
	entered chan struct{}
	release chan struct{}
	out     syncBuffer
}

func (w *blockingWriter) Write(p []byte) (int, error) {
	select {
	case w.entered <- struct{}{}:
	default:
	}
	<-w.release
	return w.out.Write(p)
}

func TestSlowOutputDoesNotBlock(t *testing.T) {
	w := &blockingWriter{entered: make(chan struct{}, 1), release: make(chan struct{})}
	l := NewAccessLogTo(w, 10*time.Millisecond, 16<<10)
	var warn syncBuffer
	l.log = logTo(&warn)
	logN(l, 1)
	<-w.entered // the writer is stuck in Write
	start := time.Now()
	logN(l, 10000)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("10000 lines took %v with the output blocked", elapsed)
	}
	dropped := l.Dropped()
	if dropped == 0 {
		t.Fatal("a full buffer dropped nothing")
	}
	close(w.release)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	written := int64(strings.Count(w.out.String(), "\n"))
	if written+dropped != 10001 {
		t.Fatalf("%d written and %d dropped of 10001", written, dropped)
	}
	if !strings.Contains(warn.String(), "lines dropped") {
		t.Fatalf("the drops were not reported: %q", warn.String())
	}
}

func TestCloseFlushes(t *testing.T) {
	var out syncBuffer
	l := NewAccessLogTo(&out, time.Hour, 0)
	logN(l, 2)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("Close left lines unwritten: %q", out.String())
	}
	logN(l, 1)
	if strings.Count(out.String(), "\n") != 2 || l.Dropped() != 1 {
		t.Fatal("a line logged after Close was not dropped")
	}
	if err := l.Close(); err != nil {
		t.Fatal("a second Close:", err)
	}
}

func TestFileOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	if err := os.WriteFile(path, []byte("earlier\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := NewAccessLog(&config.AccessLog{Output: path})
	if err != nil {
		t.Fatal(err)
	}
	logN(l, 2)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 3 || lines[0] != "earlier" || !strings.HasPrefix(lines[1], `{"time":`) {
		t.Fatalf("file holds %q", data)
	}
	if _, err := NewAccessLog(&config.AccessLog{Output: filepath.Join(t.TempDir(), "none", "access.log")}); err == nil {
		t.Fatal("a file in a missing directory opened")
	}
}

func BenchmarkLog(b *testing.B)     { benchmarkLog(b, config.FormatJSON) }
func BenchmarkLogText(b *testing.B) { benchmarkLog(b, config.FormatText) }
func BenchmarkLogLine(b *testing.B) { benchmarkLog(b, config.FormatLine) }

func benchmarkLog(b *testing.B, format string) {
	l := NewAccessLogFormatTo(discard{}, format, time.Second, 64<<20)
	// Lines come faster here than a writer that shares the CPUs with the
	// loggers can take them: what is dropped is counted, not reported.
	var dropped atomic.Int64
	l.log = logTo(countDrops{&dropped})
	defer l.Close()
	r := httptest.NewRequest("GET", "/api/items?page=2", nil)
	r.Header.Set("User-Agent", "Mozilla/5.0 (benchmark)")
	e := &Entry{Start: time.Now(), Listener: "web", Route: "api", Upstream: "10.0.0.1:8080"}
	var clients atomic.Int32
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		// Each goroutine a client of its own, as the connections of a
		// server's workers are.
		r := r.Clone(context.Background())
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:4321", clients.Add(1))
		for pb.Next() {
			l.Log(e, r, 200, 512)
		}
	})
	b.ReportMetric(float64(l.Dropped()+dropped.Load())/float64(b.N), "dropped/op")
}

// countDrops adds up the lines a log's warnings say it dropped.
type countDrops struct{ n *atomic.Int64 }

func (c countDrops) Write(p []byte) (int, error) {
	var n int64
	if _, err := fmt.Sscanf(string(p), "fit: access log: %d lines", &n); err == nil {
		c.n.Add(n)
	}
	return len(p), nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func TestShards(t *testing.T) {
	defer goruntime.GOMAXPROCS(goruntime.GOMAXPROCS(8))
	var out syncBuffer
	l := NewAccessLogTo(&out, 5*time.Millisecond, 0)
	var dropped atomic.Int64
	l.log = logTo(countDrops{&dropped})
	if len(l.shards) != 32 {
		t.Fatalf("%d shards for GOMAXPROCS 8", len(l.shards))
	}
	// Clients logging at once, as fast as they can: every line is written
	// or counted as dropped, and each client's are written in the order
	// logged.
	const clients, lines = 16, 2000
	var wg sync.WaitGroup
	for c := range clients {
		wg.Go(func() {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = fmt.Sprintf("192.0.2.%d:%d", c, 1000+c)
			for i := range lines {
				r.RequestURI = "/" + strconv.Itoa(i)
				l.Log(&Entry{Start: time.Now(), Listener: "web"}, r, 200, 1)
			}
		})
	}
	wg.Wait()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	last := map[string]int{}
	n := 0
	for line := range strings.Lines(out.String()) {
		var got struct{ Client, URI string }
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		i, _ := strconv.Atoi(strings.TrimPrefix(got.URI, "/"))
		if prev, ok := last[got.Client]; ok && i <= prev {
			t.Fatalf("client %s: line %d after line %d", got.Client, i, prev)
		}
		last[got.Client] = i
		n++
	}
	if int64(n)+dropped.Load() != clients*lines {
		t.Fatalf("%d lines written and %d dropped of %d", n, dropped.Load(), clients*lines)
	}
	if l.queued.Load() != 0 {
		t.Fatalf("%d bytes still counted as queued", l.queued.Load())
	}
}

func TestTrack(t *testing.T) {
	c := fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "/", nil), nil)
	if EntryOf(c) != nil {
		t.Fatal("an entry before Track")
	}
	before := c.Request
	e := Track(c)
	if c.Request != before || EntryOf(c) != e {
		t.Fatal("the entry is not the request's")
	}
}

func TestAppendTime(t *testing.T) {
	for _, tm := range []time.Time{
		time.Date(2026, 10, 19, 7, 39, 56, 123456789, time.UTC),
		time.Date(1999, 1, 2, 3, 4, 5, 6e6, time.FixedZone("x", 8*3600)),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1, 1, 1, 0, 0, 0, 999999999, time.UTC),
	} {
		if got, want := string(appendTime(nil, tm)), tm.UTC().Format("2006-01-02T15:04:05.000Z07:00"); got != want {
			t.Errorf("%v: %s, want %s", tm, got, want)
		}
	}
}

func TestPlainLen(t *testing.T) {
	// Every byte at every position of a run of plain ones, word-sized and
	// not.
	for _, n := range []int{8, 16, 21} {
		for pos := range n {
			for c := range 256 {
				block := []byte(strings.Repeat("a", n))
				block[pos] = byte(c)
				want := n
				if !plain[c] {
					want = pos
				}
				if got := plainLen(string(block)); got != want {
					t.Errorf("byte %#x at %d of %d: plainLen %d, want %d", c, pos, n, got, want)
				}
			}
		}
	}
	// Bytes either side of a borrow.
	for _, s := range []string{"\x00\x01\x20\x21\x22\x23\x5b\x5c", "\x21\x21\x21\x21\x21\x21\x21\x1f", "\x7f\x7f\x7f\x7f\x7f\x7f\x7f\x80"} {
		want := 0
		for want < len(s) && plain[s[want]] {
			want++
		}
		if got := plainLen(s); got != want {
			t.Errorf("%q: plainLen %d, want %d", s, got, want)
		}
	}
}

func TestAppendStringLong(t *testing.T) {
	// A byte that must be escaped at every offset of a long string, so that
	// the word-at-a-time scan finds it wherever it is.
	base := strings.Repeat("Mozilla/5.0 (X11; Linux x86_64) ", 3)
	for i := range len(base) {
		for _, c := range []string{`"`, `\`, "\n", "\x01", "é", "\xff"} {
			s := base[:i] + c + base[i:]
			var back string
			if err := json.Unmarshal(appendString(nil, s), &back); err != nil {
				t.Fatalf("%q: %v", s, err)
			}
			if want := strings.ToValidUTF8(s, "�"); back != want {
				t.Fatalf("%q came back as %q", s, back)
			}
		}
	}
}

func TestAppendSeconds(t *testing.T) {
	for _, d := range []time.Duration{0, 1, 499, 501, 999, 1000, 412345, 1500 * time.Millisecond,
		time.Second - 1, 59*time.Minute + 999999499, 1<<62 - 1, -1, -1500 * time.Microsecond} {
		got := string(appendSeconds(nil, d))
		want := strconv.FormatFloat(d.Seconds(), 'f', 6, 64)
		if want == "-0.000000" {
			want = "0.000000"
		}
		if d >= 1<<53 {
			// Past what a float64 holds to the nanosecond: the float is off,
			// not the integer.
			want = strconv.FormatInt(int64((d+500)/1000)/1e6, 10) + "." + fmt.Sprintf("%06d", int64((d+500)/1000)%1e6)
		}
		if got != want {
			t.Errorf("%d: %s, want %s", d, got, want)
		}
	}
}

func TestLogTime(t *testing.T) {
	l := &AccessLog{}
	base := time.Date(2026, 10, 19, 7, 39, 56, 0, time.UTC)
	for _, tm := range []time.Time{
		base, base.Add(123 * time.Millisecond), base.Add(999999999), base.Add(time.Second),
		base.Add(time.Second + 5*time.Millisecond).In(time.FixedZone("x", 8*3600)),
		time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), base,
	} {
		if got, want := string(l.appendTime(nil, tm)), string(appendTime(nil, tm)); got != want {
			t.Errorf("%v: %s, want %s", tm, got, want)
		}
	}
}

func TestLogLine(t *testing.T) {
	// What Log writes is what appendLine formats.
	var out syncBuffer
	l := NewAccessLogTo(&out, time.Hour, 0)
	r := httptest.NewRequest("GET", "/x?y=1", nil)
	r.Header.Set("User-Agent", "agent")
	e := &Entry{Start: time.Now(), Listener: "web", Route: "r"}
	l.Log(e, r, 200, 7)
	l.Close()
	line := out.String()
	var got map[string]any
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	ts, _ := got["time"].(string)
	want := string(appendLine(nil, e, r, 200, 7, time.Time{}))
	_, wantRest, _ := strings.Cut(want, `","listener"`)
	_, gotRest, _ := strings.Cut(line, `","listener"`)
	// The durations differ; the rest of the line does not.
	cut := func(s string) string {
		i := strings.Index(s, `,"duration"`)
		j := strings.Index(s, `,"user_agent"`)
		return s[:i] + s[j:]
	}
	if len(ts) != len("2006-01-02T15:04:05.000Z") || cut(gotRest) != cut(wantRest) {
		t.Errorf("Log wrote %q, appendLine %q", line, want)
	}
}

// logTo returns a logger writing each message to w as fit's text log does.
func logTo(w io.Writer) *logs.Logger {
	return logs.Func(func(format string, args ...any) { fmt.Fprintf(w, "fit: "+format+"\n", args...) })
}

// parseLogfmt reads the key=value pairs of a line of the text or line
// format, from its first, quoted values unquoted.
func parseLogfmt(t *testing.T, s string) map[string]string {
	t.Helper()
	m := map[string]string{}
	for s != "" {
		key, rest, ok := strings.Cut(s, "=")
		if !ok {
			t.Fatalf("no = in %q", s)
		}
		var value string
		if strings.HasPrefix(rest, `"`) {
			q, err := strconv.QuotedPrefix(rest)
			if err != nil {
				t.Fatalf("%q: %v", rest, err)
			}
			value, _ = strconv.Unquote(q)
			rest = rest[len(q):]
		} else {
			value, rest, _ = strings.Cut(rest, " ")
			rest = " " + rest
		}
		m[key] = value
		s = strings.TrimPrefix(rest, " ")
	}
	return m
}

// TestTextAndLineFormats checks the text and line formats: the fields of
// the JSON line, as key=value, a value with a space, a quote, a control
// character or a byte that is not UTF-8 quoted, and the level the status's.
func TestTextAndLineFormats(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/x?q=1&s=%22", nil)
	r.RemoteAddr = "[2001:db8::1]:5555"
	r.Host = "example.com"
	r.Header.Set("User-Agent", "agent \"quoted\"\n\x01\xff")
	e := &Entry{Start: time.Now(), Listener: "web", Route: "api", Upstream: "10.0.0.1:8080", UpstreamDuration: time.Millisecond, BytesIn: 4, User: "a=b"}
	want := map[string]string{
		"listener": "web", "route": "api", "client": "2001:db8::1", "user": "a=b", "proto": "HTTP/1.1", "method": "POST",
		"host": "example.com", "uri": "/api/x?q=1&s=%22", "status": "503", "bytes_in": "4", "bytes_out": "512",
		"upstream": "10.0.0.1:8080", "upstream_duration": "0.001000", "user_agent": "agent \"quoted\"\n\x01\xff", "referer": "",
	}
	for _, format := range []string{config.FormatText, config.FormatLine} {
		var out syncBuffer
		l := NewAccessLogFormatTo(&out, format, time.Hour, 0)
		l.Log(e, r, 503, 512)
		l.Close()
		line := out.String()
		if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 {
			t.Fatalf("%s: not one line: %q", format, line)
		}
		line = strings.TrimSuffix(line, "\n")
		if format == config.FormatLine {
			head := "2006-01-02 15:04:05.000 ERROR access "
			if len(line) < len(head) || !strings.HasPrefix(line[len("2006-01-02 15:04:05.000"):], " ERROR access ") {
				t.Fatalf("line: %q", line)
			}
			if _, err := time.ParseInLocation("2006-01-02 15:04:05.000", line[:len("2006-01-02 15:04:05.000")], time.Local); err != nil {
				t.Errorf("line's time: %v", err)
			}
			line = line[len(head):]
		}
		got := parseLogfmt(t, line)
		if format == config.FormatText {
			if got["level"] != "ERROR" || got["msg"] != "access" || !strings.HasSuffix(got["time"], "Z") {
				t.Errorf("text: %q", line)
			}
			delete(got, "time")
			delete(got, "level")
			delete(got, "msg")
		}
		if _, err := strconv.ParseFloat(got["duration"], 64); err != nil {
			t.Errorf("%s: duration %q", format, got["duration"])
		}
		delete(got, "duration")
		for k, v := range want {
			if got[k] != v {
				t.Errorf("%s: %s = %q, want %q", format, k, got[k], v)
			}
		}
		if len(got) != len(want) {
			t.Errorf("%s: fields %v", format, got)
		}
	}
}

func TestStatusLevel(t *testing.T) {
	for status, want := range map[int]string{200: "INFO", 399: "INFO", 404: "WARN", 500: "ERROR"} {
		if got := statusLevel(status); got != want {
			t.Errorf("statusLevel(%d) = %s, want %s", status, got, want)
		}
	}
	for _, s := range []string{"", "a b", `a"b`, "a=b", "\x7f", "é"} {
		if got := string(appendLogfmt(nil, s)); got == s || got[0] != '"' {
			t.Errorf("appendLogfmt(%q) = %s, not quoted", s, got)
		}
	}
	if got := string(appendLogfmt(nil, "/a?b=1")); got != `"/a?b=1"` {
		t.Errorf("a value with = is %s", got)
	}
	if got := string(appendLogfmt(nil, "10.0.0.1:8080")); got != "10.0.0.1:8080" {
		t.Errorf("a bare value is %s", got)
	}
}

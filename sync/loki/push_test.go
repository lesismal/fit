package syncloki

import (
	"bytes"
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/s2"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/pb"
)

// nestedProto is the PushRequest as pb's nested messages make it, each
// body moved up once its length is known: what appendProto must equal.
func nestedProto(b []byte, sts []stream, es []entry) []byte {
	for _, st := range sts {
		b = pb.AppendMessage(b, 1, func(b []byte) []byte {
			b = pb.AppendString(b, 1, st.labels)
			for _, i := range st.idx {
				e := &es[i]
				b = pb.AppendMessage(b, 2, func(b []byte) []byte {
					b = pb.AppendMessage(b, 1, func(b []byte) []byte {
						b = pb.AppendInt(b, 1, e.ts.Unix())
						return pb.AppendInt(b, 2, int64(e.ts.Nanosecond()))
					})
					b = pb.AppendString(b, 2, string(e.line))
					for k := 0; k+1 < len(e.meta); k += 2 {
						b = pb.AppendMessage(b, 3, func(b []byte) []byte {
							b = pb.AppendString(b, 1, e.meta[k])
							return pb.AppendString(b, 2, e.meta[k+1])
						})
					}
					return b
				})
			}
			return b
		})
	}
	return b
}

func TestProtoAsNested(t *testing.T) {
	rnd := rand.New(rand.NewPCG(1, 2))
	times := []time.Time{{}, time.Unix(0, 5), time.Unix(-3, 7), time.Unix(1_700_000_000, 0), time.Unix(1_700_000_000, 999_999_999)}
	for round := range 200 {
		n := rnd.IntN(40)
		es := make([]entry, n)
		for i := range es {
			e := &es[i]
			e.stream = `{s="` + strconv.Itoa(rnd.IntN(3)) + `"}`
			e.ts = times[rnd.IntN(len(times))]
			e.line = bytes.Repeat([]byte{'x'}, []int{0, 1, 127, 128, 300, 20000}[rnd.IntN(6)])
			for range rnd.IntN(3) {
				e.meta = append(e.meta, []string{"", "k", strings.Repeat("n", 130)}[rnd.IntN(3)], []string{"", "v", strings.Repeat("v", 200)}[rnd.IntN(3)])
			}
		}
		if round%7 == 0 && n > 0 {
			es[0].stream = strings.Repeat("l", 200) // a stream of long labels
		}
		sc := new(scratch)
		sts := sc.streams(es)
		got, want := appendProto(nil, sts, es), nestedProto(nil, sts, es)
		if !bytes.Equal(got, want) {
			t.Fatalf("round %d: %d bytes, want %d", round, len(got), len(want))
		}
	}
}

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{"plain": "plain", `a"b`: `a\"b`, `a\b`: `a\\b`, "a\nb": `a\nb`, "": ""} {
		if got := escape(in); got != want {
			t.Errorf("escape(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStreamsKeptAcrossRecords writes a batch whose labels change and
// come back: each record's stream is its own, the one before's reused only
// when its values are those.
func TestStreamsKeptAcrossRecords(t *testing.T) {
	f := newFakeLoki(t)
	s := fakeSink(t, f, map[string]any{"labels": map[string]any{"job": "app", "level": "{level}", "q": "{line}"}})
	batch := []*fitsync.Record{line("/a", 2, `x"y`), line("/a", 2, `x"y`), line("/a", 3, `x"y`), line("/a", 4, "z"), line("/a", 4, "")}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range f.got() {
		got = append(got, e.stream)
	}
	want := []string{`{job="app", level="info", q="x\"y"}`, `{job="app", level="info", q="x\"y"}`, `{job="app", level="warn", q="x\"y"}`,
		`{job="app", level="info", q="z"}`, `{job="app", level="info"}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestCanceledDropsBuffers: a push its context ends during gives its
// buffers up, which the client may still be reading.
func TestCanceledDropsBuffers(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	s, err := loadSink(t, map[string]any{"urls": []string{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	sc := new(scratch)
	es := []entry{{stream: `{a="b"}`, pairs: []string{"a", "b"}, line: []byte("x"), ts: time.Unix(1, 0)}}
	if err := s.send(ctx, sc, "", sc.streams(es), es); err == nil {
		t.Fatal("sent")
	}
	if sc.body != nil || sc.packed != nil {
		t.Fatal("kept")
	}
}

// TestReleaseBounds: a scratch keeps no buffer larger than maxKept.
func TestReleaseBounds(t *testing.T) {
	sc := &scratch{arena: make([]byte, 0, maxKept+1), body: make([]byte, 0, maxKept+1), sts: make([]stream, 1, 4)}
	sc.sts[0].labels = "x"
	release(sc)
	if sc.arena != nil || sc.body != nil || sc.sts[:1][0].labels != "" {
		t.Fatal("kept")
	}
}

// benchLines are n records as the file source makes them, of 128 bytes.
func benchLines(n int) []*fitsync.Record {
	text := strings.Repeat("a", 100)
	recs := make([]*fitsync.Record, n)
	for i := range recs {
		recs[i] = &fitsync.Record{Source: "file", Namespace: "/data/in/0.log", Op: fitsync.OpMessage,
			After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(text + strconv.Itoa(1_000_000_000_000_000+i))})}
	}
	return recs
}

// BenchmarkEncode makes the push of a batch of lines of one stream: the
// entries, the streams, the protobuf and snappy, without sending it.
func BenchmarkEncode(b *testing.B) {
	for _, n := range []int{500, 7000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s, err := loadSink(b, map[string]any{"urls": []string{"http://127.0.0.1:1"}, "labels": map[string]any{"job": "bench"}})
			if err != nil {
				b.Fatal(err)
			}
			batch := benchLines(n)
			b.ReportAllocs()
			b.SetBytes(int64(n * 128))
			for b.Loop() {
				sc := scratches.Get().(*scratch)
				now := time.Now()
				sc.arena = sc.arena[:0]
				es := sc.entries[:0]
				for i, r := range batch {
					var prev *entry
					if i > 0 {
						prev = &es[i-1]
					}
					es = append(es, s.build(r, now.Add(time.Duration(i)), sc, prev))
				}
				sc.entries = es
				sc.body = appendProto(sc.body[:0], sc.streams(es), es)
				sc.packed = s2.EncodeSnappy(sc.packed[:0], sc.body)
				release(sc)
			}
		})
	}
}

// BenchmarkWrite writes batches of lines to a Loki that reads and drops
// them.
func BenchmarkWrite(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	s, err := loadSink(b, map[string]any{"urls": []string{srv.URL}, "labels": map[string]any{"job": "bench"}})
	if err != nil {
		b.Fatal(err)
	}
	defer s.Cleanup()
	batch := benchLines(7000)
	b.ReportAllocs()
	b.SetBytes(int64(len(batch) * 128))
	for b.Loop() {
		if err := s.Write(context.Background(), batch); err != nil {
			b.Fatal(err)
		}
	}
}

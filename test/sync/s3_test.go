package synctest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	syncs3 "github.com/lesismal/fit/sync/s3"
)

// s3Server is an S3 compatible the S3 tests use, from FIT_SYNC_<NAME>_URL,
// FIT_SYNC_<NAME>_ACCESS_KEY and FIT_SYNC_<NAME>_SECRET_KEY: MINIO for
// MinIO, SEAWEED for SeaweedFS's S3 gateway.
type s3Server struct {
	name, url, access, secret, bucket string
	signer                            *syncs3.Signer
}

// s3Servers returns the servers set, a test's bucket made afresh on each.
func s3Servers(t *testing.T) []*s3Server {
	t.Helper()
	var out []*s3Server
	for _, name := range []string{"MINIO", "SEAWEED"} {
		u := os.Getenv("FIT_SYNC_" + name + "_URL")
		if u == "" {
			if os.Getenv("FIT_SYNC_INTEROP") == "required" {
				t.Fatalf("FIT_SYNC_%s_URL is not set", name)
			}
			continue
		}
		s := &s3Server{name: strings.ToLower(name), url: u, access: os.Getenv("FIT_SYNC_" + name + "_ACCESS_KEY"),
			secret: os.Getenv("FIT_SYNC_" + name + "_SECRET_KEY")}
		s.signer = &syncs3.Signer{AccessKeyID: s.access, SecretAccessKey: s.secret, Region: "us-east-1", Service: "s3"}
		s.bucket = fmt.Sprintf("fit-%s-%d", strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")), time.Now().UnixNano()%1e9)
		s.do(t, http.MethodPut, "", nil, nil)
		out = append(out, s)
	}
	if len(out) == 0 {
		t.Skip("no S3: FIT_SYNC_MINIO_URL and FIT_SYNC_SEAWEED_URL are not set (test/sync/run.sh sets them)")
	}
	return out
}

// do sends a signed request through net/http, not fib's client, and
// returns the answer's body.
func (s *s3Server) do(t *testing.T, method, key string, q url.Values, body []byte) []byte {
	t.Helper()
	path := "/" + s.bucket
	if key != "" {
		path += "/" + key
	}
	u, _ := url.Parse(s.url)
	u.Path, u.RawPath, u.RawQuery = path, syncs3.Escape(path, false), syncs3.EncodeQuery(q)
	req, _ := http.NewRequest(method, u.String(), bytes.NewReader(body))
	s.signer.Sign(req, syncs3.PayloadHash(body), time.Now())
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	return b
}

// objects returns the key and content of every object under prefix.
func (s *s3Server) objects(t *testing.T, prefix string) map[string]string {
	t.Helper()
	var out struct {
		Contents []struct{ Key string }
	}
	if err := xml.Unmarshal(s.do(t, http.MethodGet, "", url.Values{"list-type": {"2"}, "max-keys": {"1000"}, "prefix": {prefix}}, nil), &out); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, c := range out.Contents {
		m[c.Key] = string(s.do(t, http.MethodGet, c.Key, nil, nil))
	}
	return m
}

func (s *s3Server) params(extra map[string]any) map[string]any {
	p := map[string]any{"endpoint": s.url, "bucket": s.bucket, "access_key_id": s.access, "secret_access_key_env": "FIT_TEST_SYNC_S3_SECRET"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func (s *s3Server) load(t *testing.T, ns string, params map[string]any) any {
	t.Helper()
	t.Setenv("FIT_TEST_SYNC_S3_SECRET", s.secret)
	p := map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "s3", "params": s.params(params)}}}}
	if ns == fitsync.Sources {
		p["source"] = map[string]any{"module": "s3", "params": s.params(params)}
		p["sinks"] = []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}
	}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	ref := cfg.Sync.Pipelines[0].Source
	if ns == fitsync.Sinks {
		ref = cfg.Sync.Pipelines[0].Sinks[0].Sink
	}
	m, err := module.Load(ns, ref, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m
}

// s3Rec is the n-th record of a stream of orders, committed at n seconds,
// its position n.
func s3Rec(n int) *fitsync.Record {
	return &fitsync.Record{Namespace: "shop/orders", Op: fitsync.OpInsert,
		Key:        fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(n))}),
		After:      fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(int64(n))}, fitsync.Field{Name: "v", Value: fitsync.String(strings.Repeat("x", n%40))}),
		CommitTime: time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second),
		Position:   fitsync.Position(fmt.Sprintf(`{"n":%d}`, n))}
}

// TestS3SinkReplay writes a stream in batches, then the same batches again,
// as a pipeline does after a crash before its checkpoint: the same objects,
// with the same content; gzipped and not.
func TestS3SinkReplay(t *testing.T) {
	for _, srv := range s3Servers(t) {
		for _, gz := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gzip=%v", srv.name, gz), func(t *testing.T) {
				prefix := fmt.Sprintf("gz%v/", gz)
				sink := srv.load(t, fitsync.Sinks, map[string]any{"codec": map[string]any{"module": "json"}, "gzip": gz,
					"max_object_bytes": 3000, "key": prefix + "{namespace}/{yyyy}/{mm}/{dd}/{hh}/{timestamp}-{position}{ext}"}).(fitsync.Sink)
				var recs []*fitsync.Record
				for n := range 500 {
					recs = append(recs, s3Rec(n))
				}
				write := func() {
					for i := 0; i < len(recs); i += 100 {
						if err := sink.Write(context.Background(), recs[i:i+100]); err != nil {
							t.Fatal(err)
						}
					}
				}
				write()
				first := srv.objects(t, prefix)
				write()
				again := srv.objects(t, prefix)
				if len(first) < 10 || len(again) != len(first) {
					t.Fatalf("%d objects, then %d", len(first), len(again))
				}
				var keys []string
				for k, v := range first {
					if again[k] != v {
						t.Errorf("%s changed when written again", k)
					}
					if !strings.HasPrefix(k, prefix+"shop/orders/2026/11/08/10/20261108T10") || strings.HasSuffix(k, ".gz") != gz {
						t.Errorf("the key %s", k)
					}
					keys = append(keys, k)
				}
				// In key order, the objects hold the stream in order.
				sort.Strings(keys)
				var lines []string
				for _, k := range keys {
					body := []byte(first[k])
					if gz {
						zr, err := gzip.NewReader(bytes.NewReader(body))
						if err != nil {
							t.Fatal(err)
						}
						body, _ = io.ReadAll(zr)
					}
					lines = append(lines, strings.Split(strings.TrimSpace(string(body)), "\n")...)
				}
				if len(lines) != 500 || lines[0] != `{"id":0,"v":""}` || lines[499] != fmt.Sprintf(`{"id":499,"v":"%s"}`, strings.Repeat("x", 499%40)) {
					t.Errorf("%d lines: %s ... %s", len(lines), lines[0], lines[len(lines)-1])
				}
			})
		}
	}
}

// s3Collect runs src from from in mode once until it has emitted n records,
// all of them when n is 0.
func s3Collect(t *testing.T, src fitsync.Source, from fitsync.Position, n int) []*fitsync.Record {
	t.Helper()
	var got []*fitsync.Record
	stop := errors.New("enough")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	defer cancel()
	err := src.Run(ctx, &fitsync.Run{From: from, Once: true, Emit: func(r *fitsync.Record) error {
		got = append(got, r)
		if n > 0 && len(got) == n {
			return stop
		}
		return nil
	}})
	if err != nil && err != stop {
		t.Fatal(err)
	}
	return got
}

func s3Lines(recs []*fitsync.Record) []string {
	var out []string
	for _, r := range recs {
		v, _ := r.After.Get("line")
		out = append(out, fitsync.TextOf(v))
	}
	return out
}

// TestS3SourceResumes reads objects in key order, in ranges, and resumes
// inside a plain object and a gzipped one from a record's position.
func TestS3SourceResumes(t *testing.T) {
	for _, srv := range s3Servers(t) {
		t.Run(srv.name, func(t *testing.T) {
			var want []string
			put := func(key string, from, to int, gz bool) {
				var b bytes.Buffer
				for n := from; n < to; n++ {
					line := fmt.Sprintf("%s line %d %s", key, n, strings.Repeat("y", n%97))
					want = append(want, line)
					b.WriteString(line + "\n")
				}
				body := b.Bytes()
				if gz {
					var z bytes.Buffer
					zw := gzip.NewWriter(&z)
					zw.Write(body)
					zw.Close()
					body = z.Bytes()
				}
				srv.do(t, http.MethodPut, key, nil, body)
			}
			put("in/2026-11-08/a.log", 0, 3000, false)
			put("in/2026-11-08/b.log.gz", 0, 3000, true)
			put("in/2026-11-09/c.log", 0, 10, false)
			srv.do(t, http.MethodPut, "not-in/x.log", nil, []byte("x\n"))
			src := srv.load(t, fitsync.Sources, map[string]any{"prefix": "in/", "range_bytes": 65536}).(fitsync.Source)
			all := s3Collect(t, src, nil, 0)
			if got := s3Lines(all); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatalf("%d lines, want %d", len(got), len(want))
			}
			for _, at := range []int{1234, 4567, 5999} {
				rest := s3Collect(t, src, all[at].Position, 0)
				if got := s3Lines(rest); fmt.Sprint(got) != fmt.Sprint(want[at+1:]) {
					t.Errorf("resumed after %d: %d lines, want %d", at, len(got), len(want)-at-1)
				}
			}
			// Read a part, then resume from the last record read.
			part := s3Collect(t, src, nil, 4000)
			rest := s3Collect(t, src, part[len(part)-1].Position, 0)
			if got := append(s3Lines(part), s3Lines(rest)...); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("in two runs: %d lines", len(got))
			}
		})
	}
}

// TestS3RoundTrip writes records with the sink and reads them back with
// the source, as lines.
func TestS3RoundTrip(t *testing.T) {
	for _, srv := range s3Servers(t) {
		t.Run(srv.name, func(t *testing.T) {
			sink := srv.load(t, fitsync.Sinks, map[string]any{"gzip": true, "max_object_bytes": 10000}).(fitsync.Sink)
			var recs []*fitsync.Record
			var want []string
			for n := range 2000 {
				r := s3Rec(n)
				line := fmt.Sprintf("order %d", n)
				r.After = fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(line)})
				recs, want = append(recs, r), append(want, line)
			}
			for i := 0; i < len(recs); i += 250 {
				if err := sink.Write(context.Background(), recs[i:i+250]); err != nil {
					t.Fatal(err)
				}
			}
			src := srv.load(t, fitsync.Sources, map[string]any{"prefix": "shop/"}).(fitsync.Source)
			if got := s3Lines(s3Collect(t, src, nil, 0)); fmt.Sprint(got) != fmt.Sprint(want) {
				t.Errorf("%d lines read back, want %d", len(got), len(want))
			}
		})
	}
}

// TestS3LargeObject writes a batch of 4 MB as one object, a body fib's
// client sends as it is read rather than with the header, and reads it
// back in ranges.
func TestS3LargeObject(t *testing.T) {
	for _, srv := range s3Servers(t) {
		t.Run(srv.name, func(t *testing.T) {
			sink := srv.load(t, fitsync.Sinks, nil).(fitsync.Sink)
			var recs []*fitsync.Record
			for n := range 40000 {
				r := s3Rec(n)
				r.Namespace = "big"
				r.After = fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(fmt.Sprintf("%08d %s", n, strings.Repeat("z", 90)))})
				recs = append(recs, r)
			}
			if err := sink.Write(context.Background(), recs); err != nil {
				t.Fatal(err)
			}
			src := srv.load(t, fitsync.Sources, map[string]any{"prefix": "big/", "range_bytes": 1 << 20}).(fitsync.Source)
			got := s3Collect(t, src, nil, 0)
			if len(got) != len(recs) || fmt.Sprint(s3Lines(got[39999:])) != fmt.Sprint(s3Lines(recs[39999:])) {
				t.Errorf("%d lines read back", len(got))
			}
		})
	}
}

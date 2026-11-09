package synctest

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/file"
	_ "github.com/lesismal/fit/sync/loki"
	_ "github.com/lesismal/fit/sync/otlp"
)

// loadSink loads a sink module with params, as a pipeline's config does.
func loadSinkModule(t *testing.T, mod string, params map[string]any) fitsync.Sink {
	t.Helper()
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": mod, "params": params}}}})
	s, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Sink)
}

// lokiLines returns the lines of the streams the selector names, in time
// order, from an hour ago on.
func lokiLines(t *testing.T, base, selector string) []string {
	t.Helper()
	q := url.Values{"query": {selector}, "limit": {"5000"}, "direction": {"forward"},
		"start": {strconv.FormatInt(time.Now().Add(-24*time.Hour).UnixNano(), 10)}, "end": {strconv.FormatInt(time.Now().Add(time.Hour).UnixNano(), 10)}}
	resp, err := http.Get(base + "/loki/api/v1/query_range?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var r struct {
		Data struct {
			Result []struct {
				Values [][]string `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, st := range r.Data.Result {
		for _, v := range st.Values {
			out = append(out, v[1])
		}
	}
	return out
}

// waitLines waits for the selector's lines to be n or more, and returns
// them.
func waitLines(t *testing.T, base, selector string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		got := lokiLines(t, base, selector)
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestLokiPush pushes lines in both encodings, with structured metadata,
// and again: Loki keeps each once, the lines with their times.
func TestLokiPush(t *testing.T) {
	base := obsEnv(t, "FIT_SYNC_LOKI_URL")
	for _, enc := range []string{"protobuf", "json"} {
		t.Run(enc, func(t *testing.T) {
			job := fmt.Sprintf("fit-%s-%d", enc, time.Now().UnixNano())
			s := loadSinkModule(t, "loki", map[string]any{"urls": []string{base}, "encoding": enc,
				"labels": map[string]any{"job": job, "level": "{level}"}, "structured_metadata": map[string]any{"user": "{user}"}})
			var recs []*fitsync.Record
			now := time.Now().Add(-time.Minute)
			for i := range 300 {
				recs = append(recs, &fitsync.Record{Source: "test", Namespace: "app", Op: fitsync.OpMessage, CommitTime: now.Add(time.Duration(i) * time.Millisecond),
					Position: fitsync.Position(fmt.Sprintf(`{"n":%d}`, i+1)),
					After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(fmt.Sprintf("line %d \"quoted\" ü", i))},
						fitsync.Field{Name: "level", Value: fitsync.String([]string{"info", "warn", "error"}[i%3])},
						fitsync.Field{Name: "user", Value: fitsync.String("u" + strconv.Itoa(i%7))})})
			}
			writeAll(t, s, recs, 70)
			writeAll(t, s, recs[100:], 33) // a replay
			waitLines(t, base, `{job="`+job+`"}`, 300)
			time.Sleep(time.Second) // for a replay stored twice to show
			got := lokiLines(t, base, `{job="`+job+`"}`)
			if len(got) != 300 {
				t.Fatalf("%d lines, want 300", len(got))
			}
			if warn := lokiLines(t, base, `{job="`+job+`", level="warn"} | user="u1"`); len(warn) != 15 {
				t.Fatalf("%d warn lines of u1, want 15", len(warn))
			}
		})
	}
}

// writeLogFiles writes n numbered lines into files of dir, and returns
// them.
func writeLogFiles(t *testing.T, dir string, files, n int, tag string) map[string]bool {
	t.Helper()
	want := map[string]bool{}
	ws := make([]*bufio.Writer, files)
	for i := range ws {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("app-%d.log", i)))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		ws[i] = bufio.NewWriter(f)
		defer ws[i].Flush()
	}
	for i := range n {
		l := fmt.Sprintf("%s line %06d level=%s", tag, i, []string{"info", "warn"}[i%2])
		want[l] = true
		fmt.Fprintln(ws[i%files], l)
	}
	return want
}

// TestLokiFilesAcrossKills copies log files to Loki with the file source,
// killed at random points and started again: every line reaches Loki.
func TestLokiFilesAcrossKills(t *testing.T) {
	base := obsEnv(t, "FIT_SYNC_LOKI_URL")
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 11))
	dir := t.TempDir()
	job := fmt.Sprintf("fit-files-%d", time.Now().UnixNano())
	want := writeLogFiles(t, dir, 3, 3000, job)
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
		"source": map[string]any{"module": "file", "params": map[string]any{"paths": []string{filepath.Join(dir, "*.log")}}},
		"sinks": []any{map[string]any{"name": "loki", "batch": map[string]any{"max_records": 1 + rnd.IntN(200), "max_wait": "1ms"},
			"sink": map[string]any{"module": "loki", "params": map[string]any{"urls": []string{base},
				"encoding": []string{"protobuf", "json"}[rnd.IntN(2)], "labels": map[string]any{"job": job}}}}}})
	kills := runKilled(t, cfg, rnd, 8)
	// Lines written again at another time are Loki's twice, so the count
	// waited for is of the lines' distinct texts.
	var got []string
	seen := map[string]bool{}
	for deadline := time.Now().Add(time.Minute); len(seen) < len(want) && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		got = waitLines(t, base, `{job="`+job+`"}`, len(want))
		clear(seen)
		for _, l := range got {
			if !want[l] {
				t.Fatalf("a line not written: %q", l)
			}
			seen[l] = true
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("%d of %d lines in Loki", len(seen), len(want))
	}
	t.Logf("%d lines as %d across %d kills", len(want), len(got), kills)
}

// otlpRecords reads the collector's file of OTLP JSON requests and returns
// the bodies of the log records whose resource's service.name is service,
// with their attributes.
func otlpRecords(t *testing.T, file, service string) map[string]map[string]string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out := map[string]map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var req struct {
			ResourceLogs []struct {
				Resource struct {
					Attributes []struct {
						Key   string `json:"key"`
						Value struct {
							S string `json:"stringValue"`
						} `json:"value"`
					} `json:"attributes"`
				} `json:"resource"`
				ScopeLogs []struct {
					LogRecords []struct {
						Body struct {
							S string `json:"stringValue"`
						} `json:"body"`
						Severity   string `json:"severityText"`
						Attributes []struct {
							Key   string `json:"key"`
							Value struct {
								S string `json:"stringValue"`
							} `json:"value"`
						} `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue // a line being written
		}
		for _, rl := range req.ResourceLogs {
			mine := false
			for _, a := range rl.Resource.Attributes {
				mine = mine || a.Key == "service.name" && a.Value.S == service
			}
			if !mine {
				continue
			}
			for _, sl := range rl.ScopeLogs {
				for _, r := range sl.LogRecords {
					attrs := map[string]string{"severity": r.Severity}
					for _, a := range r.Attributes {
						attrs[a.Key] = a.Value.S
					}
					out[r.Body.S] = attrs
				}
			}
		}
	}
	return out
}

// TestOTLPExport exports log records in both encodings to the
// OpenTelemetry Collector, which writes them to its file as it took them.
func TestOTLPExport(t *testing.T) {
	base := obsEnv(t, "FIT_SYNC_OTLP_URL")
	file := obsEnv(t, "FIT_SYNC_OTLP_FILE")
	for _, enc := range []string{"protobuf", "json"} {
		t.Run(enc, func(t *testing.T) {
			service := fmt.Sprintf("fit-%s-%d", enc, time.Now().UnixNano())
			s := loadSinkModule(t, "otlp_logs", map[string]any{"urls": []string{base}, "encoding": enc, "service_name": service,
				"severity_field": "level", "attributes": map[string]any{"user": "{user}"}})
			if err := s.(fitsync.Tester).Test(context.Background()); err != nil {
				t.Fatal(err)
			}
			var recs []*fitsync.Record
			for i := range 200 {
				recs = append(recs, &fitsync.Record{Source: "test", Namespace: "app", Op: fitsync.OpMessage, CommitTime: time.Now(),
					After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(fmt.Sprintf("record %d", i))},
						fitsync.Field{Name: "level", Value: fitsync.String("WARN")}, fitsync.Field{Name: "user", Value: fitsync.String("u" + strconv.Itoa(i))})})
			}
			writeAll(t, s, recs, 64)
			deadline := time.Now().Add(30 * time.Second)
			var got map[string]map[string]string
			for {
				got = otlpRecords(t, file, service)
				if len(got) >= 200 || time.Now().After(deadline) {
					break
				}
				time.Sleep(200 * time.Millisecond)
			}
			if len(got) != 200 {
				t.Fatalf("%d records, want 200", len(got))
			}
			if a := got["record 7"]; a["user"] != "u7" || a["severity"] != "WARN" {
				t.Fatalf("%v", a)
			}
		})
	}
}

// TestOTLPFilesAcrossKills copies log files to the Collector with the file
// source, killed at random points: every line reaches it.
func TestOTLPFilesAcrossKills(t *testing.T) {
	base := obsEnv(t, "FIT_SYNC_OTLP_URL")
	file := obsEnv(t, "FIT_SYNC_OTLP_FILE")
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 12))
	dir := t.TempDir()
	service := fmt.Sprintf("fit-files-%d", time.Now().UnixNano())
	want := writeLogFiles(t, dir, 2, 3000, service)
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
		"source": map[string]any{"module": "file", "params": map[string]any{"paths": []string{filepath.Join(dir, "*.log")}}},
		"sinks": []any{map[string]any{"name": "otlp", "batch": map[string]any{"max_records": 1 + rnd.IntN(200), "max_wait": "1ms"},
			"sink": map[string]any{"module": "otlp_logs", "params": map[string]any{"urls": []string{base},
				"encoding": []string{"protobuf", "json"}[rnd.IntN(2)], "service_name": service}}}}})
	kills := runKilled(t, cfg, rnd, 8)
	deadline := time.Now().Add(30 * time.Second)
	var got map[string]map[string]string
	for {
		got = otlpRecords(t, file, service)
		if len(got) >= len(want) || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	for l := range got {
		if !want[l] {
			t.Fatalf("a line not written: %q", l)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("%d of %d lines exported", len(got), len(want))
	}
	t.Logf("%d lines across %d kills", len(want), kills)
}

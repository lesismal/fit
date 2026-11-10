package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// syncFake is an Admin API's /sync endpoints, answering canned bodies.
type syncFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last struct{ method, uri, body string }
	// answers are the bodies by "METHOD path", status 200 unless code says.
	answers map[string]string
	code    map[string]int
}

const fakeStatus = `{"name": "p", "state": "running", "errors": 2, "error": "boom", "checkpoint": {"n": 5}, "queue": {"records": 1, "bytes": 9, "max_records": 10, "max_bytes": 99},
	"read": 7, "acknowledged": 5, "failed": 1, "throughput": 2.5, "last_commit": "2026-11-08T00:00:00Z", "lag_seconds": 1.5,
	"job": {"started": "2026-11-08T00:00:00Z", "read": 7, "written": 5, "failed": 1, "eta": "1s", "chunks_done": 1, "chunks": [{}, {}]}, "next": "2026-11-09T00:00:00Z",
	"source_lag": {"behind": 42, "unit": "bytes", "at": "2026-11-08T00:00:00Z"}, "sinks": [{"name": "m", "records": 5, "bytes": 900, "batches": 3, "errors": 1, "retries": 1, "failed": 1}],
	"dead_letters": {"written": 1, "store": {"kept": 1, "max_records": 10000}}}`

func newSyncFake(t *testing.T) *syncFake {
	f := &syncFake{
		answers: map[string]string{
			"GET /sync/pipelines":                 `{"pipelines": [` + fakeStatus + `, {"name": "q", "state": "finished"}]}`,
			"GET /sync/pipelines/p":               fakeStatus,
			"POST /sync/pipelines/p":              fakeStatus,
			"GET /sync/pipelines/p/checkpoint":    `{"checkpoint": {"version": 1, "pipeline": "p", "position": {"n":5}, "saved": "2026-11-08T00:00:00Z", "job": {"read": 1, "written": 1}}}`,
			"PUT /sync/pipelines/p/checkpoint":    `{"checkpoint": {"version": 1, "pipeline": "p", "position": {"n":3}, "saved": "2026-11-08T00:00:00Z"}}`,
			"DELETE /sync/pipelines/p/checkpoint": `{"name": "p", "state": "starting"}`,
			"GET /sync/pipelines/q/checkpoint":    `{"checkpoint": null}`,
			"POST /sync/pipelines/p/test":         `{"ok": false, "results": [{"part": "source", "module": "mysql", "tested": true, "error": "refused"}, {"part": "sink m", "module": "memory", "tested": true, "ok": true, "warnings": ["a column loses data"]}, {"part": "sink f", "module": "file", "error": "no test"}]}`,
			"POST /sync/pipelines/q/test":         `{"ok": true, "results": []}`,
			"POST /sync/pipelines/p/dry-run":      `{"records": [{"in": {"op":"insert"}, "out": [{"op":"insert"}]}, {"in": {"op": "delete"}, "out": [], "error": "transform 0 (x): no"}], "ended": true}`,
			"POST /sync/pipelines/q/dry-run":      `{"records": [], "timed_out": true, "source_error": "down"}`,
			"POST /sync/pipelines/c":              `{"error": "it would skip or replay records: confirm it"}`,
			"GET /sync/pipelines/none":            `{"error": "no such pipeline"}`,
			"GET /sync/pipelines/p/dead-letters": `{"letters": [{"id": 3, "saved": "2026-11-08T00:00:00Z", "error": "a bad row", "at": "sink m", "attempts": 2, "record": {"op": "insert"}}],
				"store": {"path": "/s/p.dead_letters.jsonl", "kept": 1, "bytes": 300, "max_records": 10000, "max_bytes": 67108864}, "more": true}`,
			"POST /sync/pipelines/p/dead-letters/replay": `{"replayed": [3], "status": {}}`,
			"POST /sync/pipelines/q/dead-letters/replay": `{"replayed": [], "failed": [{"id": 4, "error": "sink m: down"}], "status": {}}`,
			"DELETE /sync/pipelines/p/dead-letters":      `{"dropped": 2}`,
		},
		code: map[string]int{"POST /sync/pipelines/c": 409, "GET /sync/pipelines/none": 404},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.last.method, f.last.uri, f.last.body = r.Method, r.URL.RequestURI(), string(body)
		f.mu.Unlock()
		k := r.Method + " " + r.URL.Path
		if c := f.code[k]; c != 0 {
			w.WriteHeader(c)
		}
		_, _ = w.Write([]byte(f.answers[k]))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func runFitSync(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(append([]string{"sync"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestSyncAdminCommands(t *testing.T) {
	f := newSyncFake(t)
	u, _ := url.Parse(f.srv.URL)
	boot := writeCheckBoot(t, `{"address": `+js(u.Host)+`}`)
	for _, tc := range []struct {
		args              []string
		code              int
		method, uri, body string
		out, err          []string
	}{
		{[]string{"status", "-c", boot}, 0, "GET", "/sync/pipelines", "", []string{"PIPELINE", "p  ", "running", "1.5s", "q  ", "finished"}, nil},
		{[]string{"status", "-c", boot, "p"}, 0, "GET", "/sync/pipelines/p", "", []string{`checkpoint {"n": 5}`, "7 read, 5 acknowledged, 1 failed, 2.5/s", "1 of 10 records", "lag        1.5s",
			"1 of 2 chunks, ETA 1s", "next       2026-11-09", "errors     2, the last: boom",
			"behind     42 bytes", "sink m     5 records, 900 bytes in 3 batches; 1 errors, 1 retries, 1 failed", "dead       1 written, 0 not written, 0 replayed; 1 kept of 10000"}, nil},
		{[]string{"status", "-c", boot, "-json", "p"}, 0, "GET", "/sync/pipelines/p", "", []string{`"name": "p"`}, nil},
		{[]string{"pause", "p", "-c", boot}, 0, "POST", "/sync/pipelines/p", `{"action":"pause"}`, []string{"state      running"}, nil},
		{[]string{"resume", "-c", boot, "p"}, 0, "POST", "/sync/pipelines/p", `{"action":"resume"}`, nil, nil},
		{[]string{"snapshot", "-c", boot, "-confirm", "p"}, 0, "POST", "/sync/pipelines/p", `{"action":"snapshot","confirm":true}`, nil, nil},
		{[]string{"snapshot", "-c", boot, "c"}, 1, "POST", "/sync/pipelines/c", `{"action":"snapshot","confirm":false}`, nil, []string{"answered 409", "give -confirm"}},
		{[]string{"checkpoint", "-c", boot, "p"}, 0, "GET", "/sync/pipelines/p/checkpoint", "", []string{`position {"n":5}`, "job      1 read, 1 written"}, nil},
		{[]string{"checkpoint", "-c", boot, "q"}, 0, "GET", "/sync/pipelines/q/checkpoint", "", []string{"no checkpoint yet"}, nil},
		{[]string{"checkpoint", "-c", boot, "-set", `{"n": 3}`, "-confirm", "p"}, 0, "PUT", "/sync/pipelines/p/checkpoint", `{"confirm":true,"position":{"n":3}}`, []string{`position {"n":3}`}, nil},
		{[]string{"checkpoint", "-c", boot, "-reset", "-confirm", "p"}, 0, "DELETE", "/sync/pipelines/p/checkpoint?confirm=1", "", []string{"removed; the pipeline is starting"}, nil},
		{[]string{"checkpoint", "-c", boot, "-reset", "p"}, 0, "DELETE", "/sync/pipelines/p/checkpoint", "", nil, nil},
		{[]string{"test", "-c", boot, "p"}, 1, "POST", "/sync/pipelines/p/test", "", []string{"failed: refused", "not tested: no test", "sink m  memory  ok", "warning: a column loses data"}, []string{"a connection test failed"}},
		{[]string{"test", "-c", boot, "q"}, 0, "POST", "/sync/pipelines/q/test", "", []string{"PART"}, nil},
		{[]string{"dry-run", "-c", boot, "-limit", "3", "-wait", "2s", "-from-start", "p"}, 0, "POST", "/sync/pipelines/p/dry-run", `{"from":"start","limit":3,"timeout":"2s"}`,
			[]string{`in   {"op":"insert"}`, `  out {"op":"insert"}`, "  error transform 0", "2 records read, to the source's end"}, nil},
		{[]string{"dry-run", "-c", boot, "q"}, 1, "POST", "/sync/pipelines/q/dry-run", `{"limit":0}`, []string{"0 records read, all there were"}, []string{"the source failed: down"}},
		{[]string{"dead-letters", "-c", boot, "-after", "2", "-limit", "5", "p"}, 0, "GET", "/sync/pipelines/p/dead-letters?after=2&limit=5", "",
			[]string{"3   2026-11-08T00:00:00Z  sink m  2", "a bad row", `{"op": "insert"}`, "1 dead letters kept (300 of 67108864 bytes, at most 10000) in /s/p.dead_letters.jsonl; more after"}, nil},
		{[]string{"dead-letters", "-c", boot, "p"}, 0, "GET", "/sync/pipelines/p/dead-letters", "", nil, nil},
		{[]string{"dead-letters", "-c", boot, "-replay", "p", "3"}, 0, "POST", "/sync/pipelines/p/dead-letters/replay", `{"all":false,"ids":[3]}`, []string{"1 dead letters replayed"}, nil},
		{[]string{"dead-letters", "-c", boot, "-replay", "-all", "q"}, 1, "POST", "/sync/pipelines/q/dead-letters/replay", `{"all":true,"ids":null}`, []string{"0 dead letters replayed"}, []string{"dead letter 4 kept: sink m: down"}},
		{[]string{"dead-letters", "-c", boot, "-drop", "p", "1", "2"}, 0, "DELETE", "/sync/pipelines/p/dead-letters?id=1&id=2", "", []string{"2 dead letters dropped"}, nil},
		{[]string{"dead-letters", "-c", boot, "-drop", "-all", "p"}, 0, "DELETE", "/sync/pipelines/p/dead-letters?all=1", "", nil, nil},
		{[]string{"status", "-c", boot, "none"}, 1, "GET", "/sync/pipelines/none", "", nil, []string{"answered 404: no such pipeline"}},
	} {
		code, out, errOut := runFitSync(tc.args...)
		f.mu.Lock()
		last := f.last
		f.mu.Unlock()
		if code != tc.code || last.method != tc.method || last.uri != tc.uri || last.body != tc.body {
			t.Errorf("%v: %d %+v\n%s%s", tc.args, code, last, out, errOut)
			continue
		}
		for _, w := range tc.out {
			if !strings.Contains(out, w) {
				t.Errorf("%v: no %q in\n%s", tc.args, w, out)
			}
		}
		for _, w := range tc.err {
			if !strings.Contains(errOut, w) {
				t.Errorf("%v: no %q in\n%s", tc.args, w, errOut)
			}
		}
	}

	// Used wrongly.
	for _, args := range [][]string{
		{}, {"bogus"}, {"pause", "-c", boot}, {"pause", "-c", boot, "a", "b"}, {"pause", "-nope"},
		{"checkpoint", "-c", boot, "-set", "{", "p"}, {"checkpoint", "-c", boot, "-set", "1", "-reset", "p"},
		{"status", "-c", "/no/such/fit.json"},
		{"dead-letters", "-c", boot, "p", "x"}, {"dead-letters", "-c", boot, "-replay", "-drop", "-all", "p"},
		{"dead-letters", "-c", boot, "-replay", "p"}, {"dead-letters", "-c", boot, "-drop", "-all", "p", "1"},
		{"dead-letters", "-c", boot, "-all", "p"}, {"dead-letters", "-c", boot, "p", "1"},
	} {
		if code, out, errOut := runFitSync(args...); code != 2 {
			t.Errorf("%v: %d %s %s", args, code, out, errOut)
		}
	}
	if code, out, _ := runFitSync("help"); code != 0 || !strings.Contains(out, "fit sync dry-run") {
		t.Errorf("help: %d %s", code, out)
	}
	if code, out, _ := runFitSync("status", "-c", writeCheckBoot(t, `{"address": "127.0.0.1:1"}`)); code != 1 {
		t.Errorf("no API: %d %s", code, out)
	}
	if code, _, errOut := runFitSync("status", "-c", writeCheckBoot(t, `{"address": "127.0.0.1:1"}`), "-ca", "x.pem"); code != 2 || !strings.Contains(errOut, "fit sync:") {
		t.Errorf("TLS flags without TLS: %d %s", code, errOut)
	}
}

func TestSyncAdminEmptyList(t *testing.T) {
	f := newSyncFake(t)
	f.answers["GET /sync/pipelines"] = `{"pipelines": []}`
	u, _ := url.Parse(f.srv.URL)
	if code, out, _ := runFitSync("status", "-c", writeCheckBoot(t, `{"address": `+js(u.Host)+`}`)); code != 0 || !strings.Contains(out, "no pipeline runs") {
		t.Fatalf("%d %s", code, out)
	}
}

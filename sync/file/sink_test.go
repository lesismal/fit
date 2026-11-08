package syncfile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func records(n int) []*fitsync.Record {
	recs := make([]*fitsync.Record, n)
	for i := range recs {
		recs[i] = &fitsync.Record{Namespace: "ns", Op: fitsync.OpInsert, Key: fitsync.Int(int64(i)),
			After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(fmt.Sprintf("record %04d", i))},
				fitsync.Field{Name: "n", Value: fitsync.Int(int64(i))})}
	}
	return recs
}

func sink(t *testing.T, params string) *Sink {
	t.Helper()
	return load(t, fitsync.Sinks, "file", params).(*Sink)
}

// TestSinkRotates writes lines through the log file sink, rotated by size
// as a log is.
func TestSinkRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	s := sink(t, `{"path": `+jsonString(path)+`, "rotate": {"max_bytes": 1000, "compress": false}}`)
	recs := records(300)
	for i := 0; i < len(recs); i += 50 {
		if err := s.Write(context.Background(), recs[i:i+50]); err != nil {
			t.Fatal(err)
		}
	}
	names, _ := filepath.Glob(path + ".*")
	if len(names) < 3 {
		t.Fatalf("rotated into %v", names)
	}
	sort.Strings(names) // rotated files are named by time, oldest first
	var all strings.Builder
	for _, n := range append(names, path) {
		data, err := os.ReadFile(n)
		if err != nil {
			t.Fatal(err)
		}
		if fi, _ := os.Stat(n); fi.Size() > 1000 {
			t.Fatalf("%s holds %d bytes", n, fi.Size())
		}
		all.Write(data)
	}
	lines := strings.Split(strings.TrimSuffix(all.String(), "\n"), "\n")
	if len(lines) != 300 || lines[0] != "record 0000" || lines[299] != "record 0299" {
		t.Fatalf("%d lines: %q ... %q", len(lines), lines[0], lines[len(lines)-1])
	}
	for i, l := range lines {
		if l != fmt.Sprintf("record %04d", i) {
			t.Fatalf("line %d is %q", i, l)
		}
	}
	// The file is the logs' own: listed with them, and rotated on demand.
	if logs.DefaultFiles().File(path) == nil {
		t.Fatal("the logs do not list the file")
	}
	// One sink per path, and none on a log's file.
	other := sink(t, `{"path": `+jsonString(path)+`}`)
	if err := other.Write(context.Background(), recs[:1]); err == nil || !strings.Contains(err.Error(), "another sync file sink") {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "fit.log")
	lf, err := logs.DefaultFiles().Open(logPath, nil, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer logs.DefaultFiles().Release(lf)
	if err := sink(t, `{"path": `+jsonString(logPath)+`}`).Write(context.Background(), recs[:1]); err == nil || !strings.Contains(err.Error(), "fit's log") {
		t.Fatal(err)
	}
	s.Cleanup()
	if logs.DefaultFiles().File(path) != nil {
		t.Fatal("the file is still open")
	}
}

func TestSinkFormats(t *testing.T) {
	dir := t.TempDir()
	recs := records(2)
	recs[1].After = fitsync.Null()
	recs[1].Before = fitsync.Map(fitsync.Field{Name: "n", Value: fitsync.Int(9)})
	recs[1].Op = fitsync.OpDelete
	recs[1].CommitTime = time.Date(2026, 11, 8, 1, 2, 3, 0, time.UTC)
	for _, c := range []struct{ params, want string }{
		{`"codec": {"module": "json"}`, `{"line":"record 0000","n":0}` + "\n" + `{"n":9}` + "\n"},
		{`"codec": {"module": "csv", "params": {"columns": ["n", "line"]}}`, "0,record 0000\n9,\n"},
		{`"template": "{$op} {$namespace} {n} {{x} {$key} [{$commit_time}]"`, "insert ns 0 {x} 0 []\ndelete ns 9 {x} 1 [2026-11-08T01:02:03Z]\n"},
	} {
		path := filepath.Join(dir, fmt.Sprintf("f%d.log", len(c.want)))
		s := sink(t, `{"path": `+jsonString(path)+`, `+c.params+`}`)
		if err := s.Write(context.Background(), recs); err != nil {
			t.Fatal(err)
		}
		if data, _ := os.ReadFile(path); string(data) != c.want {
			t.Errorf("%s: %q", c.params, data)
		}
		s.Cleanup()
	}
	for _, params := range []string{
		`{"path": ""}`,
		`{"path": "x", "template": "{"}`,
		`{"path": "x", "template": "{$nope}"}`,
		`{"path": "x", "template": "{a}", "codec": {"module": "line"}}`,
		`{"path": "x", "rotate": {"interval": "10ms"}}`,
		`{"path": "x", "time_zone": "Nowhere/Else"}`,
		`{"path": "x", "codec": {"module": "nope"}}`,
	} {
		if _, err := module.Load(fitsync.Sinks, config.ModuleRef{Module: "file", Params: paramsOf(t, params)}, nil); err == nil {
			t.Errorf("%s loaded", params)
		}
	}
	// A record the codec cannot encode fails, those before it written.
	path := filepath.Join(dir, "bad.log")
	s := sink(t, `{"path": `+jsonString(path)+`, "codec": {"module": "json"}}`)
	bad := records(3)
	bad[1].After = fitsync.Map(fitsync.Field{Name: "j", Value: fitsync.JSON([]byte("{nope"))})
	err := s.Write(context.Background(), bad)
	if be, ok := err.(*fitsync.BatchError); !ok || be.Index != 1 {
		t.Fatalf("%v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != `{"line":"record 0000","n":0}`+"\n" {
		t.Fatalf("%q", data)
	}
	s.Cleanup()
}

// TestFileToFile copies records through a pipeline from a file sink back
// into a file source, in the record form of the json codec.
func TestFileToFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ndjson")
	pipeline := func(name, source, sink string) string {
		return fmt.Sprintf(`{"name": %q, "mode": "once", "source": %s, "sinks": [{"name": "s", "sink": %s, "batch": {"max_wait": "1ms"}}]}`, name, source, sink)
	}
	store := "file-to-file"
	m := fitsync.MemoryStore(store)
	t.Cleanup(m.Reset)
	src := fmt.Sprintf(`{"sync": {"state_dir": %s, "pipelines": [%s]}}`, jsonString(filepath.Join(dir, "state")),
		pipeline("write", `{"module": "generate", "params": {"records": 50}}`,
			`{"module": "file", "params": {"path": `+jsonString(out)+`, "codec": {"module": "json", "params": {"form": "record"}}}}`))
	run := func(src string, name string) {
		t.Helper()
		c, err := config.Load([]byte(src))
		if err != nil {
			t.Fatal(err)
		}
		rt := fitsync.NewRuntime(fitsync.Options{})
		if err := rt.Apply(c.Sync); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		for status(rt) != "finished" {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s", name, status(rt))
			}
			time.Sleep(2 * time.Millisecond)
		}
		rt.Close(context.Background())
	}
	run(src, "write")
	src = fmt.Sprintf(`{"sync": {"state_dir": %s, "pipelines": [%s]}}`, jsonString(filepath.Join(dir, "state")),
		pipeline("read", `{"module": "file", "params": {"paths": [`+jsonString(out)+`], "codec": {"module": "json", "params": {"form": "record"}}}}`,
			`{"module": "memory", "params": {"store": "`+store+`"}}`))
	run(src, "read")
	recs := m.Records()
	if len(recs) != 50 {
		t.Fatalf("%d records", len(recs))
	}
	for i, r := range recs {
		want := fitsync.GenerateRecord("", int64(i), 50, 1)
		if r.Op != want.Op || !r.Key.Equal(want.Key) || !r.After.Equal(want.After) || !r.Before.Equal(want.Before) || r.Namespace != out {
			t.Fatalf("record %d: %+v", i, r)
		}
	}
}

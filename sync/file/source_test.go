package syncfile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// load loads module name of namespace ns with params, as a pipeline does.
func load(t *testing.T, ns, name, params string) any {
	t.Helper()
	src := fmt.Sprintf(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate", "params": %s}, "sinks": [{"name": "s", "sink": {"module": "memory"}}]}]}}`, params)
	c, err := config.Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	v, err := module.Load(ns, config.ModuleRef{Module: name, Params: c.Sync.Pipelines[0].Source.Params}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(v) })
	return v
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// collector runs a source, keeping what it emits.
type collector struct {
	mu   sync.Mutex
	recs []*fitsync.Record
	last fitsync.Position
}

func (c *collector) emit(r *fitsync.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recs = append(c.recs, r)
	if r.Position != nil {
		c.last = r.Position
	}
	return nil
}

// lines returns the line field of what was emitted.
func (c *collector) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.recs {
		v, _ := r.After.Get("line")
		out = append(out, fitsync.TextOf(v))
	}
	return out
}

// once runs s over the files as they are, from from, and returns what it
// emitted.
func once(t *testing.T, s *Source, from fitsync.Position) *collector {
	t.Helper()
	c := &collector{}
	// Only a guard against a hang: ten thousand files take ~2s, ~10s under
	// the race detector, and several times that on CI's runners.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := s.Run(ctx, &fitsync.Run{From: from, Once: true, Emit: c.emit}); err != nil {
		t.Fatal(err)
	}
	return c
}

func write(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

func TestOnceAndResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	write(t, path, "one\r\ntwo\nthree") // a last line without its newline ends the file in once
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(filepath.Join(dir, "*.log"))+`]}`).(*Source)
	c := once(t, s, nil)
	if got := strings.Join(c.lines(), "|"); got != "one|two|three" {
		t.Fatalf("%q", got)
	}
	r := c.recs[1]
	if r.Source != "file" || r.Namespace != path || r.Op != fitsync.OpMessage || c.recs[0].Position != nil || c.recs[2].Position == nil {
		t.Fatalf("%+v", r)
	}
	if off, _ := r.Key.Get("offset"); off.AsInt() != 5 {
		t.Fatalf("key %+v", r.Key)
	}
	write(t, path, "\nfour\n")
	c2 := once(t, s, c.last)
	if got := strings.Join(c2.lines(), "|"); got != "|four" {
		t.Fatalf("resumed: %q", got)
	}
	// A truncated file, and one whose inode a new file took, start again.
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c3 := once(t, s, c2.last)
	if got := strings.Join(c3.lines(), "|"); got != "new" {
		t.Fatalf("truncated: %q", got)
	}
	if k0, k3 := c.recs[0].Key.Fields()[0].Value, c3.recs[0].Key.Fields()[0].Value; k0.Equal(k3) {
		t.Fatalf("the key %v names the file before and after its truncation alike", k0)
	}
	if err := os.WriteFile(path, []byte("other content, longer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(once(t, s, c3.last).lines(), "|"); got != "other content, longer" {
		t.Fatalf("replaced: %q", got)
	}
	if err := (&Source{}).Run(context.Background(), &fitsync.Run{From: fitsync.Position("{"), Once: true}); err == nil {
		t.Fatal("a bad checkpoint was taken")
	}
}

func TestLongLinesAndMultiline(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	long := strings.Repeat("x", 100)
	write(t, path, "short\n"+long+"\nok\n"+long+long)
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(path)+`], "max_line_bytes": 16}`).(*Source)
	got := once(t, s, nil).lines()
	if strings.Join(got, "|") != "short|"+long[:16]+"|ok|"+long[:16] || s.LinesCut() != 2 {
		t.Fatalf("%q, %d cut", got, s.LinesCut())
	}

	ml := filepath.Join(dir, "m.log")
	write(t, ml, "head\nERR one\n  at a\n  at b\nERR two\nERR three\n  at c\n  at d\n  at e\n")
	s = load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(ml)+`], "multiline": {"start": "^ERR", "max_lines": 3}}`).(*Source)
	c := once(t, s, nil)
	want := []string{"head", "ERR one\n  at a\n  at b", "ERR two", "ERR three\n  at c\n  at d", "  at e"}
	if strings.Join(c.lines(), "|") != strings.Join(want, "|") {
		t.Fatalf("%q", c.lines())
	}
	// Resuming from a position taken mid-event reads the event whole.
	write(t, ml, "ERR four\n  at f\n")
	if got := once(t, s, c.last).lines(); strings.Join(got, "|") != "ERR four\n  at f" {
		t.Fatalf("%q", got)
	}
	cut := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(ml)+`], "multiline": {"start": "^ERR"}, "max_line_bytes": 12}`).(*Source)
	if got := once(t, cut, nil).lines(); got[1] != "ERR one\n  at" || cut.LinesCut() == 0 {
		t.Fatalf("%q", got)
	}
}

func TestCodecs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	write(t, path, `{"a":1}`+"\nnot json\n")
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(path)+`], "codec": {"module": "json"}}`).(*Source)
	c := once(t, s, nil)
	if a, _ := c.recs[0].After.Get("a"); a.AsInt() != 1 {
		t.Fatalf("%+v", c.recs[0])
	}
	// A line the codec refuses is kept, with the error.
	if e, _ := c.recs[1].After.Get("error"); fitsync.TextOf(e) == "" || c.lines()[1] != "not json" {
		t.Fatalf("%+v", c.recs[1])
	}
	for _, params := range []string{
		`{"paths": ["[x"]}`,
		`{"paths": ["a"], "multiline": {"start": "("}}`,
		`{"paths": ["a"], "codec": {"module": "nope"}}`,
	} {
		ref := config.ModuleRef{Module: "file", Params: paramsOf(t, params)}
		if _, err := module.Load(fitsync.Sources, ref, nil); err == nil {
			t.Errorf("%s loaded", params)
		}
	}
}

func paramsOf(t *testing.T, params string) config.Value {
	t.Helper()
	c, err := config.Load([]byte(`{"sync": {"state_dir": "x", "pipelines": [{"name": "p", "source": {"module": "generate", "params": ` + params + `}, "sinks": [{"name": "s", "sink": {"module": "memory"}}]}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	return c.Sync.Pipelines[0].Source.Params
}

// TestTenThousandFiles reads ten thousand files with at most 64 open.
func TestTenThousandFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("makes ten thousand files")
	}
	dir := t.TempDir()
	const n = 10000
	for i := range n {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%05d.log", i)), fmt.Appendf(nil, "file %d\nend %d\n", i, i), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(filepath.Join(dir, "*.log"))+`], "max_open_files": 64}`).(*Source)
	c := once(t, s, nil)
	if len(c.recs) != 2*n {
		t.Fatalf("%d records", len(c.recs))
	}
	if p := s.peak.Load(); p > 64 || p == 0 {
		t.Fatalf("%d files were open at once", p)
	}
	var pos position
	if err := json.Unmarshal(c.last, &pos); err != nil || len(pos.Files) != n {
		t.Fatalf("%d files in the position: %v", len(pos.Files), err)
	}
	// Files gone are forgotten, so the checkpoint stays bounded.
	for i := range n - 10 {
		os.Remove(filepath.Join(dir, fmt.Sprintf("f%05d.log", i)))
	}
	// A file a scan misses is forgotten once missing for close_after, as
	// it may only have been renamed while the directory was listed.
	s = load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(filepath.Join(dir, "*.log"))+`], "close_after": "1ms"}`).(*Source)
	last := c.last
	for i := range 2 {
		write(t, filepath.Join(dir, "f09999.log"), "more\n")
		c2 := once(t, s, last)
		if err := json.Unmarshal(c2.last, &pos); err != nil || len(c2.recs) != 1 {
			t.Fatalf("%d records: %v", len(c2.recs), err)
		}
		if want := []int{n, 10}[i]; len(pos.Files) != want {
			t.Fatalf("run %d: %d files in the position, want %d", i, len(pos.Files), want)
		}
		last = c2.last
		time.Sleep(5 * time.Millisecond)
	}
}

// stream runs s until stop is called, and returns what it emitted.
func stream(s *Source, from fitsync.Position) (c *collector, stop func() error) {
	c = &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, &fitsync.Run{From: from, Emit: c.emit}) }()
	return c, func() error { cancel(); return <-done }
}

func waitLines(t *testing.T, c *collector, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for len(c.lines()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d lines, want %d: %q", len(c.lines()), n, c.lines())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestPartialLineAndDeletion holds a partial line until close_after, and
// finishes a file deleted while it is read.
func TestPartialLineAndDeletion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	w, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.WriteString("first\npart")
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(path)+`], "scan_interval": "10ms", "poll_interval": "2ms", "close_after": "300ms"}`).(*Source)
	c, stop := stream(s, nil)
	waitLines(t, c, 1)
	time.Sleep(50 * time.Millisecond)
	w.WriteString("ial\n") // ends before close_after
	waitLines(t, c, 2)
	w.WriteString("late")
	waitLines(t, c, 3) // taken as it is once close_after passes
	if got := strings.Join(c.lines(), "|"); got != "first|partial|late" {
		t.Fatalf("%q", got)
	}
	if runtimeWindows() {
		stop()
		return
	}
	w.WriteString("\nmore\n")
	waitLines(t, c, 5)
	// Deleted while open: what is written to it after is still read.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	w.WriteString("after delete\n")
	waitLines(t, c, 6)
	if err := stop(); err != context.Canceled {
		t.Fatal(err)
	}
	if got := c.lines(); got[3] != "" || got[4] != "more" || got[5] != "after delete" {
		t.Fatalf("%q", got)
	}
}

// TestSameSizeNewContents replaces a closed file's contents with others of
// the same size, as a new file taking a freed inode does on Linux: the new
// contents are read whole, under a key of their own.
func TestSameSizeNewContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	write(t, path, "line 1\n")
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(path)+`], "scan_interval": "10ms", "poll_interval": "2ms", "close_after": "30ms"}`).(*Source)
	c, stop := stream(s, nil)
	defer stop()
	waitLines(t, c, 1)
	time.Sleep(150 * time.Millisecond) // closed by now
	if err := os.WriteFile(path, []byte("LINE 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitLines(t, c, 2)
	if got := c.lines(); got[1] != "LINE 2" {
		t.Fatalf("%q", got)
	}
	c.mu.Lock()
	k0, k1 := c.recs[0].Key, c.recs[1].Key
	c.mu.Unlock()
	if k0.Equal(k1) {
		t.Fatalf("both contents have the key %v", k0)
	}
}

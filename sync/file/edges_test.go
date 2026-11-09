package syncfile

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// twice is the tests' codec making two keyless records of each line.
type twice struct{}

func (twice) Decode(data []byte, emit func(*fitsync.Record)) error {
	for range 2 {
		emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "line", Value: fitsync.String(string(data))})})
	}
	return nil
}

func (twice) Encode(b []byte, _ []*fitsync.Record) ([]byte, error) { return b, nil }

type notCodec struct{}

func init() {
	module.Register(module.Info{ID: fitsync.Codecs + ".filetest_twice", New: func() any { return new(twice) }, Doc: "Tests'."})
	module.Register(module.Info{ID: fitsync.Codecs + ".filetest_notcodec", New: func() any { return new(notCodec) }, Doc: "Tests'."})
}

// TestMaxFiles: files past max_files are not read.
func TestMaxFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		write(t, filepath.Join(dir, n+".log"), n+"1\n"+n+"2\n")
	}
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(filepath.Join(dir, "*.log"))+`], "max_files": 2}`).(*Source)
	got := once(t, s, nil).lines()
	files := map[byte]int{}
	for _, l := range got {
		files[l[0]]++
	}
	if len(got) != 4 || len(files) != 2 {
		t.Fatalf("%q", got)
	}
}

// TestCodecRecordsKeyed: the records a codec makes of one line, without
// keys of their own, are keyed apart by their number in the line.
func TestCodecRecordsKeyed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	write(t, path, "one\n")
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(path)+`], "codec": {"module": "filetest_twice"}}`).(*Source)
	c := once(t, s, nil)
	if len(c.recs) != 2 {
		t.Fatalf("%d records", len(c.recs))
	}
	if _, ok := c.recs[0].Key.Get("n"); ok {
		t.Errorf("the first record's key %v has an n", c.recs[0].Key)
	}
	if n, ok := c.recs[1].Key.Get("n"); !ok || n.AsInt() != 1 || c.recs[0].Key.Equal(c.recs[1].Key) {
		t.Errorf("keys %v and %v", c.recs[0].Key, c.recs[1].Key)
	}
	ref := config.ModuleRef{Module: "file", Params: paramsOf(t, `{"paths": ["a"], "codec": {"module": "filetest_notcodec"}}`)}
	if _, err := module.Load(fitsync.Sources, ref, nil); err == nil || !strings.Contains(err.Error(), "is not a codec") {
		t.Errorf("%v", err)
	}
}

// TestBadCheckpoint: a checkpoint whose file has no fingerprint, or a
// negative offset, fails the run; one naming a file twice keeps the first.
func TestBadCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")
	write(t, path, "one\ntwo\n")
	s := load(t, fitsync.Sources, "file", `{"paths": [`+jsonString(path)+`]}`).(*Source)
	for _, from := range []string{
		`{"files": [{"dev": 1, "ino": 2, "fp": "zz", "path": "x", "off": 0}]}`,
		`{"files": [{"dev": 1, "ino": 2, "fp": "1f", "path": "x", "off": -1}]}`,
	} {
		err := s.Run(context.Background(), &fitsync.Run{From: fitsync.Position(from), Once: true, Emit: func(*fitsync.Record) error { return nil }})
		if err == nil || !strings.Contains(err.Error(), "a bad fingerprint or offset") {
			t.Errorf("%s: %v", from, err)
		}
	}
	// Read once, then resumed from a checkpoint naming the file twice: the
	// first is kept, and nothing is read again.
	c := once(t, s, nil)
	if len(c.lines()) != 2 {
		t.Fatalf("%q", c.lines())
	}
	var p struct{ Files []map[string]any }
	if err := json.Unmarshal(c.last, &p); err != nil || len(p.Files) != 1 {
		t.Fatalf("%v %s", err, c.last)
	}
	dup := map[string]any{}
	for k, v := range p.Files[0] {
		dup[k] = v
	}
	dup["off"] = 0
	from, err := json.Marshal(map[string]any{"files": []any{p.Files[0], dup}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, "three\n")
	if got := once(t, s, from).lines(); strings.Join(got, "|") != "three" {
		t.Fatalf("%q", got)
	}
}

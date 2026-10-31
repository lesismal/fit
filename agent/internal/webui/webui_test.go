package webui

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestPages(t *testing.T) {
	p := Pages(fstest.MapFS{"index.html": {Data: []byte("<p>")}, "app.js": {Data: []byte("1")}})
	var got []string
	if err := fs.WalkDir(p, ".", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			got = append(got, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"index.html": true, "app.js": true, "static/ui.js": true, "static/ui.css": true, "static/peers.js": true, "static/jobs.js": true}
	for _, g := range got {
		delete(want, g)
	}
	if len(want) != 0 {
		t.Fatalf("missing %v in %v", want, got)
	}
}

func TestEvent(t *testing.T) {
	if got := string(event("end", []byte("a\r\nb"))); got != "event: end\ndata: a\ndata: b\n\n" {
		t.Fatalf("%q", got)
	}
	if got := string(event("", []byte(`{"x":1}`))); got != "data: {\"x\":1}\n\n" {
		t.Fatalf("%q", got)
	}
}

package static

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

// with runs fn as the handler of a GET for target and returns what it
// answered.
func with(target string, fn func(c *fibhttp.Context)) *fibtest.Recorder {
	return fibtest.ServeRequest(fibhttp.HandlerFunc(fn), httptest.NewRequest("GET", target, nil))
}

// done is a queued request the pool's timeout has answered already.
func done() *queued {
	q := &queued{}
	q.state.Store(queuedDone)
	return q
}

// TestListErrors checks a listing of a directory that went, or that is a
// file, and one whose request the timeout answered first.
func TestListErrors(t *testing.T) {
	root := tree(t, map[string]string{"f.txt": "f", "d/a": "a"})
	s := mustLoad(t, root, `, "browse": true`)
	for _, full := range []string{filepath.Join(root, "missing"), filepath.Join(root, "f.txt")} {
		got := with("/x/", func(c *fibhttp.Context) { s.list(c, "/x/", full, nil) }).Response
		if got.StatusCode != 404 {
			t.Errorf("%s: %d %q", full, got.StatusCode, got.Body)
		}
	}
	for _, full := range []string{filepath.Join(root, "missing"), filepath.Join(root, "f.txt"), root} {
		rec := with("/", func(c *fibhttp.Context) { s.list(c, "/", full, done()) })
		// fib finishes a request its handler left unanswered with an empty 200.
		if got := rec.Response; got.StatusCode != 200 || len(got.Body) != 0 {
			t.Errorf("%s: answered %d %q after the timeout", full, got.StatusCode, got.Body)
		}
	}
}

// TestBrowseOrder checks that directories come before files, whatever
// order the directory gives them in, and that a listing the cache knows is
// listed again from the pool.
func TestBrowseOrder(t *testing.T) {
	files := map[string]string{}
	for i := range 6 {
		files[fmt.Sprintf("f%d", i)] = "x"
		files[fmt.Sprintf("d%d/x", i)] = "x"
	}
	s := mustLoad(t, tree(t, files), `, "browse": true, "check_interval": "1h"`)
	for range 2 {
		got := get(s, "GET", "/", nil).Response
		body := string(got.Body)
		if got.StatusCode != 200 || strings.LastIndex(body, "d5/") > strings.Index(body, ">f0<") {
			t.Fatalf("%d %q", got.StatusCode, body)
		}
	}
}

// TestCacheEdges checks the lookup cache forgetting a path once it is full,
// and the file cache forgetting a file it cannot read, or that grew past
// what it may hold.
func TestCacheEdges(t *testing.T) {
	root := tree(t, map[string]string{"a": "aaaa"})
	full := filepath.Join(root, "a")
	c := cache{entries: map[string]*entry{}, lookups: map[string]*lookup{}, maxLookups: 2, maxBytes: 6, maxFile: 6, check: time.Hour}
	for _, name := range []string{"/1", "/2", "/3"} {
		c.keep(name, &lookup{checked: time.Now()})
	}
	if len(c.lookups) != 2 || c.lookups["/3"] == nil {
		t.Fatalf("lookups %v", c.lookups)
	}
	info, _ := os.Stat(full)
	if e := c.load(full, info, variant{name: "a"}); e == nil || c.bytes != 4 {
		t.Fatalf("load: %v, %d bytes", e, c.bytes)
	}
	if e := c.load(filepath.Join(root, "missing"), info, variant{name: "missing"}); e != nil {
		t.Fatalf("a missing file loaded: %v", e)
	}
	if err := os.WriteFile(full, []byte("aaaaaaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(full)
	if e := c.load(full, info, variant{name: "a"}); e == nil || string(e.data) != "aaaaaaaa" || c.entries[full] != nil || c.bytes != 0 {
		t.Fatalf("too big: %v, %v, %d bytes", e, c.entries[full], c.bytes)
	}
}

// TestGatheredWrite checks a body written without a status first.
// TestLookupKeepsOwnName checks that the cache keeps a copy of a request
// path, not the request's string, which points into fib's buffer of request
// heads and would keep the whole buffer.
func TestLookupKeepsOwnName(t *testing.T) {
	root := tree(t, map[string]string{"a.txt": "a"})
	s := mustLoad(t, root, "")
	head := []byte("GET /a.txt HTTP/1.1")
	name := unsafe.String(&head[5], 5)
	if l := s.find(name, true); l == nil || l.status != 0 {
		t.Fatalf("find: %+v", l)
	}
	for key, l := range s.cache.lookups {
		for _, kept := range []string{key, l.name} {
			if p := unsafe.StringData(kept); uintptr(unsafe.Pointer(p)) >= uintptr(unsafe.Pointer(&head[0])) &&
				uintptr(unsafe.Pointer(p)) < uintptr(unsafe.Pointer(&head[0]))+uintptr(len(head)) {
				t.Fatalf("the cache keeps %q in the request's head", kept)
			}
		}
	}
}

func TestGatheredWrite(t *testing.T) {
	g := &gathered{header: stdhttp.Header{}}
	if _, err := g.Write([]byte("x")); err != nil || g.status != 200 {
		t.Fatalf("%d %v", g.status, err)
	}
	g.WriteHeader(404)
	if g.status != 200 {
		t.Fatalf("status changed to %d", g.status)
	}
}

// TestSendHelpers checks rangeWriter's limit on what it keeps, its copy of
// a reader that is not its file's range, and offsetFile at the end of the
// file.
func TestSendHelpers(t *testing.T) {
	root := tree(t, map[string]string{"a": "abc"})
	f, err := os.Open(filepath.Join(root, "a"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o := &offsetFile{f: f}
	p := make([]byte, 8)
	if n, err := o.Read(p); n != 3 || err != nil || string(p[:n]) != "abc" {
		t.Fatalf("read %d %v", n, err)
	}
	if n, err := o.Read(p); n != 0 || err != io.EOF {
		t.Fatalf("read at the end %d %v", n, err)
	}
	w := &rangeWriter{src: o}
	if n, err := w.ReadFrom(strings.NewReader("small")); n != 5 || err != nil || string(w.small) != "small" {
		t.Fatalf("ReadFrom %d %v %q", n, err, w.small)
	}
	if _, err := w.Write(make([]byte, maxSmall)); err == nil {
		t.Fatal("a body past maxSmall was kept")
	}
}

// TestSendFromDiskErrors checks a file that went before it was opened, a
// directory where a file was, and a request the timeout answered while the
// file was opened.
func TestSendFromDiskErrors(t *testing.T) {
	root := tree(t, map[string]string{"a": "abc", "d/x": "x"})
	s := mustLoad(t, root, "")
	tests := []struct {
		full   string
		q      *queued
		status int
	}{
		{filepath.Join(root, "missing"), nil, 404},
		{filepath.Join(root, "d"), nil, 404},
		{filepath.Join(root, "missing"), done(), 0},
		{filepath.Join(root, "d"), done(), 0},
		{filepath.Join(root, "a"), done(), 0},
	}
	for _, tt := range tests {
		rec := with("/a", func(c *fibhttp.Context) { s.sendFromDisk(c, variant{name: "a"}, tt.full, tt.q) })
		// Status 0 is no answer of the handler's, which fib finishes with an empty 200.
		got := rec.Response
		if tt.status == 0 && (got.StatusCode != 200 || len(got.Body) != 0) || tt.status != 0 && got.StatusCode != tt.status {
			t.Errorf("%s: %v %d", tt.full, rec.Written, rec.Response.StatusCode)
		}
	}
}

// TestSmallHelpers checks a redirect that would read as a host, the status
// of a file that may not be read (the tests may run as root, where
// TestPermission cannot), and the type of a twin whose name says none.
func TestSmallHelpers(t *testing.T) {
	got := with("/", func(c *fibhttp.Context) { redirectToDir(c, "/") }).Response
	if got.StatusCode != 301 || got.Header.Get("Location") != "/" {
		t.Errorf("redirect: %d %v", got.StatusCode, got.Header)
	}
	if got := statusOf(fmt.Errorf("x: %w", fs.ErrPermission)); got != 403 {
		t.Errorf("permission: %d", got)
	}
	info, err := os.Stat(tree(t, map[string]string{"a": "a"}))
	if err != nil {
		t.Fatal(err)
	}
	if h := fileHeader(variant{name: "noext", encoding: "gzip"}, info, nil); h.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("twin header %v", h)
	}
}

// TestTryFilesUnsafe checks that a try_files entry that reaches outside the
// root is passed over.
func TestTryFilesUnsafe(t *testing.T) {
	s := mustLoad(t, tree(t, map[string]string{"a": "a"}), `, "try_files": ["/../a", "{path}"]`)
	if got := get(s, "GET", "/a", nil).Response; got.StatusCode != 200 || string(got.Body) != "a" {
		t.Errorf("%d %q", got.StatusCode, got.Body)
	}
}

// TestQueuedClientGone checks that a request whose client left while it
// waited for the pool is answered no further.
func TestQueuedClientGone(t *testing.T) {
	s := mustLoad(t, tree(t, map[string]string{"a": "a"}), "")
	sr := &fibhttp.StreamRequest{Request: *httptest.NewRequest("GET", "/a", nil)}
	rec := &fibtest.Recorder{}
	c := sr.Context(nil, rec, nil)
	fibhttp.Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) { c.Retain() }), c)
	sr.Cancel(errors.New("gone"))
	deadline := time.Now().Add(10 * time.Second)
	for c.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the request was not cancelled")
		}
		time.Sleep(time.Millisecond)
	}
	s.queue(c, "/a", "/a")
	rec.Wait(100 * time.Millisecond)
	if rec.Written && rec.Response.StatusCode == 200 {
		t.Errorf("served a client that left: %d", rec.Response.StatusCode)
	}
	c.Release()
}

// TestProvisionNoCwd checks a relative root when the working directory is
// gone, which filepath.Abs cannot make absolute.
func TestProvisionNoCwd(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Skip(err)
	}
	if _, err := filepath.Abs("rel"); err == nil {
		t.Skip("the system makes a path absolute without a working directory")
	}
	if _, err := load(t, `{"root": "rel"}`); err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("err %v", err)
	}
}

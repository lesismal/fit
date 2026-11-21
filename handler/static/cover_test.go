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
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/lesismal/fib/filewatch"
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
	c := cache{entries: map[string]*entry{}, maxBytes: 6, maxFile: 6, check: time.Hour}
	c.initLookups(1, 2, 1<<20)
	keep := func(name string) { c.keep(name, c.hash(name), &lookup{checked: time.Now()}) }
	for _, name := range []string{"/1", "/2", "/3"} {
		keep(name)
	}
	// The cache is full: /3, looked up once, is not kept until it is again.
	if got := c.known(); len(got) != 2 || got["/3"] != nil {
		t.Fatalf("lookups %v", got)
	}
	keep("/3")
	if got := c.known(); len(got) != 2 || got["/3"] == nil {
		t.Fatalf("lookups %v", got)
	}
	info, _ := os.Stat(full)
	if e := c.load(full, info, filewatch.StampOf(info), variant{name: "a"}); e == nil || c.bytes != 4 {
		t.Fatalf("load: %v, %d bytes", e, c.bytes)
	}
	if e := c.load(filepath.Join(root, "missing"), info, filewatch.StampOf(info), variant{name: "missing"}); e != nil {
		t.Fatalf("a missing file loaded: %v", e)
	}
	if err := os.WriteFile(full, []byte("aaaaaaaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(full)
	if e := c.load(full, info, filewatch.StampOf(info), variant{name: "a"}); e == nil || string(e.data) != "aaaaaaaa" || c.entries[full] != nil || c.bytes != 0 {
		t.Fatalf("too big: %v, %v, %d bytes", e, c.entries[full], c.bytes)
	}
}

// TestLookupBounds checks that the lookup cache is bounded by bytes as well
// as by count, and remembers no request path longer than MaxLookupPath: a
// client asking for distinct long paths that do not exist, each up to
// max_header_bytes, cannot make it hold more.
func TestLookupBounds(t *testing.T) {
	root := tree(t, map[string]string{"a": "a"})
	s := mustLoad(t, root, "")
	long := "/" + strings.Repeat("x", MaxLookupPath)
	if l := s.find(long, true); l == nil || l.status == 0 {
		t.Fatalf("find of a long path: %+v", l)
	}
	if n, size := s.cache.lookupTotals(); n != 0 || size != 0 {
		t.Fatalf("a path longer than %d kept: %d lookups, %d bytes", MaxLookupPath, n, size)
	}
	if l := s.find(long[:MaxLookupPath], true); l == nil || len(s.cache.known()) != 1 {
		t.Fatalf("a path of %d bytes not kept: %+v", MaxLookupPath, l)
	}

	const bound = 64 << 10
	c := cache{check: time.Hour}
	c.initLookups(1, 1<<20, bound)
	sh := &c.shards[0]
	for i := range 1000 {
		name := "/" + strconv.Itoa(i) + strings.Repeat("y", 1000)
		// Twice, since a full cache keeps a path the second time it is
		// looked up.
		for range 2 {
			c.keep(name, c.hash(name), &lookup{checked: time.Now(), name: name, full: "/root" + name})
		}
		var sum int64
		for k, l := range sh.lookups {
			if want := int64(3*len(k)+len("/root")) + lookupOverhead; l.size != want {
				t.Fatalf("%s counted %d bytes", k[:8], l.size)
			}
			sum += l.size
		}
		if sum != sh.lookupBytes || sh.lookupBytes > bound {
			t.Fatalf("after %d: %d bytes counted, %d held, bound %d", i, sh.lookupBytes, sum, bound)
		}
		if sh.lookups[name] == nil {
			t.Fatalf("after %d: the path looked up twice not kept", i)
		}
	}
	if n := len(sh.lookups); n == 0 || n > bound/1000 {
		t.Fatalf("%d lookups kept", n)
	}
	// Keeping a name again replaces it, counted once.
	name := "/again"
	for range 2 {
		c.keep(name, c.hash(name), &lookup{checked: time.Now()})
	}
	before := sh.lookupBytes
	c.keep(name, c.hash(name), &lookup{checked: time.Now()})
	if sh.lookupBytes != before || sh.lookups[name] == nil {
		t.Fatalf("a name kept twice: %d then %d bytes", before, sh.lookupBytes)
	}
}

// TestGatheredWrite checks a body written without a status first.
// TestCacheFits checks that only a file the cache would keep is read into
// memory: one no larger than max_file_bytes that fits in what is left,
// counting what the file's own entry gives back.
func TestCacheFits(t *testing.T) {
	c := cache{entries: map[string]*entry{"/a": {size: 4}}, bytes: 4, maxBytes: 6, maxFile: 5}
	tests := []struct {
		full string
		size int64
		want bool
	}{
		{"/b", 2, true},
		{"/b", 3, false}, // past what is left
		{"/a", 5, true},  // the file's own 4 bytes come back
		{"/a", 6, false}, // past max_file_bytes
	}
	for _, tt := range tests {
		if got := c.fits(tt.full, tt.size); got != tt.want {
			t.Errorf("fits(%s, %d) = %v, want %v", tt.full, tt.size, got, tt.want)
		}
	}
}

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
	for key, l := range s.cache.known() {
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
	root := tree(t, map[string]string{"a": "abc", "d/x": "x", "big": strings.Repeat("b", smallFile+1)})
	s := mustLoad(t, root, "")
	small, err := os.Stat(filepath.Join(root, "a"))
	if err != nil {
		t.Fatal(err)
	}
	big, err := os.Stat(filepath.Join(root, "big"))
	if err != nil {
		t.Fatal(err)
	}
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
	// Each as a small file, read whole, and as a large one, sent with
	// SendFile.
	for _, info := range []fs.FileInfo{small, big} {
		for _, tt := range tests {
			rec := with("/a", func(c *fibhttp.Context) { s.sendFromDisk(c, variant{name: "a"}, tt.full, info, tt.q) })
			// Status 0 is no answer of the handler's, which fib finishes with an empty 200.
			got := rec.Response
			if tt.status == 0 && (got.StatusCode != 200 || len(got.Body) != 0) || tt.status != 0 && got.StatusCode != tt.status {
				t.Errorf("%s as %d bytes: %v %d", tt.full, info.Size(), rec.Written, rec.Response.StatusCode)
			}
		}
	}
}

// TestSmallFromDisk checks a small file read whole: answered with its
// validators, conditional and ranged requests answered from the buffer, and
// a file whose size changed since the lookup's stat sent as it is now.
func TestSmallFromDisk(t *testing.T) {
	root := tree(t, map[string]string{"a": "abcdef", "two": "xy"})
	s := mustLoad(t, root, "")
	full := filepath.Join(root, "a")
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	serve := func(info fs.FileInfo, header map[string]string) *fibtest.Recorder {
		r := httptest.NewRequest("GET", "/a", nil)
		for k, v := range header {
			r.Header.Set(k, v)
		}
		// Queued, as a request served from the file pool is.
		q := &queued{timer: time.NewTimer(time.Hour)}
		return fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
			s.sendFromDisk(c, variant{name: "a"}, full, info, q)
			if q.state.Load() == queuedClaimed && !q.sending {
				c.Release()
			}
		}), r)
	}
	got := serve(info, nil).Response
	tag := got.Header.Get("Etag")
	if got.StatusCode != 200 || string(got.Body) != "abcdef" || tag != etag(info) || got.Header.Get("Last-Modified") == "" {
		t.Fatalf("whole: %d %q %v", got.StatusCode, got.Body, got.Header)
	}
	if got := serve(info, map[string]string{"If-None-Match": tag}).Response; got.StatusCode != 304 || len(got.Body) != 0 {
		t.Errorf("conditional: %d %q", got.StatusCode, got.Body)
	}
	if got := serve(info, map[string]string{"Range": "bytes=1-2"}).Response; got.StatusCode != 206 || string(got.Body) != "bc" {
		t.Errorf("range: %d %q", got.StatusCode, got.Body)
	}
	// A stat of two bytes: the file has six now, which goes as it is.
	stale, err := os.Stat(filepath.Join(root, "two"))
	if err != nil {
		t.Fatal(err)
	}
	if got := serve(stale, nil).Response; got.StatusCode != 200 || string(got.Body) != "abcdef" {
		t.Errorf("changed size: %d %q", got.StatusCode, got.Body)
	}
	for _, size := range []int64{5, 7} {
		if data, err := readSmall(full, size); data != nil || err != nil {
			t.Errorf("read as %d bytes: %q %v", size, data, err)
		}
	}
	if data, err := readSmall(filepath.Join(root, "missing"), 1); data != nil || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %q %v", data, err)
	}
	if data, err := readSmall(root, 1); data != nil || err == nil {
		t.Errorf("a directory: %q %v", data, err)
	}
}

// TestOffsetFileSeek checks that offsetFile seeks by arithmetic, as
// ServeContent asks it to, and refuses a position before the start.
func TestOffsetFileSeek(t *testing.T) {
	o := &offsetFile{size: 10}
	for _, tt := range []struct {
		offset int64
		whence int
		want   int64
	}{
		{0, io.SeekEnd, 10},
		{0, io.SeekStart, 0},
		{4, io.SeekStart, 4},
		{2, io.SeekCurrent, 6},
		{-3, io.SeekEnd, 7},
	} {
		if n, err := o.Seek(tt.offset, tt.whence); n != tt.want || err != nil || o.offset != tt.want {
			t.Errorf("Seek(%d, %d) = %d %v, offset %d", tt.offset, tt.whence, n, err, o.offset)
		}
	}
	if _, err := o.Seek(-11, io.SeekEnd); err == nil || o.offset != 7 {
		t.Errorf("before the start: %v, offset %d", err, o.offset)
	}
	if _, err := o.Seek(0, 3); err == nil {
		t.Error("an invalid whence taken")
	}
}

// TestStatLink checks statLink on a file, a link to it, and a path that is
// not there.
func TestStatLink(t *testing.T) {
	root := tree(t, map[string]string{"a": "abc"})
	full := filepath.Join(root, "a")
	if info, link, err := statLink(full); err != nil || link || info.Size() != 3 {
		t.Fatalf("file: %v %v %v", info, link, err)
	}
	if _, link, err := statLink(filepath.Join(root, "missing")); err == nil || link {
		t.Fatalf("missing: %v %v", link, err)
	}
	if err := os.Symlink(full, filepath.Join(root, "l")); err != nil {
		t.Skipf("no symbolic links here: %v", err)
	}
	if info, link, err := statLink(filepath.Join(root, "l")); err != nil || !link || info.Size() != 3 || !info.Mode().IsRegular() {
		t.Fatalf("link: %v %v %v", info, link, err)
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

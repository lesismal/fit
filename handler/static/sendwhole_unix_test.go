//go:build unix

package static

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

// TestRawFile checks rawFile: reads at offsets and at the end, a descriptor
// handed over once, which Close then leaves alone, Close of one never handed
// over, its RawConn, and stands.
func TestRawFile(t *testing.T) {
	root := tree(t, map[string]string{"a": "abcdef", "d/x": "x"})
	f, err := openSendable(filepath.Join(root, "a"))
	if err != nil {
		t.Fatal(err)
	}
	r := f.(*rawFile)
	p := make([]byte, 4)
	if n, err := r.ReadAt(p, 1); n != 4 || err != nil || string(p) != "bcde" {
		t.Fatalf("read at 1: %d %v %q", n, err, p[:n])
	}
	if n, err := r.ReadAt(p, 4); n != 2 || err != io.EOF || string(p[:n]) != "ef" {
		t.Fatalf("read at 4: %d %v %q", n, err, p[:n])
	}
	if n, err := r.ReadAt(p, 9); n != 0 || err != io.EOF {
		t.Fatalf("read past the end: %d %v", n, err)
	}
	if !r.stands(6) || r.stands(7) {
		t.Fatal("stands")
	}
	rc, err := r.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var seen uintptr
	if err := rc.Control(func(fd uintptr) { seen = fd }); err != nil || int(seen) != r.fd {
		t.Fatalf("Control: %v, %d of %d", err, seen, r.fd)
	}
	if rc.Read(func(uintptr) bool { return true }) == nil || rc.Write(func(uintptr) bool { return true }) == nil {
		t.Fatal("the RawConn reads or writes")
	}
	fd, ok := r.HandOverFD()
	if !ok || fd != r.fd {
		t.Fatalf("HandOverFD: %d %v", fd, ok)
	}
	if _, ok := r.HandOverFD(); ok {
		t.Fatal("handed over twice")
	}
	// Handed over, the descriptor is the connection's: Close leaves it.
	if err := r.Close(); err != nil || !r.stands(6) {
		t.Fatalf("Close after handing over: %v", err)
	}
	syscall.Close(fd)
	if r.stands(6) {
		t.Fatal("stands on a closed descriptor")
	}
	if _, err := r.ReadAt(p, 0); err == nil {
		t.Fatal("read from a closed descriptor")
	}
	// Never handed over, Close closes it.
	g, err := openSendable(filepath.Join(root, "a"))
	if err != nil {
		t.Fatal(err)
	}
	gfd := g.(*rawFile).fd
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	var st syscall.Stat_t
	if err := syscall.Fstat(gfd, &st); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("a closed rawFile's descriptor: %v", err)
	}
	// A directory does not stand for a file.
	d, err := openSendable(filepath.Join(root, "d"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.stands(0) {
		t.Fatal("a directory stands for a file")
	}
	if _, err := openSendable(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}

// TestSendWhole checks a file above 16 KiB sent whole without conditions:
// a type sniffed for a name that gives none, a file changed since the
// lookup sent as it is, a file gone answered 404, and a request the timeout
// answered first left alone.
func TestSendWhole(t *testing.T) {
	html := append([]byte("<html><body>"), bytes.Repeat([]byte("x"), smallFile)...)
	root := tree(t, map[string]string{"page": string(html), "big": string(bytes.Repeat([]byte("b"), smallFile+10))})
	s := mustLoad(t, root, `, "max_file_bytes": 4`)
	got := get(s, "GET", "/page", nil).Response
	if got.StatusCode != 200 || !bytes.Equal(got.Body, html) || got.Header.Get("Content-Type") != "text/html; charset=utf-8" ||
		got.Header.Get("Etag") == "" || got.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("whole: %d, %d bytes, %v", got.StatusCode, len(got.Body), got.Header)
	}
	full := filepath.Join(root, "big")
	stale, err := os.Stat(filepath.Join(root, "page"))
	if err != nil {
		t.Fatal(err)
	}
	// A stat of another size: the file goes as it is now.
	rec := fromPool(s, "/big", variant{name: "big"}, full, stale)
	if rec.Response.StatusCode != 200 || len(rec.Response.Body) != smallFile+10 {
		t.Fatalf("changed since the lookup: %d, %d bytes", rec.Response.StatusCode, len(rec.Response.Body))
	}
	info, err := os.Stat(full)
	if err != nil {
		t.Fatal(err)
	}
	if rec := fromPool(s, "/gone", variant{name: "gone"}, filepath.Join(root, "gone"), info); rec.Response.StatusCode != 404 {
		t.Fatalf("gone: %d", rec.Response.StatusCode)
	}
	if rec := with("/big", func(c *fibhttp.Context) { s.sendFromDisk(c, variant{name: "big"}, full, info, done(), nil) }); rec.Response.StatusCode != 200 || len(rec.Response.Body) != 0 {
		t.Fatalf("answered by the timeout: %d, %d bytes", rec.Response.StatusCode, len(rec.Response.Body))
	}
	// A twin whose name gives no type goes as application/octet-stream.
	twin := fromPool(s, "/big", variant{name: "big", encoding: "gzip"}, full, info).Response
	if twin.StatusCode != 200 || twin.Header.Get("Content-Type") != "application/octet-stream" || twin.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("twin: %d %v", twin.StatusCode, twin.Header)
	}
	// With conditions, the file goes through ServeContent as before: gone,
	// 404; a twin, with its length.
	cond := map[string]string{"If-None-Match": `"other"`}
	if rec := fromPoolWith(s, "/gone", cond, variant{name: "gone"}, filepath.Join(root, "gone"), info); rec.Response.StatusCode != 404 {
		t.Fatalf("gone, with conditions: %d", rec.Response.StatusCode)
	}
	twin = fromPoolWith(s, "/big", cond, variant{name: "big", encoding: "gzip"}, full, info).Response
	if twin.StatusCode != 200 || len(twin.Body) != smallFile+10 || twin.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("twin, with conditions: %d, %d bytes, %v", twin.StatusCode, len(twin.Body), twin.Header)
	}
}

// fromPool runs sendFromDisk as the file pool does, with a queued request,
// released once answered unless SendFile took it over.
func fromPool(s *Static, target string, v variant, full string, info fs.FileInfo) *fibtest.Recorder {
	return fromPoolWith(s, target, nil, v, full, info)
}

// fromPoolWith is fromPool for a request with header.
func fromPoolWith(s *Static, target string, header map[string]string, v variant, full string, info fs.FileInfo) *fibtest.Recorder {
	r := httptest.NewRequest("GET", target, nil)
	for k, val := range header {
		r.Header.Set(k, val)
	}
	return fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		q := &queued{}
		s.sendFromDisk(c, v, full, info, q, nil)
		if q.state.Load() == queuedClaimed && !q.sending {
			c.Release()
		}
	}), r)
}

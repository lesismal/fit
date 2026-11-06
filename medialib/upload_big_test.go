package medialib

import (
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/config"
)

// TestUploadGB uploads a file of FIT_UPLOAD_GB gibibytes, a media file and
// padding after it, cut short half way and resumed, and finds it whole and
// probed. It writes that much to the disk: it runs on the benchmark host
// alone, when FIT_UPLOAD_GB is set.
func TestUploadGB(t *testing.T) {
	gb, _ := strconv.Atoi(os.Getenv("FIT_UPLOAD_GB"))
	if gb <= 0 {
		t.Skip("FIT_UPLOAD_GB is not set")
	}
	dir, _, l := uploadLib(t, func(c *config.MediaLibrary) { c.MaxUploadBytes = int64(gb+1) << 30 })
	head, err := os.ReadFile(filepath.Join(dir, "films", "bbb.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	size := int64(gb) << 30
	// piece returns the bytes of the file at off: the media file, then a
	// pattern.
	piece := func(buf []byte, off int64) []byte {
		for i := range buf {
			p := off + int64(i)
			if p < int64(len(head)) {
				buf[i] = head[p]
			} else {
				buf[i] = 0
			}
		}
		return buf
	}
	info, err := create(t, l, "big/film.mp4", size)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.New()
	send := func(from, to int64, cut bool) result {
		c, err := l.WriteUpload(info.ID, from)
		if err != nil {
			t.Fatal(err)
		}
		h := newHolder()
		res := make(chan result, 1)
		done := func(i UploadInfo, err error) { res <- result{i, err} }
		buf := make([]byte, 64<<10)
		for off := from; off < to; {
			n := min(int64(len(buf)), to-off)
			b := piece(buf[:n], off)
			want.Write(b)
			off += n
			c.Write(b, off == to && !cut, nil, h.hold, done)
			for h.isHeld() {
				select {
				case <-h.free:
				case r := <-res:
					return r
				}
			}
		}
		if cut {
			c.Write(nil, true, io.ErrUnexpectedEOF, h.hold, done)
		}
		return <-res
	}
	r := send(0, size/2+12345, true)
	t.Logf("cut at %d of %d", r.info.Offset, size)
	if r.info.Offset != size/2+12345 {
		t.Fatalf("the cut: %+v", r)
	}
	r = send(r.info.Offset, size, false)
	if r.err != nil || !r.info.Complete {
		t.Fatalf("the rest: %+v", r)
	}
	f, err := os.Open(filepath.Join(dir, "uploads", "big", "film.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got := sha256.New()
	if n, err := io.Copy(got, f); err != nil || n != size || string(got.Sum(nil)) != string(want.Sum(nil)) {
		t.Fatalf("the file: %d bytes, %v, sums equal %v", n, err, string(got.Sum(nil)) == string(want.Sum(nil)))
	}
	idle(t, l)
	it := l.Item("uploads/big/film")
	if it == nil {
		it = rescan(t, l).Item("uploads/big/film")
	}
	if it == nil || it.Error != "" {
		t.Fatalf("the item: %+v", it)
	}
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(it.Renditions[0], 0, fn) }); err != nil {
		t.Fatal(err)
	}
}

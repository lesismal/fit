package medialib

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/config"
)

// uploadLib is a library of a copy of testdata/lib whose upload_dir is its
// uploads directory.
func uploadLib(t *testing.T, edit func(*config.MediaLibrary)) (string, *fib.Engine, *Library) {
	t.Helper()
	dir := copyLib(t)
	c := config.MediaLibrary{Name: "lib", Roots: []string{dir}, UploadDir: filepath.Join(dir, "uploads")}
	if edit != nil {
		edit(&c)
	}
	key, l := open(t, c)
	scanned(t, l, 1)
	idle(t, l)
	return dir, key, l
}

// holder is the body's hold as a test sees it: whether it is held, and a
// wait for it to be let go.
type holder struct {
	mu   sync.Mutex
	held bool
	free chan struct{}
}

func newHolder() *holder { return &holder{free: make(chan struct{}, 64)} }

func (h *holder) hold(b bool) {
	h.mu.Lock()
	h.held = b
	h.mu.Unlock()
	if !b {
		h.free <- struct{}{}
	}
}

func (h *holder) isHeld() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.held
}

// result is what a chunk's done was told.
type result struct {
	info UploadInfo
	err  error
}

// feed writes data to c in pieces of n, as fib's OnBody would, waiting
// while the body is held; it ends with fin, or with fail. It returns what
// done was told.
func feed(t *testing.T, c *Chunk, data []byte, n int, fail error) result {
	t.Helper()
	h := newHolder()
	res := make(chan result, 1)
	done := func(info UploadInfo, err error) { res <- result{info, err} }
	if len(data) == 0 && fail == nil {
		c.Write(nil, true, nil, h.hold, done)
	}
	for len(data) > 0 {
		k := min(n, len(data))
		last := k == len(data)
		c.Write(data[:k], last && fail == nil, nil, h.hold, done)
		data = data[k:]
		for h.isHeld() {
			select {
			case <-h.free:
			case r := <-res:
				return r
			case <-time.After(20 * time.Second):
				t.Fatal("held for 20s")
			}
		}
	}
	if fail != nil {
		c.Write(nil, true, fail, h.hold, done)
	}
	select {
	case r := <-res:
		return r
	case <-time.After(20 * time.Second):
		t.Fatal("no end after 20s")
	}
	return result{}
}

func create(t *testing.T, l *Library, p string, size int64) (UploadInfo, error) {
	t.Helper()
	return get(t, func(fn func(UploadInfo, error)) { l.CreateUpload(UploadSpec{Path: p, Size: size, User: "ann"}, fn) })
}

// uploadFiles lists the uploads' temporary files in dir.
func uploadFiles(t *testing.T, dir string) []string {
	t.Helper()
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), uploadPrefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestUploadResume uploads a media file in two writes, the first cut short,
// the second from where the upload is at, and finds it probed and served
// once whole.
func TestUploadResume(t *testing.T) {
	dir, _, l := uploadLib(t, nil)
	data, err := os.ReadFile(filepath.Join(dir, "films", "bbb.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	// Writes gather at least this much before going to the disk: the test
	// makes the cut land past one.
	data = append(data, make([]byte, 2*flushBytes)...)
	info, err := create(t, l, "new/film.mp4", int64(len(data)))
	if err != nil || info.Path != "uploads/new/film.mp4" || info.Root != dir || info.Offset != 0 || info.Size != int64(len(data)) || info.User != "ann" || info.Busy {
		t.Fatalf("%+v %v", info, err)
	}
	if got := uploadFiles(t, filepath.Join(dir, "uploads")); len(got) != 2 {
		t.Fatalf("the upload's files: %v", got)
	}
	if list := l.Uploads(); len(list) != 1 || list[0].ID != info.ID {
		t.Fatalf("uploads: %+v", list)
	}

	// A write cut short: what arrived is written, and the upload goes on
	// from there.
	c, err := l.WriteUpload(info.ID, 0)
	if err != nil || c.Remaining() != int64(len(data)) {
		t.Fatal(err)
	}
	if _, err := l.WriteUpload(info.ID, 0); !errors.Is(err, ErrUploadBusy) {
		t.Errorf("a second write: %v", err)
	}
	cut := flushBytes + 1000
	r := feed(t, c, data[:cut], 64<<10, io.ErrUnexpectedEOF)
	if !errors.Is(r.err, io.ErrUnexpectedEOF) || r.info.Offset != int64(cut) || r.info.Complete {
		t.Fatalf("the cut: %+v", r)
	}
	got, ok := l.Upload(info.ID)
	if !ok || got.Offset != int64(cut) || got.Busy {
		t.Fatalf("after the cut: %+v", got)
	}
	if _, err := l.WriteUpload(info.ID, 0); !errors.Is(err, ErrOffset) {
		t.Errorf("at another offset: %v", err)
	}
	if _, err := l.WriteUpload("none", 0); !errors.Is(err, ErrNoUpload) {
		t.Errorf("no such upload: %v", err)
	}
	// An empty write ends with nothing written.
	c, _ = l.WriteUpload(info.ID, int64(cut))
	if r := feed(t, c, nil, 1, nil); r.err != nil || r.info.Offset != int64(cut) {
		t.Fatalf("an empty write: %+v", r)
	}
	// The rest, in two writes.
	c, _ = l.WriteUpload(info.ID, int64(cut))
	half := cut + (len(data)-cut)/2
	if r := feed(t, c, data[cut:half], 100<<10, nil); r.err != nil || r.info.Offset != int64(half) || r.info.Complete {
		t.Fatalf("the second write: %+v", r)
	}
	c, _ = l.WriteUpload(info.ID, int64(half))
	r = feed(t, c, data[half:], 7<<10, nil)
	if r.err != nil || !r.info.Complete || r.info.Offset != int64(len(data)) {
		t.Fatalf("the last write: %+v", r)
	}
	if _, ok := l.Upload(info.ID); ok || len(l.Uploads()) != 0 {
		t.Error("the upload is still under way")
	}
	if got := uploadFiles(t, filepath.Join(dir, "uploads")); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
	whole, err := os.ReadFile(filepath.Join(dir, "uploads", "new", "film.mp4"))
	if err != nil || string(whole) != string(data) {
		t.Fatalf("the file: %d bytes, %v", len(whole), err)
	}
	// The scan the upload asked for probes it: it plays.
	idle(t, l)
	it := l.Item("uploads/new/film")
	if it == nil {
		it = rescan(t, l).Item("uploads/new/film")
	}
	if it == nil || it.Error != "" || len(it.Renditions) != 1 {
		t.Fatalf("the item: %+v", it)
	}
	if _, err := get(t, func(fn func(*mp4.Table, error)) { l.Table(it.Renditions[0], 0, fn) }); err != nil {
		t.Fatal(err)
	}
}

// TestUploadBounds refuses uploads past max_upload_bytes, max_uploads, the
// disk's free space and the upload's own size, leaving no file.
func TestUploadBounds(t *testing.T) {
	dir, _, l := uploadLib(t, func(c *config.MediaLibrary) { c.MaxUploadBytes = 1000; c.MaxUploads = 1 })
	up := filepath.Join(dir, "uploads")
	for _, c := range []struct {
		path string
		size int64
		want error
	}{
		{"big.mp4", 1001, ErrTooLarge},
		{"none.mp4", 0, ErrUploadSize},
		{"x.txt", 10, ErrNotMedia},
		{"../x.mp4", 10, ErrBadPath},
		{".x.mp4", 10, ErrBadPath},
	} {
		if _, err := create(t, l, c.path, c.size); !errors.Is(err, c.want) {
			t.Errorf("%s of %d: %v, want %v", c.path, c.size, err, c.want)
		}
	}
	if _, err := os.Stat(up); err == nil {
		t.Error("a refused upload made upload_dir")
	}
	info, err := create(t, l, "a.mp4", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create(t, l, "b.mp4", 10); !errors.Is(err, ErrTooMany) {
		t.Errorf("past max_uploads: %v", err)
	}
	// A body longer than the upload: refused, and the upload with it.
	c, _ := l.WriteUpload(info.ID, 0)
	r := feed(t, c, make([]byte, 1001), 300, nil)
	if !errors.Is(r.err, ErrTooLarge) {
		t.Fatalf("a body too long: %+v", r)
	}
	if _, ok := l.Upload(info.ID); ok {
		t.Error("the upload is still under way")
	}
	if got := uploadFiles(t, up); len(got) != 0 {
		t.Errorf("left behind: %v", got)
	}
	// A file there, or being uploaded.
	if err := os.WriteFile(filepath.Join(up, "there.mp4"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := create(t, l, "there.mp4", 10); !errors.Is(err, ErrExists) {
		t.Errorf("a file there: %v", err)
	}
	if _, err := create(t, l, "a.mp4", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := create(t, l, "a.mp4", 10); !errors.Is(err, ErrExists) {
		t.Errorf("being uploaded: %v", err)
	}
	// A file there by the time the upload is whole.
	info, _ = l.Uploads()[0], 0
	if err := os.WriteFile(filepath.Join(up, "a.mp4"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	c, _ = l.WriteUpload(info.ID, 0)
	if r := feed(t, c, make([]byte, 10), 10, nil); !errors.Is(r.err, ErrExists) {
		t.Errorf("taken meanwhile: %+v", r)
	}
	if len(l.Uploads()) != 0 || len(uploadFiles(t, up)) != 0 {
		t.Error("left behind")
	}
}

// TestUploadDisk refuses an upload the disk has no room for, beside
// min_free_bytes.
func TestUploadDisk(t *testing.T) {
	if _, ok := diskFree(t.TempDir()); !ok {
		t.Skip("the disk's free space is not known here")
	}
	_, _, l := uploadLib(t, func(c *config.MediaLibrary) { c.MinFreeBytes = 1 << 60 })
	if _, err := create(t, l, "a.mp4", 10); !errors.Is(err, ErrNoSpace) {
		t.Errorf("%v", err)
	}
	if len(l.Uploads()) != 0 {
		t.Error("kept")
	}
}

// TestUploadRefused: no upload_dir, a removed library, and an upload_dir
// that leads out of the root.
func TestUploadRefused(t *testing.T) {
	_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{copyLib(t)}})
	if _, err := create(t, l, "a.mp4", 10); !errors.Is(err, ErrNoUploads) {
		t.Errorf("no upload_dir: %v", err)
	}
	if runtime.GOOS != "windows" {
		dir := copyLib(t)
		if err := os.Symlink(t.TempDir(), filepath.Join(dir, "out")); err != nil {
			t.Fatal(err)
		}
		_, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{dir}, UploadDir: filepath.Join(dir, "out", "up")})
		if _, err := create(t, l, "a.mp4", 10); !errors.Is(err, ErrOutside) {
			t.Errorf("out of the root: %v", err)
		}
	}
	key, l := open(t, config.MediaLibrary{Name: "lib", Roots: []string{copyLib(t)}})
	Remove(key)
	if _, err := create(t, l, "a.mp4", 10); !errors.Is(err, ErrRemoved) {
		t.Errorf("removed: %v", err)
	}
	if _, err := l.WriteUpload("x", 0); !errors.Is(err, ErrRemoved) {
		t.Errorf("removed: %v", err)
	}
}

// TestUploadCancel gives an upload up, at rest and while it is written to.
func TestUploadCancel(t *testing.T) {
	dir, _, l := uploadLib(t, nil)
	up := filepath.Join(dir, "uploads")
	info, err := create(t, l, "a.mp4", 100)
	if err != nil {
		t.Fatal(err)
	}
	if err := cancelUpload(t, l, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := cancelUpload(t, l, info.ID); !errors.Is(err, ErrNoUpload) {
		t.Errorf("twice: %v", err)
	}
	waitFor(t, func() bool { return len(uploadFiles(t, up)) == 0 })

	// While a write runs: the write ends it.
	info, _ = create(t, l, "b.mp4", 100)
	c, _ := l.WriteUpload(info.ID, 0)
	h := newHolder()
	res := make(chan result, 1)
	c.Write(make([]byte, 10), false, nil, h.hold, func(i UploadInfo, err error) { res <- result{i, err} })
	if err := cancelUpload(t, l, info.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.WriteUpload(info.ID, 0); !errors.Is(err, ErrNoUpload) {
		t.Errorf("a write to one cancelled: %v", err)
	}
	c.Write(make([]byte, 90), true, nil, h.hold, func(i UploadInfo, err error) { res <- result{i, err} })
	if r := <-res; !errors.Is(r.err, ErrNoUpload) {
		t.Errorf("the write: %+v", r)
	}
	waitFor(t, func() bool { return len(uploadFiles(t, up)) == 0 && len(l.Uploads()) == 0 })
	if err := cancelUpload(t, l, "none"); !errors.Is(err, ErrNoUpload) {
		t.Errorf("none: %v", err)
	}
}

func cancelUpload(t *testing.T, l *Library, id string) error {
	t.Helper()
	ch := make(chan error, 1)
	l.CancelUpload(id, func(err error) { ch <- err })
	select {
	case err := <-ch:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("no answer")
	}
	return nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("not after 20s")
		}
	}
}

// TestUploadRestart takes an upload up again after the library is made
// anew, as after a restart, and gives up those past upload_expiry.
func TestUploadRestart(t *testing.T) {
	dir := copyLib(t)
	c := config.MediaLibrary{Name: "lib", Roots: []string{dir}, UploadDir: filepath.Join(dir, "uploads")}
	key, l := open(t, c)
	scanned(t, l, 1)
	info, err := create(t, l, "a.mp4", 100)
	if err != nil {
		t.Fatal(err)
	}
	ch, _ := l.WriteUpload(info.ID, 0)
	if r := feed(t, ch, make([]byte, 40), 40, io.ErrUnexpectedEOF); r.info.Offset != 40 {
		t.Fatalf("%+v", r)
	}
	stale, _ := create(t, l, "b.mp4", 100)
	Remove(key)
	<-l.done
	l.Pool().Wait()
	// Files of another upload left behind, and an old one's.
	up := filepath.Join(dir, "uploads")
	for _, n := range []string{"x" + uploadPart, "y" + uploadMeta, "z.other"} {
		if err := os.WriteFile(filepath.Join(up, uploadPrefix+n), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, n := range []string{"x" + uploadPart, stale.ID + uploadPart, stale.ID + uploadMeta} {
		if err := os.Chtimes(filepath.Join(up, uploadPrefix+n), old, old); err != nil {
			t.Fatal(err)
		}
	}

	_, l = open(t, c)
	scanned(t, l, 1)
	got, ok := l.Upload(info.ID)
	if !ok || got.Offset != 40 || got.Size != 100 || got.User != "ann" || got.Path != "uploads/a.mp4" || len(l.Uploads()) != 1 {
		t.Fatalf("taken up again: %+v %v", l.Uploads(), ok)
	}
	files := strings.Join(uploadFiles(t, up), " ")
	if strings.Contains(files, stale.ID) || strings.Contains(files, "x.part") || !strings.Contains(files, "y.json") || !strings.Contains(files, "z.other") {
		t.Errorf("left: %s", files)
	}
	ch, err = l.WriteUpload(info.ID, 40)
	if err != nil {
		t.Fatal(err)
	}
	if r := feed(t, ch, make([]byte, 60), 60, nil); r.err != nil || !r.info.Complete {
		t.Fatalf("%+v", r)
	}

	// Not written to for upload_expiry: given up at the next sweep.
	info, _ = create(t, l, "c.mp4", 100)
	u := l.lookupUpload(info.ID)
	u.mu.Lock()
	u.updated = time.Now().Add(-48 * time.Hour)
	u.mu.Unlock()
	l.sweepUploads(l.set.Load().up)
	if _, ok := l.Upload(info.ID); ok || strings.Contains(strings.Join(uploadFiles(t, up), " "), info.ID) {
		t.Error("an upload past upload_expiry is kept")
	}
	// An upload_dir no longer the library's: dropped from memory, kept on
	// disk.
	info, _ = create(t, l, "d.mp4", 100)
	l.sweepUploads(nil)
	if _, ok := l.Upload(info.ID); ok || !strings.Contains(strings.Join(uploadFiles(t, up), " "), info.ID) {
		t.Error("dropping the upload_dir")
	}
	l.sweepUploads(l.set.Load().up)
	if _, ok := l.Upload(info.ID); !ok {
		t.Error("not taken up again")
	}
}

// TestUploadBusyPool: a write the pool does not take ends with ErrBusy,
// and the upload goes on.
func TestUploadBusyPool(t *testing.T) {
	_, _, l := uploadLib(t, nil)
	info, err := create(t, l, "a.mp4", 100)
	if err != nil {
		t.Fatal(err)
	}
	l.pool.Load().Close()
	c, _ := l.WriteUpload(info.ID, 0)
	if r := feed(t, c, make([]byte, 10), 10, nil); !errors.Is(r.err, ErrBusy) {
		t.Errorf("%+v", r)
	}
	if got, ok := l.Upload(info.ID); !ok || got.Busy {
		t.Errorf("%+v", got)
	}
	if _, err := create(t, l, "b.mp4", 10); !errors.Is(err, ErrBusy) {
		t.Errorf("create: %v", err)
	}
}

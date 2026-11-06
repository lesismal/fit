package medialib

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/lesismal/fib/bufferpool"
)

// The errors of uploads.
var (
	// ErrNoUploads is the error of an upload to a library without
	// upload_dir.
	ErrNoUploads = errors.New("medialib: the library takes no uploads: it has no upload_dir")
	// ErrNoUpload is the error of an upload that is not under way: never
	// begun, finished, cancelled, expired, or given up after an error.
	ErrNoUpload = errors.New("medialib: no such upload under way")
	// ErrUploadBusy is the error of a write to an upload that another
	// write is still writing to.
	ErrUploadBusy = errors.New("medialib: another request writes to the upload")
	// ErrOffset is the error of a write that does not start where the
	// upload is at.
	ErrOffset = errors.New("medialib: the write does not start where the upload is at")
	// ErrTooMany is the error of an upload past max_uploads under way.
	ErrTooMany = errors.New("medialib: max_uploads uploads are under way")
	// ErrNoSpace is the error of an upload that would leave less than
	// min_free_bytes free on its disk, or that found the disk full.
	ErrNoSpace = errors.New("medialib: not enough disk space")
	// ErrUploadSize is the error of an upload of no bytes.
	ErrUploadSize = errors.New("medialib: an upload's size is 1 byte or more")
)

// The names of an upload's files in upload_dir, which a scan, looking at no
// name that starts with '.', leaves alone.
const (
	uploadPrefix = ".fit-upload-"
	uploadPart   = ".part"
	uploadMeta   = ".json"
	// maxUploadMeta bounds an upload's .json read back after a restart.
	maxUploadMeta = 4 << 10
)

// The bounds of a write: what of a body is gathered before it is written,
// on the pool, while the body waits.
const (
	flushBytes = 512 << 10
	chunkBuf   = 1 << 20
)

// UploadSpec is an upload to begin: its path under upload_dir, as a slash
// path, its size in bytes, and who asks.
type UploadSpec struct {
	Path string
	Size int64
	User string
}

// UploadInfo is an upload under way, or the one just finished (Complete).
type UploadInfo struct {
	ID string `json:"id"`
	// Root and Path are where its file goes, Path under the root.
	Root string `json:"root"`
	Path string `json:"path"`
	// Size is its bytes, and Offset those written.
	Size     int64     `json:"size"`
	Offset   int64     `json:"offset"`
	User     string    `json:"user,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Busy     bool      `json:"busy"`
	Complete bool      `json:"complete,omitempty"`
}

// upload is an upload under way.
type upload struct {
	mu      sync.Mutex
	id      string
	root    *root
	rel     string // the file's path under the root once whole
	dir     string // upload_dir, which its .part and .json are in
	size    int64
	offset  int64
	user    string
	created time.Time
	updated time.Time
	busy    bool // being made, or written to
	gone    bool // cancelled or ended while busy: removed once not
	f       *os.File
}

func (u *upload) part() string { return filepath.Join(u.dir, uploadPrefix+u.id+uploadPart) }
func (u *upload) meta() string { return filepath.Join(u.dir, uploadPrefix+u.id+uploadMeta) }

// info returns what u is; u.mu is held.
func (u *upload) info() UploadInfo {
	return UploadInfo{ID: u.id, Root: u.root.conf, Path: u.rel, Size: u.size, Offset: u.offset, User: u.user,
		Created: u.created, Updated: u.updated, Busy: u.busy}
}

// uploads are a library's uploads under way.
type uploads struct {
	mu sync.Mutex
	m  map[string]*upload
	// loaded is the upload_dir whose uploads were read back, after a
	// restart.
	loaded string
}

// uploadMetaFile is an upload's .json, which keeps it across restarts.
type uploadMetaFile struct {
	ID      string    `json:"id"`
	Root    string    `json:"root"`
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	User    string    `json:"user,omitempty"`
	Created time.Time `json:"created"`
}

// UploadSettings is a library's upload_dir and the bounds of its uploads.
type UploadSettings struct {
	// Root is the root upload_dir lies in, as the config writes it, and
	// Dir its path under the root, "" for the root itself.
	Root     string `json:"root"`
	Dir      string `json:"dir"`
	MaxBytes int64  `json:"max_bytes"`
	Max      int    `json:"max_uploads"`
	MinFree  int64  `json:"min_free_bytes"`
	// Expiry is upload_expiry, in seconds.
	Expiry float64 `json:"expiry"`
}

// UploadSettings returns the library's upload_dir and its bounds, or nil
// when it takes no uploads.
func (l *Library) UploadSettings() *UploadSettings {
	up := l.set.Load().up
	if up == nil {
		return nil
	}
	return &UploadSettings{Root: up.root.conf, Dir: up.rel, MaxBytes: up.maxBytes, Max: up.max, MinFree: up.minFree, Expiry: up.expiry.Seconds()}
}

// Uploads returns the uploads under way, the oldest first.
func (l *Library) Uploads() []UploadInfo {
	l.uploads.mu.Lock()
	list := make([]*upload, 0, len(l.uploads.m))
	for _, u := range l.uploads.m {
		list = append(list, u)
	}
	l.uploads.mu.Unlock()
	out := make([]UploadInfo, 0, len(list))
	for _, u := range list {
		u.mu.Lock()
		if !u.gone {
			out = append(out, u.info())
		}
		u.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b UploadInfo) int { return a.Created.Compare(b.Created) })
	return out
}

// Upload returns the upload id under way, and false when there is none.
func (l *Library) Upload(id string) (UploadInfo, bool) {
	u := l.lookupUpload(id)
	if u == nil {
		return UploadInfo{}, false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.info(), !u.gone
}

func (l *Library) lookupUpload(id string) *upload {
	l.uploads.mu.Lock()
	defer l.uploads.mu.Unlock()
	return l.uploads.m[id]
}

// CreateUpload begins an upload of spec.Size bytes to spec.Path under
// upload_dir: an empty temporary file in upload_dir, and its .json, which
// keeps it across restarts. It is refused with ErrNoUploads without
// upload_dir, ErrBadPath or ErrNotMedia for a path the library does not
// write or look at, ErrUploadSize or ErrTooLarge for a size of no bytes or
// past max_upload_bytes, ErrTooMany past max_uploads, ErrExists for a file
// there already or being uploaded, ErrOutside for a path whose directory
// leads out of the root, and ErrNoSpace when the disk would be left with
// less than min_free_bytes besides what the uploads under way still need.
// It runs on the library's pool, and calls fn there, or at once with an
// error found before, as Open does.
func (l *Library) CreateUpload(spec UploadSpec, fn func(UploadInfo, error)) {
	s := l.set.Load()
	up := s.up
	rel, err := cleanRel(spec.Path, false)
	switch {
	case l.removed.Load():
		err = ErrRemoved
	case up == nil:
		err = ErrNoUploads
	case err != nil:
	case kindOfName(rel) == kindNone:
		err = ErrNotMedia
	case spec.Size <= 0:
		err = ErrUploadSize
	case spec.Size > up.maxBytes:
		err = fmt.Errorf("%w: %d bytes, past max_upload_bytes, %d", ErrTooLarge, spec.Size, up.maxBytes)
	}
	if err != nil {
		fn(UploadInfo{}, err)
		return
	}
	rel = path.Join(up.rel, rel)
	var id [16]byte
	_, _ = rand.Read(id[:])
	now := time.Now()
	u := &upload{id: hex.EncodeToString(id[:]), root: up.root, rel: rel, dir: up.abs, size: spec.Size, user: spec.User,
		created: now, updated: now, busy: true}
	// The upload takes its place at once, so that max_uploads and a path
	// taken hold for those asked for together; what the others still
	// need of the disk is reckoned now.
	var need int64
	l.uploads.mu.Lock()
	n := 0
	for _, o := range l.uploads.m {
		o.mu.Lock()
		if !o.gone {
			n++
			need += o.size - o.offset
			if o.rel == rel && o.root == u.root {
				err = fmt.Errorf("%w: %s is being uploaded", ErrExists, rel)
			}
		}
		o.mu.Unlock()
	}
	if err == nil && n >= up.max {
		err = fmt.Errorf("%w: %d", ErrTooMany, up.max)
	}
	if err == nil {
		l.uploads.m[u.id] = u
	}
	l.uploads.mu.Unlock()
	if err != nil {
		fn(UploadInfo{}, err)
		return
	}
	fail := func(err error) {
		l.dropUpload(u)
		fn(UploadInfo{}, err)
	}
	l.async(func() {
		if err := l.makeUpload(u, need+up.minFree); err != nil {
			_ = os.Remove(u.part())
			_ = os.Remove(u.meta())
			fail(err)
			return
		}
		u.mu.Lock()
		u.busy = false
		info := u.info()
		u.mu.Unlock()
		fn(info, nil)
	}, fail)
}

// makeUpload makes u's files, leaving free bytes free besides u. It blocks.
func (l *Library) makeUpload(u *upload, free int64) error {
	abs, err := writable(u.root, u.rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("%w: %s", ErrExists, u.rel)
	}
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return err
	}
	if have, ok := diskFree(u.dir); ok && int64(min(have, 1<<62)) < u.size+free {
		return fmt.Errorf("%w: %d bytes free, %d needed besides min_free_bytes and the uploads under way", ErrNoSpace, have, u.size)
	}
	f, err := os.OpenFile(u.part(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	u.f = f
	data, _ := json.Marshal(uploadMetaFile{ID: u.id, Root: u.root.conf, Path: u.rel, Size: u.size, User: u.user, Created: u.created})
	if err := writeAtomic(u.meta(), data); err != nil {
		f.Close()
		u.f = nil
		return err
	}
	return nil
}

// dropUpload takes u out of the uploads under way.
func (l *Library) dropUpload(u *upload) {
	l.uploads.mu.Lock()
	if l.uploads.m[u.id] == u {
		delete(l.uploads.m, u.id)
	}
	l.uploads.mu.Unlock()
}

// removeUpload removes u's files and takes it out of the uploads under way.
// It blocks.
func (l *Library) removeUpload(u *upload) {
	u.mu.Lock()
	u.gone = true
	f := u.f
	u.f = nil
	u.mu.Unlock()
	if f != nil {
		f.Close()
	}
	_ = os.Remove(u.part())
	_ = os.Remove(u.meta())
	l.dropUpload(u)
}

// CancelUpload gives the upload id up, and removes its temporary file, on
// the pool; one being written to is removed once its write ends. fn is
// called as Open does.
func (l *Library) CancelUpload(id string, fn func(error)) {
	u := l.lookupUpload(id)
	if u == nil {
		fn(ErrNoUpload)
		return
	}
	u.mu.Lock()
	gone, busy := u.gone, u.busy
	u.gone = true
	u.mu.Unlock()
	switch {
	case gone:
		fn(ErrNoUpload)
	case busy:
		// The write ends it.
		fn(nil)
	default:
		l.async(func() { l.removeUpload(u); fn(nil) }, func(err error) {
			// The next scan's sweep removes it.
			fn(nil)
		})
	}
}

// Chunk is a write to an upload: a request's body, from where the upload is
// at. Its bytes are gathered as they come, and written on the library's
// pool, 512 KiB at a time, while the body is held.
type Chunk struct {
	l     *Library
	u     *upload
	mu    sync.Mutex
	buf   []byte
	off   int64 // where buf goes in the file
	busy  bool  // a write runs on the pool
	ended bool
	// fin and err are the body's end, come while a write ran.
	fin  bool
	err  error
	hold func(bool)
	done func(UploadInfo, error)
}

// WriteUpload begins a write to the upload id at offset, which must be
// where it is at, and returns the Chunk to give its bytes to: ErrNoUpload,
// ErrUploadBusy while another write runs, ErrOffset for another offset. It
// never blocks.
func (l *Library) WriteUpload(id string, offset int64) (*Chunk, error) {
	if l.removed.Load() {
		return nil, ErrRemoved
	}
	u := l.lookupUpload(id)
	if u == nil {
		return nil, ErrNoUpload
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.gone:
		return nil, ErrNoUpload
	case u.busy:
		return nil, ErrUploadBusy
	case offset != u.offset:
		return nil, fmt.Errorf("%w: it is at %d", ErrOffset, u.offset)
	}
	u.busy = true
	return &Chunk{l: l, u: u, off: offset}, nil
}

// Abort ends a write before its body, with err, and gives the upload up,
// its file removed: for a body refused before it is read, as one longer
// than the upload. done is not called.
func (c *Chunk) Abort(err error) { c.fail(err, true) }

// Remaining is what the upload still takes from the write's offset.
func (c *Chunk) Remaining() int64 { return c.u.size - c.off }

// Info is the upload as the write found it.
func (c *Chunk) Info() UploadInfo {
	c.u.mu.Lock()
	defer c.u.mu.Unlock()
	return c.u.info()
}

// Write takes a piece of the body, as fib's OnBody hands it: data, valid
// only during the call, fin for the last, and err for a body that ended
// without it. What is gathered is written on the pool, with hold(true)
// before and hold(false) after, so that the caller holds the body while it
// is written; hold(false) is not called after the end. done is called once,
// at the end: with the upload, Complete once whole and renamed into place,
// or with the error: ErrTooLarge for a body past the upload's size, which
// gives the upload up and removes its file, ErrNoSpace for a full disk,
// which does too, ErrBusy when the pool does not take the write, and the
// body's own error, after which the upload goes on from what was written.
// Write never blocks; done may be called on the caller's goroutine or the
// pool's.
func (c *Chunk) Write(data []byte, fin bool, err error, hold func(bool), done func(UploadInfo, error)) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.hold, c.done = hold, done
	if c.busy {
		// A failure comes while the body is held: the write in flight
		// ends the chunk.
		c.fin, c.err = c.fin || fin, cmpErr(c.err, err)
		c.mu.Unlock()
		return
	}
	if c.off+int64(len(c.buf))+int64(len(data)) > c.u.size {
		c.mu.Unlock()
		c.fail(fmt.Errorf("%w: the body is longer than the upload's %d bytes", ErrTooLarge, c.u.size), true)
		return
	}
	if len(data) > 0 {
		if c.buf == nil {
			c.buf = bufferpool.Get(chunkBuf)[:0]
		}
		c.buf = bufferpool.Append(c.buf, data)
	}
	last := fin || err != nil
	if !last && len(c.buf) < flushBytes {
		c.mu.Unlock()
		return
	}
	c.fin, c.err = fin, err
	c.busy = true
	c.mu.Unlock()
	if !last {
		hold(true)
	}
	if !c.l.Submit(c.flush, func() { c.fail(ErrBusy, false) }) {
		c.fail(ErrBusy, false)
	}
}

func cmpErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// flush writes what c gathered, on the pool, and then ends c, or lets the
// body go on.
func (c *Chunk) flush() {
	u := c.u
	c.mu.Lock()
	buf := c.buf
	c.mu.Unlock()
	if err := c.writeAt(buf); err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			c.fail(fmt.Errorf("%w: %v", ErrNoSpace, err), true)
		} else {
			c.fail(err, false)
		}
		return
	}
	c.mu.Lock()
	c.off += int64(len(buf))
	c.buf = c.buf[:0]
	c.busy = false
	fin, err := c.fin, c.err
	c.mu.Unlock()
	u.mu.Lock()
	u.offset, u.updated = c.off, time.Now()
	gone := u.gone
	u.mu.Unlock()
	switch {
	case gone:
		c.fail(ErrNoUpload, true)
	case err != nil:
		c.fail(err, false)
	case fin && c.off == u.size:
		c.finish()
	case fin:
		c.end(c.Info(), nil)
	default:
		c.hold(false)
	}
}

// writeAt writes buf at c.off to the upload's file, opened when it is not
// yet, as after a restart. It blocks.
func (c *Chunk) writeAt(buf []byte) error {
	u := c.u
	u.mu.Lock()
	f := u.f
	u.mu.Unlock()
	if f == nil {
		var err error
		if f, err = os.OpenFile(u.part(), os.O_WRONLY, 0); err != nil {
			return err
		}
		if fi, err := f.Stat(); err != nil || fi.Size() < c.off {
			f.Close()
			return fmt.Errorf("%w: its temporary file is shorter than the upload's offset", ErrChanged)
		}
		u.mu.Lock()
		u.f = f
		u.mu.Unlock()
	}
	if len(buf) == 0 {
		return nil
	}
	_, err := f.WriteAt(buf, c.off)
	return err
}

// finish renames the whole upload into place and asks for a scan, which
// probes it. It blocks.
func (c *Chunk) finish() {
	u, l := c.u, c.l
	l.fileMu.Lock()
	err := l.place(u)
	l.fileMu.Unlock()
	if err != nil {
		c.fail(err, true)
		return
	}
	u.mu.Lock()
	info := u.info()
	u.busy, u.gone = false, true
	u.mu.Unlock()
	l.dropUpload(u)
	info.Busy, info.Complete = false, true
	l.Rescan()
	c.end(info, nil)
}

// place renames u's whole file into place. It blocks.
func (l *Library) place(u *upload) error {
	u.mu.Lock()
	f := u.f
	u.f = nil
	u.mu.Unlock()
	if f != nil {
		err := f.Sync()
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	abs, err := writable(u.root, u.rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(abs); err == nil {
		return fmt.Errorf("%w: %s", ErrExists, u.rel)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.Rename(u.part(), abs); err != nil {
		return err
	}
	_ = os.Remove(u.meta())
	return nil
}

// fail ends c with err; remove gives the upload up, its file removed.
func (c *Chunk) fail(err error, remove bool) {
	if remove {
		info := c.Info()
		c.l.removeUpload(c.u)
		c.end(info, err)
		return
	}
	c.end(c.Info(), err)
}

// end ends c, once: the upload is free for the next write, or removed if
// cancelled meanwhile, and done is told.
func (c *Chunk) end(info UploadInfo, err error) {
	c.mu.Lock()
	if c.ended {
		c.mu.Unlock()
		return
	}
	c.ended = true
	done := c.done
	if c.buf != nil {
		bufferpool.Put(c.buf)
		c.buf = nil
	}
	c.mu.Unlock()
	u := c.u
	u.mu.Lock()
	u.busy = false
	gone := u.gone && !info.Complete
	u.mu.Unlock()
	if gone && !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrNoSpace) {
		// Cancelled while it was written to.
		c.l.removeUploadLater(u)
		if err == nil {
			err = ErrNoUpload
		}
	}
	info.Busy = false
	if done != nil {
		done(info, err)
	}
}

// removeUploadLater removes u on the pool, or leaves it to the next sweep.
func (l *Library) removeUploadLater(u *upload) {
	if !l.Submit(func() { l.removeUpload(u) }, nil) {
		l.dropUpload(u)
	}
}

// closeUploads closes the uploads' files, as the library is removed; their
// files stay, to be taken up again with the library.
func (l *Library) closeUploads() {
	l.uploads.mu.Lock()
	defer l.uploads.mu.Unlock()
	for _, u := range l.uploads.m {
		u.mu.Lock()
		if u.f != nil && !u.busy {
			u.f.Close()
			u.f = nil
		}
		u.mu.Unlock()
	}
}

// sweepUploads, at the start of each scan, on the scanner's goroutine,
// reads back the uploads of up left by a restart, gives up those not
// written to for upload_expiry, and drops from memory those of an
// upload_dir no longer the library's. It blocks.
func (l *Library) sweepUploads(up *uploadDir) {
	now := time.Now()
	var stale []*upload
	l.uploads.mu.Lock()
	for _, u := range l.uploads.m {
		u.mu.Lock()
		switch {
		case u.busy:
		case up == nil || u.dir != up.abs || u.root != up.root:
			// Kept on disk, to be taken up again with its upload_dir.
			if u.f != nil {
				u.f.Close()
				u.f = nil
			}
			delete(l.uploads.m, u.id)
		case now.Sub(u.updated) > up.expiry:
			stale = append(stale, u)
		}
		u.mu.Unlock()
	}
	load := up != nil && l.uploads.loaded != up.abs
	if up == nil {
		l.uploads.loaded = ""
	} else {
		l.uploads.loaded = up.abs
	}
	l.uploads.mu.Unlock()
	for _, u := range stale {
		l.removeUpload(u)
	}
	if up != nil {
		l.readUploads(up, load, now)
	}
}

// readUploads reads upload_dir for uploads' files: those of uploads not
// under way, when load is set, taken up again, and those left behind, older
// than upload_expiry, removed. It blocks.
func (l *Library) readUploads(up *uploadDir, load bool, now time.Time) {
	d, err := os.Open(up.abs)
	if err != nil {
		return
	}
	defer d.Close()
	var metas []uploadMetaFile
	stale := map[string]bool{}
	for {
		batch, err := d.ReadDir(readDirBatch)
		for _, e := range batch {
			name := e.Name()
			id, ok := strings.CutPrefix(name, uploadPrefix)
			if !ok || !e.Type().IsRegular() {
				continue
			}
			id, isMeta := strings.CutSuffix(id, uploadMeta)
			if !isMeta {
				var isPart bool
				if id, isPart = strings.CutSuffix(id, uploadPart); !isPart {
					continue
				}
			}
			if l.lookupUpload(id) != nil {
				continue
			}
			if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > up.expiry {
				stale[id] = true
			}
			if isMeta && load {
				if m, ok := readUploadMeta(filepath.Join(up.abs, name), id); ok {
					metas = append(metas, m)
				}
			}
		}
		if err != nil || len(batch) == 0 {
			break
		}
	}
	slices.SortFunc(metas, func(a, b uploadMetaFile) int { return a.Created.Compare(b.Created) })
	for _, m := range metas {
		if stale[m.ID] {
			continue
		}
		fi, err := os.Stat(filepath.Join(up.abs, uploadPrefix+m.ID+uploadPart))
		rel, rerr := cleanRel(m.Path, false)
		if err != nil || rerr != nil || m.Root != up.root.conf || up.rel != "" && !strings.HasPrefix(rel, up.rel+"/") ||
			m.Size <= 0 || fi.Size() > m.Size {
			stale[m.ID] = true
			continue
		}
		u := &upload{id: m.ID, root: up.root, rel: rel, dir: up.abs, size: m.Size, offset: fi.Size(), user: m.User,
			created: m.Created, updated: fi.ModTime()}
		l.uploads.mu.Lock()
		if len(l.uploads.m) < up.max {
			l.uploads.m[u.id] = u
		} else {
			stale[m.ID] = true
		}
		l.uploads.mu.Unlock()
	}
	for id := range stale {
		_ = os.Remove(filepath.Join(up.abs, uploadPrefix+id+uploadPart))
		_ = os.Remove(filepath.Join(up.abs, uploadPrefix+id+uploadMeta))
	}
}

// readUploadMeta reads an upload's .json, which must be id's.
func readUploadMeta(p, id string) (uploadMetaFile, bool) {
	var m uploadMetaFile
	f, err := os.Open(p)
	if err != nil {
		return m, false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxUploadMeta+1))
	if err != nil || len(data) > maxUploadMeta || json.Unmarshal(data, &m) != nil || m.ID != id {
		return m, false
	}
	return m, true
}

package bittorrent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// watch_dir: .torrent and .magnet files dropped into it, or into a
// subdirectory named after a category, are added, then moved into the
// .done or .failed subdirectory beside them. A file is taken once it has
// kept its size and modification time across two looks, so that one still
// being written is not read half way. A look takes at most watch_max_files
// files, and reads at most watchEntriesPerFile directory entries for each,
// so that a huge directory costs a bounded look. It runs on the role's
// manager goroutine, without r.mu but while adding.

const (
	// watchDone and watchFailed are the subdirectories handled files are
	// moved into.
	watchDone   = ".done"
	watchFailed = ".failed"
	// watchEntriesPerFile bounds the directory entries a look reads for
	// each file it may take.
	watchEntriesPerFile = 16
)

// stamp is a file's size and modification time.
type stamp struct {
	size  int64
	mtime time.Time
}

// watchJob is a look at watch_dir, taken from the config under r.mu.
type watchJob struct {
	dir  string
	max  int
	cats []string
}

// watchJobLocked returns the look the config asks for. r.mu is held.
func (r *Role) watchJobLocked() *watchJob {
	j := &watchJob{dir: r.cfg.WatchDir, max: r.cfg.WatchFiles()}
	for _, c := range r.cfg.Categories {
		j.cats = append(j.cats, c.Name)
	}
	return j
}

// watchFile is a file a look may take.
type watchFile struct {
	path     string
	dir      string
	category string
	magnet   bool
	st       stamp
}

// runWatch looks at watch_dir once.
func (r *Role) runWatch(j *watchJob) {
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if fi, err := os.Stat(j.dir); err != nil || !fi.IsDir() {
		msg := fmt.Sprintf("watch_dir %s cannot be read: %v", j.dir, err)
		if err == nil {
			msg = fmt.Sprintf("watch_dir %s is not a directory", j.dir)
		}
		if msg != r.watchErr && r.log != nil {
			r.log.Errorf("%s", msg)
		}
		r.watchErr = msg
		return
	}
	r.watchErr = ""
	files := listWatch(j.dir, "", j.max)
	for _, c := range j.cats {
		if len(files) >= j.max {
			break
		}
		sub := filepath.Join(j.dir, c)
		if fi, err := os.Stat(sub); err == nil && fi.IsDir() {
			files = append(files, listWatch(sub, c, j.max-len(files))...)
		}
	}
	seen := make(map[string]stamp, len(files))
	for _, f := range files {
		seen[f.path] = f.st
		if prev, ok := r.watchSeen[f.path]; !ok || prev != f.st {
			continue // changed since the last look, or new: the next look
		}
		r.takeWatched(f)
		if _, err := os.Lstat(f.path); err != nil {
			delete(seen, f.path) // moved aside
		}
	}
	r.watchSeen = seen
	for p := range r.watchLast {
		if _, ok := seen[p]; !ok {
			delete(r.watchLast, p)
		}
	}
}

// listWatch returns at most n .torrent and .magnet files of dir, of
// category cat.
func listWatch(dir, cat string, n int) []watchFile {
	if n <= 0 {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer d.Close()
	var out []watchFile
	budget := n * watchEntriesPerFile
	for budget > 0 && len(out) < n {
		ents, err := d.ReadDir(min(budget, 256))
		budget -= len(ents)
		for _, e := range ents {
			name := e.Name()
			lower := strings.ToLower(name)
			magnet := strings.HasSuffix(lower, ".magnet")
			if strings.HasPrefix(name, ".") || !e.Type().IsRegular() || !magnet && !strings.HasSuffix(lower, ".torrent") {
				continue
			}
			fi, err := e.Info()
			if err != nil {
				continue
			}
			out = append(out, watchFile{path: filepath.Join(dir, name), dir: dir, category: cat, magnet: magnet,
				st: stamp{fi.Size(), fi.ModTime()}})
			if len(out) == n {
				break
			}
		}
		if err != nil {
			break // io.EOF, or a directory that cannot be read further
		}
	}
	return out
}

// takeWatched adds the torrent of f, and moves f aside: into .done when it
// is added or already held, into .failed when it cannot be, beside a .error
// file; it is left when it may be added later.
func (r *Role) takeWatched(f watchFile) {
	req := AddRequest{Category: f.category}
	var err error
	bound := int64(MaxTorrentBytes)
	if f.magnet {
		bound = MaxMagnetBytes
	}
	data, rerr := readBounded(f.path, bound)
	switch {
	case rerr != nil:
		err = fmt.Errorf("%w: %v", ErrBadRequest, rerr)
	case f.magnet:
		req.Magnet = firstLine(data)
		if req.Magnet == "" {
			err = fmt.Errorf("%w: no magnet link in it", ErrBadRequest)
		}
	default:
		req.Torrent = data
	}
	if err == nil {
		_, err = r.Add(req)
	}
	switch {
	case err == nil || errors.Is(err, ErrExists):
		if err != nil && r.log != nil {
			r.log.Infof("watch_dir: %s: the torrent is already held", f.path)
		} else if r.log != nil {
			r.log.Infof("watch_dir: %s added", f.path)
		}
		if merr := moveAside(f, watchDone, ""); merr != nil && r.log != nil {
			r.log.Errorf("watch_dir: %s was added but cannot be moved into %s: %v", f.path, watchDone, merr)
		}
	case errors.Is(err, ErrBadRequest):
		if r.log != nil {
			r.log.Warnf("watch_dir: %s cannot be added: %v", f.path, err)
		}
		if merr := moveAside(f, watchFailed, err.Error()); merr != nil && r.log != nil {
			r.log.Errorf("watch_dir: %s cannot be moved into %s: %v", f.path, watchFailed, merr)
		}
	default:
		// Later: max_torrents, the state not written, the client down.
		if msg := err.Error(); r.watchLast[f.path] != msg {
			r.watchLast[f.path] = msg
			if r.log != nil {
				r.log.Warnf("watch_dir: %s not added yet, tried again at each look: %v", f.path, err)
			}
		}
	}
}

// firstLine returns the first line of data that is not blank, trimmed.
func firstLine(data []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), MaxMagnetBytes+1)
	for sc.Scan() {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			return strings.TrimPrefix(s, "\uFEFF")
		}
	}
	return ""
}

// moveAside moves f into its directory's sub, with a name of its own there,
// and writes msg beside it as <name>.error when msg is not empty.
func moveAside(f watchFile, sub, msg string) error {
	dir := filepath.Join(f.dir, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	base := filepath.Base(f.path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	dst := filepath.Join(dir, base)
	for i := 1; ; i++ {
		if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		if i > 1000 {
			dst = filepath.Join(dir, stem+"-"+strconv.FormatInt(time.Now().UnixNano(), 10)+ext)
			break
		}
		dst = filepath.Join(dir, stem+"-"+strconv.Itoa(i)+ext)
	}
	if err := os.Rename(f.path, dst); err != nil {
		return err
	}
	if msg != "" {
		return writeAtomic(dir, filepath.Base(dst)+".error", []byte(msg+"\n"))
	}
	return nil
}

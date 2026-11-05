package dotfile

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// A file system that folds case (Windows' NTFS, macOS' APFS and HFS+ as
// they are set up by default) opens x.php for a request for /x.PHP: a route
// that matches \.php$ misses it, and a static handler on the same root
// would serve the script's source as text. FoldsCase tells such a root once,
// when a handler is provisioned; OnDisk and Script tell the names a handler
// on one refuses.

// foldsByDefault is what FoldsCase says of a root it cannot tell by its
// names: the systems whose file systems fold case unless set up otherwise.
var foldsByDefault = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// FoldsCase reports whether the file system root is on opens a file for a
// name that differs from the file's own only in the case of its letters.
// It looks at a name in root, or else at the names on root's path, and
// stats it with the case of its letters swapped; with no such name to look
// at, it says what the system's file systems do by default.
func FoldsCase(root string) bool {
	if f, err := os.Open(root); err == nil {
		names, _ := f.Readdirnames(64)
		_ = f.Close()
		for _, name := range names {
			if folds, ok := foldsAt(root, name); ok {
				return folds
			}
		}
	}
	for dir := filepath.Clean(root); ; {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		if folds, ok := foldsAt(parent, filepath.Base(dir)); ok {
			return folds
		}
		dir = parent
	}
	return foldsByDefault
}

// foldsAt reports whether name in dir is the same file as name with the
// case of its letters swapped; ok is false when name has no ASCII letter,
// or cannot be stat'ed.
func foldsAt(dir, name string) (folds, ok bool) {
	swapped := strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z':
			return r - 'a' + 'A'
		case 'A' <= r && r <= 'Z':
			return r - 'A' + 'a'
		}
		return r
	}, name)
	if swapped == name {
		return false, false
	}
	a, err := os.Lstat(filepath.Join(dir, name))
	if err != nil {
		return false, false
	}
	b, err := os.Lstat(filepath.Join(dir, swapped))
	return err == nil && os.SameFile(a, b), true
}

// OnDisk reports whether every segment of rel, a slash-separated path
// within root, is the name of an entry in its directory as it is written
// there, letter case and all, and not another name the file system opens
// that entry for: on a file system that folds case, X.PHP for x.php; on
// Windows also an 8.3 short name, GIT~1 for .git. It looks at the disk once
// a segment, or reads the segment's directory where the system has no
// call that answers it, so a handler asks it only of a root FoldsCase
// tells, and off its workers.
func OnDisk(root, rel string) bool {
	dir := root
	for rel != "" {
		segment, rest, _ := strings.Cut(rel, "/")
		rel = rest
		if segment == "" || segment == "." {
			continue
		}
		if !named(dir, segment) {
			return false
		}
		dir = filepath.Join(dir, segment)
	}
	return true
}

// scripts are the extensions, in lower case, that fastcgi handlers run as
// scripts (their split_path), as AddScripts recorded them.
var (
	scriptsMu sync.Mutex
	scripts   atomic.Pointer[[]string]
)

// AddScripts records exts, extensions in lower case with their dot, as
// extensions a handler runs as scripts, for Script, for as long as the
// process runs. A handler that serves files from a root a script handler
// may share refuses them, so that a script's source is never served for a
// request a script route missed.
func AddScripts(exts []string) {
	scriptsMu.Lock()
	defer scriptsMu.Unlock()
	var have []string
	if p := scripts.Load(); p != nil {
		have = *p
	}
	next := slices.Clone(have)
	for _, ext := range exts {
		if ext != "" && !slices.Contains(next, ext) {
			next = append(next, ext)
		}
	}
	if len(next) != len(have) {
		scripts.Store(&next)
	}
}

// Script reports whether name, a file's name or path, ends in an extension
// AddScripts recorded, in any case.
func Script(name string) bool {
	p := scripts.Load()
	if p == nil {
		return false
	}
	for _, ext := range *p {
		if len(name) >= len(ext) && strings.EqualFold(name[len(name)-len(ext):], ext) {
			return true
		}
	}
	return false
}

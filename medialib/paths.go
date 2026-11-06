package medialib

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// ErrBadPath is the error of a path or name the library does not write: one
// that is empty, absolute, leads up with "..", holds a backslash, a colon, a
// control character or a character Windows does not take, a name that
// starts with '.' or ends with a space or '.', a name Windows keeps for a
// device, or one past the bounds of names and paths.
var ErrBadPath = errors.New("medialib: not a path the library writes")

// The bounds of a path the library writes.
const (
	maxNameBytes = 255
	maxPathBytes = 1024
)

// validName reports whether name is a name of a file or directory the
// library writes, the same on every system fit runs on.
func validName(name string) bool {
	if name == "" || len(name) > maxNameBytes || name[0] == '.' || strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f || strings.ContainsRune(`/\:*?"<>|`, c) {
			return false
		}
	}
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(strings.TrimRight(stem, " ")) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return false
	}
	if u := strings.ToUpper(stem); len(u) == 4 && (strings.HasPrefix(u, "COM") || strings.HasPrefix(u, "LPT")) && u[3] >= '0' && u[3] <= '9' {
		return false
	}
	return true
}

// cleanRel checks p, a slash path under a root that the library writes, and
// returns it cleaned of a trailing '/': ErrBadPath unless each of its parts
// is a valid name, and it is at most MaxDepth parts and maxPathBytes long.
// "" is a root itself, taken when dir is set.
func cleanRel(p string, dir bool) (string, error) {
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		if dir {
			return "", nil
		}
		return "", ErrBadPath
	}
	if len(p) > maxPathBytes || strings.Count(p, "/") >= MaxDepth {
		return "", ErrBadPath
	}
	for part := range strings.SplitSeq(p, "/") {
		if !validName(part) {
			return "", ErrBadPath
		}
	}
	return p, nil
}

// rootOf returns the root of s that the config writes as conf, or nil.
func (s *settings) rootOf(conf string) *root {
	for i := range s.roots {
		if s.roots[i].conf == conf {
			return &s.roots[i]
		}
	}
	return nil
}

// path returns the path that rel, a slash path, opens under r.
func (r *root) path(rel string) string { return filepath.Join(r.abs, filepath.FromSlash(rel)) }

// writable checks that the directory of rel under r, as far as it exists,
// lies inside r once its symbolic links are resolved, whatever
// follow_symlinks says: the library never writes out of its roots. It
// returns rel's path, or ErrOutside. It blocks: it runs on the pool.
func writable(r *root, rel string) (string, error) {
	real, err := filepath.EvalSymlinks(r.abs)
	if err != nil {
		return "", err
	}
	abs := r.path(rel)
	for dir := filepath.Dir(abs); ; {
		t, err := filepath.EvalSymlinks(dir)
		if err == nil {
			if !inside(real, t) {
				return "", ErrOutside
			}
			return abs, nil
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir || !inside(r.abs, parent) {
			return "", err
		}
		dir = parent
	}
}

// relDir returns the directory of the slash path p, "" at the root.
func relDir(p string) string {
	if d := path.Dir(p); d != "." {
		return d
	}
	return ""
}

package adapter

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lesismal/fit/config"
)

// A config file may name other files, in a format that lets it (.konf):
// those it imports, config files of their own, in any format, merged after
// it as the files given with -c are; and those it includes, fragments of
// its own format read in its place. Each is named by a path, relative to
// the directory of the file naming it, or by a glob; a directory imported
// stands for its config files, as one given with -c does. A file included
// is read only where it is included, not as a config of its own, also when
// a directory given or imported holds it.

// RefsFunc returns the files a file names, as written: the patterns it
// imports and those it includes, which may name something other than a
// file, as a snippet .konf files define. A file it cannot parse names none; adapting
// it says what is wrong.
type RefsFunc func(data []byte) (imports, includes []string, err error)

// Includer returns the fragments pattern, written in the file from, names:
// the file it names, or the files a glob matches, in lexical order.
type Includer func(from, pattern string) ([]Source, error)

// FileAdapter adapts the config file at path, whose includes it reads
// with include.
type FileAdapter func(path string, data []byte, include Includer) (*Result, error)

// RegisterFiles gives the adapter registered as name what reads its files
// that name others: refs, the files one names, and adapt, which adapts one
// with its includes. It panics if name is not registered.
func RegisterFiles(name string, refs RefsFunc, adapt FileAdapter) {
	mu.Lock()
	defer mu.Unlock()
	e, ok := adapters[name]
	if !ok {
		panic("adapter: " + name + " is not registered")
	}
	e.refs, e.adaptFile = refs, adapt
	adapters[name] = e
}

func lookupEntry(name string) entry {
	mu.RLock()
	defer mu.RUnlock()
	return adapters[name]
}

// hasMeta says pattern is a glob.
func hasMeta(pattern string) bool {
	return strings.ContainsAny(pattern, `*?[`)
}

// refPath returns pattern, written in the file from, as a path: relative
// to from's directory unless it is absolute.
func refPath(from, pattern string) string {
	if filepath.IsAbs(pattern) {
		return filepath.Clean(pattern)
	}
	return filepath.Join(filepath.Dir(from), pattern)
}

// expandRef returns the paths on disk pattern, written in the file from,
// names: the path, which must be there, or the glob's matches, which may be
// none.
func expandRef(from, pattern string) ([]string, error) {
	p := refPath(from, pattern)
	if !hasMeta(p) {
		if _, err := os.Stat(p); err != nil {
			return nil, err
		}
		return []string{p}, nil
	}
	return filepath.Glob(p)
}

// finder finds a config's files: those given, and those they name.
type finder struct {
	files []string
	seen  map[string]bool
}

// config adds the config files p stands for: the file, or a directory's
// files with one of DirExts.
func (f *finder) config(p string) error {
	fi, err := os.Stat(p)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return f.add(p, ForPath(p), false)
	}
	found := 0
	err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != p && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !slices.Contains(DirExts, strings.ToLower(filepath.Ext(path))) {
			return nil
		}
		found++
		return f.add(path, ForPath(path), false)
	})
	if err != nil {
		return err
	}
	if found == 0 {
		return fmt.Errorf("%s: no config files in the directory (%s)", p, strings.Join(DirExts, ", "))
	}
	return nil
}

// add adds the file p, in the format of the adapter named format, and the
// files it names. A fragment, a file included, imports none.
func (f *finder) add(p, format string, fragment bool) error {
	p = filepath.Clean(p)
	if f.seen[p] {
		return nil
	}
	f.seen[p] = true
	if len(f.files) == MaxFiles {
		return fmt.Errorf("a config is read from at most %d files", MaxFiles)
	}
	f.files = append(f.files, p)
	refs := lookupEntry(format).refs
	if refs == nil {
		return nil
	}
	data, err := readLimited(p, MaxBytes)
	if err != nil {
		return nil // reading the config's files reports it
	}
	imports, includes, err := refs(data)
	if err != nil {
		return nil // adapting the file reports it
	}
	for _, pat := range includes {
		paths, err := expandRef(p, pat)
		if err != nil {
			// It may name a snippet, of a file included; adapting the
			// file says what is wrong, at its place.
			continue
		}
		for _, q := range paths {
			if err := f.add(q, format, true); err != nil {
				return err
			}
		}
	}
	if fragment {
		return nil
	}
	for _, pat := range imports {
		paths, err := expandRef(p, pat)
		if err != nil {
			return fmt.Errorf("%s: import %s: %w", p, pat, err)
		}
		for _, q := range paths {
			if err := f.config(q); err != nil {
				return fmt.Errorf("%s: import %s: %w", p, pat, err)
			}
		}
	}
	return nil
}

// snapshot is a config's files as read, which includes are read from, so
// that a config is adapted from the files as they were when read together.
type snapshot struct {
	srcs   []Source
	byPath map[string]int
}

func newSnapshot(srcs []Source) *snapshot {
	s := &snapshot{srcs: srcs, byPath: make(map[string]int, len(srcs))}
	for i, src := range srcs {
		s.byPath[filepath.Clean(src.Path)] = i
	}
	return s
}

// include is the snapshot's Includer.
func (s *snapshot) include(from, pattern string) ([]Source, error) {
	p := refPath(from, pattern)
	if !hasMeta(p) {
		i, ok := s.byPath[p]
		if !ok {
			return nil, fmt.Errorf("%s is not among the config's files read", p)
		}
		return []Source{s.srcs[i]}, nil
	}
	if _, err := filepath.Match(p, ""); err != nil {
		return nil, err
	}
	var out []Source
	for _, src := range s.srcs {
		if ok, _ := filepath.Match(p, filepath.Clean(src.Path)); ok {
			out = append(out, src)
		}
	}
	slices.SortFunc(out, func(a, b Source) int { return strings.Compare(a.Path, b.Path) })
	return out, nil
}

// fragments returns the files of the snapshot that others include, which
// are read only where they are included. A file that includes itself,
// through others or not, is an error.
func (s *snapshot) fragments() (map[string]bool, error) {
	frag := map[string]bool{}
	const visiting, done = 1, 2
	state := map[string]int{}
	var stack []string
	var visit func(src Source, format string) error
	visit = func(src Source, format string) error {
		switch state[src.Path] {
		case visiting:
			i := slices.Index(stack, src.Path)
			chain := strings.Join(append(slices.Clone(stack[i:]), src.Path), " includes ")
			return config.Errors{{File: src.Path, Msg: "includes itself: " + chain}}
		case done:
			return nil
		}
		refs := lookupEntry(format).refs
		if refs == nil {
			return nil
		}
		state[src.Path] = visiting
		stack = append(stack, src.Path)
		if _, includes, err := refs(src.Data); err == nil {
			for _, pat := range includes {
				targets, err := s.include(src.Path, pat)
				if err != nil {
					continue // adapting the file reports it, at its place
				}
				for _, t := range targets {
					frag[t.Path] = true
					if err := visit(t, format); err != nil {
						return err
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[src.Path] = done
		return nil
	}
	for _, src := range s.srcs {
		if err := visit(src, ForPath(src.Path)); err != nil {
			return nil, err
		}
	}
	return frag, nil
}

// adaptSource adapts src by its extension, its includes read from s.
func (s *snapshot) adaptSource(src Source) (*Result, error) {
	name := ForPath(src.Path)
	if e := lookupEntry(name); e.adaptFile != nil {
		return e.adaptFile(src.Path, src.Data, s.include)
	}
	return Adapt(name, src.Data)
}

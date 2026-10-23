package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

// DirExts are the extensions of the files a directory's config is read
// from: fit's own formats. Other adapters' files, an nginx config or a
// Caddyfile, are read only when named.
var DirExts = []string{".konf", ".yaml", ".yml", ".json"}

// The bounds on a config read from several files.
const (
	// MaxFiles is the most files a config is read from.
	MaxFiles = 10000
	// MaxBytes is the most bytes, all of its files together.
	MaxBytes = 64 << 20
)

// Source is a config file and what it holds.
type Source struct {
	Path string
	Data []byte
}

// Files returns the config files paths name: each file as it is, and each
// directory's files with one of DirExts, in its subdirectories too, in
// lexical order. Files and directories whose names start with a dot are
// left out of a directory's. A file named twice is read once.
func Files(paths []string) ([]string, error) {
	var files []string
	seen := map[string]bool{}
	add := func(p string) error {
		p = filepath.Clean(p)
		if seen[p] {
			return nil
		}
		seen[p] = true
		if len(files) == MaxFiles {
			return fmt.Errorf("a config is read from at most %d files", MaxFiles)
		}
		files = append(files, p)
		return nil
	}
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			if err := add(p); err != nil {
				return nil, err
			}
			continue
		}
		n := len(files)
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
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
			return add(path)
		})
		if err != nil {
			return nil, err
		}
		if len(files) == n {
			return nil, fmt.Errorf("%s: no config files in the directory (%s)", p, strings.Join(DirExts, ", "))
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no config files")
	}
	return files, nil
}

// ReadSources reads the config files paths name, as Files finds them,
// refusing more than MaxBytes in all.
func ReadSources(paths []string) ([]Source, error) {
	files, err := Files(paths)
	if err != nil {
		return nil, err
	}
	srcs := make([]Source, 0, len(files))
	var total int64
	for _, f := range files {
		data, err := readLimited(f, MaxBytes-total)
		if err != nil {
			return nil, err
		}
		total += int64(len(data))
		srcs = append(srcs, Source{Path: f, Data: data})
	}
	return srcs, nil
}

func readLimited(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var b bytes.Buffer
	n, err := io.Copy(&b, io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("%s: the config's files hold more than %d bytes", path, MaxBytes)
	}
	return b.Bytes(), nil
}

// Set is a config read from several files, each adapted by its extension
// and merged into one: the lists at the top of the config (listeners,
// routes, upstreams, services, providers) are joined in the files' order,
// and any other field may be set by one file only, but schema_version,
// which files may each give if they agree.
type Set struct {
	// Files are the files, in the order they were merged.
	Files []string
	// Parts are the files adapted, one for each.
	Parts []*Result
	// JSON is the config merged, in native JSON.
	JSON []byte

	// lists says, for each list merged, how many elements each part gave;
	// owner, which part set each other field.
	lists map[string][]int
	owner map[string]int
}

// AdaptSet adapts each of srcs and merges them. Its errors are
// config.Errors, each naming its file.
func AdaptSet(srcs []Source) (*Set, error) {
	s := &Set{lists: map[string][]int{}, owner: map[string]int{}}
	for _, src := range srcs {
		r, err := Adapt(ForPath(src.Path), src.Data)
		if err != nil {
			return nil, inFile(src.Path, err)
		}
		s.Files = append(s.Files, src.Path)
		s.Parts = append(s.Parts, r)
	}
	if len(s.Parts) == 1 {
		s.JSON = s.Parts[0].JSON
		return s, nil
	}
	for i, r := range s.Parts {
		if ForPath(s.Files[i]) == "" {
			r.Position = jsonPosition(r.JSON)
		}
	}
	merged, err := s.merge()
	if err != nil {
		return nil, err
	}
	s.JSON = merged
	return s, nil
}

// inFile returns err with each of its config errors naming file.
func inFile(file string, err error) error {
	errs := config.AsErrors(err)
	out := make(config.Errors, len(errs))
	for i, e := range errs {
		c := *e
		c.File = file
		out[i] = &c
	}
	return out
}

func (s *Set) merge() ([]byte, error) {
	var keys []string
	vals := map[string][]json.RawMessage{} // a list's elements, or one value
	for i, r := range s.Parts {
		members, err := topMembers(r.JSON)
		if err != nil {
			return nil, inFile(s.Files[i], err)
		}
		for _, m := range members {
			var elems []json.RawMessage
			isList := m.key != "schema_version" && json.Unmarshal(m.val, &elems) == nil
			if _, ok := vals[m.key]; !ok {
				keys = append(keys, m.key)
			}
			switch {
			case isList:
				if _, single := s.owner[m.key]; single {
					return nil, s.conflict(i, m.key)
				}
				counts := s.lists[m.key]
				for len(counts) < i {
					counts = append(counts, 0)
				}
				s.lists[m.key] = append(counts, len(elems))
				vals[m.key] = append(vals[m.key], elems...)
			case m.key == "schema_version" && vals[m.key] != nil:
				if !bytes.Equal(vals[m.key][0], m.val) {
					return nil, s.conflict(i, m.key)
				}
			default:
				if _, ok := vals[m.key]; ok {
					return nil, s.conflict(i, m.key)
				}
				s.owner[m.key] = i
				vals[m.key] = []json.RawMessage{m.val}
			}
		}
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		if _, list := s.lists[k]; list {
			b.WriteByte('[')
			for j, e := range vals[k] {
				if j > 0 {
					b.WriteByte(',')
				}
				b.Write(e)
			}
			b.WriteByte(']')
		} else {
			b.Write(vals[k][0])
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// conflict is the error for key, given again by part i.
func (s *Set) conflict(i int, key string) error {
	first := s.Files[s.owner[key]]
	if counts, ok := s.lists[key]; ok {
		for j, n := range counts {
			if n > 0 || j == len(counts)-1 {
				first = s.Files[j]
				break
			}
		}
	}
	msg := fmt.Sprintf("also given in %s: only the lists at the top of a config (listeners, routes, upstreams, services, providers) may be split across files", first)
	if key == "schema_version" {
		msg = fmt.Sprintf("differs from the one in %s", first)
	}
	e := &config.Error{File: s.Files[i], Path: key, Msg: msg}
	if p := s.Parts[i].Position; p != nil {
		e.Line, e.Col, _ = p(key)
	}
	return config.Errors{e}
}

type topMember struct {
	key string
	val json.RawMessage
}

// topMembers returns the members of the object a part's JSON holds.
func topMembers(data []byte) ([]topMember, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if t != json.Delim('{') {
		return nil, errors.New("a config is an object")
	}
	var ms []topMember
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		ms = append(ms, topMember{key: kt.(string), val: v})
	}
	return ms, nil
}

// locate returns which part the value at path in the merged config came
// from, and its path there.
func (s *Set) locate(path string) (int, string) {
	if len(s.Parts) == 1 {
		return 0, path
	}
	top := path
	if i := strings.IndexAny(path, ".["); i >= 0 {
		top = path[:i]
	}
	if i, ok := s.owner[top]; ok {
		return i, path
	}
	counts, ok := s.lists[top]
	if !ok {
		return -1, path
	}
	rest := path[len(top):]
	if !strings.HasPrefix(rest, "[") {
		for j, n := range counts {
			if n > 0 {
				return j, path
			}
		}
		return -1, path
	}
	end := strings.IndexByte(rest, ']')
	n, err := strconv.Atoi(rest[1:max(end, 1)])
	if end < 0 || err != nil {
		return -1, path
	}
	for j, c := range counts {
		if n < c {
			return j, fmt.Sprintf("%s[%d]%s", top, n, rest[end+1:])
		}
		n -= c
	}
	return -1, path
}

// MapErrors returns err with each of its config errors, which are about the
// merged config, at its file and its place there, its path the path in
// that file.
func (s *Set) MapErrors(err error) error {
	if err == nil {
		return nil
	}
	errs := config.AsErrors(err)
	out := make(config.Errors, 0, len(errs))
	for _, e := range errs {
		part, local := s.locate(e.Path)
		if part < 0 {
			c := *e
			c.Line, c.Col = 0, 0
			out = append(out, &c)
			continue
		}
		c := *e
		c.Path = local
		if len(s.Parts) > 1 && s.Parts[part].Position == nil {
			c.Line, c.Col = 0, 0
		}
		mapped := config.AsErrors(s.Parts[part].MapErrors(config.Errors{&c}))
		m := *mapped[0]
		m.File = s.Files[part]
		out = append(out, &m)
	}
	return out
}

// Name is how messages name the config: its file, or its files.
func (s *Set) Name() string {
	return strings.Join(s.Files, ", ")
}

// jsonPosition returns where in native JSON the value at a path is.
func jsonPosition(data []byte) func(string) (int, int, bool) {
	root, err := config.ParseValue(data, "")
	if err != nil {
		return nil
	}
	return func(path string) (int, int, bool) {
		v := root
		for p := path; p != ""; {
			switch {
			case strings.HasPrefix(p, "."):
				p = p[1:]
			case strings.HasPrefix(p, `["`):
				q, err := strconv.QuotedPrefix(p[1:])
				if err != nil {
					return 0, 0, false
				}
				k, _ := strconv.Unquote(q)
				v = v.Field(k)
				p = strings.TrimPrefix(p[1+len(q):], "]")
			case strings.HasPrefix(p, "["):
				end := strings.IndexByte(p, ']')
				if end < 0 {
					return 0, 0, false
				}
				n, err := strconv.Atoi(p[1:end])
				if err != nil {
					return 0, 0, false
				}
				v = v.Index(n)
				p = p[end+1:]
			default:
				end := strings.IndexAny(p, ".[")
				if end < 0 {
					end = len(p)
				}
				v = v.Field(p[:end])
				p = p[end:]
			}
			if v.IsZero() {
				return 0, 0, false
			}
		}
		line, col := v.Pos()
		return line, col, line > 0
	}
}

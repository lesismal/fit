package medialib

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
)

// settings is a library's config, compiled: immutable, and replaced whole by
// a reload.
type settings struct {
	roots            []root
	include, exclude []pattern
	follow           bool
	interval         time.Duration
	maxItems         int
	maxFiles         int
	catalogBytes     int64
	probes           int
	sidecarBytes     int64
	subtitleBytes    int64
	tableBytes       int64
	workers, queue   int
	timeout          time.Duration
	// keys are those of encrypted HLS, nil without a secret.
	keys *Keys
}

// root is a root of a library: as the config writes it, and absolute.
type root struct {
	conf, abs string
}

func compile(c *config.MediaLibrary) (*settings, error) {
	s := &settings{follow: c.FollowSymlinks, interval: c.Interval(), maxItems: c.Items(), maxFiles: c.Files(),
		catalogBytes: c.CatalogBytes(), probes: c.Probes(), sidecarBytes: c.SidecarBytes(), subtitleBytes: c.SubtitleBytes(),
		tableBytes: c.TableBytes(), workers: c.Workers(), queue: c.Queue(), timeout: c.Timeout()}
	if len(c.Roots) == 0 {
		return nil, fmt.Errorf("no roots")
	}
	keys, err := KeysOf(c)
	if err != nil {
		return nil, err
	}
	s.keys = keys
	for _, r := range c.Roots {
		abs, err := filepath.Abs(r)
		if err != nil || strings.TrimSpace(r) == "" {
			return nil, fmt.Errorf("root %q: not a path", r)
		}
		s.roots = append(s.roots, root{conf: r, abs: abs})
	}
	for _, f := range []struct {
		list []string
		to   *[]pattern
	}{{c.Patterns, &s.include}, {c.Exclude, &s.exclude}} {
		for _, p := range f.list {
			if !config.ValidMediaPathPattern(p) {
				return nil, fmt.Errorf("%q is not a pattern", p)
			}
			*f.to = append(*f.to, newPattern(p))
		}
	}
	return s, nil
}

// scanKey is what of s a scan depends on: a reload that changes it scans
// again at once.
func (s *settings) scanKey() string {
	var b strings.Builder
	for _, r := range s.roots {
		b.WriteString(r.abs)
		b.WriteByte(0)
	}
	for _, l := range [][]pattern{s.include, s.exclude} {
		b.WriteByte(1)
		for _, p := range l {
			b.WriteString(p.src)
			b.WriteByte(0)
		}
	}
	fmt.Fprintf(&b, "%t %d %d %d %d %d", s.follow, s.maxItems, s.maxFiles, s.catalogBytes, s.sidecarBytes, s.subtitleBytes)
	return b.String()
}

// pattern is an include or exclude pattern: one without '/' matches a
// name, one with '/' a path under the root, ** matching any run of
// directories.
type pattern struct {
	src   string
	parts []string // nil for a pattern of a name
}

func newPattern(p string) pattern {
	if !strings.Contains(p, "/") {
		return pattern{src: p}
	}
	return pattern{src: p, parts: strings.Split(p, "/")}
}

// match reports whether p matches rel, a slash path under a root.
func (p pattern) match(rel string) bool {
	if p.parts == nil {
		ok, _ := path.Match(p.src, path.Base(rel))
		return ok
	}
	return matchParts(p.parts, strings.Split(rel, "/"))
}

func matchParts(pat, name []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			// ** takes none of the names, or one more.
			for i := 0; i <= len(name); i++ {
				if matchParts(pat[1:], name[i:]) {
					return true
				}
			}
			return false
		}
		if len(name) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], name[0]); !ok {
			return false
		}
		pat, name = pat[1:], name[1:]
	}
	return len(name) == 0
}

func anyMatch(list []pattern, rel string) bool {
	for _, p := range list {
		if p.match(rel) {
			return true
		}
	}
	return false
}

// included reports whether the file at rel is looked at.
func (s *settings) included(rel string) bool {
	return (len(s.include) == 0 || anyMatch(s.include, rel)) && !anyMatch(s.exclude, rel)
}

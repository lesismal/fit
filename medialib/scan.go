package medialib

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib/media/hls"
	"github.com/lesismal/fib/media/mp4"
)

// MaxDepth is how deep under its root a scan walks directories.
const MaxDepth = 32

// readDirBatch is how many entries of a directory are read at once, so that
// a huge directory is read in pieces, each counted against max_files.
const readDirBatch = 256

// kind is what a file of a library is, by its extension.
type kind uint8

const (
	kindNone kind = iota
	kindMedia
	kindSubtitle
	kindImage
	kindSidecar
)

func kindOfName(name string) kind {
	switch strings.ToLower(path.Ext(name)) {
	case ".mp4", ".m4v", ".m4a", ".mov":
		return kindMedia
	case ".vtt", ".srt":
		return kindSubtitle
	case ".jpg", ".jpeg", ".png", ".webp":
		return kindImage
	case ".json":
		return kindSidecar
	}
	return kindNone
}

// entry is a file a scan found.
type entry struct {
	root *root
	real string // the root's path with its links resolved
	rel  string // its path under the root, with '/'
	abs  string // the path it is opened by
	size int64
	mod  time.Time
	kind kind

	claimed bool // named by a sidecar
}

func (e *entry) file() File {
	return File{Root: e.root.conf, Path: e.rel, Size: e.size, ModTime: e.mod, abs: e.abs, real: e.real}
}

// scanner is one scan of a library.
type scanner struct {
	l *Library
	s *settings

	entries  []*entry
	files    int
	problems []Problem
	more     int // problems past MaxProblems
	trunc    string
}

func (sc *scanner) problem(r *root, rel, format string, args ...any) {
	if len(sc.problems) >= MaxProblems {
		sc.more++
		return
	}
	p := Problem{Path: rel, Error: fmt.Sprintf(format, args...)}
	if r != nil {
		p.Root = r.conf
	}
	sc.problems = append(sc.problems, p)
}

func (sc *scanner) truncate(format string, args ...any) {
	if sc.trunc == "" {
		sc.trunc = fmt.Sprintf(format, args...)
	}
}

// scan scans the library's roots into a new catalog, which replaces the
// one before once it is whole. It runs on the library's scanner goroutine,
// its reads of files on the library's pool.
func (l *Library) scan() {
	sc := &scanner{l: l, s: l.set.Load()}
	l.sweepUploads(sc.s.up)
	started := time.Now()
	l.mu.Lock()
	l.status = Status{Scanning: true, Scans: l.status.Scans, Started: started}
	edits := l.edits
	l.mu.Unlock()
	cat := sc.run(l.cat.Load())
	ended := time.Now()
	l.mu.Lock()
	l.status.Scanning, l.status.Ended = false, ended
	if cat != nil {
		l.status.Scans++
	}
	l.mu.Unlock()
	if cat == nil {
		return
	}
	cat.Scanned = ended
	l.mu.Lock()
	// What the library's own changes took out while the scan ran stays out:
	// the scan may have listed it before, and the next scan lists it as it
	// is now.
	var gone []string
	for id, at := range l.forgotten {
		if at > edits {
			gone = append(gone, id)
		} else {
			delete(l.forgotten, id)
		}
	}
	if len(gone) > 0 {
		cat = cat.without(gone)
	}
	l.cat.Store(cat)
	l.mu.Unlock()
	l.tables.keep(cat)
	l.scans.Add(1)
	errs := 0
	for _, it := range cat.Items {
		if it.Error != "" {
			errs++
		}
	}
	l.log.Debugf("%s: %d items, %d of them with errors, %d problems, in %s", l.name, len(cat.Items), errs, len(cat.Problems)+cat.MoreProblems, ended.Sub(started).Round(time.Millisecond))
	if cat.Truncated != "" {
		l.log.Warnf("%s: the catalog lacks items: %s", l.name, cat.Truncated)
	}
}

// run makes the catalog, from prev's probes of the files unchanged; nil
// when the library is removed meanwhile.
func (sc *scanner) run(prev *Catalog) *Catalog {
	for i := range sc.s.roots {
		sc.walk(&sc.s.roots[i])
		if sc.l.stopped() {
			return nil
		}
	}
	sc.progress(func(st *Status) { st.Files = sc.files })
	sides := sc.readSidecars()
	groups := sc.group(sides)
	if !sc.probe(groups, prev) {
		return nil
	}
	return sc.build(groups)
}

func (sc *scanner) progress(fn func(*Status)) {
	sc.l.mu.Lock()
	fn(&sc.l.status)
	sc.l.mu.Unlock()
}

// dirTask is a directory to walk.
type dirTask struct {
	abs, real, rel string
	depth          int
}

// walk lists the files of r that the library looks at, its directories
// read in batches, each entry counted against max_files.
func (sc *scanner) walk(r *root) {
	real, err := filepath.EvalSymlinks(r.abs)
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(real); err == nil && !fi.IsDir() {
			err = errors.New("not a directory")
		}
	}
	if err != nil {
		sc.problem(r, "", "the root: %v", err)
		return
	}
	if sc.trunc != "" {
		return
	}
	stack := []dirTask{{abs: r.abs, real: real}}
	visited := map[string]bool{real: true}
	for len(stack) > 0 && sc.trunc == "" && !sc.l.stopped() {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ents, ok := sc.readDir(r, d)
		if !ok {
			return
		}
		for _, de := range ents {
			name := de.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			rel, abs := path.Join(d.rel, name), filepath.Join(d.abs, name)
			var fi os.FileInfo
			target := filepath.Join(d.real, name)
			if de.Type()&fs.ModeSymlink != 0 {
				if target, err = filepath.EvalSymlinks(abs); err == nil {
					fi, err = os.Stat(target)
				}
				switch {
				case err != nil:
					sc.problem(r, rel, "a symbolic link that leads nowhere")
					continue
				case !sc.s.follow && !inside(real, target):
					sc.problem(r, rel, "a symbolic link out of the root, refused: follow_symlinks follows it")
					continue
				}
			} else if fi, err = de.Info(); err != nil {
				// Removed since it was listed.
				continue
			}
			switch {
			case fi.IsDir():
				switch {
				case anyMatch(sc.s.exclude, rel), visited[target]:
				case d.depth+1 > MaxDepth:
					sc.problem(r, rel, "deeper than %d directories under the root: not walked", MaxDepth)
				default:
					visited[target] = true
					stack = append(stack, dirTask{abs: abs, real: target, rel: rel, depth: d.depth + 1})
				}
			case fi.Mode().IsRegular():
				if k := kindOfName(name); k != kindNone && sc.s.included(rel) {
					sc.entries = append(sc.entries, &entry{root: r, real: real, rel: rel, abs: abs, size: fi.Size(), mod: fi.ModTime(), kind: k})
				}
			}
		}
	}
}

// readDir reads d's entries, sorted by name; false once max_files is
// passed, which ends the walk.
func (sc *scanner) readDir(r *root, d dirTask) ([]os.DirEntry, bool) {
	f, err := os.Open(d.abs)
	if err != nil {
		sc.problem(r, d.rel, "%v", err)
		return nil, true
	}
	defer f.Close()
	var ents []os.DirEntry
	for {
		batch, err := f.ReadDir(readDirBatch)
		sc.files += len(batch)
		if sc.files > sc.s.maxFiles {
			sc.truncate("more than max_files, %d, files and directories: the scan stopped at %s", sc.s.maxFiles, path.Join(r.conf, d.rel))
			return nil, false
		}
		ents = append(ents, batch...)
		if err == io.EOF || len(batch) == 0 {
			break
		}
		if err != nil {
			sc.problem(r, d.rel, "%v", err)
			break
		}
	}
	slices.SortFunc(ents, func(a, b os.DirEntry) int { return strings.Compare(a.Name(), b.Name()) })
	return ents, true
}

// inside reports whether p is dir or lies inside it, both clean absolute
// paths.
func inside(dir, p string) bool {
	if p == dir {
		return true
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// onPool runs run(i) for i in [0, n) on the library's file pool, at most
// max_probes at once, and waits for them all; a job the pool refuses is
// queued again later, and one that waited past file_timeout, or that the
// library's removal leaves undone, gets lost(i) instead.
func (sc *scanner) onPool(n int, run, lost func(i int)) {
	sem := make(chan struct{}, sc.s.probes)
	var wg sync.WaitGroup
	for i := range n {
		sem <- struct{}{}
		wg.Add(1)
		done := func() { <-sem; wg.Done() }
		backoff := 5 * time.Millisecond
		for !sc.l.Pool().Submit(func() { defer done(); run(i) }, func() { defer done(); lost(i) }) {
			if !sc.l.sleep(backoff) {
				lost(i)
				done()
				break
			}
			backoff = min(2*backoff, 500*time.Millisecond)
		}
	}
	wg.Wait()
}

// sleep waits for d, and reports false when the library is removed
// meanwhile.
func (l *Library) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-l.stop:
		return false
	}
}

// readSidecars reads and parses the sidecars found, on the pool, their
// bytes bounded by max_sidecar_bytes each and by max_catalog_bytes all
// together; a sidecar that does not parse is a problem.
func (sc *scanner) readSidecars() map[*entry]*Sidecar {
	var list []*entry
	var total int64
	for _, e := range sc.entries {
		if e.kind != kindSidecar {
			continue
		}
		switch {
		case e.size > sc.s.sidecarBytes:
			sc.problem(e.root, e.rel, "a sidecar of %d bytes, past max_sidecar_bytes, %d: left out", e.size, sc.s.sidecarBytes)
		case total+e.size > sc.s.catalogBytes:
			sc.truncate("the sidecars pass max_catalog_bytes, %d: %s and those after it are left out", sc.s.catalogBytes, path.Join(e.root.conf, e.rel))
		default:
			total += e.size
			list = append(list, e)
		}
	}
	parsed := make([]*Sidecar, len(list))
	errs := make([]error, len(list))
	sc.onPool(len(list), func(i int) {
		parsed[i], errs[i] = readSidecar(list[i], sc.s.follow)
	}, func(i int) { errs[i] = ErrBusy })
	out := make(map[*entry]*Sidecar, len(list))
	for i, e := range list {
		if errs[i] != nil {
			sc.problem(e.root, e.rel, "the sidecar: %v", errs[i])
			continue
		}
		out[e] = parsed[i]
	}
	return out
}

// renditionLabel is what the last part of a media file's name before its
// extension is, when it names a rendition of an item: bbb.720p.mp4,
// bbb.4k.mp4, bbb.800k.mp4, bbb.audio-fr.m4a.
var renditionLabel = regexp.MustCompile(`^([0-9]{3,4}p[0-9]{0,3}|[0-9]+k|[0-9]+kbps|audio(-[A-Za-z0-9]+)*)$`)

// subtitleLanguage is what a part of a subtitle's name is when it names its
// language: bbb.en.srt, bbb.pt-BR.vtt, bbb.zho.srt.
var subtitleLanguage = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

// splitLabel splits a media file's name, without its extension, into the
// item's base and the rendition's label.
func splitLabel(stem string) (base, label string) {
	if i := strings.LastIndexByte(stem, '.'); i > 0 && renditionLabel.MatchString(stem[i+1:]) {
		return stem[:i], stem[i+1:]
	}
	return stem, ""
}

// member is a file of a group: a rendition, audio, a subtitle or its
// poster, with what the sidecar or its name says of it.
type member struct {
	e        *entry
	name     string
	language string
	def      bool
	forced   bool
	audio    bool // listed as audio, or labelled audio-*

	r *Rendition // the media file probed
}

// group is the files of an item, before they are probed.
type group struct {
	root  *root
	dir   string // with no '/' at the end; "" at the root
	base  string
	side  *Sidecar
	sidef *entry
	media []*member
	subs  []*member
	post  *entry
}

func (g *group) path() string { return path.Join(g.dir, g.base) }

// group gathers the files found into items: what sidecars name, then media
// files by their names' base and rendition label, then subtitles and
// posters named after an item's base.
func (sc *scanner) group(sides map[*entry]*Sidecar) []*group {
	byRel := map[*root]map[string]*entry{}
	for _, e := range sc.entries {
		if byRel[e.root] == nil {
			byRel[e.root] = map[string]*entry{}
		}
		byRel[e.root][e.rel] = e
	}
	groups := map[string]*group{}
	var order []*group
	get := func(r *root, dir, base string) *group {
		key := r.conf + "\x00" + dir + "\x00" + base
		g := groups[key]
		if g == nil {
			g = &group{root: r, dir: dir, base: base}
			groups[key] = g
			order = append(order, g)
		}
		return g
	}
	split := func(rel string) (string, string) {
		dir, name := path.Split(rel)
		return strings.TrimSuffix(dir, "/"), strings.TrimSuffix(name, path.Ext(name))
	}
	// Sidecars first, so that the files they name are theirs.
	for _, e := range sc.entries {
		sd := sides[e]
		if sd == nil {
			continue
		}
		dir, base := split(e.rel)
		g := get(e.root, dir, base)
		g.side, g.sidef = sd, e
		ref := func(field, file string, want kind) *entry {
			rel, ok := underDir(dir, file)
			var f *entry
			switch {
			case !ok:
				sc.problem(e.root, e.rel, "%s %q leads out of the sidecar's directory", field, file)
			case byRel[e.root][rel] == nil:
				sc.problem(e.root, e.rel, "%s %q is not a file the library looks at", field, file)
			case byRel[e.root][rel].kind != want:
				sc.problem(e.root, e.rel, "%s %q is not a %s", field, file, kindNames[want])
			case byRel[e.root][rel].claimed:
				sc.problem(e.root, e.rel, "%s %q is named by a sidecar already", field, file)
			default:
				f = byRel[e.root][rel]
				f.claimed = true
			}
			return f
		}
		for i, list := range [][]SidecarFile{sd.Renditions, sd.Audio} {
			for _, m := range list {
				if f := ref("file", m.File, kindMedia); f != nil {
					g.media = append(g.media, &member{e: f, name: m.Name, language: m.Language, def: m.Default, audio: i == 1})
				}
			}
		}
		for _, s := range sd.Subtitles {
			if f := ref("file", s.File, kindSubtitle); f != nil {
				g.subs = append(g.subs, &member{e: f, name: s.Name, language: s.Language, def: s.Default, forced: s.Forced})
			}
		}
		if sd.Poster != "" {
			g.post = ref("poster", sd.Poster, kindImage)
		}
	}
	for _, e := range sc.entries {
		if e.kind != kindMedia || e.claimed {
			continue
		}
		dir, stem := split(e.rel)
		base, label := splitLabel(stem)
		g := get(e.root, dir, base)
		g.media = append(g.media, &member{e: e, name: label, audio: strings.HasPrefix(label, "audio"), language: labelLanguage(label)})
	}
	byDir := map[string][]*group{}
	for _, g := range order {
		key := g.root.conf + "\x00" + g.dir
		byDir[key] = append(byDir[key], g)
	}
	for _, e := range sc.entries {
		if e.claimed || e.kind != kindSubtitle && e.kind != kindImage {
			continue
		}
		dir, stem := split(e.rel)
		var g *group
		for _, c := range byDir[e.root.conf+"\x00"+dir] {
			if (stem == c.base || strings.HasPrefix(stem, c.base+".") || e.kind == kindImage && stem == c.base+"-poster") &&
				(g == nil || len(c.base) > len(g.base)) {
				g = c
			}
		}
		switch {
		case g == nil:
		case e.kind == kindSubtitle:
			m := &member{e: e}
			for part := range strings.SplitSeq(strings.TrimPrefix(strings.TrimPrefix(stem, g.base), "."), ".") {
				switch {
				case part == "":
				case part == "forced":
					m.forced = true
				case part == "default":
					m.def = true
				case m.language == "" && subtitleLanguage.MatchString(part):
					m.language = part
				default:
					m.name = part
				}
			}
			g.subs = append(g.subs, m)
		case g.post == nil && (stem == g.base || stem == g.base+".poster" || stem == g.base+"-poster"):
			g.post = e
		}
	}
	return order
}

var kindNames = map[kind]string{kindMedia: "media file", kindSubtitle: "subtitle", kindImage: "picture"}

// labelLanguage is the language an audio rendition's label names, as fr of
// audio-fr; "" when it names none.
func labelLanguage(label string) string {
	if rest, ok := strings.CutPrefix(label, "audio-"); ok {
		first, _, _ := strings.Cut(rest, "-")
		if subtitleLanguage.MatchString(first) {
			return first
		}
	}
	return ""
}

// underDir returns the path under the root of file, a sidecar's slash path
// relative to its directory dir, and false when it leads out of dir or is
// not a plain relative path.
func underDir(dir, file string) (string, bool) {
	if file == "" || strings.HasPrefix(file, "/") || strings.Contains(file, "\\") || strings.ContainsRune(file, 0) {
		return "", false
	}
	clean := path.Clean(file)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return path.Join(dir, clean), true
}

// probeResult is what probing a media file found.
type probeResult struct {
	r         *Rendition
	transient bool // it changed while probed, or the pool was busy: probe it again next time
}

// probe probes the media files of groups on the pool, those unchanged
// since prev reused as prev found them; a file that changes while it is
// probed is probed again, once, and listed with that error if it changes
// again. It reports false when the library is removed meanwhile.
func (sc *scanner) probe(groups []*group, prev *Catalog) bool {
	old := map[string]*Rendition{}
	for _, it := range prev.Items {
		for _, list := range [][]*Rendition{it.Renditions, it.Audio} {
			for _, r := range list {
				if !r.transient {
					old[r.abs] = r
				}
			}
		}
	}
	var todo []*member
	reused := 0
	for _, g := range groups {
		for _, m := range g.media {
			if o := old[m.e.abs]; o != nil && o.Size == m.e.size && o.ModTime.Equal(m.e.mod) && o.Root == m.e.root.conf && o.Path == m.e.rel {
				r := *o
				m.r = &r
				reused++
				continue
			}
			todo = append(todo, m)
		}
	}
	sc.progress(func(st *Status) { st.Probe, st.Reused = len(todo), reused })
	for round := 0; round < 2 && len(todo) > 0; round++ {
		if round > 0 {
			sc.progress(func(st *Status) { st.Probe += len(todo) })
		}
		results := make([]probeResult, len(todo))
		sc.onPool(len(todo), func(i int) {
			results[i] = probeFile(todo[i].e, sc.s.follow)
			sc.l.probed.Add(1)
			if results[i].r.Error != "" {
				sc.l.probeErrors.Add(1)
			}
			sc.progress(func(st *Status) { st.Probed++ })
		}, func(i int) {
			results[i] = probeResult{r: &Rendition{File: todo[i].e.file(), Error: ErrBusy.Error()}, transient: true}
		})
		if sc.l.stopped() {
			return false
		}
		var again []*member
		for i, m := range todo {
			m.r = results[i].r
			m.r.transient = results[i].transient
			if results[i].transient && round == 0 {
				// As it is now, for the second try.
				if fi, err := os.Stat(m.e.abs); err == nil {
					m.e.size, m.e.mod = fi.Size(), fi.ModTime()
				}
				again = append(again, m)
			}
		}
		todo = again
	}
	return true
}

// probeFile reads the boxes of the media file e, through its root's
// checks, and tells its tracks.
func probeFile(e *entry, follow bool) probeResult {
	r := &Rendition{File: e.file()}
	f, err := openFile(&r.File, follow)
	if err != nil {
		if errors.Is(err, ErrChanged) {
			r.Error = "the file changed while it was probed; it is probed again at the next scan"
			return probeResult{r: r, transient: true}
		}
		r.Error = err.Error()
		return probeResult{r: r}
	}
	defer f.Close()
	m, err := mp4.Open(f.f, f.Size)
	if err == nil {
		// Unchanged while it was read: as it was opened.
		var fi os.FileInfo
		if fi, err = f.f.Stat(); err == nil && (fi.Size() != e.size || !fi.ModTime().Equal(e.mod)) {
			err = ErrChanged
		}
	}
	switch {
	case errors.Is(err, ErrChanged):
		r.Error = "the file changed while it was probed; it is probed again at the next scan"
		return probeResult{r: r, transient: true}
	case err != nil:
		r.Error = probeError(err)
		return probeResult{r: r}
	}
	describe(r, m)
	return probeResult{r: r}
}

// probeError says what err, from mp4.Open, means for the file.
func probeError(err error) string {
	switch {
	case errors.Is(err, mp4.ErrNoMoov):
		return "not an MP4 file, or one cut short before its moov box"
	case errors.Is(err, mp4.ErrFragmented):
		return "a fragmented MP4 file, which the library does not serve"
	case errors.Is(err, mp4.ErrTooLarge):
		return "an MP4 file past the reader's limits"
	case errors.Is(err, mp4.ErrMalformed):
		return "a malformed MP4 file"
	}
	return err.Error()
}

// describe fills r from m, the file opened.
func describe(r *Rendition, m *mp4.File) {
	var codecs []string
	playable := false
	end := time.Duration(0)
	for i, t := range m.Tracks {
		tr := Track{Index: i, ID: t.ID, Kind: kindOf(t.Handler), Codec: codecName(t.Track.Codec), CodecString: hls.CodecString(t.Track),
			Language: t.Language, SampleRate: t.Track.SampleRate, Channels: t.Track.Channels, Samples: t.Samples, TableBytes: t.TableBytes()}
		tr.Start, tr.Duration = presented(m, t)
		end = max(end, tr.Start+tr.Duration)
		if tr.Kind == KindVideo {
			tr.Width, tr.Height = t.Track.Width, t.Track.Height
			if r.Width == 0 {
				r.Width, r.Height = tr.Width, tr.Height
			}
		}
		if t.Track.Codec.IsVideo() && tr.Kind == KindVideo || t.Track.Codec.IsAudio() && tr.Kind == KindAudio {
			playable = true
			if tr.CodecString != "" {
				codecs = append(codecs, tr.CodecString)
			}
		}
		r.Tracks = append(r.Tracks, tr)
	}
	if !playable {
		r.Error = "no H.264, H.265 or AV1 video, nor AAC, Opus or MP3 audio, which the library serves"
		return
	}
	r.Duration = m.Duration
	if r.Duration <= 0 {
		r.Duration = end
	}
	if r.Duration > 0 {
		r.Bitrate = int64(float64(r.Size) * 8 / r.Duration.Seconds())
	}
	r.Codecs = strings.Join(codecs, ",")
	r.mp4 = m
}

// presented returns when t starts to play and for how long, as its edit
// list says: after the empty edits at its start, for the movie's time of
// the edits that play; its media's duration without an edit list.
func presented(m *mp4.File, t *mp4.Track) (start, length time.Duration) {
	if len(t.Edits) == 0 || m.Timescale == 0 {
		return 0, t.Duration
	}
	var empty, play int64
	for _, e := range t.Edits {
		switch {
		case e.MediaTime < 0 && play == 0:
			empty += e.Duration
		case e.MediaTime >= 0:
			play += e.Duration
		}
	}
	ts := time.Duration(m.Timescale)
	return time.Duration(empty) * time.Second / ts, time.Duration(play) * time.Second / ts
}

// build makes the catalog of groups: their items in the order of the roots
// and their paths, an ID taken twice left out, until max_items or
// max_catalog_bytes, then sorted by ID.
func (sc *scanner) build(groups []*group) *Catalog {
	rootIndex := map[*root]int{}
	for i := range sc.s.roots {
		rootIndex[&sc.s.roots[i]] = i
	}
	slices.SortStableFunc(groups, func(a, b *group) int {
		return cmp.Or(cmp.Compare(rootIndex[a.root], rootIndex[b.root]), strings.Compare(a.path(), b.path()))
	})
	cat := &Catalog{byID: map[string]*Item{}}
	where := map[string]string{}
	for _, g := range groups {
		it := sc.item(g)
		if w, ok := where[it.ID]; ok {
			sc.problem(g.root, g.path(), "the ID %q is taken by %s: left out", it.ID, w)
			continue
		}
		if len(cat.Items) >= sc.s.maxItems {
			sc.truncate("more than max_items, %d, items: %s and those after it are left out", sc.s.maxItems, path.Join(g.root.conf, g.path()))
			break
		}
		if n := it.reckon(); cat.Bytes+n > sc.s.catalogBytes {
			sc.truncate("the items pass max_catalog_bytes, %d: %s and those after it are left out", sc.s.catalogBytes, path.Join(g.root.conf, g.path()))
			break
		}
		cat.Bytes += it.bytes
		where[it.ID] = path.Join(g.root.conf, g.path())
		cat.Items = append(cat.Items, it)
		cat.byID[it.ID] = it
	}
	slices.SortFunc(cat.Items, func(a, b *Item) int { return strings.Compare(a.ID, b.ID) })
	cat.Problems, cat.MoreProblems, cat.Truncated = sc.problems, sc.more, sc.trunc
	return cat
}

// item makes the item of g, its files probed.
func (sc *scanner) item(g *group) *Item {
	it := &Item{Root: g.root.conf, Path: g.path(), Title: g.base}
	if sd := g.side; sd != nil {
		it.ID, it.Title, it.named = sd.ID, cmp.Or(sd.Title, it.Title), sd.ID != ""
		f := g.sidef.file()
		it.Sidecar = &f
	}
	if it.ID == "" {
		it.ID = pathID(it.Path)
	}
	add := func(f *File) {
		it.Size += f.Size
		if f.ModTime.After(it.ModTime) {
			it.ModTime = f.ModTime
		}
	}
	firstErr := ""
	for _, m := range g.media {
		r := m.r
		r.Name, r.Default = m.name, m.def
		r.Language = cmp.Or(m.language, firstLanguage(r))
		add(&r.File)
		switch {
		case m.audio || r.Error == "" && !hasVideo(r):
			it.Audio = append(it.Audio, r)
		default:
			it.Renditions = append(it.Renditions, r)
		}
		if r.Error != "" && firstErr == "" {
			firstErr = r.Error
		}
	}
	ok := false
	for _, list := range [][]*Rendition{it.Renditions, it.Audio} {
		for _, r := range list {
			if r.Error == "" && !ok {
				it.Duration = max(it.Duration, r.Duration)
			}
		}
		ok = ok || slices.ContainsFunc(list, func(r *Rendition) bool { return r.Error == "" })
	}
	if !ok {
		it.Error = cmp.Or(firstErr, "no media file: the sidecar names none, and none is named after it")
	}
	for _, m := range g.subs {
		s := &Subtitle{File: m.e.file(), Format: strings.TrimPrefix(strings.ToLower(path.Ext(m.e.rel)), "."),
			Language: m.language, Name: cmp.Or(m.name, m.language), Default: m.def, Forced: m.forced}
		if s.Size > sc.s.subtitleBytes {
			s.Error = fmt.Sprintf("%d bytes, past max_subtitle_bytes, %d", s.Size, sc.s.subtitleBytes)
		}
		add(&s.File)
		it.Subtitles = append(it.Subtitles, s)
	}
	if g.post != nil {
		f := g.post.file()
		it.Poster = &f
		add(&f)
	}
	if it.Sidecar != nil {
		add(it.Sidecar)
	}
	return it
}

func hasVideo(r *Rendition) bool {
	return slices.ContainsFunc(r.Tracks, func(t Track) bool { return t.Kind == KindVideo && t.Codec != "unknown" })
}

func firstLanguage(r *Rendition) string {
	for _, t := range r.Tracks {
		if t.Kind == KindAudio && t.Language != "" && t.Language != "und" {
			return t.Language
		}
	}
	return ""
}

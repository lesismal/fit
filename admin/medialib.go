package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/medialib"
)

// The Admin API's endpoints for the media libraries (I132):
//
//	GET  /media/library                    every library, with its scans' status
//	GET  /media/library/{name}             one library: its status, problems and
//	                                       items, paged (offset, limit) and
//	                                       filtered (q, codec, error,
//	                                       min_height, max_height)
//	GET  /media/library/{name}/items/{id}  one item, whole
//	POST /media/library/{name}/rescan      scan the library again, at once

// The bounds of a page of items.
const (
	defaultItemsPage = 100
	maxItemsPage     = 1000
)

// library is the library the request names, or nil once it has answered
// 404.
func (s *Server) library(w http.ResponseWriter, r *http.Request) *medialib.Library {
	l := medialib.Lookup(s.configs.env.Engine, r.PathValue("name"))
	if l == nil {
		writeError(w, http.StatusNotFound, "no media library named "+strconv.Quote(r.PathValue("name"))+" runs here")
	}
	return l
}

// librarySummary is a library as GET /media/library lists it.
type librarySummary struct {
	Name      string              `json:"name"`
	Roots     []string            `json:"roots"`
	Status    medialib.Status     `json:"status"`
	Scanned   time.Time           `json:"scanned,omitzero"`
	Items     int                 `json:"items"`
	Errors    int                 `json:"errors"`
	Problems  int                 `json:"problems"`
	Truncated string              `json:"truncated,omitempty"`
	Bytes     int64               `json:"bytes"`
	Pool      medialib.PoolStats  `json:"pool"`
	Tables    medialib.TableStats `json:"tables"`
	// Upload is the library's upload_dir and its bounds, nil without one,
	// and Uploads the uploads under way.
	Upload       *medialib.UploadSettings `json:"upload,omitempty"`
	Uploads      int                      `json:"uploads"`
	problemsList []medialib.Problem
}

func summarize(l *medialib.Library) librarySummary {
	cat := l.Catalog()
	errs := 0
	for _, it := range cat.Items {
		if it.Error != "" {
			errs++
		}
	}
	return librarySummary{Name: l.Name(), Roots: l.Roots(), Status: l.Status(), Scanned: cat.Scanned, Items: len(cat.Items), Errors: errs,
		Problems: len(cat.Problems) + cat.MoreProblems, Truncated: cat.Truncated, Bytes: cat.Bytes,
		Pool: l.Pool().Stats(), Tables: l.Tables(), Upload: l.UploadSettings(), Uploads: len(l.Uploads()), problemsList: cat.Problems}
}

// libraries answers GET /media/library.
func (s *Server) libraries(w http.ResponseWriter, _ *http.Request) {
	libs := medialib.Libraries(s.configs.env.Engine)
	out := make([]librarySummary, len(libs))
	for i, l := range libs {
		out[i] = summarize(l)
	}
	writeJSON(w, http.StatusOK, map[string]any{"libraries": out})
}

// itemSummary is an item as a page of GET /media/library/{name} lists it.
type itemSummary struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Root       string    `json:"root"`
	Path       string    `json:"path"`
	Duration   float64   `json:"duration"`
	Size       int64     `json:"size"`
	ModTime    time.Time `json:"modified"`
	Width      int       `json:"width,omitempty"`
	Height     int       `json:"height,omitempty"`
	Codecs     []string  `json:"codecs"`
	Renditions int       `json:"renditions"`
	Audio      int       `json:"audio"`
	Subtitles  int       `json:"subtitles"`
	Poster     bool      `json:"poster"`
	Error      string    `json:"error,omitempty"`
	// Problems is true when a file of the item is listed with an error,
	// the item's own Error or one of its renditions' or subtitles'.
	Problems bool `json:"problems"`
}

func summarizeItem(it *medialib.Item) itemSummary {
	sum := itemSummary{ID: it.ID, Title: it.Title, Root: it.Root, Path: it.Path, Duration: it.Duration.Seconds(), Size: it.Size,
		ModTime: it.ModTime, Codecs: itemCodecs(it), Renditions: len(it.Renditions), Audio: len(it.Audio), Subtitles: len(it.Subtitles),
		Poster: it.Poster != nil, Error: it.Error, Problems: it.Error != ""}
	for _, list := range [][]*medialib.Rendition{it.Renditions, it.Audio} {
		for _, r := range list {
			if r.Width*r.Height > sum.Width*sum.Height {
				sum.Width, sum.Height = r.Width, r.Height
			}
			sum.Problems = sum.Problems || r.Error != ""
		}
	}
	for _, st := range it.Subtitles {
		sum.Problems = sum.Problems || st.Error != ""
	}
	return sum
}

// itemCodecs is the codecs of it's renditions and audio, each once, in the
// order they come.
func itemCodecs(it *medialib.Item) []string {
	out := []string{}
	for _, list := range [][]*medialib.Rendition{it.Renditions, it.Audio} {
		for _, r := range list {
			for _, t := range r.Tracks {
				if t.Codec != "unknown" && !slices.Contains(out, t.Codec) {
					out = append(out, t.Codec)
				}
			}
		}
	}
	return out
}

// libraryPage answers GET /media/library/{name}: the library's status and
// problems, and a page of its items.
func (s *Server) libraryPage(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	q := r.URL.Query()
	offset, limit := 0, defaultItemsPage
	for _, p := range []struct {
		name string
		to   *int
		max  int
	}{{"offset", &offset, -1}, {"limit", &limit, maxItemsPage}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || p.max >= 0 && (n < 1 || n > p.max) {
			writeError(w, http.StatusBadRequest, p.name+" is not a number in range")
			return
		}
		*p.to = n
	}
	minHeight, maxHeight := -1, -1
	for _, p := range []struct {
		name string
		to   *int
	}{{"min_height", &minHeight}, {"max_height", &maxHeight}} {
		if v := q.Get(p.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, p.name+" is not a number of 0 or more")
				return
			}
			*p.to = n
		}
	}
	text, codec := strings.ToLower(q.Get("q")), q.Get("codec")
	var wantErr *bool
	if v := q.Get("error"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			writeError(w, http.StatusBadRequest, "error is true or false")
			return
		}
		wantErr = &b
	}
	sum := summarize(l)
	items := []itemSummary{}
	matched := 0
	for _, it := range l.Catalog().Items {
		is := summarizeItem(it)
		switch {
		case text != "" && !strings.Contains(strings.ToLower(it.ID), text) && !strings.Contains(strings.ToLower(it.Title), text):
			continue
		case codec != "" && !slices.Contains(is.Codecs, codec):
			continue
		case wantErr != nil && is.Problems != *wantErr:
			continue
		case minHeight >= 0 && is.Height < minHeight, maxHeight >= 0 && is.Height > maxHeight:
			continue
		}
		if matched >= offset && len(items) < limit {
			items = append(items, is)
		}
		matched++
	}
	writeJSON(w, http.StatusOK, struct {
		librarySummary
		ProblemsList []medialib.Problem `json:"problem_list"`
		Matched      int                `json:"matched"`
		Offset       int                `json:"offset"`
		Limit        int                `json:"limit"`
		Page         []itemSummary      `json:"page"`
	}{sum, append([]medialib.Problem{}, sum.problemsList...), matched, offset, limit, items})
}

// libraryItem answers GET /media/library/{name}/items/{id...}: the item,
// whole.
func (s *Server) libraryItem(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	it := l.Item(r.PathValue("id"))
	if it == nil {
		writeError(w, http.StatusNotFound, "no item "+strconv.Quote(r.PathValue("id"))+" in the library")
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// rescan answers POST /media/library/{name}/rescan: a scan of the library,
// asked for at once; queued is false when one was waiting already.
func (s *Server) rescan(w http.ResponseWriter, r *http.Request) {
	l := s.library(w, r)
	if l == nil {
		return
	}
	queued := l.Rescan()
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": queued, "status": l.Status()})
}

package static

import (
	"errors"
	"html"
	"io"
	stdhttp "net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	fibhttp "github.com/lesismal/fib/http"
)

// list answers with an HTML listing of the directory at full, whose URL
// path is urlPath, once it has claimed the request from q. Names that start
// with a dot are left out. It runs on the file pool.
func (s *Static) list(c *fibhttp.Context, urlPath, full string, q *queued) {
	// At most browse_max_entries are read, and one more to tell whether
	// there are more; a directory of millions costs no more than that.
	max := s.BrowseMax
	if max <= 0 {
		max = DefaultBrowseMax
	}
	dir, err := os.Open(full)
	if err != nil {
		if q.claim() {
			respondError(c, statusOf(err))
		}
		return
	}
	entries, err := dir.ReadDir(max + 1)
	dir.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		if q.claim() {
			respondError(c, statusOf(err))
		}
		return
	}
	more := len(entries) > max
	if more {
		entries = entries[:max]
	}
	entries = slices.DeleteFunc(entries, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), ".") })
	// Directories first, each group by name.
	slices.SortFunc(entries, func(a, b os.DirEntry) int {
		if a.IsDir() != b.IsDir() {
			if a.IsDir() {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name(), b.Name())
	})
	title := html.EscapeString("Index of " + urlPath)
	var b strings.Builder
	b.WriteString("<!doctype html>\n<meta charset=\"utf-8\">\n<title>" + title + "</title>\n<h1>" + title + "</h1>\n<table>\n")
	if urlPath != "/" {
		b.WriteString("<tr><td><a href=\"../\">../</a></td><td></td><td></td></tr>\n")
	}
	for _, e := range entries {
		name, size, modified := e.Name(), "", ""
		if e.IsDir() {
			name += "/"
		}
		if info, err := e.Info(); err == nil {
			modified = info.ModTime().UTC().Format("2006-01-02 15:04")
			if !e.IsDir() {
				size = strconv.FormatInt(info.Size(), 10)
			}
		}
		// The href is relative, its one segment escaped, so a name with ? or #
		// or a colon stays a name.
		href := "./" + url.PathEscape(strings.TrimSuffix(name, "/"))
		if e.IsDir() {
			href += "/"
		}
		b.WriteString("<tr><td><a href=\"" + html.EscapeString(href) + "\">" + html.EscapeString(name) + "</a></td><td>" +
			size + "</td><td>" + modified + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	if more {
		b.WriteString("<p>Only the first " + strconv.Itoa(max) + " entries the directory gave are listed.</p>\n")
	}
	if !q.claim() {
		return
	}
	_ = c.WriteResponse(fibhttp.Response{
		StatusCode: stdhttp.StatusOK,
		Header:     stdhttp.Header{"Content-Type": {"text/html; charset=utf-8"}, "Cache-Control": {"no-cache"}},
		Body:       []byte(b.String()),
	})
}

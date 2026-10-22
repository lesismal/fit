package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// dashboardFiles are the dashboard's pages: plain HTML, CSS and
// JavaScript, with nothing fetched from elsewhere, which read the Admin
// API's GET endpoints and write nothing.
//
//go:embed dashboard
var dashboardFiles embed.FS

// dashboardCSP keeps the dashboard to its own files and the API's origin.
const dashboardCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// dashboardHandler serves the dashboard's files under /dashboard/.
func dashboardHandler() http.Handler {
	sub, _ := fs.Sub(dashboardFiles, "dashboard")
	files := http.StripPrefix("/dashboard/", http.FileServerFS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", dashboardCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// isDashboard reports whether r asks for one of the dashboard's files.
func isDashboard(r *http.Request) bool {
	return (r.Method == http.MethodGet || r.Method == http.MethodHead) &&
		(r.URL.Path == "/dashboard" || strings.HasPrefix(r.URL.Path, "/dashboard/"))
}

// sameOriginRead reports whether r, carrying origin, is a read from the
// API's own origin, as the dashboard's requests are: a page elsewhere
// cannot read the answer, and no write is let through this way.
func sameOriginRead(r *http.Request, origin string) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	host, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		host, ok = strings.CutPrefix(origin, "https://")
	}
	return ok && host != "" && strings.EqualFold(host, r.Host)
}

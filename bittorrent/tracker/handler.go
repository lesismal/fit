package tracker

import (
	stdhttp "net/http"
	"net/netip"
	"strings"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/bencode"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".bt_tracker", New: func() any { return new(Handler) },
		Doc: "Serves the bittorrent.tracker role's HTTP announces and scrapes (BEP 3, 23, 7 and 48): a request whose path ends in announce is an announce, and one ending in scrape a scrape, whatever comes before, so that /announce, /tracker/announce and a private tracker's /<passkey>/announce all work. Refusals are answered 200 with a failure reason, as BEP 3 has them; 503 while the role does not run. The route's listeners, TLS and middleware apply as to any handler; the passkey is replaced by REDACTED in the access log."})
}

// Handler is the bt_tracker handler: it has no params.
type Handler struct {
	engine *fib.Engine
}

// Provision finds the engine the role is looked up by.
func (h *Handler) Provision(ctx *module.Context) error {
	h.engine = ctx.Engine()
	return nil
}

// Inline says the handler answers every request before it returns.
func (h *Handler) Inline() {}

// ServeHTTP serves the request through the role of the handler's engine.
func (h *Handler) ServeHTTP(c *fibhttp.Context) {
	r := Lookup(h.engine)
	if r == nil {
		redact(c.Request, true)
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("the bittorrent.tracker role is not running\n"))
		return
	}
	r.ServeHTTP(c)
}

// ServeHTTP serves an HTTP announce or scrape, on the worker that read it:
// its client address checked against the role's access, then fib's
// tracker. The passkey leaves the request's RequestURI, which the access
// log writes, before anything is answered.
func (r *Role) ServeHTTP(c *fibhttp.Context) {
	// A public tracker's path holds no passkey, but may hold a name.
	redact(c.Request, r.auth.Load() != nil)
	if !r.gate.Allows(remoteOf(c)) {
		r.refused[refusedAccess].Add(1)
		failure(c, "refused")
		return
	}
	s := r.cur.Load()
	if s == nil {
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("the bittorrent.tracker role is not running\n"))
		return
	}
	s.srv.ServeHTTP(c)
}

// failure answers a refusal as BEP 3 has it: 200, with a failure reason.
func failure(c *fibhttp.Context, reason string) {
	body, _ := bencode.Marshal(map[string]string{"failure reason": reason})
	_ = c.Respond(stdhttp.StatusOK, "text/plain", body)
}

// remoteOf returns the address a request came from, as fib's tracker
// takes it.
func remoteOf(c *fibhttp.Context) netip.AddrPort {
	if c.Conn != nil {
		if ap := c.Conn.RemoteAddrPort(); ap.IsValid() {
			return ap
		}
	}
	ap, _ := netip.ParseAddrPort(c.Request.RemoteAddr)
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// redact replaces the passkey of r's RequestURI by REDACTED: its passkey
// parameter and, with path, the path segment before its last announce or
// scrape one. r.URL keeps it, for the tracker to read.
func redact(r *stdhttp.Request, path bool) {
	if out, ok := redacted(r.RequestURI, path); ok {
		r.RequestURI = out
	}
}

// redacted returns uri with its passkey replaced, and whether it had one.
func redacted(uri string, inPath bool) (string, bool) {
	path, query, hasQuery := strings.Cut(uri, "?")
	changed := false
	// The segment before the last one, when the last begins with announce
	// or scrape.
	if i := strings.LastIndexByte(path, '/'); inPath && i > 0 {
		last := path[i+1:]
		if strings.HasPrefix(last, "announce") || strings.HasPrefix(last, "scrape") {
			j := strings.LastIndexByte(path[:i], '/')
			if seg := path[j+1 : i]; seg != "" && seg != "REDACTED" {
				path = path[:j+1] + "REDACTED" + path[i:]
				changed = true
			}
		}
	}
	if hasQuery {
		parts := strings.Split(query, "&")
		for k, kv := range parts {
			if name, v, _ := strings.Cut(kv, "="); name == "passkey" && v != "REDACTED" {
				parts[k] = "passkey=REDACTED"
				changed = true
			}
		}
		query = strings.Join(parts, "&")
	}
	if !changed {
		return uri, false
	}
	if hasQuery {
		return path + "?" + query, true
	}
	return path, true
}

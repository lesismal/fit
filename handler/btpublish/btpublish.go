// Package btpublish is the bt_publish handler: it serves what the
// bittorrent.client role publishes, an index of the publish entries and
// their torrents in JSON, each torrent's .torrent file and its magnet link.
// See docs/bittorrent.md.
package btpublish

import (
	"encoding/json"
	"mime"
	stdhttp "net/http"
	"slices"
	"strings"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/module"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".bt_publish", New: func() any { return new(Publish) },
		Doc: "Serves what the BitTorrent client publishes: at a path ending in / or index.json an index of its publish entries and their torrents in JSON, at <id>.torrent a torrent's .torrent file and at <id>.magnet its magnet link; 503 while no bittorrent.client runs."})
}

// Publish is the handler bt_publish.
type Publish struct {
	Publish []string `json:"publish,omitempty" doc:"The publish entries of the bittorrent.client served, by name; a name the client has not is listed in the index with its error. Every entry when left out."`

	engine *fib.Engine
}

// Provision checks the params.
func (h *Publish) Provision(ctx *module.Context) error {
	for i, n := range h.Publish {
		if n == "" || slices.Index(h.Publish, n) < i {
			return ctx.Params.Field("publish").Index(i).Errorf("%q is empty or listed twice", n)
		}
	}
	h.engine = ctx.Engine()
	return nil
}

// index is the body of the index.
type index struct {
	Publish []bittorrent.PublishStatus `json:"publish"`
}

// ServeHTTP answers from the role's snapshots, which it reads without a
// lock: nothing here waits.
func (h *Publish) ServeHTTP(c *fibhttp.Context) {
	r := c.Request
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		h.fail(c, stdhttp.StatusMethodNotAllowed, "GET or HEAD", "Allow", "GET, HEAD")
		return
	}
	role := bittorrent.Lookup(h.engine)
	if role == nil || !role.ClientRunning() {
		h.fail(c, stdhttp.StatusServiceUnavailable, "the bittorrent.client role does not run: publishing needs it, with publish entries in its config")
		return
	}
	p := r.URL.Path
	last := p[strings.LastIndexByte(p, '/')+1:]
	switch {
	case last == "" || last == "index.json":
		list := role.Publishing(h.Publish)
		for i := range list {
			for j := range list[i].Torrents {
				t := &list[i].Torrents[j]
				if t.Error == "" {
					t.TorrentURL, t.MagnetURL = t.ID+".torrent", t.ID+".magnet"
				}
			}
		}
		b, err := json.Marshal(index{Publish: list})
		if err != nil {
			h.fail(c, stdhttp.StatusInternalServerError, err.Error())
			return
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: stdhttp.Header{
			"Content-Type": {"application/json"}, "Cache-Control": {"no-cache"}}, Body: append(b, '\n')})
	case strings.HasSuffix(last, ".torrent"):
		torrent, name, _, ok := role.PublishedTorrent(h.Publish, strings.TrimSuffix(last, ".torrent"))
		if !ok {
			h.fail(c, stdhttp.StatusNotFound, "no such published torrent")
			return
		}
		hdr := stdhttp.Header{"Content-Type": {"application/x-bittorrent"}, "Cache-Control": {"no-cache"}}
		if d := mime.FormatMediaType("attachment", map[string]string{"filename": name + ".torrent"}); d != "" {
			hdr.Set("Content-Disposition", d)
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: torrent})
	case strings.HasSuffix(last, ".magnet"):
		_, _, magnet, ok := role.PublishedTorrent(h.Publish, strings.TrimSuffix(last, ".magnet"))
		if !ok {
			h.fail(c, stdhttp.StatusNotFound, "no such published torrent")
			return
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: stdhttp.Header{
			"Content-Type": {"text/plain; charset=utf-8"}, "Cache-Control": {"no-cache"}}, Body: []byte(magnet + "\n")})
	default:
		h.fail(c, stdhttp.StatusNotFound, "ask for index.json, <id>.torrent or <id>.magnet")
	}
}

// fail answers status with msg in JSON, and extra headers in pairs.
func (h *Publish) fail(c *fibhttp.Context, status int, msg string, extra ...string) {
	hdr := stdhttp.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
	for i := 0; i+1 < len(extra); i += 2 {
		hdr.Set(extra[i], extra[i+1])
	}
	b, _ := json.Marshal(map[string]string{"error": msg})
	_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: hdr, Body: append(b, '\n')})
}

var _ module.Provisioner = (*Publish)(nil)

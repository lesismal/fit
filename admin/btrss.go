package admin

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/lesismal/fit/bittorrent"
)

// The Admin API's endpoints for the BitTorrent client's RSS auto-download
// (I208), which fit bt rss and the pages call:
//
//	GET  /bittorrent/rss                     the feeds (last poll, result,
//	                                         counts) and the rules
//	GET  /bittorrent/rss/items               the items seen, the newest
//	                                         first: {"items", "total"},
//	                                         filtered by feed and outcome,
//	                                         at most limit (100, at most
//	                                         1000)
//	POST /bittorrent/rss/feeds/{name}/poll   poll a feed now, enabled or
//	                                         not, and answer what the poll
//	                                         did; 409 while it is polled
//	PUT  /bittorrent/rss/feeds/{name}        {"enabled": true | false |
//	                                         null}: turn its polling on or
//	                                         off, or back to the config
//	PUT  /bittorrent/rss/rules/{name}        the same for a rule
//
// Like the other BitTorrent endpoints they run on the request's goroutine,
// never a fib worker; a poll waits for the feed and the adds.

// rssOutcomes are the outcomes the items may be filtered by.
var rssOutcomes = []string{bittorrent.RSSAdded, bittorrent.RSSExists, bittorrent.RSSDuplicate, bittorrent.RSSUnmatched,
	bittorrent.RSSNoSource, bittorrent.RSSFailed}

// btRSS answers GET /bittorrent/rss.
func (s *Server) btRSS(w http.ResponseWriter, _ *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	writeJSON(w, http.StatusOK, role.RSS())
}

// btRSSItems answers GET /bittorrent/rss/items.
func (s *Server) btRSSItems(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	q := r.URL.Query()
	outcome := q.Get("outcome")
	if outcome != "" && !slices.Contains(rssOutcomes, outcome) {
		writeError(w, http.StatusBadRequest, "outcome is one of "+joinQuoted(rssOutcomes))
		return
	}
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "limit is 1 to 1000")
			return
		}
		limit = n
	}
	items, total, err := role.RSSItems(q.Get("feed"), outcome, limit)
	if err != nil {
		btRSSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

// btRSSPoll answers POST /bittorrent/rss/feeds/{name}/poll.
func (s *Server) btRSSPoll(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	res, err := role.PollFeed(r.PathValue("name"))
	if err != nil {
		btRSSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// btRSSToggle answers PUT /bittorrent/rss/feeds/{name} and
// /bittorrent/rss/rules/{name}: {"enabled"}.
func (s *Server) btRSSToggle(rule bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role := s.btRole(w)
		if role == nil {
			return
		}
		var b map[string]json.RawMessage
		if !s.readBTBody(w, r, &b) {
			return
		}
		raw, ok := b["enabled"]
		if !ok || len(b) != 1 {
			writeError(w, http.StatusBadRequest, `the body is {"enabled": true | false | null}`)
			return
		}
		var on *bool
		if err := json.Unmarshal(raw, &on); err != nil {
			writeError(w, http.StatusBadRequest, `enabled is true, false or null`)
			return
		}
		name := r.PathValue("name")
		var err error
		if rule {
			err = role.SetRuleEnabled(name, on)
		} else {
			err = role.SetFeedEnabled(name, on)
		}
		if err != nil {
			btRSSError(w, err)
			return
		}
		st := role.RSS()
		if rule {
			for _, x := range st.Rules {
				if x.Name == name {
					writeJSON(w, http.StatusOK, x)
					return
				}
			}
		} else {
			for _, x := range st.Feeds {
				if x.Name == name {
					writeJSON(w, http.StatusOK, x)
					return
				}
			}
		}
		writeJSON(w, http.StatusOK, st)
	}
}

// btRSSError answers err, an error of the role's RSS API.
func btRSSError(w http.ResponseWriter, err error) {
	switch err {
	case bittorrent.ErrRSSNoFeed, bittorrent.ErrRSSNoRule:
		writeError(w, http.StatusNotFound, err.Error())
	case bittorrent.ErrRSSBusy:
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusConflict, err.Error())
	default:
		btError(w, err)
	}
}

// joinQuoted lists names quoted, with commas.
func joinQuoted(names []string) string {
	s := ""
	for i, n := range names {
		if i > 0 {
			s += ", "
		}
		s += strconv.Quote(n)
	}
	return s
}

package admin

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/bittorrent/tracker"
	"github.com/lesismal/fit/config"
)

// The Admin API's endpoints for the BitTorrent roles (I203), which fit bt
// and the pages call:
//
//	GET    /bittorrent/torrents                 the torrents, in queue order:
//	                                            {"torrents": [...], "total"},
//	                                            filtered by state (a list
//	                                            with commas), category, q (in
//	                                            the name) and paged by offset
//	                                            and limit
//	POST   /bittorrent/torrents                 add one: JSON {"magnet"|"url"|
//	                                            "torrent" (base64), "category",
//	                                            "paused", "sequential",
//	                                            "priorities", "peers",
//	                                            "limits"}; a .torrent file as
//	                                            application/x-bittorrent, the
//	                                            options in the query; or a
//	                                            form, multipart/form-data, its
//	                                            file in the part torrent
//	GET    /bittorrent/torrents/{id}            one, with its files
//	POST   /bittorrent/torrents/{id}            an action: {"action": "pause"
//	                                            | "resume" | "recheck"}
//	PATCH  /bittorrent/torrents/{id}            change it: {"sequential",
//	                                            "category", "limits" (null
//	                                            clears them), "queue_position",
//	                                            "priorities": [{"index",
//	                                            "priority"}], "paused"}
//	DELETE /bittorrent/torrents/{id}            remove it; ?delete_data=1 with
//	                                            its files
//	GET    /bittorrent/torrents/{id}/files      its files
//	GET    /bittorrent/torrents/{id}/peers      the peers connected
//	GET    /bittorrent/torrents/{id}/trackers   its trackers and web seeds
//	GET    /bittorrent/alt_speed                the alternative rates; PUT
//	                                            {"mode": "auto" | "on" | "off"}
//	GET    /bittorrent/publish                  the publish entries and their
//	                                            torrents; .../{name} one
//	GET    /bittorrent/events                   the role's events, as
//	                                            server-sent events
//	GET    /bittorrent/tracker/stats            the tracker's totals
//	GET    /bittorrent/tracker/torrents         the allow-list's swarms (offset,
//	                                            limit); .../{info hash} one
//	                                            torrent's swarm
//	GET    /bittorrent/tracker/passkey?user=    a user's passkey
//
// A torrent's {id} is the role's ID, its v1 info hash in hex, or its v2
// hash, whose first 20 bytes are the ID. The role's API reads and writes its
// state files and may wait for fib's disk pool: it is called on the
// request's own goroutine, net/http's here and the console's pool there,
// never on a fib worker.

// The bounds of the BitTorrent endpoints.
const (
	// maxUploadBytes bounds a .torrent file sent to POST
	// /bittorrent/torrents, as one downloaded by URL is bounded.
	maxUploadBytes = bittorrent.MaxURLTorrentBytes
	// maxAddJSONBytes bounds the JSON body of an add, a .torrent file in
	// base64 among it.
	maxAddJSONBytes = maxUploadBytes/3*4 + 1<<20
	// maxFormFieldBytes bounds a form's field other than its file.
	maxFormFieldBytes = 64 << 10
	// maxFormParts bounds a form's parts.
	maxFormParts = 16
	// maxPatchBytes bounds the body of the other changes.
	maxPatchBytes = 1 << 20
	// defaultTorrentsPage and maxTorrentsPage bound a page of torrents.
	defaultTorrentsPage = 1000
	maxTorrentsPage     = 10000
)

// btHeartbeat is how often the event stream writes a comment when it has
// no event to send, so that proxies and clients see it alive; a variable
// for tests.
var btHeartbeat = 15 * time.Second

// btRole is the BitTorrent client role, or nil once it has answered 404.
func (s *Server) btRole(w http.ResponseWriter) *bittorrent.Role {
	r := bittorrent.Lookup(s.configs.env.Engine)
	if r == nil {
		writeError(w, http.StatusNotFound, "the bittorrent.client role does not run here")
	}
	return r
}

// btTracker is the tracker role, or nil once it has answered 404.
func (s *Server) btTracker(w http.ResponseWriter) *tracker.Role {
	r := tracker.Lookup(s.configs.env.Engine)
	if r == nil {
		writeError(w, http.StatusNotFound, "the bittorrent.tracker role does not run here")
	}
	return r
}

// btError answers err, an error of the role's API.
func btError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, bittorrent.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, bittorrent.ErrExists), errors.Is(err, bittorrent.ErrBroken):
		status = http.StatusConflict
	case errors.Is(err, bittorrent.ErrTooMany), errors.Is(err, bittorrent.ErrTooManyTemporary):
		status = http.StatusTooManyRequests
	case errors.Is(err, bittorrent.ErrBusy), errors.Is(err, bittorrent.ErrTooManySubscribers):
		w.Header().Set("Retry-After", "5")
		status = http.StatusServiceUnavailable
	case errors.Is(err, bittorrent.ErrNotRunning), errors.Is(err, tracker.ErrNotRunning):
		w.Header().Set("Retry-After", "5")
		status = http.StatusServiceUnavailable
	case errors.Is(err, bittorrent.ErrBadRequest):
		status = http.StatusBadRequest
	case errors.Is(err, bittorrent.ErrFetch):
		status = http.StatusBadGateway
	}
	writeError(w, status, err.Error())
}

// torrentID returns the ID the request's {id} names: 40 hex digits, or 64
// of a v2 hash, cut to 40.
func torrentID(w http.ResponseWriter, r *http.Request) (string, bool) {
	h, ok := config.ParseInfoHash(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "a torrent's ID is its info hash: 40 hex digits (v1) or 64 (v2)")
		return "", false
	}
	return hex.EncodeToString(h[:]), true
}

// btListBody is GET /bittorrent/torrents's answer.
type btListBody struct {
	Torrents []bittorrent.Status `json:"torrents"`
	// Total counts the torrents the filters match, Offset is the first
	// one's place among them.
	Total  int `json:"total"`
	Offset int `json:"offset"`
}

// btTorrents answers GET /bittorrent/torrents.
func (s *Server) btTorrents(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	q := r.URL.Query()
	offset, limit, ok := pageOf(w, q, defaultTorrentsPage, maxTorrentsPage)
	if !ok {
		return
	}
	var states []string
	if v := q.Get("state"); v != "" {
		for _, st := range strings.Split(v, ",") {
			st = strings.TrimSpace(st)
			if !slices.Contains(bittorrent.States, st) {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("a state is one of %s, not %q", strings.Join(bittorrent.States, ", "), st))
				return
			}
			states = append(states, st)
		}
	}
	_, byCategory := q["category"]
	category := q.Get("category")
	text := strings.ToLower(q.Get("q"))
	all := role.List()
	match := make([]bittorrent.Status, 0, len(all))
	for _, st := range all {
		if len(states) > 0 && !slices.Contains(states, st.State) ||
			byCategory && st.Category != category ||
			text != "" && !strings.Contains(strings.ToLower(st.Name), text) {
			continue
		}
		match = append(match, st)
	}
	out := btListBody{Torrents: []bittorrent.Status{}, Total: len(match), Offset: offset}
	if offset < len(match) {
		out.Torrents = match[offset:min(offset+limit, len(match))]
	}
	writeJSON(w, http.StatusOK, out)
}

// btLimits are a torrent's limits as the endpoints take them: seed_time is
// a duration ("12h", "-1s" to seed for ever) or nanoseconds, as a status
// reports it.
type btLimits struct {
	UploadRate   int             `json:"upload_rate"`
	DownloadRate int             `json:"download_rate"`
	MaxConns     int             `json:"max_conns"`
	UploadSlots  int             `json:"upload_slots"`
	SeedRatio    float64         `json:"seed_ratio"`
	SeedTime     json.RawMessage `json:"seed_time"`
}

func (l *btLimits) limits() (bittorrent.TorrentLimits, error) {
	out := bittorrent.TorrentLimits{UploadRate: l.UploadRate, DownloadRate: l.DownloadRate, MaxConns: l.MaxConns,
		UploadSlots: l.UploadSlots, SeedRatio: l.SeedRatio}
	if len(l.SeedTime) == 0 || string(l.SeedTime) == "null" {
		return out, nil
	}
	var s string
	if json.Unmarshal(l.SeedTime, &s) == nil {
		d, err := time.ParseDuration(s)
		if err != nil {
			return out, fmt.Errorf("%w: seed_time %q is not a duration, such as \"12h\"", bittorrent.ErrBadRequest, s)
		}
		out.SeedTime = config.Duration(d)
		return out, nil
	}
	var n int64
	if err := json.Unmarshal(l.SeedTime, &n); err != nil {
		return out, fmt.Errorf("%w: seed_time is a duration, such as \"12h\", or nanoseconds", bittorrent.ErrBadRequest)
	}
	out.SeedTime = config.Duration(n)
	return out, nil
}

// btAddBody is POST /bittorrent/torrents's JSON body.
type btAddBody struct {
	Magnet     string    `json:"magnet"`
	URL        string    `json:"url"`
	Torrent    []byte    `json:"torrent"`
	Category   string    `json:"category"`
	Paused     bool      `json:"paused"`
	Sequential bool      `json:"sequential"`
	Priorities []int     `json:"priorities"`
	Peers      []string  `json:"peers"`
	Limits     *btLimits `json:"limits"`
}

// btAdd answers POST /bittorrent/torrents.
func (s *Server) btAdd(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	var body btAddBody
	mt, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mt {
	case "application/json":
		data, ok := readBounded(w, r, maxAddJSONBytes, "the body")
		if !ok {
			return
		}
		if err := json.Unmarshal(data, &body); err != nil {
			writeError(w, http.StatusBadRequest, "the body: "+err.Error())
			return
		}
	case "application/x-bittorrent":
		data, ok := readBounded(w, r, maxUploadBytes, "the .torrent file")
		if !ok {
			return
		}
		body.Torrent = data
		if !queryOptions(w, r, &body) {
			return
		}
	case "multipart/form-data":
		if !s.readAddForm(w, r, params["boundary"], &body) {
			return
		}
	default:
		writeError(w, http.StatusUnsupportedMediaType, "the body must be sent as application/json, application/x-bittorrent or multipart/form-data")
		return
	}
	req := bittorrent.AddRequest{Magnet: body.Magnet, Torrent: body.Torrent, Category: body.Category, Paused: body.Paused,
		Sequential: body.Sequential, Priorities: body.Priorities, Peers: body.Peers}
	if body.Limits != nil {
		l, err := body.Limits.limits()
		if err != nil {
			btError(w, err)
			return
		}
		req.Limits = &l
	}
	n := 0
	for _, set := range []bool{body.Magnet != "", body.URL != "", len(body.Torrent) > 0} {
		if set {
			n++
		}
	}
	if n != 1 {
		writeError(w, http.StatusBadRequest, "give one of magnet, url and torrent")
		return
	}
	var st bittorrent.Status
	var err error
	if body.URL != "" {
		req.Magnet = ""
		st, err = role.AddURL(body.URL, req)
	} else {
		st, err = role.Add(req)
	}
	if err != nil {
		btError(w, err)
		return
	}
	w.Header().Set("Location", "/bittorrent/torrents/"+st.ID)
	writeJSON(w, http.StatusCreated, st)
}

// readBounded reads the request's body, at most limit bytes; what is the
// body's name in the errors.
func readBounded(w http.ResponseWriter, r *http.Request, limit int64, what string) ([]byte, bool) {
	if r.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("%s is larger than %d bytes", what, limit))
		return nil, false
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("%s is larger than %d bytes", what, limit))
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "reading the body: "+err.Error())
		return nil, false
	}
	return data, true
}

// boolValue reads a flag of a query or a form: 1, true, 0, false or none.
func boolValue(name, v string) (bool, error) {
	switch v {
	case "", "0", "false":
		return false, nil
	case "1", "true", "on":
		return true, nil
	}
	return false, fmt.Errorf("%s is 1 or true, 0 or false, not %q", name, v)
}

// queryOptions reads an add's options from the query: category, paused,
// sequential.
func queryOptions(w http.ResponseWriter, r *http.Request, body *btAddBody) bool {
	q := r.URL.Query()
	body.Category = q.Get("category")
	var err error
	if body.Paused, err = boolValue("paused", q.Get("paused")); err == nil {
		body.Sequential, err = boolValue("sequential", q.Get("sequential"))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// readAddForm reads an add's form: the file in the part torrent, and the
// fields magnet, url, category, paused and sequential, each bounded, the
// parts read as they arrive.
func (s *Server) readAddForm(w http.ResponseWriter, r *http.Request, boundary string, body *btAddBody) bool {
	if boundary == "" {
		writeError(w, http.StatusBadRequest, "the form has no boundary")
		return false
	}
	mr := multipart.NewReader(http.MaxBytesReader(w, r.Body, maxUploadBytes+maxFormParts*maxFormFieldBytes), boundary)
	fail := func(status int, msg string) bool {
		writeError(w, status, msg)
		return false
	}
	for n := 0; ; n++ {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return fail(http.StatusRequestEntityTooLarge, "the form is too large")
			}
			return fail(http.StatusBadRequest, "reading the form: "+err.Error())
		}
		if n >= maxFormParts {
			return fail(http.StatusBadRequest, fmt.Sprintf("the form has more than %d parts", maxFormParts))
		}
		limit := int64(maxFormFieldBytes)
		if p.FormName() == "torrent" {
			limit = maxUploadBytes
		}
		data, err := io.ReadAll(io.LimitReader(p, limit+1))
		_ = p.Close()
		if err != nil {
			return fail(http.StatusBadRequest, "reading the form: "+err.Error())
		}
		if int64(len(data)) > limit {
			return fail(http.StatusRequestEntityTooLarge, fmt.Sprintf("the form's %s is larger than %d bytes", p.FormName(), limit))
		}
		v := strings.TrimSpace(string(data))
		switch p.FormName() {
		case "torrent":
			if len(data) > 0 {
				body.Torrent = data
			}
		case "magnet":
			body.Magnet = v
		case "url":
			body.URL = v
		case "category":
			body.Category = v
		case "paused":
			body.Paused, err = boolValue("paused", v)
		case "sequential":
			body.Sequential, err = boolValue("sequential", v)
		}
		if err != nil {
			return fail(http.StatusBadRequest, err.Error())
		}
	}
	return true
}

// btTorrent answers GET /bittorrent/torrents/{id}.
func (s *Server) btTorrent(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	st, err := role.Get(id)
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// btActionBody is POST /bittorrent/torrents/{id}'s body.
type btActionBody struct {
	Action string `json:"action"`
}

// btAction answers POST /bittorrent/torrents/{id}.
func (s *Server) btAction(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	var a btActionBody
	if !s.readBTBody(w, r, &a) {
		return
	}
	var err error
	switch a.Action {
	case "pause":
		err = role.Pause(id)
	case "resume":
		err = role.Resume(id)
	case "recheck":
		err = role.Recheck(id)
	default:
		writeError(w, http.StatusBadRequest, "the action is pause, resume or recheck, not "+strconv.Quote(a.Action))
		return
	}
	s.btAnswer(w, role, id, err)
}

// btAnswer answers a change of the torrent id with its status, or err.
func (s *Server) btAnswer(w http.ResponseWriter, role *bittorrent.Role, id string, err error) {
	if err != nil {
		btError(w, err)
		return
	}
	st, err := role.Get(id)
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// readBTBody decodes the request's JSON body into v; an empty body leaves
// v as it is.
func (s *Server) readBTBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mt != "application/json" && mt != "application/merge-patch+json") {
		writeError(w, http.StatusUnsupportedMediaType, "the body must be sent as application/json")
		return false
	}
	data, ok := readBounded(w, r, maxPatchBytes, "the body")
	if !ok {
		return false
	}
	if err := json.Unmarshal(data, v); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return false
	}
	return true
}

// btFilePriority is a file's priority in a PATCH.
type btFilePriority struct {
	Index    int `json:"index"`
	Priority int `json:"priority"`
}

// btPatchBody is PATCH /bittorrent/torrents/{id}'s body: what it leaves
// out stays.
type btPatchBody struct {
	Paused        *bool            `json:"paused"`
	Sequential    *bool            `json:"sequential"`
	Category      *string          `json:"category"`
	Limits        json.RawMessage  `json:"limits"`
	QueuePosition *int             `json:"queue_position"`
	Priorities    []btFilePriority `json:"priorities"`
}

// btPatch answers PATCH /bittorrent/torrents/{id}: the changes are made in
// the order of btPatchBody's fields, each saved; the first that fails is
// answered, those before it made.
func (s *Server) btPatch(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	var b btPatchBody
	if !s.readBTBody(w, r, &b) {
		return
	}
	// The limits are read first: a bad one changes nothing.
	var limits *bittorrent.TorrentLimits
	if len(b.Limits) > 0 {
		var l bittorrent.TorrentLimits
		if string(b.Limits) != "null" {
			var bl btLimits
			if err := json.Unmarshal(b.Limits, &bl); err != nil {
				writeError(w, http.StatusBadRequest, "limits: "+err.Error())
				return
			}
			var err error
			if l, err = bl.limits(); err != nil {
				btError(w, err)
				return
			}
		}
		limits = &l
	}
	var steps []func() error
	if b.Paused != nil {
		if *b.Paused {
			steps = append(steps, func() error { return role.Pause(id) })
		} else {
			steps = append(steps, func() error { return role.Resume(id) })
		}
	}
	if b.Sequential != nil {
		steps = append(steps, func() error { return role.SetSequential(id, *b.Sequential) })
	}
	if b.Category != nil {
		steps = append(steps, func() error { return role.SetCategory(id, *b.Category) })
	}
	if limits != nil {
		steps = append(steps, func() error { return role.SetTorrentLimits(id, *limits) })
	}
	if b.QueuePosition != nil {
		steps = append(steps, func() error { return role.SetQueuePosition(id, *b.QueuePosition) })
	}
	for _, p := range b.Priorities {
		steps = append(steps, func() error { return role.SetFilePriority(id, p.Index, p.Priority) })
	}
	for _, step := range steps {
		if err := step(); err != nil {
			btError(w, err)
			return
		}
	}
	s.btAnswer(w, role, id, nil)
}

// btRemove answers DELETE /bittorrent/torrents/{id}.
func (s *Server) btRemove(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	del, err := boolValue("delete_data", r.URL.Query().Get("delete_data"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := role.Remove(id, del); err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": id, "data_deleted": del})
}

// btFiles answers GET /bittorrent/torrents/{id}/files.
func (s *Server) btFiles(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	st, err := role.Get(id)
	if err != nil {
		btError(w, err)
		return
	}
	if st.Files == nil {
		st.Files = []bittorrent.FileStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": st.Files})
}

// btPeers answers GET /bittorrent/torrents/{id}/peers.
func (s *Server) btPeers(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	peers, err := role.Peers(id)
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
}

// btTrackers answers GET /bittorrent/torrents/{id}/trackers.
func (s *Server) btTrackers(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id, ok := torrentID(w, r)
	if !ok {
		return
	}
	trackers, seeds, err := role.Trackers(id)
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trackers": trackers, "web_seeds": seeds})
}

// btAltSpeed answers GET /bittorrent/alt_speed.
func (s *Server) btAltSpeed(w http.ResponseWriter, _ *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	st, err := role.AltSpeed()
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// btSetAltSpeed answers PUT /bittorrent/alt_speed: {"mode"}.
func (s *Server) btSetAltSpeed(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	var b struct {
		Mode string `json:"mode"`
	}
	if !s.readBTBody(w, r, &b) {
		return
	}
	if err := role.SetAltSpeed(b.Mode); err != nil {
		btError(w, err)
		return
	}
	s.btAltSpeed(w, r)
}

// btPublish answers GET /bittorrent/publish and /bittorrent/publish/{name}.
func (s *Server) btPublish(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusOK, map[string]any{"entries": role.Publishing(nil)})
		return
	}
	st := role.Publishing([]string{name})[0]
	if st.Error != "" && st.Dir == "" {
		writeError(w, http.StatusNotFound, "no publish entry is named "+strconv.Quote(name))
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// btPublishedTorrent answers GET /bittorrent/publish/{name}/torrents/{id}:
// the .torrent file of a publish entry's torrent, which the pages offer to
// download (I204). It is read from the role's snapshot, without a lock.
func (s *Server) btPublishedTorrent(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	id := strings.ToLower(r.PathValue("id"))
	raw, name, _, ok := role.PublishedTorrent([]string{r.PathValue("name")}, id)
	if !ok {
		writeError(w, http.StatusNotFound, "no torrent "+strconv.Quote(id)+" is published by "+strconv.Quote(r.PathValue("name")))
		return
	}
	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".torrent"}))
	w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

// btTrackerStats answers GET /bittorrent/tracker/stats.
func (s *Server) btTrackerStats(w http.ResponseWriter, _ *http.Request) {
	t := s.btTracker(w)
	if t == nil {
		return
	}
	st, err := t.Stats()
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// btTrackerTorrents answers GET /bittorrent/tracker/torrents.
func (s *Server) btTrackerTorrents(w http.ResponseWriter, r *http.Request) {
	t := s.btTracker(w)
	if t == nil {
		return
	}
	q := r.URL.Query()
	offset, limit, ok := pageOf(w, q, tracker.MaxListed, tracker.MaxListed)
	if !ok {
		return
	}
	list, total, err := t.Torrents(offset, limit)
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"torrents": list, "total": total, "offset": offset})
}

// btTrackerTorrent answers GET /bittorrent/tracker/torrents/{hash}.
func (s *Server) btTrackerTorrent(w http.ResponseWriter, r *http.Request) {
	t := s.btTracker(w)
	if t == nil {
		return
	}
	h, ok := config.ParseInfoHash(r.PathValue("hash"))
	if !ok {
		writeError(w, http.StatusBadRequest, "not an info hash: 40 hex digits (v1) or 64 (v2)")
		return
	}
	st, err := t.Torrent(h)
	if err != nil {
		btError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// btPasskey answers GET /bittorrent/tracker/passkey?user=.
func (s *Server) btPasskey(w http.ResponseWriter, r *http.Request) {
	t := s.btTracker(w)
	if t == nil {
		return
	}
	user := r.URL.Query().Get("user")
	if user == "" {
		writeError(w, http.StatusBadRequest, "name the user: ?user=")
		return
	}
	k, err := t.Passkey(user)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": user, "passkey": k})
}

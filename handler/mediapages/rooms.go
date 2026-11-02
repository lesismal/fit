package mediapages

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	stdhttp "net/http"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
)

// The room API, below prefix/api/rooms/<room>/:
//
//   - POST join: {name, key?, audio, video, audioDevice, videoDevice,
//     screen, publishing} joins the room as name, or takes the name back
//     with the key it was given; answered {key, publish, ttlMs,
//     heartbeatMs}. It needs play on <room>-*, and announcing publishing
//     needs publish on <room>-<name>.
//   - POST members/<name>: {key, ...the state} changes the member's state
//     and is its heartbeat; 404 once the member is gone, which the page
//     answers by joining again.
//   - POST leave: {name, key} leaves; a page's unload sends it as a beacon.
//   - GET events: Server-Sent Events, "room" with the room's members
//     whenever they change, and a comment as a ping every heartbeat. It
//     needs play on <room>-*.
//
// The key, 128 random bits only its member knows, is what lets it change
// its state, as a WHIP session's URL lets its peer end it.

// roomName and memberName are what the names of rooms and members may be:
// together, <room>-<name>, they are a stream's name of whip and whep.
var (
	roomName   = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)
	memberName = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,63}$`)
)

// state is what a member announces.
type state struct {
	Audio       bool `json:"audio"`
	Video       bool `json:"video"`
	AudioDevice bool `json:"audioDevice"`
	VideoDevice bool `json:"videoDevice"`
	Screen      bool `json:"screen"`
	Publishing  bool `json:"publishing"`
}

// member is a member of a room.
type member struct {
	name    string
	key     string
	publish bool // may publish <room>-<name>
	st      state
	joined  int64 // the room's join count when it joined, its place in the list
	seen    time.Time
}

// memberJSON is a member as the events show it.
type memberJSON struct {
	Name string `json:"name"`
	state
}

// room is a room's members and watchers.
type room struct {
	name     string
	members  map[string]*member
	watchers map[*watcher]struct{}
	joins    int64
	seq      uint64
	snap     *snapshot
	timer    *time.Timer
}

// snapshot is a room's members, as the event that tells them.
type snapshot struct {
	seq uint64
	ev  []byte
}

// hub is the rooms of a prefix of an engine's routes, shared by the configs
// that serve it, so that a reload keeps the rooms and their members.
type hub struct {
	key  hubKey
	refs int // under hubsMu

	set atomic.Pointer[settings]

	mu       sync.Mutex
	rooms    map[string]*room
	watchers int
	closed   bool
}

type hubKey struct {
	engine *fib.Engine
	prefix string
}

var (
	hubsMu sync.Mutex
	hubs   = map[hubKey]*hub{}
)

// acquire returns the hub of the module's engine and prefix, made when there
// is none, with st its settings from now on.
func acquire(ctx *module.Context, prefix string, st *settings) *hub {
	k := hubKey{ctx.Engine(), prefix}
	hubsMu.Lock()
	defer hubsMu.Unlock()
	h := hubs[k]
	if h == nil {
		h = &hub{key: k, rooms: map[string]*room{}}
		hubs[k] = h
	}
	h.refs++
	h.set.Store(st)
	return h
}

// release lets go of h; the last config to let go closes it, ending its
// watchers' streams.
func (h *hub) release() {
	hubsMu.Lock()
	h.refs--
	last := h.refs == 0
	if last {
		delete(hubs, h.key)
	}
	hubsMu.Unlock()
	if !last {
		return
	}
	h.mu.Lock()
	h.closed = true
	var ws []*watcher
	for _, r := range h.rooms {
		if r.timer != nil {
			r.timer.Stop()
		}
		for w := range r.watchers {
			ws = append(ws, w)
		}
	}
	clear(h.rooms)
	h.watchers = 0
	h.mu.Unlock()
	for _, w := range ws {
		w.end(true)
	}
}

// drain ends the watchers whose requests owner serves.
func (h *hub) drain(owner *Pages) {
	h.mu.Lock()
	var ws []*watcher
	for _, r := range h.rooms {
		for w := range r.watchers {
			if w.owner == owner {
				ws = append(ws, w)
			}
		}
	}
	h.mu.Unlock()
	for _, w := range ws {
		w.end(true)
	}
}

// roomLocked returns the room named name, made when create is set and the
// hub has room for it. h.mu is held.
func (h *hub) roomLocked(name string, create bool) *room {
	r := h.rooms[name]
	if r != nil || !create || h.closed || len(h.rooms) >= h.set.Load().maxRooms {
		return r
	}
	r = &room{name: name, members: map[string]*member{}, watchers: map[*watcher]struct{}{}}
	h.rooms[name] = r
	h.scheduleLocked(r)
	return r
}

// scheduleLocked sets r's timer for its next sweep and ping. h.mu is held.
func (h *hub) scheduleLocked(r *room) {
	every := max(time.Second, h.set.Load().memberTTL/3)
	r.timer = time.AfterFunc(every, func() { h.tick(r) })
}

// tick sweeps r's members gone quiet, pings its watchers, and forgets r
// once it has neither members nor watchers.
func (h *hub) tick(r *room) {
	st := h.set.Load()
	now := time.Now()
	h.mu.Lock()
	if h.closed || h.rooms[r.name] != r {
		h.mu.Unlock()
		return
	}
	changed := false
	for name, m := range r.members {
		if now.Sub(m.seen) > st.memberTTL {
			delete(r.members, name)
			changed = true
		}
	}
	if len(r.members) == 0 && len(r.watchers) == 0 {
		delete(h.rooms, r.name)
		h.mu.Unlock()
		return
	}
	var snap *snapshot
	if changed {
		snap = r.changedLocked()
	}
	ws := r.watcherList()
	h.scheduleLocked(r)
	h.mu.Unlock()
	for _, w := range ws {
		if snap != nil {
			w.push(snap)
		} else {
			w.ping()
		}
	}
}

// changedLocked makes r's snapshot anew. h.mu is held.
func (r *room) changedLocked() *snapshot {
	r.seq++
	list := make([]*member, 0, len(r.members))
	for _, m := range r.members {
		list = append(list, m)
	}
	slices.SortFunc(list, func(a, b *member) int { return int(a.joined - b.joined) })
	out := struct {
		Room    string       `json:"room"`
		Seq     uint64       `json:"seq"`
		Members []memberJSON `json:"members"`
	}{Room: r.name, Seq: r.seq, Members: make([]memberJSON, len(list))}
	for i, m := range list {
		out.Members[i] = memberJSON{Name: m.name, state: m.st}
	}
	b, _ := json.Marshal(out)
	r.snap = &snapshot{seq: r.seq, ev: append(append([]byte("event: room\ndata: "), b...), "\n\n"...)}
	return r.snap
}

// snapshotLocked returns r's snapshot, made when there is none yet.
func (r *room) snapshotLocked() *snapshot {
	if r.snap == nil {
		return r.changedLocked()
	}
	return r.snap
}

func (r *room) watcherList() []*watcher {
	ws := make([]*watcher, 0, len(r.watchers))
	for w := range r.watchers {
		ws = append(ws, w)
	}
	return ws
}

// broadcast sends snap to r's watchers. h.mu is not held.
func broadcast(ws []*watcher, snap *snapshot) {
	for _, w := range ws {
		w.push(snap)
	}
}

func newKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// joinError is a join refused.
type joinError struct {
	status int
	msg    string
}

// join adds name to room, or gives the name back to the member that has its
// key. It returns the member's key.
func (h *hub) join(roomN, name, key string, st state, publish bool) (string, *joinError) {
	set := h.set.Load()
	h.mu.Lock()
	r := h.roomLocked(roomN, true)
	if r == nil {
		h.mu.Unlock()
		return "", &joinError{stdhttp.StatusServiceUnavailable, "too many rooms"}
	}
	now := time.Now()
	m := r.members[name]
	switch {
	case m != nil && (key == "" || subtle.ConstantTimeCompare([]byte(key), []byte(m.key)) != 1) && now.Sub(m.seen) <= set.memberTTL:
		h.mu.Unlock()
		return "", &joinError{stdhttp.StatusConflict, "the name is taken in the room"}
	case m == nil && len(r.members) >= set.maxMembers:
		h.mu.Unlock()
		return "", &joinError{stdhttp.StatusServiceUnavailable, "the room is full"}
	}
	if m == nil || m.key != key {
		r.joins++
		m = &member{name: name, key: newKey(), joined: r.joins}
		r.members[name] = m
	}
	m.publish, m.st, m.seen = publish, st, now
	snap := r.changedLocked()
	ws := r.watcherList()
	h.mu.Unlock()
	broadcast(ws, snap)
	return m.key, nil
}

// update changes a member's state, the member found by its key; it reports
// false for no such member.
func (h *hub) update(roomN, name, key string, st state) (ok, mayPublish bool) {
	h.mu.Lock()
	r := h.roomLocked(roomN, false)
	var m *member
	if r != nil {
		m = r.members[name]
	}
	if m == nil || subtle.ConstantTimeCompare([]byte(key), []byte(m.key)) != 1 {
		h.mu.Unlock()
		return false, false
	}
	if st.Publishing && !m.publish {
		h.mu.Unlock()
		return true, false
	}
	m.seen = time.Now()
	if m.st == st {
		h.mu.Unlock()
		return true, true
	}
	m.st = st
	snap := r.changedLocked()
	ws := r.watcherList()
	h.mu.Unlock()
	broadcast(ws, snap)
	return true, true
}

// leave removes a member, found by its key.
func (h *hub) leave(roomN, name, key string) bool {
	h.mu.Lock()
	r := h.roomLocked(roomN, false)
	var m *member
	if r != nil {
		m = r.members[name]
	}
	if m == nil || subtle.ConstantTimeCompare([]byte(key), []byte(m.key)) != 1 {
		h.mu.Unlock()
		return false
	}
	delete(r.members, name)
	snap := r.changedLocked()
	ws := r.watcherList()
	h.mu.Unlock()
	broadcast(ws, snap)
	return true
}

// rooms serves the room API: rest is what follows rooms/.
func (p *Pages) rooms(c *fibhttp.Context, rest string) {
	roomN, op, _ := strings.Cut(rest, "/")
	if !roomName.MatchString(roomN) {
		plain(c, stdhttp.StatusNotFound, "not a room's name: 1 to 64 letters, digits, '.', '_', '~' and '-'", nil)
		return
	}
	method := c.Request.Method
	want := stdhttp.MethodPost
	if op == "events" {
		want = stdhttp.MethodGet
	}
	if method != want {
		plain(c, stdhttp.StatusMethodNotAllowed, "the room API's "+op+" takes "+want, stdhttp.Header{"Allow": {want}})
		return
	}
	switch {
	case op == "events":
		p.auth.Guard(c, mediaauth.Play, roomN+"-*", func(string) { p.watch(c, roomN) })
	case op == "join":
		p.auth.Guard(c, mediaauth.Play, roomN+"-*", func(string) { p.join(c, roomN) })
	case op == "leave":
		readBody(c, p.settings.maxMessage, func(body []byte) {
			var in struct{ Name, Key string }
			if json.Unmarshal(body, &in) != nil || !p.hub.leave(roomN, in.Name, in.Key) {
				plain(c, stdhttp.StatusNotFound, "no such member", nil)
				return
			}
			writeJSON(c, stdhttp.StatusOK, map[string]any{}, nil)
		})
	case strings.HasPrefix(op, "members/"):
		name := strings.TrimPrefix(op, "members/")
		readBody(c, p.settings.maxMessage, func(body []byte) {
			var in struct {
				Key string `json:"key"`
				state
			}
			if err := json.Unmarshal(body, &in); err != nil {
				plain(c, stdhttp.StatusBadRequest, "the body must be JSON: {key, audio, video, audioDevice, videoDevice, screen, publishing}", nil)
				return
			}
			ok, may := p.hub.update(roomN, name, in.Key, in.state)
			switch {
			case !ok:
				plain(c, stdhttp.StatusNotFound, "no such member: join again", nil)
			case !may:
				plain(c, stdhttp.StatusForbidden, "the credentials the member joined with do not allow publishing "+roomN+"-"+name, nil)
			default:
				writeJSON(c, stdhttp.StatusOK, map[string]any{}, nil)
			}
		})
	default:
		plain(c, stdhttp.StatusNotFound, "no such API", nil)
	}
}

// join joins a member, its credentials checked for play on <room>-*.
func (p *Pages) join(c *fibhttp.Context, roomN string) {
	readBody(c, p.settings.maxMessage, func(body []byte) {
		var in struct {
			Name string `json:"name"`
			Key  string `json:"key"`
			state
		}
		if err := json.Unmarshal(body, &in); err != nil {
			plain(c, stdhttp.StatusBadRequest, "the body must be JSON: {name, key, audio, video, audioDevice, videoDevice, screen, publishing}", nil)
			return
		}
		if !memberName.MatchString(in.Name) {
			plain(c, stdhttp.StatusBadRequest, "not a member's name: 1 to 63 letters, digits, '.', '_', '~' and '-'", nil)
			return
		}
		stream := roomN + "-" + in.Name
		publish := true
		if p.auth != nil {
			// The credentials allowed play already: a password among them
			// was checked, and is remembered, so this does not wait.
			_, err := p.auth.Allow(mediaauth.Publish, stream, mediaauth.FromHTTP(c.Request))
			publish = err == nil
		}
		if in.Publishing && !publish {
			plain(c, stdhttp.StatusForbidden, "the credentials do not allow publishing "+stream, nil)
			return
		}
		key, e := p.hub.join(roomN, in.Name, in.Key, in.state, publish)
		if e != nil {
			hdr := stdhttp.Header(nil)
			if e.status == stdhttp.StatusServiceUnavailable {
				hdr = stdhttp.Header{"Retry-After": {"5"}}
			}
			plain(c, e.status, e.msg, hdr)
			return
		}
		set := p.hub.set.Load()
		writeJSON(c, stdhttp.StatusOK, map[string]any{
			"key": key, "publish": publish, "stream": stream,
			"ttlMs": set.memberTTL.Milliseconds(), "heartbeatMs": (set.memberTTL / 3).Milliseconds(),
		}, nil)
	})
}

// watcher is a room's event stream to a browser.
type watcher struct {
	c     *fibhttp.Context
	owner *Pages
	h     *hub
	room  *room

	mu      sync.Mutex
	ended   bool
	waiting bool      // above the limit: OnWritable is set
	pending *snapshot // the latest snapshot while waiting
	sent    uint64    // the seq of the last snapshot written
}

// watch starts a room's event stream.
func (p *Pages) watch(c *fibhttp.Context, roomN string) {
	h := p.hub
	set := h.set.Load()
	w := &watcher{c: c, owner: p, h: h}
	h.mu.Lock()
	r := h.roomLocked(roomN, true)
	switch {
	case r == nil:
		h.mu.Unlock()
		plain(c, stdhttp.StatusServiceUnavailable, "too many rooms", stdhttp.Header{"Retry-After": {"5"}})
		return
	case len(r.watchers) >= set.maxWatchers:
		h.mu.Unlock()
		plain(c, stdhttp.StatusServiceUnavailable, "the room has too many watchers", stdhttp.Header{"Retry-After": {"5"}})
		return
	}
	w.room = r
	r.watchers[w] = struct{}{}
	h.watchers++
	snap := r.snapshotLocked()
	h.mu.Unlock()

	c.Retain()
	p.graph.Retain()
	// The stream lasts as long as the browser follows the room: the
	// listener's write timeout, meant for whole responses, is lifted, and
	// each wait for the browser is bounded by stall_timeout instead.
	_ = c.SetWriteDeadline(time.Time{})
	hdr := c.Header()
	hdr.Set("Content-Type", "text/event-stream; charset=utf-8")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("X-Accel-Buffering", "no")
	c.WriteHeader(stdhttp.StatusOK)
	w.mu.Lock()
	ok := w.write([]byte("retry: 2000\n\n"))
	w.mu.Unlock()
	if ok {
		c.Flush()
		w.push(snap)
	}
	c.OnCancel(func(error) { w.end(false) })
}

// push sends snap, unless a later one was sent; while the browser has not
// taken what was sent, only the latest is kept, to send once it has.
func (w *watcher) push(snap *snapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended || snap.seq <= w.sent {
		return
	}
	if w.waiting {
		if w.pending == nil || snap.seq > w.pending.seq {
			w.pending = snap
		}
		return
	}
	w.sent = snap.seq
	w.write(snap.ev)
}

// ping sends a comment, which keeps proxies from closing a quiet stream.
func (w *watcher) ping() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended || w.waiting {
		return
	}
	w.write([]byte(": ping\n\n"))
}

// write writes b without waiting; w.mu is held. Past the limit it waits for
// the browser, at most stall_timeout. It reports false when the stream
// failed, and is ended.
func (w *watcher) write(b []byte) bool {
	above, err := w.c.WriteNoWait(b)
	if err != nil {
		go w.end(false)
		return false
	}
	if above {
		w.waiting = true
		_ = w.c.SetWriteDeadline(time.Now().Add(w.h.set.Load().stall))
		if !w.c.OnWritable(w.writable) {
			w.waiting = false
			_ = w.c.SetWriteDeadline(time.Time{})
		}
	}
	return true
}

// writable sends the latest snapshot held back, once the browser has taken
// what was sent.
func (w *watcher) writable(err error) {
	if err != nil {
		w.end(false)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ended {
		return
	}
	w.waiting = false
	_ = w.c.SetWriteDeadline(time.Time{})
	if s := w.pending; s != nil {
		w.pending = nil
		if s.seq > w.sent {
			w.sent = s.seq
			w.write(s.ev)
		}
	}
}

// end ends the stream once: it leaves its room, finishes the response when
// finish is set (a drain, or the hub closed), and lets go of the request.
func (w *watcher) end(finish bool) {
	w.mu.Lock()
	if w.ended {
		w.mu.Unlock()
		return
	}
	w.ended = true
	w.mu.Unlock()
	h := w.h
	h.mu.Lock()
	if _, ok := w.room.watchers[w]; ok {
		delete(w.room.watchers, w)
		h.watchers--
	}
	h.mu.Unlock()
	if finish {
		_ = w.c.Finish()
	}
	w.c.Release()
	w.owner.graph.Release()
}

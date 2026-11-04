package mediapages

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// The event stream's own paths, driven through a stream the test controls:
// a stream that fails as it starts or as it writes, a browser that falls
// behind and catches up, and a hub closed under its watchers.

// pipe is a stream a room's events are written to without a network, as an
// HTTP/2 or HTTP/3 stream is: the test says when a write is above the
// stream's limit, when one fails, and when the browser has taken what it was
// sent, by calling the function OnWritable left.
type pipe struct {
	mu         sync.Mutex
	body       []byte
	beginErr   error // BeginResponse fails with it
	writeErr   error // every write fails with it
	above      bool  // the writes report above the limit
	notify     bool  // NotifyWritable takes its fn
	writable   func(error)
	onDeadline func() // runs once, at the next SetWriteDeadline
	ended      bool
}

func (s *pipe) set(fn func(s *pipe)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

func (s *pipe) WriteResponse(_ *stdhttp.Request, r fibhttp.Response) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = append(s.body, r.Body...)
	return nil
}

func (s *pipe) WriteInterim(int, stdhttp.Header) error {
	return errors.New("pipe: no interim responses")
}

func (s *pipe) Push(*stdhttp.Request, string, *stdhttp.PushOptions) error {
	return errors.New("pipe: no push")
}

func (s *pipe) BeginResponse(*stdhttp.Request, int, stdhttp.Header) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginErr
}

func (s *pipe) WriteBody(p []byte, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	s.body = append(s.body, p...)
	return nil
}

func (s *pipe) EndResponse(stdhttp.Header, bool) error { return nil }

func (s *pipe) WriteBodyNoWait(p []byte) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return false, s.writeErr
	}
	s.body = append(s.body, p...)
	return s.above, nil
}

func (s *pipe) NotifyWritable(fn func(error)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.notify {
		return false
	}
	s.writable = fn
	return true
}

// takeWritable returns the function OnWritable left, once.
func (s *pipe) takeWritable(t *testing.T) func(error) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	fn := s.writable
	if fn == nil {
		t.Fatal("no OnWritable function waits")
	}
	s.writable = nil
	return fn
}

func (s *pipe) SetReadDeadline(time.Time) error { return nil }

func (s *pipe) SetWriteDeadline(time.Time) error {
	s.mu.Lock()
	fn := s.onDeadline
	s.onDeadline = nil
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (s *pipe) RequestEnded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *pipe) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// sent returns what was written to the stream.
func (s *pipe) sent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.body)
}

// holder counts the holds on a config, as a graph does.
type holder struct{ n atomic.Int64 }

func (h *holder) Retain()  { h.n.Add(1) }
func (h *holder) Release() { h.n.Add(-1) }

// pipePages is Pages over a hub of its own, without a config, and with no
// auth, whose hub is closed as the test ends.
func pipePages(t *testing.T, chat bool) (*Pages, *holder) {
	t.Helper()
	st := &settings{maxRooms: 8, maxMembers: 64, maxWatchers: 128, maxMessage: 65536, maxFile: 1 << 20,
		memberTTL: 30 * time.Second, stall: 30 * time.Second, chat: chat}
	h := &hub{key: hubKey{nil, "/pipe/" + t.Name()}, refs: 1, rooms: map[string]*room{}}
	h.set.Store(st)
	g := new(holder)
	p := &Pages{prefix: "/media", graph: g, hub: h, settings: st}
	t.Cleanup(h.release)
	return p, g
}

// watchPipe follows room through s, as a browser's GET events does.
func watchPipe(p *Pages, room string, s *pipe) *fibhttp.Context {
	c := fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "/media/api/rooms/"+room+"/events", nil), s)
	fibhttp.Serve(p, c)
	return c
}

// watcherOf returns room's one watcher.
func watcherOf(t *testing.T, h *hub, room string) *watcher {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[room]
	if r == nil || len(r.watchers) != 1 {
		t.Fatalf("room %s has no one watcher", room)
	}
	for w := range r.watchers {
		return w
	}
	return nil
}

// roomWatchers is how many watchers room has.
func roomWatchers(h *hub, room string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[room]; r != nil {
		return len(r.watchers)
	}
	return 0
}

// ended waits for a watcher of room to have let go of its request and of the
// config.
func ended(t *testing.T, p *Pages, g *holder, s *pipe, room string) {
	t.Helper()
	eventually(t, "the watcher gone", func() bool { return roomWatchers(p.hub, room) == 0 && g.n.Load() == 0 && s.isEnded() })
}

// joinN joins n members with long names to room, for a snapshot of more than
// the bytes a response holds back before it begins.
func joinN(t *testing.T, h *hub, room string, n int) {
	t.Helper()
	for i := range n {
		name := strings.Repeat("m", 60) + string(rune('A'+i/26)) + string(rune('a'+i%26))
		if _, e := h.join(room, name, "", state{}, false); e != nil {
			t.Fatalf("join %s: %v", name, e.msg)
		}
	}
}

// A stream drained while it is being started is not started: it lets go of
// the request and the config at once.
func TestWatcherDrainedAsItStarts(t *testing.T) {
	p, g := pipePages(t, true)
	s := &pipe{}
	s.onDeadline = p.Drain
	watchPipe(p, "r", s)
	ended(t, p, g, s, "r")
	if strings.Contains(s.sent(), "retry:") {
		t.Fatalf("a drained stream was started: %q", s.sent())
	}
}

// A stream that fails as its first events are written, the chat's history or
// the room's snapshot, ends, and lets go of the request and the config.
func TestWatcherFailsAsItStarts(t *testing.T) {
	reset := errors.New("pipe: the stream was reset")

	p, g := pipePages(t, true)
	key, e := p.hub.join("r", "ann", "", state{}, false)
	if e != nil {
		t.Fatal(e.msg)
	}
	for range 3 {
		if _, e := p.hub.chat("r", "ann", key, strings.Repeat("h", 2000)); e != nil {
			t.Fatal(e.msg)
		}
	}
	s := &pipe{beginErr: reset}
	watchPipe(p, "r", s)
	ended(t, p, g, s, "r")

	// No chat, so the snapshot is the first event past what is held back.
	p, g = pipePages(t, false)
	joinN(t, p.hub, "r", 30)
	s = &pipe{beginErr: reset}
	watchPipe(p, "r", s)
	ended(t, p, g, s, "r")
}

// A browser that falls behind is sent nothing more until it has taken what
// it was sent: chat events wait in order, only the latest snapshot is kept,
// pings are skipped; once it has taken it, what waited goes, and when it is
// behind again the rest waits again.
func TestWatcherCatchesUp(t *testing.T) {
	p, g := pipePages(t, true)
	h := p.hub
	key, e := h.join("r", "ann", "", state{}, false)
	if e != nil {
		t.Fatal(e.msg)
	}
	s := &pipe{notify: true}
	watchPipe(p, "r", s)
	w := watcherOf(t, h, "r")

	// The next write is past the limit: the stream waits for the browser.
	s.set(func(s *pipe) { s.above = true })
	if _, e := h.join("r", "bob", "", state{}, false); e != nil {
		t.Fatal(e.msg)
	}
	var texts []string
	for _, text := range []string{"one", "two"} {
		if _, e := h.chat("r", "ann", key, text); e != nil {
			t.Fatal(e.msg)
		}
		texts = append(texts, text)
	}
	if _, e := h.join("r", "cat", "", state{}, false); e != nil {
		t.Fatal(e.msg)
	}
	w.ping()
	before := s.sent()
	if strings.Contains(before, `"one"`) || strings.Contains(before, "cat") || strings.Contains(before, "ping") {
		t.Fatalf("a waiting stream was written to: %q", before)
	}

	// The browser takes it, but the first chat event is past the limit
	// again: the second waits.
	s.takeWritable(t)(nil)
	if got := s.sent(); !strings.Contains(got, `"one"`) || strings.Contains(got, `"two"`) {
		t.Fatalf("after the first catch-up: %q", got[len(before):])
	}
	// Now it keeps up: the second chat event, then the latest snapshot.
	s.set(func(s *pipe) { s.above = false })
	s.takeWritable(t)(nil)
	got := s.sent()
	if i, j := strings.Index(got, `"two"`), strings.LastIndex(got, `"cat"`); i < 0 || j < i {
		t.Fatalf("after the second catch-up: %q", got[len(before):])
	}
	w.ping()
	if !strings.HasSuffix(s.sent(), ": ping\n\n") {
		t.Fatal("a stream that keeps up was not pinged")
	}

	// Past the limit with nowhere to wait, OnWritable refusing, the stream
	// goes on writing.
	s.set(func(s *pipe) { s.above, s.notify = true, false })
	if _, e := h.join("r", "dan", "", state{}, false); e != nil {
		t.Fatal(e.msg)
	}
	w.ping()
	if !strings.HasSuffix(s.sent(), ": ping\n\n") {
		t.Fatal("a stream OnWritable refused is waiting")
	}

	// Behind again; then the browser goes as what waited is written.
	s.set(func(s *pipe) { s.notify = true })
	if _, e := h.join("r", "eve", "", state{}, false); e != nil {
		t.Fatal(e.msg)
	}
	if _, e := h.chat("r", "ann", key, "three"); e != nil {
		t.Fatal(e.msg)
	}
	s.set(func(s *pipe) { s.writeErr = errors.New("pipe: the browser went") })
	writable := s.takeWritable(t)
	writable(nil)
	ended(t, p, g, s, "r")
	// What reaches an ended stream is dropped: a message, the browser
	// taking what it was sent, a ping.
	w.post(newTextMessage("ann", "late"))
	writable(nil)
	w.ping()
	if strings.Contains(s.sent(), "late") {
		t.Fatal("an ended stream was written to")
	}
}

// A browser behind with only a snapshot waiting, that goes as the snapshot
// is written, ends the stream.
func TestWatcherFailsCatchingUp(t *testing.T) {
	p, g := pipePages(t, true)
	h := p.hub
	s := &pipe{notify: true}
	watchPipe(p, "r", s)
	s.set(func(s *pipe) { s.above = true })
	for _, name := range []string{"ann", "bob"} {
		if _, e := h.join("r", name, "", state{}, false); e != nil {
			t.Fatal(e.msg)
		}
	}
	s.set(func(s *pipe) { s.writeErr = errors.New("pipe: the browser went") })
	s.takeWritable(t)(nil)
	ended(t, p, g, s, "r")
	if strings.Contains(s.sent(), "bob") {
		t.Fatal("the snapshot that failed was written")
	}
}

// The hub closed under its watchers, as the last config of a prefix lets go
// of it, ends their streams; a sweep that comes after finds nothing to do.
func TestHubClosedUnderWatchers(t *testing.T) {
	p, g := pipePages(t, true)
	h := p.hub
	s := &pipe{}
	watchPipe(p, "r", s)
	r := func() *room { h.mu.Lock(); defer h.mu.Unlock(); return h.rooms["r"] }()
	h.release()
	h.refs = 1 // for the test's own release as it ends
	eventually(t, "the watcher gone", func() bool { return g.n.Load() == 0 && s.isEnded() })
	h.tick(r)
	if roomCount(h) != 0 {
		t.Fatal("a closed hub has rooms")
	}
}

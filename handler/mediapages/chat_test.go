package mediapages

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The chat's tests: messages over the room API and their events, the file's
// round trip and bounds, the history, eviction, a slow watcher, the auth, and
// a room forgotten.

// msgJSON is a chat message as the API and the events show it.
type msgJSON struct {
	ID   string `json:"id"`
	From string `json:"from"`
	At   int64  `json:"at"`
	Text string `json:"text"`
	File *struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		Type string `json:"type"`
	} `json:"file"`
}

// chatStream follows a room's events, taking the chat's history and messages
// and the room's snapshots.
type chatStream struct {
	resp *http.Response
	hist chan []msgJSON
	chat chan msgJSON
	room chan []member1
	done chan struct{}

	mu    sync.Mutex
	first string // the first event's name
}

func followChat(t *testing.T, url string, hdr map[string]string) *chatStream {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("events: %d %s", resp.StatusCode, b)
	}
	e := &chatStream{
		resp: resp,
		hist: make(chan []msgJSON, 8),
		chat: make(chan msgJSON, 2048),
		room: make(chan []member1, 64),
		done: make(chan struct{}),
	}
	t.Cleanup(e.close)
	go func() {
		defer close(e.done)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		name, data := "", ""
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			case line == "":
				if name != "" {
					e.mu.Lock()
					if e.first == "" {
						e.first = name
					}
					e.mu.Unlock()
				}
				switch name {
				case "history":
					var v struct{ Messages []msgJSON }
					if json.Unmarshal([]byte(data), &v) == nil {
						select {
						case e.hist <- v.Messages:
						default:
						}
					}
				case "chat":
					var m msgJSON
					if json.Unmarshal([]byte(data), &m) == nil {
						select {
						case e.chat <- m:
						default:
						}
					}
				case "room":
					var v struct{ Members []member1 }
					if json.Unmarshal([]byte(data), &v) == nil {
						// Keep reading: only the latest snapshot is wanted.
						for {
							select {
							case e.room <- v.Members:
							default:
								select {
								case <-e.room:
								default:
								}
								continue
							}
							break
						}
					}
				}
				name, data = "", ""
			}
		}
	}()
	return e
}

func (e *chatStream) close() { e.resp.Body.Close() }

// waitHistory waits for a history event ok accepts.
func (e *chatStream) waitHistory(t *testing.T, what string, ok func([]msgJSON) bool) []msgJSON {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case m := <-e.hist:
			if ok(m) {
				return m
			}
		case <-e.done:
			t.Fatalf("the event stream ended waiting for %s", what)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// waitChat waits for a chat message ok accepts.
func (e *chatStream) waitChat(t *testing.T, what string, ok func(msgJSON) bool) msgJSON {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case m := <-e.chat:
			if ok(m) {
				return m
			}
		case <-e.done:
			t.Fatalf("the event stream ended waiting for %s", what)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// waitRoom waits for a room event whose members ok accepts.
func (e *chatStream) waitRoom(t *testing.T, what string, ok func([]member1) bool) []member1 {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		select {
		case m := <-e.room:
			if ok(m) {
				return m
			}
		case <-e.done:
			t.Fatalf("the event stream ended waiting for %s", what)
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// firstEvent returns the name of the stream's first event.
func (e *chatStream) firstEvent(t *testing.T) string {
	t.Helper()
	deadline := time.After(waitTimeout)
	for {
		e.mu.Lock()
		first := e.first
		e.mu.Unlock()
		if first != "" {
			return first
		}
		select {
		case <-e.done:
			t.Fatalf("the event stream ended before its first event")
		case <-deadline:
			t.Fatalf("timed out waiting for the stream's first event")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// chatAuth is hdr with the chat's member headers.
func chatAuth(hdr map[string]string, name, key string) map[string]string {
	out := map[string]string{"X-Fit-Member": name, "X-Fit-Room-Key": key}
	for k, v := range hdr {
		out[k] = v
	}
	return out
}

// joinKey joins a member and returns its key.
func joinKey(t *testing.T, f *fit, room, name string, hdr map[string]string) string {
	t.Helper()
	code, v := join(t, f, room, `{"name": "`+name+`"}`, hdr)
	if code != 200 {
		t.Fatalf("join %s: %d %v", name, code, v)
	}
	return v["key"].(string)
}

// postChat sends a text message.
func postChat(t *testing.T, f *fit, room, name, key, text string, hdr map[string]string) (*http.Response, msgJSON) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"text": text})
	resp, b := do(t, "POST", f.base+"/media/api/rooms/"+room+"/chat", string(body), chatAuth(hdr, name, key))
	var m msgJSON
	_ = json.Unmarshal([]byte(b), &m)
	return resp, m
}

// postFile sends a file message.
func postFile(t *testing.T, f *fit, room, name, key, fname, ctype, body string, hdr map[string]string) (*http.Response, msgJSON) {
	t.Helper()
	u := f.base + "/media/api/rooms/" + room + "/files?file=" + url.QueryEscape(fname)
	h := chatAuth(hdr, name, key)
	if ctype != "" {
		h["Content-Type"] = ctype
	}
	resp, b := do(t, "POST", u, body, h)
	var m msgJSON
	_ = json.Unmarshal([]byte(b), &m)
	return resp, m
}

func filesURL(f *fit, room, id string) string {
	return f.base + "/media/api/rooms/" + room + "/files/" + id
}

func fileBytesOf(t *testing.T, h *hub) int64 {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.fileBytes
}

func roomCount(h *hub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.rooms)
}

// A text message reaches both members over SSE, and the history of a late
// watcher holds it; a wrong key or name is no such member.
func TestChatText(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	viv := joinKey(t, f, "room1", "viv", basic("viv", "viv-password"))
	ev := f.base + "/media/api/rooms/room1/events"
	wa := followChat(t, ev, basic("ann", "ann-password"))
	wv := followChat(t, ev, basic("viv", "viv-password"))
	if h := wa.waitHistory(t, "ann's empty history", func([]msgJSON) bool { return true }); len(h) != 0 {
		t.Fatalf("ann's history: %v", h)
	}
	wv.waitHistory(t, "viv's empty history", func([]msgJSON) bool { return true })
	if first := wa.firstEvent(t); first != "history" {
		t.Fatalf("the stream starts with %q, not history", first)
	}

	resp, m := postChat(t, f, "room1", "ann", ann, "hi there", basic("ann", "ann-password"))
	if resp.StatusCode != 200 || len(m.ID) != 32 || m.From != "ann" || m.Text != "hi there" ||
		m.At < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatalf("chat: %d %+v", resp.StatusCode, m)
	}
	for what, w := range map[string]*chatStream{"ann": wa, "viv": wv} {
		got := w.waitChat(t, what+" gets the message", func(x msgJSON) bool { return x.ID == m.ID })
		if got.Text != "hi there" || got.From != "ann" || got.At != m.At || got.File != nil {
			t.Fatalf("%s's message: %+v", what, got)
		}
	}

	// A watcher that joins later gets the messages in its history.
	late := followChat(t, ev, bearer)
	hist := late.waitHistory(t, "the late watcher's history", func(h []msgJSON) bool { return len(h) == 1 })
	if hist[0].ID != m.ID || hist[0].Text != "hi there" || hist[0].From != "ann" {
		t.Fatalf("history: %+v", hist)
	}

	// A wrong key, and a name that is not the member's, are no such member.
	if resp, _ := postChat(t, f, "room1", "ann", "wrong", "x", basic("ann", "ann-password")); resp.StatusCode != 404 {
		t.Fatalf("a wrong key: %d", resp.StatusCode)
	}
	if resp, _ := postChat(t, f, "room1", "viv", ann, "x", basic("viv", "viv-password")); resp.StatusCode != 404 {
		t.Fatalf("another's key: %d", resp.StatusCode)
	}
	if resp, _ := postChat(t, f, "room1", "ann", viv, "x", basic("ann", "ann-password")); resp.StatusCode != 404 {
		t.Fatalf("the member's name with the wrong key: %d", resp.StatusCode)
	}
	if resp, _ := postChat(t, f, "nobody", "ann", ann, "x", bearer); resp.StatusCode != 404 {
		t.Fatalf("a room with no member: %d", resp.StatusCode)
	}
	// A member that is not in a room the credentials do not play: 403.
	if resp, _ := postChat(t, f, "room9", "ann", ann, "x", basic("ann", "ann-password")); resp.StatusCode != 403 {
		t.Fatalf("another room: %d", resp.StatusCode)
	}
}

// A message that is not JSON, empty, blank, invalid UTF-8 or past
// max_message_bytes is refused, and one with text is taken.
func TestChatBounds(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	post := func(body string) int {
		t.Helper()
		resp, _ := do(t, "POST", f.base+"/media/api/rooms/room1/chat", body, chatAuth(basic("ann", "ann-password"), "ann", ann))
		return resp.StatusCode
	}
	for _, tc := range []struct {
		body string
		want int
	}{
		{``, 400},
		{`{"text": ""}`, 400},
		{`{"text": "   "}`, 400},
		{`{"text": "\t\n"}`, 400},
		{`not json`, 400},
		{`{"text": 5}`, 400},
		{`{"text": "` + string([]byte{0xff, 0xfe}) + `"}`, 400},
		{`{"text": "` + strings.Repeat("x", 2000) + `"}`, 413},
	} {
		if got := post(tc.body); got != tc.want {
			t.Errorf("%.60s: %d, want %d", tc.body, got, tc.want)
		}
	}
	// A right message with UTF-8 text is taken as it is.
	resp, m := postChat(t, f, "room1", "ann", ann, "你好, media!", basic("ann", "ann-password"))
	if resp.StatusCode != 200 || m.Text != "你好, media!" {
		t.Fatalf("UTF-8 text: %d %+v", resp.StatusCode, m)
	}
}

// With the chat off, its API is not there, and the event stream carries no
// history.
func TestChatOff(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live", "chat": false}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	if resp, _ := postChat(t, f, "room1", "ann", ann, "hi", basic("ann", "ann-password")); resp.StatusCode != 404 {
		t.Fatalf("chat off, a message: %d", resp.StatusCode)
	}
	if resp, _ := postFile(t, f, "room1", "ann", ann, "a.bin", "application/octet-stream", "x", basic("ann", "ann-password")); resp.StatusCode != 404 {
		t.Fatalf("chat off, a file: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", filesURL(f, "room1", strings.Repeat("0", 32)), "", bearer); resp.StatusCode != 404 {
		t.Fatalf("chat off, a download: %d", resp.StatusCode)
	}
	w := followChat(t, f.base+"/media/api/rooms/room1/events", basic("ann", "ann-password"))
	w.waitRoom(t, "the room", func(m []member1) bool { return names(m) == "ann" })
	if first := w.firstEvent(t); first != "room" {
		t.Fatalf("with the chat off the stream's first event is %q", first)
	}
	select {
	case h := <-w.hist:
		t.Fatalf("history with the chat off: %v", h)
	case <-time.After(200 * time.Millisecond):
	}
}

// A file's bytes go in and come back out with its name, its type and a
// Content-Disposition that names it; the name is sanitized, and an unknown
// file is 404.
func TestChatFile(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	const data = "hello file\n"
	resp, m := postFile(t, f, "room1", "ann", ann, "a b.png", "image/png", data, basic("ann", "ann-password"))
	if resp.StatusCode != 200 || len(m.ID) != 32 || m.From != "ann" || m.Text != "" || m.File == nil {
		t.Fatalf("file message: %d %+v", resp.StatusCode, m)
	}
	if m.File.Name != "a b.png" || m.File.Size != int64(len(data)) || m.File.Type != "image/png" {
		t.Fatalf("file: %+v", *m.File)
	}
	resp, body := do(t, "GET", filesURL(f, "room1", m.ID), "", basic("ann", "ann-password"))
	if resp.StatusCode != 200 || body != data {
		t.Fatalf("download: %d %q", resp.StatusCode, body)
	}
	if resp.ContentLength != int64(len(data)) || resp.Header.Get("Content-Length") != fmt.Sprint(len(data)) {
		t.Fatalf("download length: %d %q", resp.ContentLength, resp.Header.Get("Content-Length"))
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Fatalf("download type: %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment") || !strings.Contains(cd, "a b.png") {
		t.Fatalf("Content-Disposition: %q", cd)
	}
	if v := resp.Header.Get("X-Content-Type-Options"); v != "nosniff" {
		t.Fatalf("X-Content-Type-Options: %q", v)
	}
	if v := resp.Header.Get("Cache-Control"); v != "no-store" {
		t.Fatalf("Cache-Control: %q", v)
	}

	// The name is sanitized of path separators and control characters, and
	// a file without a type downloads as an octet stream.
	resp, m = postFile(t, f, "room1", "ann", ann, "../../a\tb.txt", "", "x", basic("ann", "ann-password"))
	if resp.StatusCode != 200 || m.File.Name != ".._.._a_b.txt" || m.File.Type != "application/octet-stream" {
		t.Fatalf("sanitized name: %d %+v", resp.StatusCode, m.File)
	}
	if resp, body := do(t, "GET", filesURL(f, "room1", m.ID), "", bearer); resp.StatusCode != 200 || body != "x" ||
		resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("octet-stream download: %d %q %v", resp.StatusCode, body, resp.Header)
	}

	// An empty or long name, and a wrong key, are refused.
	if resp, _ := postFile(t, f, "room1", "ann", ann, "", "text/plain", "x", basic("ann", "ann-password")); resp.StatusCode != 400 {
		t.Fatalf("an empty name: %d", resp.StatusCode)
	}
	if resp, _ := postFile(t, f, "room1", "ann", ann, strings.Repeat("n", 201), "text/plain", "x", basic("ann", "ann-password")); resp.StatusCode != 400 {
		t.Fatalf("a long name: %d", resp.StatusCode)
	}
	if resp, _ := postFile(t, f, "room1", "ann", "wrong", "a.bin", "text/plain", "x", basic("ann", "ann-password")); resp.StatusCode != 404 {
		t.Fatalf("a wrong key: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", filesURL(f, "room1", strings.Repeat("0", 32)), "", bearer); resp.StatusCode != 404 {
		t.Fatalf("an unknown file: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", filesURL(f, "room2", m.ID), "", bearer); resp.StatusCode != 404 {
		t.Fatalf("another room's file: %d", resp.StatusCode)
	}
}

// A file past max_file_bytes is 413, by its Content-Length and when its
// length arrives with it, and one at the bound is taken.
func TestChatFileTooLarge(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live", "max_file_bytes": 1024}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	big := strings.Repeat("x", 2000)
	if resp, _ := postFile(t, f, "room1", "ann", ann, "big.bin", "application/octet-stream", big, basic("ann", "ann-password")); resp.StatusCode != 413 {
		t.Fatalf("a declared length past the bound: %d", resp.StatusCode)
	}
	// The body of no declared length: built with a reader, so it is chunked.
	req, _ := http.NewRequest("POST", f.base+"/media/api/rooms/room1/files?file=big.bin", io.MultiReader(strings.NewReader(big)))
	req.Header.Set("Content-Type", "application/octet-stream")
	for k, v := range chatAuth(basic("ann", "ann-password"), "ann", ann) {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 413 || req.ContentLength != 0 {
		t.Fatalf("a chunked body past the bound: %d (length %d)", resp.StatusCode, req.ContentLength)
	}
	// At the bound it is taken.
	if resp, m := postFile(t, f, "room1", "ann", ann, "ok.bin", "application/octet-stream", strings.Repeat("y", 1024), basic("ann", "ann-password")); resp.StatusCode != 200 || m.File.Size != 1024 {
		t.Fatalf("a body at the bound: %d %+v", resp.StatusCode, m)
	}
}

// A room keeps at most chatHistory messages, oldest dropped.
func TestChatHistoryCap(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	ids := make([]string, 0, chatHistory+6)
	for i := 0; i < chatHistory+6; i++ {
		resp, m := postChat(t, f, "room1", "ann", ann, fmt.Sprintf("m%d", i), basic("ann", "ann-password"))
		if resp.StatusCode != 200 {
			t.Fatalf("message %d: %d", i, resp.StatusCode)
		}
		ids = append(ids, m.ID)
	}
	late := followChat(t, f.base+"/media/api/rooms/room1/events", basic("ann", "ann-password"))
	hist := late.waitHistory(t, "the capped history", func(h []msgJSON) bool { return len(h) == chatHistory })
	if hist[0].ID != ids[6] || hist[0].Text != "m6" || hist[chatHistory-1].ID != ids[chatHistory+5] {
		t.Fatalf("history: %d messages, first %+v, last %+v", len(hist), hist[0], hist[chatHistory-1])
	}
}

// Adding files past chatFileBytes drops the oldest file messages across the
// rooms, whose download then 404s, while a room forgotten drops its bytes from
// the count.
func TestChatFileEviction(t *testing.T) {
	old := chatFileBytes
	chatFileBytes = 2500
	t.Cleanup(func() { chatFileBytes = old })

	// A room is forgotten at its next sweep, every third of member_ttl:
	// 30s by default would sweep at 10s, as late as the wait for it.
	f := startFit(t, conf(`{"auth": "live", "member_ttl": "3s"}`))
	h := hubOf(t, f)
	ann1 := joinKey(t, f, "room1", "ann", bearer)
	ann2 := joinKey(t, f, "room2", "ann", bearer)
	one := func(c byte) string { return strings.Repeat(string(c), 1000) }
	_, a := postFile(t, f, "room1", "ann", ann1, "a.bin", "application/octet-stream", one('a'), bearer)
	resp, b := postFile(t, f, "room2", "ann", ann2, "b.bin", "application/octet-stream", one('b'), bearer)
	if resp.StatusCode != 200 || fileBytesOf(t, h) != 2000 {
		t.Fatalf("two files: %d, %d bytes", resp.StatusCode, fileBytesOf(t, h))
	}
	// A third file evicts the oldest, a's, across the rooms.
	resp, c := postFile(t, f, "room1", "ann", ann1, "c.bin", "application/octet-stream", one('c'), bearer)
	if resp.StatusCode != 200 || fileBytesOf(t, h) != 2000 {
		t.Fatalf("the third file: %d, %d bytes", resp.StatusCode, fileBytesOf(t, h))
	}
	if resp, _ := do(t, "GET", filesURL(f, "room1", a.ID), "", bearer); resp.StatusCode != 404 {
		t.Fatalf("the evicted file: %d", resp.StatusCode)
	}
	for _, want := range []struct{ room, id string }{{"room2", b.ID}, {"room1", c.ID}} {
		if resp, _ := do(t, "GET", filesURL(f, want.room, want.id), "", bearer); resp.StatusCode != 200 {
			t.Fatalf("%s's file: %d", want.room, resp.StatusCode)
		}
	}

	// A room forgotten drops its bytes; so does the hub closed.
	if resp, _ := do(t, "POST", f.base+"/media/api/rooms/room1/leave", `{"name": "ann", "key": "`+ann1+`"}`, nil); resp.StatusCode != 200 {
		t.Fatalf("leave: %d", resp.StatusCode)
	}
	eventually(t, "room1 forgotten", func() bool { return roomCount(h) == 1 })
	if got := fileBytesOf(t, h); got != 1000 {
		t.Fatalf("after room1 was forgotten: %d bytes", got)
	}
	f.rt.Close()
	eventually(t, "the hub closed", func() bool {
		hubsMu.Lock()
		defer hubsMu.Unlock()
		return hubs[hubKey{f.env.Engine, "/media"}] == nil
	})
	if got := fileBytesOf(t, h); got != 0 {
		t.Fatalf("after the hub closed: %d bytes", got)
	}
}

// A watcher that reads none of its events is cut off by the chat traffic once
// its queue is past its bound, while one that reads keeps up.
func TestChatSlowWatcher(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live", "max_message_bytes": 65536}`))
	ann := joinKey(t, f, "r", "ann", bearer)
	h := hubOf(t, f)

	// A watcher that never reads.
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	if _, err := conn.Write([]byte("GET /media/api/rooms/r/events HTTP/1.1\r\nHost: x\r\n" +
		"Authorization: Bearer 0123456789abcdef-token\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the slow watcher", func() bool { return watcherCount(h) == 1 })
	good := followChat(t, f.base+"/media/api/rooms/r/events", bearer)
	good.waitHistory(t, "the empty history", func([]msgJSON) bool { return true })

	// Messages long enough that the queue's byte bound cuts the stream soon
	// after the connection's buffer fills, while the other watcher reads.
	if got := watcherCount(h); got != 2 {
		t.Fatalf("%d watchers, want the slow one and the reading one", got)
	}
	text := strings.Repeat("m", 60000)
	for i := 0; i < 200 && watcherCount(h) == 2; i++ {
		resp, _ := postChat(t, f, "r", "ann", ann, text, bearer)
		if resp.StatusCode != 200 {
			t.Fatalf("message %d: %d", i, resp.StatusCode)
		}
	}
	eventually(t, "the slow watcher cut off", func() bool { return watcherCount(h) == 1 })
	// The watcher that reads keeps up, and gets what came after the cut.
	resp, last := postChat(t, f, "r", "ann", ann, "still here", bearer)
	if resp.StatusCode != 200 {
		t.Fatalf("the message after the cut: %d", resp.StatusCode)
	}
	got := good.waitChat(t, "the reading watcher's message", func(m msgJSON) bool { return m.ID == last.ID })
	if got.Text != "still here" {
		t.Fatalf("the reading watcher's message: %+v", got)
	}
	if got := watcherCount(h); got != 1 {
		t.Fatalf("%d watchers left, want the reading one", got)
	}
}

// The chat asks for play on <room>-*, as the room API does.
func TestChatAuth(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	// ann may only play room1's streams: room2's chat is 403.
	for _, tc := range []struct {
		name string
		url  string
		body string
	}{
		{"a message", f.base + "/media/api/rooms/room1/chat", `{"text": "hi"}`},
		{"a file", f.base + "/media/api/rooms/room1/files?file=a.bin", "x"},
	} {
		if resp, _ := do(t, "POST", tc.url, tc.body, chatAuth(nil, "ann", ann)); resp.StatusCode != 401 || resp.Header.Get("Www-Authenticate") == "" {
			t.Fatalf("%s without credentials: %d %v", tc.name, resp.StatusCode, resp.Header)
		}
		if resp, _ := do(t, "POST", strings.Replace(tc.url, "room1", "room2", 1), tc.body, chatAuth(basic("ann", "ann-password"), "ann", ann)); resp.StatusCode != 403 {
			t.Fatalf("%s of another room: %d", tc.name, resp.StatusCode)
		}
	}
	if resp, _ := do(t, "GET", filesURL(f, "room1", strings.Repeat("0", 32)), "", nil); resp.StatusCode != 401 {
		t.Fatalf("a download without credentials: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "GET", filesURL(f, "room2", strings.Repeat("0", 32)), "", basic("ann", "ann-password")); resp.StatusCode != 403 {
		t.Fatalf("a download of another room: %d", resp.StatusCode)
	}
	// The chat off is no such API even with credentials.
	g := startFit(t, conf(`{"auth": "live", "chat": false}`))
	ann2 := joinKey(t, g, "room1", "ann", basic("ann", "ann-password"))
	if resp, _ := postChat(t, g, "room1", "ann", ann2, "hi", basic("ann", "ann-password")); resp.StatusCode != 404 {
		t.Fatalf("with the chat off: %d", resp.StatusCode)
	}
}

// A chat's message never comes before the stream's headers: a browser's first
// event is the history, then the room's members.
func TestChatEventOrder(t *testing.T) {
	f := startFit(t, conf(`{"auth": "live"}`))
	ann := joinKey(t, f, "room1", "ann", basic("ann", "ann-password"))
	w := followChat(t, f.base+"/media/api/rooms/room1/events", basic("ann", "ann-password"))
	hist := w.waitHistory(t, "the history", func([]msgJSON) bool { return true })
	if len(hist) != 0 {
		t.Fatalf("a fresh room's history: %v", hist)
	}
	m := w.waitRoom(t, "the snapshot", func(m []member1) bool { return names(m) == "ann" })
	if len(m) != 1 {
		t.Fatalf("snapshot: %v", m)
	}
	if first := w.firstEvent(t); first != "history" {
		t.Fatalf("the first event is %q", first)
	}
	// The message that follows arrives whole, after the history.
	if _, m := postChat(t, f, "room1", "ann", ann, "after", basic("ann", "ann-password")); m.ID == "" {
		t.Fatal("no message")
	}
	if got := w.waitChat(t, "the message", func(x msgJSON) bool { return x.Text == "after" }); got.From != "ann" {
		t.Fatalf("the message: %+v", got)
	}
}

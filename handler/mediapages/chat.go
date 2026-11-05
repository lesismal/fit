package mediapages

import (
	"crypto/subtle"
	"encoding/json"
	"mime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	stdhttp "net/http"

	fibhttp "github.com/lesismal/fib/http"
)

// The room chat: text messages and small files, carried between the members
// of a room and kept in memory with it. A message is immutable once stored:
// its text, its JSON and its event are built once, and its file's bytes are
// never written to again.
//
// Everything is bounded: a text by max_message_bytes, a file by
// max_file_bytes, a room by chatHistory messages, a hub's file bytes by
// chatFileBytes, the uploads under way by chatUploadBytes, and what a watcher that does not take its events holds by
// maxWatcherQueueEvents and maxWatcherQueueBytes, past which its stream is
// cut. Nothing blocks a worker: the upload's body is gathered with readBody,
// a download writes the stored slice with WriteResponse, and the hub's work
// stays under short locks.

const (
	// chatHistory is how many messages a room keeps, text and file
	// messages together; past it the room's oldest goes.
	chatHistory = 64
	// maxWatcherQueueEvents and maxWatcherQueueBytes bound the chat events
	// a watcher holds for a browser that has not taken what it was sent;
	// past either the stream is cut.
	maxWatcherQueueEvents = 256
	maxWatcherQueueBytes  = 1 << 20
	// maxChatFileName is the most bytes a file's name may have, after the
	// name was sanitized.
	maxChatFileName = 200
	// maxChatFileType is the most bytes of a Content-Type a file's message
	// keeps; a longer one is not a type.
	maxChatFileType = 256
)

// chatFileBytes is the most file bytes the rooms of a hub keep together. It
// is a package variable so that tests can lower it; adding a file past it
// drops the oldest file messages, whose download then 404s.
var chatFileBytes int64 = 64 << 20

// chatUploadBytes is the most bytes of uploads under way that the process
// holds at once, across every media_pages handler: an upload reserves its
// Content-Length, or max_file_bytes without one, before its body is read,
// and one that does not fit is answered 503. It is a package variable so
// that tests can lower it.
var chatUploadBytes int64 = 64 << 20

// uploading is the bytes uploads under way have reserved.
var uploading atomic.Int64

// reserveUpload reserves n bytes of chatUploadBytes, reporting whether they
// fit.
func reserveUpload(n int64) bool {
	for {
		cur := uploading.Load()
		if cur+n > chatUploadBytes {
			return false
		}
		if uploading.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

// chatError is a chat request refused.
type chatError struct {
	status int
	msg    string
}

var (
	errNoChatMember = &chatError{stdhttp.StatusNotFound, "no such member"}
	errChatFull     = &chatError{stdhttp.StatusRequestEntityTooLarge, "the chat has no room for the file"}
)

// fileMessage is a message's file.
type fileMessage struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Type string `json:"type"`
}

// message is a chat message, immutable once stored.
type message struct {
	ID   string       `json:"id"`
	From string       `json:"from"`
	At   int64        `json:"at"`
	Text string       `json:"text,omitempty"`
	File *fileMessage `json:"file,omitempty"`

	seq  uint64 // the hub's order, which evicts the oldest file
	js   []byte // the message's JSON
	ev   []byte // the event that tells it
	data []byte // a file's bytes
}

// newTextMessage makes a text message from a member.
func newTextMessage(from, text string) *message {
	m := &message{ID: newKey(), From: from, At: time.Now().UnixMilli(), Text: text}
	m.encode()
	return m
}

// newFileMessage makes a file message from a member, taking over data.
func newFileMessage(from, name, ctype string, data []byte) *message {
	m := &message{ID: newKey(), From: from, At: time.Now().UnixMilli(),
		File: &fileMessage{Name: name, Size: int64(len(data)), Type: ctype}, data: data}
	m.encode()
	return m
}

// encode fills the message's JSON and its event; it is called once, and the
// message is immutable after it.
func (m *message) encode() {
	m.js, _ = json.Marshal(m) // a message of strings, a timestamp and a file's numbers
	ev := make([]byte, 0, len("event: chat\ndata: ")+len(m.js)+2)
	ev = append(ev, "event: chat\ndata: "...)
	ev = append(ev, m.js...)
	ev = append(ev, "\n\n"...)
	m.ev = ev
}

// historyEvent is the event that fills a browser's chat window when it joins
// the room: its messages, oldest first.
func historyEvent(msgs []*message) []byte {
	n := len("event: history\ndata: {\"messages\":[]}\n\n")
	for _, m := range msgs {
		n += len(m.js) + 1
	}
	b := make([]byte, 0, n)
	b = append(b, "event: history\ndata: {\"messages\":["...)
	for i, m := range msgs {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, m.js...)
	}
	return append(b, "]}\n\n"...)
}

// broadcastMessage sends a chat message's event to ws. It does not block.
func broadcastMessage(ws []*watcher, msg *message) {
	for _, w := range ws {
		w.post(msg)
	}
}

// chatMemberLocked returns the member named name, when key is its key.
// h.mu is held.
func chatMemberLocked(r *room, name, key string) *member {
	if r == nil {
		return nil
	}
	m := r.members[name]
	if m == nil || subtle.ConstantTimeCompare([]byte(key), []byte(m.key)) != 1 {
		return nil
	}
	return m
}

// chat adds a text message from the member its name and key belong to.
func (h *hub) chat(roomN, name, key, text string) (*message, *chatError) {
	h.mu.Lock()
	r := h.roomLocked(roomN, false)
	m := chatMemberLocked(r, name, key)
	if m == nil {
		h.mu.Unlock()
		return nil, errNoChatMember
	}
	msg := newTextMessage(m.name, text)
	h.storeLocked(r, msg)
	ws := r.watcherList()
	h.mu.Unlock()
	broadcastMessage(ws, msg)
	return msg, nil
}

// isMember reports whether name and key are a member of room roomN.
func (h *hub) isMember(roomN, name, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return chatMemberLocked(h.roomLocked(roomN, false), name, key) != nil
}

// file adds a file message from the member its name and key belong to. Adding
// it past chatFileBytes drops the oldest file messages of the hub's rooms.
func (h *hub) file(roomN, name, key, fname, ctype string, data []byte) (*message, *chatError) {
	h.mu.Lock()
	r := h.roomLocked(roomN, false)
	m := chatMemberLocked(r, name, key)
	if m == nil {
		h.mu.Unlock()
		return nil, errNoChatMember
	}
	size := int64(len(data))
	if size > chatFileBytes {
		// No eviction makes room for it: the files there stay.
		h.mu.Unlock()
		return nil, errChatFull
	}
	for h.fileBytes+size > chatFileBytes {
		victim, room := h.oldestFileLocked()
		if victim == nil {
			h.mu.Unlock()
			return nil, errChatFull
		}
		h.removeMessageLocked(room, victim)
	}
	msg := newFileMessage(m.name, fname, ctype, data)
	h.storeLocked(r, msg)
	ws := r.watcherList()
	h.mu.Unlock()
	broadcastMessage(ws, msg)
	return msg, nil
}

// fileOf returns the file message id of room roomN, while it is still there.
func (h *hub) fileOf(roomN, id string) *message {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[roomN]
	if r == nil {
		return nil
	}
	for _, m := range r.messages {
		if m.ID == id && m.File != nil {
			return m
		}
	}
	return nil
}

// storeLocked numbers msg, counts its file's bytes, adds it to r, and drops
// r's oldest message past chatHistory. h.mu is held.
func (h *hub) storeLocked(r *room, msg *message) {
	h.chatSeq++
	msg.seq = h.chatSeq
	if msg.File != nil {
		h.fileBytes += msg.File.Size
		r.fileBytes += msg.File.Size
	}
	r.messages = append(r.messages, msg)
	for len(r.messages) > chatHistory {
		h.dropFirstLocked(r)
	}
}

// dropFirstLocked drops r's oldest message.
func (h *hub) dropFirstLocked(r *room) {
	old := r.messages[0]
	r.messages[0] = nil
	r.messages = r.messages[1:]
	if old.File != nil {
		h.fileBytes -= old.File.Size
		r.fileBytes -= old.File.Size
	}
}

// removeMessageLocked removes msg, one of r's messages, as eviction does.
func (h *hub) removeMessageLocked(r *room, msg *message) {
	i := slices.Index(r.messages, msg)
	copy(r.messages[i:], r.messages[i+1:])
	r.messages[len(r.messages)-1] = nil
	r.messages = r.messages[:len(r.messages)-1]
	if msg.File != nil {
		h.fileBytes -= msg.File.Size
		r.fileBytes -= msg.File.Size
	}
}

// oldestFileLocked returns the hub's oldest file message, its room's messages
// being in order, and its room. h.mu is held.
func (h *hub) oldestFileLocked() (*message, *room) {
	var oldest *message
	var oldestRoom *room
	for _, r := range h.rooms {
		for _, m := range r.messages {
			if m.File == nil {
				continue
			}
			if oldest == nil || m.seq < oldest.seq {
				oldest, oldestRoom = m, r
			}
			break
		}
	}
	return oldest, oldestRoom
}

// forgetLocked drops r's file bytes from the hub's count, when the room is
// forgotten or the hub is closed. h.mu is held.
func (h *hub) forgetLocked(r *room) {
	h.fileBytes -= r.fileBytes
	r.fileBytes = 0
	r.messages = nil
}

// chatHeaders returns the member a chat request says it is.
func chatHeaders(c *fibhttp.Context) (name, key string) {
	return strings.TrimSpace(c.Request.Header.Get("X-Fit-Member")), strings.TrimSpace(c.Request.Header.Get("X-Fit-Room-Key"))
}

// chat answers a text message: {text}, as the member X-Fit-Member and
// X-Fit-Room-Key name.
func (p *Pages) chat(c *fibhttp.Context, roomN string) {
	set := p.hub.set.Load()
	member, key := chatHeaders(c)
	if !memberName.MatchString(member) || key == "" {
		plain(c, stdhttp.StatusNotFound, "no such member", nil)
		return
	}
	readBody(c, p.settings.maxMessage, func(body []byte) {
		if !utf8.Valid(body) {
			plain(c, stdhttp.StatusBadRequest, "the message is not UTF-8", nil)
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(body, &in) != nil {
			plain(c, stdhttp.StatusBadRequest, "the body must be JSON: {\"text\"}", nil)
			return
		}
		switch {
		case strings.TrimSpace(in.Text) == "":
			plain(c, stdhttp.StatusBadRequest, "the message is empty", nil)
			return
		case len(in.Text) > set.maxMessage:
			plain(c, stdhttp.StatusBadRequest, "the message is longer than max_message_bytes", nil)
			return
		}
		msg, e := p.hub.chat(roomN, member, key, in.Text)
		if e != nil {
			plain(c, e.status, e.msg, nil)
			return
		}
		writeJSONBytes(c, stdhttp.StatusOK, msg.js, nil)
	})
}

// upload answers a file message: the body is the file, its name is the file
// query parameter, and it is from the member X-Fit-Member and
// X-Fit-Room-Key name. Before a byte of the body is read, the member must be
// one, the file must fit max_file_bytes and what the hub keeps by its
// Content-Length, and its bytes must fit in chatUploadBytes, so that a
// client that is no member, or many uploading at once, cannot make the
// process hold their bodies; the member is checked again once it is all
// here.
func (p *Pages) upload(c *fibhttp.Context, roomN string) {
	set := p.hub.set.Load()
	name := sanitizeFileName(c.Request.URL.Query().Get("file"))
	if name == "" || len(name) > maxChatFileName {
		plain(c, stdhttp.StatusBadRequest, "the file's name must be 1 to 200 bytes", nil)
		return
	}
	member, key := chatHeaders(c)
	if !memberName.MatchString(member) || key == "" {
		plain(c, stdhttp.StatusNotFound, "no such member", nil)
		return
	}
	ctype := strings.TrimSpace(c.Request.Header.Get("Content-Type"))
	if len(ctype) > maxChatFileType {
		ctype = "application/octet-stream"
	}
	if _, _, err := mime.ParseMediaType(ctype); err != nil {
		ctype = "application/octet-stream"
	}
	if !p.hub.isMember(roomN, member, key) {
		plain(c, errNoChatMember.status, errNoChatMember.msg, nil)
		return
	}
	n := c.Request.ContentLength
	if n < 0 {
		n = set.maxFile
	}
	switch {
	case n > set.maxFile:
		plain(c, stdhttp.StatusRequestEntityTooLarge, "the body is too large", nil)
		return
	case n > chatFileBytes:
		plain(c, errChatFull.status, errChatFull.msg, nil)
		return
	case !reserveUpload(n):
		plain(c, stdhttp.StatusServiceUnavailable, "too many files are being uploaded; try again", stdhttp.Header{"Retry-After": {"1"}})
		return
	}
	readUpload(c, n, func(body []byte) {
		msg, e := p.hub.file(roomN, member, key, name, ctype, body)
		if e != nil {
			plain(c, e.status, e.msg, nil)
			return
		}
		writeJSONBytes(c, stdhttp.StatusOK, msg.js, nil)
	})
}

// readUpload gathers an upload's body of at most reserved bytes, which
// reserveUpload reserved, calls done with it, and gives the reservation
// back once the body is done with, read whole or not. The buffer never
// grows past reserved: one of the Content-Length is made at once, and
// without one it doubles up to reserved.
func readUpload(c *fibhttp.Context, reserved int64, done func([]byte)) {
	var buf []byte
	if c.Request.ContentLength > 0 {
		buf = make([]byte, 0, reserved)
	}
	finished := false
	c.OnBody(func(data []byte, fin bool, err error) {
		if finished {
			return
		}
		if err != nil {
			finished = true
			uploading.Add(-reserved)
			return
		}
		need := int64(len(buf)) + int64(len(data))
		if need > reserved {
			finished = true
			uploading.Add(-reserved)
			plain(c, stdhttp.StatusRequestEntityTooLarge, "the body is too large", nil)
			return
		}
		if need > int64(cap(buf)) {
			nb := make([]byte, len(buf), min(max(2*int64(cap(buf)), need, 64<<10), reserved))
			copy(nb, buf)
			buf = nb
		}
		buf = append(buf, data...)
		if fin {
			finished = true
			done(buf)
			uploading.Add(-reserved)
		}
	})
}

// download answers a file message's bytes.
func (p *Pages) download(c *fibhttp.Context, roomN, id string) {
	msg := p.hub.fileOf(roomN, id)
	if msg == nil {
		plain(c, stdhttp.StatusNotFound, "no such file", nil)
		return
	}
	// The upload made the type one that parses, application/octet-stream
	// at worst; a filename parameter of any value formats.
	hdr := stdhttp.Header{
		"Content-Type":           {msg.File.Type},
		"Content-Length":         {strconv.Itoa(len(msg.data))},
		"Content-Disposition":    {mime.FormatMediaType("attachment", map[string]string{"filename": msg.File.Name})},
		"X-Content-Type-Options": {"nosniff"},
		"Cache-Control":          {"no-store"},
	}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: msg.data})
}

// sanitizeFileName removes what a file's name may not have: control
// characters, and the path separators / and \.
func sanitizeFileName(name string) string {
	if len(name) > 4*maxChatFileName {
		// No name this long can come back short enough.
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r == '/', r == '\\', unicode.IsControl(r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

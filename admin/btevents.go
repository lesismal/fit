package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lesismal/fit/bittorrent"
)

// GET /bittorrent/events streams the BitTorrent client's events as
// server-sent events: each one "id: <n>", "event: <type>" and "data: <the
// event as JSON>", with a comment every btHeartbeat while none comes. A
// client that reconnects with Last-Event-ID (or ?after=<id>) gets the
// events it missed first, of the last bittorrent.EventHistory the role
// keeps; when some are no longer kept, or fit started again, the stream
// begins with an event reset, after which the client reads the torrents
// again. A client that does not take its events fast enough, so that
// bittorrent.SubscriberBuffer of them wait, gets an event lagged and is
// disconnected, to reconnect from the last ID it had; when the role stops,
// an event end closes the stream. At most bittorrent.MaxSubscribers streams
// run at once; one more is answered 503.
//
// The stream needs a connection of its own: it is served on the Admin
// API's address, not through the console, which answers whole responses
// (501 there).

// btEvents answers GET /bittorrent/events.
func (s *Server) btEvents(w http.ResponseWriter, r *http.Request) {
	role := s.btRole(w)
	if role == nil {
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, http.StatusNotImplemented, "the event stream is served on the Admin API's own address, not through the console")
		return
	}
	var after uint64
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = r.URL.Query().Get("after")
	}
	if last != "" {
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Last-Event-ID, or after, is an event's ID: a number")
			return
		}
		after = n
	}
	sub, replay, gap, err := role.Subscribe(after)
	if err != nil {
		btError(w, err)
		return
	}
	defer sub.Close()

	rc := http.NewResponseController(w)
	// The stream lasts as long as the client listens: not the server's
	// read timeout, which would end the request, nor its write timeout,
	// which each write sets again below.
	_ = rc.SetReadDeadline(time.Time{})
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(b []byte) bool {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	notice := func(event, msg string) []byte {
		data, _ := json.Marshal(map[string]string{"message": msg})
		return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, data)
	}
	if !send([]byte("retry: 5000\n: fit bittorrent events\n\n")) {
		return
	}
	if gap && !send(notice("reset", "events were missed, or fit started again: read the torrents again")) {
		return
	}
	for _, e := range replay {
		if !send(eventBytes(e)) {
			return
		}
	}
	beat := time.NewTicker(btHeartbeat)
	defer beat.Stop()
	for {
		select {
		case e, ok := <-sub.Events():
			if !ok {
				if sub.Lagged() {
					send(notice("lagged", fmt.Sprintf("more than %d events were waiting: reconnect with Last-Event-ID", bittorrent.SubscriberBuffer)))
				} else {
					send(notice("end", "the bittorrent.client role stopped"))
				}
				return
			}
			if !send(eventBytes(e)) {
				return
			}
		case now := <-beat.C:
			if !send(fmt.Appendf(nil, ": heartbeat %s\n\n", now.UTC().Format(time.RFC3339))) {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.closing:
			send(notice("end", "the Admin API is shutting down"))
			return
		}
	}
}

// eventBytes is e as a server-sent event.
func eventBytes(e bittorrent.Event) []byte {
	data, err := json.Marshal(e)
	if err != nil {
		data = []byte(`{}`)
	}
	return fmt.Appendf(nil, "id: %d\nevent: %s\ndata: %s\n\n", e.ID, e.Type, data)
}

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/bittorrent"
)

// The console's own route for the BitTorrent client's events (I204):
// GET /api/m/admin/bittorrent/events, the same stream as the Admin API's
// GET /bittorrent/events (btevents.go), which the console's proxy cannot
// pass on, since it answers whole responses. It is served on fib's
// connection itself: the handler subscribes and writes the stream's start
// on the worker, which neither waits nor blocks, and one goroutine of the
// stream's own then writes each event with WriteNoWait; when the
// connection holds more than its watermark, that goroutine, never a
// worker, waits for OnWritable, at most btStall, and a client that takes
// nothing for that long is cut. The goroutines are bounded by the role's
// bittorrent.MaxSubscribers, and each subscription by its buffer, which
// disconnects a client that falls behind with an event lagged, as the
// Admin API's stream does.

// btStall bounds how long a stream waits for its client to take what it
// was sent.
var btStall = 30 * time.Second

// btConsoleEvents answers GET /api/bittorrent/events on the console.
func (cs *Console) btConsoleEvents(r *console.Request) {
	role := bittorrent.Lookup(cs.configs.env.Engine)
	if role == nil {
		_ = console.Error(r.Context, http.StatusNotFound, "the bittorrent.client role does not run here")
		return
	}
	var after uint64
	last := r.Request.Header.Get("Last-Event-ID")
	if last == "" {
		last = r.Request.URL.Query().Get("after")
	}
	if last != "" {
		n, err := strconv.ParseUint(last, 10, 64)
		if err != nil {
			_ = console.Error(r.Context, http.StatusBadRequest, "Last-Event-ID, or after, is an event's ID: a number")
			return
		}
		after = n
	}
	sub, replay, gap, err := role.Subscribe(after)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, bittorrent.ErrTooManySubscribers) || errors.Is(err, bittorrent.ErrNotRunning) {
			r.Context.Header().Set("Retry-After", "5")
			status = http.StatusServiceUnavailable
		}
		_ = console.Error(r.Context, status, err.Error())
		return
	}
	s := &btStream{ctx: r.Context, sub: sub, done: make(chan struct{}), writable: make(chan error, 1)}
	r.Context.Retain()
	ctx := r.Context
	_ = ctx.SetWriteDeadline(time.Time{})
	h := ctx.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	ctx.WriteHeader(http.StatusOK)
	if _, err := ctx.WriteNoWait([]byte("retry: 5000\n: fit bittorrent events\n\n")); err != nil {
		sub.Close()
		ctx.Release()
		return
	}
	ctx.Flush()
	ctx.OnCancel(func(error) { s.stop() })
	go s.run(replay, gap)
}

// btStream is a stream of events on the console.
type btStream struct {
	ctx      *fibhttp.Context
	sub      *bittorrent.Subscription
	done     chan struct{} // closed when the client left
	once     sync.Once
	writable chan error // OnWritable's word: one waits at a time
}

func (s *btStream) stop() { s.once.Do(func() { close(s.done) }) }

// run writes the stream until the client leaves, falls behind or the role
// stops, then ends the response.
func (s *btStream) run(replay []bittorrent.Event, gap bool) {
	defer func() {
		s.sub.Close()
		s.ctx.Release()
	}()
	notice := func(event, msg string) []byte {
		data, _ := json.Marshal(map[string]string{"message": msg})
		return fmt.Appendf(nil, "event: %s\ndata: %s\n\n", event, data)
	}
	if gap && !s.write(notice("reset", "events were missed, or fit started again: read the torrents again")) {
		return
	}
	for _, e := range replay {
		if !s.write(eventBytes(e)) {
			return
		}
	}
	beat := time.NewTicker(btHeartbeat)
	defer beat.Stop()
	for {
		select {
		case e, ok := <-s.sub.Events():
			if !ok {
				if s.sub.Lagged() {
					s.write(notice("lagged", fmt.Sprintf("more than %d events were waiting: reconnect with Last-Event-ID", bittorrent.SubscriberBuffer)))
				} else {
					s.write(notice("end", "the bittorrent.client role stopped"))
				}
				return
			}
			if !s.write(eventBytes(e)) {
				return
			}
		case now := <-beat.C:
			if !s.write(fmt.Appendf(nil, ": heartbeat %s\n\n", now.UTC().Format(time.RFC3339))) {
				return
			}
		case <-s.done:
			return
		}
	}
}

// write writes b without blocking a worker: past the connection's
// watermark it waits, on the stream's goroutine, for the client to take
// half of it, at most btStall. It reports false once the stream is over.
func (s *btStream) write(b []byte) bool {
	above, err := s.ctx.WriteNoWait(b)
	if err != nil {
		return false
	}
	if !above {
		return true
	}
	_ = s.ctx.SetWriteDeadline(time.Now().Add(btStall))
	if !s.ctx.OnWritable(func(err error) { s.writable <- err }) {
		_ = s.ctx.SetWriteDeadline(time.Time{})
		return true
	}
	select {
	case err := <-s.writable:
		if err != nil {
			return false
		}
		_ = s.ctx.SetWriteDeadline(time.Time{})
		return true
	case <-s.done:
		return false
	}
}

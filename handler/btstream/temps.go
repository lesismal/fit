package btstream

import (
	"context"
	"sync"
	"time"

	"github.com/lesismal/fit/bittorrent"
)

// The torrents streamed, by role and ID, shared by every bt_stream handler
// and across reloads: how many streams read each, and, once none does, the
// timer that removes it, temporary_ttl later, when it is a temporary one
// (the role's RemoveTemporary leaves any other). While a removal runs, a
// stream of the same torrent waits for it, so that it never reads a torrent
// being removed under it; it then adds it again, or finds it gone.
var (
	tempMu sync.Mutex
	temps  = map[key]*held{}
)

type key struct {
	role *bittorrent.Role
	id   string
}

type held struct {
	streams int
	timer   *time.Timer
	// gen tells a timer superseded by a stream since from the last one.
	gen uint64
	// removing is closed once a removal under way ends.
	removing chan struct{}
}

// acquire counts a stream of k, after any removal of it under way, and
// stops its removal timer. It reports false when ctx ended first.
func acquire(ctx context.Context, k key) bool {
	tempMu.Lock()
	for {
		e := temps[k]
		if e == nil {
			e = &held{}
			temps[k] = e
		}
		if ch := e.removing; ch != nil {
			tempMu.Unlock()
			select {
			case <-ch:
			case <-ctx.Done():
				return false
			}
			tempMu.Lock()
			continue
		}
		e.streams++
		if e.timer != nil {
			e.timer.Stop()
			e.timer = nil
		}
		e.gen++
		tempMu.Unlock()
		return true
	}
}

// release counts a stream of k ended; after the last, k is removed ttl
// later if it is temporary and no stream has acquired it since.
func release(k key, ttl time.Duration) {
	tempMu.Lock()
	defer tempMu.Unlock()
	e := temps[k]
	if e == nil || e.streams == 0 {
		return
	}
	e.streams--
	if e.streams > 0 {
		return
	}
	e.gen++
	gen := e.gen
	e.timer = time.AfterFunc(ttl, func() { linger(k, gen) })
}

// linger removes k, its timer having fired, unless a stream acquired it
// since. It runs on the timer's goroutine: the removal deletes files.
func linger(k key, gen uint64) {
	tempMu.Lock()
	e := temps[k]
	if e == nil || e.gen != gen || e.streams > 0 {
		tempMu.Unlock()
		return
	}
	e.timer = nil
	ch := make(chan struct{})
	e.removing = ch
	tempMu.Unlock()

	_, _ = k.role.RemoveTemporary(k.id)

	tempMu.Lock()
	close(ch)
	e.removing = nil
	if e.streams == 0 && e.timer == nil {
		delete(temps, k)
	}
	tempMu.Unlock()
}

// streamsOf returns how many streams read the torrent id of role, for
// tests.
func streamsOf(role *bittorrent.Role, id string) int {
	tempMu.Lock()
	defer tempMu.Unlock()
	if e := temps[key{role, id}]; e != nil {
		return e.streams
	}
	return 0
}

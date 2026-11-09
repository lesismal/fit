package bittorrent

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
)

// The role's events, for the Admin API's /bittorrent/events stream and the
// pages: torrents added and removed, a torrent's state changing, completing
// or failing, a peer banned, a torrent reaching its seed limit, and the
// alternative rates switching. They are published by the role's API, its
// manager goroutine and fib's event goroutine, never on a fib worker, and
// publishing never waits: each subscriber has a bounded buffer, and one
// whose buffer is full is dropped (its Events channel closed, Lagged true),
// to subscribe again from the last event it had, which the role keeps the
// last EventHistory of.

// The types of the role's events.
const (
	// EventAdded: a torrent was added (State is its state then).
	EventAdded = "added"
	// EventRemoved: a torrent was removed (DataDeleted says its files went
	// with it).
	EventRemoved = "removed"
	// EventState: a torrent's state changed from Previous to State, as a
	// status reports them (Error says why for the state error).
	EventState = "state"
	// EventCompleted: a torrent has every piece it wants.
	EventCompleted = "completed"
	// EventError: a torrent's storage failed (Error), which does not stop
	// it; a torrent stopped by an error is an EventState to error.
	EventError = "error"
	// EventPeerBanned: a peer, or every peer of an address (Peer), was
	// banned (Error says why); it has no torrent.
	EventPeerBanned = "peer_banned"
	// EventSeedLimit: a torrent reached its seed ratio or seed time and
	// paused.
	EventSeedLimit = "seed_limit"
	// EventAltSpeed: the alternative rates started (State "on") or stopped
	// ("off") holding; it has no torrent.
	EventAltSpeed = "alt_speed"
	// EventDropped: the client dropped Dropped of its piece and peer
	// events since the last look, its queue for the role being full; it
	// has no torrent. The role publishes none of those events, so nothing
	// of the stream is missing; a torrent's numbers are read again from
	// its status. At most one a second.
	EventDropped = "events_dropped"
)

// EventQueue bounds the client's events waiting for the role's handler,
// fib's Config.EventQueue: past it fib drops piece and peer events, never
// the others the role publishes. The handler takes a short lock and
// returns, so the queue fills only in a burst.
const EventQueue = fibbt.DefaultEventQueue

// The bounds of the events.
const (
	// MaxSubscribers bounds the subscribers at once: one more is refused
	// with ErrTooManySubscribers.
	MaxSubscribers = 32
	// SubscriberBuffer is how many events a subscriber may have waiting:
	// past it the subscriber is dropped.
	SubscriberBuffer = 256
	// EventHistory is how many of the last events the role keeps for a
	// subscriber to start after.
	EventHistory = 1024
)

// ErrTooManySubscribers is a Subscribe while MaxSubscribers subscribers
// listen.
var ErrTooManySubscribers = errors.New("bittorrent: as many event subscribers as allowed are listening already")

// Event is something that happened to the role or one of its torrents.
type Event struct {
	// ID numbers the role's events from 1, in the order they happened,
	// since the role started.
	ID   uint64    `json:"id"`
	Time time.Time `json:"time"`
	Type string    `json:"type"`
	// Torrent, Name and Category are the torrent's, for the events of a
	// torrent.
	Torrent  string `json:"torrent,omitempty"`
	Name     string `json:"name,omitempty"`
	Category string `json:"category,omitempty"`
	// State is the torrent's state, or "on" and "off" for EventAltSpeed;
	// Previous the state before, for EventState.
	State    string `json:"state,omitempty"`
	Previous string `json:"previous,omitempty"`
	Error    string `json:"error,omitempty"`
	// Peer is the peer EventPeerBanned bans: an address, or an address and
	// port.
	Peer string `json:"peer,omitempty"`
	// DataDeleted says an EventRemoved deleted the torrent's files.
	DataDeleted bool `json:"data_deleted,omitempty"`
	// Dropped is how many client events an EventDropped stands for.
	Dropped int64 `json:"dropped,omitempty"`
}

// hub keeps the last events and hands new ones to the subscribers.
type hub struct {
	mu   sync.Mutex
	seq  uint64
	ring [EventHistory]Event
	// n is how many events ring holds; the newest is at (seq-1)%len.
	n      int
	subs   map[*Subscription]struct{}
	closed bool
	// dropped counts the subscribers dropped for a full buffer.
	dropped atomic.Uint64
}

func newHub() *hub { return &hub{subs: map[*Subscription]struct{}{}} }

// Subscription is a subscriber to the role's events.
type Subscription struct {
	h      *hub
	c      chan Event
	lagged atomic.Bool
}

// Events returns the subscription's channel, closed when the subscriber is
// dropped (Lagged), closed (Close) or the role stops.
func (s *Subscription) Events() <-chan Event { return s.c }

// Lagged reports whether the subscription was dropped for not taking its
// events fast enough: its buffer of SubscriberBuffer filled.
func (s *Subscription) Lagged() bool { return s.lagged.Load() }

// Close ends the subscription.
func (s *Subscription) Close() {
	h := s.h
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.c)
	}
}

// emit numbers e, keeps it, and hands it to every subscriber, dropping
// those whose buffer is full. It never waits.
func (h *hub) emit(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.ID = h.seq
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	h.ring[(h.seq-1)%EventHistory] = e
	h.n = min(h.n+1, EventHistory)
	for s := range h.subs {
		select {
		case s.c <- e:
		default:
			s.lagged.Store(true)
			delete(h.subs, s)
			close(s.c)
			h.dropped.Add(1)
		}
	}
}

// subscribe adds a subscriber. With after > 0 it also returns the events
// kept after the event after, oldest first, which come before any on the
// subscription's channel, and whether some were lost in between: the event
// after+1 is no longer kept.
func (h *hub) subscribe(after uint64) (*Subscription, []Event, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, nil, false, ErrNotRunning
	}
	if len(h.subs) >= MaxSubscribers {
		return nil, nil, false, ErrTooManySubscribers
	}
	s := &Subscription{h: h, c: make(chan Event, SubscriberBuffer)}
	h.subs[s] = struct{}{}
	if after == 0 || after >= h.seq {
		// after past the last event: the role started again since.
		return s, nil, after > h.seq, nil
	}
	oldest := h.seq - uint64(h.n) + 1
	gap := after+1 < oldest
	from := max(after+1, oldest)
	replay := make([]Event, 0, h.seq-from+1)
	for id := from; id <= h.seq; id++ {
		replay = append(replay, h.ring[(id-1)%EventHistory])
	}
	return s, replay, gap, nil
}

// close ends every subscription, as the role stops.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		delete(h.subs, s)
		close(s.c)
	}
}

// subscribers counts the subscribers now.
func (h *hub) subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Subscribe subscribes to the role's events. With after, the ID of the last
// event a subscriber had, it also returns the events kept since, oldest
// first, to take before those of the subscription, and gap true when some
// in between are no longer kept, or after is past the last event, as after
// a restart of fit: the subscriber should read the torrents again. At most
// MaxSubscribers subscribe at once (ErrTooManySubscribers); a stopped role
// answers ErrNotRunning. Close the subscription when done.
func (r *Role) Subscribe(after uint64) (sub *Subscription, replay []Event, gap bool, err error) {
	return r.events.subscribe(after)
}

// event publishes e and writes it to the role's log: torrents added and
// removed, peers banned and seed limits at info, state changes at debug.
// Completions, storage errors and the alternative rates are logged where
// they are noticed, as before the events. It never waits; r.mu may be
// held.
func (r *Role) event(e Event) {
	if r.log != nil {
		switch e.Type {
		case EventAdded:
			r.log.Infof("torrent %s (%s) added, %s", e.Torrent, e.Name, e.State)
		case EventRemoved:
			if e.DataDeleted {
				r.log.Infof("torrent %s (%s) removed with its files", e.Torrent, e.Name)
			} else {
				r.log.Infof("torrent %s (%s) removed", e.Torrent, e.Name)
			}
		case EventState:
			if e.Error != "" {
				r.log.Debugf("torrent %s (%s): %s -> %s: %s", e.Torrent, e.Name, e.Previous, e.State, e.Error)
			} else {
				r.log.Debugf("torrent %s (%s): %s -> %s", e.Torrent, e.Name, e.Previous, e.State)
			}
		case EventPeerBanned:
			r.log.Infof("peer %s banned: %s", e.Peer, e.Error)
		case EventSeedLimit:
			r.log.Infof("torrent %s (%s) reached its seed limit and paused", e.Torrent, e.Name)
		case EventDropped:
			r.log.Debugf("the client dropped %d piece and peer events, its queue full", e.Dropped)
		}
	}
	r.events.emit(e)
}

// torrentEvent returns an event of type typ about rec. r.mu is held.
func torrentEvent(typ string, rec *record) Event {
	return Event{Type: typ, Torrent: rec.id, Name: rec.name, Category: rec.category}
}

// stateLocked returns rec's state as a status reports it, and why for the
// state error. r.mu is held.
func (r *Role) stateLocked(rec *record) (string, string) {
	switch {
	case rec.err != nil:
		return "error", rec.err.Error()
	case rec.t == nil:
		return "stopped", ""
	case rec.held == holdQueue:
		return "queued", ""
	}
	st := rec.t.State()
	if st == fibbt.StateError {
		if err := rec.t.Err(); err != nil {
			return "error", err.Error()
		}
	}
	return st.String(), ""
}

// noteStatesLocked publishes the state changes of the torrents since the
// last look, and the client events dropped since. r.mu is held.
func (r *Role) noteStatesLocked() {
	if n := r.dropPending.Swap(0); n > 0 {
		r.event(Event{Type: EventDropped, Dropped: n})
	}
	for _, rec := range r.recs {
		s, why := r.stateLocked(rec)
		if rec.lastState == s {
			continue
		}
		if rec.lastState != "" {
			e := torrentEvent(EventState, rec)
			e.State, e.Previous, e.Error = s, rec.lastState, why
			r.event(e)
		}
		rec.lastState = s
	}
}

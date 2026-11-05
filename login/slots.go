package login

import (
	"net/netip"
	"sync"
	"sync/atomic"
)

// AuthShare is the part of a page's connections kept for authenticated
// ones: connections that have not authenticated hold at most max_conns
// less max_conns / AuthShare of them.
const AuthShare = 4

// Slots counts the connections of the Admin API or a console against
// max_conns, and against each client's share of them (Peers).
//
// A connection that has not authenticated yet is one anyone may open. When
// the connections are all taken, or the unauthenticated ones hold all but
// the share kept for authenticated ones, a new connection is not refused:
// it closes an unauthenticated one and takes its slot, the oldest of the
// client that holds the most of them (the host's own clients, on loopback
// or a Unix socket, counting as one), so that clients that only reconnect,
// from a few addresses or from the host, cannot keep out one that
// authenticates, and their reconnecting closes their own connections first.
// Only when every connection has authenticated is a new one refused.
// Authenticated connections are never closed for another. Its methods are
// safe to call from any goroutine.
type Slots struct {
	max, unauthMax int
	peers          *Peers

	mu     sync.Mutex
	open   int
	unauth int
	// held counts the unauthenticated connections of each client, by
	// Peer, the host's own under the zero Addr.
	held map[netip.Addr]int
	// head and tail are the unauthenticated connections, oldest first.
	head, tail *Slot
}

// NewSlots returns the counts of a server that keeps conns connections open
// at most, and none for conns 0 or less.
func NewSlots(conns int) *Slots {
	conns = max(0, conns)
	return &Slots{max: conns, unauthMax: conns - conns/AuthShare, peers: NewPeers(conns), held: map[netip.Addr]int{}}
}

// Peers returns the counts of each client's connections.
func (s *Slots) Peers() *Peers { return s.peers }

// Slot is one connection Slots counts.
type Slot struct {
	s     *Slots
	ip    netip.Addr
	key   netip.Addr // its client, as Peer names it, or the zero Addr for the host's own
	evict func()
	// authed is written under s.mu, and read anywhere.
	authed     atomic.Bool
	released   bool // under s.mu
	prev, next *Slot
}

// Open counts a connection from ip, unauthenticated, and returns its slot;
// or nil, counting nothing, when its client already holds its share, or
// every connection has authenticated. It may close an unauthenticated
// connection for it, as Slots says, calling that one's evict, after which
// that one's Release does nothing. Each slot is released when its
// connection closes.
func (s *Slots) Open(ip netip.Addr, evict func()) *Slot {
	if !s.peers.Open(ip) {
		return nil
	}
	key, _ := Peer(ip)
	s.mu.Lock()
	var victim *Slot
	if s.open >= s.max || s.unauth >= s.unauthMax {
		if victim = s.victim(); victim == nil {
			s.mu.Unlock()
			s.peers.Close(ip)
			return nil
		}
		s.drop(victim)
	}
	sl := &Slot{s: s, ip: ip, key: key, evict: evict}
	s.open++
	s.push(sl)
	s.mu.Unlock()
	if victim != nil {
		s.peers.Close(victim.ip)
		victim.evict()
	}
	return sl
}

// victim returns the unauthenticated connection a new one closes: the
// oldest of the client that holds the most; nil when there is none.
func (s *Slots) victim() *Slot {
	most := 0
	for _, n := range s.held {
		most = max(most, n)
	}
	for sl := s.head; sl != nil; sl = sl.next {
		if s.held[sl.key] == most {
			return sl
		}
	}
	return nil
}

// push puts sl at the end of the unauthenticated connections.
func (s *Slots) push(sl *Slot) {
	sl.prev, sl.next = s.tail, nil
	if s.tail != nil {
		s.tail.next = sl
	} else {
		s.head = sl
	}
	s.tail = sl
	s.unauth++
	s.held[sl.key]++
}

// unlink takes sl out of the unauthenticated connections.
func (s *Slots) unlink(sl *Slot) {
	if sl.prev != nil {
		sl.prev.next = sl.next
	} else {
		s.head = sl.next
	}
	if sl.next != nil {
		sl.next.prev = sl.prev
	} else {
		s.tail = sl.prev
	}
	sl.prev, sl.next = nil, nil
	s.unauth--
	if s.held[sl.key]--; s.held[sl.key] == 0 {
		delete(s.held, sl.key)
	}
}

// drop releases sl's slot, with s.mu held.
func (s *Slots) drop(sl *Slot) {
	if !sl.authed.Load() {
		s.unlink(sl)
	}
	sl.released = true
	s.open--
}

// Release frees the slot, once its connection has closed; again, or after
// the slot was taken for another, it does nothing.
func (sl *Slot) Release() {
	s := sl.s
	s.mu.Lock()
	if sl.released {
		s.mu.Unlock()
		return
	}
	s.drop(sl)
	s.mu.Unlock()
	s.peers.Close(sl.ip)
}

// Authenticate marks the connection as having authenticated: it is no
// longer closed for a newer one. It reports whether it was not marked so
// already.
func (sl *Slot) Authenticate() bool {
	if sl.authed.Load() {
		return false
	}
	s := sl.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if sl.authed.Load() || sl.released {
		return false
	}
	s.unlink(sl)
	sl.authed.Store(true)
	return true
}

// Unauthenticate marks an authenticated connection as unauthenticated
// again, as when its login has ended: it is then the newest of those a new
// connection may close. It reports whether it was authenticated.
func (sl *Slot) Unauthenticate() bool {
	if !sl.authed.Load() {
		return false
	}
	s := sl.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if !sl.authed.Load() || sl.released {
		return false
	}
	sl.authed.Store(false)
	s.push(sl)
	return true
}

// Authenticated reports whether the connection is marked as authenticated.
func (sl *Slot) Authenticated() bool { return sl.authed.Load() }

package login

import (
	"net/netip"
	"sync"
	"testing"
)

// slotsConn is a connection a test opens on Slots, and whether it was
// closed for a newer one.
type slotsConn struct {
	slot    *Slot
	evicted bool
}

func openSlot(s *Slots, ip string) *slotsConn {
	c := &slotsConn{}
	var addr netip.Addr
	if ip != "" {
		addr = netip.MustParseAddr(ip)
	}
	c.slot = s.Open(addr, func() { c.evicted = true })
	if c.slot == nil {
		return nil
	}
	return c
}

// TestSlotsEvictOldestUnauthenticated: with 8 connections, unauthenticated
// ones hold 6 at most; a new one past that closes the oldest
// unauthenticated one, and the authenticated ones are never closed for it.
func TestSlotsEvictOldestUnauthenticated(t *testing.T) {
	s := NewSlots(8)
	if s.Peers().Share() != 2 {
		t.Fatalf("share %d", s.Peers().Share())
	}
	var cs []*slotsConn
	for range 6 {
		cs = append(cs, openSlot(s, "127.0.0.1"))
	}
	// The seventh takes the first's slot.
	c7 := openSlot(s, "127.0.0.1")
	if c7 == nil || !cs[0].evicted || cs[1].evicted {
		t.Fatalf("evicted: %v %v", cs[0].evicted, cs[1].evicted)
	}
	cs[0].slot.Release() // after its eviction, nothing
	if s.open != 6 || s.unauth != 6 {
		t.Fatalf("open %d, unauth %d", s.open, s.unauth)
	}
	// Two authenticate: unauthenticated ones hold 4, so that a new one
	// takes a free slot.
	if !cs[1].slot.Authenticate() || cs[1].slot.Authenticate() || !cs[1].slot.Authenticated() {
		t.Fatal("Authenticate")
	}
	cs[2].slot.Authenticate()
	c8 := openSlot(s, "127.0.0.1")
	if c8 == nil || cs[3].evicted || s.open != 7 {
		t.Fatalf("a connection was closed with a slot free; open %d", s.open)
	}
	for _, c := range []*slotsConn{cs[3], cs[4], cs[5], c7, c8} {
		c.slot.Authenticate()
	}
	// Full, with one unauthenticated: it goes, and never an authenticated
	// one.
	a := openSlot(s, "::1")
	b := openSlot(s, "")
	if a == nil || b == nil || !a.evicted || s.open != 8 {
		t.Fatalf("open %d, a evicted %v", s.open, a.evicted)
	}
	for _, c := range append(cs[1:], c7, c8) {
		if c.evicted {
			t.Fatal("an authenticated connection was closed")
		}
	}
	b.slot.Authenticate()
	if openSlot(s, "127.0.0.1") != nil {
		t.Fatal("accepted with every connection authenticated")
	}
	// One that closes frees its slot.
	cs[1].slot.Release()
	cs[1].slot.Release()
	if openSlot(s, "127.0.0.1") == nil {
		t.Fatal("refused with a slot free")
	}
}

// TestSlotsUnauthenticate: a connection whose login ended is the newest
// unauthenticated one, and the last one closed for a new one.
func TestSlotsUnauthenticate(t *testing.T) {
	s := NewSlots(4) // 3 unauthenticated at most
	a, b, c := openSlot(s, "127.0.0.1"), openSlot(s, "127.0.0.1"), openSlot(s, "127.0.0.1")
	if a.slot.Unauthenticate() {
		t.Fatal("Unauthenticate on an unauthenticated slot")
	}
	a.slot.Authenticate()
	if !a.slot.Unauthenticate() || a.slot.Authenticated() {
		t.Fatal("Unauthenticate")
	}
	// b is the oldest unauthenticated one now, then c, then a.
	openSlot(s, "127.0.0.1")
	openSlot(s, "127.0.0.1")
	if !b.evicted || !c.evicted || a.evicted {
		t.Fatalf("evicted %v %v %v", b.evicted, c.evicted, a.evicted)
	}
	// Neither changes a slot taken for another.
	if b.slot.Authenticate() || b.slot.Unauthenticate() {
		t.Fatal("an evicted slot changed")
	}
	a.slot.Authenticate()
	a.slot.Release()
	if a.slot.Unauthenticate() {
		t.Fatal("a released slot changed")
	}
}

// TestSlotsPeerShare: a client past its share is refused, before any
// connection is closed for it; and none is taken with no connections.
func TestSlotsPeerShare(t *testing.T) {
	s := NewSlots(8)
	x, y := openSlot(s, "203.0.113.9"), openSlot(s, "203.0.113.9")
	if x == nil || y == nil || openSlot(s, "203.0.113.9") != nil {
		t.Fatal("the client's share")
	}
	z := openSlot(s, "2001:db8::1")
	w1, w2, w3 := openSlot(s, "127.0.0.1"), openSlot(s, "::1"), openSlot(s, "")
	// Full of unauthenticated ones: the oldest of the client that holds
	// the most, the host with 3, goes.
	if openSlot(s, "198.51.100.1") == nil || !w1.evicted || x.evicted {
		t.Fatal("not the host's oldest was closed")
	}
	// 203.0.113.9 and the host hold 2 each: the oldest of them, x, goes,
	// and its client's count with it.
	if openSlot(s, "2001:db8::2") == nil || !x.evicted || z.evicted {
		t.Fatal("not the oldest of the clients holding the most was closed")
	}
	if openSlot(s, "203.0.113.9") == nil {
		t.Fatal("the client's evicted connection is still counted")
	}
	// Now the /64 of z holds 2, as the host does: z is older.
	if !z.evicted || w2.evicted || w3.evicted || y.evicted {
		t.Fatal("not z was closed")
	}
	if NewSlots(0).Open(netip.Addr{}, func() {}) != nil || NewSlots(-1).Open(netip.Addr{}, func() {}) != nil {
		t.Fatal("a connection taken with max_conns 0")
	}
	one := NewSlots(1)
	first := openSlot(one, "")
	if second := openSlot(one, ""); first == nil || second == nil {
		t.Fatal("max_conns 1 takes no unauthenticated connection")
	}
}

// TestSlotsReconnectingAttacker: clients from three addresses that hold
// every unauthenticated slot, and reconnect each time one of theirs is
// closed, close only their own connections from then on: one from another
// address that came in once stays until it authenticates.
func TestSlotsReconnectingAttacker(t *testing.T) {
	s := NewSlots(16) // 12 unauthenticated, 4 a client
	addrs := []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"}
	var attackers []*slotsConn
	for _, a := range addrs {
		for range 4 {
			attackers = append(attackers, openSlot(s, a))
		}
	}
	admin := openSlot(s, "198.51.100.7")
	if admin == nil {
		t.Fatal("the admin was refused")
	}
	for i := 0; i < 1000; i++ {
		for j, c := range attackers {
			if c.evicted {
				if attackers[j] = openSlot(s, addrs[j/4]); attackers[j] == nil {
					t.Fatal("an attacker's reconnect was refused")
				}
			}
		}
		if admin.evicted {
			t.Fatalf("the admin's connection was closed after %d rounds", i)
		}
	}
	if !admin.slot.Authenticate() {
		t.Fatal("Authenticate")
	}
}

// TestSlotsConcurrent: under the race detector, connections that open,
// authenticate, unauthenticate and close at once leave the counts at 0.
func TestSlotsConcurrent(t *testing.T) {
	s := NewSlots(16)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 200 {
				sl := s.Open(netip.MustParseAddr("127.0.0.1"), func() {})
				if sl == nil {
					continue
				}
				if (g+i)%2 == 0 {
					sl.Authenticate()
					sl.Unauthenticate()
				}
				sl.Release()
			}
		})
	}
	wg.Wait()
	if s.open != 0 || s.unauth != 0 || s.head != nil || s.tail != nil {
		t.Fatalf("open %d, unauth %d", s.open, s.unauth)
	}
}

package login

import (
	"net/netip"
	"sync"
)

// PeerShare is the part of a page's connections one client may hold: a
// client other than a loopback one is refused a connection past
// max_conns / PeerShare of them, so that a few clients that never log in
// cannot hold every connection of the Admin API or a console.
const PeerShare = 4

// Peers counts the open connections of each client, by Peer, against a
// share of a bound on connections. Its methods are safe to call from any
// goroutine.
type Peers struct {
	share int
	mu    sync.Mutex
	n     map[netip.Addr]int // at most as many as connections are open
}

// NewPeers returns the counts of a server that keeps conns connections
// open at most: each client may hold conns/PeerShare of them, one at least.
func NewPeers(conns int) *Peers {
	return &Peers{share: max(1, conns/PeerShare), n: map[netip.Addr]int{}}
}

// Peer returns the client ip is counted as, as failed logins are: an IPv4
// address as it is, an IPv6 one by its IPv6Prefix; and false for one not
// counted, a loopback address or none, as a Unix socket's peer has: those
// clients are the host's own.
func Peer(ip netip.Addr) (netip.Addr, bool) {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() {
		return netip.Addr{}, false
	}
	if ip.Is6() {
		p, _ := ip.Prefix(IPv6Prefix)
		ip = p.Addr()
	}
	return ip, true
}

// Open counts a connection opened from ip, and reports false, counting
// nothing, when its client already holds its share. Each true is followed
// by Close(ip) when the connection closes.
func (p *Peers) Open(ip netip.Addr) bool {
	k, ok := Peer(ip)
	if !ok {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n[k] >= p.share {
		return false
	}
	p.n[k]++
	return true
}

// Close counts a connection from ip closed, as Open counted it.
func (p *Peers) Close(ip netip.Addr) {
	k, ok := Peer(ip)
	if !ok {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.n[k] <= 1 {
		delete(p.n, k)
	} else {
		p.n[k]--
	}
}

// Share returns how many connections a client may hold.
func (p *Peers) Share() int { return p.share }

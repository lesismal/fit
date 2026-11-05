// Package plaintcp listens the way net.Listen does, but never with Multipath
// TCP.
//
// Since Go 1.24, net.Listen on Linux opens a TCP listener as IPPROTO_MPTCP
// wherever the kernel allows it. fib's own listeners are plain TCP, and so
// should the ones fit opens beside them through the net package: the Admin
// API, and the binds that check an address is free. An MPTCP listener is what
// the published exploit of CVE-2026-98432, a use-after-free in the kernel's
// IPv4 TCP handshake, needs on its target. Turning MPTCP off does not fix
// that bug, which only a patched kernel does, but it keeps fit from offering
// more than fib does.
package plaintcp

import (
	"context"
	"net"
)

// Listen is net.Listen without Multipath TCP. SetMultipathTCP(false) wins over
// GODEBUG=multipathtcp, so no environment turns it back on. Networks other
// than TCP listen as net.Listen would.
func Listen(network, address string) (net.Listener, error) {
	var lc net.ListenConfig
	lc.SetMultipathTCP(false)
	return lc.Listen(context.Background(), network, address)
}

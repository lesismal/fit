// Package nettcp listens the way net.Listen does, with Multipath TCP on or
// off exactly as the caller says it: never left to the GODEBUG setting, and
// never taken by a listener that does not ask for it.
//
// Since Go 1.24, net.Listen on Linux opens a TCP listener as IPPROTO_MPTCP
// wherever the kernel allows it, which is what GODEBUG=multipathtcp controls.
// fib's own listeners are plain TCP unless a binding asks for Multipath TCP;
// the binds that check an address is free are plain TCP always, and the Admin
// API is plain TCP unless admin.mptcp asks for more, so both go through this
// package. An MPTCP listener is what the published exploit of CVE-2026-98432,
// a use-after-free in the kernel's IPv4 TCP handshake, needs on its target.
// Turning MPTCP off does not fix that bug, which only a patched kernel does,
// but it keeps fit from offering more than its config asks for.
package nettcp

import (
	"context"
	"net"
)

// Listen is net.Listen with Multipath TCP on when multipathTCP is set and off
// when it is not, whatever GODEBUG says. Multipath TCP is taken where the
// kernel offers it (Linux 5.6+, net.mptcp.enabled), and falls back to plain
// TCP where it does not, as net.ListenConfig.SetMultipathTCP(true) does;
// networks other than TCP listen as net.Listen would.
func Listen(network, address string, multipathTCP bool) (net.Listener, error) {
	var lc net.ListenConfig
	lc.SetMultipathTCP(multipathTCP)
	return lc.Listen(context.Background(), network, address)
}

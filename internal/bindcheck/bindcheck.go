// Package bindcheck checks that an address is free before one of fit's
// bindings takes it.
//
// fib's engines listen with SO_REUSEPORT by default, so that their pollers
// each accept an address's connections, or read its datagrams, on a socket
// of their own. The kernel lets any other socket of the same user that sets
// SO_REUSEPORT too share the address, another process's or another of this
// process's engine's bindings, and splits the connections between them
// without a word. A plain bind, without SO_REUSEPORT, which a socket holding
// the address refuses, tells: fib does the same itself for an engine without
// ReusePort (claimAddress in its internal/listener), and an address bound
// here is claimed the same way, a binding at a time, with ReusePort kept for
// the engine's pollers. Config validation refuses two of the config's own
// addresses on one port before anything binds; this catches the rest, at the
// moment of binding: another process, or an address a role's pages changed.
package bindcheck

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"

	"github.com/lesismal/fit/internal/nettcp"
)

// Free fails when a socket already holds address on network, "tcp" (TCP,
// TLS, WebSocket) or "udp" (QUIC, HTTP/3, SRT, TURN, WebRTC), with an
// error that says so. Port 0 is always free: the kernel picks one no socket
// holds.
//
// The probe binds and closes at once, so a socket that takes the address
// between it and the binding that follows is not caught: the window is that
// of fib's own claim, a few microseconds, and only a socket of the same user
// that sets SO_REUSEPORT itself can join a fib binding after it is bound.
func Free(network, address string) error {
	if _, port, err := net.SplitHostPort(address); err == nil && port == "0" {
		return nil
	}
	var err error
	if network == "udp" {
		var pc net.PacketConn
		if pc, err = net.ListenPacket("udp", address); err == nil {
			return pc.Close()
		}
	} else {
		var ln net.Listener
		if ln, err = nettcp.Listen("tcp", address, false); err == nil {
			return ln.Close()
		}
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("%s %s is taken: another process, or another part of fit, listens there: %w", strings.ToUpper(network), address, err)
	}
	return fmt.Errorf("listen on %s: %w", address, err)
}

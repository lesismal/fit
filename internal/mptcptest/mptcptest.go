// Package mptcptest tells tests whether Multipath TCP is available on this
// machine, and whether a connection uses it.
//
// Multipath TCP is a Linux feature (5.6+, net.mptcp.enabled): a kernel that
// offers it gives a net listener that asks for it, and a client that dials it,
// an MPTCP connection; everywhere else the two fall back to plain TCP, and
// tests of an MPTCP listener have nothing to check.
package mptcptest

import (
	"context"
	"net"
	"testing"
)

// Supported reports whether this machine's kernel offers Multipath TCP on
// loopback: a reference net listener that asks for it and a client that dials
// it get a connection that uses it. A test that needs Multipath TCP skips when
// it is false.
func Supported(t *testing.T) bool {
	t.Helper()
	var lc net.ListenConfig
	lc.SetMultipathTCP(true)
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return false
	}
	defer ln.Close()
	c, err := Dial(ln.Addr().String())
	if err != nil {
		return false
	}
	defer c.Close()
	s, err := ln.Accept()
	if err != nil {
		return false
	}
	defer s.Close()
	return InUse(s)
}

// SkipUnlessSupported skips the test when this machine's kernel does not offer
// Multipath TCP.
func SkipUnlessSupported(t *testing.T) {
	t.Helper()
	if !Supported(t) {
		t.Skip("Multipath TCP is not available here: no MPTCP in this kernel, or it is turned off")
	}
}

// Dial dials address asking for Multipath TCP, which the kernel falls back to
// plain TCP for where it, or the listener, does not offer it.
func Dial(address string) (*net.TCPConn, error) {
	var d net.Dialer
	d.SetMultipathTCP(true)
	c, err := d.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	return c.(*net.TCPConn), nil
}

// InUse reports whether c is a connection that uses Multipath TCP: the client
// and server sides met on it in the handshake.
func InUse(c net.Conn) bool {
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		return false
	}
	using, err := tcp.MultipathTCP()
	return err == nil && using
}

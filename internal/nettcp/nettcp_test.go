package nettcp

import (
	"context"
	"net"
	"testing"
)

// accepted listens with listen, dials it as dial says, and reports whether the
// connection it accepts uses Multipath TCP.
func accepted(t *testing.T, listen func(network, address string) (net.Listener, error), dial func(address string) (net.Conn, error)) bool {
	t.Helper()
	ln, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	using, err := s.(*net.TCPConn).MultipathTCP()
	if err != nil {
		t.Fatal(err)
	}
	return using
}

// plain dials the way net.Dial does, never with Multipath TCP.
func plain(address string) (net.Conn, error) {
	return net.Dial("tcp", address)
}

// multipath dials with Multipath TCP where the kernel offers it, falling back
// to plain TCP where it does not.
func multipath(address string) (net.Conn, error) {
	var d net.Dialer
	d.SetMultipathTCP(true)
	return d.DialContext(context.Background(), "tcp", address)
}

func TestListenIsNotMultipath(t *testing.T) {
	// An environment that would turn MPTCP on for net.Listen must not turn
	// it on here.
	t.Setenv("GODEBUG", "multipathtcp=1")
	listen := func(network, address string) (net.Listener, error) { return Listen(network, address, false) }
	if accepted(t, listen, plain) {
		t.Fatal("nettcp.Listen with Multipath TCP off accepted a Multipath TCP connection")
	}
	if accepted(t, listen, multipath) {
		t.Fatal("nettcp.Listen with Multipath TCP off accepted a Multipath TCP connection from an MPTCP client")
	}
}

// TestListenMultipath shows what Listen does when it is asked for Multipath
// TCP: it takes it where the kernel offers it, and falls back to plain TCP
// where it does not.
func TestListenMultipath(t *testing.T) {
	listen := func(network, address string) (net.Listener, error) { return Listen(network, address, true) }
	if !netListenIsMultipath(t) {
		// The kernel does not offer Multipath TCP, so the listener is plain
		// TCP, as net.Listen's fallback is.
		if accepted(t, listen, multipath) {
			t.Fatal("a kernel without Multipath TCP accepted a Multipath TCP connection")
		}
		return
	}
	if !accepted(t, listen, multipath) {
		t.Fatal("nettcp.Listen with Multipath TCP on did not take a Multipath TCP connection")
	}
}

// netListenIsMultipath reports whether the kernel offers Multipath TCP here:
// net.Listen takes it where it does.
func netListenIsMultipath(t *testing.T) bool {
	t.Helper()
	return accepted(t, net.Listen, multipath)
}

// TestNetListenIsMultipath shows what Listen guards against: where the kernel
// offers Multipath TCP, net.Listen takes it.
func TestNetListenIsMultipath(t *testing.T) {
	t.Setenv("GODEBUG", "")
	if !netListenIsMultipath(t) {
		t.Skip("net.Listen is not Multipath TCP here: no MPTCP in this kernel, or it is turned off")
	}
}

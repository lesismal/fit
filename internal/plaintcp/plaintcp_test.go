package plaintcp

import (
	"net"
	"testing"
)

// accepted listens with listen, dials it, and reports whether the connection
// it accepts uses Multipath TCP.
func accepted(t *testing.T, listen func(network, address string) (net.Listener, error)) bool {
	t.Helper()
	ln, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
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

func TestListenIsNotMultipath(t *testing.T) {
	if accepted(t, Listen) {
		t.Fatal("plaintcp.Listen accepted a Multipath TCP connection")
	}
}

// TestNetListenIsMultipath shows what Listen guards against: where the kernel
// offers Multipath TCP, net.Listen takes it.
func TestNetListenIsMultipath(t *testing.T) {
	t.Setenv("GODEBUG", "")
	if !accepted(t, net.Listen) {
		t.Skip("net.Listen is not Multipath TCP here: no MPTCP in this kernel, or it is turned off")
	}
}

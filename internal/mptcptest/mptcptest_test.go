package mptcptest

import (
	"net"
	"testing"
)

func TestSupportedAgreesWithNet(t *testing.T) {
	// The reference is net itself: a listener asked for MPTCP and a client
	// that dials it must agree, and Supported tells what that pair got.
	var lc net.ListenConfig
	lc.SetMultipathTCP(true)
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := Dial(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, want := Supported(t), InUse(s); got != want {
		t.Fatalf("Supported = %v, the reference listener's connection uses MPTCP = %v", got, want)
	}
}

func TestInUsePlain(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, err := ln.Accept()
		if err != nil {
			return
		}
		s.Close()
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	<-done
	if InUse(c) {
		t.Fatal("a plain TCP connection reports Multipath TCP")
	}
}

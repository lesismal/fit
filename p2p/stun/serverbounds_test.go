package stun_test

import (
	"net"
	"testing"
	"time"

	"github.com/lesismal/fit/p2p/stun"
)

// TestServerKnownAttrs checks a request with the attributes the server
// understands, though it does not check them, is answered.
func TestServerKnownAttrs(t *testing.T) {
	udp, _ := startServer(t, stun.ServerConfig{})
	conn, err := net.Dial("udp", udp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := bindingRequest(true, stun.AttrPriority, stun.AttrUseCandidate)
	checkMapped(t, exchange(t, conn, req.Raw, false), req, conn)
}

// TestServerTCPSmallest checks a server whose bound is below a header takes
// a header's worth, and that a header which arrives in two parts and
// announces more than that ends the connection.
func TestServerTCPSmallest(t *testing.T) {
	_, tcp := startServer(t, stun.ServerConfig{MaxTCPMessage: 4})
	conn, err := net.Dial("tcp", tcp)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := bindingRequest(false)
	if _, err := conn.Write(req.Raw[:10]); err != nil {
		t.Fatal(err)
	}
	// The rest comes later, as its own read on the server's side.
	time.Sleep(50 * time.Millisecond)
	if _, err := conn.Write(req.Raw[10:]); err != nil {
		t.Fatal(err)
	}
	checkMapped(t, read(t, conn, true), req, conn)

	big := bindingRequest(false, stun.AttrSoftware).Raw
	if _, err := conn.Write(big[:10]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if _, err := conn.Write(big[10:20]); err != nil {
		t.Fatal(err)
	}
	closed(t, conn)
}

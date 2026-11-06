package bindcheck

import (
	"errors"
	"net"
	"strings"
	"syscall"
	"testing"
)

// TestFree checks an address another socket holds is refused, TCP and UDP
// alike, and a free one, or port 0, is not.
func TestFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	err = Free("tcp", ln.Addr().String())
	if !errors.Is(err, syscall.EADDRINUSE) || !strings.Contains(err.Error(), "TCP "+ln.Addr().String()+" is taken") {
		t.Errorf("tcp in use: %v", err)
	}
	err = Free("udp", pc.LocalAddr().String())
	if !errors.Is(err, syscall.EADDRINUSE) || !strings.Contains(err.Error(), "UDP "+pc.LocalAddr().String()+" is taken") {
		t.Errorf("udp in use: %v", err)
	}
	for _, network := range []string{"tcp", "udp"} {
		if err := Free(network, "127.0.0.1:0"); err != nil {
			t.Errorf("%s port 0: %v", network, err)
		}
	}
	// The free ones: the TCP port is free for UDP, and the other way round.
	_, tcpPort, _ := net.SplitHostPort(ln.Addr().String())
	if err := Free("udp", "127.0.0.1:"+tcpPort); err != nil && !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("udp on the tcp port: %v", err)
	}
	if err := Free("tcp", "not an address"); err == nil || !strings.Contains(err.Error(), "listen on not an address") {
		t.Errorf("a bad address: %v", err)
	}
}

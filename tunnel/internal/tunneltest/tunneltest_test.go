package tunneltest

import (
	"io"
	"net"
	"testing"
	"time"
)

func TestEcho(t *testing.T) {
	for _, banner := range []string{"", "hi "} {
		c, err := net.Dial("tcp", Echo(t, banner))
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(banner)+4)
		if _, err := io.ReadFull(c, got); err != nil || string(got) != banner+"ping" {
			t.Fatalf("got %q, %v", got, err)
		}
		_ = c.Close()
	}
}

// Stall hands over the connections it accepts, and closes those the test
// left with it as the test ends.
func TestStall(t *testing.T) {
	var c net.Conn
	t.Run("stall", func(t *testing.T) {
		conns, addr := Stall(t)
		var err error
		if c, err = net.Dial("tcp", addr); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(10 * time.Second)
		for len(conns) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
	})
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Errorf("read from a connection Stall closed: %v", err)
	}
}

func TestEngine(t *testing.T) {
	if Engine(t) == nil {
		t.Fatal("no engine")
	}
}

package proto

import (
	"encoding/binary"
	"testing"
)

func TestNodeListOlder(t *testing.T) {
	cur := &NodeList{Epoch: 7, Version: 5}
	for _, c := range []struct {
		l     NodeList
		older bool
	}{
		{NodeList{Epoch: 7, Version: 4}, true},
		{NodeList{Epoch: 7, Version: 5}, false},
		{NodeList{Epoch: 7, Version: 6}, false},
		// A restarted hub counts again from 1.
		{NodeList{Epoch: 8, Version: 1}, false},
		// A hub that does not count.
		{NodeList{}, false},
	} {
		if got := c.l.Older(cur); got != c.older {
			t.Errorf("%+v: %v", c.l, got)
		}
	}
	if (&NodeList{Epoch: 7, Version: 1}).Older(nil) || (&NodeList{Epoch: 7, Version: 1}).Older(&NodeList{}) {
		t.Error("older than no list, or than an uncounted one")
	}
}

func TestMethodString(t *testing.T) {
	if s := MethodTunnelOpen.String(); s != "tunnel/open" {
		t.Fatal(s)
	}
	if s := Method(250).String(); s != "method 250" {
		t.Fatal(s)
	}
}

func TestFindService(t *testing.T) {
	info := NewInfo("n", TypeNode)
	info.Services = []Service{{Name: "a", Addr: "127.0.0.1:1"}}
	if s, ok := info.FindService("a"); !ok || s.Addr != "127.0.0.1:1" {
		t.Fatalf("%+v %v", s, ok)
	}
	if _, ok := info.FindService("b"); ok {
		t.Fatal("found a missing service")
	}
}

func TestFrames(t *testing.T) {
	id, payload, err := ParseFrame(append(binary.BigEndian.AppendUint64(nil, TunnelID(3, 9)), AckPayload(42)...))
	if err != nil || TunnelConn(id) != 3 || uint32(id) != 9 {
		t.Fatalf("%x, %v", id, err)
	}
	if n, err := ParseAck(payload); err != nil || n != 42 {
		t.Fatalf("%d, %v", n, err)
	}
	if _, _, err := ParseFrame(make([]byte, 7)); err != ErrShortFrame {
		t.Fatal(err)
	}
	if _, err := ParseAck(make([]byte, 7)); err == nil {
		t.Fatal("a short ack parsed")
	}
}

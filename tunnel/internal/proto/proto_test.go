package proto

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestVerifier(t *testing.T) {
	v := &Verifier{Password: "pw"}
	req := RegisterReq{Info: Info{Name: "lab", Type: TypeNode}}
	Sign(&req, "pw")
	if err := v.Verify(&req); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(&req); err == nil || !strings.Contains(err.Error(), "replayed") {
		t.Fatalf("a replay: %v", err)
	}
	other := RegisterReq{Info: Info{Name: "lab", Type: TypeNode}}
	Sign(&other, "nope")
	if err := v.Verify(&other); err == nil {
		t.Fatal("a wrong password was taken")
	}
	odd := RegisterReq{Info: Info{Name: "x", Type: "agent"}}
	Sign(&odd, "pw")
	if err := v.Verify(&odd); err == nil {
		t.Fatal("an unknown type was taken")
	}
}

// The nonces a Verifier keeps are bounded.
func TestVerifierBound(t *testing.T) {
	v := &Verifier{Password: "pw"}
	for range MaxNonces {
		req := RegisterReq{Info: Info{Name: "c", Type: TypeClient}}
		Sign(&req, "pw")
		if err := v.Verify(&req); err != nil {
			t.Fatal(err)
		}
	}
	req := RegisterReq{Info: Info{Name: "c", Type: TypeClient}}
	Sign(&req, "pw")
	if err := v.Verify(&req); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Fatalf("past the bound: %v", err)
	}
	if len(v.seen) > MaxNonces {
		t.Fatalf("kept %d nonces", len(v.seen))
	}
}

func TestNodeListOlder(t *testing.T) {
	cur := &NodeList{Epoch: 7, Version: 5}
	for _, c := range []struct {
		l     NodeList
		older bool
	}{
		{NodeList{Epoch: 7, Version: 4}, true},
		{NodeList{Epoch: 7, Version: 5}, false},
		{NodeList{Epoch: 7, Version: 6}, false},
		// A restarted server counts again from 1.
		{NodeList{Epoch: 8, Version: 1}, false},
		// A server that does not count.
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

func TestVerifierRefuses(t *testing.T) {
	v := &Verifier{Password: "pw"}
	noName := RegisterReq{Info: Info{Type: TypeNode}}
	Sign(&noName, "pw")
	if err := v.Verify(&noName); err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("no name: %v", err)
	}
	stale := RegisterReq{Info: Info{Name: "lab", Type: TypeNode}}
	Sign(&stale, "pw")
	stale.Time -= int64(2 * MaxClockSkew / time.Second)
	stale.MAC = mac(&stale, "pw")
	if err := v.Verify(&stale); err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("a stale time: %v", err)
	}
}

// A sweep forgets the nonces whose registrations can no longer pass.
func TestVerifierSweep(t *testing.T) {
	v := &Verifier{Password: "pw", seen: map[string]time.Time{"old": time.Now().Add(-3 * MaxClockSkew)}}
	req := RegisterReq{Info: Info{Name: "c", Type: TypeClient}}
	Sign(&req, "pw")
	if err := v.Verify(&req); err != nil {
		t.Fatal(err)
	}
	if _, ok := v.seen["old"]; ok || len(v.seen) != 1 {
		t.Fatalf("seen after a sweep: %v", v.seen)
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

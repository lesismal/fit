package proto

import (
	"encoding/binary"
	"errors"
	"runtime/debug"
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
		req := RegisterReq{Info: Info{Name: "c", Type: TypeProxy}}
		Sign(&req, "pw")
		if err := v.Verify(&req); err != nil {
			t.Fatal(err)
		}
	}
	req := RegisterReq{Info: Info{Name: "c", Type: TypeProxy}}
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
	req := RegisterReq{Info: Info{Name: "c", Type: TypeProxy}}
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

// A hub's proof is taken for the registration it answers, under the
// password, and for nothing else: not without the password, not the
// registration's own MAC sent back, not another registration's proof, not
// an answer changed.
func TestHubProof(t *testing.T) {
	req := RegisterReq{Info: Info{Name: "lab", Type: TypeNode}}
	Sign(&req, "pw")
	rsp := RegisterRsp{Peer: 1, Conn: 2, Index: 3, Hub: "hub"}
	if err := CheckProof(&req, &rsp, "pw"); !errors.Is(err, ErrOldHub) || !strings.Contains(err.Error(), "upgrade") || !strings.Contains(err.Error(), Version) {
		t.Errorf("no proof: %v", err)
	}
	Prove(&req, &rsp, "pw")
	if err := CheckProof(&req, &rsp, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := CheckProof(&req, &rsp, "other"); !errors.Is(err, ErrHubProof) {
		t.Errorf("another password: %v", err)
	}

	wrong := rsp
	Prove(&req, &wrong, "guess")
	if err := CheckProof(&req, &wrong, "pw"); !errors.Is(err, ErrHubProof) {
		t.Errorf("a hub without the password: %v", err)
	}
	reflected := rsp
	reflected.Proof = req.MAC
	if err := CheckProof(&req, &reflected, "pw"); !errors.Is(err, ErrHubProof) {
		t.Errorf("the registration's MAC sent back: %v", err)
	}
	// Nor is a proof a registration's MAC, for the hub to take.
	as := req
	as.MAC = rsp.Proof
	if err := (&Verifier{Password: "pw"}).Verify(&as); err == nil {
		t.Error("a proof was taken as a registration's MAC")
	}

	// Another link's registration, with a nonce of its own.
	other := RegisterReq{Info: req.Info}
	Sign(&other, "pw")
	if err := CheckProof(&other, &rsp, "pw"); !errors.Is(err, ErrHubProof) {
		t.Errorf("a proof replayed onto another registration: %v", err)
	}
	for _, change := range []func(*RegisterRsp){
		func(r *RegisterRsp) { r.Peer++ },
		func(r *RegisterRsp) { r.Conn++ },
		func(r *RegisterRsp) { r.Index++ },
		func(r *RegisterRsp) { r.Hub = "hub2" },
	} {
		changed := rsp
		change(&changed)
		if err := CheckProof(&req, &changed, "pw"); !errors.Is(err, ErrHubProof) {
			t.Errorf("a changed answer %+v: %v", changed, err)
		}
	}
}

// TestVersion: a peer reports fit's version from fit's own build, or from a
// build that has fit as a dependency, and plain "fit" from one that records
// neither.
func TestVersion(t *testing.T) {
	fit := "github.com/lesismal/fit"
	for _, tc := range []struct {
		name string
		info *debug.BuildInfo
		ok   bool
		want string
	}{
		{"fit itself", &debug.BuildInfo{Main: debug.Module{Path: fit, Version: "v1.7.1"}}, true, "fit v1.7.1"},
		{"fit as a dependency", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"},
			Deps: []*debug.Module{{Path: "example.com/other", Version: "v0.1.0"}, {Path: fit, Version: "v1.7.0"}}}, true, "fit v1.7.0"},
		{"fit without a version", &debug.BuildInfo{Main: debug.Module{Path: fit}}, true, "fit"},
		{"no fit", &debug.BuildInfo{Main: debug.Module{Path: "example.com/app"}}, true, "fit"},
		{"no build info", nil, false, "fit"},
	} {
		if got := version(tc.info, tc.ok); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

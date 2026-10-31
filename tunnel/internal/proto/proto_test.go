package proto

import (
	"strings"
	"testing"
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
		// fibhub's server does not count.
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

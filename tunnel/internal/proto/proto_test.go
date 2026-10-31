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

package auth

import (
	"bytes"
	"testing"
)

func init() { Iterations = 1000 }

func hello() (*Hello, *HelloRsp) {
	return &Hello{Version: Version, Type: "node", Name: "lab", Nonce: Random(NonceLen)},
		&HelloRsp{Version: Version, Nonce: Random(NonceLen), Salt: Random(SaltLen)}
}

// Both sides of a handshake with the same passwords agree on its MACs and
// keys; any other password, salt, name or random bytes do not.
func TestHandshake(t *testing.T) {
	if _, err := Derive("", Random(SaltLen)); err == nil {
		t.Fatal("an empty password")
	}
	h, r := hello()
	key, _ := Derive("pw", r.Salt)
	body, _ := Derive("body", r.Salt)
	peer, err := NewHandshake(h, r, key, body)
	if err != nil {
		t.Fatal(err)
	}
	hub, _ := NewHandshake(h, r, key, body)
	if !Equal(peer.PeerMAC(), hub.PeerMAC()) || !Equal(peer.HubProof(), hub.HubProof()) || Equal(peer.PeerMAC(), peer.HubProof()) {
		t.Fatal("the MACs")
	}
	ps, _ := peer.Session(false)
	hs, _ := hub.Session(true)
	sealed, _ := ps.Send.SealInPlace(append(make([]byte, 0, 32), "x"...), nil)
	if got, err := hs.Recv.OpenInPlace(sealed, nil); err != nil || string(got) != "x" {
		t.Fatalf("%q, %v", got, err)
	}

	other := func(what string, h *Hello, r *HelloRsp, key, body Key) {
		t.Helper()
		o, err := NewHandshake(h, r, key, body)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if Equal(o.PeerMAC(), peer.PeerMAC()) || Equal(o.HubProof(), peer.HubProof()) {
			t.Fatalf("%s: the same MACs", what)
		}
	}
	wrong, _ := Derive("nope", r.Salt)
	other("another password", h, r, wrong, body)
	other("no body password", h, r, key, nil)
	h2 := *h
	h2.Type = "proxy"
	other("another type", &h2, r, key, body)
	h2 = *h
	h2.Name = "lab2"
	other("another name", &h2, r, key, body)
	h2 = *h
	h2.Nonce = Random(NonceLen)
	other("other random bytes", &h2, r, key, body)
	r2 := *r
	r2.Salt = Random(SaltLen)
	other("another salt", h, &r2, key, body)
	if k2, _ := Derive("pw", r2.Salt); bytes.Equal(k2, key) {
		t.Fatal("a salt changes nothing")
	}

	for what, f := range map[string]func(*Hello, *HelloRsp){
		"version":     func(h *Hello, _ *HelloRsp) { h.Version = 1 },
		"hub version": func(_ *Hello, r *HelloRsp) { r.Version = 3 },
		"short nonce": func(h *Hello, _ *HelloRsp) { h.Nonce = h.Nonce[:8] },
		"short salt":  func(_ *Hello, r *HelloRsp) { r.Salt = nil },
	} {
		h, r := hello()
		f(h, r)
		if _, err := NewHandshake(h, r, key, nil); err == nil {
			t.Errorf("%s: taken", what)
		}
	}
	if _, err := NewHandshake(h, r, nil, nil); err == nil {
		t.Error("no key")
	}
}

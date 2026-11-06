package linktoken

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

func transcript() Transcript {
	return Transcript{Label: "test", Token: "0123456789abcdef", Dialer: bytes.Repeat([]byte{1}, NonceSize),
		Listener: bytes.Repeat([]byte{2}, NonceSize), Extra: []string{"node", "lab"}}
}

func TestSecretRoundTrip(t *testing.T) {
	s := New()
	if !config.ValidLinkTokenID(s.ID) {
		t.Fatalf("id %q", s.ID)
	}
	p, err := Parse(s.String())
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != s.ID || !bytes.Equal(p.secret, s.secret) || p.Hash() != s.Hash() {
		t.Fatal("parse does not round-trip")
	}
	if _, err := StoredOf(s.Hash()); err != nil {
		t.Fatal(err)
	}
	if _, err := StoredOf("zz"); err == nil {
		t.Fatal("bad hash taken")
	}
	for _, bad := range []string{"", "fitt1.", "fitt1.0123456789abcdef", "fitt1.0123456789ABCDEF.aaaaaaaaaaaaaaaaaaaaaaaa",
		"fitt2.0123456789abcdef.aaaaaaaaaaaaaaaaaaaaaaaa", "fitt1.0123456789abcdef.aaaa", "fitt1.0123456789abcdef.aaaaaaaaaaaaaaaaaaaaaaa!",
		"fitt1.0123456789abcdef.aaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) took it", bad)
		}
	}
}

func TestHandshake(t *testing.T) {
	s := New()
	stored := s.Stored()
	tr := transcript()
	proof := s.Prove(tr)
	if err := Verify(stored, tr, proof); err != nil {
		t.Fatal(err)
	}
	// What the listener keeps does not prove the token.
	forged := mac(stored, tr.bytes(labelDialer))
	if Verify(stored, tr, forged) == nil {
		t.Fatal("the stored key alone proved the token")
	}
	// Another listener nonce, dialer nonce, label, token or extra: replayed.
	for _, change := range []func(*Transcript){
		func(t *Transcript) { t.Listener = bytes.Repeat([]byte{3}, NonceSize) },
		func(t *Transcript) { t.Dialer = bytes.Repeat([]byte{3}, NonceSize) },
		func(t *Transcript) { t.Label = "other" },
		func(t *Transcript) { t.Token = "fedcba9876543210" },
		func(t *Transcript) { t.Extra = []string{"proxy", "lab"} },
	} {
		other := transcript()
		change(&other)
		if Verify(stored, other, proof) == nil {
			t.Fatal("a proof checked over another transcript")
		}
	}
	if Verify(stored, tr, proof[:10]) == nil || Verify(stored[:10], tr, proof) == nil {
		t.Fatal("short input checked")
	}
	if Verify(New().Stored(), tr, proof) == nil {
		t.Fatal("another token's stored key checked")
	}
	lp := ListenerProof(stored, tr, "answer")
	if err := s.VerifyListener(tr, lp, "answer"); err != nil {
		t.Fatal(err)
	}
	if s.VerifyListener(tr, lp, "other") == nil {
		t.Fatal("listener proof over another answer checked")
	}
	// Reflection: neither proof checks as the other.
	if s.VerifyListener(tr, proof) == nil || Verify(stored, tr, ListenerProof(stored, tr)) == nil {
		t.Fatal("a proof checked the other way")
	}
	if New().VerifyListener(tr, lp, "answer") == nil {
		t.Fatal("another token checked the listener")
	}
	if len(BodyKey(stored)) != 32 || bytes.Equal(BodyKey(stored), BodyKey(New().Stored())) {
		t.Fatal("body key")
	}
	if len(Nonce()) != NonceSize || bytes.Equal(Nonce(), Nonce()) {
		t.Fatal("nonce")
	}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func rec(name string, exp int64, members ...string) (Secret, config.LinkToken) {
	s := New()
	return s, config.LinkToken{ID: s.ID, Name: name, Hash: s.Hash(), CreatedAt: 1, ExpiresAt: exp, Members: members}
}

func TestRegistry(t *testing.T) {
	c := &clock{t: time.Unix(1000, 0)}
	var binds []string
	r := &Registry{Now: c.now, OnBind: func(id, m string) { binds = append(binds, id+" "+m) }}
	defer r.Stop()
	a, ra := rec("a", 0, "node/old")
	b, rb := rec("b", 2000)
	_, rx := rec("x", 0)
	rx.Hash = "bad"
	r.Set([]config.LinkToken{ra, rb, rx})
	if r.Len() != 2 {
		t.Fatalf("len %d", r.Len())
	}
	if st, err := r.Lookup(a.ID); err != nil || !bytes.Equal(st, a.Stored()) {
		t.Fatal(err)
	}
	if _, err := r.Lookup("nope"); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	var closedA, closedB atomic.Int32
	ua, err := r.Admit(a.ID, "node/lab", "1.2.3.4:5", func() { closedA.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Admit(a.ID, "node/lab", "1.2.3.4:6", func() { closedA.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if len(binds) != 1 || binds[0] != a.ID+" node/lab" {
		t.Fatalf("binds %v", binds)
	}
	// Another token cannot take a bound member.
	if _, err := r.Admit(b.ID, "node/lab", "x", nil); !errors.Is(err, ErrBound) {
		t.Fatal(err)
	}
	if _, err := r.Admit(b.ID, "node/old", "x", nil); !errors.Is(err, ErrBound) {
		t.Fatal(err)
	}
	if _, err := r.Admit("nope", "m", "x", nil); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
	if _, err := r.Admit(b.ID, "proxy/p", "y", func() { closedB.Add(1) }); err != nil {
		t.Fatal(err)
	}
	v, ok := r.Get(a.ID)
	if !ok || len(v.Links) != 2 || len(v.Members) != 2 || v.LastUsed != 1000 || v.LastAddr != "1.2.3.4:6" {
		t.Fatalf("view %+v", v)
	}
	if _, ok := r.Get("nope"); ok {
		t.Fatal("got nope")
	}
	list := r.List()
	if len(list) != 2 || list[0].Name != "a" {
		t.Fatalf("list %+v", list)
	}
	ua.Release()
	ua.Release()
	(*Use)(nil).Release()
	if v, _ := r.Get(a.ID); len(v.Links) != 1 {
		t.Fatal("release")
	}
	// A set that keeps the tokens keeps the links and the runtime binding.
	r.Set([]config.LinkToken{ra, rb})
	if v, _ := r.Get(a.ID); len(v.Links) != 1 || len(v.Members) != 2 {
		t.Fatalf("kept %+v", v)
	}
	// Expiry: the timer closes b's link.
	c.add(time.Second * 1000)
	r.sweep()
	if closedB.Load() != 1 {
		t.Fatal("expired token's link not closed")
	}
	if _, err := r.Lookup(b.ID); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	if _, err := r.Admit(b.ID, "proxy/p", "y", nil); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	// Revoking a closes its link and frees its members.
	r.Set([]config.LinkToken{rb})
	if closedA.Load() != 1 {
		t.Fatalf("revoked token's links closed %d", closedA.Load())
	}
	rb.ExpiresAt = 0
	r.Set([]config.LinkToken{rb})
	if _, err := r.Admit(b.ID, "node/lab", "z", nil); err != nil {
		t.Fatal("a freed member did not bind:", err)
	}
	// A changed hash closes the links.
	closed := 0
	_, err = r.Admit(b.ID, "node/lab", "z", func() { closed++ })
	if err != nil {
		t.Fatal(err)
	}
	rb.Hash = New().Hash()
	r.Set([]config.LinkToken{rb})
	if closed != 1 {
		t.Fatal("changed hash kept the link")
	}
	// An expiry set into the past closes at once.
	_, err = r.Admit(b.ID, "node/lab", "z", func() { closed++ })
	if err != nil {
		t.Fatal(err)
	}
	rb.ExpiresAt = 10
	r.Set([]config.LinkToken{rb})
	if closed != 2 {
		t.Fatal("expired by set kept the link")
	}
}

// Tokens of one name list by ID.
func TestRegistryListOrder(t *testing.T) {
	r := &Registry{}
	defer r.Stop()
	_, a := rec("same", 0)
	_, b := rec("same", 0)
	r.Set([]config.LinkToken{a, b})
	l := r.List()
	if len(l) != 2 || l[0].ID > l[1].ID {
		t.Fatalf("list %+v", l)
	}
}

func TestRegistryFull(t *testing.T) {
	r := &Registry{}
	defer r.Stop()
	s, rc := rec("a", 0)
	r.Set([]config.LinkToken{rc})
	for i := range config.MaxLinkTokenMembers {
		if _, err := r.Admit(s.ID, "m"+strings.Repeat("x", i), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.Admit(s.ID, "one-more", "", nil); !errors.Is(err, ErrFull) {
		t.Fatal(err)
	}
}

func TestRegistryTimer(t *testing.T) {
	r := &Registry{}
	s, rc := rec("a", time.Now().Add(1100*time.Millisecond).Unix())
	r.Set([]config.LinkToken{rc})
	done := make(chan struct{})
	if _, err := r.Admit(s.ID, "m", "", func() { close(done) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the timer did not close the link")
	}
	r.Stop()
	r.Set([]config.LinkToken{rc}) // scheduling after Stop does nothing
	r.Stop()
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"30d": 30 * 24 * time.Hour, "1h": time.Hour} {
		if d, err := ParseTTL(in); err != nil || d != want {
			t.Errorf("%s: %v %v", in, d, err)
		}
	}
	for _, in := range []string{"", "0s", "-1h", "xd", "1y"} {
		if _, err := ParseTTL(in); err == nil {
			t.Errorf("%q taken", in)
		}
	}
}

// memEditor is an Editor over tokens held in memory, applied to a registry
// as a config would be.
type memEditor struct {
	mu     sync.Mutex
	tokens []config.LinkToken
	reg    *Registry
	fail   error
}

func (e *memEditor) edit(_ context.Context, change func([]config.LinkToken) ([]config.LinkToken, error)) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.fail != nil {
		return e.fail
	}
	ts, err := change(append([]config.LinkToken(nil), e.tokens...))
	if err != nil {
		return err
	}
	e.tokens = ts
	e.reg.Set(ts)
	return nil
}

func TestManager(t *testing.T) {
	c := &clock{t: time.Unix(10_000, 0)}
	reg := &Registry{Now: c.now}
	defer reg.Stop()
	ed := &memEditor{reg: reg}
	var out bytes.Buffer
	root, err := logs.New(nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Role: "tunnel.hub", Reg: reg, Edit: ed.edit, Log: root.Logger(config.LoggerAdmin)}
	defer func() {
		if !strings.Contains(out.String(), "revoked link token") {
			t.Errorf("the admin log lacks the revocation: %s", out.String())
		}
	}()
	ctx := context.Background()
	if bad("x %d", 1).Error() != "x 1" {
		t.Fatal("bad request text")
	}
	for _, req := range []IssueReq{{Name: ""}, {Name: strings.Repeat("n", 65)}, {Name: "a\x01"}, {Name: "a", Note: strings.Repeat("n", 1025)},
		{Name: "a", ExpiresAt: 5}, {Name: "a", ExpiresAt: -1}, {Name: "a", ExpiresAt: 20_000, TTL: "1h"}, {Name: "a", TTL: "nope"}} {
		if _, err := m.Issue(ctx, req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%+v: %v", req, err)
		}
	}
	is, err := m.Issue(ctx, IssueReq{Name: " lab ", Note: "the lab", TTL: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	if is.Name != "lab" || is.ExpiresAt != 10_000+3600 || !strings.HasPrefix(is.Token, config.LinkTokenPrefix) {
		t.Fatalf("issued %+v", is)
	}
	sec, err := Parse(is.Token)
	if err != nil || ed.tokens[0].Hash != sec.Hash() {
		t.Fatal("the config does not keep the token's hash")
	}
	if len(m.List()) != 1 {
		t.Fatal("list")
	}
	v, err := m.SetExpiry(ctx, is.ID, ExpiryReq{ExpiresAt: 50_000})
	if err != nil || v.ExpiresAt != 50_000 {
		t.Fatal(v, err)
	}
	if v, err = m.SetExpiry(ctx, is.ID, ExpiryReq{}); err != nil || v.ExpiresAt != 0 {
		t.Fatal(v, err)
	}
	if _, err := m.SetExpiry(ctx, "nope", ExpiryReq{}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := m.SetExpiry(ctx, is.ID, ExpiryReq{TTL: "bad"}); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	if err := m.Revoke(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := m.Revoke(ctx, is.ID); err != nil || len(ed.tokens) != 0 {
		t.Fatal(err)
	}
	ed.fail = errors.New("boom")
	if _, err := m.Issue(ctx, IssueReq{Name: "a"}); err == nil {
		t.Fatal("edit failure")
	}
	ed.fail = nil
	ed.tokens = make([]config.LinkToken, config.MaxLinkTokens)
	if _, err := m.Issue(ctx, IssueReq{Name: "a"}); !errors.Is(err, ErrBadRequest) {
		t.Fatal(err)
	}
	if expiresText(0) != "never expiring" {
		t.Fatal(expiresText(0))
	}
	// Issue when the registry has not taken the edit yet still answers.
	m2 := &Manager{Reg: &Registry{}, Edit: func(context.Context, func([]config.LinkToken) ([]config.LinkToken, error)) error { return nil }}
	if is, err := m2.Issue(ctx, IssueReq{Name: "x"}); err != nil || is.Name != "x" {
		t.Fatal(is, err)
	}
}

func TestBind(t *testing.T) {
	ts := []config.LinkToken{{ID: "a"}}
	ts = Bind(ts, "a", "m")
	ts = Bind(ts, "a", "m")
	ts = Bind(ts, "b", "m")
	if len(ts[0].Members) != 1 {
		t.Fatal(ts)
	}
	ts[0].Members = make([]string, config.MaxLinkTokenMembers)
	if ts = Bind(ts, "a", "n"); len(ts[0].Members) != config.MaxLinkTokenMembers {
		t.Fatal("bound past the bound")
	}
}

func TestStringAs(t *testing.T) {
	s := New()
	got, err := ParseAs(s.StringAs("fite1."), "fite1.")
	if err != nil || got.ID != s.ID || got.Hash() != s.Hash() {
		t.Fatal(got, err)
	}
	for _, bad := range []string{s.String(), "fite1." + s.ID, "fite1.XYZ.abc", "fite1." + s.ID + ".!!", "fite1." + s.ID + ".AAAA"} {
		if _, err := ParseAs(bad, "fite1."); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

package hub

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
	"github.com/lesismal/fit/tunnel/internal/bodycrypt"
	"github.com/lesismal/fit/tunnel/internal/link"
	"github.com/lesismal/fit/tunnel/internal/proto"
	"github.com/lesismal/fit/tunnel/internal/transport"
)

// tokenJSON is a token of a hub's member, for sec.
func tokenJSON(sec linktoken.Secret, name string, extra string) string {
	return fmt.Sprintf(`{"id": %q, "name": %q, "hash": %q%s}`, sec.ID, name, sec.Hash(), extra)
}

// challenge asks the hub for a challenge with token id, and returns both
// nonces.
func (p *peer) challenge(id string) ([]byte, []byte, error) {
	own := linktoken.Nonce()
	var ch proto.ChallengeRsp
	if err := p.call(proto.MethodChallenge, &proto.ChallengeReq{Token: id, Nonce: hex.EncodeToString(own)}, &ch); err != nil {
		return nil, nil, err
	}
	hub, err := proto.DecodeNonce(ch.Nonce)
	return own, hub, err
}

// registerToken registers p as typ name with sec, and checks the hub's
// proof of the token.
func (p *peer) registerToken(typ, name string, sec linktoken.Secret) error {
	own, hub, err := p.challenge(sec.ID)
	if err != nil {
		return err
	}
	req := proto.RegisterReq{Info: proto.NewInfo(name, typ)}
	proto.ProveToken(&req, sec, own, hub)
	if err := p.l.Call(proto.MethodRegister, &req, &p.rsp, 5*time.Second); err != nil {
		return err
	}
	return proto.CheckTokenProof(&req, &p.rsp, sec, own, hub)
}

// A token registers any role, binds the member it first registers, and is
// refused when unknown, forged, replayed or used without its challenge.
func TestTokenRegister(t *testing.T) {
	a, b := linktoken.New(), linktoken.New()
	v := newHub(t, `{"name": "hub", "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}], "tokens": [`+
		tokenJSON(a, "a", "")+`, `+tokenJSON(b, "b", "")+`]}`)
	addr := v.listen()
	n := v.dial(addr)
	if err := n.registerToken(proto.TypeNode, "lab", a); err != nil {
		t.Fatal(err)
	}
	// Not bound to a role: the same token registers a proxy.
	if err := v.dial(addr).registerToken(proto.TypeProxy, "me", a); err != nil {
		t.Fatal(err)
	}
	// Another link of the node, with its token.
	if err := v.dial(addr).registerToken(proto.TypeNode, "lab", a); err != nil {
		t.Fatal(err)
	}
	// The binding is written into the member.
	waitFor(t, "the binding in the config", func() bool { return strings.Contains(v.current(), `"node/lab"`) })
	// Another token cannot take the node over.
	if err := v.dial(addr).registerToken(proto.TypeNode, "lab", b); err == nil || !strings.Contains(err.Error(), "bound to another token") {
		t.Fatalf("took over: %v", err)
	}
	// An unknown token, and a forged proof of a known one.
	if err := v.dial(addr).registerToken(proto.TypeNode, "x", linktoken.New()); err == nil || !strings.Contains(err.Error(), "no such token") {
		t.Fatalf("unknown token: %v", err)
	}
	forged, _ := linktoken.Parse(linktoken.New().String())
	forged.ID = a.ID
	if err := v.dial(addr).registerToken(proto.TypeNode, "y", forged); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("forged: %v", err)
	}
	// A registration replayed on another link, after its own challenge,
	// does not check: the hub's nonce differs.
	p1 := v.dial(addr)
	own, hub, err := p1.challenge(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := proto.RegisterReq{Info: proto.NewInfo("z", proto.TypeProxy)}
	proto.ProveToken(&req, a, own, hub)
	p2 := v.dial(addr)
	if _, _, err := p2.challenge(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := p2.call(proto.MethodRegister, &req, nil); err == nil || !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("replayed: %v", err)
	}
	// A challenge proves one registration: once spent, the same one is
	// refused on its own link too.
	if err := p1.call(proto.MethodRegister, &req, nil); err != nil {
		t.Fatal(err)
	}
	p3 := v.dial(addr)
	if err := p3.call(proto.MethodRegister, &req, nil); err == nil || !strings.Contains(err.Error(), "follows its challenge") {
		t.Fatalf("without a challenge: %v", err)
	}
	if _, _, err := p3.challenge(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p3.challenge(a.ID); err == nil || !strings.Contains(err.Error(), "one challenge") {
		t.Fatalf("a second challenge: %v", err)
	}
	if err := p3.call(proto.MethodChallenge, &proto.ChallengeReq{Token: a.ID, Nonce: "zz"}, nil); err == nil {
		t.Fatal("a bad nonce taken")
	}
	if err := p3.call(proto.MethodChallenge, "not json", nil); err == nil {
		t.Fatal("a bad challenge taken")
	}
	// A password registration at a hub without a password.
	if err := v.dial(addr).register(proto.TypeNode, "w", "pw"); err == nil || !strings.Contains(err.Error(), "tokens only") {
		t.Fatalf("password: %v", err)
	}

	st := v.s.Status()
	if len(st.PasswordPeers) != 0 {
		t.Fatalf("password peers %v", st.PasswordPeers)
	}
	for _, p := range st.Peers {
		if len(p.Tokens) != 1 || p.Tokens[0] != a.ID || p.Password {
			t.Fatalf("peer %+v", p)
		}
	}
	views := v.s.LinkTokens().List()
	if len(views) != 2 || views[0].ID != a.ID || len(views[0].Links) != 4 || views[0].LastUsed == 0 {
		t.Fatalf("tokens %+v", views)
	}

	// Revoking a closes its links at once, and frees the node.
	var m map[string]any
	_ = json.Unmarshal([]byte(v.current()), &m)
	ts := m["tokens"].([]any)
	if mem := ts[0].(map[string]any)["members"].([]any); len(mem) != 3 {
		t.Fatalf("members %v", mem)
	}
	m["tokens"] = ts[1:]
	data, _ := json.Marshal(m)
	if err := v.apply(string(data)); err != nil {
		t.Fatal(err)
	}
	n.waitClosed()
	if err := v.dial(addr).registerToken(proto.TypeNode, "lab", b); err != nil {
		t.Fatalf("the freed node: %v", err)
	}
}

// A token that expires is refused, and the links it holds close.
func TestTokenExpires(t *testing.T) {
	a := linktoken.New()
	exp := time.Now().Add(1500 * time.Millisecond).Unix()
	v := newHub(t, fmt.Sprintf(`{"listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}], "tokens": [%s]}`,
		tokenJSON(a, "a", fmt.Sprintf(`, "expires_at": %d`, exp))))
	n := v.dial(v.listen())
	if err := n.registerToken(proto.TypeNode, "lab", a); err != nil {
		t.Fatal(err)
	}
	n.waitClosed()
	if err := v.dial(v.listen()).registerToken(proto.TypeNode, "lab", a); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token: %v", err)
	}
}

// A hub takes the shared password beside tokens, and lists who uses it.
func TestTokenAndPassword(t *testing.T) {
	a := linktoken.New()
	v := newHub(t, tcpMember(`, "tokens": [`+tokenJSON(a, "a", "")+`]`))
	v.dial(v.listen()).mustRegister(proto.TypeNode, "old")
	if err := v.dial(v.listen()).registerToken(proto.TypeProxy, "new", a); err != nil {
		t.Fatal(err)
	}
	st := v.s.Status()
	if len(st.PasswordPeers) != 1 || st.PasswordPeers[0] != "node/old" {
		t.Fatalf("password peers %v", st.PasswordPeers)
	}
}

// With a body password, a token's link seals with the token's keys, which
// its hello names; the registration must be with the same token.
func TestTokenSealed(t *testing.T) {
	a, b := linktoken.New(), linktoken.New()
	v := newHub(t, `{"password": "pw", "body_password": "bp", "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}], "tokens": [`+
		tokenJSON(a, "a", "")+`, `+tokenJSON(b, "b", "")+`]}`)
	dial := func(sec *linktoken.Secret, body string) *peer {
		var cd *bodycrypt.Coder
		if sec != nil {
			cd = bodycrypt.NewToken(sec.ID, linktoken.BodyKey(sec.Stored()), body).Dialer()
		} else {
			pc, _ := bodycrypt.New(body)
			cd = pc.Dialer()
		}
		p := &peer{t: t, notes: make(chan note, 16)}
		if _, err := transport.Dial(v.e, v.listen(), 5*time.Second, func(c transport.Conn) transport.Handler {
			p.l = link.New(c, link.NewRouter(), cd)
			return p.l
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = p.l.Close() })
		return p
	}
	if err := dial(&a, "bp").registerToken(proto.TypeNode, "lab", a); err != nil {
		t.Fatal(err)
	}
	// The hello names a, the registration b.
	if err := dial(&a, "bp").registerToken(proto.TypeNode, "lab2", b); err == nil || !strings.Contains(err.Error(), "another token") {
		t.Fatalf("crossed tokens: %v", err)
	}
	// The hello names a token, the registration the password.
	if err := dial(&a, "bp").register(proto.TypeNode, "lab3", "pw"); err == nil || !strings.Contains(err.Error(), "named a token") {
		t.Fatalf("token hello, password registration: %v", err)
	}
	// Another body password: the first sealed frame does not open.
	p := dial(&a, "other")
	if err := p.registerToken(proto.TypeNode, "lab4", a); err == nil {
		t.Fatal("registered under another body password")
	}
	// An unknown token's hello closes the link.
	u := linktoken.New()
	p = dial(&u, "bp")
	if err := p.registerToken(proto.TypeNode, "lab5", u); err == nil {
		t.Fatal("registered with an unknown token's hello")
	}
	// The password's links still seal under the password.
	if err := dial(nil, "bp").register(proto.TypeNode, "lab6", "pw"); err != nil {
		t.Fatal(err)
	}
}

// The pages issue, list, change and revoke tokens.
func TestTokenPages(t *testing.T) {
	v := newHub(t, tcpMember(`, "console": `+consoleJSON("127.0.0.1:0")))
	pg := login(t, v.s.console.Addr())
	for _, path := range []string{"/api/tokens", "/api/tokens/expiry", "/api/tokens/revoke"} {
		pg.expect(400, path, "not json")
	}
	pg.expect(400, "/api/tokens", map[string]any{"name": ""})
	var is linktoken.Issued
	if err := json.Unmarshal([]byte(pg.expect(200, "/api/tokens", map[string]any{"name": "lab", "ttl": "30d", "note": "n"})), &is); err != nil {
		t.Fatal(err)
	}
	sec, err := linktoken.Parse(is.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.current(), sec.Hash()) || strings.Contains(v.current(), is.Token) {
		t.Fatalf("the member keeps %s", v.current())
	}
	var list struct {
		Tokens []linktoken.View `json:"tokens"`
	}
	if code, s := pg.do("GET", "/api/tokens", nil, &list); code != 200 || len(list.Tokens) != 1 || list.Tokens[0].Name != "lab" {
		t.Fatalf("list %d %s", code, s)
	}
	n := v.dial(v.listen())
	if err := n.registerToken(proto.TypeNode, "lab", sec); err != nil {
		t.Fatal(err)
	}
	pg.expect(404, "/api/tokens/expiry", map[string]any{"id": "0000000000000000"})
	pg.expect(400, "/api/tokens/expiry", map[string]any{"id": sec.ID, "ttl": "soon"})
	pg.expect(200, "/api/tokens/expiry", map[string]any{"id": sec.ID, "ttl": "1h"})
	if c, _ := load(v.current()); c.Tokens[0].ExpiresAt < time.Now().Add(59*time.Minute).Unix() {
		t.Fatalf("expiry %+v", c.Tokens[0])
	}
	pg.expect(404, "/api/tokens/revoke", map[string]any{"id": "0000000000000000"})
	pg.expect(200, "/api/tokens/revoke", map[string]any{"id": sec.ID})
	n.waitClosed()
	if c, _ := load(v.current()); len(c.Tokens) != 0 {
		t.Fatalf("tokens %+v", c.Tokens)
	}
	// The password cleared, once every member has a token.
	pg.expect(200, "/api/settings", map[string]any{"clear_password": true})
	if v.s.settings().cfg.Password != "" {
		t.Fatal("the password stayed")
	}
}

var _ = config.LinkToken{}

package mcp

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRefused: a request without a token, with a wrong, revoked or expired
// one, from an address the settings or the token do not allow, of a user
// who is no longer one or whose assistant is off, is refused; and the
// server answers nothing while admin.assistant.mcp is not set.
func TestRefused(t *testing.T) {
	var gone atomic.Bool
	st := &memStore{}
	f := start(t, settings(`{}`, ""), st, func(o *Options) {
		o.HasUser = func(u string) bool { return !(gone.Load() && u == "ada") }
	})
	tok := f.token("t", "read")
	ok := func() *answer { return f.modern(tok, "tools/list", nil) }
	if a := ok(); a.status != 200 {
		t.Fatalf("a good token: %d %s", a.status, a.body)
	}
	for name, raw := range map[string]string{"none": "", "garbage": "abc", "no secret": "fitmcp_abc", "wrong secret": tok[:len(tok)-2] + "xx",
		"unknown": "fitmcp_0011223344556677_" + strings.Repeat("a", 43)} {
		a := f.modern(raw, "tools/list", nil)
		if a.status != 401 || a.header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d %s", name, a.status, a.body)
		}
		if raw == tok[:len(tok)-2]+"xx" && !strings.Contains(a.body, "not valid") {
			t.Errorf("a wrong secret says: %s", a.body)
		}
	}

	gone.Store(true)
	if a := ok(); a.status != 401 || !strings.Contains(a.body, "no longer a console user") {
		t.Errorf("a user gone: %d %s", a.status, a.body)
	}
	gone.Store(false)

	f.s.skew.Store(int64(31 * 24 * time.Hour))
	if a := ok(); a.status != 401 || !strings.Contains(a.body, "expired") {
		t.Errorf("expired: %d %s", a.status, a.body)
	}
	f.s.skew.Store(0)

	// A token for another address.
	far := f.tokenAs(f.c, f.csrf, `{"name":"far","days":1,"allow":["10.1.2.3","192.168.0.0/16"]}`)
	if a := f.modern(far, "tools/list", nil); a.status != 403 {
		t.Errorf("a token off its addresses: %d %s", a.status, a.body)
	}
	near := f.tokenAs(f.c, f.csrf, `{"name":"near","days":1,"allow":["127.0.0.0/8","::1"]}`)
	if a := f.modern(near, "tools/list", nil); a.status != 200 {
		t.Errorf("a token on its addresses: %d %s", a.status, a.body)
	}

	// Revoked.
	_, body := f.do("GET", "/api/m/ai/mcp", "")
	var v mcpView
	_ = json.Unmarshal([]byte(body), &v)
	id := ""
	for _, tv := range v.Tokens {
		if tv.Name == "t" {
			id = tv.ID
		}
	}
	if status, body := f.do("DELETE", "/api/m/ai/mcp/tokens/"+id, ""); status != 200 {
		t.Fatalf("revoke: %d %s", status, body)
	}
	if a := ok(); a.status != 401 {
		t.Errorf("revoked: %d %s", a.status, a.body)
	}

	// The settings change: the server's addresses, then a level of off,
	// then no MCP at all, each checked on the next request.
	put := func(s string) {
		t.Helper()
		_, cur := f.do("GET", "/api/m/ai/settings", "")
		var sb struct{ Version string }
		_ = json.Unmarshal([]byte(cur), &sb)
		if status, out := f.do("PUT", "/api/m/ai/settings", `{"settings":`+s+`,"version":"`+sb.Version+`"}`); status != 200 {
			t.Fatalf("PUT settings: %d %s", status, out)
		}
	}
	put(settings(`{"allow":["10.0.0.0/8"]}`, ""))
	if a := f.modern(near, "tools/list", nil); a.status != 403 || !strings.Contains(a.body, "allow") {
		t.Errorf("off the server's addresses: %d %s", a.status, a.body)
	}
	put(`{"users":[{"username":"ada","level":"off"}],"mcp":{}}`)
	if a := f.modern(near, "tools/list", nil); a.status != 403 || !strings.Contains(a.body, "off") {
		t.Errorf("a user whose level is off: %d %s", a.status, a.body)
	}
	put(`{"users":[{"username":"ada","level":"apply"}]}`)
	if a := f.modern(near, "tools/list", nil); a.status != 404 {
		t.Errorf("MCP off: %d %s", a.status, a.body)
	}
	_, body = f.do("GET", "/api/m/ai/mcp", "")
	if !strings.Contains(body, `"enabled":false`) {
		t.Errorf("the page of a server off: %s", body)
	}
}

// TestTokensPage: tokens are made, listed and revoked on the MCP page,
// within their bounds; a user sees their own, one who applies everyone's;
// the store keeps only a hash, and the tokens across a restart.
func TestTokensPage(t *testing.T) {
	st := &memStore{}
	f := start(t, settings(`{"max_tokens_per_user":2,"max_token_days":30}`, ""), st)
	for body, want := range map[string]int{
		`{"name":"","days":1}`:                      400,
		`{"name":"x\u0001","days":1}`:               400,
		`{"name":"x","level":"apply","days":1}`:     400,
		`{"name":"x","days":0}`:                     400,
		`{"name":"x","days":31}`:                    400,
		`{"name":"x","days":1,"allow":["nowhere"]}`: 400,
		`{"name":"x","days":1,"allow":[` + strings.Repeat(`"::1",`, 16) + `"::1"]}`: 400,
		`{"name":` + `"` + strings.Repeat("n", 65) + `","days":1}`:                  400,
	} {
		if status, out := f.do("POST", "/api/m/ai/mcp/tokens", body); status != want {
			t.Errorf("%s: %d %s", body, status, out)
		}
	}
	raw := f.token("one", "propose")
	if status, out := f.do("POST", "/api/m/ai/mcp/tokens", `{"name":"one","days":1}`); status != 409 {
		t.Errorf("a name taken: %d %s", status, out)
	}
	f.token("two", "read")
	if status, out := f.do("POST", "/api/m/ai/mcp/tokens", `{"name":"three","days":1}`); status != 409 {
		t.Errorf("past max_tokens_per_user: %d %s", status, out)
	}
	sec := string(st.Section(Section))
	_, secret, _ := strings.Cut(strings.TrimPrefix(raw, tokenPrefix), "_")
	if strings.Contains(sec, secret) || !strings.Contains(sec, hashOf(secret)) {
		t.Errorf("the store keeps %s", sec)
	}

	// bob, whose level is read, makes read tokens only, and sees his own.
	bc, bcsrf := f.login("bob")
	if status, out := f.page(bc, bcsrf, "POST", "/api/m/ai/mcp/tokens", `{"name":"b","level":"propose","days":1}`); status != 403 {
		t.Errorf("bob's propose token: %d %s", status, out)
	}
	bobTok := f.tokenAs(bc, bcsrf, `{"name":"b","days":1}`)
	_, body := f.page(bc, bcsrf, "GET", "/api/m/ai/mcp", "")
	var bv mcpView
	_ = json.Unmarshal([]byte(body), &bv)
	if len(bv.Tokens) != 1 || bv.Tokens[0].Name != "b" || bv.All || strings.Contains(body, "hash") || strings.Contains(body, bobTok) {
		t.Fatalf("bob's page: %s", body)
	}
	if !strings.HasSuffix(bv.Endpoint, "/mcp") || !bv.Enabled || !bv.Store || bv.Settings["max_tokens_per_user"] != float64(2) {
		t.Errorf("bob's page: %s", body)
	}
	_, body = f.do("GET", "/api/m/ai/mcp", "")
	var av mcpView
	_ = json.Unmarshal([]byte(body), &av)
	if len(av.Tokens) != 3 || !av.All {
		t.Fatalf("ada's page: %s", body)
	}
	var adaID, bobID string
	for _, tv := range av.Tokens {
		switch tv.Name {
		case "one":
			adaID = tv.ID
			if tv.Effective != "propose" {
				t.Errorf("one's level: %+v", tv)
			}
		case "b":
			bobID = tv.ID
		}
	}
	// bob revokes none of ada's; ada, who applies, revokes bob's.
	if status, _ := f.page(bc, bcsrf, "DELETE", "/api/m/ai/mcp/tokens/"+adaID, ""); status != 404 {
		t.Errorf("bob revoking ada's: %d", status)
	}
	if status, out := f.do("DELETE", "/api/m/ai/mcp/tokens/"+bobID, ""); status != 200 {
		t.Errorf("ada revoking bob's: %d %s", status, out)
	}
	if a := f.modern(bobTok, "tools/list", nil); a.status != 401 {
		t.Errorf("bob's revoked token: %d", a.status)
	}

	// A restart: the tokens the store keeps still work.
	g := start(t, settings(`{}`, ""), st)
	if a := g.modern(raw, "tools/list", nil); a.status != 200 || !has(toolNames(a), "config_propose") {
		t.Fatalf("after a restart: %d %s", a.status, a.body)
	}

	// A store that cannot be written: no token is made, and a revoke says
	// it holds until a restart.
	st.setFail(true)
	if status, out := g.do("POST", "/api/m/ai/mcp/tokens", `{"name":"later","days":1}`); status != 500 {
		t.Errorf("a token the store does not keep: %d %s", status, out)
	}
	if status, out := g.do("DELETE", "/api/m/ai/mcp/tokens/"+adaID, ""); status != 500 || !strings.Contains(out, "restarts") {
		t.Errorf("a revoke the store does not keep: %d %s", status, out)
	}
	if a := g.modern(raw, "tools/list", nil); a.status != 401 {
		t.Errorf("revoked in memory: %d", a.status)
	}
	st.setFail(false)
	_, body = g.do("GET", "/api/m/ai/mcp", "")
	if strings.Contains(body, `"later"`) {
		t.Errorf("the token not kept is listed: %s", body)
	}

	// A section that does not load is left, and said so.
	bad := &memStore{sec: map[string][]byte{Section: []byte("{")}}
	h := start(t, settings(`{}`, ""), bad)
	if len(h.s.tokens.view("", true)) != 0 || !strings.Contains(h.logged(), "do not load") {
		t.Errorf("a bad section: %v", h.logged())
	}
}

// TestTokensWithoutStore: without a store, tokens last until fit restarts,
// and the page says so; an expired token stays listed a while, then goes.
func TestTokensWithoutStore(t *testing.T) {
	f := start(t, settings(`{}`, ""), nil)
	raw := f.token("mem", "read")
	if a := f.modern(raw, "tools/list", nil); a.status != 200 {
		t.Fatalf("%d %s", a.status, a.body)
	}
	_, body := f.do("GET", "/api/m/ai/mcp", "")
	if !strings.Contains(body, `"store":false`) || !strings.Contains(body, `"last_addr":"127.0.0.1"`) {
		t.Errorf("the page: %s", body)
	}
	// Expired: listed as such.
	f.s.skew.Store(int64(31 * 24 * time.Hour))
	_, body = f.do("GET", "/api/m/ai/mcp", "")
	if !strings.Contains(body, `"expired":true`) {
		t.Errorf("an expired token: %s", body)
	}
	f.s.skew.Store(0)

	// The tokens' writes drop those expired a week ago.
	ts := &tokens{save: func([]byte) error { return nil }}
	ts.list = []*Token{{ID: "a", Hash: "h", Expires: time.Now().Add(-8 * 24 * time.Hour).UnixMilli()},
		{ID: "b", Hash: "h", Expires: time.Now().Add(-time.Hour).UnixMilli()}}
	if err := ts.write(); err != nil || len(ts.list) != 1 || ts.list[0].ID != "b" {
		t.Errorf("write: %v %+v", err, ts.list)
	}
}

func TestEffective(t *testing.T) {
	for _, c := range []struct{ token, user, want string }{
		{"read", "apply", "read"}, {"propose", "apply", "propose"}, {"propose", "edit", "edit"}, {"propose", "read", "read"},
		{"read", "off", "off"}, {"bogus", "apply", "off"},
	} {
		if got := effective(c.token, c.user); got != c.want {
			t.Errorf("effective(%s, %s) = %s, want %s", c.token, c.user, got, c.want)
		}
	}
	for v, want := range map[string]string{"plain": "plain", "=?base64?aGk=?=": "hi", "=?base64?open": "=?base64?open"} {
		if got, ok := headerValue(v); !ok || got != want {
			t.Errorf("headerValue(%q) = %q %v", v, got, ok)
		}
	}
	if anchor("Media auth: the_x-y!") != "media-auth-the_x-y" {
		t.Errorf("anchor: %s", anchor("Media auth: the_x-y!"))
	}
}

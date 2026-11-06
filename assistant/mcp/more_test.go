package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// TestSmall covers what the protocol's tests reach only rarely: the
// defaults, an address that does not parse, a missing Accept, the version
// without build information, and propose calls the pool refuses.
func TestSmall(t *testing.T) {
	s := New(Options{})
	if s.o.URL() != "" || s.proposalsURL() != "/#/ai/proposals" {
		t.Errorf("the defaults: %q", s.proposalsURL())
	}
	s.o.Logf("nothing %d", 1)
	if a := addrOf("not an address"); a.IsValid() {
		t.Errorf("addrOf: %v", a)
	}
	if a := addrOf("[::ffff:127.0.0.1]:80"); a.String() != "127.0.0.1" {
		t.Errorf("addrOf: %v", a)
	}
	if versionOf(nil, false) != "devel" || versionOf(&debug.BuildInfo{Main: debug.Module{Version: "v1.2.3"}}, true) != "v1.2.3" {
		t.Error("versionOf")
	}

	// The pool takes none of three propose calls: each is refused, in
	// turn, and the token's turn ends.
	s.live["id"] = &live{sessions: map[string]*session{}}
	var refused []int
	no := func(func()) bool { return false }
	ran := 0
	yes := func(fn func()) bool { fn(); return true }
	s.live["id"].proposing = true
	for i := range 2 {
		s.live["id"].queue = append(s.live["id"].queue, job{run: func() { ran++ }, refuse: func() { refused = append(refused, i+1) }, submit: no})
	}
	s.runSerial("id", job{run: func() { ran++ }, refuse: func() { refused = append(refused, 0) }, submit: no})
	if fmt.Sprint(refused) != "[0 1 2]" || ran != 0 || s.live["id"].proposing {
		t.Errorf("refused %v, ran %d", refused, ran)
	}
	// One taken: it runs, and the next after it.
	s.serial("id", job{run: func() { ran++ }, submit: yes})
	if ran != 1 || s.live["id"].proposing {
		t.Errorf("ran %d", ran)
	}
}

// TestMore: a page request that is not JSON, a user whose assistant is off,
// a request without Accept, a legacy session past its idle time, a store
// that cannot keep the token's conversation, a skill's required input, and
// a client that reads its large result late.
func TestMore(t *testing.T) {
	st := &memStore{}
	f := start(t, settings(`{"session_idle_timeout": "1m"}`, `"default_level": "off"`), st)
	if status, _ := f.do("POST", "/api/m/ai/mcp/tokens", `{`); status != 400 {
		t.Errorf("a body not JSON: %d", status)
	}
	cc, ccsrf := f.login("cy")
	if status, out := f.page(cc, ccsrf, "POST", "/api/m/ai/mcp/tokens", `{"name":"c","days":1}`); status != 403 {
		t.Errorf("a user whose level is off: %d %s", status, out)
	}
	tok := f.token("m", "propose")

	// No Accept: JSON.
	body, h := modernRequest("tools/list", nil)
	h["Accept"] = ""
	if a := f.post(tok, body, h); a.status != 200 || a.sse {
		t.Errorf("no Accept: %d %s", a.status, a.body)
	}

	// A session past its idle time ends; one more opened prunes it.
	sid := f.legacy(tok, "2025-11-25")
	other := f.legacy(tok, "2025-11-25")
	f.s.skew.Store(int64(2 * time.Minute))
	if a := f.inSession(tok, sid, "2025-11-25", "ping", ""); a.status != 404 {
		t.Errorf("an idle session: %d", a.status)
	}
	f.legacy(tok, "2025-11-25")
	f.s.mu.Lock()
	n := 0
	for _, lv := range f.s.live {
		n += len(lv.sessions)
	}
	_, kept := f.s.live[strings.SplitN(strings.TrimPrefix(tok, tokenPrefix), "_", 2)[0]].sessions[other]
	f.s.mu.Unlock()
	if n != 1 || kept {
		t.Errorf("%d sessions, the idle one kept: %v", n, kept)
	}
	f.s.skew.Store(0)

	// The token's conversation, not kept by a failing store: said so.
	st.setFail(true)
	a := f.modern(tok, "tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{"summary": "answer two",
		"ops": []map[string]any{{"op": "replace", "path": "/routes/0/handler/params/body", "value": "two"}}}})
	if a.result()["isError"] != false || !strings.Contains(f.logged(), "kept in memory only") {
		t.Errorf("a store that fails: %s %s", a.body, f.logged())
	}
	st.setFail(false)

	// A skill with a required input.
	skill := "---\nname: needs-one\ntitle: Needs one\ndescription: Takes a route.\ntools: [config_get]\ninputs:\n  - name: route\n    description: A route.\n    required: true\n---\nExplain the route.\n"
	src, _ := json.Marshal(map[string]string{"source": skill})
	if status, out := f.do("POST", "/api/m/ai/skills", string(src)); status != 200 {
		t.Fatalf("a skill: %d %s", status, out)
	}
	if a := f.modern(tok, "prompts/get", map[string]any{"name": "needs-one"}); a.errCode() != codeInvalidParams || !strings.Contains(a.body, "needs the argument route") {
		t.Errorf("a required input: %s", a.body)
	}
}

// TestLateReader: a client that reads its large result a while after the
// call ended: the stream waits for it, within stallTimeout, and it gets all
// of it.
func TestLateReader(t *testing.T) {
	f := start(t, settings(`{}`, `"limits": {"max_tool_result_bytes": 16777216}`), nil, newGated().tools())
	tok := f.token("s", "read")
	conn := bigCall(t, f, tok)
	time.Sleep(200 * time.Millisecond)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	n, err := io.Copy(io.Discard, conn)
	if err != nil || n < 2<<20 {
		t.Errorf("a late reader read %d bytes, then %v", n, err)
	}
}

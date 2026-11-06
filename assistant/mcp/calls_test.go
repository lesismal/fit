package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/assistant"
	"github.com/lesismal/fit/config"
)

// gated are tools that wait for their gate, or for their call to end.
type gated struct {
	gate      chan struct{}
	started   chan string
	cancelled atomic.Int32
}

func newGated() *gated { return &gated{gate: make(chan struct{}), started: make(chan string, 16)} }

func (g *gated) tools() []*assistant.Tool {
	wait := func(name string) func(c *assistant.Call) (string, error) {
		return func(c *assistant.Call) (string, error) {
			g.started <- name
			select {
			case <-g.gate:
				return name + " done", nil
			case <-c.Context.Done():
				g.cancelled.Add(1)
				return "", c.Context.Err()
			}
		}
	}
	obj := json.RawMessage(`{"type":"object","properties":{}}`)
	return []*assistant.Tool{
		{Name: "slow_read", Group: "test", Level: config.AssistantRead, Description: "waits", Parameters: obj, Run: wait("slow_read")},
		{Name: "slow_propose", Group: "test", Level: config.AssistantPropose, Description: "waits", Parameters: obj, Run: wait("slow_propose")},
		{Name: "big_read", Group: "test", Level: config.AssistantRead, Description: "large", Parameters: obj,
			Run: func(*assistant.Call) (string, error) { return strings.Repeat("x", 2<<20), nil }},
		{Name: "panic_read", Group: "test", Level: config.AssistantRead, Description: "panics", Parameters: obj,
			Run: func(*assistant.Call) (string, error) { panic("boom") }},
		{Name: "page_read", Group: assistant.GroupPage, Level: config.AssistantRead, Description: "a page's", Parameters: obj,
			Run: func(*assistant.Call) (string, error) { return "", nil }},
	}
}

// inflight returns the requests under way of the token raw.
func (f *fixture) inflight(raw string) int {
	id, _, _ := strings.Cut(strings.TrimPrefix(raw, tokenPrefix), "_")
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if lv := f.s.live[id]; lv != nil {
		return lv.inflight
	}
	return 0
}

// eventually waits up to 30s for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(30 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("%s: not by 30s", what)
}

// TestPropose: a propose token's config_propose adds steps to its one
// pending proposal, kept in a conversation of its user's, which the
// workbench lists; the result links to the workbench and applies nothing.
// A read token gets no propose tools; calls made at once take their steps
// one at a time.
func TestPropose(t *testing.T) {
	st := &memStore{}
	f := start(t, settings(`{}`, ""), st)
	read := f.token("reader", "read")
	if names := toolNames(f.modern(read, "tools/list", nil)); has(names, "config_propose") || has(names, "rollback_propose") {
		t.Fatalf("a read token's tools: %v", names)
	}
	if a := f.modern(read, "tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{"summary": "x"}}); a.errCode() != codeInvalidParams {
		t.Fatalf("a read token proposing: %s", a.body)
	}
	tok := f.token("agent", "propose")
	names := toolNames(f.modern(tok, "tools/list", nil))
	for _, n := range []string{"config_propose", "reconcile_propose", "rollback_propose", "cache_purge_propose", "skill_load"} {
		if !has(names, n) {
			t.Errorf("a propose token lacks %s: %v", n, names)
		}
	}
	// Neither ask_user nor the page tools (I154), which need the person
	// and the open page, are served, though the token's level, propose,
	// is above edit's.
	for _, n := range []string{"ask_user", assistant.ToolPageGet, assistant.ToolPageEdit, assistant.ToolPageRevert} {
		if has(names, n) {
			t.Errorf("%s is served: %v", n, names)
		}
		if a := f.modern(tok, "tools/call", map[string]any{"name": n, "arguments": map[string]any{}}); a.errCode() != codeInvalidParams {
			t.Errorf("calling %s: %s", n, a.body)
		}
	}
	if f.a.Registry().Tool(assistant.ToolPageEdit) == nil {
		t.Fatal("the registry has no page tools to leave out")
	}
	step := func(body string) *answer {
		return f.modern(tok, "tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{"summary": "answer " + body,
			"ops": []map[string]any{{"op": "replace", "path": "/routes/0/handler/params/body", "value": body}}}})
	}
	a := step("two")
	if a.result()["isError"] != false || !strings.Contains(a.text(), `"valid":true`) || !strings.Contains(a.text(), "/#/ai/proposals") ||
		!strings.Contains(a.text(), "No tool applies it") {
		t.Fatalf("config_propose: %s", a.body)
	}
	noSecret(t, "a proposal's diff", a.body)
	_, body := f.do("GET", "/api/m/ai/proposals", "")
	var pl struct {
		Proposals []struct {
			User, Conversation, Title, ID string
			Steps                         []assistant.AuditStep
		}
	}
	_ = json.Unmarshal([]byte(body), &pl)
	if len(pl.Proposals) != 1 || pl.Proposals[0].Title != "MCP: agent" || len(pl.Proposals[0].Steps) != 1 {
		t.Fatalf("the workbench's proposals: %s", body)
	}
	conv := pl.Proposals[0].Conversation

	// Three steps at once: each joins the one proposal.
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			if a := step(fmt.Sprint("n", i)); a.result()["isError"] != false {
				t.Errorf("step %d: %s", i, a.body)
			}
		})
	}
	wg.Wait()
	_, body = f.do("GET", "/api/m/ai/proposals/"+conv, "")
	if c := strings.Count(body, `"tool":"config_propose"`); c != 4 {
		t.Fatalf("the proposal has %d steps: %s", c, body)
	}
	// The conversation is the token's from now on, across a restart.
	g := start(t, settings(`{}`, ""), st)
	g.modern(tok, "tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{"summary": "answer five",
		"ops": []map[string]any{{"op": "replace", "path": "/routes/0/handler/params/body", "value": "five"}}}})
	_, body = g.do("GET", "/api/m/ai/proposals", "")
	if strings.Count(body, `"conversation":"`+conv+`"`) != 1 || strings.Count(body, `"tool":"config_propose"`) != 5 {
		t.Fatalf("after a restart: %s", body)
	}
	// A wrong step is the call's error, added to nothing.
	if a := g.modern(tok, "tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{"summary": "admin",
		"members": map[string]any{"admin": map[string]any{}}}}); a.result()["isError"] != true || !strings.Contains(a.text(), "admin") {
		t.Fatalf("a step into admin: %s", a.body)
	}
	// The audit log names the token and the proposal.
	entries, _ := g.a.AuditLog(assistant.AuditFilter{Kind: assistant.AuditTool})
	found := false
	for _, e := range entries {
		if e.Token == "agent" && e.Proposal != nil && e.Conversation == conv {
			found = true
		}
	}
	if !found {
		t.Errorf("no audit entry names the proposal: %+v", entries)
	}
}

// TestBounds: a token's requests a minute, its requests at once and its
// request's bytes are bounded; a slow tool is given up on at its timeout;
// a stream the client cuts cancels its call, and a propose call it held
// adds no step; a client that takes nothing has its stream cut; and the
// token's count of requests under way comes back to 0 each time.
func TestBounds(t *testing.T) {
	g := newGated()
	f := start(t, settings(`{"max_calls_per_minute": 1000, "max_concurrent_calls": 2, "max_request_bytes": 4096}`,
		`"limits": {"max_tool_result_bytes": 16777216}`), nil, g.tools())
	tok := f.token("b", "propose")

	// Request bytes.
	if a := f.modern(tok, "tools/call", map[string]any{"name": "status_get", "arguments": map[string]any{"pad": strings.Repeat("p", 5000)}}); a.status != 413 {
		t.Errorf("a large request: %d %s", a.status, a.body)
	}

	// Requests at once: two slow calls hold both; a third is refused.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *answer, 2)
	for range 2 {
		go func() {
			body, h := modernRequest("tools/call", map[string]any{"name": "slow_read"})
			done <- f.send(ctx, "POST", tok, body, h)
		}()
		<-g.started
	}
	if a := f.modern(tok, "tools/list", nil); a.status != 429 || a.header.Get("Retry-After") == "" {
		t.Errorf("past max_concurrent_calls: %d %s", a.status, a.body)
	}
	// The clients cut their streams: the calls are cancelled.
	cancel()
	<-done
	<-done
	eventually(t, "the cut calls' cancel", func() bool { return g.cancelled.Load() == 2 && f.inflight(tok) == 0 })

	// A propose call waits for the token's one before it; cut while it
	// waits, it adds no step once its turn comes.
	ctx2, cancel2 := context.WithCancel(context.Background())
	first := make(chan *answer, 1)
	go func() {
		body, h := modernRequest("tools/call", map[string]any{"name": "slow_propose"})
		first <- f.send(context.Background(), "POST", tok, body, h)
	}()
	<-g.started
	second := make(chan *answer, 1)
	go func() {
		body, h := modernRequest("tools/call", map[string]any{"name": "config_propose", "arguments": map[string]any{"summary": "answer late",
			"ops": []map[string]any{{"op": "replace", "path": "/routes/0/handler/params/body", "value": "late"}}}})
		second <- f.send(ctx2, "POST", tok, body, h)
	}()
	eventually(t, "the second call's admission", func() bool { return f.inflight(tok) == 2 })
	cancel2()
	<-second
	eventually(t, "the cut call's end", func() bool { return f.inflight(tok) == 1 })
	close(g.gate)
	if a := <-first; !strings.Contains(a.text(), "slow_propose done") {
		t.Fatalf("the first propose call: %s", a.body)
	}
	time.Sleep(100 * time.Millisecond)
	_, body := f.do("GET", "/api/m/ai/proposals", "")
	if strings.Contains(body, "answer late") {
		t.Fatalf("a cut call added its step: %s", body)
	}
	eventually(t, "the calls' end", func() bool { return f.inflight(tok) == 0 })

	// A tool that panics fails its call, and a page tool is not served.
	if a := f.modern(tok, "tools/call", map[string]any{"name": "panic_read"}); a.result()["isError"] != true || !strings.Contains(a.text(), "boom") {
		t.Errorf("a panic: %s", a.body)
	}
	if a := f.modern(tok, "tools/call", map[string]any{"name": "page_read"}); a.errCode() != codeInvalidParams {
		t.Errorf("a page tool: %s", a.body)
	}
}

// TestTimeoutAndStall: a call past callTimeout is answered as failed, and
// a client that takes nothing of a large result has its stream cut.
func TestTimeoutAndStall(t *testing.T) {
	oldCall, oldStall, oldPing := callTimeout, stallTimeout, pingInterval
	callTimeout, stallTimeout, pingInterval = 300*time.Millisecond, 300*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { callTimeout, stallTimeout, pingInterval = oldCall, oldStall, oldPing })
	g := newGated()
	f := start(t, settings(`{}`, `"limits": {"max_tool_result_bytes": 16777216}`), nil, g.tools())
	tok := f.token("s", "read")
	a := f.modern(tok, "tools/call", map[string]any{"name": "slow_read"})
	if a.result()["isError"] != true || !strings.Contains(a.text(), "longer than") || a.comment < 2 {
		t.Fatalf("a slow tool: %d comments, %s", a.comment, a.body)
	}
	eventually(t, "the slow call's cancel", func() bool { return g.cancelled.Load() == 1 && f.inflight(tok) == 0 })
}

// bigCall sends big_read's call to f's /mcp with tok over a connection of
// its own, which reads nothing, and returns once the call has ended, its
// 2 MiB result handed to the connection. The server's socket buffer is cut
// to 16 KiB and the client's set to 256 KiB, so that the kernel takes far
// less than the result on every OS, and fib's queue holds the rest: on
// Linux, loopback's send buffer starts at about 2.6 MiB (a 64 KiB MSS
// times the initial window), and would take the whole result. The client's
// buffer stays above the MSS, which Linux's TCP needs to keep a window
// open without dropping what it is sent.
func bigCall(t *testing.T, f *fixture, tok string) net.Conn {
	t.Helper()
	streamOpened = func(r *console.Request) {
		if err := r.Context.Conn.SetWriteBuffer(16 << 10); err != nil {
			t.Errorf("SetWriteBuffer: %v", err)
		}
	}
	t.Cleanup(func() { streamOpened = nil })
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := conn.(*net.TCPConn).SetReadBuffer(256 << 10); err != nil {
		t.Fatal(err)
	}
	body, h := modernRequest("tools/call", map[string]any{"name": "big_read"})
	req := "POST /mcp HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer " + tok +
		"\r\nContent-Type: application/json\r\nAccept: application/json, text/event-stream\r\nContent-Length: " + fmt.Sprint(len(body)) + "\r\nConnection: close\r\n"
	for k, v := range h {
		req += k + ": " + v + "\r\n"
	}
	if _, err := conn.Write([]byte(req + "\r\n" + body)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the call's end", func() bool {
		entries, _ := f.a.AuditLog(assistant.AuditFilter{Kind: assistant.AuditTool})
		return len(entries) > 0 && entries[0].Calls[0].Tool == "big_read" && f.inflight(tok) == 0
	})
	return conn
}

// TestStall: a client that takes nothing of a large result has its stream
// cut stallTimeout after the queue passed the watermark: it reads what the
// kernels held, then the end of a connection the server closed, never the
// whole result, and never waits for it.
func TestStall(t *testing.T) {
	oldStall := stallTimeout
	stallTimeout = 300 * time.Millisecond
	t.Cleanup(func() { stallTimeout = oldStall })
	f := start(t, settings(`{}`, `"limits": {"max_tool_result_bytes": 16777216}`), nil, newGated().tools())
	tok := f.token("s", "read")
	conn := bigCall(t, f, tok)
	time.Sleep(stallTimeout + time.Second)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	n, err := io.Copy(io.Discard, conn)
	var ne net.Error
	if n >= 2<<20 || errors.As(err, &ne) && ne.Timeout() {
		t.Errorf("a client that took nothing read %d bytes, then %v", n, err)
	}
}

// TestRates: past its requests a minute, a token is answered 429 until its
// share comes back.
func TestRates(t *testing.T) {
	f := start(t, settings(`{"max_calls_per_minute": 2}`, ""), nil)
	tok := f.token("r", "read")
	f.modern(tok, "tools/list", nil)
	f.modern(tok, "tools/list", nil)
	a := f.modern(tok, "tools/list", nil)
	if a.status != 429 || a.header.Get("Retry-After") == "" {
		t.Fatalf("past the minute's share: %d %s", a.status, a.body)
	}
	f.s.skew.Store(int64(time.Minute))
	if a := f.modern(tok, "tools/list", nil); a.status != 200 {
		t.Errorf("a minute later: %d %s", a.status, a.body)
	}
}

// TestDocs: without docs, or with docs that fail, the reference is not
// served, and reading it says why.
func TestDocs(t *testing.T) {
	f := start(t, settings(`{}`, ""), nil, func(o *Options) { o.Docs = nil })
	tok := f.token("d", "read")
	if a := f.modern(tok, "resources/list", nil); strings.Contains(a.body, docsURI) {
		t.Errorf("docs without docs: %s", a.body)
	}
	if a := f.modern(tok, "resources/templates/list", nil); strings.Contains(a.body, docsURI) {
		t.Errorf("templates without docs: %s", a.body)
	}
	if a := f.modern(tok, "resources/read", map[string]any{"uri": docsURI}); a.errCode() != codeInvalidParams {
		t.Errorf("reading docs without docs: %s", a.body)
	}
	g := start(t, settings(`{}`, ""), nil, func(o *Options) { o.Docs = func() ([]byte, error) { return nil, fmt.Errorf("no reference") } })
	tok = g.token("d", "read")
	if a := g.modern(tok, "resources/read", map[string]any{"uri": docsURI}); a.errCode() != codeInternal || !strings.Contains(a.body, "no reference") {
		t.Errorf("reading failed docs: %s", a.body)
	}
	if a := g.modern(tok, "resources/read", map[string]any{"uri": "fit://status"}); a.errCode() != 0 {
		t.Errorf("status beside failed docs: %s", a.body)
	}
	big := "x" + strings.Repeat("é", assistant.MaxResourceBytes)
	if c := clipText(big); len(c) > assistant.MaxResourceBytes+100 || !strings.HasSuffix(c, "chapter instead]") {
		t.Errorf("clipText: %d", len(c))
	}
	if c := clipText("short"); c != "short" {
		t.Errorf("clipText: %s", c)
	}
	h := start(t, settings(`{}`, ""), nil)
	tok = h.token("d", "read")
	if a := h.modern(tok, "resources/read", map[string]any{"uri": docsURI}); !strings.Contains(a.body, "All of it") {
		t.Errorf("the reference: %s", a.body)
	}
}

// TestPrompts: a prompt's arguments are a skill's inputs, checked.
func TestPrompts(t *testing.T) {
	f := start(t, settings(`{}`, ""), nil)
	tok := f.token("p", "read")
	var withInput string
	var required string
	for _, sk := range f.a.EnabledSkills() {
		for _, in := range sk.Inputs {
			if withInput == "" {
				withInput = sk.Name + "/" + in.Name
			}
			if in.Required && required == "" {
				required = sk.Name + "/" + in.Name
			}
		}
	}
	if withInput == "" {
		t.Skip("no built-in skill takes an input")
	}
	name, input, _ := strings.Cut(withInput, "/")
	if a := f.modern(tok, "prompts/get", map[string]any{"name": name, "arguments": map[string]string{input: "the value"}}); !strings.Contains(a.body, "the value") {
		t.Errorf("an input: %s", a.body)
	}
	for what, args := range map[string]map[string]any{
		"unknown prompt": {"name": "nothing"},
		"unknown arg":    {"name": name, "arguments": map[string]string{"nothing": "x"}},
		"long arg":       {"name": name, "arguments": map[string]string{input: strings.Repeat("x", maxPromptArg+1)}},
		"not strings":    {"name": name, "arguments": map[string]any{input: 1}},
	} {
		if a := f.modern(tok, "prompts/get", args); a.errCode() != codeInvalidParams {
			t.Errorf("%s: %s", what, a.body)
		}
	}
	if required != "" {
		name, _, _ := strings.Cut(required, "/")
		if a := f.modern(tok, "prompts/get", map[string]any{"name": name}); a.errCode() != codeInvalidParams {
			t.Errorf("a required input left out: %s", a.body)
		}
	}
	// A skill naming tools a read token may not call says so.
	if a := f.modern(tok, "prompts/get", map[string]any{"name": "route-builder"}); !strings.Contains(a.body, "may not") {
		t.Errorf("route-builder for a read token: %s", a.body)
	}
}

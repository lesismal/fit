package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant"
)

// toolNames returns a tools/list answer's tools' names.
func toolNames(a *answer) []string {
	var out []string
	for _, t := range asList(a.result()["tools"]) {
		m, _ := t.(map[string]any)
		out = append(out, m["name"].(string))
	}
	return out
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// TestModern: a client of the stateless revision discovers the server,
// lists the tools, prompts and resources, reads the config and the status,
// calls a tool over SSE, and no secret reaches it.
func TestModern(t *testing.T) {
	f := start(t, settings(`{}`, ""), &memStore{})
	tok := f.token("laptop", "read")

	d := f.modern(tok, "server/discover", nil)
	if d.status != 200 || d.result()["resultType"] != "complete" || !strings.Contains(d.body, versionModern) || !strings.Contains(d.body, `"tools":{}`) ||
		!strings.Contains(d.body, metaServerInfo) {
		t.Fatalf("discover: %d %s", d.status, d.body)
	}
	if d.header.Get("Content-Type") != "application/json" || d.header.Get(headerSession) != "" {
		t.Errorf("discover's headers: %v", d.header)
	}

	l := f.modern(tok, "tools/list", nil)
	names := toolNames(l)
	if !has(names, "config_get") || !has(names, "status_get") || has(names, "config_propose") || has(names, "ask_user") {
		t.Fatalf("a read token's tools: %v", names)
	}
	if l.result()["ttlMs"] != float64(0) || l.result()["cacheScope"] != "private" {
		t.Errorf("tools/list's cache hints: %s", l.body)
	}

	c := f.modern(tok, "tools/call", map[string]any{"name": "config_get", "arguments": map[string]any{}})
	if c.status != 200 || !c.sse || c.comment == 0 || c.result()["isError"] != false || !strings.Contains(c.text(), `"routes"`) {
		t.Fatalf("config_get: %d sse=%v %s", c.status, c.sse, c.body)
	}
	noSecret(t, "config_get", c.body)
	st := f.modern(tok, "tools/call", map[string]any{"name": "status_get"})
	if !strings.Contains(st.text(), `"connections"`) {
		t.Fatalf("status_get: %s", st.body)
	}

	pl := f.modern(tok, "prompts/list", nil)
	if !strings.Contains(pl.body, `"name":"explain"`) {
		t.Fatalf("prompts/list: %s", pl.body)
	}
	pg := f.modern(tok, "prompts/get", map[string]any{"name": "explain"})
	if !strings.Contains(pg.body, `skill name=\"explain\"`) || !strings.Contains(pg.body, `"role":"user"`) {
		t.Fatalf("prompts/get: %s", pg.body)
	}

	rl := f.modern(tok, "resources/list", nil)
	for _, uri := range []string{"fit://config/base", "fit://config/effective", "fit://status", "fit://versions", "fit://schema", docsURI, docsURI + "/media-auth"} {
		if !strings.Contains(rl.body, `"`+uri+`"`) {
			t.Errorf("resources/list lacks %s: %s", uri, rl.body)
		}
	}
	if strings.Contains(rl.body, "not-a-title") {
		t.Errorf("a fenced line was taken for a chapter: %s", rl.body)
	}
	tl := f.modern(tok, "resources/templates/list", nil)
	for _, u := range []string{"fit://schema/{path}", "fit://versions/{n}", docsURI + "/{chapter}"} {
		if !strings.Contains(tl.body, u) {
			t.Errorf("resources/templates/list lacks %s: %s", u, tl.body)
		}
	}
	for _, uri := range []string{"fit://config/base", "fit://config/effective", "fit://versions/2", "fit://versions/3", "fit://status", "fit://versions"} {
		r := f.modern(tok, "resources/read", map[string]any{"uri": uri})
		if r.status != 200 || r.errCode() != 0 || !strings.Contains(r.body, `"contents"`) {
			t.Fatalf("read %s: %d %s", uri, r.status, r.body)
		}
		noSecret(t, uri, r.body)
	}
	if r := f.modern(tok, "resources/read", map[string]any{"uri": "fit://schema/admin.console"}); !strings.Contains(r.body, "users") {
		t.Errorf("schema resource: %s", r.body)
	}
	if r := f.modern(tok, "resources/read", map[string]any{"uri": docsURI + "/listeners"}); !strings.Contains(r.body, "Where fit listens") || strings.Contains(r.body, "Who plays") {
		t.Errorf("a chapter: %s", r.body)
	}
	for _, uri := range []string{"fit://versions/9", "fit://nothing", "fit://versions/x", docsURI + "/nothing", "fit://schema/a/b"} {
		r := f.modern(tok, "resources/read", map[string]any{"uri": uri})
		if r.errCode() != codeInvalidParams {
			t.Errorf("read %s: %s", uri, r.body)
		}
	}

	// Every call is in the audit log, with its source and token.
	entries, _ := f.a.AuditLog(assistant.AuditFilter{Kind: assistant.AuditTool})
	if len(entries) < 5 {
		t.Fatalf("audit: %+v", entries)
	}
	for _, e := range entries {
		if e.Source != assistant.SourceMCP || e.Token != "laptop" || e.User != "ada" {
			t.Fatalf("an entry: %+v", e)
		}
	}
}

// TestModernChecks: the modern revision's headers are checked against its
// body, an unknown version is refused with the versions served, and an
// unknown method is a 404.
func TestModernChecks(t *testing.T) {
	f := start(t, settings(`{}`, ""), nil)
	tok := f.token("t", "read")
	body, h := modernRequest("tools/call", map[string]any{"name": "status_get"})
	for name, change := range map[string]map[string]string{
		"no version":      {headerVersion: ""},
		"another version": {headerVersion: "2025-11-25"},
		"no method":       {headerMethod: ""},
		"another method":  {headerMethod: "tools/list"},
		"no name":         {headerName: ""},
		"another name":    {headerName: "config_get"},
		"bad base64":      {headerName: "=?base64?!!!?="},
	} {
		hh := map[string]string{}
		for k, v := range h {
			hh[k] = v
		}
		for k, v := range change {
			hh[k] = v
		}
		a := f.post(tok, body, hh)
		if a.status != 400 || a.errCode() != codeHeaderMismatch {
			t.Errorf("%s: %d %s", name, a.status, a.body)
		}
	}
	// A name sent as Base64 is decoded.
	hh := map[string]string{}
	for k, v := range h {
		hh[k] = v
	}
	hh[headerName] = "=?base64?c3RhdHVzX2dldA==?="
	if a := f.post(tok, body, hh); a.status != 200 || a.result()["isError"] != false {
		t.Errorf("a Base64 name: %d %s", a.status, a.body)
	}

	old := strings.Replace(body, versionModern, "1900-01-01", 1)
	a := f.post(tok, old, map[string]string{headerVersion: "1900-01-01", headerMethod: "tools/call", headerName: "status_get"})
	if a.status != 400 || a.errCode() != codeUnsupportedVersion || !strings.Contains(a.body, `"supported":["2026-07-28","2025-11-25"`) || !strings.Contains(a.body, `"requested":"1900-01-01"`) {
		t.Errorf("an unknown version: %d %s", a.status, a.body)
	}
	for _, m := range []string{"ping", "subscriptions/listen", "logging/setLevel", "nothing"} {
		if a := f.modern(tok, m, nil); a.status != 404 || a.errCode() != codeMethodNotFound {
			t.Errorf("%s: %d %s", m, a.status, a.body)
		}
	}
	if a := f.modern(tok, "tools/call", map[string]any{"name": "nothing"}); a.errCode() != codeInvalidParams {
		t.Errorf("an unknown tool: %s", a.body)
	}
	// A notification is taken, and answered with nothing.
	if a := f.post(tok, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{}}`, nil); a.status != 202 || a.body != "" {
		t.Errorf("a notification: %d %q", a.status, a.body)
	}
	// Without a session nor a version, the request is refused, as a
	// legacy client's sent before its initialize.
	if a := f.post(tok, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil); a.status != 400 || a.errCode() != codeInvalidRequest {
		t.Errorf("no session: %d %s", a.status, a.body)
	}
	for name, c := range map[string]struct {
		body    string
		headers map[string]string
		status  int
	}{
		"a batch":       {`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`, nil, 400},
		"not JSON":      {`{`, nil, 400},
		"not 2.0":       {`{"jsonrpc":"1.0","id":1,"method":"ping"}`, nil, 400},
		"not JSON type": {`{}`, map[string]string{"Content-Type": "text/plain"}, 415},
		"no accept":     {`{}`, map[string]string{"Accept": "text/html"}, 406},
		"bad params":    {`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[1]}`, nil, 200},
	} {
		if a := f.post(tok, c.body, c.headers); a.status != c.status {
			t.Errorf("%s: %d %s", name, a.status, a.body)
		}
	}
	// GET opens no stream; DELETE without a session ends none.
	if a := f.send(t.Context(), "GET", tok, "", nil); a.status != 405 || a.header.Get("Allow") != "POST, DELETE" {
		t.Errorf("GET: %d %s", a.status, a.body)
	}
	if a := f.send(t.Context(), "DELETE", tok, "", nil); a.status != 405 {
		t.Errorf("DELETE: %d %s", a.status, a.body)
	}
	if a := f.send(t.Context(), "PUT", tok, "", nil); a.status != 405 {
		t.Errorf("PUT: %d %s", a.status, a.body)
	}
	// A client that takes JSON only gets the call's result as JSON.
	b2, h2 := modernRequest("tools/call", map[string]any{"name": "status_get"})
	h2["Accept"] = "application/json"
	if a := f.post(tok, b2, h2); a.sse || a.status != 200 || a.result()["isError"] != false {
		t.Errorf("JSON only: sse=%v %d %s", a.sse, a.status, a.body)
	}
}

// TestLegacy: a client of the revisions before 2026-07-28 opens a session
// with initialize, uses it, and ends it; the sessions are bounded per
// token, and a session is the token's own.
func TestLegacy(t *testing.T) {
	f := start(t, settings(`{"max_sessions": 2}`, ""), nil)
	tok := f.token("old", "read")
	sid := f.legacy(tok, "2025-06-18")
	if a := f.inSession(tok, sid, "2025-06-18", "tools/list", ""); a.status != 200 || !has(toolNames(a), "config_get") || a.result()["resultType"] != nil {
		t.Fatalf("tools/list: %d %s", a.status, a.body)
	}
	if a := f.post(tok, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, map[string]string{headerSession: sid}); a.status != 202 {
		t.Errorf("initialized: %d", a.status)
	}
	if a := f.inSession(tok, sid, "2025-06-18", "ping", ""); a.status != 200 || a.body != `{"jsonrpc":"2.0","id":7,"result":{}}` {
		t.Errorf("ping: %d %s", a.status, a.body)
	}
	if a := f.inSession(tok, sid, "2025-11-25", "ping", ""); a.status != 400 {
		t.Errorf("another version than the session's: %d %s", a.status, a.body)
	}
	if a := f.inSession(tok, sid, "2025-06-18", "server/discover", ""); a.status != 200 || a.errCode() != codeMethodNotFound {
		t.Errorf("discover in a session: %d %s", a.status, a.body)
	}
	if a := f.inSession(tok, sid, "2025-06-18", "resources/read", `{"uri":"fit://nothing"}`); a.errCode() != codeLegacyResourceNotFound {
		t.Errorf("a legacy resource not found: %s", a.body)
	}
	if a := f.inSession(tok, sid, "2025-06-18", "tools/call", `{"name":"status_get"}`); !strings.Contains(a.text(), "connections") {
		t.Errorf("tools/call: %s", a.body)
	}
	// An unknown version is answered with the latest legacy one.
	a := f.post(tok, `{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`, nil)
	if !strings.Contains(a.body, `"protocolVersion":"2025-11-25"`) || !strings.Contains(a.body, `"serverInfo"`) {
		t.Errorf("initialize 2024-11-05: %s", a.body)
	}
	// A third session ends the one used least recently: the first.
	third := f.legacy(tok, "2025-11-25")
	if a := f.inSession(tok, sid, "2025-06-18", "ping", ""); a.status != 404 {
		t.Errorf("the first session past max_sessions: %d %s", a.status, a.body)
	}
	// Another token does not reach a session.
	other := f.token("other", "read")
	if a := f.inSession(other, third, "2025-11-25", "ping", ""); a.status != 404 {
		t.Errorf("another token's session: %d", a.status)
	}
	if a := f.send(t.Context(), "DELETE", tok, "", map[string]string{headerSession: third}); a.status != 204 {
		t.Errorf("DELETE: %d %s", a.status, a.body)
	}
	if a := f.send(t.Context(), "DELETE", tok, "", map[string]string{headerSession: third}); a.status != 404 {
		t.Errorf("DELETE again: %d %s", a.status, a.body)
	}
	if a := f.inSession(tok, third, "2025-11-25", "ping", ""); a.status != 404 {
		t.Errorf("an ended session: %d", a.status)
	}
	// The modern revision ignores a session's header.
	body, h := modernRequest("tools/list", nil)
	h[headerSession] = "whatever"
	if a := f.post(tok, body, h); a.status != 200 {
		t.Errorf("modern with a session header: %d %s", a.status, a.body)
	}
	var ignored map[string]any
	_ = json.Unmarshal([]byte(body), &ignored)
}

package assistant

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	fitruntime "github.com/lesismal/fit/runtime"
)

// checkRoute says whether rt's request goes where it must on g, compiled
// from cfg, the config as JSON.
func checkRoute(g *fitruntime.Graph, cfg map[string]any, rt evalRoute) error {
	what := fmt.Sprintf("%s%s on :%d", rt.Host, rt.Path, rt.Port)
	if rt.Remote != "" {
		what += " from " + rt.Remote
	}
	var l *fitruntime.Listener
	for _, x := range g.Listeners {
		_, port, err := net.SplitHostPort(x.Address)
		if err == nil && port == strconv.Itoa(rt.Port) && (x.Router != nil || x.RedirectHTTPS != 0) {
			l = x
			break
		}
	}
	if l == nil {
		return fmt.Errorf("%s: no listener serves HTTP on port %d", what, rt.Port)
	}
	if l.RedirectHTTPS != 0 {
		if rt.Want.Redirect != "" && strings.HasPrefix(rt.Want.Redirect, "https://") {
			return nil
		}
		return fmt.Errorf("%s: the listener only redirects to HTTPS", what)
	}
	req := httptest.NewRequest("GET", "http://"+rt.Host+rt.Path, nil)
	req.RemoteAddr = "198.51.100.7:40000"
	if rt.Remote != "" {
		req.RemoteAddr = net.JoinHostPort(rt.Remote, "40000")
	}
	for k, v := range rt.Header {
		req.Header.Set(k, v)
	}
	route, ok := l.Router.Match(req)
	if !ok {
		if rt.Want.Denied {
			return nil
		}
		return fmt.Errorf("%s: no route takes it", what)
	}
	routes, _ := cfg["routes"].([]any)
	if route.Index >= len(routes) {
		return fmt.Errorf("%s: route %d is not in the config", what, route.Index)
	}
	rc, _ := routes[route.Index].(map[string]any)
	handler, _ := rc["handler"].(map[string]any)
	module, _ := handler["module"].(string)
	params, _ := handler["params"].(map[string]any)
	got := fmt.Sprintf("%s: route %q, handler %s", what, route.Name, module)
	w := rt.Want
	if w.Denied {
		status, _ := params["status"].(float64)
		if module != "respond" || !slices.Contains([]float64{401, 403, 404}, status) {
			return fmt.Errorf("%s, not refused", got)
		}
	}
	if w.Module != "" && module != w.Module {
		return fmt.Errorf("%s, not %s", got, w.Module)
	}
	// The route's text, with every upstream it names.
	text, _ := json.Marshal(rc)
	for _, name := range regexp.MustCompile(`"(?:upstream|failover)":"([^"]+)"`).FindAllStringSubmatch(string(text), -1) {
		if u := upstreamOf(cfg, name[1]); u != nil {
			b, _ := json.Marshal(u)
			text = append(text, b...)
		}
	}
	if len(w.Upstream) > 0 {
		if module != "proxy" {
			return fmt.Errorf("%s, not a proxy to %v", got, w.Upstream)
		}
		var addrs []string
		for _, name := range proxyUpstreams(params) {
			if u := upstreamOf(cfg, name); u != nil {
				list, _ := u["addresses"].([]any)
				for _, a := range list {
					s, _ := a.(string)
					addrs = append(addrs, s)
				}
			}
		}
		for _, a := range w.Upstream {
			if !slices.Contains(addrs, a) {
				return fmt.Errorf("%s, to %v, not %v", got, addrs, w.Upstream)
			}
		}
	}
	if w.Redirect != "" && !redirects(rc, w.Redirect) {
		return fmt.Errorf("%s, not a redirect to %s", got, w.Redirect)
	}
	for _, s := range w.Contains {
		if !strings.Contains(string(text), s) {
			return fmt.Errorf("%s, without %q", got, s)
		}
	}
	return nil
}

// proxyUpstreams returns the upstreams a proxy's params send requests to.
func proxyUpstreams(params map[string]any) []string {
	var out []string
	if s, _ := params["upstream"].(string); s != "" {
		out = append(out, s)
	}
	list, _ := params["upstreams"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		if s, _ := m["upstream"].(string); s != "" {
			out = append(out, s)
		}
		if s, _ := m["name"].(string); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// upstreamOf returns the upstream named name of cfg.
func upstreamOf(cfg map[string]any, name string) map[string]any {
	list, _ := cfg["upstreams"].([]any)
	for _, x := range list {
		if m, _ := x.(map[string]any); m["name"] == name {
			return m
		}
	}
	return nil
}

// redirects reports whether route rc redirects to a URL that holds to: a
// redirect middleware, or handler.
func redirects(rc map[string]any, to string) bool {
	refs, _ := rc["middleware"].([]any)
	refs = append(refs, rc["handler"])
	for _, x := range refs {
		m, _ := x.(map[string]any)
		params, _ := m["params"].(map[string]any)
		if t, _ := params["to"].(string); m["module"] == "redirect" && strings.Contains(t, to) {
			return true
		}
	}
	return false
}

// finding is a review's finding, as its answer writes one.
type finding struct {
	severity, member, line string
	fix                    bool
}

// findingLine is a finding's line: an item of a list, its severity, maybe
// bold or in brackets, then its member in backticks.
var findingLine = regexp.MustCompile("(?im)^[ \\t]*(?:[-*•]|\\d+[.)])[ \\t]*(?:\\*\\*|__|\\[)?[ \\t]*(high|medium|low|高|中|低)[ \\t]*(?:\\*\\*|__|\\])?[ \\t]*[:：-]?[ \\t]*`([^`\\n]+)`(.*)$")

// parseFindings returns the findings of a review's answer.
func parseFindings(answer string) []finding {
	var out []finding
	for _, m := range findingLine.FindAllStringSubmatch(answer, -1) {
		sev := strings.ToLower(m[1])
		switch sev {
		case "高":
			sev = "high"
		case "中":
			sev = "medium"
		case "低":
			sev = "low"
		}
		rest := strings.ToLower(m[3])
		out = append(out, finding{severity: sev, member: normPath(m[2]), line: strings.ToLower(m[0]),
			fix: strings.Contains(rest, "fix") || strings.Contains(rest, "修复")})
	}
	return out
}

// normPath returns a member's path as written in a finding, compared.
func normPath(p string) string {
	p = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), " ", ""))
	return strings.TrimPrefix(strings.TrimPrefix(p, "$"), ".")
}

// within reports whether path is want, or a member inside it.
func within(path, want string) bool {
	want = normPath(want)
	return path == want || strings.HasPrefix(path, want+".") || strings.HasPrefix(path, want+"[")
}

// top returns a path's top-level member.
func top(path string) string {
	if i := strings.IndexAny(path, ".["); i >= 0 {
		return path[:i]
	}
	return path
}

// matches reports whether fd names the planted problem p.
func (p evalFinding) matches(fd finding) bool {
	for _, m := range p.Members {
		if within(fd.member, m) {
			return true
		}
	}
	if len(p.Members) == 0 || top(fd.member) != top(normPath(p.Members[0])) {
		return false
	}
	for _, m := range p.Mentions {
		if strings.Contains(fd.line, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// TestEvalFindings: findings are read from an answer however a model
// formats the list, in English or Chinese, and a planted problem is found
// by its member, one inside it, or a mention under the same member.
func TestEvalFindings(t *testing.T) {
	answer := "Findings:\n\n" +
		"- **high** `routes[2]` (admin-panel): no auth. Fix: add basic_auth.\n" +
		"1. [medium] `admin.console.address`: everyone reaches it. Fix: listen on loopback.\n" +
		"* LOW: `Upstreams[1].response_timeout`: 24h. Fix: 60s.\n" +
		"- **高** `listeners[0]`：明文 HTTP。修复：重定向到 HTTPS。\n" +
		"- **medium** `routes[5]` (login) has no rate limit, nothing to change\n" +
		"- the `routes` look fine otherwise.\n"
	got := parseFindings(answer)
	if len(got) != 5 {
		t.Fatalf("%+v", got)
	}
	want := []finding{{severity: "high", member: "routes[2]", fix: true}, {severity: "medium", member: "admin.console.address", fix: true},
		{severity: "low", member: "upstreams[1].response_timeout", fix: true}, {severity: "high", member: "listeners[0]", fix: true},
		{severity: "medium", member: "routes[5]", fix: false}}
	for i, w := range want {
		if got[i].severity != w.severity || got[i].member != w.member || got[i].fix != w.fix {
			t.Errorf("%d: %+v, want %+v", i, got[i], w)
		}
	}
	for _, c := range []struct {
		p    evalFinding
		fd   int
		want bool
	}{
		{evalFinding{Members: []string{"routes[2]"}}, 0, true},
		{evalFinding{Members: []string{"routes[2].middleware"}}, 0, false},
		{evalFinding{Members: []string{"admin.console"}}, 1, true},
		{evalFinding{Members: []string{"admin"}}, 1, true},
		{evalFinding{Members: []string{"routes[20]"}}, 0, false},
		{evalFinding{Members: []string{"routes[7]"}, Mentions: []string{"Admin-Panel"}}, 0, true},
		{evalFinding{Members: []string{"upstreams[7]"}, Mentions: []string{"admin-panel"}}, 0, false},
		{evalFinding{Mentions: []string{"admin-panel"}}, 0, false},
		{evalFinding{Members: []string{"upstreams[1]"}}, 2, true},
	} {
		if got := c.p.matches(parseFindings(answer)[c.fd]); got != c.want {
			t.Errorf("%+v on %d: %v", c.p, c.fd, got)
		}
	}
}

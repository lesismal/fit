// Package mcpsdk checks fit's MCP server (admin.assistant.mcp) with the
// official Go MCP SDK's client: fit is built from this checkout and run,
// a token is made through the console's API as the workbench's MCP page
// makes it, and the SDK's client lists, reads, calls and proposes, in the
// stateless revision 2026-07-28 and in 2025-11-25's sessions. A module of
// its own, so that the SDK stays out of fit's dependencies; see
// docs/decisions/031-assistant.md.
package mcpsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/crypto/bcrypt"
)

// fitBin is the fit binary TestMain builds.
var fitBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fit-mcpsdk")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fitBin = filepath.Join(dir, "fit")
	// fit's own module, two directories up, built as fit builds.
	build := exec.Command("go", "build", "-C", "../..", "-o", fitBin, "./cmd/fit")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building fit:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// handedOut is every address freePort returned: the probe listener is
// closed before the address is used, so the kernel may give the same port
// to the next probe.
var handedOut struct {
	sync.Mutex
	addrs map[string]bool
}

// freePort returns a loopback address no one listens on now and that no
// earlier call returned.
func freePort(t *testing.T) string {
	t.Helper()
	for range 50 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		handedOut.Lock()
		taken := handedOut.addrs[addr]
		if !taken {
			if handedOut.addrs == nil {
				handedOut.addrs = make(map[string]bool)
			}
			handedOut.addrs[addr] = true
		}
		handedOut.Unlock()
		if !taken {
			return addr
		}
	}
	t.Fatal("no free port")
	return ""
}

// console is a fit with a console, logged in as ada.
type console struct {
	t    *testing.T
	url  string
	c    *http.Client
	csrf string
}

// startFit runs fit with a console, the assistant and its MCP server, and
// logs in.
func startFit(t *testing.T) *console {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	consoleAddr, adminAddr := freePort(t), freePort(t)
	doc := `{"listeners": [{"name": "web", "address": "` + freePort(t) + `"}],
"routes": [{"handler": {"module": "respond", "params": {"body": "one"}}}],
"admin": {"address": "` + adminAddr + `", "console": {"address": "` + consoleAddr + `", "users": [{"username": "ada", "password_hash": "` + string(hash) + `"}]},
  "assistant": {"users": [{"username": "ada", "level": "apply"}], "mcp": {}}}}`
	cfg := filepath.Join(t.TempDir(), "fit.json")
	if err := os.WriteFile(cfg, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fitBin, "run", "-c", cfg)
	cmd.Stdout, cmd.Stderr = io.Discard, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	jar, _ := cookiejar.New(nil)
	c := &console{t: t, url: "http://" + consoleAddr, c: &http.Client{Jar: jar, Timeout: 20 * time.Second}}
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		status, body := c.do("POST", "/api/login", `{"username":"ada","password":"pw"}`)
		if status == 200 {
			var s struct{ CSRF string }
			_ = json.Unmarshal([]byte(body), &s)
			c.csrf = s.CSRF
			return c
		}
		if time.Now().After(end) {
			t.Fatalf("the console did not start: %d %s", status, body)
		}
	}
}

func (c *console) do(method, path, body string) (int, string) {
	req, _ := http.NewRequest(method, c.url+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Fit-Csrf", c.csrf)
	resp, err := c.c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// token makes a token of ada's at level.
func (c *console) token(name, level string) string {
	c.t.Helper()
	status, body := c.do("POST", "/api/m/ai/mcp/tokens", `{"name":"`+name+`","level":"`+level+`","days":1}`)
	var b struct{ Token string }
	if status != 200 || json.Unmarshal([]byte(body), &b) != nil || b.Token == "" {
		c.t.Fatalf("a token: %d %s", status, body)
	}
	return b.Token
}

// bearer adds a token to every request, as a client's header setting does,
// and keeps each request's protocol version and session, and its answer.
type bearer struct {
	token string
	next  http.RoundTripper

	mu   sync.Mutex
	seen []string
}

func (b *bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	resp, err := b.next.RoundTrip(r)
	line := r.Method + " version=" + r.Header.Get("Mcp-Protocol-Version") + " method=" + r.Header.Get("Mcp-Method")
	if r.Header.Get("Mcp-Session-Id") != "" {
		line += " session"
	}
	if err == nil {
		line += fmt.Sprint(" ", resp.StatusCode)
	}
	b.mu.Lock()
	b.seen = append(b.seen, line)
	b.mu.Unlock()
	return resp, err
}

func (b *bearer) requests() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.seen, "\n")
}

// connect connects the SDK's client to fit's /mcp with token, in version
// ("" for the SDK's latest), and returns the session and what its requests
// were.
func (c *console) connect(ctx context.Context, token, version string) (*mcp.ClientSession, *bearer) {
	c.t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "fit-sdk-test", Version: "1"}, nil)
	rt := &bearer{token: token, next: http.DefaultTransport}
	tr := &mcp.StreamableClientTransport{Endpoint: c.url + "/mcp", MaxRetries: -1,
		HTTPClient: &http.Client{Transport: rt, Timeout: 60 * time.Second}}
	var opts *mcp.ClientSessionOptions
	if version != "" {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: version}
	}
	s, err := client.Connect(ctx, tr, opts)
	if err != nil {
		c.t.Fatalf("connect %s: %v", version, err)
	}
	c.t.Cleanup(func() { _ = s.Close() })
	return s, rt
}

func text(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// TestSDK: the SDK's client, in the stateless revision and in a legacy
// session, lists fit's tools, prompts and resources, reads its config and
// status, and with a propose token makes a proposal the workbench applies
// only after its confirm; a read token is offered no propose tool.
func TestSDK(t *testing.T) {
	c := startFit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	propose := c.token("sdk", "propose")
	read := c.token("sdk-read", "read")

	for _, version := range []string{"", "2025-11-25", "2025-06-18"} {
		s, rt := c.connect(ctx, propose, version)
		tools, err := s.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("%q ListTools: %v", version, err)
		}
		names := map[string]bool{}
		for _, tool := range tools.Tools {
			names[tool.Name] = true
		}
		for _, n := range []string{"config_get", "status_get", "schema_describe", "config_propose", "skill_load"} {
			if !names[n] {
				t.Errorf("%q: no tool %s in %v", version, n, names)
			}
		}
		if names["ask_user"] {
			t.Errorf("%q: ask_user is served", version)
		}
		prompts, err := s.ListPrompts(ctx, nil)
		if err != nil || len(prompts.Prompts) == 0 {
			t.Fatalf("%q ListPrompts: %v %v", version, prompts, err)
		}
		p, err := s.GetPrompt(ctx, &mcp.GetPromptParams{Name: "explain"})
		if err != nil || len(p.Messages) != 1 {
			t.Fatalf("%q GetPrompt: %v %v", version, p, err)
		}
		res, err := s.ListResources(ctx, nil)
		if err != nil || len(res.Resources) < 5 {
			t.Fatalf("%q ListResources: %v %v", version, res, err)
		}
		tpl, err := s.ListResourceTemplates(ctx, nil)
		if err != nil || len(tpl.ResourceTemplates) < 2 {
			t.Fatalf("%q ListResourceTemplates: %v %v", version, tpl, err)
		}
		cfg, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "fit://config/base"})
		if err != nil || len(cfg.Contents) != 1 || !strings.Contains(cfg.Contents[0].Text, `"one"`) || strings.Contains(cfg.Contents[0].Text, "$2a$") {
			t.Fatalf("%q the config: %v %v", version, cfg, err)
		}
		if _, err := s.ReadResource(ctx, &mcp.ReadResourceParams{URI: "fit://docs/reference/listeners"}); err != nil {
			t.Errorf("%q a chapter of the reference: %v", version, err)
		}
		st, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "status_get", Arguments: map[string]any{}})
		if err != nil || st.IsError || !strings.Contains(text(st), "listeners") {
			t.Fatalf("%q status_get: %v %v", version, st, err)
		}
		// The era the client spoke: the stateless revision from its
		// first request, or a session of the version asked for.
		seen := rt.requests()
		switch version {
		case "":
			if !strings.HasPrefix(seen, "POST version=2026-07-28 method=server/discover 200") || strings.Contains(seen, "session") ||
				strings.Contains(seen, " 4") {
				t.Errorf("the stateless revision's requests:\n%s", seen)
			}
		default:
			if !strings.Contains(seen, "version="+version) || !strings.Contains(seen, "session 200") || strings.Contains(seen, "2026-07-28") {
				t.Errorf("%s's requests:\n%s", version, seen)
			}
		}
	}

	// A proposal: it shows in the workbench, and is applied only there.
	s, _ := c.connect(ctx, propose, "")
	r, err := s.CallTool(ctx, &mcp.CallToolParams{Name: "config_propose", Arguments: map[string]any{"summary": "answer two",
		"ops": []map[string]any{{"op": "replace", "path": "/routes/0/handler/params/body", "value": "two"}}}})
	if err != nil || r.IsError || !strings.Contains(text(r), `"valid":true`) || !strings.Contains(text(r), "#/ai/proposals") {
		t.Fatalf("config_propose: %v %v", text(r), err)
	}
	_, body := c.do("GET", "/api/m/ai/proposals", "")
	var list struct {
		Proposals []struct{ Conversation, Title string }
	}
	if json.Unmarshal([]byte(body), &list) != nil || len(list.Proposals) != 1 || list.Proposals[0].Title != "MCP: sdk" {
		t.Fatalf("the workbench's proposals: %s", body)
	}
	_, before := c.do("GET", "/api/m/admin/versions", "")
	conv := list.Proposals[0].Conversation
	_, body = c.do("GET", "/api/m/ai/proposals/"+conv, "")
	var got struct {
		Proposal struct {
			ID       string `json:"id"`
			Revision int    `json:"revision"`
			ETag     string `json:"etag"`
		} `json:"proposal"`
	}
	_ = json.Unmarshal([]byte(body), &got)
	apply, _ := json.Marshal(map[string]any{"id": got.Proposal.ID, "revision": got.Proposal.Revision, "etag": got.Proposal.ETag})
	if status, body := c.do("POST", "/api/m/ai/proposals/"+conv+"/apply", string(apply)); status != 200 || !strings.Contains(body, `"outcome":"applied"`) {
		t.Fatalf("apply: %d %s", status, body)
	}
	_, after := c.do("GET", "/api/m/admin/versions", "")
	if strings.Count(after, `"version"`) != strings.Count(before, `"version"`)+1 {
		t.Errorf("the versions before %s and after %s", before, after)
	}

	// A read token is offered no propose tool.
	rs, _ := c.connect(ctx, read, "")
	tools, err := rs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if strings.HasSuffix(tool.Name, "_propose") {
			t.Errorf("a read token is offered %s", tool.Name)
		}
	}
}

// TestRefused: a wrong token is refused before anything is served.
func TestRefused(t *testing.T) {
	c := startFit(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "fit-sdk-test", Version: "1"}, nil)
	tr := &mcp.StreamableClientTransport{Endpoint: c.url + "/mcp", MaxRetries: -1,
		HTTPClient: &http.Client{Transport: &bearer{token: "fitmcp_00_wrong", next: http.DefaultTransport}, Timeout: 20 * time.Second}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if s, err := client.Connect(ctx, tr, nil); err == nil {
		_ = s.Close()
		t.Fatal("a wrong token connected")
	}
}

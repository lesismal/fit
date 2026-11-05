package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/lesismal/fit/internal/linktoken"
)

// tokenFake is an Admin API's /tokens endpoints, answering as fit does.
type tokenFake struct {
	srv  *httptest.Server
	mu   sync.Mutex
	last struct {
		method, path string
		body         []byte
	}
}

func newTokenFake(t *testing.T) *tokenFake {
	f := &tokenFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.last.method, f.last.path, f.last.body = r.Method, r.URL.Path, body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		view := linktoken.View{ID: "0123456789abcdef", Name: "lab", ExpiresAt: 1893456000, LastUsed: 1793456000, LastAddr: "10.0.0.1:5",
			Members: []string{"node/lab"}, Links: []linktoken.LinkView{{Member: "node/lab"}}}
		switch {
		case r.URL.Path == "/tokens/nope":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error": "no role nope runs here"}`))
		case r.URL.Path == "/tokens" && r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"roles": map[string][]linktoken.View{"tunnel.hub": {view, {ID: "1111111111111111", Name: "x", ExpiresAt: 1, Expired: true}}}})
		case r.Method == "GET":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": []linktoken.View{view}})
		case r.Method == "POST" && strings.Contains(string(body), `"bad"`):
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST":
			_ = json.NewEncoder(w).Encode(linktoken.Issued{Token: "fitt1.0123456789abcdef.secret", View: view})
		case r.Method == "PATCH":
			_ = json.NewEncoder(w).Encode(view)
		default:
			_, _ = w.Write([]byte(`{"ok": true}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *tokenFake) boot(t *testing.T) string {
	u, _ := url.Parse(f.srv.URL)
	return writeCheckBoot(t, `{"address": `+js(u.Host)+`}`)
}

func runFitToken(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := run(append([]string{"token"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestToken(t *testing.T) {
	f := newTokenFake(t)
	boot := f.boot(t)

	code, out, errOut := runFitToken("create", "-c", boot, "-role", "tunnel.hub", "-name", "lab", "-ttl", "30d", "-note", "n")
	if code != 0 || strings.TrimSpace(out) != "fitt1.0123456789abcdef.secret" || !strings.Contains(errOut, "shown this once") {
		t.Fatalf("create: %d %q %q", code, out, errOut)
	}
	if f.last.method != "POST" || f.last.path != "/tokens/tunnel.hub" || !strings.Contains(string(f.last.body), `"ttl":"30d"`) {
		t.Fatalf("create sent %+v", f.last)
	}
	code, out, _ = runFitToken("create", "-c", boot, "-role", "tunnel.hub", "-name", "x", "-expires", "2030-01-01T00:00:00Z")
	if code != 0 || !strings.Contains(string(f.last.body), `"expires_at":1893456000`) {
		t.Fatalf("create with -expires: %d %s %s", code, out, f.last.body)
	}
	if code, _, errOut = runFitToken("create", "-c", boot, "-role", "tunnel.hub", "-name", "bad"); code != 1 || !strings.Contains(errOut, "not a token") {
		t.Fatalf("create, a bad answer: %d %s", code, errOut)
	}

	code, out, _ = runFitToken("list", "-c", boot)
	if code != 0 || !strings.Contains(out, "0123456789abcdef") || !strings.Contains(out, "node/lab") || !strings.Contains(out, "(expired)") {
		t.Fatalf("list: %d %s", code, out)
	}
	code, out, _ = runFitToken("list", "-c", boot, "-role", "llm.hub")
	if code != 0 || !strings.Contains(out, "llm.hub") || f.last.path != "/tokens/llm.hub" {
		t.Fatalf("list a role: %d %s", code, out)
	}
	code, out, _ = runFitToken("list", "-c", boot, "-json")
	if code != 0 || !strings.Contains(out, `"roles"`) {
		t.Fatalf("list -json: %d %s", code, out)
	}
	code, out, errOut = runFitToken("list", "-c", boot, "-role", "nope")
	if code != 1 || !strings.Contains(errOut, "no role nope runs here") {
		t.Fatalf("list of no role: %d %s %s", code, out, errOut)
	}

	code, out, _ = runFitToken("expiry", "-c", boot, "-role", "tunnel.hub", "-id", "0123456789abcdef", "-never")
	if code != 0 || f.last.method != "PATCH" || f.last.path != "/tokens/tunnel.hub/0123456789abcdef" || !strings.Contains(out, "expires") {
		t.Fatalf("expiry: %d %s %+v", code, out, f.last)
	}
	code, out, _ = runFitToken("revoke", "-c", boot, "-role", "tunnel.hub", "-id", "0123456789abcdef")
	if code != 0 || f.last.method != "DELETE" || !strings.Contains(out, "revoked") {
		t.Fatalf("revoke: %d %s", code, out)
	}
}

func TestTokenUsage(t *testing.T) {
	boot := newTokenFake(t).boot(t)
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{nil, 2, "Usage: fit token"},
		{[]string{"-h"}, 0, "Usage: fit token"},
		{[]string{"nope"}, 2, "unknown command"},
		{[]string{"list", "-bogus"}, 2, "flag provided but not defined"},
		{[]string{"list", "x"}, 2, "unexpected argument"},
		{[]string{"create", "-name", "a"}, 2, "-role is required"},
		{[]string{"create", "-role", "tunnel.hub"}, 2, "-name is required"},
		{[]string{"revoke", "-role", "tunnel.hub"}, 2, "-id is required"},
		{[]string{"expiry", "-role", "r", "-id", "i"}, 2, "give -ttl, -expires or -never"},
		{[]string{"expiry", "-role", "r", "-id", "i", "-never", "-ttl", "1h"}, 2, "-never takes no"},
		{[]string{"create", "-role", "r", "-name", "a", "-ttl", "1h", "-expires", "x"}, 2, "not both"},
		{[]string{"create", "-role", "r", "-name", "a", "-ttl", "soon"}, 2, "not a positive duration"},
		{[]string{"create", "-c", boot, "-role", "r", "-name", "a", "-expires", "tomorrow"}, 2, "RFC 3339"},
		{[]string{"list", "-c", "/nonexistent/fit.json"}, 2, "fit token"},
		{[]string{"list", "-c", writeCheckBoot(t, `{"address": "127.0.0.1:1", "token_file": "/nonexistent"}`)}, 2, "token_file"},
	} {
		code, _, errOut := runFitToken(tc.args...)
		if code != tc.code || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: %d %q, want %d %q", tc.args, code, errOut, tc.code, tc.want)
		}
	}
	var out bytes.Buffer
	printTokenList(&out, nil)
	if !strings.Contains(out.String(), "no role") {
		t.Fatal(out.String())
	}
}

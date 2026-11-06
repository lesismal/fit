package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
)

// fakeIssuer is a tunnel.hub that issues link tokens, kept in its member as
// the real hub keeps them.
type fakeIssuer struct {
	reg *linktoken.Registry
	m   *linktoken.Manager
}

func (r *fakeIssuer) Apply(member any) error {
	r.reg.Set(member.(*config.TunnelHub).Tokens)
	return nil
}

func (r *fakeIssuer) Stop(context.Context)           { r.reg.Stop() }
func (r *fakeIssuer) LinkTokens() *linktoken.Manager { return r.m }

func init() {
	app.Register(app.Info{
		ID: "tunnel.hub",
		Member: func(c *config.Config) any {
			if c.Tunnel == nil {
				return nil
			}
			return c.Tunnel.Hub
		},
		New: func(h *app.Host) (app.Role, error) {
			r := &fakeIssuer{reg: &linktoken.Registry{}}
			r.m = &linktoken.Manager{Role: h.ID, Reg: r.reg, Log: h.Admin, Edit: func(ctx context.Context, change func([]config.LinkToken) ([]config.LinkToken, error)) error {
				done := make(chan error, 1)
				h.Edit(func(cur []byte) ([]byte, error) {
					var m map[string]json.RawMessage
					if err := json.Unmarshal(cur, &m); err != nil {
						return nil, err
					}
					var ts []config.LinkToken
					_ = json.Unmarshal(m["tokens"], &ts)
					ts, err := change(ts)
					if err != nil {
						return nil, err
					}
					m["tokens"], _ = json.Marshal(ts)
					return json.Marshal(m)
				}, func(_ []string, err error) { done <- err })
				select {
				case err := <-done:
					return err
				case <-ctx.Done():
					return ctx.Err()
				}
			}}
			return r, nil
		},
	})
}

// The Admin API issues, lists, changes and revokes a role's tokens, through
// the config, and names a role that is not running or issues none.
func TestTokensAPI(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	send := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, f.api+path, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, got := f.do(t, req)
		return resp.StatusCode, got
	}
	if code, got := send("GET", "/tokens/tunnel.hub", ""); code != http.StatusNotFound || !strings.Contains(got, "no role runs") {
		t.Fatalf("without apps: %d %s", code, got)
	}
	m := app.NewManager(f.server.Engine(), nil, nil, f.configs.EditMember)
	t.Cleanup(func() { m.Stop(context.Background()) })
	f.configs.UseApps(m)
	with := strings.TrimSuffix(doc("one"), "}") + `, "tunnel": {"hub": {}, "node": {}}}`
	if _, err := f.configs.Apply([]byte(with), SourceAdmin); err != nil {
		t.Fatal(err)
	}

	if code, got := send("GET", "/tokens/llm.hub", ""); code != http.StatusNotFound {
		t.Fatalf("a role not running: %d %s", code, got)
	}
	if code, got := send("GET", "/tokens/tunnel.node", ""); code != http.StatusBadRequest || !strings.Contains(got, "issues no link tokens") {
		t.Fatalf("a role without tokens: %d %s", code, got)
	}
	req, _ := http.NewRequest("POST", f.api+"/tokens/tunnel.hub", strings.NewReader("{}"))
	if resp, got := f.do(t, req); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("no content type: %d %s", resp.StatusCode, got)
	}
	if code, got := send("POST", "/tokens/tunnel.hub", "{"); code != http.StatusBadRequest {
		t.Fatalf("bad JSON: %d %s", code, got)
	}
	if code, got := send("POST", "/tokens/tunnel.hub", `{"name": ""}`); code != http.StatusBadRequest {
		t.Fatalf("no name: %d %s", code, got)
	}
	code, got := send("POST", "/tokens/tunnel.hub", `{"name": "lab", "ttl": "30d"}`)
	if code != http.StatusOK {
		t.Fatalf("issue: %d %s", code, got)
	}
	var is linktoken.Issued
	if err := json.Unmarshal([]byte(got), &is); err != nil || !strings.HasPrefix(is.Token, config.LinkTokenPrefix) {
		t.Fatalf("issued %s", got)
	}
	cur := string(f.configs.Current().Data())
	if strings.Contains(cur, is.Token) || !strings.Contains(cur, is.ID) {
		t.Fatalf("the config keeps %s", cur)
	}
	if code, got := send("GET", "/tokens", ""); code != http.StatusOK || !strings.Contains(got, `"tunnel.hub":[{"id":"`+is.ID) {
		t.Fatalf("all: %d %s", code, got)
	}
	if code, got := send("GET", "/tokens/tunnel.hub", ""); code != http.StatusOK || !strings.Contains(got, is.ID) {
		t.Fatalf("list: %d %s", code, got)
	}
	if code, got := send("PATCH", "/tokens/tunnel.hub/"+is.ID, `{"ttl": "1h"}`); code != http.StatusOK {
		t.Fatalf("expiry: %d %s", code, got)
	}
	if code, got := send("PATCH", "/tokens/tunnel.hub/0000000000000000", `{}`); code != http.StatusNotFound {
		t.Fatalf("expiry of none: %d %s", code, got)
	}
	if code, got := send("PATCH", "/tokens/tunnel.hub/"+is.ID, `nope`); code != http.StatusBadRequest {
		t.Fatalf("expiry bad JSON: %d %s", code, got)
	}
	if code, got := send("PATCH", "/tokens/tunnel.node/x", `{}`); code != http.StatusBadRequest {
		t.Fatalf("expiry of a role without tokens: %d %s", code, got)
	}
	if code, got := send("DELETE", "/tokens/tunnel.hub/"+is.ID, ""); code != http.StatusOK {
		t.Fatalf("revoke: %d %s", code, got)
	}
	if code, got := send("DELETE", "/tokens/tunnel.hub/"+is.ID, ""); code != http.StatusNotFound {
		t.Fatalf("revoke twice: %d %s", code, got)
	}
	if code, got := send("DELETE", "/tokens/tunnel.node/x", ""); code != http.StatusBadRequest {
		t.Fatalf("revoke of a role without tokens: %d %s", code, got)
	}
	if code, got := send("POST", "/tokens/tunnel.node", `{}`); code != http.StatusBadRequest {
		t.Fatalf("issue at a role without tokens: %d %s", code, got)
	}
	if strings.Contains(string(f.configs.Current().Data()), is.ID) {
		t.Fatal("the revoked token stayed in the config")
	}
}

func TestTokenError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{app.ErrBusy, http.StatusServiceUnavailable},
		{context.DeadlineExceeded, http.StatusServiceUnavailable},
		{&config.Error{Msg: "x"}, http.StatusBadRequest},
		{config.Errors{&config.Error{}}, http.StatusBadRequest},
		{linktoken.ErrNotFound, http.StatusNotFound},
		{context.Canceled, http.StatusInternalServerError},
	} {
		rec := &recorder{header: http.Header{}}
		tokenError(rec, tc.err)
		if rec.status != tc.want {
			t.Errorf("%v: %d, want %d", tc.err, rec.status, tc.want)
		}
	}
}

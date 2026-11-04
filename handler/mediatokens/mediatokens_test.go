package mediatokens

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

const waitTimeout = 10 * time.Second

// oldSecret is the test's first secret, a file's content, 32 bytes or more.
const oldSecret = "the media tokens' first secret, 32 bytes or more"

// fit is fit serving a config, as it does: compiled, swapped in and served.
type fit struct {
	env        runtime.Env
	rt         *runtime.Runtime
	server     *listener.Server
	base       string
	secretFile string            // a single-key config's file
	keyFiles   map[string]string // a keyring config's key -> file
}

// startFit serves a config with media_tokens on prefix and below, its params
// params, and a media_auth policy "live" whose secret_file is secretFile.
func startFit(t *testing.T, secretFile, prefix, params string) *fit {
	t.Helper()
	f := startDoc(t, conf(secretFile, prefix, params))
	f.secretFile = secretFile
	return f
}

// startDoc serves doc as it is, with maybe a handler pool hook.
func startDoc(t *testing.T, doc string) *fit {
	t.Helper()
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{env: runtime.Env{Metrics: observe.NewMetrics(), Engine: up}, rt: new(runtime.Runtime)}
	f.swap(t, doc)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = f.server.Close()
		f.rt.Close()
		stopUp()
	})
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	return f
}

// swap compiles doc and swaps it in, as a reload does.
func (f *fit) swap(t *testing.T, doc string) {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, f.env)
	if err != nil {
		t.Fatal(err)
	}
	f.rt.Swap(g)
}

// conf is a config with media_tokens on prefix and below, and a policy
// "live" signed by secretFile, whose token_ttl is 1h.
func conf(secretFile, prefix, params string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["` + prefix + `", "` + prefix + `/*", "/elsewhere"]},
    "handler": {"module": "media_tokens", "params": ` + params + `}}],
  "media_auth": [{"name": "live", "secret_file": "` + secretFile + `", "token_ttl": "1h"}]
}`
}

// newSecretFile makes a file holding oldSecret, and returns its path.
func newSecretFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "media-secret")
	if err := os.WriteFile(path, []byte(oldSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// send sends a request and returns its response and body, without failing a
// test, for a goroutine to use.
func send(method, url, body string, hdr map[string]string) (*http.Response, string, error) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		return nil, "", err
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: waitTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return resp, string(b), err
}

// do sends a request and returns its response and body.
func do(t *testing.T, method, url, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	resp, b, err := send(method, url, body, hdr)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// issue posts a batch to f's prefix.
func issue(t *testing.T, f *fit, tokens string) (*http.Response, string) {
	t.Helper()
	return do(t, "POST", f.base+"/media-tokens", `{"tokens": [`+tokens+`]}`,
		map[string]string{"Content-Type": "application/json"})
}

// revoke posts a revoke to f's prefix.
func revoke(t *testing.T, f *fit) (*http.Response, string) {
	t.Helper()
	return do(t, "POST", f.base+"/media-tokens/revoke", "", nil)
}

// mustIssue is issue, failing the test unless it is answered 200, and
// returning the batch's tokens.
func mustIssue(t *testing.T, f *fit, tokens string) []tokenAnswer {
	t.Helper()
	resp, body := issue(t, f, tokens)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issue: %d %s", resp.StatusCode, body)
	}
	var out issueAnswer
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("issue: %v: %s", err, body)
	}
	return out.Tokens
}

// handler returns the compiled handler, for the pool hook the revoke tests
// set.
func (f *fit) handler(t *testing.T) *Tokens {
	t.Helper()
	g := f.rt.Current()
	defer g.Release()
	for _, l := range g.Listeners {
		for _, r := range l.Routes {
			if h, ok := r.Handler.(*Tokens); ok {
				return h
			}
		}
	}
	t.Fatal("no media_tokens handler")
	return nil
}

// policy returns the running policy named name.
func (f *fit) policy(t *testing.T, name string) *mediaauth.Policy {
	t.Helper()
	p := mediaauth.Lookup(f.env.Engine, name)
	if p == nil {
		t.Fatalf("no policy %q", name)
	}
	return p
}

// fromFile returns a policy rebuilt from f's secret file, as a restart does.
func (f *fit) fromFile(t *testing.T) *mediaauth.Policy {
	t.Helper()
	p, err := mediaauth.New(&config.MediaAuth{Name: "live", SecretFile: f.secretFile})
	if err != nil {
		t.Fatalf("a policy from the secret file: %v", err)
	}
	return p
}

// allowed reports the error of tok on stream for action.
func allowed(p *mediaauth.Policy, action mediaauth.Action, stream, tok string) error {
	_, err := p.Allow(action, stream, mediaauth.Credentials{Token: tok})
	return err
}

// near fails unless got, a unix second, is within tolerance of want.
func near(t *testing.T, got, want int64, tolerance time.Duration) {
	t.Helper()
	delta := time.Duration(got-want) * time.Second
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		t.Fatalf("expires %d, want %d (a difference of %v, past %v)", got, want, delta, tolerance)
	}
}

// TestIssue: a batch of two different subjects and lifetimes is answered in
// order, and both tokens are accepted by the policy for the streams they
// name and refused for others; a token without a ttl gets the policy's
// token_ttl.
func TestIssue(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	now := time.Now().Unix()
	tokens := mustIssue(t, f,
		`{"subject": "alice@mtg-42", "publish": ["mtg-42-*"], "play": ["mtg-42-*"], "ttl": "2h"},`+
			`{"subject": "bob@mtg-43", "publish": ["mtg-43-*"], "play": ["mtg-43-*"]}`)
	if len(tokens) != 2 {
		t.Fatalf("%d tokens, want 2", len(tokens))
	}
	if tokens[0].Subject != "alice@mtg-42" || tokens[1].Subject != "bob@mtg-43" {
		t.Fatalf("subjects %q, %q", tokens[0].Subject, tokens[1].Subject)
	}
	if !strings.HasPrefix(tokens[0].Token, mediaauth.TokenPrefix) || !strings.HasPrefix(tokens[1].Token, mediaauth.TokenPrefix) {
		t.Fatalf("tokens %q, %q", tokens[0].Token, tokens[1].Token)
	}
	near(t, tokens[0].Expires, now+int64((2*time.Hour).Seconds()), 5*time.Second)
	near(t, tokens[1].Expires, now+int64(time.Hour.Seconds()), 5*time.Second)

	p := f.policy(t, "live")
	for _, tc := range []struct {
		what   string
		action mediaauth.Action
		stream string
		tok    string
		err    error
	}{
		{"alice publishes mtg-42-1", mediaauth.Publish, "mtg-42-1", tokens[0].Token, nil},
		{"alice plays mtg-42-1", mediaauth.Play, "mtg-42-1", tokens[0].Token, nil},
		{"alice may not play mtg-43-1", mediaauth.Play, "mtg-43-1", tokens[0].Token, mediaauth.ErrForbidden},
		{"bob publishes mtg-43-1", mediaauth.Publish, "mtg-43-1", tokens[1].Token, nil},
		{"bob may not publish mtg-42-1", mediaauth.Publish, "mtg-42-1", tokens[1].Token, mediaauth.ErrForbidden},
	} {
		if err := allowed(p, tc.action, tc.stream, tc.tok); err != tc.err {
			t.Errorf("%s: %v, want %v", tc.what, err, tc.err)
		}
	}
}

// TestIssueValidation: every bad request of a batch is answered 400, and a
// batch with one bad token issues none.
func TestIssueValidation(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	valid := `{"subject": "alice@mtg-42", "publish": ["mtg-42-*"], "play": ["mtg-42-*"]}`
	for _, tc := range []struct {
		what string
		body string
	}{
		{"an empty body", ``},
		{"a body that is not JSON", `not json`},
		{"no tokens", `{}`},
		{"an empty batch", `{"tokens": []}`},
		{"an empty subject", `{"tokens": [{"subject": "", "publish": ["mtg-42-*"]}]}`},
		{"a subject with a control character", `{"tokens": [{"subject": "a\nb", "publish": ["mtg-42-*"]}]}`},
		{"a subject past 128 bytes", `{"tokens": [{"subject": "` + strings.Repeat("a", 129) + `", "publish": ["mtg-42-*"]}]}`},
		{"no patterns", `{"tokens": [{"subject": "alice@mtg-42"}]}`},
		{"a bad pattern", `{"tokens": [{"subject": "alice@mtg-42", "publish": ["mtg 42"]}]}`},
		{"a bad ttl", `{"tokens": [{"subject": "alice@mtg-42", "publish": ["mtg-42-*"], "ttl": "soon"}]}`},
		{"a negative ttl", `{"tokens": [{"subject": "alice@mtg-42", "publish": ["mtg-42-*"], "ttl": "-1h"}]}`},
		{"one bad token of two", `{"tokens": [` + valid + `, {"subject": "", "publish": ["mtg-42-*"]}]}`},
	} {
		resp, body := do(t, "POST", f.base+"/media-tokens", tc.body, map[string]string{"Content-Type": "application/json"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.what, resp.StatusCode, body)
		}
		if strings.Contains(body, mediaauth.TokenPrefix) {
			t.Errorf("%s: a refused batch answered a token: %s", tc.what, body)
		}
	}
}

// TestIssueLimits: a ttl past max_ttl, more tokens than max_tokens and a
// body past max_body_bytes are refused.
func TestIssueLimits(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live", "max_ttl": "1h", "max_tokens": 2, "max_body_bytes": 256}`)
	if resp, body := issue(t, f, `{"subject": "a", "publish": ["mtg-*"], "ttl": "2h"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a ttl past max_ttl: %d %s", resp.StatusCode, body)
	}
	if n := len(mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"], "ttl": "1h"}`)); n != 1 {
		t.Fatalf("%d tokens", n)
	}
	three := `{"subject": "a", "publish": ["mtg-*"]},{"subject": "b", "publish": ["mtg-*"]},{"subject": "c", "publish": ["mtg-*"]}`
	if resp, body := issue(t, f, three); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("more tokens than max_tokens: %d %s", resp.StatusCode, body)
	}
	// A body whose length is known, and one that is chunked, both past the
	// limit.
	big := `{"tokens": [` + strings.Repeat(" ", 300) + `]}`
	if resp, body := do(t, "POST", f.base+"/media-tokens", big, nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body past max_body_bytes: %d %s", resp.StatusCode, body)
	}
	req, err := http.NewRequest("POST", f.base+"/media-tokens",
		io.MultiReader(strings.NewReader(`{"tokens": [`), strings.NewReader(strings.Repeat(" ", 1000))))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("a chunked body past max_body_bytes: %d", resp.StatusCode)
	}
}

// TestIssuePrefixes: every publish and play pattern must start with one of
// prefixes.
func TestIssuePrefixes(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live", "prefixes": ["mtg-"]}`)
	if n := len(mustIssue(t, f, `{"subject": "a", "publish": ["mtg-42-*"], "play": ["mtg-42-*"]}`)); n != 1 {
		t.Fatalf("%d tokens", n)
	}
	for _, tc := range []struct{ what, tokens string }{
		{"a publish pattern outside the prefixes", `{"subject": "a", "publish": ["other-*"]}`},
		{"a play pattern outside the prefixes", `{"subject": "a", "publish": ["mtg-42-*"], "play": ["other-*"]}`},
	} {
		if resp, body := issue(t, f, tc.tokens); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.what, resp.StatusCode, body)
		}
	}
}

// TestMethodsAndPaths: GET and other methods are 405 with Allow, and a path
// outside the prefix is 404.
func TestMethodsAndPaths(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/media-tokens", http.StatusMethodNotAllowed},
		{"PUT", "/media-tokens", http.StatusMethodNotAllowed},
		{"GET", "/media-tokens/revoke", http.StatusMethodNotAllowed},
		{"DELETE", "/media-tokens/revoke", http.StatusMethodNotAllowed},
		{"GET", "/media-tokens/elsewhere", http.StatusNotFound},
		{"POST", "/media-tokens/", http.StatusNotFound},
		{"GET", "/elsewhere", http.StatusNotFound},
	} {
		resp, body := do(t, tc.method, f.base+tc.path, "", nil)
		if resp.StatusCode != tc.want {
			t.Errorf("%s %s: %d %s, want %d", tc.method, tc.path, resp.StatusCode, body, tc.want)
		}
		if tc.want == http.StatusMethodNotAllowed && resp.Header.Get("Allow") != "POST" {
			t.Errorf("%s %s: Allow %q", tc.method, tc.path, resp.Header.Get("Allow"))
		}
	}
}

// TestCustomPrefix: the route serves under its prefix, not under the
// default one.
func TestCustomPrefix(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/tokens", `{"auth": "live", "prefix": "/tokens"}`)
	resp, body := do(t, "POST", f.base+"/tokens", `{"tokens": [{"subject": "a", "publish": ["mtg-*"]}]}`,
		map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the configured prefix: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do(t, "POST", f.base+"/media-tokens", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("the default prefix is served past the route: %d", resp.StatusCode)
	}
}

// TestProvisionNeedsSecretFile: a policy with secret but no secret_file, or
// no auth, is a config error.
func TestProvisionNeedsSecretFile(t *testing.T) {
	for _, tc := range []struct{ what, params, mediaAuth, want string }{
		{"auth left out", `{}`, `[{"name": "live", "secret_file": "` + newSecretFile(t) + `"}]`,
			"media_tokens needs a media_auth policy with secret_file, or a keyring of secret_files: auth names none"},
		{"a policy with secret", `{"auth": "live"}`, `[{"name": "live", "secret": "0123456789abcdef-secret"}]`,
			"media_tokens needs a media_auth policy with secret_file, or a keyring of secret_files"},
		{"a policy that does not exist", `{"auth": "nope"}`, `[]`,
			`there is no media_auth policy named "nope"`},
	} {
		doc := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "media_tokens", "params": ` + tc.params + `}}],
  "media_auth": ` + tc.mediaAuth + `}`
		c, err := config.Load([]byte(doc))
		if err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		if _, err = runtime.Compile(c, runtime.Env{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.what, err, tc.want)
		}
	}
}

// TestPoolClose: a closed pool refuses a submit rather than blocking a
// worker, as Cleanup leaves it.
func TestPoolClose(t *testing.T) {
	p := newPool()
	if !p.submit(func() {}) {
		t.Fatal("a new pool refused a job")
	}
	p.close()
	p.close() // idempotent
	// Wait for the worker to take the job and end.
	deadline := time.Now().Add(waitTimeout)
	for p.submit(func() {}) {
		if time.Now().After(deadline) {
			t.Fatal("a closed pool took a job")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestConcurrentIssueRevoke issues and revokes at once, under the race
// detector.
func TestConcurrentIssueRevoke(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)
	const n = 8
	var wg sync.WaitGroup
	type result struct {
		code int
		body string
		err  error
	}
	results := make([]result, 2*n)
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, body, err := send("POST", f.base+"/media-tokens",
				`{"tokens": [{"subject": "a", "publish": ["mtg-*"]}]}`,
				map[string]string{"Content-Type": "application/json"})
			if err != nil {
				results[2*i] = result{err: err}
				return
			}
			results[2*i] = result{code: resp.StatusCode, body: body}
		}()
		go func() {
			defer wg.Done()
			resp, body, err := send("POST", f.base+"/media-tokens/revoke", "", nil)
			if err != nil {
				results[2*i+1] = result{err: err}
				return
			}
			results[2*i+1] = result{code: resp.StatusCode, body: body}
		}()
	}
	wg.Wait()
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d: %v", i, r.err)
		}
		switch {
		case i%2 == 0 && r.code != http.StatusOK:
			t.Errorf("issue %d: %d %s", i/2, r.code, r.body)
		case i%2 == 1 && r.code != http.StatusOK && r.code != http.StatusServiceUnavailable:
			t.Errorf("revoke %d: %d %s", i/2, r.code, r.body)
		}
	}
}

// newRingFiles writes a keyring's secret files, k1 and k2, and returns them
// by key.
func newRingFiles(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{}
	for i, key := range []string{"k1", "k2"} {
		path := filepath.Join(dir, "media-secret-"+key)
		if err := os.WriteFile(path, []byte(strings.Repeat(string(rune('a'+i)), 32)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		files[key] = path
	}
	return files
}

// startRing serves a config whose policy live has a keyring of k1 and k2,
// each with its own secret file.
func startRing(t *testing.T, prefix, params string) *fit {
	t.Helper()
	files := newRingFiles(t)
	f := startDoc(t, ringConf(files, prefix, params))
	f.keyFiles = files
	return f
}

// ringConf is a config with media_tokens on prefix and a policy live with a
// keyring of files.
func ringConf(files map[string]string, prefix, params string) string {
	keys := make([]string, 0, len(files))
	for _, key := range slices.Sorted(maps.Keys(files)) {
		keys = append(keys, fmt.Sprintf(`{"key": %q, "secret_file": %q}`, key, files[key]))
	}
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"match": {"path": ["` + prefix + `", "` + prefix + `/*", "/elsewhere"]},
    "handler": {"module": "media_tokens", "params": ` + params + `}}],
  "media_auth": [{"name": "live", "keys": [` + strings.Join(keys, ",") + `], "token_ttl": "1h"}]
}`
}

// ringPolicy builds the policy from the key files, as a restart does.
func (f *fit) ringPolicy(t *testing.T) *mediaauth.Policy {
	t.Helper()
	keys := make([]config.MediaKey, 0, len(f.keyFiles))
	for _, key := range slices.Sorted(maps.Keys(f.keyFiles)) {
		keys = append(keys, config.MediaKey{Key: key, SecretFile: f.keyFiles[key]})
	}
	p, err := mediaauth.New(&config.MediaAuth{Name: "live", Keys: keys})
	if err != nil {
		t.Fatalf("a policy from the key files: %v", err)
	}
	return p
}

// issueKey posts a batch signed with key.
func issueKey(t *testing.T, f *fit, key, tokens string) (*http.Response, string) {
	t.Helper()
	return do(t, "POST", f.base+"/media-tokens",
		`{"key": `+strconv.Quote(key)+`, "tokens": [`+tokens+`]}`,
		map[string]string{"Content-Type": "application/json"})
}

// mustIssueKey is issueKey, failing the test unless it is answered 200.
func mustIssueKey(t *testing.T, f *fit, key, tokens string) []tokenAnswer {
	t.Helper()
	resp, body := issueKey(t, f, key, tokens)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issue with key %q: %d %s", key, resp.StatusCode, body)
	}
	var out issueAnswer
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("issue with key %q: %v: %s", key, err, body)
	}
	return out.Tokens
}

// revokeWith posts a revoke with body.
func revokeWith(t *testing.T, f *fit, body string) (*http.Response, string) {
	t.Helper()
	return do(t, "POST", f.base+"/media-tokens/revoke", body,
		map[string]string{"Content-Type": "application/json"})
}

// contents reads each of files.
func contents(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for key, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		out[key] = string(b)
	}
	return out
}

// TestIssueWithKey: a batch's key signs it, the answer says which key, and
// the policy checks the tokens of every key.
func TestIssueWithKey(t *testing.T) {
	f := startRing(t, "/media-tokens", `{"auth": "live"}`)
	named := mustIssueKey(t, f, "k2", `{"subject": "a", "publish": ["mtg-*"]}`)
	if len(named) != 1 || named[0].Key != "k2" || named[0].Subject != "a" {
		t.Fatalf("with key k2: %+v", named)
	}
	def := mustIssue(t, f, `{"subject": "b", "play": ["mtg-*"]}`)
	if len(def) != 1 || def[0].Key != "k1" {
		t.Fatalf("the default key: %+v", def)
	}
	p := f.policy(t, "live")
	if err := allowed(p, mediaauth.Publish, "mtg-1", named[0].Token); err != nil {
		t.Fatalf("k2's token: %v", err)
	}
	if err := allowed(p, mediaauth.Play, "mtg-1", def[0].Token); err != nil {
		t.Fatalf("k1's token: %v", err)
	}
}

// TestIssueKeyErrors: a key the policy has none of is 400, and a key on a
// policy with one secret is 400.
func TestIssueKeyErrors(t *testing.T) {
	f := startRing(t, "/media-tokens", `{"auth": "live"}`)
	if resp, body := issueKey(t, f, "nope", `{"subject": "a", "publish": ["mtg-*"]}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no key named") {
		t.Fatalf("an unknown key: %d %s", resp.StatusCode, body)
	}
	single := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	if resp, body := issueKey(t, single, "k1", `{"subject": "a", "publish": ["mtg-*"]}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "one key") {
		t.Fatalf("a key on a single-key policy: %d %s", resp.StatusCode, body)
	}
}

// TestRevokeOneKey: revoking one key rotates only its file and refuses only
// its tokens; the other key's stay good, and the policy rebuilt from the
// files agrees.
func TestRevokeOneKey(t *testing.T) {
	f := startRing(t, "/media-tokens", `{"auth": "live"}`)
	before := contents(t, f.keyFiles)
	k1 := mustIssueKey(t, f, "k1", `{"subject": "a", "publish": ["mtg-*"]}`)[0].Token
	k2 := mustIssueKey(t, f, "k2", `{"subject": "b", "publish": ["mtg-*"]}`)[0].Token
	resp, body := revokeWith(t, f, `{"key": "k1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke k1: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Revoked bool     `json:"revoked"`
		Since   int64    `json:"since"`
		Keys    []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || !out.Revoked || len(out.Keys) != 1 || out.Keys[0] != "k1" {
		t.Fatalf("revoke k1: %v: %s", err, body)
	}
	after := contents(t, f.keyFiles)
	if after["k2"] != before["k2"] {
		t.Fatal("k2's secret file changed")
	}
	if after["k1"] == before["k1"] || len(strings.TrimSpace(after["k1"])) < 16 {
		t.Fatalf("k1's secret file: %q", after["k1"])
	}
	p := f.policy(t, "live")
	if err := allowed(p, mediaauth.Publish, "mtg-1", k1); !errors.Is(err, mediaauth.ErrBadCredentials) {
		t.Fatalf("k1's old token: %v", err)
	}
	if err := allowed(p, mediaauth.Publish, "mtg-1", k2); err != nil {
		t.Fatalf("k2's token after k1's revoke: %v", err)
	}
	newer := mustIssueKey(t, f, "k1", `{"subject": "c", "publish": ["mtg-*"]}`)[0].Token
	if err := allowed(p, mediaauth.Publish, "mtg-1", newer); err != nil {
		t.Fatalf("a new k1 token: %v", err)
	}
	// A policy rebuilt from the files, as a restart reads them.
	ring := f.ringPolicy(t)
	if err := allowed(ring, mediaauth.Publish, "mtg-1", newer); err != nil {
		t.Fatalf("the new k1 token after a restart: %v", err)
	}
	if err := allowed(ring, mediaauth.Publish, "mtg-1", k1); !errors.Is(err, mediaauth.ErrBadCredentials) {
		t.Fatalf("the old k1 token after a restart: %v", err)
	}
	if err := allowed(ring, mediaauth.Publish, "mtg-1", k2); err != nil {
		t.Fatalf("k2's token after a restart: %v", err)
	}
}

// TestRevokeAllKeys: {"all": true} rotates every key of the ring.
func TestRevokeAllKeys(t *testing.T) {
	f := startRing(t, "/media-tokens", `{"auth": "live"}`)
	before := contents(t, f.keyFiles)
	k1 := mustIssueKey(t, f, "k1", `{"subject": "a", "publish": ["mtg-*"]}`)[0].Token
	k2 := mustIssueKey(t, f, "k2", `{"subject": "b", "publish": ["mtg-*"]}`)[0].Token
	resp, body := revokeWith(t, f, `{"all": true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke all: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Keys) != 2 {
		t.Fatalf("revoke all: %v: %s", err, body)
	}
	after := contents(t, f.keyFiles)
	for key := range f.keyFiles {
		if after[key] == before[key] {
			t.Fatalf("%s's secret file did not change", key)
		}
	}
	p := f.policy(t, "live")
	for _, tok := range []string{k1, k2} {
		if err := allowed(p, mediaauth.Publish, "mtg-1", tok); !errors.Is(err, mediaauth.ErrBadCredentials) {
			t.Fatalf("an old token after revoke all: %v", err)
		}
	}
	for _, key := range []string{"k1", "k2"} {
		if err := allowed(p, mediaauth.Publish, "mtg-1", mustIssueKey(t, f, key, `{"subject": "n", "publish": ["mtg-*"]}`)[0].Token); err != nil {
			t.Fatalf("a new %s token: %v", key, err)
		}
	}
}

// TestRevokeRingChoices: a keyring revoke names a key or all, and a policy
// with one secret takes neither.
func TestRevokeRingChoices(t *testing.T) {
	f := startRing(t, "/media-tokens", `{"auth": "live"}`)
	if resp, body := revoke(t, f); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "name a key") {
		t.Fatalf("an empty revoke of a ring: %d %s", resp.StatusCode, body)
	}
	if resp, body := revokeWith(t, f, `{"key": "nope"}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "no key named") {
		t.Fatalf("an unknown key: %d %s", resp.StatusCode, body)
	}
	single := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	if resp, body := revokeWith(t, single, `{"key": "k1"}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "one key") {
		t.Fatalf("a key on a single-key policy: %d %s", resp.StatusCode, body)
	}
	if resp, body := revoke(t, single); resp.StatusCode != http.StatusOK {
		t.Fatalf("the single key's revoke: %d %s", resp.StatusCode, body)
	}
}

// TestKeyringProvision: a keyring whose key has an inline secret is a config
// error, since that key could not be rotated.
func TestKeyringProvision(t *testing.T) {
	doc := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "media_tokens", "params": {"auth": "live"}}}],
  "media_auth": [{"name": "live", "keys": [
    {"key": "k1", "secret_file": "` + newSecretFile(t) + `"},
    {"key": "k2", "secret": "0123456789abcdef-inline"}]}]}`
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.Compile(c, runtime.Env{}); err == nil || !strings.Contains(err.Error(), "secret_file for every key") {
		t.Fatalf("an inline key in the keyring: %v", err)
	}
}

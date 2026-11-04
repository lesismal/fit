package mediatokens

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/runtime"
)

// TestProvisionParams: a prefix that is not a path, and a stream prefix that
// is not a pattern, are config errors.
func TestProvisionParams(t *testing.T) {
	for _, tc := range []struct{ params, want string }{
		{`{"auth": "live", "prefix": "media-tokens"}`, `"media-tokens" is not a path of segments`},
		{`{"auth": "live", "prefix": "/a b"}`, `"/a b" is not a path of segments`},
		{`{"auth": "live", "prefixes": ["mtg-", "a b"]}`, `prefixes[1]: "a b" is not a stream pattern`},
	} {
		c, err := config.Load([]byte(conf(newSecretFile(t), "/media-tokens", tc.params)))
		if err != nil {
			t.Fatalf("%s: %v", tc.params, err)
		}
		if _, err = runtime.Compile(c, runtime.Env{}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.params, err, tc.want)
		}
	}
}

// bounded provisions the Tokens it holds as they are set, past the bounds a
// config's schema puts on them, as a Go caller that builds a Tokens may.
type bounded struct{ t *Tokens }

func (b *bounded) Provision(ctx *module.Context) error { return b.t.Provision(ctx) }
func (b *bounded) ServeHTTP(*fibhttp.Context)          {}

var boundedSeq atomic.Int64

// provisionTokens runs tok's Provision through module.Load, as a route's
// handler is provisioned, without the config's schema checking its fields.
func provisionTokens(t *testing.T, tok *Tokens) error {
	t.Helper()
	id := fmt.Sprintf("test_media_tokens_bounded_%d", boundedSeq.Add(1))
	module.Register(module.Info{ID: module.Handlers + "." + id, New: func() any { return &bounded{t: tok} }})
	c, err := config.Load([]byte(`{"listeners": [{"name": "w", "address": ":0"}], "routes": [{"handler": {"module": "` + id + `", "params": {}}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = module.Load(module.Handlers, c.Routes[0].Handler, &module.Env{})
	return err
}

// TestProvisionBounds: Provision checks max_tokens and max_body_bytes itself,
// for a Tokens the schema did not check.
func TestProvisionBounds(t *testing.T) {
	for _, tc := range []struct {
		what string
		tok  *Tokens
		want string
	}{
		{"too many tokens", &Tokens{Auth: "live", MaxTokens: MaxMaxTokens + 1}, "is not between 1 and 1000"},
		{"too few tokens", &Tokens{Auth: "live", MaxTokens: -1}, "is not between 1 and 1000"},
		{"too small a body", &Tokens{Auth: "live", MaxBodyBytes: MinMaxBodyBytes - 1}, "a body is at least 256 and at most 1048576 bytes"},
		{"too large a body", &Tokens{Auth: "live", MaxBodyBytes: MaxMaxBodyBytes + 1}, "a body is at least 256 and at most 1048576 bytes"},
	} {
		if err := provisionTokens(t, tc.tok); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", tc.what, err, tc.want)
		}
	}
}

// TestDefaultTTLCappedByMaxTTL: a token whose request names no ttl gets the
// policy's token_ttl, but never more than max_ttl.
func TestDefaultTTLCappedByMaxTTL(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live", "max_ttl": "30m"}`)
	before := time.Now()
	toks := mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)
	near(t, toks[0].Expires, before.Add(30*time.Minute).Unix(), 5*time.Second)
}

// TestIssueGrantTooLong: a grant that does not fit in a token is 400, and
// the batch issues nothing.
func TestIssueGrantTooLong(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	patterns := make([]string, 20)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("%q", fmt.Sprintf("mtg-%02d-", i)+strings.Repeat("p", 240))
	}
	resp, body := issue(t, f, `{"subject": "a", "publish": ["mtg-*"]}, {"subject": "b", "play": [`+strings.Join(patterns, ",")+`]}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "tokens[1]: ") || !strings.Contains(body, "too long for a token") {
		t.Fatalf("a grant too long for a token: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, "fm1.") {
		t.Fatalf("a refused batch issued a token: %s", body)
	}
}

// TestPolicyRemoved: once the policy the handler holds is removed, as a
// config without it removes it, a batch and a revoke are 503 with
// Retry-After.
func TestPolicyRemoved(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)
	mediaauth.Remove(f.env.Engine)
	resp, body := issue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != retryAfter || !strings.Contains(body, "not loaded") {
		t.Fatalf("a batch of a removed policy: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	resp, body = revoke(t, f)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != retryAfter || !strings.Contains(body, "not loaded") {
		t.Fatalf("a revoke of a removed policy: %d %v %s", resp.StatusCode, resp.Header, body)
	}
}

// TestRevokeBadBody: a revoke's body that is not JSON is 400.
func TestRevokeBadBody(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	if resp, body := revokeWith(t, f, `{"all": `); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "must be JSON") {
		t.Fatalf("a revoke that is not JSON: %d %s", resp.StatusCode, body)
	}
}

// TestRevokePolicyChanged: the policy applied again under the handler, as
// the role media_auth applies a config, may no longer be what Provision
// checked: a key without a secret file, no keyring at all, or a keyring
// where there was one secret. Each revoke is 500, and rotates nothing.
func TestRevokePolicyChanged(t *testing.T) {
	ring := startRing(t, "/media-tokens", `{"auth": "live"}`)
	apply := func(f *fit, cfg config.MediaAuth) {
		t.Helper()
		cfg.Name = "live"
		if err := mediaauth.Apply(f.env.Engine, []config.MediaAuth{cfg}); err != nil {
			t.Fatal(err)
		}
	}
	before := contents(t, ring.keyFiles)
	apply(ring, config.MediaAuth{Keys: []config.MediaKey{
		{Key: "k1", SecretFile: ring.keyFiles["k1"]},
		{Key: "k2", Secret: "0123456789abcdef-inline"},
	}})
	if resp, body := revokeWith(t, ring, `{"all": true}`); resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, `no secret file for key "k2"`) {
		t.Fatalf("revoke all with an inline key: %d %s", resp.StatusCode, body)
	}
	apply(ring, config.MediaAuth{SecretFile: ring.keyFiles["k1"]})
	if resp, body := revokeWith(t, ring, `{"all": true}`); resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "no secret to rotate") {
		t.Fatalf("revoke all of a policy with no keyring: %d %s", resp.StatusCode, body)
	}
	if after := contents(t, ring.keyFiles); after["k1"] != before["k1"] || after["k2"] != before["k2"] {
		t.Fatal("a refused revoke wrote a key file")
	}

	single := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	apply(single, config.MediaAuth{Keys: []config.MediaKey{{Key: "k1", Secret: "0123456789abcdef-inline"}}})
	if resp, body := revoke(t, single); resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "the new secret cannot be used") {
		t.Fatalf("a single-secret revoke of a keyring: %d %s", resp.StatusCode, body)
	}
}

// rawPost sends a chunked POST of chunks, then tail, on a connection of its
// own, and returns what the server answered before it closed it.
func rawPost(t *testing.T, f *fit, path string, chunks []string, tail string) string {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(waitTimeout))
	var b strings.Builder
	b.WriteString("POST " + path + " HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n")
	for _, c := range chunks {
		fmt.Fprintf(&b, "%x\r\n%s\r\n", len(c), c)
	}
	b.WriteString(tail)
	// The server may answer and close before it has read everything.
	_, _ = conn.Write([]byte(b.String()))
	out, _ := io.ReadAll(conn)
	return string(out)
}

// TestBodyFails: a body past the listener's stream threshold reaches the
// handler as it arrives; when its chunked framing then breaks, the body
// fails, nothing is issued, and the connection is closed.
func TestBodyFails(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live", "max_body_bytes": 1048576}`)
	out := rawPost(t, f, "/media-tokens", []string{`{"tokens": [{"subject": "` + strings.Repeat("s", 70<<10)}, "zz\r\n")
	if strings.Contains(out, "200 OK") || strings.Contains(out, "fm1.") {
		t.Fatalf("a broken body was answered: %q", out)
	}
}

// TestStreamedBodyTooLarge: a streamed body past max_body_bytes is 413 once
// it outgrows it, and what follows of it is let go.
func TestStreamedBodyTooLarge(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	big := strings.Repeat("s", 40<<10)
	out := rawPost(t, f, "/media-tokens", []string{`{"tokens": [{"subject": "` + big, big, big}, "0\r\n\r\n")
	if !strings.HasPrefix(out, "HTTP/1.1 413") {
		t.Fatalf("a streamed body past the limit: %q", out)
	}
}

// TestRevokeDirGone: a secret file whose directory is gone cannot be
// rewritten, so the revoke is 500 and rotates nothing.
func TestRevokeDirGone(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	old := mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)[0].Token
	if err := os.RemoveAll(filepath.Dir(f.secretFile)); err != nil {
		t.Fatal(err)
	}
	if resp, body := revoke(t, f); resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "cannot be written") {
		t.Fatalf("a revoke whose directory is gone: %d %s", resp.StatusCode, body)
	}
	if err := allowed(f.policy(t, "live"), mediaauth.Publish, "mtg-1", old); err != nil {
		t.Fatalf("a failed revoke rotated the policy: %v", err)
	}
}

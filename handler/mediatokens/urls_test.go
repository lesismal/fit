package mediatokens

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/mediaauth"
	"github.com/lesismal/fit/runtime"
)

// startURLs serves media_tokens with params on a policy live with a
// keyring and url_signing of signing.
func startURLs(t *testing.T, params, signing string) *fit {
	t.Helper()
	files := newRingFiles(t)
	doc := strings.Replace(ringConf(files, "/media-tokens", withAuth(params)), `"token_ttl": "1h"`, `"token_ttl": "1h", "url_signing": `+signing, 1)
	f := startDoc(t, doc)
	f.keyFiles = files
	return f
}

func askURLs(t *testing.T, f *fit, body string) (*http.Response, string) {
	t.Helper()
	return do(t, "POST", f.base+"/media-tokens/urls", body, map[string]string{"Content-Type": "application/json"})
}

func mustURLs(t *testing.T, f *fit, body string) urlsAnswer {
	t.Helper()
	resp, b := askURLs(t, f, body)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("urls: %d %s", resp.StatusCode, b)
	}
	var out urlsAnswer
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// check reports the status p's GuardResource answers a GET of u, from
// remote, with.
func check(p *mediaauth.Policy, u, remote string) int {
	pu, _ := url.Parse(u)
	res, _ := mediaauth.ResourceOf(pu.Path)
	r := httptest.NewRequest("GET", pu.RequestURI(), nil)
	r.RemoteAddr = remote
	rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		p.GuardResource(c, "x", res, func(mediaauth.Access) { _ = c.Respond(200, "text/plain", nil) })
	}), r)
	return rec.Response.StatusCode
}

// TestURLs signs URLs whole for a viewer, each good for its own resource,
// bound to the address and session asked for, with the key asked for.
func TestURLs(t *testing.T) {
	f := startURLs(t, `{"url_base": "https://media.example.com/", "url_prefixes": ["/vod/", "/live/"], "max_ttl": "1h"}`, `{"ttl": "5m", "bind_address": "subnet"}`)
	before := time.Now()
	out := mustURLs(t, f, `{"subject": "user-1", "address": "203.0.113.7", "session": "tab-1", "key": "k2",
	  "paths": ["/vod/films/films/bbb/index.m3u8", "/live/cam.flv"]}`)
	if out.Subject != "user-1" || out.Session != "tab-1" || out.Key != "k2" || len(out.URLs) != 2 {
		t.Fatalf("the answer: %+v", out)
	}
	near(t, out.Expires, before.Add(5*time.Minute).Unix(), 5*time.Second)
	for i, prefix := range []string{"https://media.example.com/vod/films/films/bbb/index.m3u8?", "https://media.example.com/live/cam.flv?"} {
		u := out.URLs[i]
		if !strings.HasPrefix(u, prefix) || !strings.Contains(u, "kid=k2") || !strings.Contains(u, "ip=203.0.113.0/24") || !strings.Contains(u, "sid=tab-1") {
			t.Fatalf("URL %d: %s", i, u)
		}
	}
	p := f.policy(t, "live")
	if got := check(p, out.URLs[0], "203.0.113.99:1"); got != 200 {
		t.Fatalf("the URL from its network: %d", got)
	}
	if got := check(p, out.URLs[0], "198.51.100.1:1"); got != 403 {
		t.Fatalf("the URL from elsewhere: %d", got)
	}
	if got := check(p, strings.Replace(out.URLs[0], "/bbb/", "/bbc/", 1), "203.0.113.99:1"); got != 403 {
		t.Fatalf("another item: %d", got)
	}
	if got := check(p, out.URLs[1], "203.0.113.99:1"); got != 200 {
		t.Fatalf("the stream: %d", got)
	}

	// No session asked for: a new one each time; the ttl asked for; the
	// ring's first key; no address, from anywhere.
	a := mustURLs(t, f, `{"subject": "user-1", "ttl": "10m", "paths": ["/vod/films/a/index.m3u8"]}`)
	b := mustURLs(t, f, `{"subject": "user-1", "paths": ["/vod/films/a/index.m3u8"]}`)
	if a.Session == "" || a.Session == b.Session || a.Key != "k1" || strings.Contains(a.URLs[0], "ip=") {
		t.Fatalf("defaults: %+v %+v", a, b)
	}
	near(t, a.Expires, time.Now().Add(10*time.Minute).Unix(), 5*time.Second)
	if got := check(p, a.URLs[0], "198.51.100.1:1"); got != 200 {
		t.Fatalf("an unbound URL: %d", got)
	}
}

func TestURLsValidation(t *testing.T) {
	f := startURLs(t, `{"url_prefixes": ["/vod/"], "max_ttl": "1h", "max_tokens": 2}`, `{}`)
	for body, want := range map[string]string{
		`nonsense`:                      "must be JSON",
		`{"subject": "u", "paths": []}`: "paths is empty",
		`{"subject": "u", "paths": ["/vod/a/x", "/vod/a/y", "/vod/a/z"]}`: "at most 2",
		`{"paths": ["/vod/a/x"]}`:                                    "not a subject",
		`{"subject": "a\u0001", "paths": ["/vod/a/x"]}`:              "not a subject",
		`{"subject": "u", "session": "a b", "paths": ["/vod/a/x"]}`:  "not a session",
		`{"subject": "u", "address": "nope", "paths": ["/vod/a/x"]}`: "not an IP address",
		`{"subject": "u", "ttl": "nope", "paths": ["/vod/a/x"]}`:     "not a duration",
		`{"subject": "u", "ttl": "2h", "paths": ["/vod/a/x"]}`:       "longer than max_ttl",
		`{"subject": "u", "key": "k9", "paths": ["/vod/a/x"]}`:       "no key named",
		`{"subject": "u", "paths": ["/vod/../x"]}`:                   "not a path a URL is signed for",
		`{"subject": "u", "paths": ["/live/a/index.m3u8"]}`:          "none of the url_prefixes",
	} {
		resp, b := askURLs(t, f, body)
		if resp.StatusCode != 400 || !strings.Contains(b, want) {
			t.Errorf("%s: %d %q, want 400 %q", body, resp.StatusCode, b, want)
		}
	}
	if resp, _ := do(t, "GET", f.base+"/media-tokens/urls", "", nil); resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET: %d", resp.StatusCode)
	}
	// A key asked of a policy with one secret.
	g := startDoc(t, strings.Replace(conf(newSecretFile(t), "/media-tokens", `{"auth": "live"}`), `"token_ttl": "1h"`, `"token_ttl": "1h", "url_signing": {}`, 1))
	if resp, b := askURLs(t, g, `{"subject": "u", "key": "k1", "paths": ["/vod/a/x"]}`); resp.StatusCode != 400 || !strings.Contains(b, "one key") {
		t.Fatalf("a key of a single secret: %d %s", resp.StatusCode, b)
	}
	out := mustURLs(t, g, `{"subject": "u", "paths": ["/vod/a/x"]}`)
	if out.Key != "" || out.URLs[0] != "/vod/a/x?"+out.URLs[0][len("/vod/a/x?"):] || strings.Contains(out.URLs[0], "kid=") {
		t.Fatalf("a single secret's URL: %+v", out)
	}
	// A policy without url_signing.
	h := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	if resp, b := askURLs(t, h, `{"subject": "u", "paths": ["/vod/a/x"]}`); resp.StatusCode != 404 || !strings.Contains(b, "no url_signing") {
		t.Fatalf("without url_signing: %d %s", resp.StatusCode, b)
	}
}

func TestURLsProvision(t *testing.T) {
	for _, params := range []string{
		`{"url_base": "ftp://x"}`,
		`{"url_base": "https://"}`,
		`{"url_base": "https://x/?a"}`,
		`{"url_base": "https://u@x"}`,
		`{"url_base": "::"}`,
		`{"url_prefixes": ["vod/"]}`,
		`{"url_prefixes": ["/a/../b"]}`,
		`{"url_prefixes": ["/a b"]}`,
	} {
		files := newRingFiles(t)
		doc := strings.Replace(ringConf(files, "/media-tokens", withAuth(params)), `"token_ttl": "1h"`, `"token_ttl": "1h", "url_signing": {}`, 1)
		if err := compileErr(t, doc); err == nil || !strings.Contains(err.Error(), "url_") {
			t.Errorf("%s: %v", params, err)
		}
	}
}

// compileErr loads and compiles doc, and returns the first error.
func compileErr(t *testing.T, doc string) error {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		return err
	}
	g, err := runtime.Compile(c, runtime.Env{})
	if err == nil {
		g.Release()
	}
	return err
}

// withAuth returns params, a JSON object, with auth live.
func withAuth(params string) string {
	if strings.TrimSpace(params) == "{}" {
		return `{"auth": "live"}`
	}
	return `{"auth": "live", ` + strings.TrimPrefix(params, "{")
}

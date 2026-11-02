package media

import (
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/mediaauth"
)

const pubToken = "publisher-token-0123456789"

// authDoc is a config with whip and whep behind the media_auth policy
// "live", whose JSON is policy, and webrtc.media.
func authDoc(policy string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": [` + policy + `],
  "routes": [
    {"match": {"path": ["/whip/*"]}, "handler": {"module": "whip", "params": {"auth": "live"}}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "whep", "params": {"auth": "live"}}}
  ],
  "webrtc": {"media": ` + loopbackMedia + `}
}`
}

func livePolicy(t *testing.T, extra string) string {
	h, err := bcrypt.GenerateFromPassword([]byte("bob-pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return `{"name": "live", "secret": "a-secret-of-sixteen-bytes",
	  "tokens": [{"name": "cam", "token": "` + pubToken + `", "publish": ["cam*"]}],
	  "users": [{"name": "alice", "password": "alice-pw", "play": ["cam1"]},
	            {"name": "bob", "password_hash": "` + string(h) + `", "publish": ["bob"], "play": ["*"]}]` + extra + `}`
}

// postSDP posts an offer to url with what set adds to the request.
func postSDP(t *testing.T, url, sdp string, set func(*http.Request)) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(sdp))
	req.Header.Set("Content-Type", "application/sdp")
	if set != nil {
		set(req)
	}
	return do(t, req)
}

func bearer(tok string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
}

func basic(u, pw string) func(*http.Request) {
	return func(r *http.Request) { r.SetBasicAuth(u, pw) }
}

// whip and whep with auth: 401 with a Bearer challenge without
// credentials or with bad ones, 403 for what they do not allow, 201 for
// what they do, by token, signed token, query token, Basic and a bcrypt
// password; a session's DELETE needs no credentials; a reload changes the
// policy in place, and public opens an action to all.
func TestHandlerAuth(t *testing.T) {
	f := startFit(t, authDoc(livePolicy(t, "")))
	clients := startEngine(t)
	offer := func() string { return offerOf(t, newPublisher(t, clients).peer) }
	view := func() string { return offerOf(t, newViewer(t, clients).peer) }

	resp, _ := postSDP(t, f.base+"/whip/cam1", offer(), nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("Www-Authenticate") != `Bearer realm="fit"` {
		t.Fatalf("no credentials: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = postSDP(t, f.base+"/whip/cam1", offer(), bearer("not-a-token-at-all"))
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("Www-Authenticate"), "invalid_token") {
		t.Fatalf("a bad token: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, _ = postSDP(t, f.base+"/whip/other", offer(), bearer(pubToken)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a stream the token may not publish: %d", resp.StatusCode)
	}
	resp, body := postSDP(t, f.base+"/whip/cam1", offer(), bearer(pubToken))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish: %d %s", resp.StatusCode, body)
	}
	session := resp.Header.Get("Location")

	// whep: alice plays cam1 and nothing else; bob's bcrypt password goes
	// through the pool.
	if resp, _ = postSDP(t, f.base+"/whep/cam1", view(), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("view without credentials: %d", resp.StatusCode)
	}
	if resp, _ = postSDP(t, f.base+"/whep/cam2", view(), basic("alice", "alice-pw")); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("alice on cam2: %d", resp.StatusCode)
	}
	if resp, _ = postSDP(t, f.base+"/whep/cam1", view(), basic("alice", "wrong")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("alice's wrong password: %d", resp.StatusCode)
	}
	if resp, body = postSDP(t, f.base+"/whep/cam1", view(), basic("alice", "alice-pw")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("alice on cam1: %d %s", resp.StatusCode, body)
	}
	if resp, body = postSDP(t, f.base+"/whep/cam1", view(), basic("bob", "bob-pw")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob on cam1: %d %s", resp.StatusCode, body)
	}
	if resp, _ = postSDP(t, f.base+"/whep/cam1", view(), basic("bob", "nope")); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bob's wrong password: %d", resp.StatusCode)
	}
	// A token fit signed, in the query.
	p := mediaauth.Lookup(f.up, "live")
	if p == nil {
		t.Fatal("no policy live")
	}
	tok, _, err := p.Issue("viewer1", mediaauth.Grant{Play: []string{"cam*"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if resp, body = postSDP(t, f.base+"/whep/cam1?token="+tok, view(), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("a signed token in the query: %d %s", resp.StatusCode, body)
	}
	if resp, _ = postSDP(t, f.base+"/whip/cam9", offer(), bearer(tok)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a play token publishing: %d", resp.StatusCode)
	}
	// OPTIONS needs nothing; the session's DELETE needs nothing.
	req, _ := http.NewRequest("OPTIONS", f.base+"/whip/cam1", nil)
	if resp, _ = do(t, req); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("OPTIONS: %d", resp.StatusCode)
	}
	if code := request(t, "DELETE", f.base+session); code != http.StatusOK {
		t.Fatalf("DELETE of the session: %d", code)
	}

	// A reload changes the policy in place: the token may now publish
	// other*, and play is public.
	f.reload(t, authDoc(strings.Replace(livePolicy(t, `, "public": ["play"]`), `"cam*"`, `"other*"`, 1)))
	if mediaauth.Lookup(f.up, "live") != p {
		t.Fatal("the policy was replaced, not changed in place")
	}
	if resp, _ = postSDP(t, f.base+"/whip/cam2", offer(), bearer(pubToken)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("after the reload, cam2: %d", resp.StatusCode)
	}
	if resp, body = postSDP(t, f.base+"/whip/other1", offer(), bearer(pubToken)); resp.StatusCode != http.StatusCreated {
		t.Fatalf("after the reload, other1: %d %s", resp.StatusCode, body)
	}
	if resp, body = postSDP(t, f.base+"/whep/other1", view(), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("public play: %d %s", resp.StatusCode, body)
	}
	// A reload without auth opens the routes, and the policy goes.
	f.reload(t, doc(loopbackMedia))
	if mediaauth.Lookup(f.up, "live") != nil {
		t.Fatal("the policy outlived its config")
	}
	if _, err := p.Allow(mediaauth.Play, "x", mediaauth.Credentials{}); err != mediaauth.ErrUnavailable {
		t.Fatalf("the old policy: %v", err)
	}
	if resp, body = postSDP(t, f.base+"/whip/open1", offer(), nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("without auth: %d %s", resp.StatusCode, body)
	}
}

// An auth naming no policy fails the config, and webrtc_proxy's auth needs
// its action.
func TestHandlerAuthConfig(t *testing.T) {
	for _, tt := range []struct{ doc, want string }{
		{`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
		  "routes": [{"handler": {"module": "whip", "params": {"auth": "nope"}}}]}`, `no media_auth policy named "nope"`},
		{`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "media_auth": [{"name": "p", "public": ["play"]}],
		  "routes": [{"handler": {"module": "whep", "params": {"auth": "q"}}}]}`, `no media_auth policy named "q"`},
		{`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "media_auth": [{"name": "p", "public": ["play"]}],
		  "upstreams": [{"name": "m", "addresses": ["127.0.0.1:1"]}],
		  "routes": [{"handler": {"module": "webrtc_proxy", "params": {"upstream": "m", "auth": "p"}}}]}`, "auth needs action"},
		{`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "media_auth": [{"name": "p", "public": ["play"]}],
		  "upstreams": [{"name": "m", "addresses": ["127.0.0.1:1"]}],
		  "routes": [{"handler": {"module": "webrtc_proxy", "params": {"upstream": "m", "auth": "p", "action": "watch"}}}]}`, "watch"},
	} {
		if err := compileErr(t, tt.doc); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v, want %q", err, tt.want)
		}
	}
}

// webrtc_proxy checks an offer before a media server sees it, and passes a
// session's requests on without credentials.
func TestProxyAuth(t *testing.T) {
	backend := startFit(t, doc(loopbackMedia))
	addr := strings.TrimPrefix(backend.base, "http://")
	f := startFit(t, `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "media_auth": [`+livePolicy(t, "")+`],
  "upstreams": [{"name": "media", "addresses": ["`+addr+`"]}],
  "routes": [
    {"match": {"path": ["/whip/*"]}, "handler": {"module": "webrtc_proxy", "params": {"upstream": "media", "timeout": "1s", "auth": "live", "action": "publish"}}},
    {"match": {"path": ["/whep/*"]}, "handler": {"module": "webrtc_proxy", "params": {"upstream": "media", "timeout": "1s", "auth": "live", "action": "play"}}}
  ],
  "webrtc": {"proxy": `+loopbackProxy+`}
}`)
	clients := startEngine(t)
	offer := func() string { return offerOf(t, newPublisher(t, clients).peer) }
	if resp, _ := postSDP(t, f.base+"/whip/cam1", offer(), nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credentials: %d", resp.StatusCode)
	}
	if resp, _ := postSDP(t, f.base+"/whip/bob", offer(), bearer(pubToken)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forbidden: %d", resp.StatusCode)
	}
	if m := metrics(backend.media()); !strings.Contains(m, "fit_webrtc_media_streams 0") {
		t.Fatalf("a refused offer reached the media server:\n%s", m)
	}
	resp, body := postSDP(t, f.base+"/whip/cam1", offer(), bearer(pubToken))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("publish: %d %s", resp.StatusCode, body)
	}
	// bob's bcrypt password, through the pool, for publish.
	if resp, body = postSDP(t, f.base+"/whip/bob", offer(), basic("bob", "bob-pw")); resp.StatusCode != http.StatusCreated {
		t.Fatalf("bob publishes: %d %s", resp.StatusCode, body)
	}
	if resp2, _ := postSDP(t, f.base+"/whep/cam1", offerOf(t, newViewer(t, clients).peer), basic("alice", "alice-pw")); resp2.StatusCode != http.StatusCreated {
		t.Fatalf("alice plays: %d", resp2.StatusCode)
	}
	if code := request(t, "DELETE", f.base+resp.Header.Get("Location")); code != http.StatusOK {
		t.Fatalf("DELETE: %d", code)
	}
}

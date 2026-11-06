package mediaauth

import (
	"crypto/tls"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/observe"
)

// t0 is the tests' clock's start.
var t0 = time.Unix(1_800_000_000, 0)

// clock sets mediaauth's clock to at, and back when the test ends; set it
// again with the function returned.
func clock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	var mu sync.Mutex
	cur := at
	old := now
	now = func() time.Time { mu.Lock(); defer mu.Unlock(); return cur }
	t.Cleanup(func() { now = old })
	return func(at time.Time) { mu.Lock(); cur = at; mu.Unlock() }
}

// urlPolicy is a policy with a keyring k1, k2 and url_signing as mod
// changes it.
func urlPolicy(t *testing.T, mod func(*config.MediaAuth)) *Policy {
	t.Helper()
	cfg := config.MediaAuth{
		Name:       "site",
		Keys:       []config.MediaKey{{Key: "k1", Secret: "the first key, 16 bytes+"}, {Key: "k2", Secret: "the second key, 16 bytes+"}},
		Tokens:     []config.MediaToken{{Token: staticToken, Play: []string{"vod/*"}}},
		URLSigning: &config.URLSigning{},
	}
	if mod != nil {
		mod(&cfg)
	}
	return newPolicy_(t, cfg)
}

// played is what a request GuardResource let through, or not, came to.
type played struct {
	rec    *fibtest.Recorder
	access Access
	called bool
	uri    string // the request's RequestURI once guarded
	entry  *observe.Entry
}

// guardURL serves GET path (with its query) of resource through p's
// GuardResource, its request set up by setup.
func guardURL(p *Policy, path, resource string, setup func(*stdhttp.Request)) played {
	r := httptest.NewRequest("GET", path, nil)
	r.RemoteAddr = "203.0.113.7:4000"
	if setup != nil {
		setup(r)
	}
	var out played
	out.entry = &observe.Entry{}
	out.rec = fibtest.ServeRequest(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		c.SetValue(out.entry)
		p.GuardResource(c, "vod/films/a", resource, func(a Access) {
			out.access, out.called = a, true
			_ = c.Respond(200, "text/plain", []byte("ok"))
		})
	}), r)
	out.uri = r.RequestURI
	return out
}

func signed(t *testing.T, p *Policy, resource string, v Viewer, exp time.Time, key string) string {
	t.Helper()
	q, err := p.SignURL(resource, v, exp, key)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// refusedFor checks that pl was refused 403 for reason.
func refusedFor(t *testing.T, what string, pl played, reason string) {
	t.Helper()
	if pl.called || pl.rec.Response.StatusCode != 403 || !strings.Contains(string(pl.rec.Response.Body), "refused: "+reason) {
		t.Fatalf("%s: %d %q, want 403 for %s", what, pl.rec.Response.StatusCode, pl.rec.Response.Body, reason)
	}
}

func allowed(t *testing.T, what string, pl played) {
	t.Helper()
	if !pl.called || pl.rec.Response.StatusCode != 200 {
		t.Fatalf("%s: %d %q, want 200", what, pl.rec.Response.StatusCode, pl.rec.Response.Body)
	}
}

const res = "/vod/films/a/"

// A URL is good for its resource alone, until its expiry, and for no other
// resource or with any field changed; its signature never reaches the
// request's RequestURI.
func TestSignedURL(t *testing.T) {
	clock(t, t0)
	p := urlPolicy(t, nil)
	q := signed(t, p, res, Viewer{Subject: "user 42", Session: "s1"}, t0.Add(time.Minute), "")
	if !strings.Contains(q, "kid=k1") || !strings.Contains(q, "sub=user+42") || !strings.Contains(q, "sid=s1") || strings.Contains(q, "&g=") {
		t.Fatalf("the query: %s", q)
	}
	pl := guardURL(p, res+"index.m3u8?"+q, res, nil)
	allowed(t, "the URL", pl)
	if !pl.access.URL() || pl.access.Subject != "user 42" || pl.entry.User != "user 42" || !pl.access.Expiry().Equal(t0.Add(time.Minute)) {
		t.Fatalf("the access: %+v, entry %+v", pl.access, pl.entry)
	}
	if strings.Contains(pl.uri, "sig=") && !strings.Contains(pl.uri, "sig=REDACTED") || !strings.Contains(pl.uri, "sig=REDACTED") {
		t.Fatalf("the RequestURI: %s", pl.uri)
	}
	sig := q[strings.Index(q, "sig=")+4:]
	if strings.Contains(pl.uri, sig) {
		t.Fatalf("the RequestURI holds the signature: %s", pl.uri)
	}
	// Another item, and every byte of the resource changed.
	refusedFor(t, "another item", guardURL(p, "/vod/films/b/index.m3u8?"+q, "/vod/films/b/", nil), "signature")
	for i := 1; i < len(res)-1; i++ {
		b := []byte(res)
		b[i] ^= 0x01
		refusedFor(t, "the resource with byte "+string(rune('0'+i%10))+" changed", guardURL(p, "/x/index.m3u8?"+q, string(b), nil), "signature")
	}
	// Every field changed, added or taken out.
	vals, _ := url.ParseQuery(q)
	for _, change := range []func(url.Values){
		func(v url.Values) { v.Set("exp", "1800000061") },
		func(v url.Values) { v.Set("sub", "user 43") },
		func(v url.Values) { v.Set("sid", "s2") },
		func(v url.Values) { v.Del("sid") },
		func(v url.Values) { v.Set("kid", "k2") },
		func(v url.Values) { v.Set("kid", "k3") },
		func(v url.Values) { v.Del("kid") },
		func(v url.Values) { v.Set("ip", "203.0.113.7") },
		func(v url.Values) { v.Set("g", "1") },
		func(v url.Values) { v.Set("g", "2") },
		func(v url.Values) { v.Set("sig", strings.Repeat("A", 22)) },
		func(v url.Values) { v.Set("sig", "short") },
		func(v url.Values) { v.Del("sub") },
		func(v url.Values) { v.Set("exp", "-1") },
		func(v url.Values) { v.Add("exp", "1800000001") },
		func(v url.Values) { v.Set("sub", "a\nb") },
	} {
		v := url.Values{}
		for k, l := range vals {
			v[k] = append([]string(nil), l...)
		}
		change(v)
		refusedFor(t, "changed "+v.Encode(), guardURL(p, res+"index.m3u8?"+v.Encode(), res, nil), "signature")
	}
	if got := p.URLRefusals()["signature"]; got < 20 {
		t.Fatalf("signature refusals counted: %d", got)
	}
	// No signature: a token, as Guard checks it; nothing at all, 401.
	allowed(t, "a token", guardURL(p, res+"index.m3u8", res, func(r *stdhttp.Request) { r.Header.Set("Authorization", "Bearer "+staticToken) }))
	if pl := guardURL(p, res+"index.m3u8", res, nil); pl.rec.Response.StatusCode != 401 {
		t.Fatalf("nothing: %d", pl.rec.Response.StatusCode)
	}
	// A nil policy lets everyone in.
	allowed(t, "a nil policy", guardURL(nil, res+"index.m3u8?"+q, res, nil))
	// A policy that makes playing public does not look at the URL.
	pub := urlPolicy(t, func(c *config.MediaAuth) { c.Public = []string{"play"} })
	allowed(t, "a public policy", guardURL(pub, res+"index.m3u8?sig=x", res, nil))
}

// Once a URL expires its playlist is refused, while the grant its playlist
// handed on lives play_grace past the later of the URL's expiry and the
// item's end, and is handed on unchanged.
func TestGrantOutlivesTheURL(t *testing.T) {
	set := clock(t, t0)
	p := urlPolicy(t, nil)
	q := signed(t, p, res, Viewer{Subject: "u", Session: "s1"}, t0.Add(time.Minute), "k2")
	pl := guardURL(p, res+"index.m3u8?"+q, res, nil)
	allowed(t, "the URL", pl)
	child, cookie := pl.access.PassOn(2 * time.Hour)
	if cookie != "" || !strings.Contains(child, "&g=1&") || !strings.Contains(child, "kid=k2") {
		t.Fatalf("the grant: %q %q", child, cookie)
	}
	vals, _ := url.ParseQuery(child)
	if want := t0.Add(2*time.Hour + config.DefaultPlayGrace).Unix(); vals.Get("exp") != itoa(want) {
		t.Fatalf("the grant's expiry %s, want %d", vals.Get("exp"), want)
	}
	// A short item: play_grace past the URL's expiry.
	short, _ := pl.access.PassOn(10 * time.Second)
	if v, _ := url.ParseQuery(short); v.Get("exp") != itoa(t0.Add(time.Minute+config.DefaultPlayGrace).Unix()) {
		t.Fatalf("a short item's grant: %s", short)
	}
	set(t0.Add(time.Minute))
	refusedFor(t, "the URL expired", guardURL(p, res+"index.m3u8?"+q, res, nil), "expired")
	pl = guardURL(p, res+"v0.12.m4s?"+child, res, nil)
	allowed(t, "the grant after the URL's expiry", pl)
	// A playlist fetched with the grant hands the same grant on.
	pl = guardURL(p, res+"v0.fmp4.m3u8?"+child, res, nil)
	if again, _ := pl.access.PassOn(5 * time.Hour); again != child {
		t.Fatalf("the grant handed on again:\n%s\n%s", again, child)
	}
	refusedFor(t, "the grant for another item", guardURL(p, "/vod/films/b/v0.0.m4s?"+child, "/vod/films/b/", nil), "signature")
	set(t0.Add(2*time.Hour + config.DefaultPlayGrace))
	refusedFor(t, "the grant expired", guardURL(p, res+"v0.12.m4s?"+child, res, nil), "expired")
	if p.URLRefusals()["expired"] != 2 {
		t.Fatalf("refusals: %v", p.URLRefusals())
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// tlsState marks a request as one that came over TLS.
var tlsState tls.ConnectionState

// A URL bound to an address is refused from another, to its network with
// bind_address subnet, and the viewer behind trusted proxies is the last
// untrusted address of X-Forwarded-For.
func TestAddressBound(t *testing.T) {
	clock(t, t0)
	p := urlPolicy(t, nil)
	q := signed(t, p, res, Viewer{Subject: "u", Address: netip.MustParseAddr("::ffff:203.0.113.7")}, t0.Add(time.Minute), "")
	if !strings.Contains(q, "ip=203.0.113.7&") {
		t.Fatalf("the query: %s", q)
	}
	allowed(t, "the address", guardURL(p, res+"i.m3u8?"+q, res, nil))
	refusedFor(t, "another address", guardURL(p, res+"i.m3u8?"+q, res, func(r *stdhttp.Request) { r.RemoteAddr = "203.0.113.8:1" }), "address")
	if p.URLRefusals()["address"] != 1 {
		t.Fatal("not counted")
	}
	sub := urlPolicy(t, func(c *config.MediaAuth) {
		c.URLSigning.BindAddress = "subnet"
		c.URLSigning.TrustedProxies = []string{"10.0.0.0/8"}
	})
	q = signed(t, sub, res, Viewer{Subject: "u", Address: netip.MustParseAddr("203.0.113.7")}, t0.Add(time.Minute), "")
	if !strings.Contains(q, "ip=203.0.113.0/24&") {
		t.Fatalf("the query: %s", q)
	}
	allowed(t, "the network", guardURL(sub, res+"i.m3u8?"+q, res, func(r *stdhttp.Request) { r.RemoteAddr = "203.0.113.200:1" }))
	refusedFor(t, "another network", guardURL(sub, res+"i.m3u8?"+q, res, func(r *stdhttp.Request) { r.RemoteAddr = "203.0.114.7:1" }), "address")
	behind := func(xff string) func(*stdhttp.Request) {
		return func(r *stdhttp.Request) { r.RemoteAddr = "10.1.2.3:1"; r.Header.Set("X-Forwarded-For", xff) }
	}
	allowed(t, "behind a proxy", guardURL(sub, res+"i.m3u8?"+q, res, behind("198.51.100.1, 203.0.113.9, 10.0.0.2")))
	refusedFor(t, "another behind a proxy", guardURL(sub, res+"i.m3u8?"+q, res, behind("203.0.113.9, 198.51.100.1")), "address")
	refusedFor(t, "a forwarded list that does not parse", guardURL(sub, res+"i.m3u8?"+q, res, behind("nonsense")), "address")
	refusedFor(t, "all trusted", guardURL(sub, res+"i.m3u8?"+q, res, behind("10.9.9.9")), "address")
	q6 := signed(t, sub, res, Viewer{Subject: "u", Address: netip.MustParseAddr("2001:db8:1:2:3::4")}, t0.Add(time.Minute), "")
	if !strings.Contains(q6, "ip=2001:db8:1:2::/64&") {
		t.Fatalf("an IPv6 query: %s", q6)
	}
	allowed(t, "IPv6 network", guardURL(sub, res+"i.m3u8?"+q6, res, func(r *stdhttp.Request) { r.RemoteAddr = "[2001:db8:1:2:ffff::1]:1" }))
	refusedFor(t, "another IPv6 network", guardURL(sub, res+"i.m3u8?"+q6, res, func(r *stdhttp.Request) { r.RemoteAddr = "[2001:db8:1:3::1]:1" }), "address")
	// An address escaped in the query is read as it is meant.
	esc := strings.Replace(q6, "ip=2001:db8:1:2::/64", "ip="+url.QueryEscape("2001:db8:1:2::/64"), 1)
	allowed(t, "an escaped address", guardURL(sub, res+"i.m3u8?"+esc, res, func(r *stdhttp.Request) { r.RemoteAddr = "[2001:db8:1:2::9]:1" }))
	if !boundTo("203.0.113.7", netip.MustParseAddr("203.0.113.7")) || boundTo("nonsense", netip.MustParseAddr("203.0.113.7")) || boundTo("x/1", netip.Addr{}) {
		t.Fatal("boundTo")
	}
	if peer("203.0.113.7") != netip.MustParseAddr("203.0.113.7") || peer("nonsense").IsValid() {
		t.Fatal("peer")
	}
}

// max_sessions refuses a third session, or ends the oldest; sessions idle
// past session_idle are over; Sessions lists them and EndSession ends one.
func TestSessions(t *testing.T) {
	set := clock(t, t0)
	exp := t0.Add(time.Hour)
	for _, endOldest := range []bool{false, true} {
		p := urlPolicy(t, func(c *config.MediaAuth) {
			c.URLSigning.MaxSessions = 2
			if endOldest {
				c.URLSigning.OnLimit = "end_oldest"
			}
		})
		set(t0)
		play := func(sub, sid string) played {
			return guardURL(p, res+"v0.0.m4s?"+signed(t, p, res, Viewer{Subject: sub, Session: sid}, exp, ""), res, nil)
		}
		allowed(t, "a", play("u", "a"))
		set(t0.Add(time.Second))
		allowed(t, "b", play("u", "b"))
		allowed(t, "another subject", play("v", "a"))
		allowed(t, "a again", play("u", "a"))
		if !endOldest {
			refusedFor(t, "a third", play("u", "c"), "session_limit")
			allowed(t, "a after the third", play("u", "a"))
			if p.URLRefusals()["session_limit"] != 1 {
				t.Fatal("not counted")
			}
		} else {
			allowed(t, "a third", play("u", "c"))
			refusedFor(t, "the oldest", play("u", "a"), "session_ended")
			allowed(t, "b", play("u", "b"))
			// The grant the oldest was handed is refused too.
			if p.URLRefusals()["session_ended"] != 1 {
				t.Fatal("not counted")
			}
		}
		ss := p.Sessions()
		want := 3
		if endOldest {
			want = 4
		}
		if len(ss) != want || ss[0].Subject != "u" || ss[0].ID != "a" || ss[0].Resource != res || ss[0].Address != "203.0.113.7:4000" || ss[want-1].Subject != "v" {
			t.Fatalf("sessions: %+v", ss)
		}
		if ss[0].Ended != endOldest {
			t.Fatalf("a ended: %+v", ss[0])
		}
		// Past session_idle the sessions are over, and new ones begin.
		set(t0.Add(time.Second + config.DefaultSessionIdle + time.Second))
		allowed(t, "d", play("u", "d"))
		allowed(t, "e", play("u", "e"))
		if !p.EndSession("u", "d") || p.EndSession("u", "d") || p.EndSession("u", "zz") || p.EndSession("w", "d") {
			t.Fatal("EndSession")
		}
		refusedFor(t, "an ended session", play("u", "d"), "session_ended")
		allowed(t, "f", play("u", "f"))
		// An ended session is remembered until its grants expire.
		set(exp)
		if ss := p.Sessions(); len(ss) != 0 {
			t.Fatalf("sessions past every grant: %+v", ss)
		}
	}
}

// A grant handed on keeps an ended session refused as long as the grant
// lives, past the URL's expiry.
func TestEndedSessionGrant(t *testing.T) {
	set := clock(t, t0)
	p := urlPolicy(t, func(c *config.MediaAuth) {
		c.URLSigning.MaxSessions = 1
		c.URLSigning.OnLimit = "end_oldest"
	})
	pl := guardURL(p, res+"index.m3u8?"+signed(t, p, res, Viewer{Subject: "u", Session: "a"}, t0.Add(time.Minute), ""), res, nil)
	allowed(t, "a", pl)
	child, _ := pl.access.PassOn(time.Hour)
	allowed(t, "b", guardURL(p, res+"index.m3u8?"+signed(t, p, res, Viewer{Subject: "u", Session: "b"}, t0.Add(time.Minute), ""), res, nil))
	set(t0.Add(30 * time.Minute))
	refusedFor(t, "a's grant", guardURL(p, res+"v0.1.m4s?"+child, res, nil), "session_ended")
}

// max_tracked_sessions bounds the table: a new session past it is 503,
// until a sweep finds sessions over.
func TestTrackedSessions(t *testing.T) {
	set := clock(t, t0)
	p := urlPolicy(t, func(c *config.MediaAuth) { c.URLSigning.MaxTrackedSessions = 2 })
	play := func(sub string) played {
		return guardURL(p, res+"v0.0.m4s?"+signed(t, p, res, Viewer{Subject: sub, Session: "s"}, t0.Add(time.Hour), ""), res, nil)
	}
	allowed(t, "1", play("a"))
	allowed(t, "2", play("b"))
	if pl := play("c"); pl.called || pl.rec.Response.StatusCode != 503 || pl.rec.Response.Header.Get("Retry-After") != "1" {
		t.Fatalf("past the bound: %d", pl.rec.Response.StatusCode)
	}
	allowed(t, "a session already tracked", play("a"))
	set(t0.Add(config.DefaultSessionIdle + time.Minute))
	allowed(t, "after the others are over", play("c"))
	if p.sess.total.Load() != 1 {
		t.Fatalf("tracked: %d", p.sess.total.Load())
	}
	// A URL without a session is not tracked.
	allowed(t, "no session", guardURL(p, res+"v0.0.m4s?"+signed(t, p, res, Viewer{Subject: "z"}, t0.Add(2*time.Hour), ""), res, nil))
}

// Rotating a key refuses the URLs and grants it signed, and only those;
// rotating a single secret refuses every one.
func TestRotateRefusesURLs(t *testing.T) {
	clock(t, t0)
	p := urlPolicy(t, nil)
	q1 := signed(t, p, res, Viewer{Subject: "u"}, t0.Add(time.Minute), "k1")
	q2 := signed(t, p, res, Viewer{Subject: "u"}, t0.Add(time.Minute), "k2")
	pl := guardURL(p, res+"index.m3u8?"+q1, res, nil)
	child, _ := pl.access.PassOn(time.Hour)
	if err := p.RotateKey("k1", "a fresh secret, 16 bytes+"); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, "k1's URL", guardURL(p, res+"index.m3u8?"+q1, res, nil), "signature")
	refusedFor(t, "k1's grant", guardURL(p, res+"v0.0.m4s?"+child, res, nil), "signature")
	allowed(t, "k2's URL", guardURL(p, res+"index.m3u8?"+q2, res, nil))

	one := urlPolicy(t, func(c *config.MediaAuth) { c.Keys = nil; c.Secret = "a single secret of 16+" })
	q := signed(t, one, res, Viewer{Subject: "u"}, t0.Add(time.Minute), "")
	if strings.Contains(q, "kid=") {
		t.Fatalf("a single secret's URL names a key: %s", q)
	}
	allowed(t, "the single secret's URL", guardURL(one, res+"index.m3u8?"+q, res, nil))
	if err := one.Rotate("another single secret 16+"); err != nil {
		t.Fatal(err)
	}
	refusedFor(t, "after Rotate", guardURL(one, res+"index.m3u8?"+q, res, nil), "signature")
	refusedFor(t, "a key the policy has not", guardURL(one, res+"index.m3u8?"+q2, res, nil), "signature")
}

// referers and origins refuse requests from other pages and scripts, and
// let in those that send neither.
func TestReferersAndOrigins(t *testing.T) {
	clock(t, t0)
	p := urlPolicy(t, func(c *config.MediaAuth) {
		c.URLSigning.Referers = []string{"https://*.Example.com"}
		c.URLSigning.Origins = []string{"https://www.example.com", "http://localhost:*"}
	})
	q := signed(t, p, res, Viewer{Subject: "u"}, t0.Add(time.Minute), "")
	with := func(k, v string) func(*stdhttp.Request) { return func(r *stdhttp.Request) { r.Header.Set(k, v) } }
	allowed(t, "neither", guardURL(p, res+"i.m3u8?"+q, res, nil))
	allowed(t, "a referer", guardURL(p, res+"i.m3u8?"+q, res, with("Referer", "https://WWW.example.com/watch/1?x=y")))
	allowed(t, "a referer without a path", guardURL(p, res+"i.m3u8?"+q, res, with("Referer", "https://a.example.com")))
	refusedFor(t, "another referer", guardURL(p, res+"i.m3u8?"+q, res, with("Referer", "https://evil.test/example.com")), "referer")
	refusedFor(t, "a referer that is no URL", guardURL(p, res+"i.m3u8?"+q, res, with("Referer", "nonsense")), "referer")
	refusedFor(t, "a long referer", guardURL(p, res+"i.m3u8?"+q, res, with("Referer", "https://"+strings.Repeat("a", 300)+".example.com/")), "referer")
	allowed(t, "an origin", guardURL(p, res+"i.m3u8?"+q, res, with("Origin", "http://localhost:8080")))
	refusedFor(t, "another origin", guardURL(p, res+"i.m3u8?"+q, res, with("Origin", "https://example.com")), "origin")
}

// With cookie, a playlist hands its grant on as a cookie on the resource's
// path, and requests carry it there.
func TestGrantCookie(t *testing.T) {
	set := clock(t, t0)
	p := urlPolicy(t, func(c *config.MediaAuth) { c.URLSigning.Cookie = true })
	q := signed(t, p, res, Viewer{Subject: "u", Session: "s"}, t0.Add(time.Minute), "")
	pl := guardURL(p, res+"index.m3u8?"+q, res, nil)
	query, cookie := pl.access.PassOn(time.Hour)
	if query != "" || !strings.HasPrefix(cookie, GrantCookie+"=exp=") || !strings.Contains(cookie, "; Path="+res+"; Max-Age=") ||
		!strings.Contains(cookie, "; HttpOnly; SameSite=Lax") || strings.Contains(cookie, "Secure") {
		t.Fatalf("the cookie: %q %q", query, cookie)
	}
	resp := stdhttp.Response{Header: stdhttp.Header{"Set-Cookie": {cookie}}}
	ck := resp.Cookies()
	if len(ck) != 1 || ck[0].MaxAge != int((time.Hour+config.DefaultPlayGrace).Seconds()) || ck[0].Path != res {
		t.Fatalf("the cookie parsed: %+v", ck)
	}
	value := ck[0].Value
	set(t0.Add(2 * time.Minute))
	withCookie := func(v string) func(*stdhttp.Request) {
		return func(r *stdhttp.Request) { r.Header.Set("Cookie", "other=1; "+GrantCookie+"="+v) }
	}
	pl = guardURL(p, res+"v0.fmp4.m3u8", res, withCookie(value))
	allowed(t, "the cookie", pl)
	if q, c := pl.access.PassOn(time.Hour); q != "" || c != "" {
		t.Fatalf("a playlist fetched with the cookie hands on %q %q", q, c)
	}
	// Two cookies, of a resource above and this one: the one that holds.
	other := guardURL(p, res+"x", res, func(r *stdhttp.Request) {
		r.Header.Add("Cookie", GrantCookie+"=garbage")
		r.Header.Add("Cookie", GrantCookie+"="+value)
	})
	allowed(t, "two cookies", other)
	refusedFor(t, "the cookie on another item", guardURL(p, "/vod/films/b/v0.0.m4s", "/vod/films/b/", withCookie(value)), "signature")
	refusedFor(t, "a cookie that does not parse", guardURL(p, res+"v0.0.m4s", res, withCookie("x")), "signature")
	// A bad cookie beside a token goes to the token.
	allowed(t, "a bad cookie and a token", guardURL(p, res+"v0.0.m4s", res, func(r *stdhttp.Request) {
		withCookie("x")(r)
		r.Header.Set("Authorization", "Bearer "+staticToken)
	}))
	if pl := guardURL(p, res+"v0.0.m4s", res, nil); pl.rec.Response.StatusCode != 401 {
		t.Fatalf("no cookie: %d", pl.rec.Response.StatusCode)
	}
	// Over TLS the cookie is Secure, for players on another site.
	pl = guardURL(p, res+"index.m3u8?"+signed(t, p, res, Viewer{Subject: "u", Session: "s"}, t0.Add(time.Hour), ""), res, func(r *stdhttp.Request) {
		r.TLS = &tlsState
	})
	if _, c := pl.access.PassOn(0); !strings.Contains(c, "; Secure; SameSite=None") {
		t.Fatalf("over TLS: %q", c)
	}
}

// A token from the query is what a playlist hands on when no URL let it
// in, as before signed URLs.
func TestPassOnToken(t *testing.T) {
	p := urlPolicy(t, nil)
	pl := guardURL(p, res+"index.m3u8?token="+url.QueryEscape(staticToken), res, nil)
	allowed(t, "a token in the query", pl)
	if q, c := pl.access.PassOn(time.Hour); q != "token="+url.QueryEscape(staticToken) || c != "" {
		t.Fatalf("the token handed on: %q %q", q, c)
	}
	if !strings.Contains(pl.uri, "token=REDACTED") {
		t.Fatalf("the token in the RequestURI: %s", pl.uri)
	}
	var none Access
	if q, c := none.PassOn(0); q != "" || c != "" || !none.Expiry().IsZero() || none.URL() {
		t.Fatal("an empty Access hands something on")
	}
	// A URL access whose policy lost url_signing hands nothing on.
	clock(t, t0)
	pl = guardURL(p, res+"index.m3u8?"+signed(t, p, res, Viewer{Subject: "u"}, t0.Add(time.Minute), ""), res, nil)
	cfg := config.MediaAuth{Name: "site", Secret: "a single secret of 16+"}
	st, err := p.compile(&cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.st.Store(st)
	if q, c := pl.access.PassOn(0); q != "" || c != "" {
		t.Fatal("handed on without url_signing")
	}
}

func TestResourceOf(t *testing.T) {
	for path, want := range map[string]string{
		"/vod/films/films/bbb/index.m3u8": "/vod/films/films/bbb/",
		"/vod/films/a/v0.12.m4s":          "/vod/films/a/",
		"/live/cam.flv":                   "/live/cam/",
		"/flv/live/cam.flv":               "/flv/live/cam/",
		"/hls/live/cam/index.m3u8":        "/hls/live/cam/",
		"/index.m3u8":                     "",
		"/.flv":                           "",
		"/a/.flv":                         "/a/",
		"/a/":                             "",
		"vod/a/index.m3u8":                "",
		"/a/../b/index.m3u8":              "",
		"/a//index.m3u8":                  "",
		"/a/./index.m3u8":                 "",
		"/a b/index.m3u8":                 "",
		"/a/index.m3u8?x":                 "",
		"/a/" + strings.Repeat("b", 1100): "",
	} {
		got, ok := ResourceOf(path)
		if ok != (want != "") || ok && got != want {
			t.Errorf("ResourceOf(%q) = %q, %v; want %q", path, got, ok, want)
		}
	}
}

func TestSignURLErrors(t *testing.T) {
	clock(t, t0)
	p := urlPolicy(t, nil)
	exp := t0.Add(time.Minute)
	for what, err := range map[string]error{
		"resource":     signErr(p, "/a", Viewer{Subject: "u"}, exp, ""),
		"subject":      signErr(p, res, Viewer{}, exp, ""),
		"control":      signErr(p, res, Viewer{Subject: "a\x01"}, exp, ""),
		"session":      signErr(p, res, Viewer{Subject: "u", Session: "a b"}, exp, ""),
		"key":          signErr(p, res, Viewer{Subject: "u"}, exp, "k9"),
		"before 1970":  signErr(p, res, Viewer{Subject: "u"}, time.Unix(0, 0), ""),
		"single + key": signErr(urlPolicy(t, func(c *config.MediaAuth) { c.Keys = nil; c.Secret = "a single secret of 16+" }), res, Viewer{Subject: "u"}, exp, "k1"),
	} {
		if err == nil {
			t.Errorf("%s: no error", what)
		}
	}
	plain := newPolicy_(t, config.MediaAuth{Name: "x", Secret: "a single secret of 16+"})
	if _, err := plain.SignURL(res, Viewer{Subject: "u"}, exp, ""); !errors.Is(err, ErrNoURLSigning) || plain.URLSigning() || plain.URLTTL() != config.DefaultURLTTL {
		t.Fatalf("without url_signing: %v", err)
	}
	gone := &Policy{name: "gone"}
	gone.st.Store(&state{removed: true})
	if _, err := gone.SignURL(res, Viewer{Subject: "u"}, exp, ""); err != ErrUnavailable {
		t.Fatalf("a removed policy: %v", err)
	}
	if p.URLTTL() != config.DefaultURLTTL || !p.URLSigning() {
		t.Fatal("URLTTL")
	}
}

func signErr(p *Policy, r string, v Viewer, exp time.Time, key string) error {
	_, err := p.SignURL(r, v, exp, key)
	return err
}

func TestURLSigningCompileErrors(t *testing.T) {
	for what, cfg := range map[string]config.MediaAuth{
		"no secret":      {Name: "x", URLSigning: &config.URLSigning{}, Public: []string{"publish"}},
		"a bad referer":  {Name: "x", Secret: "a single secret of 16+", URLSigning: &config.URLSigning{Referers: []string{"example.com"}}},
		"a bad proxy":    {Name: "x", Secret: "a single secret of 16+", URLSigning: &config.URLSigning{TrustedProxies: []string{"nonsense"}}},
		"a bad sessions": {Name: "x", Secret: "a single secret of 16+", URLSigning: &config.URLSigning{MaxSessions: -1}},
	} {
		if _, err := New(&cfg); err == nil {
			t.Errorf("%s: compiled", what)
		}
	}
}

// Checking a signed URL takes no allocation.
func TestVerifyAllocations(t *testing.T) {
	clock(t, t0)
	p := urlPolicy(t, func(c *config.MediaAuth) {
		c.URLSigning.Referers = []string{"https://*.example.com"}
		c.URLSigning.BindAddress = "subnet"
	})
	q := signed(t, p, res, Viewer{Subject: "user-42", Session: "s1", Address: netip.MustParseAddr("203.0.113.7")}, t0.Add(time.Minute), "k2")
	r := httptest.NewRequest("GET", res+"v0.1.m4s?"+q, nil)
	r.RemoteAddr = "203.0.113.7:4000"
	r.Header.Set("Referer", "https://www.example.com/watch")
	st := p.state()
	if n := testing.AllocsPerRun(200, func() {
		var g grant
		if !g.parse(r.URL.RawQuery) {
			panic("parse")
		}
		if _, err := st.verify(r, res, &g, t0); err != nil {
			panic(err)
		}
		if err := p.sess.touch(g.sub, g.sid, res, r.RemoteAddr, g.exp, t0, st.url); err != nil {
			panic(err)
		}
	}); n != 0 {
		t.Fatalf("%v allocations a check", n)
	}
}

func BenchmarkVerifyURL(b *testing.B) {
	p, err := New(&config.MediaAuth{Name: "site", Keys: []config.MediaKey{{Key: "k1", Secret: "the first key, 16 bytes+"}}, URLSigning: &config.URLSigning{}})
	if err != nil {
		b.Fatal(err)
	}
	q, _ := p.SignURL(res, Viewer{Subject: "user-42", Session: "s1"}, time.Now().Add(time.Hour), "")
	r := httptest.NewRequest("GET", res+"v0.1.m4s?"+q, nil)
	st := p.state()
	t := time.Now()
	b.ReportAllocs()
	for b.Loop() {
		var g grant
		g.parse(r.URL.RawQuery)
		if _, err := st.verify(r, res, &g, t); err != nil {
			b.Fatal(err)
		}
		_ = p.sess.touch(g.sub, g.sid, res, r.RemoteAddr, g.exp, t, st.url)
	}
}

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		"/a":                         "/a",
		"/a?":                        "/a?",
		"/a?token=x&b=1&sig=yy&sigx": "/a?token=REDACTED&b=1&sig=REDACTED&sigx",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.RequestURI = in
		Redact(r)
		if r.RequestURI != want {
			t.Errorf("Redact(%q) = %q, want %q", in, r.RequestURI, want)
		}
	}
	r := httptest.NewRequest("GET", "/a?token=REDACTED&sig=REDACTED&x=1", nil)
	if n := testing.AllocsPerRun(100, func() { Redact(r) }); n != 0 {
		t.Fatalf("Redact of a redacted URI: %v allocations", n)
	}
}

// The small parts: errors, names, patterns and the sweep's pacing.
func TestURLParts(t *testing.T) {
	if e := urlErrors[reasonExpired]; e.Error() != "mediaauth: the playback URL is refused: expired" || Status(e) != 403 {
		t.Fatalf("an URLError: %v", e)
	}
	if Status(ErrSessionsFull) != 503 {
		t.Fatal("ErrSessionsFull")
	}
	var g grant
	if g.parse("exp=1&sub=%zz&sig=" + strings.Repeat("a", 22)) {
		t.Fatal("a bad escape parsed")
	}
	if validKeyName("a b") || validKeyName(strings.Repeat("a", 65)) || !validKeyName("a.b_c~d-e") {
		t.Fatal("validKeyName")
	}
	if !matchBytes("a*b*c", []byte("aXbYbZc")) || matchBytes("a*b", []byte("aXc")) || !matchBytes("a**", []byte("a")) {
		t.Fatal("matchBytes")
	}
	var tbl sessionTable
	tbl.sweep(int64(time.Hour), time.Minute)
	first := tbl.swept.Load()
	tbl.sweep(int64(time.Hour)+1, time.Minute)
	if tbl.swept.Load() != first {
		t.Fatal("swept twice within sweepEvery")
	}
}

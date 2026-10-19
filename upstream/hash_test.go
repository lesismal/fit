package upstream

import (
	"fmt"
	stdhttp "net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// within reports whether got is within tolerance (a fraction) of want.
func within(got, want int, tolerance float64) bool {
	d := float64(got - want)
	return d <= tolerance*float64(want) && -d <= tolerance*float64(want)
}

func TestRandom(t *testing.T) {
	got := counts(NewBalancer(Random, backends(1, 1, 1), ""), 30000)
	for _, k := range []string{"a", "b", "c"} {
		if !within(got[k], 10000, 0.05) {
			t.Errorf("random: %v", got)
		}
	}
	got = counts(NewBalancer(Random, backends(3, 1), ""), 40000)
	if !within(got["a"], 30000, 0.03) || !within(got["b"], 10000, 0.08) {
		t.Errorf("random by weight 3 and 1: %v", got)
	}
}

func TestP2C(t *testing.T) {
	bs := backends(1, 1, 1)
	b := NewBalancer(P2C, bs, "")
	// The busiest is never the less busy of two.
	bs[0].active.Store(10)
	if got := counts(b, 3000); got["a"] != 0 || !within(got["b"], 1500, 0.1) {
		t.Fatalf("a busy: %v", got)
	}
	// Requests that stay keep the backends level.
	bs[0].active.Store(0)
	for range 3000 {
		b.Pick(nil).active.Add(1)
	}
	for _, x := range bs {
		if !within(int(x.active.Load()), 1000, 0.02) {
			t.Fatalf("under way: %d %d %d", bs[0].active.Load(), bs[1].active.Load(), bs[2].active.Load())
		}
	}
	// By weight: a of weight 3 carries three times what b does.
	bs = backends(3, 1)
	b = NewBalancer(P2C, bs, "")
	for range 4000 {
		b.Pick(nil).active.Add(1)
	}
	if a := bs[0].active.Load(); !within(int(a), 3000, 0.01) {
		t.Fatalf("by weight: %d and %d", a, bs[1].active.Load())
	}
	if got := counts(NewBalancer(P2C, backends(1), ""), 10); got["a"] != 10 {
		t.Fatalf("one backend: %v", got)
	}
}

// keyed returns a request whose key, for policy, is key.
func keyed(policy, key string) *stdhttp.Request {
	r := &stdhttp.Request{Header: stdhttp.Header{}, URL: &url.URL{Path: "/"}, RemoteAddr: "192.0.2.1:1000"}
	switch policy {
	case IPHash:
		r.RemoteAddr = key + ":4321"
	case URIHash:
		r.URL.Path, r.URL.RawQuery = "/"+key, "q=1"
	case HeaderHash:
		r.Header.Set("X-User", key)
	case CookieHash:
		r.Header.Set("Cookie", "a=b; user="+key+"; c=d")
	}
	return r
}

func hashKey(policy string) string {
	switch policy {
	case HeaderHash:
		return "x-user"
	case CookieHash:
		return "user"
	}
	return ""
}

var hashPolicies = []string{IPHash, URIHash, HeaderHash, CookieHash}

func keyOf(policy string, i int) string {
	if policy == IPHash {
		return fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255)
	}
	return fmt.Sprint("key", i)
}

func TestHashDistribution(t *testing.T) {
	for _, policy := range hashPolicies {
		b := NewBalancer(policy, backends(1, 1, 1, 1), hashKey(policy))
		got := map[string]int{}
		for i := range 40000 {
			got[b.Pick(keyed(policy, keyOf(policy, i))).Address]++
		}
		for _, k := range []string{"a", "b", "c", "d"} {
			if !within(got[k], 10000, 0.2) {
				t.Errorf("%s: %v", policy, got)
			}
		}
		b = NewBalancer(policy, backends(2, 1, 1), hashKey(policy))
		got = map[string]int{}
		for i := range 40000 {
			got[b.Pick(keyed(policy, keyOf(policy, i))).Address]++
		}
		if !within(got["a"], 20000, 0.15) {
			t.Errorf("%s by weight 2, 1, 1: %v", policy, got)
		}
	}
}

// TestHashStable: the same key goes to the same backend; adding a backend
// moves only keys to it, about its share of them; a backend down moves only
// its own keys, and they come back with it.
func TestHashStable(t *testing.T) {
	for _, policy := range hashPolicies {
		three := backends(1, 1, 1)
		b3 := NewBalancer(policy, three, hashKey(policy))
		b4 := NewBalancer(policy, append(backends(1, 1, 1), &Backend{Address: "d", Weight: 1}), hashKey(policy))
		const n = 20000
		before := make([]string, n)
		moved := 0
		for i := range n {
			r := keyed(policy, keyOf(policy, i))
			before[i] = b3.Pick(r).Address
			if again := b3.Pick(r).Address; again != before[i] {
				t.Fatalf("%s: key %d went to %s, then %s", policy, i, before[i], again)
			}
			if after := b4.Pick(r).Address; after != before[i] {
				if after != "d" {
					t.Fatalf("%s: key %d moved from %s to %s, not to the new backend", policy, i, before[i], after)
				}
				moved++
			}
		}
		if !within(moved, n/4, 0.3) {
			t.Errorf("%s: %d of %d keys moved to the fourth backend", policy, moved, n)
		}

		three[1].failed(1, time.Hour)
		for i := range n {
			got := b3.Pick(keyed(policy, keyOf(policy, i))).Address
			if before[i] != "b" && got != before[i] || got == "b" {
				t.Fatalf("%s with b down: key %d went to %s, before to %s", policy, i, got, before[i])
			}
		}
		three[1].succeeded()
		for i := range n {
			if got := b3.Pick(keyed(policy, keyOf(policy, i))).Address; got != before[i] {
				t.Fatalf("%s with b back: key %d went to %s, before to %s", policy, i, got, before[i])
			}
		}
	}
}

func TestHashKeys(t *testing.T) {
	b := NewBalancer(IPHash, backends(1, 1, 1, 1, 1, 1, 1, 1), "")
	pick := func(addr string) string {
		return b.Pick(&stdhttp.Request{RemoteAddr: addr, URL: &url.URL{}}).Address
	}
	// The port does not count.
	if pick("10.0.0.1:1") != pick("10.0.0.1:2") || pick("[2001:db8::1]:1") != pick("[2001:db8::1]:2") {
		t.Fatal("ip_hash depends on the port")
	}
	if hostOf("[2001:db8::1]:80") != "2001:db8::1" || hostOf("10.0.0.1:80") != "10.0.0.1" || hostOf("pipe") != "pipe" {
		t.Fatal("hostOf")
	}
	// uri_hash counts the query.
	u := NewBalancer(URIHash, backends(1, 1, 1, 1, 1, 1, 1, 1), "")
	diff := 0
	for i := range 20 {
		r1 := &stdhttp.Request{URL: &url.URL{Path: "/p", RawQuery: fmt.Sprint("a=", i)}}
		r2 := &stdhttp.Request{URL: &url.URL{Path: "/p", RawQuery: fmt.Sprint("a=", i+100)}}
		if u.Pick(r1) != u.Pick(r2) {
			diff++
		}
	}
	if diff == 0 {
		t.Fatal("uri_hash ignores the query")
	}

	h := stdhttp.Header{"Cookie": {`a=1; user="x"; b=2`, "c=3"}}
	for name, want := range map[string]string{"a": "1", "user": "x", "b": "2", "c": "3", "d": "", "us": ""} {
		if got := cookieValue(h, name); got != want {
			t.Errorf("cookie %s = %q, want %q", name, got, want)
		}
	}

	// A request without its key goes in turn.
	for _, policy := range []string{HeaderHash, CookieHash} {
		b := NewBalancer(policy, backends(1, 1, 1), hashKey(policy))
		got := map[string]int{}
		for range 300 {
			got[b.Pick(keyed(policy, "")).Address]++
		}
		if got["a"] != 100 || got["b"] != 100 || got["c"] != 100 {
			t.Errorf("%s without a key: %v", policy, got)
		}
		if b.Pick(nil) == nil {
			t.Errorf("%s: no pick without a request", policy)
		}
	}
}

func TestNewPoliciesAllocs(t *testing.T) {
	for _, policy := range append([]string{Random, P2C}, hashPolicies...) {
		b := NewBalancer(policy, backends(3, 2, 1, 1), hashKey(policy))
		r := keyed(policy, "someone")
		if n := testing.AllocsPerRun(1000, func() { b.Pick(r) }); n != 0 {
			t.Errorf("%s: %v allocations a pick", policy, n)
		}
	}
	bs := backends(1, 1)
	s := newSticky(&config.StickyCookie{}, bs)
	r := &stdhttp.Request{Header: stdhttp.Header{"Cookie": {"fit_sticky=" + StickyID("b")}}}
	if n := testing.AllocsPerRun(1000, func() { s.pick(r) }); n != 0 {
		t.Errorf("sticky: %v allocations a pick", n)
	}
}

func TestPickSkipsDownNewPolicies(t *testing.T) {
	for _, policy := range []string{Random, P2C} {
		bs := backends(1, 1, 1)
		bal := NewBalancer(policy, bs, "")
		bs[1].failed(1, time.Hour)
		if got := counts(bal, 300); got["b"] != 0 || got["a"]+got["c"] != 300 {
			t.Errorf("%s with b down: %v", policy, got)
		}
		bs[0].failed(1, time.Hour)
		bs[2].failed(1, time.Hour)
		if got := counts(bal, 300); got["a"] == 0 || got["b"] == 0 || got["c"] == 0 {
			t.Errorf("%s with all down: %v", policy, got)
		}
	}
	bs := backends(3, 1)
	bal := NewBalancer(Random, bs, "")
	bs[0].failed(1, time.Hour)
	if got := counts(bal, 100); got["b"] != 100 {
		t.Errorf("weighted random with a down: %v", got)
	}
}

func TestSticky(t *testing.T) {
	a, b := newBackend(t, hello("a")), newBackend(t, hello("b"))
	p := newPool(t, `{"addresses": [%s], "sticky_cookie": {"name": "s", "path": "/app", "secure": true}}`, a, b)
	req := func(cookie string) *stdhttp.Request {
		r := &stdhttp.Request{Header: stdhttp.Header{}, URL: &url.URL{Path: "/"}}
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		return r
	}
	pick := func(r *stdhttp.Request) (*Backend, string) {
		b := p.Pick(r)
		defer p.Done(b, false)
		return b, p.SetCookie(r, b)
	}

	// Without a cookie: in turn, each answer naming its backend.
	r := req("")
	first, set := pick(r)
	if want := "s=" + StickyID(first.Address) + "; Path=/app; HttpOnly; SameSite=Lax; Secure"; set != want {
		t.Fatalf("Set-Cookie %q, want %q", set, want)
	}
	if second, _ := pick(req("")); second == first {
		t.Fatal("requests without a cookie did not go in turn")
	}
	// With it: always there, and no cookie set again.
	withCookie := req("x=1; s=" + StickyID(first.Address))
	for range 10 {
		if got, set := pick(withCookie); got != first || set != "" {
			t.Fatalf("with the cookie: %s %q", got.Address, set)
		}
	}
	// Its backend down: another, and a cookie that names it.
	first.failed(1, time.Hour)
	other, set := pick(withCookie)
	if other == first || !strings.HasPrefix(set, "s="+StickyID(other.Address)+";") {
		t.Fatalf("with its backend down: %s %q", other.Address, set)
	}
	first.succeeded()
	if got, _ := pick(withCookie); got != first {
		t.Fatal("not back once its backend is up")
	}
	// A value that names nothing goes by the policy.
	if got, set := pick(req("s=nothing")); got == nil || set == "" {
		t.Fatal("unknown cookie value")
	}
}

func BenchmarkPickNew(b *testing.B) {
	for _, policy := range append([]string{Random, P2C}, hashPolicies...) {
		bal := NewBalancer(policy, backends(3, 2, 1, 1), hashKey(policy))
		r := keyed(policy, "someone")
		b.Run(policy, func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					bal.Pick(r)
				}
			})
		})
	}
}

func TestHashedPolicyConfig(t *testing.T) {
	for _, tt := range []struct{ doc, want string }{
		{`{"addresses": ["a:1"], "load_balancing": "header_hash"}`, `header_hash needs hash_key, the name of the header to hash`},
		{`{"addresses": ["a:1"], "load_balancing": "cookie_hash", "hash_key": "a b"}`, `"a b" is not a valid cookie name`},
		{`{"addresses": ["a:1"], "hash_key": "x"}`, `hash_key is for header_hash and cookie_hash`},
		{`{"addresses": ["a:1"], "sticky_cookie": {"name": "a;b", "path": "x"}}`, `"a;b" is not a valid cookie name`},
		{`{"addresses": ["a:1"], "sticky_cookie": {"path": "/a;b"}}`, `"/a;b" is not a cookie path`},
	} {
		if _, err := decode(t, tt.doc); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.doc, err)
		}
	}
}

// TestP2COneUp: with one backend up, p2c takes it every time.
func TestP2COneUp(t *testing.T) {
	bs := backends(1, 1, 1)
	b := NewBalancer(P2C, bs, "")
	bs[0].failed(1, time.Hour)
	bs[2].failed(1, time.Hour)
	if got := counts(b, 1000); got["b"] != 1000 {
		t.Fatalf("only b up: %v", got)
	}
}

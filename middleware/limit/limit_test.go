package limit

import (
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t testing.TB, name, params string) (module.Middleware, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "`+name+`", "params": `+params+`}`), "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		return nil, err
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(module.Middleware), nil
}

func mustLoad[T any](t testing.TB, name, params string) T {
	t.Helper()
	m, err := load(t, name, params)
	if err != nil {
		t.Fatal(err)
	}
	return m.(T)
}

var ok = fibhttp.HandlerFunc(func(c *fibhttp.Context) { _ = c.Respond(200, "text/plain", []byte("ok")) })

// fakeClock is a clock for Rate that tests move by hand.
type fakeClock struct{ now atomic.Int64 }

func (f *fakeClock) clock() int64            { return f.now.Load() }
func (f *fakeClock) advance(d time.Duration) { f.now.Add(int64(d)) }

func request(remote string, header ...string) *stdhttp.Request {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = remote
	for i := 0; i < len(header); i += 2 {
		r.Header.Add(header[i], header[i+1])
	}
	return r
}

func TestRateBucket(t *testing.T) {
	rl := mustLoad[*Rate](t, "rate_limit", `{"rate": 1, "burst": 3}`)
	var clk fakeClock
	rl.clock = clk.clock
	h := rl.Wrap(ok)
	serve := func() (int, string) {
		res := fibtest.ServeRequest(h, request("192.0.2.1:1")).Response
		return res.StatusCode, res.Header.Get("Retry-After")
	}
	for i := range 3 {
		if s, _ := serve(); s != 200 {
			t.Fatalf("request %d of the burst: %d", i, s)
		}
	}
	if s, after := serve(); s != 429 || after != "1" {
		t.Fatalf("past the burst: %d, Retry-After %q", s, after)
	}
	clk.advance(500 * time.Millisecond)
	if s, after := serve(); s != 429 || after != "1" {
		t.Fatalf("half a token later: %d, Retry-After %q", s, after)
	}
	clk.advance(500 * time.Millisecond)
	if s, _ := serve(); s != 200 {
		t.Fatalf("a token later: %d", s)
	}
	if s, _ := serve(); s != 429 {
		t.Fatalf("that token again: %d", s)
	}
	// Idle long, the bucket fills to the burst and no further.
	clk.advance(time.Hour)
	for i := range 3 {
		if s, _ := serve(); s != 200 {
			t.Fatalf("request %d after an hour: %d", i, s)
		}
	}
	if s, _ := serve(); s != 429 {
		t.Fatalf("past the burst after an hour: %d", s)
	}
}

func TestRateSlow(t *testing.T) {
	rl := mustLoad[*Rate](t, "rate_limit", `{"rate": 0.01, "status": 503}`)
	var clk fakeClock
	rl.clock = clk.clock
	h := rl.Wrap(ok)
	if s := fibtest.ServeRequest(h, request("192.0.2.1:1")).Response.StatusCode; s != 200 {
		t.Fatalf("first: %d", s)
	}
	res := fibtest.ServeRequest(h, request("192.0.2.1:1")).Response
	if res.StatusCode != 503 || res.Header.Get("Retry-After") != "100" {
		t.Fatalf("second: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
}

func TestRateKeys(t *testing.T) {
	for _, tt := range []struct {
		name, params string
		requests     []*stdhttp.Request
		want         []int
	}{
		{
			name:   "by ip",
			params: `{"rate": 1, "burst": 1}`,
			requests: []*stdhttp.Request{
				request("192.0.2.1:1"), request("192.0.2.1:2"), request("192.0.2.2:1"),
				request("[::ffff:192.0.2.2]:1"), request("[2001:db8::1]:1"),
			},
			want: []int{200, 429, 200, 429, 200},
		},
		{
			name:   "by route",
			params: `{"rate": 1, "burst": 2, "key": "route"}`,
			requests: []*stdhttp.Request{
				request("192.0.2.1:1"), request("192.0.2.2:1"), request("192.0.2.3:1"),
			},
			want: []int{200, 200, 429},
		},
		{
			name:   "by header, and by ip without it",
			params: `{"rate": 1, "burst": 1, "key": "header:x-api-key"}`,
			requests: []*stdhttp.Request{
				request("192.0.2.1:1", "X-Api-Key", "a"),
				request("192.0.2.2:1", "X-Api-Key", "a"),
				request("192.0.2.1:1", "X-Api-Key", "b"),
				request("192.0.2.1:1"),
				request("192.0.2.1:1"),
				request("192.0.2.1:1", "X-Api-Key", strings.Repeat("k", 200)),
				request("192.0.2.2:1", "X-Api-Key", strings.Repeat("k", 200)),
				request("192.0.2.2:1", "X-Api-Key", strings.Repeat("k", 199)+"j"),
			},
			want: []int{200, 429, 200, 200, 429, 200, 429, 200},
		},
		{
			name:   "behind trusted proxies",
			params: `{"rate": 1, "burst": 1, "trusted_proxies": ["10.0.0.0/8"]}`,
			requests: []*stdhttp.Request{
				request("10.0.0.1:1", "X-Forwarded-For", "192.0.2.1"),
				request("10.0.0.2:1", "X-Forwarded-For", "192.0.2.1, 10.0.0.9"),
				request("10.0.0.1:1", "X-Forwarded-For", "192.0.2.7, 192.0.2.2"),
				request("192.0.2.3:1", "X-Forwarded-For", "192.0.2.9"),
				request("192.0.2.4:1", "X-Forwarded-For", "192.0.2.3"),
				request("10.0.0.1:1"),
				request("10.0.0.1:1", "X-Forwarded-For", "10.9.9.9"), // all trusted: the first is the client
			},
			want: []int{200, 429, 200, 200, 200, 200, 200},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rl := mustLoad[*Rate](t, "rate_limit", tt.params)
			var clk fakeClock
			rl.clock = clk.clock
			h := rl.Wrap(ok)
			for i, r := range tt.requests {
				if s := fibtest.ServeRequest(h, r).Response.StatusCode; s != tt.want[i] {
					t.Errorf("request %d (%s %v): %d, want %d", i, r.RemoteAddr, r.Header, s, tt.want[i])
				}
			}
		})
	}
}

func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("2001:db8::/32")}
	for _, tt := range []struct {
		remote string
		xff    []string
		want   string
	}{
		{"192.0.2.1:1", []string{"198.51.100.1"}, "192.0.2.1"},
		{"10.0.0.1:1", nil, "10.0.0.1"},
		{"10.0.0.1:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"10.0.0.1:1", []string{"198.51.100.1, 198.51.100.2"}, "198.51.100.2"},
		{"10.0.0.1:1", []string{"198.51.100.1", "198.51.100.2 ,10.1.1.1"}, "198.51.100.2"},
		{"10.0.0.1:1", []string{"198.51.100.1,10.1.1.1", "10.2.2.2"}, "198.51.100.1"},
		{"10.0.0.1:1", []string{"10.1.1.1"}, "10.1.1.1"},
		{"10.0.0.1:1", []string{"garbage, 10.1.1.1"}, "10.1.1.1"},
		{"10.0.0.1:1", []string{"198.51.100.1, garbage"}, "10.0.0.1"},
		{"[2001:db8::1]:1", []string{"::ffff:198.51.100.1"}, "198.51.100.1"},
		{"10.0.0.1:1", []string{",,"}, "10.0.0.1"},
	} {
		r := request(tt.remote)
		r.Header["X-Forwarded-For"] = tt.xff
		if got := clientIP(r, trusted); got.String() != tt.want {
			t.Errorf("%s, %q: %s, want %s", tt.remote, tt.xff, got, tt.want)
		}
	}
}

func TestRateMemoryBounded(t *testing.T) {
	rl := mustLoad[*Rate](t, "rate_limit", `{"rate": 1, "max_keys": 640}`)
	var clk fakeClock
	rl.clock = clk.clock
	for i := range 100000 {
		a := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
		rl.buckets.take(bucketKey{addr: a}, clk.clock())
		clk.advance(time.Microsecond)
	}
	n := 0
	for i := range rl.buckets.shards {
		n += len(rl.buckets.shards[i].m)
	}
	if n == 0 || n > 640 {
		t.Fatalf("%d keys kept, want at most 640", n)
	}
}

func TestEvictPrefersFull(t *testing.T) {
	b := &buckets{rate: 1, burst: 2 * token, maxPerShard: 2}
	sh := &b.shards[0]
	full, busy := bucketKey{s: "full"}, bucketKey{s: "busy"}
	for range 20 { // map order is random: try it many times
		sh.m = map[bucketKey]bucket{full: {tokens: 2 * token, last: 0}, busy: {tokens: 0, last: 0}}
		b.evict(sh, 0)
		if _, ok := sh.m[busy]; !ok || len(sh.m) != 1 {
			t.Fatalf("evicted the busy key: %v", sh.m)
		}
	}
	// With none full, one goes all the same.
	sh.m = map[bucketKey]bucket{{s: "a"}: {}, {s: "b"}: {}}
	b.evict(sh, 0)
	if len(sh.m) != 1 {
		t.Fatalf("nothing evicted: %v", sh.m)
	}
}

func TestRateAllocs(t *testing.T) {
	rl := mustLoad[*Rate](t, "rate_limit", `{"rate": 1000000, "trusted_proxies": ["10.0.0.0/8"]}`)
	for _, r := range []*stdhttp.Request{
		request("192.0.2.1:1"),
		request("10.0.0.1:1", "X-Forwarded-For", "198.51.100.1, 10.0.0.2"),
	} {
		rl.buckets.take(rl.key(r), rl.clock())
		if n := testing.AllocsPerRun(100, func() { rl.buckets.take(rl.key(r), rl.clock()) }); n != 0 {
			t.Errorf("%s: %v allocations", r.RemoteAddr, n)
		}
	}
}

func TestParamErrors(t *testing.T) {
	for _, tt := range []struct{ name, params, want string }{
		{"rate_limit", `{"rate": 0}`, "0 is below 0.001"},
		{"rate_limit", `{"rate": 1, "key": "cookie:a"}`, `"cookie:a" is not ip, route or header:Name`},
		{"rate_limit", `{"rate": 1, "key": "header:"}`, `"header:" is not ip, route or header:Name`},
		{"rate_limit", `{"rate": 1, "trusted_proxies": ["x"]}`, `"x" is not an IP address or a CIDR prefix`},
		{"rate_limit", `{"rate": 1, "status": 200}`, "200 is not an error status"},
		{"rate_limit", `{"rate": 1, "burst": -1}`, "burst"},
		{"concurrency_limit", `{}`, "max"},
		{"concurrency_limit", `{"max": 0}`, "max"},
		{"concurrency_limit", `{"max": 1, "status": 302}`, "302 is not an error status"},
		{"body_limit", `{}`, "max_bytes"},
	} {
		_, err := load(t, tt.name, tt.params)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %s: %v; want %q", tt.name, tt.params, err, tt.want)
		}
	}
}

func TestConcurrency(t *testing.T) {
	cl := mustLoad[*Concurrency](t, "concurrency_limit", `{"max": 2}`)
	answer := make(chan *fibhttp.Context, 4)
	h := cl.Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		if c.Request.URL.Path == "/now" {
			_ = c.Respond(200, "text/plain", nil)
			return
		}
		c.Retain() // answered later, from the test
		answer <- c
	}))
	// Requests answered inline give their place back.
	for range 5 {
		if s := fibtest.Serve(h, "GET", "/now").Response.StatusCode; s != 200 {
			t.Fatalf("inline: %d", s)
		}
	}
	if n := cl.InFlight(); n != 0 {
		t.Fatalf("%d in flight after inline requests", n)
	}
	// Two held: a third is refused.
	recs := make(chan *fibtest.Recorder, 2)
	for range 2 {
		go func() { recs <- fibtest.Serve(h, "GET", "/later") }()
	}
	held := []*fibhttp.Context{<-answer, <-answer}
	if s := fibtest.Serve(h, "GET", "/now").Response.StatusCode; s != 429 {
		t.Fatalf("past the limit: %d", s)
	}
	// One answered elsewhere: there is room again.
	_ = held[0].Respond(200, "text/plain", nil)
	held[0].Release()
	<-recs
	if s := fibtest.Serve(h, "GET", "/now").Response.StatusCode; s != 200 {
		t.Fatalf("after one answered: %d", s)
	}
	_ = held[1].Respond(200, "text/plain", nil)
	held[1].Release()
	<-recs
	if n := cl.InFlight(); n != 0 {
		t.Fatalf("%d in flight at the end", n)
	}
}

func TestBody(t *testing.T) {
	h := mustLoad[*Body](t, "body_limit", `{"max_bytes": 5}`).Wrap(ok)
	for _, tt := range []struct {
		length int64
		want   int
	}{{0, 200}, {5, 200}, {6, 413}, {1 << 40, 413}, {-1, 200}} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat("x", int(max(tt.length, 0)%100))))
		r.ContentLength = tt.length
		if s := fibtest.ServeRequest(h, r).Response.StatusCode; s != tt.want {
			t.Errorf("Content-Length %d: %d, want %d", tt.length, s, tt.want)
		}
	}
}

func BenchmarkRate(b *testing.B) {
	for _, keys := range []int{1, 1000, 1000000} {
		b.Run(fmt.Sprintf("keys=%d", keys), func(b *testing.B) {
			rl := mustLoad[*Rate](b, "rate_limit", `{"rate": 1000000000, "max_keys": 100000}`)
			var next atomic.Uint32
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					i := next.Add(1) % uint32(keys)
					a := netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)})
					rl.buckets.take(bucketKey{addr: a}, rl.clock())
				}
			})
		})
	}
}

// BenchmarkConcurrency serves an inline handler through fibtest, bare and
// behind the limit: the difference is the limit's cost.
func BenchmarkConcurrency(b *testing.B) {
	cl := mustLoad[*Concurrency](b, "concurrency_limit", `{"max": 1000000}`)
	for name, h := range map[string]module.Handler{"none": ok, "limit": cl.Wrap(ok)} {
		b.Run(name, func(b *testing.B) {
			r := httptest.NewRequest("GET", "/", nil)
			b.ReportAllocs()
			for b.Loop() {
				fibtest.ServeRequest(h, r)
			}
		})
	}
}

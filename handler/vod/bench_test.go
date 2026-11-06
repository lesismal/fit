package vod

import (
	"net/http/httptest"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
)

// BenchmarkServeSegment is the handler's own cost for a segment the cache
// holds, as most requests are: open, with no auth; with a bearer token; and
// with a signed URL (signed.go). The request is served on the benchmark's
// goroutine, through fib's Context, with no network.
func BenchmarkServeSegment(b *testing.B) {
	const seg = "/vod/films/films/movie/v0.0.m4s"
	run := func(b *testing.B, f *fit, target string, hdr ...string) {
		v := f.handler(b)
		serve := func() int {
			r := httptest.NewRequest("GET", target, nil)
			for i := 0; i+1 < len(hdr); i += 2 {
				r.Header.Set(hdr[i], hdr[i+1])
			}
			return fibtest.ServeRequest(fibhttp.HandlerFunc(v.ServeHTTP), r).Response.StatusCode
		}
		if s := serve(); s != 200 {
			b.Fatalf("%s: %d", target, s)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			serve()
		}
	}
	b.Run("open", func(b *testing.B) {
		run(b, startVOD(b, copyLib(b), "", `{"library": "films"}`, ""), seg)
	})
	b.Run("token", func(b *testing.B) {
		run(b, startVOD(b, copyLib(b), "", `{"library": "films", "auth": "site"}`, authConf), seg, "Authorization", "Bearer "+viewToken)
	})
	benchSigned(b, run)
}

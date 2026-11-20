package compress

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"net/http/httptest"
	"strings"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

// records is n bytes of a JSON array of records, which compresses as an
// API's responses do, about to a third: not a line repeated, which
// compresses far better than anything sent.
func records(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	words := strings.Fields("alpha beta gamma delta order shipped pending user admin product category price stock warehouse region north south east west created updated")
	var b bytes.Buffer
	b.WriteString("[")
	for b.Len() < n {
		fmt.Fprintf(&b, `{"id":%d,"uuid":"%016x%016x","name":"%s %s","status":"%s","price":%.2f,"qty":%d,"ts":"2026-11-%02dT%02d:%02d:%02dZ","tags":["%s","%s"]},`,
			r.IntN(1e7), r.Uint64(), r.Uint64(), words[r.IntN(len(words))], words[r.IntN(len(words))], words[r.IntN(len(words))],
			r.Float64()*1000, r.IntN(500), r.IntN(28)+1, r.IntN(24), r.IntN(60), r.IntN(60), words[r.IntN(len(words))], words[r.IntN(len(words))])
	}
	return b.Bytes()[:n]
}

// benchHandler is the middleware with params in front of a handler that
// sends body whole.
func benchHandler(b *testing.B, params string, body []byte) fibhttp.Handler {
	v, err := config.ParseValue([]byte(`{"module": "compress", "params": `+params+`}`), "middleware")
	if err != nil {
		b.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		b.Fatal(err)
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		b.Fatal(err)
	}
	return m.(module.Middleware).Wrap(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		_ = c.Respond(200, "application/json", body)
	}))
}

// BenchmarkCompressBody compresses JSON bodies sent whole, by size,
// encoding and level.
func BenchmarkCompressBody(b *testing.B) {
	for _, size := range []int{1 << 10, 16 << 10, 64 << 10} {
		body := records(size)
		for _, enc := range []string{"none", Gzip, Brotli, Zstd} {
			for _, level := range []string{"fastest", "default", "better"} {
				if enc == "none" && level != "fastest" {
					continue
				}
				h := benchHandler(b, `{"level": "`+level+`", "min_bytes": 512}`, body)
				r := httptest.NewRequest("GET", "/", nil)
				if enc != "none" {
					r.Header.Set("Accept-Encoding", enc)
				}
				b.Run(fmt.Sprintf("%dK/%s/%s", size>>10, enc, level), func(b *testing.B) {
					b.SetBytes(int64(len(body)))
					b.ReportAllocs()
					var n int
					for b.Loop() {
						n = len(fibtest.ServeRequest(h, r).Response.Body)
					}
					b.ReportMetric(float64(n)/float64(len(body)), "ratio")
				})
			}
		}
	}
}

// BenchmarkCompressParallel compresses a small body on every core at once,
// where what the responses share, the pool and the count in flight, would
// show.
func BenchmarkCompressParallel(b *testing.B) {
	body := records(2 << 10)
	h := benchHandler(b, `{"level": "fastest", "min_bytes": 512}`, body)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", Gzip)
		for pb.Next() {
			fibtest.ServeRequest(h, r)
		}
	})
}

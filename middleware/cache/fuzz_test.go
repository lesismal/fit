package cache

import (
	stdhttp "net/http"
	"testing"
	"time"
)

// FuzzDirectives checks that any Cache-Control, Vary and Cache-Tag fields
// parse without a panic into bounded values.
func FuzzDirectives(f *testing.F) {
	f.Add("max-age=60, s-maxage=\"30\", stale-while-revalidate=99999999999999999999, no-cache", "Accept-Encoding, *", "a, b")
	f.Add("max-stale, min-fresh=-1,,=,", "", "")
	f.Fuzz(func(t *testing.T, cc, vary, tags string) {
		d := parseDirectives([]string{cc})
		for _, v := range []time.Duration{d.maxAge, d.sMaxAge, d.maxStale, d.minFresh, d.staleWhileRevalidate, d.staleIfError} {
			if v < -1 || v > maxDelta {
				t.Fatalf("%q: %v", cc, v)
			}
		}
		h := stdhttp.Header{"Vary": {vary}, "Cache-Tag": {tags}, "Cache-Control": {cc}}
		_, _ = varyNames(h)
		if got := tagsOf(h); len(got) > maxTags {
			t.Fatalf("%d tags", len(got))
		}
		_, _ = freshness(h, d, 200, time.Now(), 0)
	})
}

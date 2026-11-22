package static

import (
	"math/rand/v2"
	stdhttp "net/http"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAppendHTTPTime checks appendHTTPTime against AppendFormat, over
// times at random from year 0 to past 9999, and in zones not UTC.
func TestAppendHTTPTime(t *testing.T) {
	zone := time.FixedZone("x", -7*3600-1800)
	check := func(tm time.Time) {
		got := string(appendHTTPTime([]byte("x"), tm))
		want := "x" + tm.UTC().Format(stdhttp.TimeFormat)
		if got != want {
			t.Fatalf("%v: %q, want %q", tm, got, want)
		}
	}
	for _, tm := range []time.Time{{}, time.Unix(0, 0), time.Date(9999, 12, 31, 23, 59, 59, 999, time.UTC), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)} {
		check(tm)
	}
	r := rand.New(rand.NewPCG(1, 2))
	for range 100000 {
		tm := time.Unix(r.Int64N(320000000000)-62135596800, r.Int64N(1e9))
		check(tm)
		check(tm.In(zone))
	}
}

func BenchmarkAppendHTTPTime(b *testing.B) {
	tm := time.Now()
	buf := make([]byte, 0, 64)
	b.Run("fast", func(b *testing.B) {
		for range b.N {
			buf = appendHTTPTime(buf[:0], tm)
		}
	})
	b.Run("AppendFormat", func(b *testing.B) {
		for range b.N {
			buf = tm.UTC().AppendFormat(buf[:0], stdhttp.TimeFormat)
		}
	})
}

// TestJoinRoot checks joinRoot against filepath.Join for names resolve
// cleans.
func TestJoinRoot(t *testing.T) {
	for _, root := range []string{"/", "/srv", "/srv/www"} {
		s := &Static{root: root}
		for _, name := range []string{"", "a", "a/b.txt", "d/", "x/../y", "./z", "a//b"} {
			clean := strings.TrimPrefix(path.Clean("/"+name), "/")
			if got, want := s.joinRoot(clean), filepath.Join(root, filepath.FromSlash(clean)); got != want {
				t.Errorf("root %q, name %q: %q, want %q", root, clean, got, want)
			}
		}
	}
}

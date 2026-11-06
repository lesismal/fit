package vod

import (
	"testing"
	"time"

	"github.com/lesismal/fit/mediaauth"
)

// benchSigned is BenchmarkServeSegment's case of a signed URL, with a
// session, as /urls issues it.
func benchSigned(b *testing.B, run func(b *testing.B, f *fit, target string, hdr ...string)) {
	b.Run("signed", func(b *testing.B) {
		f := startVOD(b, copyLib(b), "", `{"library": "films", "auth": "site"}`, signedConf(`{}`))
		q, err := f.handler(b).auth.SignURL(movie, mediaauth.Viewer{Subject: "viewer-1", Session: "s1"}, time.Now().Add(time.Hour), "")
		if err != nil {
			b.Fatal(err)
		}
		run(b, f, movie+"v0.0.m4s?"+q)
	})
}

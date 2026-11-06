package vod

import (
	"testing"

	"github.com/lesismal/fib/media/mp4"
)

// BenchmarkBuildSegment is what building a segment costs on the file pool,
// clear and encrypted (encrypt): the file opened, its samples read and
// muxed, and, encrypted, the item's key derived and the segment encrypted
// in place. The segment is the test library's films/movie's first, of
// each container.
func BenchmarkBuildSegment(b *testing.B) {
	for _, enc := range []bool{false, true} {
		for _, c := range []container{fmp4C, tsC} {
			name := c.String() + "/clear"
			lib, params := "", `{"library": "films"}`
			if enc {
				name = c.String() + "/aes-128"
				lib, params = encrypted, `{"library": "films", "auth": "site", "encrypt": true}`
			}
			b.Run(name, func(b *testing.B) {
				f := startVOD(b, copyLib(b), lib, params, authConf)
				v := f.handler(b)
				it := f.lib.Item("films/movie")
				r := it.Renditions[0]
				type planned struct {
					p   *plan
					err error
				}
				pc := make(chan planned, 1)
				v.st.plan(f.lib, r, func(p *plan, err error) { pc <- planned{p, err} })
				got := <-pc
				if got.err != nil {
					b.Fatal(got.err)
				}
				p := got.p
				type read struct {
					vt, at *mp4.Table
					err    error
				}
				tc := make(chan read, 1)
				tables(f.lib, r, p.video, p.audio, func(vt, at *mp4.Table, err error) { tc <- read{vt, at, err} })
				t := <-tc
				if t.err != nil {
					b.Fatal(t.err)
				}
				m := r.MP4()
				src := &source{vt: t.vt, at: t.at, vtrack: m.Tracks[p.video].Track, atrack: m.Tracks[p.audio].Track}
				data, err := v.st.build(f.lib, &r.File, it.ID, p, 0, c, src)
				if err != nil {
					b.Fatal(err)
				}
				b.SetBytes(int64(len(data)))
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, err := v.st.build(f.lib, &r.File, it.ID, p, 0, c, src); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

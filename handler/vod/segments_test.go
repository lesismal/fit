package vod

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/fmp4"
	"github.com/lesismal/fib/media/mp4"
	"github.com/lesismal/fib/media/mpegts"
)

// fileTables opens the MP4 file at p and reads its tables: those of its
// first served video and audio tracks, nil for none.
func fileTables(t *testing.T, p string) (vt, at *mp4.Table, m *mp4.File, data []byte) {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	r := bytes.NewReader(data)
	if m, err = mp4.Open(r, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	vi, ai := pickTracks(m)
	if vi >= 0 {
		if vt, err = m.Tracks[vi].ReadTable(r); err != nil {
			t.Fatal(err)
		}
	}
	if ai >= 0 {
		if at, err = m.Tracks[ai].ReadTable(r); err != nil {
			t.Fatal(err)
		}
	}
	return vt, at, m, data
}

// segmentURIs returns the URIs of a media playlist's segments, and its
// EXT-X-MAP's.
func segmentURIs(t *testing.T, playlist []byte) (segs []string, durs []float64, init string) {
	t.Helper()
	var dur float64
	for line := range strings.Lines(string(playlist)) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#EXT-X-MAP:URI=\""):
			init = strings.TrimSuffix(strings.TrimPrefix(line, "#EXT-X-MAP:URI=\""), "\"")
		case strings.HasPrefix(line, "#EXTINF:"):
			fmt.Sscanf(strings.TrimPrefix(line, "#EXTINF:"), "%f,", &dur)
		case line != "" && !strings.HasPrefix(line, "#"):
			segs = append(segs, line)
			durs = append(durs, dur)
		}
	}
	if !strings.Contains(string(playlist), "#EXT-X-ENDLIST") {
		t.Fatalf("no ENDLIST in %s", playlist)
	}
	return segs, durs, init
}

// played is what a player gets of a track from a playlist's segments.
type played struct {
	pkts  []*av.Packet
	first []int // index of each segment's first packet
}

// demuxFMP4 reads the packets of init and each segment.
func demuxFMP4(t *testing.T, init []byte, segs [][]byte, ntracks int) []played {
	t.Helper()
	in, err := fmp4.ParseInit(init)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Tracks) != ntracks {
		t.Fatalf("init of %d tracks, want %d", len(in.Tracks), ntracks)
	}
	out := make([]played, ntracks)
	for n, s := range segs {
		for i := range out {
			out[i].first = append(out[i].first, len(out[i].pkts))
		}
		seq, pkts, rest, err := in.ParseFragment(s)
		if err != nil || len(rest) != 0 || seq != uint32(n+1) {
			t.Fatalf("segment %d: seq %d, %d bytes left, %v", n, seq, len(rest), err)
		}
		for _, p := range pkts {
			out[p.Track].pkts = append(out[p.Track].pkts, p)
		}
	}
	return out
}

// demuxTS reads the packets of each segment, each alone, as a player
// starting at it would.
func demuxTS(t *testing.T, segs [][]byte, ntracks int) []played {
	t.Helper()
	out := make([]played, ntracks)
	for n, s := range segs {
		if len(s)%mpegts.PacketSize != 0 {
			t.Fatalf("segment %d: %d bytes, not whole packets", n, len(s))
		}
		for i := range out {
			out[i].first = append(out[i].first, len(out[i].pkts))
		}
		var tracks int
		d := mpegts.NewDemuxer(mpegts.DemuxerConfig{
			OnTracks: func(tr []*av.Track) { tracks = len(tr) },
			OnPacket: func(p *av.Packet) { out[p.Track].pkts = append(out[p.Track].pkts, p) },
		})
		d.Write(s)
		d.Flush()
		if tracks != ntracks {
			t.Fatalf("segment %d: %d tracks, want %d", n, tracks, ntracks)
		}
	}
	return out
}

// within reports whether a and b are within tolerance of each other.
func within(a, b, tolerance time.Duration) bool {
	d := a - b
	return d <= tolerance && d >= -tolerance
}

// checkTrack checks that p is every sample of tb, in order, at its times
// (lead included), each segment of a track with video starting with a
// keyframe; and for fMP4 with its data as the file has it.
//
// rebase, for MPEG-TS, is what the demuxer takes from each segment's times:
// the earliest decode time of its first packets, which it counts from.
func checkTrack(t *testing.T, what string, p played, tb *mp4.Table, file []byte, keyed, sameData bool, minCTS int64, rebase []time.Duration) {
	t.Helper()
	if len(p.pkts) != tb.Len() {
		t.Fatalf("%s: %d packets, the file has %d samples", what, len(p.pkts), tb.Len())
	}
	// A tick of the 90 kHz clock, and the rounding of times to it.
	tolerance := 23 * time.Microsecond
	seg := 0
	for i, s := range tb.Samples(0) {
		pkt := p.pkts[i]
		for seg+1 < len(p.first) && i >= p.first[seg+1] {
			seg++
		}
		dts := tb.Time(s.DTS+minCTS) + lead
		pts := tb.Time(s.PTS()) + lead
		if rebase != nil {
			dts -= rebase[seg]
			pts -= rebase[seg]
		}
		if !within(pkt.DTS, dts, tolerance) || !within(pkt.PTS, pts, tolerance) {
			t.Fatalf("%s: sample %d at DTS %v PTS %v, want %v %v", what, i, pkt.DTS, pkt.PTS, dts, pts)
		}
		if sameData && !bytes.Equal(pkt.Data, file[s.Offset:s.Offset+int64(s.Size)]) {
			t.Fatalf("%s: sample %d's data differs from the file's", what, i)
		}
	}
	if keyed {
		for n, i := range p.first {
			if i < len(p.pkts) && !p.pkts[i].Keyframe {
				t.Errorf("%s: segment %d starts with sample %d, not a keyframe", what, n, i)
			}
		}
	}
}

// TestSegmentsCarryEveryFrame plays every rendition and audio file of the
// test library in both containers, as a player would from its playlists,
// and checks that the segments hold every sample of the file at its time,
// each segment of video starting on a keyframe with its audio within a
// frame of its video, and that the playlist's durations add up.
func TestSegmentsCarryEveryFrame(t *testing.T) {
	root := copyLib(t)
	f := startVOD(t, root, "", `{"library": "films", "segment_duration": "2s"}`, "")
	cases := []struct {
		item, ref, file string
	}{
		{"films/movie", "v0", "films/movie.360p.mp4"},
		{"films/movie", "v1", "films/movie.mp4"},
		{"films/movie", "a0", "films/movie.audio-fr.m4a"},
		{"clips/hevc", "v0", "clips/hevc.mp4"},
		{"clips/av1", "v0", "clips/av1.mp4"},
		{"clips/longgop", "v0", "clips/longgop.mp4"},
		{"music/song", "a0", "music/song.m4a"},
	}
	for _, tc := range cases {
		vt, at, m, file := fileTables(t, filepath.Join(root, tc.file))
		for _, c := range []container{fmp4C, tsC} {
			t.Run(tc.file+"/"+c.String(), func(t *testing.T) {
				vi, _ := pickTracks(m)
				base := "/vod/films/" + tc.item + "/"
				list := base + tc.ref + "." + c.String() + ".m3u8"
				if c == tsC && vi >= 0 && m.Tracks[vi].Track.Codec == av.CodecAV1 {
					f.mustGet(t, list, 404)
					return
				}
				uris, durs, initURI := segmentURIs(t, f.mustGet(t, list, 200))
				var segs [][]byte
				for _, u := range uris {
					segs = append(segs, f.mustGet(t, base+u, 200))
				}
				ntracks := 0
				for _, tb := range []*mp4.Table{vt, at} {
					if tb != nil {
						ntracks++
					}
				}
				var got []played
				if c == fmp4C {
					got = demuxFMP4(t, f.mustGet(t, base+initURI, 200), segs, ntracks)
				} else {
					if initURI != "" {
						t.Fatal("an EXT-X-MAP in an MPEG-TS playlist")
					}
					got = demuxTS(t, segs, ntracks)
				}
				k := 0
				var minCTS int64
				var rebase []time.Duration
				if c == tsC {
					if vt != nil {
						minCTS = int64(vt.MinCTS())
					}
					rebase = tsRebase(got, []*mp4.Table{vt, at}, minCTS, len(segs))
				}
				if vt != nil {
					checkTrack(t, "video", got[0], vt, file, tc.file != "clips/longgop.mp4", c == fmp4C, minCTS, rebase)
					k++
				}
				if at != nil {
					checkTrack(t, "audio", got[k], at, file, false, c == fmp4C, 0, rebase)
				}
				// The durations add up to the file's.
				var total float64
				for _, d := range durs {
					total += d
				}
				end := time.Duration(0)
				for _, tb := range []*mp4.Table{vt, at} {
					if tb != nil {
						end = max(end, tb.Time(tb.End()))
					}
				}
				if d := end.Seconds() - total; d > 0.05 || d < -0.05 {
					t.Errorf("the playlist's durations add up to %.3fs, the file lasts %v", total, end)
				}
				// Each segment's audio starts within a frame of its video.
				if vt != nil && at != nil {
					frame := tb0Frame(vt)
					for n := range segs {
						vp, ap := got[0].pkts, got[1].pkts
						i, j := got[0].first[n], got[1].first[n]
						if i >= len(vp) || j >= len(ap) || n+1 < len(segs) && j >= got[1].first[n+1] {
							continue
						}
						if d := ap[j].PTS - vp[i].PTS; d > frame || d < -frame {
							t.Errorf("segment %d: audio at %v, video at %v: past a frame (%v)", n, ap[j].PTS, vp[i].PTS, frame)
						}
					}
				}
			})
		}
	}
}

// tsRebase returns what the MPEG-TS demuxer counts each segment's times
// from: the earliest decode time, lead included, of the segment's first
// packet of each track.
func tsRebase(got []played, tables []*mp4.Table, minCTS int64, segs int) []time.Duration {
	out := make([]time.Duration, segs)
	for n := range segs {
		first := true
		k := 0
		for ti, tb := range tables {
			if tb == nil {
				continue
			}
			p := got[k]
			k++
			i := p.first[n]
			if i >= len(p.pkts) || n+1 < segs && i >= p.first[n+1] {
				continue
			}
			m := int64(0)
			if ti == 0 {
				m = minCTS
			}
			d := tb.Time(tb.Sample(i).DTS+m) + lead
			if first || d < out[n] {
				out[n], first = d, false
			}
		}
	}
	return out
}

// tb0Frame is the duration of a video table's first sample.
func tb0Frame(vt *mp4.Table) time.Duration {
	s := vt.Sample(0)
	return vt.Time(int64(s.Duration)) - vt.Time(0)
}

package vod

import (
	"errors"
	"fmt"
	"io"
	"math/bits"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/fmp4"
	"github.com/lesismal/fib/media/mp4"
	"github.com/lesismal/fib/media/mpegts"

	"github.com/lesismal/fit/medialib"
)

// tsAudioFrames is how many audio frames an MPEG-TS segment puts in a PES:
// about a tenth of a second of AAC or Opus, which saves most of the TS
// packets' stuffing a frame each would cost.
const tsAudioFrames = 6

// source is what a segment is cut from: the file's tracks the plan
// carries, and their tables, nil for a track it does not carry.
type source struct {
	vt, at         *mp4.Table
	vtrack, atrack *av.Track
}

// tracks returns the tracks of src as a segment carries them, video first.
func (src *source) tracks() []*av.Track {
	var out []*av.Track
	if src.vt != nil {
		out = append(out, src.vtrack)
	}
	if src.at != nil {
		out = append(out, src.atrack)
	}
	return out
}

// samples are a segment's samples of one track, in decode order, with
// their data, and the decode time after the last.
type samples struct {
	s    []mp4.Sample
	data [][]byte
	next int64
}

// collect returns the samples [from, to) of tb.
func collect(tb *mp4.Table, from, to int) samples {
	out := samples{s: make([]mp4.Sample, 0, max(to-from, 0))}
	if from >= to {
		return out
	}
	var last mp4.Sample
	for i, s := range tb.Samples(from) {
		if i >= to {
			out.next = s.DTS
			return out
		}
		out.s = append(out.s, s)
		last = s
	}
	out.next = last.DTS + int64(last.Duration)
	return out
}

// read reads the data of every sample of lists from r into one buffer,
// each run of samples that lie one after another in the file in one read.
func read(r io.ReaderAt, lists ...*samples) error {
	total := 0
	for _, l := range lists {
		for _, s := range l.s {
			total += int(s.Size)
		}
	}
	buf := make([]byte, total)
	pos := 0
	for _, l := range lists {
		l.data = make([][]byte, len(l.s))
		for i := 0; i < len(l.s); {
			// The run of samples from i that follow each other in the file.
			j, end := i+1, l.s[i].Offset+int64(l.s[i].Size)
			for j < len(l.s) && l.s[j].Offset == end {
				end += int64(l.s[j].Size)
				j++
			}
			n := int(end - l.s[i].Offset)
			if _, err := r.ReadAt(buf[pos:pos+n], l.s[i].Offset); err != nil {
				if errors.Is(err, io.EOF) {
					// The file is shorter than its tables say: it changed.
					return medialib.ErrChanged
				}
				return err
			}
			for k := i; k < j; k++ {
				size := int(l.s[k].Size)
				l.data[k] = buf[pos : pos+size : pos+size]
				pos += size
			}
			i = j
		}
	}
	return nil
}

// clock turns a track's media times into the ticks of a segment's
// timescale, lead included.
type clock struct {
	shift   int64 // the track's edit list shift, in its own ticks
	in, out int64 // timescales
	lead    int64 // lead, in out's ticks
}

func newClock(tb *mp4.Table, out uint32) clock {
	return clock{shift: tb.Shift, in: int64(tb.Timescale), out: int64(out),
		lead: int64(lead) * int64(out) / int64(time.Second)}
}

// at is the media time ticks on the segment's clock.
func (k clock) at(ticks int64) int64 { return mulDivFloor(ticks+k.shift, k.out, k.in) + k.lead }

// mulDivFloor returns floor(a*b/c) for b, c > 0, without overflow while
// the result fits.
func mulDivFloor(a, b, c int64) int64 {
	neg := a < 0
	ua := uint64(a)
	if neg {
		ua = uint64(-a)
	}
	hi, lo := bits.Mul64(ua, uint64(b))
	if hi >= uint64(c) {
		// Past what 64 bits hold, which no file's times reach.
		hi %= uint64(c)
	}
	q, r := bits.Div64(hi, lo, uint64(c))
	if !neg {
		return int64(q)
	}
	if r != 0 {
		q++
	}
	return -int64(q)
}

// fragmentOverhead is what a fragment's boxes take beside its samples: a
// moof, its trafs and an mdat header, and a trun entry of 16 bytes a
// sample.
func fragmentOverhead(n int) int { return 256 + 16*n }

// buildFMP4 builds segment n of p, as fragmented MP4 with sequence number
// n+1, from r. init is the initialization section the segment goes with,
// whose timescales it is written in.
func buildFMP4(r io.ReaderAt, p *plan, n int, src *source, init *initSection) ([]byte, error) {
	v, a, err := gather(r, p, n, src)
	if err != nil {
		return nil, err
	}
	f := fmp4.Fragment{Sequence: uint32(n + 1)}
	id := uint32(1)
	if src.vt != nil {
		f.Tracks = append(f.Tracks, trackFragment(id, &v, newClock(src.vt, init.timescales[0]), true))
		id++
	}
	if src.at != nil {
		f.Tracks = append(f.Tracks, trackFragment(id, &a, newClock(src.at, init.timescales[id-1]), false))
	}
	size := fragmentOverhead(len(v.s) + len(a.s))
	for _, l := range [][][]byte{v.data, a.data} {
		for _, d := range l {
			size += len(d)
		}
	}
	return f.Append(make([]byte, 0, size))
}

// trackFragment returns the samples l of a track as a traf of id.
func trackFragment(id uint32, l *samples, k clock, video bool) fmp4.TrackFragment {
	tf := fmp4.TrackFragment{TrackID: id, Video: video, Samples: make([]fmp4.Sample, len(l.s))}
	if len(l.s) == 0 {
		return tf
	}
	tf.BaseDecodeTime = uint64(max(k.at(l.s[0].DTS), 0))
	for i, s := range l.s {
		dts := k.at(s.DTS)
		next := l.next
		if i+1 < len(l.s) {
			next = l.s[i+1].DTS
		}
		tf.Samples[i] = fmp4.Sample{
			Duration:          uint32(max(k.at(next)-dts, 0)),
			CompositionOffset: int32(k.at(s.DTS+int64(s.CTS)) - dts),
			Keyframe:          s.Keyframe || !video,
			Data:              l.data[i],
		}
	}
	return tf
}

// buildTS builds segment n of p as MPEG-TS from r: its PAT and PMT, then
// its samples, video and audio interleaved by decode time.
func buildTS(r io.ReaderAt, p *plan, n int, src *source) ([]byte, error) {
	v, a, err := gather(r, p, n, src)
	if err != nil {
		return nil, err
	}
	m, err := mpegts.NewMuxer(src.tracks(), mpegts.MuxerConfig{AudioFramesPerPES: tsAudioFrames})
	if err != nil {
		return nil, err
	}
	size := 0
	for _, l := range [][][]byte{v.data, a.data} {
		for _, d := range l {
			size += len(d)
		}
	}
	// Each 184 bytes of payload take a 188-byte packet; a PES header, an
	// access unit delimiter and parameter sets, and the stuffing of its last
	// packet, about two more each.
	out := make([]byte, 0, size+size/40+2*mpegts.PacketSize*(len(v.s)+len(a.s)/tsAudioFrames+4))
	out = m.WriteTables(out)
	vi, ai := 0, 0
	var minCTS int64
	audioTrack := 0
	if src.vt != nil {
		minCTS = int64(src.vt.MinCTS())
		audioTrack = 1
	}
	for vi < len(v.s) || ai < len(a.s) {
		var pkt av.Packet
		takeVideo := vi < len(v.s)
		if takeVideo && ai < len(a.s) {
			takeVideo = src.vt.Time(v.s[vi].DTS+minCTS) <= src.at.Time(a.s[ai].DTS)
		}
		if takeVideo {
			s := v.s[vi]
			pkt = av.Packet{Track: 0, DTS: src.vt.Time(s.DTS + minCTS), PTS: src.vt.Time(s.PTS()), Keyframe: s.Keyframe, Data: v.data[vi]}
			vi++
		} else {
			s := a.s[ai]
			pkt = av.Packet{Track: audioTrack, DTS: src.at.Time(s.DTS), PTS: src.at.Time(s.PTS()), Keyframe: true, Data: a.data[ai]}
			ai++
		}
		if out, err = m.WritePacket(out, &pkt); err != nil {
			return nil, fmt.Errorf("vod: an MPEG-TS packet of track %d: %w", pkt.Track, err)
		}
	}
	return m.Flush(out), nil
}

// gather reads segment n's samples of each track of src from r.
func gather(r io.ReaderAt, p *plan, n int, src *source) (v, a samples, err error) {
	if src.vt != nil {
		from, to := p.videoRange(n, src.vt)
		v = collect(src.vt, from, to)
	}
	if src.at != nil {
		from, to := p.audioRange(n, src.at)
		a = collect(src.at, from, to)
	}
	return v, a, read(r, &v, &a)
}

// initSection is the fMP4 initialization section of a file's tracks, and
// the timescales of its tracks, in order.
type initSection struct {
	data       []byte
	timescales []uint32
}

// buildInit returns the initialization section of tracks.
func buildInit(tracks []*av.Track) (*initSection, error) {
	// The timescales are the init's own, as fmp4 chose them: read back.
	data, err := fmp4.AppendInit(nil, tracks)
	var in *fmp4.Init
	if err == nil {
		in, err = fmp4.ParseInit(data)
	}
	if err != nil {
		return nil, err
	}
	s := &initSection{data: data}
	for _, t := range in.Tracks {
		s.timescales = append(s.timescales, t.Timescale)
	}
	return s, nil
}

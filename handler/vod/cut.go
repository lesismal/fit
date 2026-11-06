package vod

import (
	"iter"
	"math"
	"time"

	"github.com/lesismal/fib/media/mp4"
)

// buildPlan cuts a file of tracks vt, its video, and at, its audio, either
// nil, into segments of about d, each of whose samples take no more than
// maxBytes, but for a sample past it alone.
//
// A file with video is cut at keyframes: a segment ends at the keyframe
// nearest d after its start, so that a file whose keyframes come every d,
// or every fraction of it, is cut every d, and two renditions with their
// keyframes in the same places are cut alike. A segment that would outgrow
// maxBytes is cut at its last keyframe, or, without one, where it reaches
// maxBytes, not starting with a keyframe. Its audio is the audio samples
// presented from its start to the next's, so that a player that starts at
// any segment has its sound from its first frame on; audio that goes on
// past the video is cut on its own. A file of audio alone is cut every d.
//
// Samples that would decode more than lead before the file's time 0, as an
// edit list cutting a file's start makes them, are left out.
func buildPlan(vt, at *mp4.Table, d time.Duration, maxBytes int64) (*plan, error) {
	p := &plan{video: -1, audio: -1, independent: true}
	if vt != nil {
		if err := p.cutVideo(vt, at, d, maxBytes); err != nil {
			return nil, err
		}
		p.addBytes(vt)
		if at != nil {
			p.addAudio(at, vt.Len(), maxBytes)
		}
	} else if !p.cutAudio(at, d, maxBytes) {
		return nil, errNoKeyframe
	}
	longest := time.Duration(0)
	for n := range p.segs {
		longest = max(longest, p.duration(n))
		p.independent = p.independent && p.segs[n].key
	}
	p.target = max(1, int(math.Round(longest.Seconds())))
	p.segs = p.segs[:len(p.segs):len(p.segs)]
	return p, nil
}

// cutVideo cuts p at vt's keyframes, reckoning each segment's bytes with
// the audio of at, if not nil, decoded by then.
func (p *plan) cutVideo(vt, at *mp4.Table, d time.Duration, maxBytes int64) error {
	// cand is a sample the open segment may yet end before: the last
	// keyframe after its start, or where it reaches maxBytes; and the bytes
	// of the segment up to it.
	type cand struct {
		i     int
		t     time.Duration
		bytes int64
	}
	none := cand{i: -1}
	prev := none
	var bytes int64 // of the open segment
	cut := func(at cand, key bool) {
		p.segs = append(p.segs, seg{start: at.t, v: int32(at.i), key: key})
		bytes -= at.bytes
		prev = none
	}
	audio := newAudioCount(at)
	defer audio.stop()
	for i, s := range vt.Samples(0) {
		t := vt.Time(s.PTS())
		size := int64(s.Size)
		if len(p.segs) == 0 {
			if !s.Keyframe || vt.Time(s.DTS)+lead < 0 {
				continue
			}
			p.segs = append(p.segs, seg{start: t, v: int32(i), key: true})
		}
		bytes += audio.upTo(vt.Time(s.DTS))
		if s.Keyframe && i > int(p.segs[len(p.segs)-1].v) {
			here := cand{i: i, t: t, bytes: bytes}
			for {
				start := p.segs[len(p.segs)-1].start
				target := start + d
				if t < target {
					prev = here
					break
				}
				if prev.i >= 0 && target-prev.t < t-target && prev.t-start >= d/2 {
					// The keyframe before is nearer the target: the
					// segment ends there, and this one is weighed again
					// against the next.
					here.bytes -= prev.bytes
					cut(prev, true)
					continue
				}
				cut(here, true)
				break
			}
		}
		if bytes+size > maxBytes && i > int(p.segs[len(p.segs)-1].v) {
			if prev.i >= 0 {
				cut(prev, true)
			}
			if bytes+size > maxBytes && i > int(p.segs[len(p.segs)-1].v) {
				// No keyframe to end it at: it ends here, and the next
				// does not start with one.
				start := max(vt.Time(s.DTS), p.segs[len(p.segs)-1].start+time.Millisecond)
				cut(cand{i: i, t: start, bytes: bytes}, s.Keyframe)
			}
		}
		bytes += size
	}
	if len(p.segs) == 0 {
		return errNoKeyframe
	}
	p.end = max(vt.Time(vt.End()), p.segs[len(p.segs)-1].start+time.Millisecond)
	// The end is weighed as a keyframe would be: the last segment ends at
	// the keyframe before it when that is nearer its target.
	start := p.segs[len(p.segs)-1].start
	if target := start + d; p.end > target && prev.i >= 0 && target-prev.t < p.end-target && prev.t-start >= d/2 {
		cut(prev, true)
	}
	return nil
}

// audioCount counts the bytes of an audio track's samples in decode order,
// as a video track's are walked.
type audioCount struct {
	next func() (int, mp4.Sample, bool)
	stop func()
	tb   *mp4.Table
	s    mp4.Sample
	ok   bool
}

func newAudioCount(at *mp4.Table) *audioCount {
	if at == nil {
		return &audioCount{stop: func() {}}
	}
	a := &audioCount{tb: at}
	a.next, a.stop = iter.Pull2(at.Samples(0))
	_, a.s, a.ok = a.next()
	return a
}

// upTo returns the bytes of the samples decoded at or before t that it has
// not counted yet.
func (a *audioCount) upTo(t time.Duration) int64 {
	var n int64
	for a.ok && a.tb.Time(a.s.DTS) <= t {
		n += int64(a.s.Size)
		_, a.s, a.ok = a.next()
	}
	return n
}

// addBytes sets each segment's bytes to its video samples'.
func (p *plan) addBytes(vt *mp4.Table) {
	k := 0
	for i, s := range vt.Samples(int(p.segs[0].v)) {
		for k+1 < len(p.segs) && i >= int(p.segs[k+1].v) {
			k++
		}
		p.segs[k].bytes = clampBytes(int64(p.segs[k].bytes) + int64(s.Size))
	}
}

// addAudio gives each segment of p its audio: the samples of at presented
// from its start to the next's. The audio past the last segment's video,
// vlen its samples, past maxBytes goes on in segments of its own.
func (p *plan) addAudio(at *mp4.Table, vlen int, maxBytes int64) {
	for n := range p.segs {
		p.segs[n].a = int32(at.Len())
	}
	k, started := 0, false
	for j, s := range at.Samples(0) {
		t := at.Time(s.PTS())
		if t+lead < 0 {
			continue
		}
		if !started {
			p.segs[0].a, started = int32(j), true
		}
		for k+1 < len(p.segs) && t >= p.segs[k+1].start {
			k++
			p.segs[k].a = int32(j)
		}
		size := int64(s.Size)
		if k == len(p.segs)-1 && int64(p.segs[k].bytes)+size > maxBytes && t >= p.end && int(p.segs[k].a) < j {
			p.segs = append(p.segs, seg{start: t, v: int32(vlen), a: int32(j), key: true})
			k++
		}
		p.segs[k].bytes = clampBytes(int64(p.segs[k].bytes) + size)
	}
	p.end = max(p.end, at.Time(at.End()))
}

// cutAudio cuts p, of audio alone, every d, and reports whether it has a
// sample to play.
func (p *plan) cutAudio(at *mp4.Table, d time.Duration, maxBytes int64) bool {
	var bytes int64
	for j, s := range at.Samples(0) {
		t := at.Time(s.PTS())
		if t+lead < 0 {
			continue
		}
		size := int64(s.Size)
		if n := len(p.segs); n == 0 || t >= p.segs[n-1].start+d || bytes+size > maxBytes && j > int(p.segs[n-1].a) {
			if n > 0 {
				p.segs[n-1].bytes = clampBytes(bytes)
			}
			p.segs = append(p.segs, seg{start: t, a: int32(j), key: true})
			bytes = 0
		}
		bytes += size
	}
	if len(p.segs) == 0 {
		return false
	}
	p.segs[len(p.segs)-1].bytes = clampBytes(bytes)
	p.end = max(at.Time(at.End()), p.segs[len(p.segs)-1].start+time.Millisecond)
	return true
}

func clampBytes(n int64) uint32 { return uint32(min(max(n, 0), math.MaxUint32)) }

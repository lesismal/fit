package vod

import (
	"errors"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/mp4"
	"github.com/lesismal/fib/media/mpegts"
)

// lead is where a file's time 0 is on the clock of its segments: the
// timestamps fMP4 and MPEG-TS segments carry are the file's presentation
// times plus lead, so that a file whose decoding starts before its
// presentation, as B-frames and AAC's priming make it, never goes below 0.
// It is MPEG-TS's own offset, so both containers carry one clock, and the
// subtitles' X-TIMESTAMP-MAP names it.
const lead = mpegts.DefaultTimeOffset

// leadMPEGTS is lead on the 90 kHz clock.
const leadMPEGTS = int64(lead) * 90000 / int64(time.Second)

// errNoKeyframe is a video track without a keyframe to start from.
var errNoKeyframe = errors.New("vod: the video track has no keyframe to start from")

// plan is how a media file is cut into segments: the tracks its playlist
// carries, and where each segment starts. It is built once from the
// tracks' sample tables, on the library's pool, and kept in the cache; it
// holds no table, so that the library's bound on them holds.
type plan struct {
	// video and audio are the MP4's tracks the playlist carries, -1 for
	// none.
	video, audio int
	segs         []seg
	// end is when the last segment ends, as a presentation time.
	end time.Duration
	// target is EXT-X-TARGETDURATION: the longest segment, rounded.
	target int
	// independent says every segment starts with a keyframe.
	independent bool
}

// seg is a segment of a plan.
type seg struct {
	// start is its presentation time: its first keyframe's, or its first
	// audio sample's.
	start time.Duration
	// v and a are its first video and audio samples, in decode order; it
	// takes those up to the next segment's.
	v, a int32
	// bytes is the size of its samples.
	bytes uint32
	// key says it starts with a keyframe.
	key bool
}

// segBytes is what a seg is reckoned to take, and planBytes a plan beside
// its segs.
const (
	segBytes  = 24
	planBytes = 128
)

// memory is what p takes, for the cache's bound.
func (p *plan) memory() int64 { return planBytes + int64(cap(p.segs))*segBytes }

// duration is segment n's.
func (p *plan) duration(n int) time.Duration {
	if n+1 < len(p.segs) {
		return p.segs[n+1].start - p.segs[n].start
	}
	return p.end - p.segs[n].start
}

// videoRange is the video samples of segment n, [from, to).
func (p *plan) videoRange(n int, vt *mp4.Table) (int, int) {
	to := vt.Len()
	if n+1 < len(p.segs) {
		to = int(p.segs[n+1].v)
	}
	return int(p.segs[n].v), to
}

// audioRange is the audio samples of segment n, [from, to).
func (p *plan) audioRange(n int, at *mp4.Table) (int, int) {
	to := at.Len()
	if n+1 < len(p.segs) {
		to = int(p.segs[n+1].a)
	}
	return int(p.segs[n].a), to
}

// The codecs a segment carries: fMP4 takes H.264, H.265 and AV1, and
// MPEG-TS the first two; both take AAC and Opus.
func servedVideo(c av.CodecID) bool {
	return c == av.CodecH264 || c == av.CodecH265 || c == av.CodecAV1
}

func servedAudio(c av.CodecID) bool { return c == av.CodecAAC || c == av.CodecOpus }

// carried reports whether container c carries a track of codec id.
func carried(c container, id av.CodecID) bool {
	return c != tsC || id != av.CodecAV1
}

// pickTracks returns the tracks of m a playlist carries: its first video
// track of a codec served, and its first audio track of one; -1 for none.
func pickTracks(m *mp4.File) (video, audio int) {
	video, audio = -1, -1
	for i, t := range m.Tracks {
		if t.Track == nil || t.Samples == 0 {
			continue
		}
		switch {
		case video < 0 && t.Handler == "vide" && servedVideo(t.Track.Codec):
			video = i
		case audio < 0 && t.Handler == "soun" && servedAudio(t.Track.Codec):
			audio = i
		}
	}
	return video, audio
}

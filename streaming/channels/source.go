package channels

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/lesismal/fib/media/av"
	"github.com/lesismal/fib/media/mp4"

	"github.com/lesismal/fit/medialib"
)

// The errors of an item a channel cannot play.
var (
	errNoLibrary = errors.New("the channel's library does not run")
	errNotReady  = errors.New("the library has not been scanned yet")
	errNoMedia   = errors.New("the item has no video or audio fit can play")
	errNALSize   = errors.New("the video's NAL units are not 4-byte length prefixed")
	errSample    = errors.New("a sample is larger than max_buffer_bytes")
)

// Where an item played came from.
const (
	fromList     = "list"
	fromSchedule = "schedule"
	fromAdmin    = "admin"
)

// choice is an item to play next: its ID, where it came from and where to
// start it.
type choice struct {
	item   string
	from   string
	offset time.Duration // into the item
	slot   time.Time     // the slot's start, for fromSchedule
}

// srcTrack is a track of an item played: where its samples are, and how far
// they have been read.
type srcTrack struct {
	rend   *medialib.Rendition
	index  int // the track's in the rendition's MP4
	info   medialib.Track
	track  *av.Track
	tb     *mp4.Table
	adjust int64 // ticks added to decode times: MinCTS, so that DTS <= PTS
	next   int   // the next sample to read
	video  bool
}

// dts returns sample i's decode time, the edit list applied, adjusted.
func (t *srcTrack) dts(s mp4.Sample) time.Duration { return t.tb.Time(s.DTS + t.adjust) }

// source is an item being played: its tracks, as the hub's stream has them,
// and where its samples go on the channel's timeline.
type source struct {
	lib    *medialib.Library
	item   *medialib.Item
	rend   *medialib.Rendition
	choice choice
	tracks []*srcTrack
	avs    []*av.Track

	// off is the channel time of the item's time 0, as Table.Time gives
	// it: a sample's channel DTS is off + its dts, and its PTS off + its
	// presentation time. base is the DTS of its first sample, delay how much
	// earlier than its first presentation that is (B-frames), end the
	// channel time its presentation ends at, and cut, when not 0, where its
	// presentation is cut: at the next slot's start.
	off, base, delay, end, cut time.Duration
	// done is set once every sample has been read, cutHit when the cut
	// stopped it.
	done, cutHit bool
	// listAfter is the channel's list as it was once the item was chosen,
	// for a skip to go on from.
	listAfter listState
}

// pickRendition returns the rendition of it named name, or the one with
// the most pixels, the highest bit rate among equals; nil when none can be
// served.
func pickRendition(it *medialib.Item, name string) *medialib.Rendition {
	var best *medialib.Rendition
	for _, r := range it.Renditions {
		if r.Error != "" || r.MP4() == nil {
			continue
		}
		if name != "" && r.Name == name {
			return r
		}
		if best == nil || r.Width*r.Height > best.Width*best.Height ||
			r.Width*r.Height == best.Width*best.Height && r.Bitrate > best.Bitrate {
			best = r
		}
	}
	return best
}

// playable reports whether fit carries a track of kind and codec.
func playable(t medialib.Track) (video, ok bool) {
	switch t.Codec {
	case "h264", "h265", "av1":
		return true, t.Kind == medialib.KindVideo
	case "aac", "opus", "mp3":
		return false, t.Kind == medialib.KindAudio
	}
	return false, false
}

// newSource picks the tracks of it to play: its rendition's first video
// and first audio, or, when the rendition has no audio, the item's default
// audio's.
func newSource(lib *medialib.Library, it *medialib.Item, rendition string, c choice) (*source, error) {
	src := &source{lib: lib, item: it, choice: c}
	src.rend = pickRendition(it, rendition)
	var audio *srcTrack
	if src.rend != nil {
		for _, t := range src.rend.Tracks {
			video, ok := playable(t)
			switch {
			case !ok:
			case video && len(src.tracks) == 0:
				src.tracks = append(src.tracks, &srcTrack{rend: src.rend, index: t.Index, info: t, video: true})
			case !video && audio == nil:
				audio = &srcTrack{rend: src.rend, index: t.Index, info: t}
			}
		}
	}
	if audio == nil {
		var alt *medialib.Rendition
		for _, r := range it.Audio {
			if r.Error == "" && r.MP4() != nil && (alt == nil || r.Default && !alt.Default) {
				alt = r
			}
		}
		if alt != nil {
			for _, t := range alt.Tracks {
				if video, ok := playable(t); ok && !video {
					audio = &srcTrack{rend: alt, index: t.Index, info: t}
					break
				}
			}
		}
	}
	if audio != nil {
		src.tracks = append(src.tracks, audio)
	}
	if len(src.tracks) == 0 {
		return nil, errNoMedia
	}
	for _, t := range src.tracks {
		t.track = t.rend.MP4().Tracks[t.index].Track
		if !nalSize4(t.track) {
			return nil, errNALSize
		}
		src.avs = append(src.avs, t.track)
	}
	if src.rend == nil {
		src.rend = src.tracks[0].rend
	}
	return src, nil
}

// nalSize4 reports whether a video track's NAL units are prefixed with 4
// bytes, as package av carries them.
func nalSize4(t *av.Track) bool {
	switch t.Codec {
	case av.CodecH264:
		return len(t.Config) > 4 && t.Config[4]&3 == 3
	case av.CodecH265:
		return len(t.Config) > 21 && t.Config[21]&3 == 3
	}
	return true
}

// load reads the sample tables of src's tracks, through the library, and
// calls done once, from the library's pool or at once.
func (src *source) load(done func(error)) {
	var mu sync.Mutex
	left := len(src.tracks)
	var first error
	for _, t := range src.tracks {
		src.lib.Table(t.rend, t.index, func(tb *mp4.Table, err error) {
			mu.Lock()
			if err == nil {
				t.tb = tb
			} else if first == nil {
				first = err
			}
			left--
			last := left == 0
			mu.Unlock()
			if last {
				done(first)
			}
		})
	}
}

// place puts src on the channel's timeline, started at its choice's
// offset: its presentation from pres on, its first packet's DTS at dts or
// later, and each track's first DTS at its after or later, so that decode
// times never go back. It returns false when nothing is left to play from
// there.
//
// Items follow one another by their presentation, as players show them:
// one item's first picture comes as the last one's ends. A track's samples
// presented wholly before the start, as AAC's priming, are left out, and
// its DTS may come before its presentation (B-frames), which the DTS of
// the item before leaves room for.
func (src *source) place(pres, dts time.Duration, after []time.Duration) bool {
	for _, t := range src.tracks {
		if mc := t.tb.MinCTS(); mc < 0 {
			t.adjust = int64(mc)
		}
		t.next = 0
	}
	// start is the item's time its presentation starts at.
	start := max(src.choice.offset, 0)
	for _, t := range src.tracks {
		if t.video && src.choice.offset > 0 {
			k := t.tb.SeekKeyframe(src.choice.offset)
			if k < 0 {
				return false
			}
			t.next = k
			start = t.tb.Time(t.tb.Sample(k).PTS())
		}
	}
	for _, t := range src.tracks {
		if t.video {
			continue
		}
		i := max(t.tb.IndexAt(start)-1, 0)
		for i < t.tb.Len() {
			s := t.tb.Sample(i)
			if t.tb.Time(s.PTS()+int64(s.Duration)) > start {
				break
			}
			i++
		}
		t.next = i
	}
	var first, end time.Duration
	found := false
	for _, t := range src.tracks {
		if t.next >= t.tb.Len() {
			continue
		}
		d := t.dts(t.tb.Sample(t.next)) - start
		e := t.tb.Time(t.tb.End()+t.adjust) - start
		if t.info.Duration > 0 {
			e = t.info.Start + t.info.Duration - start
		}
		if !found || d < first {
			first = d
		}
		if !found || e > end {
			end = e
		}
		found = true
	}
	if !found {
		return false
	}
	// p is where the presentation starts on the channel.
	p := max(pres, dts-first)
	for i, t := range src.tracks {
		if i < len(after) && t.next < t.tb.Len() {
			p = max(p, after[i]-(t.dts(t.tb.Sample(t.next))-start))
		}
	}
	src.off = p - start
	src.base = p + first
	src.delay = -first
	src.end = p + end
	return true
}

// head returns the track whose next sample comes first, and its channel
// time; nil when every track has been read, or has reached the cut.
func (src *source) head() (*srcTrack, mp4.Sample, time.Duration) {
	var best *srcTrack
	var bs mp4.Sample
	var bt time.Duration
	for _, t := range src.tracks {
		if t.next >= t.tb.Len() {
			continue
		}
		s := t.tb.Sample(t.next)
		at := src.off + t.dts(s)
		if src.cut > 0 && src.cutAt(t, s, at) {
			src.cutHit = true
			continue
		}
		if best == nil || at < bt {
			best, bs, bt = t, s, at
		}
	}
	return best, bs, bt
}

// cutAt reports whether sample s of t, of channel DTS at, is past the cut:
// video by its DTS, decoded the item's delay before it is shown, so that
// the pictures shown before the cut are those read; the rest by their
// presentation.
func (src *source) cutAt(t *srcTrack, s mp4.Sample, at time.Duration) bool {
	if t.video {
		return at+src.delay >= src.cut
	}
	return src.off+t.tb.Time(s.PTS()) >= src.cut
}

// pick is a sample chosen to read.
type pick struct {
	track int
	s     mp4.Sample
	pos   int // in the slab
}

// read reads src's next samples, on the library's pool: up to span of
// media, and up to budget bytes but for the first, which may not pass
// limit. It returns them as packets of the hub's stream, in decode order,
// with their bytes, and marks src done once every sample has been read.
func (src *source) read(span time.Duration, budget, limit int64) ([]*av.Packet, int64, error) {
	var picks []pick
	var total int64
	first := time.Duration(-1)
	for {
		t, s, at := src.head()
		if t == nil {
			src.done = true
			break
		}
		if first < 0 {
			first = at
		}
		if len(picks) > 0 && (at-first >= span || total+int64(s.Size) > budget) {
			break
		}
		if int64(s.Size) > limit {
			return nil, 0, errSample
		}
		picks = append(picks, pick{track: slices.Index(src.tracks, t), s: s})
		total += int64(s.Size)
		t.next++
	}
	if len(picks) == 0 {
		return nil, 0, nil
	}
	data, err := src.readSamples(picks, total)
	if err != nil {
		return nil, 0, err
	}
	pkts := make([]*av.Packet, len(picks))
	for i, p := range picks {
		t := src.tracks[p.track]
		pkts[i] = &av.Packet{
			Track:    p.track,
			DTS:      src.off + t.dts(p.s),
			PTS:      src.off + t.tb.Time(p.s.PTS()+t.adjust),
			Keyframe: p.s.Keyframe || !t.video,
			Data:     data[p.pos : p.pos+int(p.s.Size) : p.pos+int(p.s.Size)],
		}
	}
	return pkts, total, nil
}

// readSamples reads the samples picked into one slab of total bytes, each
// file's in the order they lie in it, a run of samples that follow one
// another read at once, and sets each pick's place in the slab.
func (src *source) readSamples(picks []pick, total int64) ([]byte, error) {
	slab := make([]byte, total)
	order := make([]int, len(picks))
	for i := range order {
		order[i] = i
	}
	rendOf := func(i int) *medialib.Rendition { return src.tracks[picks[i].track].rend }
	slices.SortFunc(order, func(a, b int) int {
		ra, rb := rendOf(a), rendOf(b)
		return cmp.Or(cmp.Compare(ra.Root, rb.Root), cmp.Compare(ra.Path, rb.Path), cmp.Compare(picks[a].s.Offset, picks[b].s.Offset))
	})
	pos := 0
	for i := 0; i < len(order); {
		r := rendOf(order[i])
		h, err := src.lib.OpenFile(&r.File)
		if err != nil {
			return nil, err
		}
		j := i
		for j < len(order) && rendOf(order[j]) == r {
			// A run of samples that follow one another in the file.
			k := j
			start := picks[order[j]].s.Offset
			end := start
			for k < len(order) && rendOf(order[k]) == r && picks[order[k]].s.Offset == end {
				picks[order[k]].pos = pos + int(end-start)
				end += int64(picks[order[k]].s.Size)
				k++
			}
			n := int(end - start)
			if _, err := h.ReadAt(slab[pos:pos+n], start); err != nil {
				h.Close()
				return nil, fmt.Errorf("reading %s: %w", r.Path, err)
			}
			pos += n
			j = k
		}
		h.Close()
		i = j
	}
	return slab, nil
}

package vod

import (
	"bytes"
	"cmp"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/medialib"
)

// The groups of a multivariant playlist's renditions.
const (
	audioGroup    = "audio"
	subtitleGroup = "subs"
)

// appendSeconds appends d in seconds with three decimals, as EXTINF has it.
func appendSeconds(b []byte, d time.Duration) []byte {
	return strconv.AppendFloat(b, d.Seconds(), 'f', 3, 64)
}

// appendURI appends the URI name, with query after it when there is one:
// what every URI a playlist names carries, a token today.
func appendURI(b []byte, name []byte, query string) []byte {
	b = append(b, name...)
	if query != "" {
		b = append(b, '?')
		b = append(b, query...)
	}
	return b
}

// variant is a rendition, or an audio file of an item of audio alone, a
// multivariant playlist lists, with the tracks its playlist carries.
type variant struct {
	r            *medialib.Rendition
	i            int
	audio        bool
	video, sound int // the MP4's tracks, -1 for none
}

// variants returns the renditions of it that container c can carry, and
// the audio files it can, as a multivariant playlist lists them: the
// renditions as variants and the audio files as an audio group; or, for an
// item of audio alone, the audio files as variants and no group.
func variants(it *medialib.Item, c container) (vs, alts []variant) {
	for i, r := range it.Renditions {
		if v, ok := servable(r, c); ok && v.video >= 0 {
			v.i = i
			vs = append(vs, v)
		}
	}
	for i, r := range it.Audio {
		if v, ok := servable(r, c); ok && v.sound >= 0 {
			v.i, v.audio = i, true
			alts = append(alts, v)
		}
	}
	if len(vs) == 0 {
		return alts, nil
	}
	return vs, alts
}

// servable returns r as a variant of container c, false when c can carry
// none of its tracks.
func servable(r *medialib.Rendition, c container) (variant, bool) {
	m := r.MP4()
	if r.Error != "" || m == nil {
		return variant{}, false
	}
	v, a := pickTracks(m)
	if v >= 0 && !carried(c, m.Tracks[v].Track.Codec) {
		// A video MPEG-TS does not carry: not this rendition at all.
		return variant{}, false
	}
	return variant{r: r, video: v, sound: a}, v >= 0 || a >= 0
}

// codecs returns the RFC 6381 codecs of the tracks of v.
func (v variant) codecs() []string {
	var out []string
	for _, t := range v.r.Tracks {
		if (t.Index == v.video || t.Index == v.sound) && t.CodecString != "" {
			out = append(out, t.CodecString)
		}
	}
	return out
}

// language returns the language of v's audio, as the sidecar or its track
// says, or "" when unknown.
func (v variant) language() string {
	if v.r.Language == "und" {
		return ""
	}
	return v.r.Language
}

// version is the EXT-X-VERSION of c's playlists: 7 for fMP4, whose
// EXT-X-MAP needs at least 6, and 3 for MPEG-TS, whose EXTINF are decimal.
func version(c container) int {
	if c == tsC {
		return 3
	}
	return 7
}

// renderMaster returns the multivariant playlist of it in container c, its
// URIs with query, or nil when c can carry nothing of it.
func renderMaster(it *medialib.Item, c container, query string) []byte {
	vs, alts := variants(it, c)
	if len(vs) == 0 {
		return nil
	}
	b := make([]byte, 0, 512+256*(len(vs)+len(alts)+len(it.Subtitles)))
	b = append(b, "#EXTM3U\n#EXT-X-VERSION:"...)
	b = strconv.AppendInt(b, int64(version(c)), 10)
	b = append(b, '\n')
	names := map[string]int{}
	unique := func(n string) string {
		names[n]++
		if k := names[n]; k > 1 {
			return n + " (" + strconv.Itoa(k) + ")"
		}
		return n
	}
	var uri []byte
	// The audio group: the variants' own audio, when every one has some,
	// then the item's audio files.
	group := len(alts) > 0
	altCodecs := []string{}
	var altRate int64
	if group {
		main := true
		for _, v := range vs {
			main = main && v.sound >= 0
		}
		// The default: the variants' own audio, or else the first audio
		// file the sidecar says is, or else the first.
		def := -1
		if main {
			lang := vs[0].language()
			b = appendMedia(b, "AUDIO", audioGroup, unique(nameOr("main", lang)), lang, true, false, nil)
		} else {
			def = alts[0].i
			for _, a := range alts {
				if a.r.Default {
					def = a.i
					break
				}
			}
		}
		for _, a := range alts {
			uri = appendURI(appendMediaName(uri[:0], true, a.i, c), nil, query)
			lang := a.language()
			n := cmp.Or(a.r.Name, nameOr("audio "+strconv.Itoa(a.i), lang))
			b = appendMedia(b, "AUDIO", audioGroup, unique(n), lang, a.i == def, false, uri)
			for _, cs := range a.codecs() {
				if !contains(altCodecs, cs) {
					altCodecs = append(altCodecs, cs)
				}
			}
			altRate = max(altRate, a.r.Bitrate)
		}
	}
	subs := false
	for k, s := range it.Subtitles {
		if s.Error != "" {
			continue
		}
		subs = true
		uri = append(append(uri[:0], 's'), strconv.Itoa(k)...)
		uri = appendURI(append(uri, ".m3u8"...), nil, query)
		n := cmp.Or(s.Name, nameOr("subtitles "+strconv.Itoa(k), s.Language))
		b = appendMedia(b, "SUBTITLES", subtitleGroup, unique(n), s.Language, s.Default, s.Forced, uri)
	}
	for _, v := range vs {
		rate := max(v.r.Bitrate, 1)
		if group {
			rate += altRate
		}
		b = append(b, "#EXT-X-STREAM-INF:BANDWIDTH="...)
		b = strconv.AppendInt(b, rate, 10)
		b = append(b, ",AVERAGE-BANDWIDTH="...)
		b = strconv.AppendInt(b, rate, 10)
		cs := v.codecs()
		if group {
			for _, a := range altCodecs {
				if !contains(cs, a) {
					cs = append(cs, a)
				}
			}
		}
		if len(cs) > 0 {
			b = append(b, ",CODECS=\""...)
			b = append(b, strings.Join(cs, ",")...)
			b = append(b, '"')
		}
		if v.video >= 0 && v.r.Width > 0 && v.r.Height > 0 {
			b = append(b, ",RESOLUTION="...)
			b = strconv.AppendInt(b, int64(v.r.Width), 10)
			b = append(b, 'x')
			b = strconv.AppendInt(b, int64(v.r.Height), 10)
		}
		if group {
			b = append(b, ",AUDIO=\""+audioGroup+"\""...)
		}
		if subs {
			b = append(b, ",SUBTITLES=\""+subtitleGroup+"\""...)
		}
		b = append(b, '\n')
		uri = appendMediaName(uri[:0], v.audio, v.i, c)
		b = appendURI(b, uri, query)
		b = append(b, '\n')
	}
	return b
}

// appendMedia appends an EXT-X-MEDIA tag; a nil uri leaves URI out, for
// the audio the variants carry themselves.
func appendMedia(b []byte, typ, group, name, lang string, def, forced bool, uri []byte) []byte {
	b = append(b, "#EXT-X-MEDIA:TYPE="...)
	b = append(b, typ...)
	b = append(b, ",GROUP-ID=\""...)
	b = append(b, group...)
	b = append(b, "\",NAME=\""...)
	b = append(b, quoted(name)...)
	b = append(b, '"')
	if lang != "" && lang != "und" {
		b = append(b, ",LANGUAGE=\""...)
		b = append(b, quoted(lang)...)
		b = append(b, '"')
	}
	b = append(b, ",DEFAULT="...)
	b = append(b, yesNo(def)...)
	b = append(b, ",AUTOSELECT=YES"...)
	if typ == "SUBTITLES" {
		b = append(b, ",FORCED="...)
		b = append(b, yesNo(forced)...)
	}
	if uri != nil {
		b = append(b, ",URI=\""...)
		b = append(b, uri...)
		b = append(b, '"')
	}
	return append(b, '\n')
}

func yesNo(v bool) string {
	if v {
		return "YES"
	}
	return "NO"
}

// quoted returns s as a quoted-string attribute may hold it: without
// double quotes, carriage returns and line feeds.
func quoted(s string) string {
	if !strings.ContainsAny(s, "\"\r\n") {
		return s
	}
	return strings.NewReplacer("\"", "'", "\r", " ", "\n", " ").Replace(s)
}

func nameOr(def, lang string) string {
	if lang != "" && lang != "und" {
		return lang
	}
	return def
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// renderMedia returns the media playlist of p, in container c, of the
// rendition or audio file ref (audio, i), its URIs with query.
func renderMedia(p *plan, audio bool, i int, c container, query string) []byte {
	b := make([]byte, 0, 256+40*len(p.segs))
	b = append(b, "#EXTM3U\n#EXT-X-VERSION:"...)
	b = strconv.AppendInt(b, int64(version(c)), 10)
	b = append(b, "\n#EXT-X-TARGETDURATION:"...)
	b = strconv.AppendInt(b, int64(p.target), 10)
	b = append(b, "\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n"...)
	if p.independent {
		b = append(b, "#EXT-X-INDEPENDENT-SEGMENTS\n"...)
	}
	var name []byte
	if c == fmp4C {
		b = append(b, "#EXT-X-MAP:URI=\""...)
		name = appendInitName(name[:0], audio, i)
		b = appendURI(b, name, query)
		b = append(b, "\"\n"...)
	}
	for n := range p.segs {
		b = append(b, "#EXTINF:"...)
		b = appendSeconds(b, p.duration(n))
		b = append(b, ",\n"...)
		name = appendSegmentName(name[:0], audio, i, n, c)
		b = appendURI(b, name, query)
		b = append(b, '\n')
	}
	return append(b, "#EXT-X-ENDLIST\n"...)
}

// renderSubtitles returns the playlist of subtitle k of an item of
// duration d: the whole WebVTT file as one segment.
func renderSubtitles(k int, d time.Duration, query string) []byte {
	d = max(d, time.Millisecond)
	b := make([]byte, 0, 192)
	b = append(b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:"...)
	b = strconv.AppendInt(b, int64(math.Ceil(d.Seconds())), 10)
	b = append(b, "\n#EXT-X-MEDIA-SEQUENCE:0\n#EXT-X-PLAYLIST-TYPE:VOD\n#EXTINF:"...)
	b = appendSeconds(b, d)
	b = append(b, ",\n"...)
	b = appendURI(b, append(append([]byte{'s'}, strconv.Itoa(k)...), ".vtt"...), query)
	return append(b, "\n#EXT-X-ENDLIST\n"...)
}

// timestampMap is the WebVTT header that puts the cues' time 0 where the
// segments' clock has the file's: at lead.
var timestampMap = []byte("X-TIMESTAMP-MAP=MPEGTS:" + strconv.FormatInt(leadMPEGTS, 10) + ",LOCAL:00:00:00.000")

// withTimestampMap returns vtt, a WebVTT file, with an X-TIMESTAMP-MAP
// header after its first line, unless it has one, so that HLS players
// show its cues at the times of the segments' clock.
func withTimestampMap(vtt []byte) []byte {
	head := vtt
	if i := bytes.Index(vtt, []byte("\n\n")); i >= 0 {
		head = vtt[:i]
	}
	if bytes.Contains(head, []byte("X-TIMESTAMP-MAP")) {
		return vtt
	}
	i := bytes.IndexByte(vtt, '\n')
	if i < 0 {
		i = len(vtt)
	}
	out := make([]byte, 0, len(vtt)+len(timestampMap)+2)
	out = append(out, vtt[:i]...)
	out = append(out, '\n')
	out = append(out, timestampMap...)
	if i < len(vtt) {
		out = append(out, vtt[i:]...)
	} else {
		out = append(out, '\n')
	}
	return out
}

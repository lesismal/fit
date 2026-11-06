package vod

import (
	"strconv"
	"strings"
)

// What a name of an item's directory is. An item's URLs are its ID's path
// after the handler's prefix, and one of these names after it:
//
//	index.m3u8             the multivariant playlist, of the default container
//	index.fmp4.m3u8        the same, of fragmented MP4 segments
//	index.ts.m3u8          the same, of MPEG-TS segments
//	v0.fmp4.m3u8           the media playlist of rendition 0 (a for audio)
//	v0.init.mp4            its fMP4 initialization section
//	v0.12.m4s, v0.12.ts    its segment 12
//	s0.m3u8, s0.vtt        subtitle 0's playlist, and the subtitle as WebVTT
//	v0.mp4, a0.m4a         the file itself, its own extension, with Range
//	poster.jpg             the poster, its own extension
type kind uint8

const (
	kindMaster kind = iota + 1
	kindMedia
	kindInit
	kindSegment
	kindSubtitles
	kindVTT
	kindFile
	kindPoster
)

// container is a segment format.
type container uint8

const (
	fmp4C container = iota + 1
	tsC
)

func (c container) String() string {
	if c == tsC {
		return "ts"
	}
	return "fmp4"
}

// maxIndex bounds the numbers of a name, so that one parses into an int32.
const maxIndex = 1 << 24

// name is a name of an item's directory, parsed.
type name struct {
	kind  kind
	c     container // of a playlist or segment; 0 for the default
	audio bool      // a0..., one of the item's audio files; v0... a rendition
	i     int       // the rendition, audio file or subtitle
	n     int       // the segment
	ext   string    // of a file or the poster, as the request writes it
}

// parseName parses the last part of a request's path; false for a name the
// handler does not serve.
func parseName(s string) (name, bool) {
	if s == "index.m3u8" {
		return name{kind: kindMaster}, true
	}
	parts := strings.Split(s, ".")
	switch len(parts) {
	case 2:
		if parts[0] == "poster" {
			return name{kind: kindPoster, ext: parts[1]}, parts[1] != ""
		}
		t, i, ok := fileRef(parts[0])
		if !ok {
			return name{}, false
		}
		switch {
		case t == 's' && parts[1] == "m3u8":
			return name{kind: kindSubtitles, i: i}, true
		case t == 's' && parts[1] == "vtt":
			return name{kind: kindVTT, i: i}, true
		case t != 's' && mediaExt[parts[1]]:
			return name{kind: kindFile, audio: t == 'a', i: i, ext: parts[1]}, true
		}
	case 3:
		if parts[0] == "index" {
			c, ok := containerOf(parts[1])
			return name{kind: kindMaster, c: c}, ok && parts[2] == "m3u8"
		}
		t, i, ok := fileRef(parts[0])
		if !ok || t == 's' {
			return name{}, false
		}
		nm := name{audio: t == 'a', i: i}
		switch parts[2] {
		case "m3u8":
			c, ok := containerOf(parts[1])
			nm.kind, nm.c = kindMedia, c
			return nm, ok
		case "mp4":
			nm.kind, nm.c = kindInit, fmp4C
			return nm, parts[1] == "init"
		case "m4s", "ts":
			n, ok := number(parts[1])
			nm.kind, nm.n, nm.c = kindSegment, n, fmp4C
			if parts[2] == "ts" {
				nm.c = tsC
			}
			return nm, ok
		}
	}
	return name{}, false
}

// mediaExt are the extensions of the media files a library holds.
var mediaExt = map[string]bool{"mp4": true, "m4v": true, "m4a": true, "mov": true}

func containerOf(s string) (container, bool) {
	switch s {
	case "fmp4":
		return fmp4C, true
	case "ts":
		return tsC, true
	}
	return 0, false
}

// fileRef parses v12, a12 or s12.
func fileRef(s string) (byte, int, bool) {
	if len(s) < 2 || s[0] != 'v' && s[0] != 'a' && s[0] != 's' {
		return 0, 0, false
	}
	i, ok := number(s[1:])
	return s[0], i, ok
}

// number parses a decimal number below maxIndex, written without leading
// zeros, so that a name has one spelling.
func number(s string) (int, bool) {
	if s == "" || len(s) > 8 || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, n < maxIndex
}

// The names an item's playlists write, relative to the item's directory.

func appendRef(b []byte, audio bool, i int) []byte {
	if audio {
		b = append(b, 'a')
	} else {
		b = append(b, 'v')
	}
	return strconv.AppendInt(b, int64(i), 10)
}

func appendMediaName(b []byte, audio bool, i int, c container) []byte {
	b = appendRef(b, audio, i)
	b = append(b, '.')
	b = append(b, c.String()...)
	return append(b, ".m3u8"...)
}

func appendInitName(b []byte, audio bool, i int) []byte {
	return append(appendRef(b, audio, i), ".init.mp4"...)
}

func appendSegmentName(b []byte, audio bool, i, n int, c container) []byte {
	b = appendRef(b, audio, i)
	b = append(b, '.')
	b = strconv.AppendInt(b, int64(n), 10)
	if c == tsC {
		return append(b, ".ts"...)
	}
	return append(b, ".m4s"...)
}

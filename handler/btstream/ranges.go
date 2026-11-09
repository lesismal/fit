package btstream

import (
	"path"
	"strconv"
	"strings"
)

// parseRange returns the part of a file of size that a Range header asks
// for: start and n bytes, partial for a single byte range that applies,
// and satisfiable false for one that starts past the end (416). A header
// left out, malformed, of another unit, or of several ranges asks for the
// whole file, as RFC 9110 lets a server answer any of these: a player asks
// for one range at a time.
func parseRange(header string, size int64) (start, n int64, partial, satisfiable bool) {
	spec, ok := strings.CutPrefix(header, "bytes=")
	if !ok || strings.Contains(spec, ",") {
		return 0, size, false, true
	}
	first, last, ok := strings.Cut(strings.TrimSpace(spec), "-")
	if !ok {
		return 0, size, false, true
	}
	if first == "" {
		// The last n bytes.
		suffix, err := strconv.ParseInt(last, 10, 64)
		if err != nil || suffix < 0 {
			return 0, size, false, true
		}
		if suffix == 0 || size == 0 {
			return 0, 0, false, false
		}
		suffix = min(suffix, size)
		return size - suffix, suffix, true, true
	}
	from, err := strconv.ParseInt(first, 10, 64)
	if err != nil || from < 0 {
		return 0, size, false, true
	}
	end := size - 1
	if last != "" {
		to, err := strconv.ParseInt(last, 10, 64)
		if err != nil || to < from {
			return 0, size, false, true
		}
		end = min(to, size-1)
	}
	if from >= size {
		return 0, 0, false, false
	}
	return from, end - from + 1, true, true
}

// noneMatch reports whether an If-None-Match header holds etag, or *.
func noneMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || part == etag {
			return true
		}
	}
	return false
}

// types are the Content-Types of the files a torrent streams, by their
// extension: video, audio and their subtitles first. A table of the
// handler's own, rather than mime's, which reads the system's files.
var types = map[string]string{
	"mp4": "video/mp4", "m4v": "video/mp4", "mkv": "video/x-matroska", "webm": "video/webm",
	"mov": "video/quicktime", "avi": "video/x-msvideo", "ts": "video/mp2t", "m2ts": "video/mp2t",
	"mpg": "video/mpeg", "mpeg": "video/mpeg", "ogv": "video/ogg", "flv": "video/x-flv", "wmv": "video/x-ms-wmv",
	"3gp": "video/3gpp",
	"mp3": "audio/mpeg", "m4a": "audio/mp4", "aac": "audio/aac", "flac": "audio/flac", "ogg": "audio/ogg",
	"oga": "audio/ogg", "opus": "audio/ogg", "wav": "audio/wav", "mka": "audio/x-matroska", "wma": "audio/x-ms-wma",
	"srt": "application/x-subrip", "vtt": "text/vtt; charset=utf-8", "ass": "text/x-ssa", "ssa": "text/x-ssa",
	"txt": "text/plain; charset=utf-8", "nfo": "text/plain; charset=utf-8", "pdf": "application/pdf",
	"epub": "application/epub+zip", "jpg": "image/jpeg", "jpeg": "image/jpeg", "png": "image/png",
	"gif": "image/gif", "webp": "image/webp", "zip": "application/zip",
}

// contentType returns the Content-Type of the file at p, by its extension.
func contentType(p string) string {
	if t, ok := types[strings.ToLower(strings.TrimPrefix(path.Ext(p), "."))]; ok {
		return t
	}
	return "application/octet-stream"
}

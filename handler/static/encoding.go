package static

import (
	"io/fs"
	stdhttp "net/http"
	"os"
	"strconv"
	"strings"
)

// encoding is a content coding a file's compressed twin may be in.
type encoding struct {
	name   string // as Accept-Encoding and Content-Encoding name it
	suffix string // the twin's name is the file's with this added
}

// encodings are the codings precompressed names, by its names for them.
var encodings = map[string]encoding{
	"br":   {"br", ".br"},
	"zstd": {"zstd", ".zst"},
	"gzip": {"gzip", ".gz"},
}

// twin returns the compressed twin of the file at full that r's client
// prefers among the handler's encodings, with its path and what a stat said
// of it, or a nil encoding when the client takes none of them or no twin is
// on disk. The client's q values rank them; among equals, the order
// precompressed lists them.
func (s *Static) twin(r *stdhttp.Request, full string) (*encoding, string, fs.FileInfo) {
	accept := r.Header["Accept-Encoding"]
	if len(accept) == 0 {
		return nil, "", nil
	}
	var best *encoding
	var bestQ float64
	var bestFull string
	var bestInfo fs.FileInfo
	for i := range s.precompressed {
		enc := &s.precompressed[i]
		q := acceptQ(accept, enc.name)
		if q <= 0 || q <= bestQ {
			continue
		}
		twinFull := full + enc.suffix
		info, err := os.Stat(twinFull)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		best, bestQ, bestFull, bestInfo = enc, q, twinFull, info
	}
	return best, bestFull, bestInfo
}

// acceptQ is the q value the Accept-Encoding fields give coding: its own,
// or the one of "*" when they do not name it, and 0 when they refuse it or
// name neither.
func acceptQ(fields []string, coding string) float64 {
	q, wildcard := -1.0, -1.0
	for _, field := range fields {
		for field != "" {
			var member string
			member, field, _ = strings.Cut(field, ",")
			name, params, _ := strings.Cut(member, ";")
			name = strings.TrimSpace(name)
			value := 1.0
			for params != "" {
				var param string
				param, params, _ = strings.Cut(params, ";")
				if key, v, ok := strings.Cut(strings.TrimSpace(param), "="); ok && strings.EqualFold(key, "q") {
					if parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && parsed >= 0 && parsed <= 1 {
						value = parsed
					}
				}
			}
			switch {
			case strings.EqualFold(name, coding):
				q = value
			case name == "*":
				wildcard = value
			}
		}
	}
	if q < 0 {
		q = max(wildcard, 0)
	}
	return q
}

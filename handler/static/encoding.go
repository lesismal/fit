package static

import (
	stdhttp "net/http"
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

// preferred returns the index, among the handler's encodings, of the
// compressed twin of l's file that r's client prefers of those on disk, or
// -1 when the client takes none of them or none is on disk. The client's q
// values rank them; among equals, the order precompressed lists them.
func (s *Static) preferred(r *stdhttp.Request, l *lookup) int {
	accept := r.Header["Accept-Encoding"]
	if len(accept) == 0 || len(l.twins) == 0 {
		return -1
	}
	best, bestQ := -1, 0.0
	for i := range s.precompressed {
		if l.twins[i].info == nil {
			continue
		}
		if q := acceptQ(accept, s.precompressed[i].name); q > 0 && q > bestQ {
			best, bestQ = i, q
		}
	}
	return best
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

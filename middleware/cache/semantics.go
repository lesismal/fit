package cache

import (
	stdhttp "net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// directives are the Cache-Control directives the cache reads, from a
// request or a response. Durations are -1 when the directive is absent.
type directives struct {
	maxAge, sMaxAge, maxStale, minFresh time.Duration
	// maxStaleAny is max-stale with no value: any staleness will do.
	maxStaleAny bool
	noStore, noCache, private, public, mustRevalidate, proxyRevalidate bool
	onlyIfCached                                                        bool
}

// maxDelta bounds delta-seconds, as RFC 9111 section 1.2.2 has caches do:
// 2^31 seconds, about 68 years.
const maxDelta = time.Duration(1<<31) * time.Second

// parseDirectives reads the directives of Cache-Control fields. A qualified
// no-cache or private, naming fields, is taken as the unqualified one, as a
// cache that does not track fields must. A delta that does not parse is
// taken as 0, which makes a response stale, as section 4.2.1 says to.
func parseDirectives(fields []string) directives {
	d := directives{maxAge: -1, sMaxAge: -1, maxStale: -1, minFresh: -1}
	for _, field := range fields {
		for member := range strings.SplitSeq(field, ",") {
			name, value, _ := strings.Cut(member, "=")
			name = strings.ToLower(strings.TrimSpace(name))
			value = strings.Trim(strings.TrimSpace(value), `"`)
			switch name {
			case "max-age":
				d.maxAge = delta(value)
			case "s-maxage":
				d.sMaxAge = delta(value)
			case "max-stale":
				if value == "" {
					d.maxStaleAny = true
				} else {
					d.maxStale = delta(value)
				}
			case "min-fresh":
				d.minFresh = delta(value)
			case "no-store":
				d.noStore = true
			case "no-cache":
				d.noCache = true
			case "private":
				d.private = true
			case "public":
				d.public = true
			case "must-revalidate":
				d.mustRevalidate = true
			case "proxy-revalidate":
				d.proxyRevalidate = true
			case "only-if-cached":
				d.onlyIfCached = true
			}
		}
	}
	return d
}

// delta parses delta-seconds, as 0 when it is not a number, and bounded by
// maxDelta.
func delta(s string) time.Duration {
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return maxDelta
		}
		return 0
	}
	if n >= uint64(maxDelta/time.Second) {
		return maxDelta
	}
	return time.Duration(n) * time.Second
}

// requestDirectives reads a request's Cache-Control, and Pragma: no-cache
// when it has no Cache-Control, as section 5.4 allows for HTTP/1.0 clients.
func requestDirectives(h stdhttp.Header) directives {
	cc := h["Cache-Control"]
	d := parseDirectives(cc)
	if len(cc) == 0 {
		for _, v := range h["Pragma"] {
			if strings.Contains(strings.ToLower(v), "no-cache") {
				d.noCache = true
			}
		}
	}
	return d
}

// heuristic reports whether a response of status may be given a heuristic
// freshness lifetime, or stored with one given by the config, when it has
// none of its own: the statuses RFC 9110 section 15.1 calls heuristically
// cacheable, but 204 and 206, whose bodies the cache does not keep.
func heuristic(status int) bool {
	switch status {
	case 200, 203, 300, 301, 308, 404, 405, 410, 414, 501:
		return true
	}
	return false
}

// storableStatus reports whether the cache keeps responses of status at all:
// the heuristic ones, and the temporary redirects when they give a lifetime
// of their own.
func storableStatus(status int, explicit bool) bool {
	return heuristic(status) || explicit && (status == 302 || status == 307)
}

// maxHeuristic bounds a lifetime made from Last-Modified.
const maxHeuristic = 24 * time.Hour

// freshness returns a response's freshness lifetime, as section 4.2.1 has a
// shared cache work it out, and whether the response gave one itself. date
// is when the response was made. A response that gives none gets
// defaultTTL, when it is set, or a tenth of the time since it was last
// modified, at most a day.
func freshness(h stdhttp.Header, d directives, status int, date time.Time, defaultTTL time.Duration) (time.Duration, bool) {
	switch {
	case d.sMaxAge >= 0:
		return d.sMaxAge, true
	case d.maxAge >= 0:
		return d.maxAge, true
	}
	if v, ok := h["Expires"]; ok {
		if len(v) == 0 {
			return 0, true
		}
		t, err := stdhttp.ParseTime(v[0])
		if err != nil {
			return 0, true // invalid, as "0" is: already expired
		}
		return max(t.Sub(date), 0), true
	}
	if !heuristic(status) && !d.public {
		return 0, false
	}
	if defaultTTL > 0 {
		return defaultTTL, false
	}
	if lm, err := stdhttp.ParseTime(h.Get("Last-Modified")); err == nil && lm.Before(date) {
		return min(date.Sub(lm)/10, maxHeuristic), false
	}
	return 0, false
}

// varyNames returns the request fields a response's Vary names, in
// canonical form, and false for Vary: *, which no request can match.
func varyNames(h stdhttp.Header) ([]string, bool) {
	var names []string
	for _, v := range h["Vary"] {
		for name := range strings.SplitSeq(v, ",") {
			name = strings.TrimSpace(name)
			switch name {
			case "":
			case "*":
				return nil, false
			default:
				names = append(names, textproto.CanonicalMIMEHeaderKey(name))
			}
		}
	}
	return names, true
}

// varyValue is a request field's value as variants are told apart by: its
// lines joined, with the space around each list member taken out, so that
// "gzip, br" and "gzip,br" select the same variant.
func varyValue(h stdhttp.Header, name string) string {
	lines := h[name]
	switch len(lines) {
	case 0:
		return ""
	case 1:
		if !strings.Contains(lines[0], ", ") && strings.TrimSpace(lines[0]) == lines[0] {
			return lines[0]
		}
	}
	var b strings.Builder
	for _, line := range lines {
		for member := range strings.SplitSeq(line, ",") {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(strings.TrimSpace(member))
		}
	}
	return b.String()
}

// notModified reports whether a request's preconditions are met by a stored
// response with etag and lastModified, so that it is answered 304: its
// If-None-Match lists the ETag, weakly compared, or *; or, with no
// If-None-Match, its If-Modified-Since is no earlier than Last-Modified.
func notModified(h stdhttp.Header, etag, lastModified string) bool {
	if inm, ok := h["If-None-Match"]; ok {
		if etag == "" {
			return false
		}
		for _, line := range inm {
			for member := range strings.SplitSeq(line, ",") {
				member = strings.TrimSpace(member)
				if member == "*" || weakEqual(member, etag) {
					return true
				}
			}
		}
		return false
	}
	ims := h.Get("If-Modified-Since")
	if ims == "" || lastModified == "" {
		return false
	}
	since, err := stdhttp.ParseTime(ims)
	if err != nil {
		return false
	}
	lm, err := stdhttp.ParseTime(lastModified)
	return err == nil && !lm.After(since)
}

// weakEqual compares entity tags weakly: their opaque parts match, whether
// either is weak or not.
func weakEqual(a, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}

// conditional reports whether a request carries a precondition the cache
// answers for itself.
func conditional(h stdhttp.Header) bool {
	_, inm := h["If-None-Match"]
	_, ims := h["If-Modified-Since"]
	return inm || ims
}

// unstored are the fields a stored response is kept without: those that
// belong to one connection, Age, which is worked out when the response is
// served, and the length, which the stored body gives.
var unstored = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Connection": true, "Transfer-Encoding": true,
	"Upgrade": true, "Te": true, "Trailer": true, "Content-Length": true, "Age": true,
	"Cache-Status": true,
}

// notModifiedFields are the fields of a stored response a 304 made from it
// carries, as RFC 9110 section 15.4.5 lists them.
var notModifiedFields = []string{"Cache-Control", "Content-Location", "Date", "Etag", "Expires", "Last-Modified", "Vary"}

// updateExcluded are the fields a 304 does not change in the stored
// response it freshens (RFC 9111 section 3.2).
var updateExcluded = map[string]bool{
	"Content-Length": true, "Content-Encoding": true, "Content-Type": true, "Content-Range": true,
}

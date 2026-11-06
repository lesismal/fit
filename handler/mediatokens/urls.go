package mediatokens

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/netip"
	"strings"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/mediaauth"
)

// urlsRequest is what <prefix>/urls is asked for: the viewer, the paths to
// sign URLs of, and their lifetime and key.
type urlsRequest struct {
	Subject string   `json:"subject"`
	Address string   `json:"address"`
	Session string   `json:"session"`
	TTL     string   `json:"ttl"`
	Key     string   `json:"key"`
	Paths   []string `json:"paths"`
}

// urlsAnswer is its answer: the URLs, whole, in the request's order, and
// what they were signed for.
type urlsAnswer struct {
	Subject string   `json:"subject"`
	Session string   `json:"session"`
	Expires int64    `json:"expires"`
	Key     string   `json:"key,omitempty"`
	URLs    []string `json:"urls"`
}

// urls signs playback URLs (url_signing) for one viewer: each path's
// resource, as mediaauth.ResourceOf gives it, for the subject, bound to the
// address when one is named, in the session named or a new one; all or
// none, as a batch of tokens. Pure computation, as issue is.
func (t *Tokens) urls(c *fibhttp.Context) {
	readBody(c, t.maxBody, func(body []byte) { t.urlsBody(c, body) })
}

func (t *Tokens) urlsBody(c *fibhttp.Context, body []byte) {
	if !t.auth.URLSigning() {
		plain(c, stdhttp.StatusNotFound, "the media_auth policy has no url_signing", nil)
		return
	}
	var in urlsRequest
	if err := json.Unmarshal(body, &in); err != nil {
		plain(c, stdhttp.StatusBadRequest, `the body must be JSON: {"subject": "...", "paths": [...]}`, nil)
		return
	}
	bad := func(msg string) { plain(c, stdhttp.StatusBadRequest, msg, nil) }
	switch {
	case len(in.Paths) == 0:
		bad("paths is empty: ask for at least one URL")
		return
	case len(in.Paths) > t.maxTokens:
		bad(fmt.Sprintf("paths has %d paths: at most %d a request", len(in.Paths), t.maxTokens))
		return
	case in.Subject == "" || len(in.Subject) > maxSubjectBytes ||
		strings.ContainsFunc(in.Subject, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		bad(fmt.Sprintf("%q is not a subject: 1 to %d bytes with no control characters", in.Subject, maxSubjectBytes))
		return
	case in.Session != "" && !mediaauth.ValidSession(in.Session):
		bad(fmt.Sprintf("%q is not a session: 1 to %d letters, digits, '.', '_', '~' and '-'", in.Session, mediaauth.MaxSessionBytes))
		return
	}
	v := mediaauth.Viewer{Subject: in.Subject, Session: in.Session}
	if in.Address != "" {
		a, err := netip.ParseAddr(in.Address)
		if err != nil || a.Zone() != "" {
			bad(fmt.Sprintf("%q is not an IP address", in.Address))
			return
		}
		v.Address = a
	}
	if v.Session == "" {
		v.Session = newSession()
	}
	ttl := min(t.auth.URLTTL(), t.maxTTL)
	if in.TTL != "" {
		d, err := time.ParseDuration(in.TTL)
		switch {
		case err != nil || d < time.Second:
			bad(fmt.Sprintf("%q is not a duration of a second or more, as 10m", in.TTL))
			return
		case d > t.maxTTL:
			bad(fmt.Sprintf("the lifetime %s is longer than max_ttl %s", d, t.maxTTL))
			return
		}
		ttl = d
	}
	key := in.Key
	if key != "" {
		if !t.ring {
			bad("the policy has one key: leave key out of the request")
			return
		}
		if _, ok := t.auth.KeyFile(key); !ok {
			bad(fmt.Sprintf("no key named %q", key))
			return
		}
	} else if t.ring {
		key = t.auth.DefaultKey()
	}
	expiry := time.Now().Add(ttl)
	out := urlsAnswer{Subject: in.Subject, Session: v.Session, Expires: expiry.Unix(), Key: key, URLs: make([]string, 0, len(in.Paths))}
	for i, path := range in.Paths {
		res, ok := mediaauth.ResourceOf(path)
		if !ok {
			bad(fmt.Sprintf("paths[%d]: %q is not a path a URL is signed for, as /vod/films/films/bbb/index.m3u8 or /live/cam.flv", i, path))
			return
		}
		if !t.urlAllowed(path) {
			bad(fmt.Sprintf("paths[%d]: %q starts with none of the url_prefixes this route may sign for: %s", i, path, strings.Join(t.URLPrefixes, ", ")))
			return
		}
		query, err := t.auth.SignURL(res, v, expiry, key)
		if err != nil {
			if errors.Is(err, mediaauth.ErrUnavailable) || errors.Is(err, mediaauth.ErrNoURLSigning) {
				plain(c, stdhttp.StatusServiceUnavailable, "the media_auth policy is not loaded", stdhttp.Header{"Retry-After": {retryAfter}})
				return
			}
			bad(fmt.Sprintf("paths[%d]: %v", i, err))
			return
		}
		out.URLs = append(out.URLs, t.urlBase+path+"?"+query)
	}
	b, _ := json.Marshal(out)
	hdr := stdhttp.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
	_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: hdr, Body: b})
}

// urlAllowed reports whether path starts with one of url_prefixes; with
// none configured, every path is.
func (t *Tokens) urlAllowed(path string) bool {
	if len(t.URLPrefixes) == 0 {
		return true
	}
	for _, p := range t.URLPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// newSession returns a new playback session's ID: 12 random bytes, raw URL
// base64.
func newSession() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

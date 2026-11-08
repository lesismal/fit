package syncs3

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// EmptySHA256 is the SHA-256 of no bytes, in hex: the payload hash of a
// request without a body.
const EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Signer signs requests with AWS Signature Version 4, in the Authorization
// header, as S3 and its compatibles check them. It is safe for use by
// several goroutines.
type Signer struct {
	AccessKeyID, SecretAccessKey, SessionToken string
	Region, Service                            string

	mu   sync.Mutex
	day  string
	dkey []byte
}

// PayloadHash returns the hex SHA-256 of body, as x-amz-content-sha256
// carries it.
func PayloadHash(body []byte) string {
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// Sign sets req's x-amz-date, x-amz-content-sha256, x-amz-security-token
// (with a session token) and Authorization headers, signing its method, URL
// (its EscapedPath and RawQuery, as they are sent), its host and every
// x-amz- and content-md5 header, with payloadHash the body's hex SHA-256. now
// is the signature's time.
func (s *Signer) Sign(req *http.Request, payloadHash string, now time.Time) {
	s.signWith(req, payloadHash, now, nil)
}

// signWith is Sign, signing the headers extra names too, in lower case.
func (s *Signer) signWith(req *http.Request, payloadHash string, now time.Time, extra []string) {
	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if s.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", s.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	signed, canonHeaders := canonicalHeaders(req.Header, host, extra)
	canon := strings.Join([]string{req.Method, req.URL.EscapedPath(), canonicalQuery(req.URL.RawQuery),
		canonHeaders, signed, payloadHash}, "\n")
	scope := day + "/" + s.Region + "/" + s.Service + "/aws4_request"
	sum := sha256.Sum256([]byte(canon))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	sig := hex.EncodeToString(hmacSHA256(s.key(day), toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKeyID+"/"+scope+
		", SignedHeaders="+signed+", Signature="+sig)
}

// key returns the signing key of day, kept until the day changes.
func (s *Signer) key(day string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.day != day {
		k := hmacSHA256([]byte("AWS4"+s.SecretAccessKey), day)
		k = hmacSHA256(k, s.Region)
		k = hmacSHA256(k, s.Service)
		s.dkey, s.day = hmacSHA256(k, "aws4_request"), day
	}
	return s.dkey
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// canonicalHeaders returns the signed headers' names and their canonical
// lines: host, content-md5, content-type and every x-amz- header, by name.
func canonicalHeaders(h http.Header, host string, extra []string) (string, string) {
	vals := map[string]string{"host": host}
	for k, v := range h {
		lk := strings.ToLower(k)
		if lk == "content-md5" || lk == "content-type" || strings.HasPrefix(lk, "x-amz-") || slices.Contains(extra, lk) {
			parts := make([]string, len(v))
			for i, s := range v {
				parts[i] = strings.Join(strings.Fields(s), " ")
			}
			vals[lk] = strings.Join(parts, ",")
		}
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, k := range names {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(vals[k])
		b.WriteByte('\n')
	}
	return strings.Join(names, ";"), b.String()
}

// canonicalQuery returns a query's parameters sorted by name, then value,
// each encoded as Escape does.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var ps []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		uk, err1 := url.QueryUnescape(k)
		uv, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			uk, uv = k, v
		}
		ps = append(ps, kv{Escape(uk, true), Escape(uv, true)})
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].k != ps[j].k {
			return ps[i].k < ps[j].k
		}
		return ps[i].v < ps[j].v
	})
	var b strings.Builder
	for i, p := range ps {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p.k)
		b.WriteByte('=')
		b.WriteString(p.v)
	}
	return b.String()
}

// Escape encodes s as SigV4 has it: every byte but the unreserved letters,
// digits, '-', '.', '_' and '~' as %XX in upper case, and '/' too when
// slash is true (a query's parameter), kept otherwise (a path).
func Escape(s string, slash bool) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexd[c>>4])
			b.WriteByte(hexd[c&15])
		}
	}
	return b.String()
}

// EncodeQuery encodes q as SigV4's canonical query is, sorted, so that what
// is sent is what is signed.
func EncodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			if b.Len() > 0 {
				b.WriteByte('&')
			}
			b.WriteString(Escape(k, true))
			b.WriteByte('=')
			b.WriteString(Escape(v, true))
		}
	}
	return b.String()
}

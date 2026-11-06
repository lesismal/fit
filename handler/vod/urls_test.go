package vod

import (
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/mediaauth"
)

// signedConf is a policy site with a keyring, a viewer's token and
// url_signing with url as its params.
func signedConf(params string) string {
	return `[{"name": "site", "keys": [{"key": "k1", "secret": "the first key of the site"}, {"key": "k2", "secret": "the second key of the site"}],
  "tokens": [{"name": "viewer", "token": "` + viewToken + `", "play": ["vod/films/films/*"]}],
  "url_signing": ` + params + `}]`
}

const movie = "/vod/films/films/movie/"

// sign returns the URL of name of movie's resource for v, good until exp.
func (f *fit) sign(t *testing.T, name string, v mediaauth.Viewer, exp time.Time, key string) string {
	t.Helper()
	q, err := f.handler(t).auth.SignURL(movie, v, exp, key)
	if err != nil {
		t.Fatal(err)
	}
	return movie + name + "?" + q
}

// uriLines are the URIs a playlist names: its lines that are not tags, and
// its tags' URI attributes.
var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

func uris(playlist string) []string {
	var out []string
	for _, line := range strings.Split(playlist, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			for _, m := range uriAttr.FindAllStringSubmatch(line, -1) {
				out = append(out, m[1])
			}
		default:
			out = append(out, line)
		}
	}
	return out
}

// TestSignedURLs plays an item through a signed URL: its playlists hand
// every URI a grant, which plays on once the URL has expired; the URL opens
// no other item, nor its own path changed in any byte; and its playlists
// are the viewer's own.
func TestSignedURLs(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "auth": "site", "cache_control": "public, max-age=60"}`, signedConf(`{}`))
	viewer := mediaauth.Viewer{Subject: "viewer-1", Session: "s1"}
	u := f.sign(t, "index.m3u8", viewer, time.Now().Add(time.Hour), "")
	resp, body := f.get(t, u)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("the master: %d %v %q", resp.StatusCode, resp.Header, body)
	}
	master := string(body)
	list := uris(master)
	if len(list) < 3 {
		t.Fatalf("the master names %v", list)
	}
	var grant string
	for _, uri := range list {
		name, q, ok := strings.Cut(uri, "?")
		if !ok || !strings.Contains(q, "&g=1&") || !strings.Contains(q, "sub=viewer-1") || strings.Contains(name, "/") {
			t.Fatalf("a URI without the grant: %q", uri)
		}
		if grant == "" {
			grant = q
		} else if q != grant {
			t.Fatalf("two grants: %q and %q", q, grant)
		}
	}
	// What the playlists name plays with the grant: a media playlist, its
	// init section and segments, and subtitles.
	media := string(f.mustGet(t, movie+"v0.fmp4.m3u8?"+grant, 200))
	for _, uri := range uris(media)[:3] {
		if !strings.HasSuffix(uri, "?"+grant) {
			t.Fatalf("the media playlist hands on %q, not the grant", uri)
		}
		f.mustGet(t, movie+uri, 200)
	}
	subs := string(f.mustGet(t, movie+"s0.m3u8?"+grant, 200))
	f.mustGet(t, movie+uris(subs)[0], 200)
	f.mustGet(t, movie+"v0.mp4?"+grant, 200)
	// A media playlist fetched without a query, by a token, is the one the
	// cache keeps, with the route's Cache-Control.
	resp, plain := f.get(t, movie+"v0.fmp4.m3u8", "Authorization", "Bearer "+viewToken)
	if resp.StatusCode != 200 || strings.Contains(string(plain), "sig=") || resp.Header.Get("Cache-Control") != "public, max-age=60" {
		t.Fatalf("the playlist by a token: %d %v", resp.StatusCode, resp.Header)
	}

	// Expired, the URL is refused, while the grant its playlist handed on
	// plays on.
	expired := f.sign(t, "index.m3u8", viewer, time.Now().Add(-time.Second), "")
	if resp, body := f.get(t, expired); resp.StatusCode != 403 || !strings.Contains(string(body), "expired") {
		t.Fatalf("an expired URL: %d %q", resp.StatusCode, body)
	}
	f.mustGet(t, movie+"v0.1.m4s?"+grant, 200)

	// Another item, and the path changed in any byte.
	_, q, _ := strings.Cut(u, "?")
	if resp, _ := f.get(t, "/vod/films/clips/hevc/index.m3u8?"+q); resp.StatusCode != 403 {
		t.Fatalf("another item: %d", resp.StatusCode)
	}
	for i := len("/vod/films/"); i < len(movie)-1; i++ {
		b := []byte(movie + "index.m3u8")
		b[i] ^= 0x01
		if resp, _ := f.get(t, string(b)+"?"+q); resp.StatusCode == 200 {
			t.Fatalf("%s: 200", b)
		}
	}

	// Bound to an address: refused from another.
	if resp, _ := f.get(t, f.sign(t, "index.m3u8", mediaauth.Viewer{Subject: "v", Address: netip.MustParseAddr("192.0.2.1")}, time.Now().Add(time.Hour), "")); resp.StatusCode != 403 {
		t.Fatalf("another address: %d", resp.StatusCode)
	}
	f.mustGet(t, f.sign(t, "index.m3u8", mediaauth.Viewer{Subject: "v", Address: netip.MustParseAddr("127.0.0.1")}, time.Now().Add(time.Hour), ""), 200)
}

// TestSignedURLSessions refuses a third session past max_sessions 2, and
// rotating a key refuses the URLs it signed and only those.
func TestSignedURLSessions(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "auth": "site"}`, signedConf(`{"max_sessions": 2}`))
	exp := time.Now().Add(time.Hour)
	at := func(sid, key string) string {
		return f.sign(t, "v0.0.m4s", mediaauth.Viewer{Subject: "u", Session: sid}, exp, key)
	}
	f.mustGet(t, at("a", "k1"), 200)
	f.mustGet(t, at("b", "k2"), 200)
	if resp, body := f.get(t, at("c", "k1")); resp.StatusCode != 403 || !strings.Contains(string(body), "session_limit") {
		t.Fatalf("a third session: %d %q", resp.StatusCode, body)
	}
	a, b := at("a", "k1"), at("b", "k2")
	if err := f.handler(t).auth.RotateKey("k1", "the first key, rotated"); err != nil {
		t.Fatal(err)
	}
	if resp, _ := f.get(t, a); resp.StatusCode != 403 {
		t.Fatalf("k1's URL after a rotation: %d", resp.StatusCode)
	}
	f.mustGet(t, b, 200)
}

// TestSignedURLCookie hands the grant on as a cookie: the playlists name
// their URIs plainly, and a client that keeps the cookie plays.
func TestSignedURLCookie(t *testing.T) {
	f := startVOD(t, copyLib(t), "", `{"library": "films", "auth": "site"}`, signedConf(`{"cookie": true}`))
	resp, body := f.get(t, f.sign(t, "v0.fmp4.m3u8", mediaauth.Viewer{Subject: "u"}, time.Now().Add(time.Hour), ""))
	ck := resp.Cookies()
	if resp.StatusCode != 200 || len(ck) != 1 || ck[0].Name != mediaauth.GrantCookie || ck[0].Path != movie || !ck[0].HttpOnly {
		t.Fatalf("the playlist: %d %v", resp.StatusCode, resp.Header)
	}
	if strings.Contains(string(body), "?") {
		t.Fatalf("a playlist names a query:\n%s", body)
	}
	cookie := mediaauth.GrantCookie + "=" + ck[0].Value
	for _, uri := range uris(string(body))[:2] {
		f.mustGet(t, movie+uri, 200, "Cookie", cookie)
	}
	// Kept by the cache, the playlist goes out again with a cookie of its
	// own, and the cookie without it is refused for another item.
	resp, _ = f.get(t, f.sign(t, "v0.fmp4.m3u8", mediaauth.Viewer{Subject: "u"}, time.Now().Add(time.Hour), ""))
	if len(resp.Cookies()) != 1 || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Fatalf("the playlist again: %v", resp.Header)
	}
	if resp, _ := f.get(t, "/vod/films/clips/hevc/index.m3u8", "Cookie", cookie); resp.StatusCode == 200 {
		t.Fatal("the cookie opens another item")
	}
	if resp, _ := f.get(t, movie+"v0.0.m4s"); resp.StatusCode != 401 {
		t.Fatalf("no cookie: %d", resp.StatusCode)
	}
}

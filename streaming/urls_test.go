package streaming

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// signedDoc is doc with url_signing on the policy live.
func signedDoc(t *testing.T) string {
	return strings.Replace(doc(t, allListeners), `"secret": "a-secret-of-sixteen-bytes",`, `"secret": "a-secret-of-sixteen-bytes", "url_signing": {},`, 1)
}

// signFor returns the query of a URL of path for subject, signed as the
// policy live of signedDoc signs, good until exp.
func signFor(t *testing.T, path, subject string, exp time.Time) string {
	t.Helper()
	p, err := mediaauth.New(&config.MediaAuth{Name: "live", Secret: "a-secret-of-sixteen-bytes", URLSigning: &config.URLSigning{}})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := mediaauth.ResourceOf(path)
	if !ok {
		t.Fatalf("no resource of %s", path)
	}
	q, err := p.SignURL(res, mediaauth.Viewer{Subject: subject}, exp, "")
	if err != nil {
		t.Fatal(err)
	}
	return path + "?" + q
}

// A stream, a channel's as any other, plays over HLS and HTTP-FLV through
// a signed URL: its playlists hand every URI a grant; the URL opens no
// other stream, and is refused once expired.
func TestSignedURLStreams(t *testing.T) {
	f := startFit(t, signedDoc(t))
	e := startEngine(t)
	if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 8*time.Second); err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	exp := time.Now().Add(time.Hour)
	u := signFor(t, "/hls/live/cam/index.m3u8", "viewer-9", exp)
	resp, body := get(t, f.base+u, 1<<16, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the playlist: %d %s", resp.StatusCode, body)
	}
	media := ""
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			media = line
		}
	}
	if !strings.Contains(media, "&g=1&") || !strings.Contains(media, "sub=viewer-9") {
		t.Fatalf("the multivariant playlist names %q:\n%s", media, body)
	}
	resp, body = get(t, f.base+"/hls/live/cam/"+media, 1<<16, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the media playlist: %d %s", resp.StatusCode, body)
	}
	_, grant, _ := strings.Cut(media, "?")
	seg := ""
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") && seg == "" {
			seg = line
		}
	}
	if !strings.HasSuffix(seg, "?"+grant) && !strings.Contains(seg, "&"+grant) {
		t.Fatalf("the media playlist names %q, not the grant:\n%s", seg, body)
	}
	if resp, _ = get(t, f.base+"/hls/live/cam/"+seg, 1<<20, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a segment with the grant: %d", resp.StatusCode)
	}
	_, q, _ := strings.Cut(u, "?")
	if resp, _ = get(t, f.base+"/hls/live/other/index.m3u8?"+q, 1<<16, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another stream: %d", resp.StatusCode)
	}
	if resp, _ = get(t, f.base+signFor(t, "/hls/live/cam/index.m3u8", "viewer-9", time.Now().Add(-time.Second)), 1<<16, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("an expired URL: %d", resp.StatusCode)
	}

	resp, body = get(t, f.base+signFor(t, "/flv/live/cam.flv", "viewer-9", exp), 3, nil)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, []byte("FLV")) {
		t.Fatalf("HTTP-FLV: %d %q", resp.StatusCode, body)
	}
	if resp, _ = get(t, f.base+"/flv/live/other.flv?"+strings.SplitN(signFor(t, "/flv/live/cam.flv", "v", exp), "?", 2)[1], 0, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("HTTP-FLV, another stream: %d", resp.StatusCode)
	}
}

// With url_signing's cookie, an HLS playlist sets its grant as a cookie on
// the stream's path, and names its URIs without it.
func TestSignedURLStreamCookie(t *testing.T) {
	f := startFit(t, strings.Replace(signedDoc(t), `"url_signing": {}`, `"url_signing": {"cookie": true}`, 1))
	e := startEngine(t)
	if _, err := publishRTMP(t, e, "rtmp://"+f.addr(t, "rtmp")+"/live/cam?token="+pubToken, 8*time.Second); err != nil {
		t.Fatal(err)
	}
	f.waitLive(t, "live/cam")
	resp, body := get(t, f.base+signFor(t, "/hls/live/cam/index.m3u8", "v", time.Now().Add(time.Hour)), 1<<16, nil)
	ck := resp.Cookies()
	if resp.StatusCode != http.StatusOK || len(ck) != 1 || ck[0].Name != mediaauth.GrantCookie || ck[0].Path != "/hls/live/cam/" || strings.Contains(string(body), "sig=") {
		t.Fatalf("the playlist: %d %v\n%s", resp.StatusCode, resp.Header, body)
	}
	media := ""
	for _, line := range strings.Split(string(body), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			media = line
		}
	}
	if resp, _ = get(t, f.base+"/hls/live/cam/"+media, 1<<16, func(r *http.Request) { r.AddCookie(ck[0]) }); resp.StatusCode != http.StatusOK {
		t.Fatalf("the media playlist with the cookie: %d", resp.StatusCode)
	}
}

package streaming

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/srt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

func TestParseStreamID(t *testing.T) {
	for in, want := range map[string][2]string{
		"#!::r=live/cam?token=T,m=publish": {"live/cam", "token=T"},
		"#!::r=live/cam,m=publish":         {"live/cam", ""},
		"live/cam?user=u&pass=p":           {"live/cam", "user=u&pass=p"},
	} {
		id, err := parseStreamID(in)
		if err != nil || id.Resource != want[0] || id.Query != want[1] {
			t.Errorf("%s: %+v %v", in, id, err)
		}
	}
	for _, bad := range []string{"#!::r=?token=T", "", "#!::m=publish"} {
		if _, err := parseStreamID(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestRTSPPath(t *testing.T) {
	for in, want := range map[string][2]string{
		"rtsp://h/live/cam":                     {"live/cam", ""},
		"rtsp://h:8554/live/cam/?token=T":       {"live/cam", "token=T"},
		"rtsp://h/live/cam?token=T/trackID=0":   {"live/cam", "token=T"},
		"rtsp://h/live/cam?token=T/":            {"live/cam", "token=T"},
		"rtsps://h/live/cam?user=a&pass=b":      {"live/cam", "user=a&pass=b"},
		"/live/cam":                             {"live/cam", ""},
		"rtsp://h/live/cam?token=T/streamid=0/": {"live/cam", "token=T"},
	} {
		p, q, ok := rtspPath(in)
		if !ok || p != want[0] || q != want[1] {
			t.Errorf("%s: %q %q %v", in, p, q, ok)
		}
	}
	if _, _, ok := rtspPath("http://h/x"); ok {
		t.Error("an http URL")
	}
	if _, _, ok := rtspPath("rtsp://h/%zz"); ok {
		t.Error("a bad URL")
	}
}

// Digest: a nonce of another listener, an old one (stale), a realm or
// algorithm not the listener's, Basic not offered.
func TestRTSPDigest(t *testing.T) {
	p, err := mediaauth.New(&config.MediaAuth{Name: "x", Users: []config.MediaUser{{Name: "alice", Password: "pw", Play: []string{"*"}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := newRTSPAuth(&config.StreamRTSP{})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := newRTSPAuth(&config.StreamRTSP{})
	now := time.Now()
	authz := func(nonce, extra string) string {
		return `Digest username="alice", realm="fit", nonce="` + nonce + `", uri="rtsp://h/s", response="` +
			digestResponse("alice", "fit", "pw", "DESCRIBE", "rtsp://h/s", nonce, "", "", "") + `"` + extra
	}
	if c, good, _ := a.verify(p, "DESCRIBE", authz(a.nonce(now), ""), now); !good || c.User != "alice" || c.Password != "pw" {
		t.Fatalf("a right Digest: %+v %v", c, good)
	}
	if _, good, _ := a.verify(p, "DESCRIBE", authz(other.nonce(now), ""), now); good {
		t.Fatal("another listener's nonce")
	}
	if _, good, stale := a.verify(p, "DESCRIBE", authz(a.nonce(now.Add(-time.Hour)), ""), now); good || !stale {
		t.Fatal("an old nonce")
	}
	if _, good, _ := a.verify(p, "DESCRIBE", authz(a.nonce(now), ", algorithm=SHA-256"), now); good {
		t.Fatal("another algorithm")
	}
	if _, good, _ := a.verify(p, "DESCRIBE", "Basic YWxpY2U6cHc=", now); good {
		t.Fatal("Basic, not offered")
	}
	// qop=auth, as most clients answer.
	nonce := a.nonce(now)
	resp := digestResponse("alice", "fit", "pw", "PLAY", "rtsp://h/s", nonce, "auth", "00000001", "abc")
	qop := `Digest username="alice", realm="fit", nonce="` + nonce + `", uri="rtsp://h/s", qop=auth, nc=00000001, cnonce="abc", response="` + resp + `"`
	if _, good, _ := a.verify(p, "PLAY", qop, now); !good {
		t.Fatal("qop=auth")
	}
	if ch := a.challenge(now, true); ch.StatusCode != 401 || !strings.Contains(ch.Header.Get("WWW-Authenticate"), "stale=TRUE") {
		t.Fatalf("a stale challenge: %+v", ch)
	}
	if m := parseAuthParams(strings.Repeat("a=1, ", 40)); len(m) > 16 {
		t.Fatal("parseAuthParams is unbounded")
	}
}

// A listener naming a policy the engine has none of refuses everyone.
func TestGuardFailsClosed(t *testing.T) {
	g := &guard{name: "nope"}
	if _, err := g.allow(mediaauth.Play, "s", mediaauth.Credentials{Token: "x"}); !errors.Is(err, mediaauth.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err := (&guard{}).allow(mediaauth.Play, "s", mediaauth.Credentials{}); err != nil {
		t.Fatal(err)
	}
	var nilGuard *guard
	if _, err := nilGuard.allow(mediaauth.Publish, "s", mediaauth.Credentials{}); err != nil || nilGuard.policy() != nil {
		t.Fatal(err)
	}
	if mediaauth.Status(errRetry) != 503 {
		t.Fatal("errRetry")
	}
	s := &Server{}
	s.refused(protoSRT, srt.RejectForbidden)
	if s.m.refused[protoSRT][refuseUnavailable].Load() != 1 {
		t.Fatal("refused")
	}
}

func TestSameAddress(t *testing.T) {
	if !sameAddress(":1935", "127.0.0.1:1935") || !sameAddress("[::]:1", "127.0.0.1:1") || sameAddress(":0", ":0") ||
		sameAddress("127.0.0.1:1", "127.0.0.2:1") || sameAddress("x", "y") {
		t.Fatal("sameAddress")
	}
}

// Basic that is not base64 or names no user, and a nonce of another length,
// are not right; a quoted value keeps what a backslash escapes, and a
// parameter without = ends the list.
func TestRTSPMalformed(t *testing.T) {
	p, err := mediaauth.New(&config.MediaAuth{Name: "x", Users: []config.MediaUser{{Name: "alice", Password: "pw", Play: []string{"*"}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := newRTSPAuth(&config.StreamRTSP{AuthMethods: []string{"digest", "basic"}})
	now := time.Now()
	for _, authz := range []string{"Basic !!!", "Basic " + base64Std(":pw"), "Basic " + base64Std("alice"),
		`Digest username="alice", realm="fit", nonce="short", uri="rtsp://h/s", response="x"`} {
		if _, good, _ := a.verify(p, "DESCRIBE", authz, now); good {
			t.Errorf("%s was right", authz)
		}
	}
	if c, good, _ := a.verify(p, "DESCRIBE", "Basic "+base64Std("alice:pw"), now); !good || c.User != "alice" {
		t.Fatalf("a right Basic: %+v %v", c, good)
	}
	m := parseAuthParams(`a="x\"y", b=2, c`)
	if m["a"] != `x"y` || m["b"] != "2" || len(m) != 2 {
		t.Fatalf("parseAuthParams: %q", m)
	}
}

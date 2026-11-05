package streaming

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib/media/rtsp"
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
		"rtsp://h/live/cam?token=T/trackID=0":   {"live/cam/trackID=0", "token=T"},
		"rtsp://h/live/cam/trackID=1":           {"live/cam/trackID=1", ""},
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
	for in, want := range map[string]string{
		"live/cam/trackID=1": "live/cam", "live/cam": "live/cam", "trackID=0": "",
		"live/cam/trackID=x": "live/cam/trackID=x", "live/cam/trackID=-1": "live/cam/trackID=-1",
		"live/cam/streamid=0": "live/cam/streamid=0",
	} {
		if got := rtspStream(in); got != want {
			t.Errorf("rtspStream(%q) = %q", in, got)
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
	if c, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/s", authz(a.nonce(now), ""), now); !good || c.User != "alice" || c.Password != "pw" {
		t.Fatalf("a right Digest: %+v %v", c, good)
	}
	if _, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/s", authz(other.nonce(now), ""), now); good {
		t.Fatal("another listener's nonce")
	}
	if _, good, stale := a.verify(p, "DESCRIBE", "rtsp://h/s", authz(a.nonce(now.Add(-time.Hour)), ""), now); good || !stale {
		t.Fatal("an old nonce")
	}
	if _, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/s", authz(a.nonce(now), ", algorithm=SHA-256"), now); good {
		t.Fatal("another algorithm")
	}
	if _, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/s", "Basic YWxpY2U6cHc=", now); good {
		t.Fatal("Basic, not offered")
	}
	// A Digest is good for the URL it was made for only, absolute or not: one
	// captured is not replayed on another stream.
	if _, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/other", authz(a.nonce(now), ""), now); good {
		t.Fatal("a Digest for another URL")
	}
	if _, good, _ := a.verify(p, "DESCRIBE", "/s", authz(a.nonce(now), ""), now); !good {
		t.Fatal("a Digest for the URL's absolute form")
	}
	if !sameURI("/s?x", "rtsp://h/s?x") || !sameURI("rtsp://h", "/") || !sameURI("rtsp://h?q", "/?q") ||
		sameURI("", "rtsp://h/s") || sameURI("http://h/s", "/s") {
		t.Fatal("sameURI")
	}
	// qop=auth, as most clients answer.
	nonce := a.nonce(now)
	resp := digestResponse("alice", "fit", "pw", "PLAY", "rtsp://h/s", nonce, "auth", "00000001", "abc")
	qop := `Digest username="alice", realm="fit", nonce="` + nonce + `", uri="rtsp://h/s", qop=auth, nc=00000001, cnonce="abc", response="` + resp + `"`
	if _, good, _ := a.verify(p, "PLAY", "rtsp://h/s", qop, now); !good {
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
		if _, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/s", authz, now); good {
			t.Errorf("%s was right", authz)
		}
	}
	if c, good, _ := a.verify(p, "DESCRIBE", "rtsp://h/s", "Basic "+base64Std("alice:pw"), now); !good || c.User != "alice" {
		t.Fatalf("a right Basic: %+v %v", c, good)
	}
	m := parseAuthParams(`a="x\"y", b=2, c`)
	if m["a"] != `x"y` || m["b"] != "2" || len(m) != 2 {
		t.Fatalf("parseAuthParams: %q", m)
	}
}

// The streams an RTSP connection's SETUPs name are checked, and a PLAY is
// checked against every one of them as well as its own URL: fib plays the
// stream a SETUP chose, whatever the PLAY URL says. A SETUP under the stream
// the connection announced is a publisher's and needs publish on that
// stream; should fib have refused the ANNOUNCE and play it, the PLAY after
// it needs play on it.
func TestRTSPSetupAuth(t *testing.T) {
	const pa, view = "publish-a-play-b-token", "view-a-token-0123456"
	p, err := mediaauth.New(&config.MediaAuth{Name: "x", Tokens: []config.MediaToken{
		{Token: pa, Publish: []string{"a"}, Play: []string{"b"}},
		{Token: view, Play: []string{"a", "n*"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := newRTSPAuth(&config.StreamRTSP{})
	if err != nil {
		t.Fatal(err)
	}
	a.s = &Server{}
	a.s.auth[protoRTSP].Store(&guard{name: "x", p: p})
	sc := new(rtsp.ServerConn)
	do := func(method, url string) int {
		t.Helper()
		resp := a.onRequest(sc, &rtsp.Request{Method: method, URL: url})
		if resp == nil {
			return 200
		}
		return resp.StatusCode
	}
	cases := []struct {
		method, url string
		want        int
	}{
		// The attack: a SETUP of a stream the client may not play, then a
		// PLAY of one it may.
		{rtsp.MethodSetup, "rtsp://h/secret/trackID=0", 401},
		{rtsp.MethodSetup, "rtsp://h/secret?token=" + view + "/trackID=0", 403},
		{rtsp.MethodSetup, "rtsp://h/secret/trackID=0?token=" + view, 403},
		{rtsp.MethodPlay, "rtsp://h/a?token=" + view, 200},
		// A player's own stream, by track and aggregate.
		{rtsp.MethodDescribe, "rtsp://h/a?token=" + view, 200},
		{rtsp.MethodSetup, "rtsp://h/a?token=" + view + "/trackID=0", 200},
		{rtsp.MethodSetup, "rtsp://h/a/trackID=1?token=" + view, 200},
		{rtsp.MethodPlay, "rtsp://h/a?token=" + view + "/", 200},
		{rtsp.MethodPlay, "rtsp://h/b?token=" + pa, 403}, // a was set up
		{rtsp.MethodPlay, "rtsp://h/a/trackID=0?token=" + view, 200},
	}
	for _, c := range cases {
		if got := do(c.method, c.url); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.url, got, c.want)
		}
	}
	if st := a.state(sc, false); st == nil || len(st.setups) != 1 || st.setups[0] != "a" {
		t.Fatalf("setups: %+v", st)
	}

	// A publisher: ANNOUNCE and SETUPs under it need publish, not play.
	a.forget(sc)
	pub := new(rtsp.ServerConn)
	sc = pub
	for _, c := range []struct {
		method, url string
		want        int
	}{
		{rtsp.MethodAnnounce, "rtsp://h/a?token=" + view, 403},
		{rtsp.MethodSetup, "rtsp://h/a?token=" + pa + "/trackID=0", 403}, // nothing announced: play on a
		{rtsp.MethodAnnounce, "rtsp://h/a?token=" + pa, 200},
		{rtsp.MethodSetup, "rtsp://h/a?token=" + pa + "/streamid=0", 200},
		{rtsp.MethodSetup, "rtsp://h/a/streamid=1?token=" + pa, 200},
		{rtsp.MethodSetup, "rtsp://h/a/trackID=0?token=" + view, 403}, // publish on a
		{rtsp.MethodRecord, "rtsp://h/a?token=" + pa, 200},
		// Should fib have refused the ANNOUNCE and played a for the
		// SETUP, a PLAY of b needs play on a too.
		{rtsp.MethodPlay, "rtsp://h/b?token=" + pa, 403},
	} {
		if got := do(c.method, c.url); got != c.want {
			t.Errorf("%s %s: %d, want %d", c.method, c.url, got, c.want)
		}
	}

	// The streams a connection's SETUPs name are bounded.
	sc = new(rtsp.ServerConn)
	for i := range rtspMaxSetupStreams {
		if got := do(rtsp.MethodSetup, "rtsp://h/n"+strconv.Itoa(i)+"/trackID=0?token="+view); got != 200 {
			t.Fatalf("SETUP %d: %d", i, got)
		}
	}
	if got := do(rtsp.MethodSetup, "rtsp://h/n0/trackID=1?token="+view); got != 200 {
		t.Fatalf("a SETUP of a stream already set up: %d", got)
	}
	if got := do(rtsp.MethodSetup, "rtsp://h/n-more/trackID=0?token="+view); got != rtsp.StatusNotEnoughBandwidth {
		t.Fatalf("a SETUP past the bound: %d", got)
	}

	// Closed connections leave nothing behind; requests other than these
	// five, and a listener without a policy, are not checked.
	a.forget(sc)
	a.forget(pub)
	if len(a.conns) != 0 {
		t.Fatalf("%d connections kept", len(a.conns))
	}
	if do(rtsp.MethodTeardown, "rtsp://h/secret") != 200 || do(rtsp.MethodOptions, "*") != 200 {
		t.Fatal("TEARDOWN or OPTIONS checked")
	}
	a.s.auth[protoRTSP].Store(nil)
	if do(rtsp.MethodSetup, "rtsp://h/secret/trackID=0") != 200 || len(a.conns) != 0 {
		t.Fatal("a listener without a policy")
	}
}

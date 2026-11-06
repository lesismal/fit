package mediaauth

import (
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
)

// secretIn writes a secret file in dir and returns its path.
func secretIn(t *testing.T, dir, name, secret string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// readSecret reads a secret file, which must hold a new secret of at least
// 16 bytes, readable by its owner alone.
func readSecret(t *testing.T, path, old string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); len(s) < config.MinMediaSecret || s == old || strings.ContainsAny(s, "\r\n") {
		t.Fatalf("%s holds %q", path, s)
	}
	if fi, err := os.Stat(path); err != nil || goruntime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("%s: %v %v", path, fi.Mode(), err)
	}
	return string(data)
}

// RotateFile writes a new secret to the file of the single secret or of
// the keys named, rotates to it, and refuses what the old one signed.
func TestRotateFile(t *testing.T) {
	clock(t, t0)
	exp := t0.Add(time.Minute)
	dir := t.TempDir()

	single := urlPolicy(t, func(c *config.MediaAuth) {
		c.Keys = nil
		c.SecretFile = secretIn(t, dir, "single", "the single secret 16+")
	})
	q := signed(t, single, res, Viewer{Subject: "u"}, exp, "")
	allowed(t, "before", guardURL(single, res+"index.m3u8?"+q, res, nil))
	got, err := single.RotateFile("", false)
	if err != nil || len(got) != 1 || got[0] != "" {
		t.Fatalf("the single secret: %q %v", got, err)
	}
	readSecret(t, filepath.Join(dir, "single"), "the single secret 16+")
	refusedFor(t, "the old secret's URL", guardURL(single, res+"index.m3u8?"+q, res, nil), "signature")
	allowed(t, "a URL signed since", guardURL(single, res+"index.m3u8?"+signed(t, single, res, Viewer{Subject: "u"}, exp, ""), res, nil))
	if _, err := single.RotateFile("k1", false); !errors.Is(err, ErrKeyChoice) {
		t.Errorf("a key of a single secret: %v", err)
	}
	if _, err := single.RotateFile("", true); !errors.Is(err, ErrKeyChoice) {
		t.Errorf("all of a single secret: %v", err)
	}

	ring := urlPolicy(t, func(c *config.MediaAuth) {
		c.Keys = []config.MediaKey{{Key: "k1", SecretFile: secretIn(t, dir, "k1", "the first key, 16 bytes+")},
			{Key: "k2", SecretFile: secretIn(t, dir, "k2", "the second key, 16 bytes+")}}
	})
	q1 := signed(t, ring, res, Viewer{Subject: "u"}, exp, "k1")
	q2 := signed(t, ring, res, Viewer{Subject: "u"}, exp, "k2")
	if got, err := ring.RotateFile("k1", false); err != nil || strings.Join(got, ",") != "k1" {
		t.Fatalf("k1: %q %v", got, err)
	}
	s1 := readSecret(t, filepath.Join(dir, "k1"), "the first key, 16 bytes+")
	refusedFor(t, "k1's URL", guardURL(ring, res+"index.m3u8?"+q1, res, nil), "signature")
	allowed(t, "k2's URL", guardURL(ring, res+"index.m3u8?"+q2, res, nil))
	if got, err := ring.RotateFile("", true); err != nil || strings.Join(got, ",") != "k1,k2" {
		t.Fatalf("all: %q %v", got, err)
	}
	readSecret(t, filepath.Join(dir, "k1"), s1)
	readSecret(t, filepath.Join(dir, "k2"), "the second key, 16 bytes+")
	refusedFor(t, "k2's URL after all", guardURL(ring, res+"index.m3u8?"+q2, res, nil), "signature")
	for _, c := range []struct {
		key  string
		all  bool
		want error
	}{{"", false, ErrKeyChoice}, {"k1", true, ErrKeyChoice}, {"k3", false, ErrNoKey}} {
		if _, err := ring.RotateFile(c.key, c.all); !errors.Is(err, c.want) {
			t.Errorf("%q %v: %v, want %v", c.key, c.all, err, c.want)
		}
	}

	// Inline secrets are the config's: nothing is written.
	inline := urlPolicy(t, func(c *config.MediaAuth) { c.Keys = nil; c.Secret = "an inline secret of 16+" })
	if _, err := inline.RotateFile("", false); !errors.Is(err, ErrInlineSecret) {
		t.Errorf("an inline secret: %v", err)
	}
	mixed := urlPolicy(t, func(c *config.MediaAuth) {
		c.Keys = []config.MediaKey{{Key: "k1", SecretFile: secretIn(t, dir, "m1", "the first key, 16 bytes+")}, {Key: "k2", Secret: "an inline key, 16 bytes+"}}
	})
	if _, err := mixed.RotateFile("", true); !errors.Is(err, ErrInlineSecret) {
		t.Errorf("all with an inline key: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "m1")); string(data) != "the first key, 16 bytes+\n" {
		t.Errorf("a refused rotation wrote %q", data)
	}

	// A file that cannot be written leaves the key as it was.
	gone := urlPolicy(t, func(c *config.MediaAuth) {
		c.Keys = []config.MediaKey{{Key: "k1", SecretFile: secretIn(t, dir, "g1", "the first key, 16 bytes+")}}
	})
	qg := signed(t, gone, res, Viewer{Subject: "u"}, exp, "k1")
	if err := os.Remove(filepath.Join(dir, "g1")); err != nil {
		t.Fatal(err)
	}
	gone.st.Load().keys[0].file = filepath.Join(dir, "none", "g1")
	if got, err := gone.RotateFile("k1", false); err == nil || len(got) != 0 {
		t.Errorf("an unwritable file: %q %v", got, err)
	}
	allowed(t, "the key not rotated", guardURL(gone, res+"index.m3u8?"+qg, res, nil))

	removed := &Policy{name: "x"}
	removed.st.Store(&state{removed: true})
	if _, err := removed.RotateFile("", false); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a removed policy: %v", err)
	}
}

// writeSecret's failures past creating its file: one it cannot rename over.
func TestWriteSecretRename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeSecret(target, "a secret of 16 bytes+"); err == nil {
		t.Error("a secret renamed over a directory")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "secret.tmp-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

// Policies, LiveSessions and the role's metrics: the policies of an engine,
// the sessions playing, and the URLs refused by reason.
func TestManageMetrics(t *testing.T) {
	set := clock(t, t0)
	engine := new(fib.Engine)
	r := &role{key: engine}
	var empty observe.TextWriter
	r.WriteMetrics(&empty)
	if len(empty.B) != 0 || len(Policies(engine)) != 0 {
		t.Fatalf("metrics of no policies: %s", empty.B)
	}
	err := Apply(engine, []config.MediaAuth{
		{Name: "site", Secret: "the site's secret, 16+", URLSigning: &config.URLSigning{}},
		{Name: "api", Tokens: []config.MediaToken{{Token: staticToken, Play: []string{"*"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Remove(engine) })
	pols := Policies(engine)
	if len(pols) != 2 || pols[0].Name() != "api" || pols[1].Name() != "site" {
		t.Fatalf("policies: %v", pols)
	}
	p := pols[1]
	exp := t0.Add(time.Hour)
	for _, sid := range []string{"a", "b"} {
		allowed(t, sid, guardURL(p, res+"index.m3u8?"+signed(t, p, res, Viewer{Subject: "u", Session: sid}, exp, ""), res, nil))
	}
	refusedFor(t, "expired", guardURL(p, res+"index.m3u8?"+signed(t, p, res, Viewer{Subject: "u"}, t0, ""), res, nil), "expired")
	p.EndSession("u", "b")
	if n := p.LiveSessions(); n != 1 {
		t.Errorf("live sessions: %d", n)
	}
	var w observe.TextWriter
	r.WriteMetrics(&w)
	for _, line := range []string{
		`fit_media_auth_url_refused_total{policy="site",reason="expired"} 1`,
		`fit_media_auth_url_refused_total{policy="api",reason="session_limit"} 0`,
		`fit_media_auth_sessions{policy="site"} 1`,
		`fit_media_auth_sessions{policy="api"} 0`,
	} {
		if !strings.Contains(string(w.B), line) {
			t.Errorf("metrics lack %s:\n%s", line, w.B)
		}
	}
	set(t0.Add(time.Second + config.DefaultSessionIdle))
	if n := p.LiveSessions(); n != 0 {
		t.Errorf("live sessions past session_idle: %d", n)
	}
}

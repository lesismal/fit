package mediaauth

import (
	"errors"
	stdhttp "net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// whois runs Whois and waits for its answer.
func whois(t *testing.T, p *Policy, c Credentials) (string, Grant, error) {
	t.Helper()
	type res struct {
		s   string
		g   Grant
		err error
	}
	ch := make(chan res, 1)
	p.Whois(c, func(s string, g Grant, err error) { ch <- res{s, g, err} })
	select {
	case r := <-ch:
		return r.s, r.g, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("Whois never answered")
		return "", Grant{}, nil
	}
}

func TestWhois(t *testing.T) {
	p := newPolicy_(t, policyConfig(t))
	tok, _, err := p.Issue("viewer-1", Grant{Play: []string{"vod/*"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		creds   Credentials
		subject string
		play    string
		err     error
	}{
		{"a signed token", Credentials{Token: tok}, "viewer-1", "vod/*", nil},
		{"a static token", Credentials{Token: staticToken}, "token:cam", "*", nil},
		{"a plain password", Credentials{User: "alice", Password: "alice-pw"}, "alice", "*", nil},
		{"a bcrypt password", Credentials{User: "bob", Password: "bob-pw"}, "bob", "live/*", nil},
		{"a wrong password", Credentials{User: "bob", Password: "nope"}, "", "", ErrBadCredentials},
		{"a wrong token", Credentials{Token: "fit1.nonsense"}, "", "", ErrBadCredentials},
		{"none", Credentials{}, "", "", ErrNoCredentials},
	} {
		s, g, err := whois(t, p, c.creds)
		if !errors.Is(err, c.err) || s != c.subject || c.err == nil && (len(g.Play) != 1 || g.Play[0] != c.play) {
			t.Errorf("%s: %q %+v %v", c.name, s, g, err)
		}
	}
	var nilPolicy *Policy
	if _, _, err := whois(t, nilPolicy, Credentials{Token: tok}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a nil policy: %v", err)
	}
	removed := newPolicy_(t, policyConfig(t))
	removed.st.Load().removed = true
	if _, _, err := whois(t, removed, Credentials{Token: tok}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("a removed policy: %v", err)
	}
}

func TestViewerAddr(t *testing.T) {
	r := &stdhttp.Request{RemoteAddr: "10.0.0.1:5000", Header: stdhttp.Header{"X-Forwarded-For": {"203.0.113.7, 10.0.0.2"}}}
	cfg := policyConfig(t)
	plain := newPolicy_(t, cfg)
	cfg.URLSigning = &config.URLSigning{TrustedProxies: []string{"10.0.0.0/8"}}
	behind := newPolicy_(t, cfg)
	var nilPolicy *Policy
	for _, c := range []struct {
		name string
		p    *Policy
		want string
	}{
		{"no url_signing", plain, "10.0.0.1"},
		{"a nil policy", nilPolicy, "10.0.0.1"},
		{"behind trusted proxies", behind, "203.0.113.7"},
	} {
		if got := c.p.ViewerAddr(r); got != netip.MustParseAddr(c.want) {
			t.Errorf("%s: %v, want %s", c.name, got, c.want)
		}
	}
}

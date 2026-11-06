package cluster

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/internal/certpin"
)

func TestInputChecks(t *testing.T) {
	ca, _, err := certpin.SelfSigned("ca", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	pin := strings.Repeat("AB:", 31) + "AB"
	ok := func() *Input { return consoleInput("a", "https://10.0.0.1:2020") }
	cases := []struct {
		field string
		edit  func(in *Input)
	}{
		{"", func(in *Input) {}},
		{"", func(in *Input) { in.URL = "http://localhost:2020/base/" }},
		{"", func(in *Input) { in.TLS.CA = string(ca) }},
		{"", func(in *Input) { in.TLS.Fingerprint = pin }},
		{"", func(in *Input) { in.Labels = map[string]string{"region": "eu-west", "tier": ""} }},
		{"", func(in *Input) { in.Access = "" }},
		{"name", func(in *Input) { in.Name = "" }},
		{"name", func(in *Input) { in.Name = "-a" }},
		{"name", func(in *Input) { in.Name = strings.Repeat("a", MaxNameBytes+1) }},
		{"labels", func(in *Input) {
			in.Labels = map[string]string{}
			for i := range MaxLabels + 1 {
				in.Labels[strings.Repeat("k", i+1)] = "v"
			}
		}},
		{"labels", func(in *Input) { in.Labels = map[string]string{"-x": "v"} }},
		{"labels", func(in *Input) { in.Labels = map[string]string{"x": "v "} }},
		{"url", func(in *Input) { in.URL = "" }},
		{"url", func(in *Input) { in.URL = "https://u:p@h" }},
		{"url", func(in *Input) { in.URL = "https://h/?q=1" }},
		{"url", func(in *Input) { in.URL = "http://10.0.0.1:2020" }},
		{"url", func(in *Input) { in.URL = "ftp://h" }},
		{"url", func(in *Input) { in.URL = "::" }},
		{"username", func(in *Input) { in.Username = "" }},
		{"username", func(in *Input) { in.Username = "a\x01" }},
		{"password", func(in *Input) { in.Password = "" }},
		{"password", func(in *Input) { in.Password = "\xff" }},
		{"token", func(in *Input) { in.Access, in.Token = AccessAdminAPI, "" }},
		{"token", func(in *Input) { in.Access, in.Token = AccessAdminAPI, "a b" }},
		{"access", func(in *Input) { in.Access = "ssh" }},
		{"tls", func(in *Input) { in.TLS = TLS{CA: string(ca), Fingerprint: pin} }},
		{"tls.ca", func(in *Input) { in.TLS.CA = strings.Repeat("x", MaxCABytes+1) }},
		{"tls.ca", func(in *Input) { in.TLS.CA = "not PEM" }},
		{"tls.ca", func(in *Input) { in.TLS.CA = "-----BEGIN KEY-----\nAAAA\n-----END KEY-----\n" }},
		{"tls.ca", func(in *Input) { in.TLS.CA = "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n" }},
		{"tls.fingerprint", func(in *Input) { in.TLS.Fingerprint = "abc" }},
		{"tls", func(in *Input) { in.URL, in.TLS.Fingerprint = "http://127.0.0.1:1", pin }},
	}
	for i, c := range cases {
		in := ok()
		c.edit(in)
		m, err := in.member(nil, time.Now())
		var fe *FieldError
		switch {
		case c.field == "" && err != nil:
			t.Errorf("%d: %v", i, err)
		case c.field != "" && (!errors.As(err, &fe) || fe.Field != c.field):
			t.Errorf("%d: want %s, got %v", i, c.field, err)
		case c.field == "" && in.TLS.Fingerprint != "" && m.TLS.Fingerprint != strings.ToLower(pin):
			t.Errorf("%d: the pin %q", i, m.TLS.Fingerprint)
		}
	}

	// A change keeps the credentials it leaves empty, of the same access
	// only; the other access's are dropped.
	old, _ := consoleInput("a", "https://h").member(nil, time.Now())
	in := &Input{Name: "a", URL: "https://h", Username: "ops"}
	m, err := in.member(&old, time.Now())
	if err != nil || m.Password != fakePassword || m.Created != old.Created {
		t.Errorf("%+v %v", m, err)
	}
	in = &Input{Name: "a", URL: "https://h", Access: AccessAdminAPI}
	if _, err := in.member(&old, time.Now()); err == nil {
		t.Error("a token is needed when the access changes")
	}
	in = &Input{Name: "a", URL: "https://h", Access: AccessAdminAPI, Token: "t", Username: "x", Password: "y"}
	if m, err := in.member(&old, time.Now()); err != nil || m.Username != "" || m.Password != "" {
		t.Errorf("%+v %v", m, err)
	}
	tok, _ := tokenInput("b", "https://h").member(nil, time.Now())
	in = &Input{Name: "b", URL: "https://h", Access: AccessAdminAPI}
	if m, err := in.member(&tok, time.Now()); err != nil || m.Token != fakeToken {
		t.Errorf("%+v %v", m, err)
	}
	if v := tok.View(); v.Labels == nil || !v.TokenSet || v.PasswordSet {
		t.Errorf("%+v", v)
	}
}

func TestDocument(t *testing.T) {
	d, newer, err := readDocument(nil)
	if err != nil || newer || d.Version != DocVersion {
		t.Errorf("%+v %v %v", d, newer, err)
	}
	if _, _, err := readDocument([]byte("[")); err == nil {
		t.Error("not JSON")
	}
	if _, newer, _ := readDocument([]byte(`{"version":3}`)); !newer {
		t.Error("a newer version")
	}
	// I141's version 1 is read, and written as version 2.
	if d, newer, err := readDocument([]byte(`{"version":1,"members":[]}`)); newer || err != nil || !strings.Contains(string(d.marshal()), `"version":2`) {
		t.Error("version 1", newer, err)
	}
	// Groups are I144's, templates I145's, rollouts I146's.
	d, _, _ = readDocument([]byte(`{"version":1,"groups":[{"name":"g","selector":"a"}],"templates":[{"name":"t","config":{}}],"rollouts":[{"id":"r","state":"done","members":[]}]}`))
	if got := string(d.marshal()); !strings.Contains(got, `"groups":[{"name":"g","selector":"a",`) || !strings.Contains(got, `"templates":[{"name":"t","groups":null,"order":0,"config":{}`) || !strings.Contains(got, `"rollouts":[{"id":"r","kind":"","planned_by":"","state":"done"`) || !strings.Contains(got, `"members":[]`) {
		t.Errorf("%s", got)
	}
	if _, _, err := readDocument([]byte(`{"version":2,"rollouts":[true]}`)); err == nil {
		t.Error("rollouts that are not")
	}
}

func TestHelpers(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST", "127.0.0.2", "::1"} {
		if !loopback(h) {
			t.Error(h)
		}
	}
	for _, h := range []string{"example.com", "10.0.0.1"} {
		if loopback(h) {
			t.Error(h)
		}
	}
	if printable("a\x7f") || !printable("héllo") {
		t.Error("printable")
	}
	if got := (TLS{}).Verified() + (TLS{CA: "x"}).Verified() + (TLS{Fingerprint: "x"}).Verified(); got != "systemcapin" {
		t.Error(got)
	}
	n := &node{spec: Member{Password: fakePassword, Token: "abc"}}
	if got := n.redact("pw " + fakePassword + " abc " + strings.Repeat("x", maxMessage)); strings.Contains(got, fakePassword) || !strings.Contains(got, "abc") || !strings.HasSuffix(got, "…") {
		t.Errorf("%q", got)
	}
	if got := changes(&Member{}, &Member{}); got != "nothing" {
		t.Error(got)
	}
	a, b := Member{Name: "a", Username: "u", Password: "p", Token: "t", TLS: TLS{CA: "c"}, URL: "u", Access: "x"}, Member{Name: "a"}
	if got := changes(&a, &b); got != "url, access, username, password, token, tls.ca" {
		t.Error(got)
	}
	if nonNil(nil) == nil {
		t.Error("nonNil")
	}
	if (&FieldError{Field: "f", Msg: "m"}).Error() != "f: m" {
		t.Error("FieldError")
	}
}

// TestCapabilities keeps the capabilities the poller looks for those the
// member's Admin API lists.
func TestCapabilities(t *testing.T) {
	if capStatus != admin.CapStatus || capCheck != admin.CapDrift || capReload != admin.CapReload || capVersions != admin.CapVersions ||
		capCache != admin.CapCache || capCachePrefix != admin.CapCachePrefix {
		t.Error("the capabilities differ from package admin's")
	}
}

package labels

import (
	"strings"
	"testing"
)

func TestPolicy(t *testing.T) {
	p := &Policy{AllowedHosts: []string{"*.apps.example.com", "exact.example.com."}}
	for h, want := range map[string]bool{
		"a.apps.example.com": true, "A.b.Apps.Example.com": true, "*.apps.example.com": true, "**.apps.example.com": true,
		"exact.example.com": true, "apps.example.com": false, "evilapps.example.com": false, "*.exact.example.com": false,
		"admin.example.com": false, "*": false, "**.example.com": false,
	} {
		if got := p.hostAllowed(h); got != want {
			t.Errorf("hostAllowed(%q) = %v", h, got)
		}
	}
	// Settings of the upstream's that the config refuses leave the whole
	// source out; a proxy_protocol fit has not is refused as it is read.
	l, err := Parse(map[string]string{"fit.upstream": `{"load_balancing": "bogus"}`}, "fit.", nil, kind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Restrict("u", p, nil); err == nil || !strings.Contains(err.Error(), "the upstream's settings: load_balancing") {
		t.Errorf("Restrict: %v", err)
	}
	if _, err := Parse(map[string]string{"fit.services.s.proxy_protocol": "v3"}, "fit.", nil, kind); err == nil {
		t.Error("proxy_protocol v3 is taken")
	}
	l, _ = Parse(map[string]string{"fit.services.s.listeners": "tcp", "fit.services.s.server_names": "10.0.0.1"}, "fit.", nil, kind)
	if problems, err := l.Restrict("u", p, nil); err != nil || len(problems) != 1 || !strings.HasPrefix(problems[0], "service s: server_names[0]") || len(l.Services) != 0 {
		t.Errorf("Restrict: %v %v", problems, err)
	}
	if !(&Policy{}).Local("127.0.0.1") || (&Policy{AllowLocal: true}).Local("127.0.0.1") {
		t.Error("Local")
	}
}

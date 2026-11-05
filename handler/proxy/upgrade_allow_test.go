package proxy

import (
	stdhttp "net/http"
	"strings"
	"testing"
)

// TestUpgradeAllowed checks that only websocket, and what upgrade_protocols
// names, is tunnelled: an h2c upgrade, which would open the upstream's HTTP/2
// to requests the proxy never sees, goes on as an ordinary request, without
// Upgrade or HTTP2-Settings.
func TestUpgradeAllowed(t *testing.T) {
	addr := echoBackend(t, "http/1.1")
	// A listener without h2c, which would take an h2c upgrade itself.
	h1 := func(params string) string {
		return strings.Replace(config1(params), `["http/1.1", "h2c"]`, `["http/1.1"]`, 1)
	}
	plain := startFit(t, h1(`{"upstream": "app"}`), addr)
	allowing := startFit(t, h1(`{"upstream": "app", "upgrade_protocols": ["Foo", "tls/1.2"]}`), addr)
	for _, tc := range []struct {
		base, upgrade string
		tunnelled     bool
	}{
		{plain, "h2c", false},
		{plain, "H2C", false},
		{plain, "foo", false},
		{allowing, "h2c", false},
		{allowing, "foo", true},
		{allowing, "TLS/1.2", true},
		{allowing, "bar", false},
	} {
		req, _ := stdhttp.NewRequest("GET", tc.base+"/", nil)
		req.Header.Set("Connection", "Upgrade, HTTP2-Settings")
		req.Header.Set("Upgrade", tc.upgrade)
		req.Header.Set("HTTP2-Settings", "AAMAAABkAAQAoAAAAAIAAAAA")
		resp, e := send(t, client(false), req)
		if resp.StatusCode != 201 {
			t.Errorf("%s: %d", tc.upgrade, resp.StatusCode)
		}
		if _, ok := e.Header["Http2-Settings"]; ok {
			t.Errorf("%s: HTTP2-Settings reached the upstream", tc.upgrade)
		}
		got := e.Header.Get("Upgrade")
		switch {
		case tc.tunnelled && (got != tc.upgrade || !strings.EqualFold(e.Header.Get("Connection"), "Upgrade")):
			t.Errorf("%s: upstream saw Upgrade %q Connection %q, want the upgrade", tc.upgrade, got, e.Header.Get("Connection"))
		case !tc.tunnelled && (got != "" || e.Header.Get("Connection") != ""):
			t.Errorf("%s: upstream saw Upgrade %q Connection %q, want none", tc.upgrade, got, e.Header.Get("Connection"))
		}
	}
}

func TestUpgradeProtocolsParam(t *testing.T) {
	for params, want := range map[string]string{
		`["h2c"]`:       "h2c cannot be tunnelled",
		`["H2C/1"]`:     "h2c cannot be tunnelled",
		`["a b"]`:       `"a b" is not a protocol`,
		`["a/b/c"]`:     `"a/b/c" is not a protocol`,
		`["a/"]`:        `"a/" is not a protocol`,
		`[""]`:          `"" is not a protocol`,
		`["ok", "x/1"]`: "",
	} {
		doc := strings.Replace(config1(`{"upstream": "app", "upgrade_protocols": `+params+`}`), "%s", `"127.0.0.1:1"`, 1)
		_, err := compileDoc(t, doc)
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v, want %q", params, err, want)
		}
	}
}

package config

import (
	"strings"
	"testing"
)

// tls.max_version takes "1.2" or "1.3", quoted or bare, as YAML and .conf
// write it unquoted, and nothing else.
func TestTLSMaxVersion(t *testing.T) {
	doc := func(v string) string {
		return `{"listeners": [{"name": "a", "address": ":443", "tls": {"acme": ["a.example"], "max_version": ` + v + `}}],
  "acme": {"email": "x@a.example"}}`
	}
	for v, want := range map[string]string{`"1.2"`: "1.2", `1.2`: "1.2", `"1.3"`: "1.3", `1.3`: "1.3"} {
		c, err := Load([]byte(doc(v)))
		if err != nil {
			t.Errorf("%s: %v", v, err)
			continue
		}
		if got := c.Listeners[0].TLS.MaxVersion; got != want {
			t.Errorf("%s: %q", v, got)
		}
	}
	for _, v := range []string{`"1.1"`, `1.1`, `1`, `12`, `true`, `"tls1.2"`} {
		if _, err := Load([]byte(doc(v))); err == nil || !strings.Contains(err.Error(), "listeners[0].tls.max_version") {
			t.Errorf("%s: %v", v, err)
		}
	}
}

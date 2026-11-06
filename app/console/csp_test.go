package console

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The Content-Security-Policy: the console's own alone, unless Sources
// names origins, which images, fetches and media may come from too.
func TestPolicySources(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if got := (&Console{}).policy(r); got != csp {
		t.Errorf("without Sources: %s", got)
	}
	none := &Console{sources: func(*http.Request) []string { return nil }}
	if got := none.policy(r); got != csp {
		t.Errorf("without origins: %s", got)
	}
	var asked *http.Request
	two := &Console{sources: func(r *http.Request) []string {
		asked = r
		return []string{"http://192.168.1.5:9401", "https://[::1]:8443"}
	}}
	got := two.policy(r)
	for _, want := range []string{
		"img-src 'self' data: http://192.168.1.5:9401 https://[::1]:8443;",
		"connect-src 'self' http://192.168.1.5:9401 https://[::1]:8443;",
		"media-src 'self' blob: http://192.168.1.5:9401 https://[::1]:8443;",
		"default-src 'none'; script-src 'self'; style-src 'self';",
		"base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy lacks %q: %s", want, got)
		}
	}
	if asked != r {
		t.Error("Sources was not asked of the request")
	}

	// Served: every response carries it.
	_, base := startConsole(t)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Content-Security-Policy"); got != csp {
		t.Errorf("served: %s", got)
	}
}

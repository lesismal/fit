package console

import (
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"testing"
	"time"
)

// A module's Prefixes are served at their whole paths, behind the login and
// the CSRF token; the console's own endpoints come first, and a path no
// module claims is no endpoint.
func TestModulePrefixes(t *testing.T) {
	_, base := startConsole(t)
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, c: &http.Client{Jar: jar, Timeout: 10 * time.Second}, base: base}
	if code, _ := b.do("GET", "/api/x/a", ""); code != 401 {
		t.Fatalf("before logging in: %d", code)
	}
	code, body := b.do("POST", "/api/login", `{"username":"admin","password":"secret"}`)
	if code != 200 {
		t.Fatalf("login: %d %s", code, body)
	}
	var s sessionBody
	_ = json.Unmarshal([]byte(body), &s)
	if code, body := b.do("GET", "/api/x/a/b", ""); code != 200 || body != `{"user":"admin","x":"/api/x/a/b"}` {
		t.Errorf("%d %s", code, body)
	}
	if code, _ := b.do("POST", "/api/x/a", "{}"); code != 403 {
		t.Errorf("without the CSRF token: %d", code)
	}
	b.csrf = s.CSRF
	if code, _ := b.do("POST", "/api/x/a", "{}"); code != 200 {
		t.Errorf("with the CSRF token: %d", code)
	}
	if code, body := b.do("GET", "/api/now", ""); code != 200 || body != `{"user":"admin"}` {
		t.Errorf("the console's own: %d %s", code, body)
	}
	if code, _ := b.do("GET", "/api/y/a", ""); code != 404 {
		t.Errorf("no module's: %d", code)
	}
}

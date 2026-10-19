package proxy

import (
	"io"
	stdhttp "net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/upstream"
)

// named starts an HTTP/1.1 backend that answers its name, and sets a cookie
// of its own.
func named(t *testing.T, name string) string {
	return serveBackend(t, "http/1.1", stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		w.Header().Add("Set-Cookie", "own=1")
		_, _ = io.WriteString(w, name)
	}))
}

func get(t *testing.T, url string, header ...string) (string, []string) {
	t.Helper()
	req, _ := stdhttp.NewRequest("GET", url, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := client(false).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body), resp.Header.Values("Set-Cookie")
}

func TestStickyCookie(t *testing.T) {
	a, b := named(t, "a"), named(t, "b")
	doc := strings.Replace(config1(`{"upstream": "app"}`), `"addresses": [%s]`, `"addresses": [%s], "sticky_cookie": {"max_age": "1h"}`, 1)
	base := startFit(t, doc, a, b)

	first, cookies := get(t, base+"/")
	if len(cookies) != 2 || cookies[0] != "own=1" || !strings.HasPrefix(cookies[1], "fit_sticky=") ||
		!strings.HasSuffix(cookies[1], "; Path=/; Max-Age=3600; HttpOnly; SameSite=Lax") {
		t.Fatalf("Set-Cookie: %q", cookies)
	}
	addr := map[string]string{"a": a, "b": b}[first]
	if want := "fit_sticky=" + upstream.StickyID(addr); !strings.HasPrefix(cookies[1], want+";") {
		t.Fatalf("cookie %q, want %s", cookies[1], want)
	}
	cookie := strings.SplitN(cookies[1], ";", 2)[0]
	for range 10 {
		body, cookies := get(t, base+"/", "Cookie", "x=y; "+cookie)
		if body != first || len(cookies) != 1 {
			t.Fatalf("with the cookie: %q from %s, want %s", cookies, body, first)
		}
	}
	// A cookie naming no address is replaced.
	if _, cookies := get(t, base+"/", "Cookie", "fit_sticky=0000"); len(cookies) != 2 {
		t.Fatalf("a stale cookie: %q", cookies)
	}
}

func TestHeaderHash(t *testing.T) {
	backends := []string{named(t, "a"), named(t, "b"), named(t, "c")}
	doc := strings.Replace(config1(`{"upstream": "app"}`), `"addresses": [%s]`, `"addresses": [%s], "load_balancing": "header_hash", "hash_key": "x-user"`, 1)
	base := startFit(t, doc, backends...)
	seen := map[string]bool{}
	for _, user := range []string{"ann", "bob", "cat", "dan", "eve", "fay", "gus", "hal"} {
		first, _ := get(t, base+"/", "X-User", user)
		for range 5 {
			if got, _ := get(t, base+"/", "X-User", user); got != first {
				t.Fatalf("user %s: %s, then %s", user, first, got)
			}
		}
		seen[first] = true
	}
	if len(seen) < 2 {
		t.Fatalf("eight users all went to %v", seen)
	}
}

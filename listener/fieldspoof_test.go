package listener

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// TestIdentityFieldSpellings sends, over HTTP/1.1 and h2c, requests that
// carry a forged X-User in the spellings an application behind CGI reads as
// the same field, through basic_auth with user_header and a proxy: the
// upstream must see the X-User basic_auth set, and no other field that folds
// to it. It checks end to end what the auth middleware's field set relies
// on, that fib's servers key every such spelling as textproto's canonical
// form does.
func TestIdentityFieldSpellings(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	be := &stdhttp.Server{Handler: stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		// Every field an application folding - and _ reads as X-User.
		var seen []string
		for key, values := range r.Header {
			if strings.EqualFold(strings.ReplaceAll(key, "_", "-"), "X-User") {
				for _, v := range values {
					seen = append(seen, key+"="+v)
				}
			}
		}
		slices.Sort(seen)
		_, _ = io.WriteString(w, strings.Join(seen, ","))
	})}
	go func() { _ = be.Serve(ln) }()
	t.Cleanup(func() { _ = be.Close() })

	hash, err := bcrypt.GenerateFromPassword([]byte("builder"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	doc := `{
 "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
 "upstreams": [{"name": "app", "addresses": ["` + ln.Addr().String() + `"]}],
 "routes": [{"middleware": [{"module": "basic_auth", "params": {"user_header": "X-User",
   "users": [{"username": "bob", "password_hash": "` + string(hash) + `"}]}}],
  "handler": {"module": "proxy", "params": {"upstream": "app"}}}]}`
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	g, err := runtime.Compile(c, runtime.Env{Engine: engine})
	if err != nil {
		t.Fatal(err)
	}
	rt := new(runtime.Runtime)
	rt.Swap(g)
	s, err := Start(rt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); rt.Close(); stop() })
	addr := s.Addrs()[0].Addr.String()

	const want = "X-User=bob"
	forged := []string{"X_User", "x_user", "X_USER", "x_uSeR", "X-user", "x-USER"}

	// HTTP/1.1, the field names written as they are.
	for _, name := range forged {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		_, _ = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nAuthorization: Basic Ym9iOmJ1aWxkZXI=\r\n%s: alice\r\nConnection: close\r\n\r\n", name)
		res, err := stdhttp.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = conn.Close()
		if res.StatusCode != 200 || string(body) != want {
			t.Errorf("http/1.1 %s: %d %q, want 200 %q", name, res.StatusCode, body, want)
		}
	}

	// h2c, where field names arrive lowercase.
	var p stdhttp.Protocols
	p.SetUnencryptedHTTP2(true)
	client := &stdhttp.Client{Transport: &stdhttp.Transport{Protocols: &p}, Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	for _, name := range forged {
		req, _ := stdhttp.NewRequest("GET", "http://"+addr+"/", nil)
		req.SetBasicAuth("bob", "builder")
		req.Header[name] = []string{"alice"}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.ProtoMajor != 2 {
			t.Fatalf("h2c: the request went over HTTP/%d", res.ProtoMajor)
		}
		if res.StatusCode != 200 || string(body) != want {
			t.Errorf("h2c %s: %d %q, want 200 %q", name, res.StatusCode, body, want)
		}
	}
}

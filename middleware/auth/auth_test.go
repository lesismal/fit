package auth

import (
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/module"
)

func load(t testing.TB, name, params string) (module.Middleware, error) {
	t.Helper()
	v, err := config.ParseValue([]byte(`{"module": "`+name+`", "params": `+params+`}`), "middleware")
	if err != nil {
		t.Fatal(err)
	}
	var ref config.ModuleRef
	if err := v.Decode(&ref); err != nil {
		return nil, err
	}
	m, err := module.Load(module.Middlewares, ref, nil)
	if err != nil {
		return nil, err
	}
	return m.(module.Middleware), nil
}

func mustLoad[T any](t testing.TB, name, params string) T {
	t.Helper()
	m, err := load(t, name, params)
	if err != nil {
		t.Fatal(err)
	}
	return m.(T)
}

// hash is the bcrypt hash of password at the lowest cost, which keeps the
// tests quick.
func hash(t testing.TB, password string) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}

// echo answers with who the request was authenticated as, and the fields
// the middleware set or left.
var echo = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	h := c.Request.Header
	_ = c.Respond(200, "text/plain", []byte("user="+UserOf(c)+" x-user="+h.Get("X-User")+
		" x-subject="+h.Get("X-Subject")+" authorization="+h.Get("Authorization")))
})

func basicRequest(user, password string) *stdhttp.Request {
	r := httptest.NewRequest("GET", "/", nil)
	if user != "" {
		r.SetBasicAuth(user, password)
	}
	return r
}

func TestBasic(t *testing.T) {
	ba := mustLoad[*Basic](t, "basic_auth", `{"realm": "staff", "user_header": "x-user",
  "users": [{"username": "alice", "password_hash": "`+hash(t, "wonderland")+`"},
            {"username": "bob", "password_hash": "`+hash(t, "builder")+`"}]}`)
	h := ba.Wrap(echo)
	refused := func(user, password string) {
		t.Helper()
		res := fibtest.ServeRequest(h, basicRequest(user, password)).Response
		if res.StatusCode != 401 || res.Header.Get("Www-Authenticate") != `Basic realm="staff", charset="UTF-8"` {
			t.Errorf("%q %q: %d %v, want 401 asking for a password", user, password, res.StatusCode, res.Header)
		}
	}
	admitted := func(user, password string) {
		t.Helper()
		rec := fibtest.ServeRequest(h, basicRequest(user, password))
		want := "user=" + user + " x-user=" + user + " x-subject= authorization="
		if rec.Response.StatusCode != 200 || string(rec.Response.Body) != want {
			t.Errorf("%q %q: %d %q, want 200 %q", user, password, rec.Response.StatusCode, rec.Response.Body, want)
		}
	}

	refused("", "")
	refused("alice", "")
	refused("alice", "Wonderland")
	refused("carol", "wonderland") // unknown, though alice's password
	refused("bob", "wonderland")   // another user's password
	if ba.accounts["alice"].good.Load() != nil {
		t.Fatal("a password remembered before one passed")
	}
	admitted("alice", "wonderland")
	if ba.accounts["alice"].good.Load() == nil {
		t.Fatal("the password that passed is not remembered")
	}
	admitted("alice", "wonderland") // remembered: answered at once
	refused("alice", "wonderland!") // another password is checked anew
	admitted("bob", "builder")
	admitted("alice", "wonderland") // each user remembers their own

	// A client may not say who it is in the user's field.
	r := basicRequest("", "")
	r.Header.Set("X-User", "alice")
	if res := fibtest.ServeRequest(h, r).Response; res.StatusCode != 401 {
		t.Errorf("without a password: %d", res.StatusCode)
	}
	r = basicRequest("bob", "builder")
	r.Header.Add("X-User", "alice")
	if rec := fibtest.ServeRequest(h, r); !strings.Contains(string(rec.Response.Body), " x-user=bob ") {
		t.Errorf("the client's own field kept: %q", rec.Response.Body)
	}
}

func TestBasicForwardAuthorization(t *testing.T) {
	h := mustLoad[*Basic](t, "basic_auth", `{"forward_authorization": true,
  "users": [{"username": "alice", "password_hash": "`+hash(t, "pw")+`"}]}`).Wrap(echo)
	for range 2 { // checked, then remembered
		rec := fibtest.ServeRequest(h, basicRequest("alice", "pw"))
		if !strings.HasSuffix(string(rec.Response.Body), " authorization=Basic YWxpY2U6cHc=") {
			t.Errorf("%d %q", rec.Response.StatusCode, rec.Response.Body)
		}
	}
	res := fibtest.ServeRequest(h, basicRequest("", "")).Response
	if got := res.Header.Get("Www-Authenticate"); got != `Basic realm="fit", charset="UTF-8"` {
		t.Errorf("realm by default: %q", got)
	}
}

// withPool runs f with checks replaced by p.
func withPool(t *testing.T, p *pool, f func()) {
	saved := checks
	checks = p
	defer func() { checks = saved }()
	f()
}

// TestBasicBusy checks that a check the pool has no room for is answered
// 503 at once, and one that waited too long is answered 503 when it comes
// up, while a remembered password is let in all along.
func TestBasicBusy(t *testing.T) {
	ba := mustLoad[*Basic](t, "basic_auth", `{"users": [{"username": "alice", "password_hash": "`+hash(t, "pw")+`"}]}`)
	h := ba.Wrap(echo)
	full := &pool{}
	full.once.Do(func() { full.jobs = make(chan *check) }) // no room, no workers
	withPool(t, full, func() {
		res := fibtest.ServeRequest(h, basicRequest("alice", "pw")).Response
		if res.StatusCode != 503 || res.Header.Get("Retry-After") != "1" {
			t.Errorf("no room: %d %v", res.StatusCode, res.Header)
		}
	})

	held := &pool{}
	held.once.Do(func() { held.jobs = make(chan *check, 1) }) // room for one, no workers
	withPool(t, held, func() {
		rec := &fibtest.Recorder{}
		fibhttp.Serve(h, fibhttp.NewStreamContext(nil, basicRequest("alice", "pw"), rec))
		k := <-held.jobs
		k.queued = k.queued.Add(-maxWait - time.Second)
		held.do(k)
		if !rec.Wait(5*time.Second) || rec.Response.StatusCode != 503 {
			t.Fatalf("waited too long: %+v", rec.Response)
		}

		// Checked on a pool with a worker, the password is remembered...
		withPool(t, &pool{workers: 1}, func() {
			if res := fibtest.ServeRequest(h, basicRequest("alice", "pw")).Response; res.StatusCode != 200 {
				t.Fatalf("checked: %d", res.StatusCode)
			}
		})
		// ...and let in with no room for checks.
		if res := fibtest.ServeRequest(h, basicRequest("alice", "pw")).Response; res.StatusCode != 200 {
			t.Errorf("remembered: %d", res.StatusCode)
		}
	})
}

func TestBasicParams(t *testing.T) {
	good := hash(t, "pw")
	for _, tt := range []struct{ params, err string }{
		{`{"users": []}`, "at least one user"},
		{`{"users": [{"username": "a:b", "password_hash": "` + good + `"}]}`, "not a user name"},
		{`{"users": [{"username": "", "password_hash": "` + good + `"}]}`, "not a user name"},
		{`{"users": [{"username": "a", "password_hash": "` + good + `"}, {"username": "a", "password_hash": "` + good + `"}]}`, "listed twice"},
		{`{"users": [{"username": "a", "password_hash": "pw"}]}`, "not a bcrypt hash"},
		{`{"realm": "say \"hi\"", "users": [{"username": "a", "password_hash": "` + good + `"}]}`, "quote"},
		{`{"user_header": "x user", "users": [{"username": "a", "password_hash": "` + good + `"}]}`, "not a field name"},
		{`{}`, "users"},
	} {
		if _, err := load(t, "basic_auth", tt.params); err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: %v, want %q", tt.params, err, tt.err)
		}
	}
	if _, err := load(t, "client_cert", `{"subject_header": "a b"}`); err == nil || !strings.Contains(err.Error(), "not a field name") {
		t.Errorf("client_cert: %v", err)
	}
}

// tlsRequest is a request over TLS whose client sent cert, verified when
// verified says so.
func tlsRequest(cert *x509.Certificate, verified bool) *stdhttp.Request {
	r := httptest.NewRequest("GET", "https://a.example/", nil)
	r.TLS = &tls.ConnectionState{HandshakeComplete: true}
	if cert != nil {
		r.TLS.PeerCertificates = []*x509.Certificate{cert}
		if verified {
			r.TLS.VerifiedChains = [][]*x509.Certificate{{cert}}
		}
	}
	return r
}

func TestClientCert(t *testing.T) {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "alice", Organization: []string{"Example"}}}
	h := mustLoad[*ClientCert](t, "client_cert", `{"subject_header": "x-subject"}`).Wrap(echo)
	optional := mustLoad[*ClientCert](t, "client_cert", `{"subject_header": "x-subject", "optional": true}`).Wrap(echo)
	for _, tt := range []struct {
		name string
		h    module.Handler
		r    *stdhttp.Request
		code int
		body string
	}{
		{"verified", h, tlsRequest(cert, true), 200, "user=CN=alice,O=Example x-user= x-subject=CN=alice,O=Example authorization="},
		{"none", h, tlsRequest(nil, false), 403, "Forbidden\n"},
		{"unverified", h, tlsRequest(cert, false), 403, "Forbidden\n"},
		{"in the clear", h, httptest.NewRequest("GET", "/", nil), 403, "Forbidden\n"},
		{"optional, none", optional, tlsRequest(nil, false), 200, "user= x-user= x-subject= authorization="},
		{"optional, verified", optional, tlsRequest(cert, true), 200, "user=CN=alice,O=Example x-user= x-subject=CN=alice,O=Example authorization="},
	} {
		tt.r.Header.Set("X-Subject", "CN=forged")
		rec := fibtest.ServeRequest(tt.h, tt.r)
		if rec.Response.StatusCode != tt.code || string(rec.Response.Body) != tt.body {
			t.Errorf("%s: %d %q, want %d %q", tt.name, rec.Response.StatusCode, rec.Response.Body, tt.code, tt.body)
		}
	}

	// basic_auth's user stays who the request is, the subject in its field.
	ba := mustLoad[*Basic](t, "basic_auth", `{"users": [{"username": "bob", "password_hash": "`+hash(t, "pw")+`"}]}`)
	both := ba.Wrap(mustLoad[*ClientCert](t, "client_cert", `{"subject_header": "x-subject"}`).Wrap(echo))
	r := tlsRequest(cert, true)
	r.SetBasicAuth("bob", "pw")
	if rec := fibtest.ServeRequest(both, r); string(rec.Response.Body) != "user=bob x-user= x-subject=CN=alice,O=Example authorization=" {
		t.Errorf("both: %d %q", rec.Response.StatusCode, rec.Response.Body)
	}
}

func TestHeaderSafe(t *testing.T) {
	for in, want := range map[string]string{
		"CN=alice":          "CN=alice",
		"CN=a\tb":           "CN=a\tb",
		"CN=a\r\nX-Evil: 1": "CN=a??X-Evil: 1",
		"\x00\x7f":          "??",
		"CN=héllo":          "CN=héllo",
	} {
		if got := headerSafe(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

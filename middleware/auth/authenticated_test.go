package auth

import (
	"crypto/x509"
	"crypto/x509/pkix"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/observe"
)

// authenticatedEcho answers whether the request was marked authenticated.
var authenticatedEcho = fibhttp.HandlerFunc(func(c *fibhttp.Context) {
	if observe.Authenticated(c) {
		_ = c.Respond(200, "text/plain", []byte("yes"))
	} else {
		_ = c.Respond(200, "text/plain", []byte("no"))
	}
})

// TestMarkAuthenticated checks that each auth middleware marks the requests
// it lets in, for a cache behind it, and only those.
func TestMarkAuthenticated(t *testing.T) {
	k := testKeys()
	_, secret := keyFiles(t, k)
	jwtCookie := mustLoad[*JWT](t, "jwt", `{"hmac_secret_file": "`+secret+`", "token_from": "cookie:t"}`).Wrap(authenticatedEcho)
	withCookie := httptest.NewRequest("GET", "/", nil)
	withCookie.AddCookie(&stdhttp.Cookie{Name: "t", Value: sign(t, "HS256", "", k.secret, valid())})
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "alice"}}
	optional := mustLoad[*ClientCert](t, "client_cert", `{"optional": true}`).Wrap(authenticatedEcho)
	basic := mustLoad[*Basic](t, "basic_auth", `{"users": [{"username": "alice", "password_hash": "`+hash(t, "pw")+`"}]}`).Wrap(authenticatedEcho)
	for _, tt := range []struct {
		what string
		h    fibhttp.Handler
		r    *stdhttp.Request
		want string
	}{
		{"basic_auth", basic, basicRequest("alice", "pw"), "yes"},
		{"jwt from a cookie", jwtCookie, withCookie, "yes"},
		{"client_cert", optional, tlsRequest(cert, true), "yes"},
		{"client_cert, optional and absent", optional, tlsRequest(nil, false), "no"},
	} {
		rec := fibtest.ServeRequest(tt.h, tt.r)
		if rec.Response.StatusCode != 200 || string(rec.Response.Body) != tt.want {
			t.Errorf("%s: %d %q, want %q", tt.what, rec.Response.StatusCode, rec.Response.Body, tt.want)
		}
	}
}

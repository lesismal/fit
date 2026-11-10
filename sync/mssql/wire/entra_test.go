package mssqlwire

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// rsaCert is a self-signed RSA certificate with its key.
func rsaCert(t *testing.T) *tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fit"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestEntraSecret gets a token with a client secret: the client
// credentials flow's form at the tenant's endpoint, the token kept until
// five minutes before it expires.
func TestEntraSecret(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		if r.Method != http.MethodPost || r.URL.Path != "/tenant-1/oauth2/v2.0/token" || r.Form.Get("grant_type") != "client_credentials" ||
			r.Form.Get("client_id") != "app-1" || r.Form.Get("client_secret") != "s3cret" || r.Form.Get("scope") != "https://database.windows.net/.default" {
			http.Error(w, `{"error":"invalid_request","error_description":"bad form"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, `{"token_type":"Bearer","expires_in":3599,"access_token":"tok-%d"}`, calls.Load())
	}))
	defer srv.Close()
	now := time.Unix(1_800_000_000, 0)
	e := &EntraClient{TenantID: "tenant-1", ClientID: "app-1", ClientSecret: "s3cret", Authority: srv.URL + "/", now: func() time.Time { return now }}
	for i := 0; i < 2; i++ {
		tok, err := e.Token(context.Background())
		if err != nil || tok != "tok-1" {
			t.Fatal(tok, err)
		}
	}
	now = now.Add(55 * time.Minute)
	if tok, _ := e.Token(context.Background()); tok != "tok-2" {
		t.Fatal(tok)
	}
	e.ClientSecret = "wrong"
	e.token = ""
	if _, err := e.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid_request: bad form") {
		t.Fatal(err)
	}
}

// TestEntraCertificate signs a client assertion: RS256, x5t the
// certificate's SHA-1, the token endpoint its audience.
func TestEntraCertificate(t *testing.T) {
	cert := rsaCert(t)
	var endpoint string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("client_assertion_type") != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" || r.Form.Get("client_secret") != "" {
			http.Error(w, "no assertion", http.StatusBadRequest)
			return
		}
		parts := strings.Split(r.Form.Get("client_assertion"), ".")
		enc := base64.RawURLEncoding
		head, _ := enc.DecodeString(parts[0])
		claims, _ := enc.DecodeString(parts[1])
		sig, _ := enc.DecodeString(parts[2])
		var h map[string]string
		var c map[string]any
		json.Unmarshal(head, &h)
		json.Unmarshal(claims, &c)
		thumb := sha1.Sum(cert.Certificate[0])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		key := cert.PrivateKey.(*rsa.PrivateKey)
		if h["alg"] != "RS256" || h["x5t"] != enc.EncodeToString(thumb[:]) || c["aud"] != endpoint || c["iss"] != "app-1" || c["sub"] != "app-1" ||
			rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil {
			http.Error(w, "a bad assertion", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"access_token":"cert-token","expires_in":"3600"}`))
	}))
	defer srv.Close()
	endpoint = srv.URL + "/t/oauth2/v2.0/token"
	e := &EntraClient{TenantID: "t", ClientID: "app-1", Certificate: cert, Authority: srv.URL, HTTPClient: srv.Client()}
	if tok, err := e.Token(context.Background()); err != nil || tok != "cert-token" || time.Until(e.expires) < 59*time.Minute {
		t.Fatal(tok, err)
	}
	// Certificates that cannot sign.
	for _, c := range []*tls.Certificate{{}, {Certificate: cert.Certificate, PrivateKey: "not a key"}} {
		e := &EntraClient{TenantID: "t", ClientID: "app-1", Certificate: c, Authority: srv.URL}
		if _, err := e.Token(context.Background()); err == nil {
			t.Fatal("signed")
		}
	}
}

// TestEntraManagedIdentity asks IMDS (Metadata: true, api-version
// 2018-02-01) and App Service's endpoint (X-IDENTITY-HEADER) for the
// resource's token, the client ID of a user-assigned identity.
func TestEntraManagedIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("resource") != "https://database.windows.net/" {
			http.Error(w, "resource", http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == "/imds" && r.Header.Get("Metadata") == "true" && q.Get("api-version") == "2018-02-01":
			fmt.Fprintf(w, `{"access_token":"imds-%s","expires_on":"%d"}`, q.Get("client_id"), time.Now().Add(time.Hour).Unix())
		case r.URL.Path == "/appservice" && r.Header.Get("X-IDENTITY-HEADER") == "h1" && q.Get("api-version") == "2019-08-01":
			w.Write([]byte(`{"access_token":"appservice","expires_on":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("not here"))
		}
	}))
	defer srv.Close()
	t.Setenv("IDENTITY_ENDPOINT", "")
	e := &EntraClient{ManagedIdentity: true, ClientID: "mi-1", IdentityEndpoint: srv.URL + "/imds", Resource: DefaultEntraResource}
	if tok, err := e.Token(context.Background()); err != nil || tok != "imds-mi-1" {
		t.Fatal(tok, err)
	}
	t.Setenv("IDENTITY_ENDPOINT", srv.URL+"/appservice")
	t.Setenv("IDENTITY_HEADER", "h1")
	e = &EntraClient{ManagedIdentity: true}
	if tok, err := e.Token(context.Background()); err != nil || tok != "appservice" {
		t.Fatal(tok, err)
	}
	// Expired on arrival: asked again.
	if _, err := e.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IDENTITY_ENDPOINT", srv.URL+"/nowhere")
	e = &EntraClient{ManagedIdentity: true}
	if _, err := e.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "404 Not Found: not here") {
		t.Fatal(err)
	}
	t.Setenv("IDENTITY_ENDPOINT", "")
	e = &EntraClient{ManagedIdentity: true, IdentityEndpoint: "http://[::1"}
	if _, err := e.Token(context.Background()); err == nil {
		t.Fatal("a bad endpoint")
	}
	e = &EntraClient{ManagedIdentity: true}
	if r, err := e.identityRequest(context.Background()); err != nil || !strings.HasPrefix(r.URL.String(), DefaultIMDSEndpoint+"?") {
		t.Fatal(r, err)
	}
}

// TestEntraRefusals: missing settings, an unreachable endpoint, a token
// without its field, requests that cannot be made.
func TestEntraRefusals(t *testing.T) {
	for _, e := range []*EntraClient{{ClientID: "a", ClientSecret: "s"}, {TenantID: "t", ClientSecret: "s"}, {TenantID: "t", ClientID: "a"}} {
		if _, err := e.Token(context.Background()); err == nil {
			t.Fatal("a token")
		}
	}
	e := &EntraClient{TenantID: "t", ClientID: "a", ClientSecret: "s", Authority: "http://127.0.0.1:1"}
	if _, err := e.Token(context.Background()); err == nil {
		t.Fatal("a token")
	}
	e = &EntraClient{TenantID: "t", ClientID: "a", ClientSecret: "s", Authority: "http://bad host"}
	if _, err := e.Token(context.Background()); err == nil {
		t.Fatal("a token")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"token_type":"Bearer"}`)) }))
	defer srv.Close()
	e = &EntraClient{TenantID: "t", ClientID: "a", ClientSecret: "s", Authority: srv.URL}
	if _, err := e.Token(context.Background()); err == nil || !strings.Contains(err.Error(), "200 OK") {
		t.Fatal(err)
	}
	// A body cut short.
	cut := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("{"))
	}))
	defer cut.Close()
	e = &EntraClient{TenantID: "t", ClientID: "a", ClientSecret: "s", Authority: cut.URL}
	if _, err := e.Token(context.Background()); err == nil {
		t.Fatal("a token")
	}
	if n, ok := seconds(true); ok || n != 0 {
		t.Fatal(n)
	}
	if e := (&EntraClient{TenantID: "t/x"}); !strings.Contains(url.PathEscape(e.TenantID), "%2F") {
		t.Fatal("escaped")
	}
}

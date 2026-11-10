package mssqlwire

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The defaults of EntraClient.
const (
	DefaultEntraAuthority = "https://login.microsoftonline.com"
	DefaultEntraResource  = "https://database.windows.net/"
	// DefaultIMDSEndpoint is Azure's instance metadata service's token
	// endpoint, which a VM's (or AKS node's, or container instance's)
	// managed identity answers on.
	DefaultIMDSEndpoint = "http://169.254.169.254/metadata/identity/oauth2/token"
)

// EntraClient gets Microsoft Entra ID access tokens for Azure SQL, written
// on net/http with no Azure SDK: a service principal's, by OAuth 2's client
// credentials flow against the tenant's token endpoint (a client secret,
// or a certificate signing a client assertion), or a managed identity's,
// from the host's identity endpoint. Its Token is a Config.AccessToken; a
// token is kept until five minutes before it expires.
type EntraClient struct {
	// TenantID is the directory (tenant) ID or domain of a service
	// principal.
	TenantID string
	// ClientID is the service principal's application (client) ID; with
	// ManagedIdentity, a user-assigned identity's client ID (the
	// system-assigned identity when empty).
	ClientID string
	// ClientSecret is the service principal's secret.
	ClientSecret string
	// Certificate, with its RSA private key, signs a client assertion in
	// place of a secret.
	Certificate *tls.Certificate
	// ManagedIdentity takes the token from the host's managed identity.
	ManagedIdentity bool

	// Authority is the token endpoint's origin; DefaultEntraAuthority when
	// empty (another cloud's: https://login.microsoftonline.us, ...).
	Authority string
	// Resource is the token's audience; DefaultEntraResource when empty.
	// The client credentials flow asks for its scope Resource + ".default".
	Resource string
	// IdentityEndpoint is the managed identity's token endpoint: App
	// Service's and Functions' IDENTITY_ENDPOINT (with IDENTITY_HEADER)
	// when the environment has them, DefaultIMDSEndpoint otherwise.
	IdentityEndpoint string
	// HTTPClient sends the requests; one with a 30s timeout when nil.
	HTTPClient *http.Client

	mu      sync.Mutex
	token   string
	expires time.Time
	// now is the clock; the tests set it.
	now func() time.Time
}

// tokenSlack is how long before its expiry a kept token is replaced.
const tokenSlack = 5 * time.Minute

// Token returns an access token, a kept one while it is good.
func (e *EntraClient) Token(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	if e.now != nil {
		now = e.now()
	}
	if e.token != "" && now.Add(tokenSlack).Before(e.expires) {
		return e.token, nil
	}
	var req *http.Request
	var err error
	if e.ManagedIdentity {
		req, err = e.identityRequest(ctx)
	} else {
		req, err = e.credentialsRequest(ctx, now)
	}
	if err != nil {
		return "", err
	}
	hc := e.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("entra: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("entra: reading the token: %w", err)
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   any    `json:"expires_in"`
		ExpiresOn   any    `json:"expires_on"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
		Message     string `json:"message"`
	}
	jerr := json.Unmarshal(body, &t)
	if resp.StatusCode != http.StatusOK || t.AccessToken == "" {
		msg := strings.TrimSpace(t.Error + ": " + t.Description + t.Message)
		if jerr != nil || msg == ":" {
			msg = strings.TrimSpace(string(body[:min(len(body), 200)]))
		}
		return "", fmt.Errorf("entra: %s answered %s: %s", req.URL.Host, resp.Status, msg)
	}
	e.token, e.expires = t.AccessToken, now.Add(10*time.Minute)
	if n, ok := seconds(t.ExpiresIn); ok {
		e.expires = now.Add(time.Duration(n) * time.Second)
	} else if n, ok := seconds(t.ExpiresOn); ok {
		e.expires = time.Unix(n, 0)
	}
	return e.token, nil
}

// seconds reads a count of seconds sent as a number or a string.
func seconds(v any) (int64, bool) {
	switch x := v.(type) {
	case float64:
		return int64(x), true
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		return n, err == nil
	}
	return 0, false
}

func (e *EntraClient) resource() string {
	if e.Resource == "" {
		return DefaultEntraResource
	}
	return e.Resource
}

// credentialsRequest is the client credentials flow's request.
func (e *EntraClient) credentialsRequest(ctx context.Context, now time.Time) (*http.Request, error) {
	if e.TenantID == "" || e.ClientID == "" {
		return nil, errors.New("entra: a service principal needs a tenant ID and a client ID")
	}
	authority := e.Authority
	if authority == "" {
		authority = DefaultEntraAuthority
	}
	endpoint := strings.TrimSuffix(authority, "/") + "/" + url.PathEscape(e.TenantID) + "/oauth2/v2.0/token"
	form := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {e.ClientID},
		"scope":      {strings.TrimSuffix(e.resource(), "/") + "/.default"},
	}
	switch {
	case e.Certificate != nil:
		a, err := e.assertion(endpoint, now)
		if err != nil {
			return nil, err
		}
		form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
		form.Set("client_assertion", a)
	case e.ClientSecret != "":
		form.Set("client_secret", e.ClientSecret)
	default:
		return nil, errors.New("entra: a service principal needs a client secret or a certificate")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("entra: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

// assertion is the client assertion of a certificate: a JWT signed with
// RS256, its header naming the certificate by its SHA-1 thumbprint (x5t),
// as Entra ID asks.
func (e *EntraClient) assertion(aud string, now time.Time) (string, error) {
	cert := e.Certificate
	if len(cert.Certificate) == 0 {
		return "", errors.New("entra: the client certificate is empty")
	}
	key, ok := cert.PrivateKey.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("entra: the client certificate's key is not RSA")
	}
	thumb := sha1.Sum(cert.Certificate[0])
	jti := make([]byte, 16)
	rand.Read(jti)
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "x5t": enc.EncodeToString(thumb[:])})
	claims, _ := json.Marshal(map[string]any{
		"aud": aud, "iss": e.ClientID, "sub": e.ClientID, "jti": hex.EncodeToString(jti),
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(),
	})
	signed := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("entra: signing the client assertion: %w", err)
	}
	return signed + "." + enc.EncodeToString(sig), nil
}

// identityRequest is the managed identity's request: App Service's
// identity endpoint when the environment names one, IMDS's otherwise.
func (e *EntraClient) identityRequest(ctx context.Context) (*http.Request, error) {
	endpoint, header, version := e.IdentityEndpoint, "", "2018-02-01"
	if endpoint == "" {
		if ep := os.Getenv("IDENTITY_ENDPOINT"); ep != "" && os.Getenv("IDENTITY_HEADER") != "" {
			endpoint, header, version = ep, os.Getenv("IDENTITY_HEADER"), "2019-08-01"
		} else {
			endpoint = DefaultIMDSEndpoint
		}
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("entra: the identity endpoint %q: %w", endpoint, err)
	}
	q := u.Query()
	q.Set("api-version", version)
	q.Set("resource", e.resource())
	if e.ClientID != "" {
		q.Set("client_id", e.ClientID)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("entra: %w", err)
	}
	if header != "" {
		req.Header.Set("X-IDENTITY-HEADER", header)
	} else {
		req.Header.Set("Metadata", "true")
	}
	return req, nil
}

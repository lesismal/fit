package tlsmgr

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/internal/pebbletest"
)

func TestCertID(t *testing.T) {
	// RFC 9773's example: the key identifier and the serial number's DER.
	leaf := &x509.Certificate{
		AuthorityKeyId: []byte{0x69, 0x88, 0x5b, 0x6b, 0x87, 0x46, 0x40, 0x41, 0xe1, 0xb3, 0x7b, 0x84, 0x7b, 0xa0, 0xae, 0x2c, 0xde, 0x01, 0xc8, 0xd4},
		SerialNumber:   new(big.Int).SetBytes([]byte{0x00, 0x87, 0x65, 0x43, 0x21}),
	}
	if got, want := certID(leaf), "aYhba4dGQEHhs3uEe6CuLN4ByNQ.AIdlQyE"; got != want {
		t.Errorf("%s, want %s", got, want)
	}
	leaf.SerialNumber = big.NewInt(0x0102)
	if got := certID(leaf); !strings.HasSuffix(got, ".AQI") {
		t.Errorf("a serial without its top bit set: %s", got)
	}
	if certID(&x509.Certificate{SerialNumber: big.NewInt(1)}) != "" {
		t.Error("an identifier for a certificate without a key identifier")
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              ariCheckDefault,
		"nonsense":                      ariCheckDefault,
		"3600":                          time.Hour,
		"1":                             ariCheckMin,
		"9999999":                       ariCheckMax,
		"Tue, 20 Oct 2026 14:00:00 GMT": 2 * time.Hour,
	} {
		if got := retryAfter(v, now, ariCheckMin, ariCheckMax); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

// ariCA is a CA that serves a directory with renewal information, whose
// window a test sets, and fails every other request, so that a renewal it
// starts shows as an order that fails.
type ariCA struct {
	srv *httptest.Server

	mu           sync.Mutex
	start, end   time.Time
	asked        []string // the certificate identifiers asked about
	newAccounts  atomic.Int32
	retryAfterSe string
}

func newARICA(t *testing.T) (*ariCA, string) {
	t.Helper()
	ca := &ariCA{}
	ca.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dir":
			base := "https://" + r.Host
			_ = json.NewEncoder(w).Encode(map[string]string{
				"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/order",
				"renewalInfo": base + "/ari/",
			})
		case strings.HasPrefix(r.URL.Path, "/ari/"):
			ca.mu.Lock()
			ca.asked = append(ca.asked, strings.TrimPrefix(r.URL.Path, "/ari/"))
			start, end, ra := ca.start, ca.end, ca.retryAfterSe
			ca.mu.Unlock()
			if ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"suggestedWindow": map[string]time.Time{"start": start, "end": end}})
		default:
			if r.URL.Path == "/account" || r.URL.Path == "/nonce" {
				ca.newAccounts.Add(1)
			}
			http.Error(w, "not here", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(ca.srv.Close)
	caFile := t.TempDir() + "/ca.pem"
	writePEM(t, caFile, ca.srv.Certificate())
	return ca, caFile
}

func (ca *ariCA) window(start, end time.Time) {
	ca.mu.Lock()
	ca.start, ca.end = start, end
	ca.mu.Unlock()
}

// storeIssued stores a certificate for name that a test CA issued, with a
// key identifier ARI names it by, as ACME would have stored it.
func storeIssued(t *testing.T, a *ACME, name string) *x509.Certificate {
	t.Helper()
	issuer, err := certtest.NewCA("fit test CA")
	if err != nil {
		t.Fatal(err)
	}
	c, err := issuer.Issue(t.TempDir(), "", name)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := os.ReadFile(c.CertFile)
	keyPEM, _ := os.ReadFile(c.KeyFile)
	if err := a.storage.Store(context.Background(), a.certKey(name), append(certPEM, keyPEM...)); err != nil {
		t.Fatal(err)
	}
	if len(c.Leaf.AuthorityKeyId) == 0 {
		t.Fatal("the test certificate has no authority key identifier")
	}
	return c.Leaf
}

// TestARIWindowBegun has the CA suggest a window that has begun for a
// stored certificate, which its lifetime says is far from due: ACME renews
// it at once, serving it meanwhile.
func TestARIWindowBegun(t *testing.T) {
	ca, caFile := newARICA(t)
	ca.window(time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	var l logLines
	a := testACME(t, &config.ACME{Directory: ca.srv.URL + "/dir", CAFile: caFile}, &l)
	leaf := storeIssued(t, a, "ari.fit.test")
	a.start()
	a.Manage([]string{"ari.fit.test"})
	eventually(t, 5*time.Second, "a renewal started", func() bool { return ca.newAccounts.Load() > 0 })
	if c := a.Certificate("ari.fit.test"); c == nil || c.Leaf.SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Error("the stored certificate is not served while it is renewed")
	}
	ca.mu.Lock()
	asked := append([]string(nil), ca.asked...)
	ca.mu.Unlock()
	if len(asked) == 0 || asked[0] != certID(leaf) {
		t.Errorf("asked about %v, want %s", asked, certID(leaf))
	}
}

// TestARIWindowLater has the CA suggest a window to come, and ask again soon
// after: nothing is renewed before it, and ACME asks again when told to.
func TestARIWindowLater(t *testing.T) {
	ca, caFile := newARICA(t)
	ca.window(time.Now().Add(24*time.Hour), time.Now().Add(48*time.Hour))
	ca.mu.Lock()
	ca.retryAfterSe = "1"
	ca.mu.Unlock()
	var l logLines
	a := testACME(t, &config.ACME{Directory: ca.srv.URL + "/dir", CAFile: caFile}, &l)
	a.ariMin = 100 * time.Millisecond
	storeIssued(t, a, "later.fit.test")
	a.start()
	a.Manage([]string{"later.fit.test"})
	eventually(t, 5*time.Second, "asked again", func() bool {
		ca.mu.Lock()
		defer ca.mu.Unlock()
		return len(ca.asked) >= 3
	})
	if n := ca.newAccounts.Load(); n != 0 {
		t.Errorf("a renewal started before the window: %d requests", n)
	}
	a.mu.Lock()
	s := a.names["later.fit.test"].ari
	a.mu.Unlock()
	if s == nil || s.renewAt.Before(time.Now().Add(23*time.Hour)) || s.renewAt.After(time.Now().Add(48*time.Hour)) {
		t.Errorf("renewal picked at %+v, not in the window", s)
	}
}

// TestPebbleARI obtains a certificate from Pebble, sees the window Pebble
// suggests for it, then has Pebble move the window into the past: the
// certificate is renewed.
func TestPebbleARI(t *testing.T) {
	port := pebbletest.FreePort(t)
	p := pebbletest.Start(t, pebbletest.Options{HTTPPort: port})
	var l logLines
	a := testACME(t, &config.ACME{Directory: p.Directory, CAFile: p.CAFile, Challenges: []string{"http-01"}}, &l)
	a.ariMax = 200 * time.Millisecond // Pebble says six hours
	serveHTTP01(t, a, port)
	a.start()
	a.Manage([]string{"ari.fit.test"})
	t.Cleanup(func() { l.mu.Lock(); t.Log(strings.Join(l.lines, "\n")); l.mu.Unlock() })
	eventually(t, 30*time.Second, "a certificate from pebble", func() bool { return a.Certificate("ari.fit.test") != nil })
	first := a.Certificate("ari.fit.test").Leaf
	eventually(t, 5*time.Second, "renewal information", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.names["ari.fit.test"].ari != nil
	})
	a.mu.Lock()
	s := *a.names["ari.fit.test"].ari
	a.mu.Unlock()
	if s.renewAt.Before(first.NotBefore) || s.renewAt.After(first.NotAfter) {
		t.Errorf("renewal picked at %s, outside the certificate's lifetime %s to %s", s.renewAt, first.NotBefore, first.NotAfter)
	}

	p.SetRenewalInfo(t, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: first.Raw}),
		time.Now().Add(-time.Hour), time.Now().Add(-time.Minute))
	eventually(t, 30*time.Second, "the certificate renewed", func() bool {
		c := a.Certificate("ari.fit.test")
		return c != nil && c.Leaf.SerialNumber.Cmp(first.SerialNumber) != 0
	})
}

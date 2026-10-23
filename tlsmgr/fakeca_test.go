package tlsmgr

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/certtest"
)

// fakeCA is a minimal ACME CA (RFC 8555) for tests that obtain certificates
// without Pebble: it does not check signatures, and it takes a challenge as
// answered when its check, if any, says so. Its knobs make each step fail.
type fakeCA struct {
	t   *testing.T
	srv *httptest.Server
	ca  *certtest.CA
	// caFile is a PEM file of the certificate the CA serves its API with.
	caFile string

	mu sync.Mutex
	// offer is the challenge types offered, in order.
	offer []string
	// valid has every authorization valid from the start.
	valid bool
	// fail names the steps answered with an error: account, order, authz,
	// accept, finalize, cert; and the authorization or order going invalid:
	// authz-invalid, order-invalid.
	fail map[string]bool
	// issue is what the certificate is issued as: "" for the name ordered,
	// "wrong-name" for another, "garbage" for bytes that are no certificate.
	issue string
	// ocsp, if set, is the OCSP responder the certificates name.
	ocsp string
	// check, if set, says whether a challenge accepted was answered.
	check func(typ, token, name string) error

	orders   []*fakeOrder
	accepted []string // the challenge types accepted
	issued   atomic.Int32
	nonce    atomic.Int64
}

type fakeOrder struct {
	name      string
	accepted  bool
	finalized bool
	certPEM   []byte
}

func newFakeCA(t *testing.T) *fakeCA {
	t.Helper()
	ca, err := certtest.NewCA("fit fake ACME CA")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeCA{t: t, ca: ca, offer: []string{"http-01", "tls-alpn-01"}, fail: map[string]bool{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	f.caFile = t.TempDir() + "/ca.pem"
	writePEM(t, f.caFile, f.srv.Certificate())
	return f
}

func (f *fakeCA) directory() string { return f.srv.URL + "/dir" }

func (f *fakeCA) set(fn func(f *fakeCA)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

func (f *fakeCA) acceptedTypes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.accepted...)
}

// problem answers with an ACME error that the client does not retry.
func problem(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:unauthorized", "detail": detail, "status": 403})
}

func (f *fakeCA) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Replay-Nonce", "nonce-"+strconv.FormatInt(f.nonce.Add(1), 10))
	base := "https://" + r.Host
	if r.URL.Path == "/dir" {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/order",
			"revokeCert": base + "/revoke", "keyChange": base + "/key-change",
		})
		return
	}
	if r.URL.Path == "/nonce" {
		return
	}
	// Everything else is a JWS POST.
	var jws struct{ Payload string }
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, &jws); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	payload, _ := base64.RawURLEncoding.DecodeString(jws.Payload)

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	f.mu.Lock()
	defer f.mu.Unlock()
	var o *fakeOrder
	if len(parts) >= 2 {
		i, err := strconv.Atoi(parts[1])
		if err != nil || i < 0 || i >= len(f.orders) {
			http.NotFound(w, r)
			return
		}
		o = f.orders[i]
	}
	id := ""
	if len(parts) >= 2 {
		id = parts[1]
	}
	orderJSON := func(status string) map[string]any {
		v := map[string]any{"status": status, "identifiers": []map[string]string{{"type": "dns", "value": o.name}},
			"authorizations": []string{base + "/authz/" + id}, "finalize": base + "/finalize/" + id}
		if status == "valid" {
			v["certificate"] = base + "/cert/" + id
		}
		if status == "invalid" {
			v["error"] = map[string]any{"type": "urn:ietf:params:acme:error:unauthorized", "detail": "order gone bad", "status": 403}
		}
		return v
	}
	switch parts[0] {
	case "account":
		if f.fail["account"] {
			problem(w, "no account for you")
			return
		}
		w.Header().Set("Location", base+"/acct/1")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "valid"})
	case "order":
		if o == nil {
			if f.fail["order"] {
				problem(w, "no order for you")
				return
			}
			var req struct {
				Identifiers []struct{ Value string }
			}
			_ = json.Unmarshal(payload, &req)
			f.orders = append(f.orders, &fakeOrder{name: req.Identifiers[0].Value})
			id = strconv.Itoa(len(f.orders) - 1)
			o = f.orders[len(f.orders)-1]
			w.Header().Set("Location", base+"/order/"+id)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(orderJSON("pending"))
			return
		}
		switch {
		case f.fail["order-invalid"]:
			_ = json.NewEncoder(w).Encode(orderJSON("invalid"))
		case o.finalized:
			_ = json.NewEncoder(w).Encode(orderJSON("valid"))
		default:
			_ = json.NewEncoder(w).Encode(orderJSON("ready"))
		}
	case "authz":
		if f.fail["authz"] {
			problem(w, "no authorization for you")
			return
		}
		v := map[string]any{"identifier": map[string]string{"type": "dns", "value": o.name}}
		var chals []map[string]string
		for _, typ := range f.offer {
			chals = append(chals, map[string]string{"type": typ, "url": base + "/chal/" + id + "/" + typ, "token": "tok-" + id, "status": "pending"})
		}
		v["challenges"] = chals
		switch {
		case f.valid || o.accepted && !f.fail["authz-invalid"]:
			v["status"] = "valid"
		case o.accepted:
			v["status"] = "invalid"
			v["challenges"] = []map[string]any{{"type": f.offer[0], "url": base + "/chal/" + id + "/" + f.offer[0], "token": "tok-" + id, "status": "invalid",
				"error": map[string]any{"type": "urn:ietf:params:acme:error:unauthorized", "detail": "challenge failed", "status": 403}}}
		default:
			v["status"] = "pending"
		}
		_ = json.NewEncoder(w).Encode(v)
	case "chal":
		typ := parts[2]
		if f.fail["accept"] {
			problem(w, "no challenge for you")
			return
		}
		if f.check != nil {
			if err := f.check(typ, "tok-"+id, o.name); err != nil {
				f.t.Errorf("challenge %s for %s not answered: %v", typ, o.name, err)
			}
		}
		o.accepted = true
		f.accepted = append(f.accepted, typ)
		_ = json.NewEncoder(w).Encode(map[string]string{"type": typ, "url": base + r.URL.Path, "token": "tok-" + id, "status": "processing"})
	case "finalize":
		if f.fail["finalize"] {
			problem(w, "no certificate for you")
			return
		}
		var req struct{ CSR string }
		_ = json.Unmarshal(payload, &req)
		der, _ := base64.RawURLEncoding.DecodeString(req.CSR)
		csr, err := x509.ParseCertificateRequest(der)
		if err != nil {
			problem(w, err.Error())
			return
		}
		o.certPEM = f.sign(csr, o.name)
		o.finalized = true
		w.Header().Set("Location", base+"/order/"+id)
		_ = json.NewEncoder(w).Encode(orderJSON("valid"))
	case "cert":
		if f.fail["cert"] {
			problem(w, "the certificate is gone")
			return
		}
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(o.certPEM)
	default:
		http.NotFound(w, r)
	}
}

// sign issues the certificate of an order for name, with the CA's after it.
// f.mu is held.
func (f *fakeCA) sign(csr *x509.CertificateRequest, name string) []byte {
	if f.issue == "garbage" {
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("no certificate")})
	}
	if f.issue == "wrong-name" {
		name = "wrong.fit.test"
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if f.ocsp != "" {
		tmpl.OCSPServer = []string{f.ocsp}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, f.ca.Cert, csr.PublicKey, f.ca.Key)
	if err != nil {
		panic(fmt.Sprintf("fake CA: %v", err))
	}
	f.issued.Add(1)
	return append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.ca.Cert.Raw})...)
}

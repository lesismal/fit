//go:build e2e

// Package e2e is the docker test of cluster management (I141): a manager
// and three members, each a fit in a container of its own on a network of
// their own, which test/docker/cluster/e2e.sh runs. Members are added
// through the manager's console by their console's password and by their
// Admin API's token; each way a member fails is reported apart; a member's
// restart, a member down and the manager's restart are recovered from; and
// no credential is answered or logged.
//
// It needs Docker, FIT_BIN (fit built for the containers' Linux), and takes
// FIT_IMAGE (the containers' image, debian:trixie) and FIT_PORT (the host
// port the manager's console is published on, 29441).
package e2e

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// The credentials the members are given; none may appear in an answer or a
// log.
const (
	memberPassword = "m1-console-pa55word"
	m2Password     = "m2-console-pa55word"
	memberToken    = "m3-admin-api-token-0123456789abcdef"
	wrongPassword  = "not-the-m1-password"
	managerPass    = "manager-pw"
	storePassword  = "manager-store-pw"
)

type cluster struct {
	t       *testing.T
	dir     string
	net     string
	image   string
	port    string
	prefix  string
	base    string
	c       *http.Client
	csrf    string
	answers []string
	// names are the containers, for their logs: the manager and m1 to m3
	// when empty.
	names []string
}

func (k *cluster) docker(args ...string) string {
	k.t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		k.t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (k *cluster) write(name string, data []byte) {
	k.t.Helper()
	if err := os.WriteFile(filepath.Join(k.dir, name), data, 0o644); err != nil {
		k.t.Fatal(err)
	}
}

// run starts the container name with fit and the config name.json.
func (k *cluster) run(name string, publish ...string) {
	args := []string{"run", "-d", "--name", k.prefix + name, "--network", k.net, "--network-alias", name, "-v", k.dir + ":/w"}
	for _, p := range publish {
		args = append(args, "-p", p)
	}
	args = append(args, k.image, "/w/fit", "run", "-c", "/w/"+name+".json", "-watch-certs=false")
	k.docker(args...)
}

func (k *cluster) logs() string {
	var b strings.Builder
	names := k.names
	if len(names) == 0 {
		names = []string{"manager", "m1", "m2", "m3"}
	}
	for _, n := range names {
		out, _ := exec.Command("docker", "logs", k.prefix+n).CombinedOutput()
		fmt.Fprintf(&b, "== %s\n%s\n", n, out)
	}
	return b.String()
}

// do calls the manager's console.
func (k *cluster) do(method, path, body string) (int, string) {
	k.t.Helper()
	req, _ := http.NewRequest(method, k.base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if k.csrf != "" {
		req.Header.Set("X-Fit-Csrf", k.csrf)
	}
	resp, err := k.c.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	k.answers = append(k.answers, string(data))
	return resp.StatusCode, string(data)
}

func (k *cluster) login() {
	k.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		k.csrf = ""
		code, body := k.do("POST", "/api/login", `{"username":"admin","password":"`+managerPass+`"}`)
		if code == 200 {
			var s struct{ CSRF string }
			_ = json.Unmarshal([]byte(body), &s)
			k.csrf = s.CSRF
			return
		}
		if time.Now().After(deadline) {
			k.t.Fatalf("the manager's login: %d %s\n%s", code, body, k.logs())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (k *cluster) mustDo(method, path, body string, want int) string {
	k.t.Helper()
	code, got := k.do(method, path, body)
	if code != want {
		k.t.Fatalf("%s %s: %d %s", method, path, code, got)
	}
	return got
}

type nodeState struct {
	Name, Status string
	Error        *struct {
		Kind, Message string
		Certificate   *struct{ Fingerprint string }
	}
	Node *struct {
		ID      string
		Started time.Time
	}
	Config *struct{ Version uint64 }
	Drift  *struct{ Count int }
	TLS    *struct{ Fingerprint, Verified string }
}

func (k *cluster) states() map[string]nodeState {
	k.t.Helper()
	code, body := k.do("GET", "/api/m/cluster/state", "")
	out := map[string]nodeState{}
	if code != 200 {
		return out
	}
	var s struct{ Nodes []nodeState }
	_ = json.Unmarshal([]byte(body), &s)
	for _, n := range s.Nodes {
		out[n.Name] = n
	}
	return out
}

// wait waits for the member name's state to satisfy cond.
func (k *cluster) wait(name, what string, cond func(nodeState) bool) nodeState {
	k.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		st, ok := k.states()[name]
		if ok && cond(st) {
			k.t.Logf("%s: %s", name, what)
			return st
		}
		if time.Now().After(deadline) {
			k.t.Fatalf("%s is not %s: %+v %+v\n%s", name, what, st, st.Error, k.logs())
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func up(s nodeState) bool { return s.Status == "up" && s.Node != nil }

func kind(want string) func(nodeState) bool {
	return func(s nodeState) bool { return s.Error != nil && s.Error.Kind == want }
}

func TestCluster(t *testing.T) {
	bin := os.Getenv("FIT_BIN")
	if bin == "" {
		t.Skip("FIT_BIN is not set: run test/docker/cluster/e2e.sh")
	}
	k := &cluster{t: t, dir: t.TempDir(), image: env("FIT_IMAGE", "debian:trixie"), port: env("FIT_PORT", "29441"),
		prefix: "fit-cluster-e2e-"}
	k.net = k.prefix + "net"
	_ = os.Chmod(k.dir, 0o755)
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(k.dir, "fit"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		for _, n := range []string{"manager", "m1", "m2", "m3"} {
			_ = exec.Command("docker", "rm", "-f", k.prefix+n).Run()
		}
		_ = exec.Command("docker", "network", "rm", k.net).Run()
	}
	cleanup()
	t.Cleanup(func() {
		if t.Failed() {
			t.Log(k.logs())
		}
		cleanup()
	})

	// A CA that signs m1's console and m3's Admin API; m2's console and
	// the manager's are self-signed.
	caCert, caKey := newCA(t)
	k.write("ca.pem", pemCert(caCert.Raw))
	for _, name := range []string{"m1", "m3"} {
		cert, key := leaf(t, name, caCert, caKey)
		k.write(name+".crt", cert)
		k.write(name+".key", key)
	}
	for _, name := range []string{"m2", "manager"} {
		cert, key := leaf(t, name, nil, nil)
		k.write(name+".crt", cert)
		k.write(name+".key", key)
	}
	hash := func(pw string) string {
		h, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.MinCost)
		return string(h)
	}
	k.write("token", []byte(memberToken+"\n"))
	const rest = `"listeners": [{"name": "web", "address": "0.0.0.0:8080"}], "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`
	console := func(name, pw string) string {
		return `"console": {"address": "0.0.0.0:2020", "cert_file": "/w/` + name + `.crt", "key_file": "/w/` + name + `.key", "users": [{"username": "ops", "password_hash": "` + hash(pw) + `"}]}`
	}
	k.write("m1.json", []byte(`{"admin": {`+console("m1", memberPassword)+`}, `+rest))
	k.write("m2.json", []byte(`{"admin": {`+console("m2", m2Password)+`}, `+rest))
	k.write("m3.json", []byte(`{"admin": {"address": "0.0.0.0:2019", "token_file": "/w/token", "cert_file": "/w/m3.crt", "key_file": "/w/m3.key"}, `+rest))
	k.write("manager.json", []byte(`{"admin": {"store": {"path": "/w/manager.store", "password": "`+storePassword+`"},
"console": {"address": "0.0.0.0:2020", "cert_file": "/w/manager.crt", "key_file": "/w/manager.key", "users": [{"username": "admin", "password_hash": "`+hash(managerPass)+`"}]},
"cluster": {"poll_interval": "2s", "timeout": "5s", "max_concurrency": 2, "max_response_bytes": 262144}}, `+rest))

	k.docker("network", "create", k.net)
	for _, n := range []string{"m1", "m2", "m3"} {
		k.run(n)
	}
	k.run("manager", "127.0.0.1:"+k.port+":2020")
	jar, _ := cookiejar.New(nil)
	// The test's own client of the manager's console, whose certificate it
	// made: not one of the manager's calls.
	k.c = &http.Client{Jar: jar, Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec
	k.base = "https://127.0.0.1:" + k.port
	k.login()

	ca := string(pemCert(caCert.Raw))
	add := func(v map[string]any) {
		t.Helper()
		b, _ := json.Marshal(v)
		k.mustDo("POST", "/api/m/cluster/nodes", string(b), 201)
	}
	// By password, through the console, trusted by its CA; by token,
	// through the Admin API; m2 by password, its self-signed certificate
	// not trusted yet; and the failures.
	add(map[string]any{"name": "m1", "url": "https://m1:2020", "access": "console", "username": "ops", "password": memberPassword, "tls": map[string]string{"ca": ca}, "labels": map[string]string{"role": "edge"}})
	add(map[string]any{"name": "m3", "url": "https://m3:2019", "access": "admin_api", "token": memberToken, "tls": map[string]string{"ca": ca}})
	add(map[string]any{"name": "m2", "url": "https://m2:2020", "access": "console", "username": "ops", "password": m2Password})
	add(map[string]any{"name": "m1-wrong-password", "url": "https://m1:2020", "access": "console", "username": "ops", "password": wrongPassword, "tls": map[string]string{"ca": ca}})
	m1 := k.wait("m1", "up by password", up)
	m3 := k.wait("m3", "up by token", up)
	if m1.Node.ID == "" || m1.TLS == nil || m1.TLS.Verified != "ca" || m3.TLS.Verified != "ca" || m1.Drift == nil {
		t.Errorf("m1 %+v %+v, m3 %+v", m1, m1.TLS, m3.TLS)
	}
	k.wait("m1-wrong-password", "refused its password", kind("auth_failed"))
	st := k.wait("m2", "not trusted", kind("tls_untrusted"))
	if st.Error.Certificate == nil || st.Error.Certificate.Fingerprint == "" {
		t.Fatalf("m2 shows no certificate: %+v", st.Error)
	}

	// The test shows m2's fingerprint for a person to confirm; pinned, m2
	// is up. A pin that is not m2's is reported as such.
	var res struct {
		OK          bool
		Certificate struct{ Fingerprint string }
		Error       *struct{ Kind string }
	}
	_ = json.Unmarshal([]byte(k.mustDo("POST", "/api/m/cluster/nodes/m2/test", "", 200)), &res)
	if res.OK || res.Error == nil || res.Error.Kind != "tls_untrusted" || res.Certificate.Fingerprint != st.Error.Certificate.Fingerprint {
		t.Fatalf("m2's test: %+v", res)
	}
	pin := res.Certificate.Fingerprint
	k.mustDo("PUT", "/api/m/cluster/nodes/m2", `{"url":"https://m2:2020","access":"console","username":"ops","tls":{"fingerprint":"`+pin+`"}}`, 200)
	if st := k.wait("m2", "up, pinned", up); st.TLS == nil || st.TLS.Verified != "pin" {
		t.Errorf("m2: %+v", st.TLS)
	}
	add(map[string]any{"name": "m2-wrong-pin", "url": "https://m2:2020", "access": "console", "username": "ops", "password": m2Password,
		"tls": map[string]string{"fingerprint": m1.TLS.Fingerprint}})
	k.wait("m2-wrong-pin", "refused for its pin", kind("tls_pin_mismatch"))

	// A member down, and back.
	k.docker("stop", k.prefix+"m3")
	st = k.wait("m3", "down", func(s nodeState) bool { return s.Status == "down" })
	t.Logf("m3 down: %+v", st.Error)
	k.docker("start", k.prefix+"m3")
	k.wait("m3", "back up", up)

	// A member restarted: its sessions are gone, and the manager logs in
	// again.
	k.docker("restart", k.prefix+"m1")
	k.wait("m1", "up after its restart", func(s nodeState) bool { return up(s) && s.Node.Started.After(m1.Node.Started) })

	// The manager restarted: the members are read from its store, and up.
	k.docker("restart", k.prefix+"manager")
	k.login()
	for _, n := range []string{"m1", "m2", "m3"} {
		k.wait(n, "up after the manager's restart", up)
	}
	k.wait("m1-wrong-password", "still refused", kind("auth_failed"))
	k.wait("m2-wrong-pin", "still refused", kind("tls_pin_mismatch"))

	// A member's page reads the member through the manager (I142): by
	// password and by token; a member refused answers its problem.
	for _, n := range []string{"m1", "m2", "m3"} {
		for _, what := range []string{"node", "status", "versions", "check"} {
			k.mustDo("GET", "/api/m/cluster/nodes/"+n+"/read/"+what, "", 200)
		}
	}
	if got := k.mustDo("GET", "/api/m/cluster/nodes/m2-wrong-pin/read/status", "", 502); !strings.Contains(got, `"kind":"tls_pin_mismatch"`) {
		t.Errorf("m2-wrong-pin's read: %s", got)
	}

	// The list masks the credentials; no answer and no log holds one, nor a
	// member's session cookie.
	list := k.mustDo("GET", "/api/m/cluster/nodes", "", 200)
	if !strings.Contains(list, `"password_set":true`) || !strings.Contains(list, `"token_set":true`) {
		t.Errorf("%s", list)
	}
	logs := k.logs()
	for _, secret := range []string{memberPassword, m2Password, memberToken, wrongPassword, "fit_console_"} {
		for _, a := range k.answers {
			if strings.Contains(a, secret) {
				t.Errorf("an answer holds %q: %s", secret, a)
			}
		}
		if strings.Contains(logs, secret) {
			t.Errorf("a log holds %q", secret)
		}
	}
	t.Logf("the manager's log:\n%s", logs[:strings.Index(logs, "== m1")])
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func pemCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func newCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fit cluster e2e CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return c, key
}

// leaf makes a certificate for host, signed by ca, or self-signed when ca
// is nil, and returns it and its key as PEM.
func leaf(t *testing.T, host string, ca *x509.Certificate, caKey *ecdsa.PrivateKey) ([]byte, []byte) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	parent, signer := tmpl, any(key)
	if ca != nil {
		parent, signer = ca, caKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	return pemCert(der), bytes.TrimSpace(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
}

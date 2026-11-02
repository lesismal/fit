package admin

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
)

// TestBootstrapNotObject checks the bootstrap file's helpers with a config
// that is not a JSON object, or not JSON at all.
func TestBootstrapNotObject(t *testing.T) {
	for _, data := range []string{`{`, `[1]`} {
		if _, _, err := SplitAdmin([]byte(data)); err == nil {
			t.Errorf("SplitAdmin(%s): no error", data)
		}
		if hasAdmin([]byte(data)) {
			t.Errorf("hasAdmin(%s)", data)
		}
	}
	if got := WithAdmin([]byte(`[1]`), []byte(`{}`)); string(got) != `[1]` {
		t.Errorf("WithAdmin of an array: %s", got)
	}
	if got := WithAdmin([]byte("{ \n}"), []byte(`{"a": 1}`)); string(got) != "{ \n\n\"admin\": {\"a\": 1}\n}" {
		t.Errorf("WithAdmin of an empty object: %q", got)
	}
}

// TestEditMemberErrors checks a member edited through a member that is not
// an object, and an edit that gives no JSON: neither is applied.
func TestEditMemberErrors(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	_, err := f.configs.EditMember("test", []string{"listeners", "x"}, func(cur []byte) ([]byte, error) { return cur, nil })
	if err == nil || !strings.Contains(err.Error(), "is not an object") {
		t.Errorf("through an array: %v", err)
	}
	_, err = f.configs.EditMember("test", []string{"tunnel"}, func([]byte) ([]byte, error) { return []byte("{"), nil })
	if err == nil {
		t.Error("an edit that is not JSON applied")
	}
}

// TestSaveFails checks a config that runs but cannot be saved to the store
// says so, and is saved by the next apply that can.
func TestSaveFails(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	fail := true
	var saved []byte
	f.configs.UseStore(nil, func(data []byte) error {
		if fail {
			return errors.New("the disk is full")
		}
		saved = data
		return nil
	})
	r, err := f.configs.Apply([]byte(doc("two")), "test")
	if err != nil || len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, "\n"), "the disk is full") {
		t.Fatalf("%v %v", r.Warnings, err)
	}
	fail = false
	if _, err := f.configs.Apply([]byte(doc("two")), "test"); err != nil || string(saved) != doc("two") {
		t.Errorf("saved %q: %v", saved, err)
	}
}

// TestConsoleTLS checks a console with a certificate serves over TLS, with
// HSTS, and takes requests from its own https origin.
func TestConsoleTLS(t *testing.T) {
	cert, err := certtest.Write(t.TempDir(), "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	c := &config.Console{Address: "127.0.0.1:0", CertFile: cert.CertFile, KeyFile: cert.KeyFile,
		Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}
	f := start(t, doc("one"), &config.Admin{})
	cs, err := StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Shutdown(context.Background())
	if !strings.HasPrefix(cs.URL(), "https://") {
		t.Fatalf("URL %s", cs.URL())
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(cert)}}}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest("POST", cs.URL()+"api/login", strings.NewReader(`{"username": "ada", "password": "pw"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://"+cs.Addr().String())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Errorf("%d %v", resp.StatusCode, resp.Header)
	}
	var secure bool
	for _, ck := range resp.Cookies() {
		secure = secure || strings.HasPrefix(ck.Name, sessionCookie+"_") && ck.Secure
	}
	if !secure {
		t.Error("the session cookie is not Secure")
	}
}

// TestConsoleStartFails checks a console whose certificate does not load,
// or whose address is taken, does not start.
func TestConsoleStartFails(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	dir := t.TempDir()
	c := &config.Console{Address: "127.0.0.1:0", CertFile: dir + "/none.crt", KeyFile: dir + "/none.key"}
	if _, err := StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, nil); err == nil {
		t.Error("started without its certificate")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c = &config.Console{Address: ln.Addr().String()}
	if _, err := StartConsole(testEngine(t), c, &config.Admin{}, f.configs, nil, nil, nil); err == nil {
		t.Error("started on a taken address")
	}
}

// TestConsoleRefuses checks the pages refuse what only reads them does not
// do, an endpoint that is not there, and a login whose body is not JSON.
func TestConsoleRefuses(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)
	if code, _, h := b.do("POST", "/index.html", "x"); code != http.StatusMethodNotAllowed || h.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST to a page: %d %v", code, h)
	}
	if code, _, _ := b.do("GET", "/api/nope", ""); code != http.StatusUnauthorized {
		t.Errorf("an endpoint before a login: %d", code)
	}
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	if code, _, _ := b.do("GET", "/api/nope", ""); code != http.StatusNotFound {
		t.Errorf("an endpoint that is not there: %d", code)
	}
	if code, _, _ := b.do("POST", "/api/login", "{"); code != http.StatusBadRequest {
		t.Errorf("a login that is not JSON: %d", code)
	}
	if code, _, _ := b.do("GET", "/api/m/nope/x", ""); code != http.StatusNotFound {
		t.Errorf("a module that is not there: %d", code)
	}
}

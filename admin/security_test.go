package admin

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/login"
)

// TestConsoleRebindingTakeover is the takeover the console's Host check
// stops: fit run -admin makes a console password temporary, and a web page
// whose name rebinds to 127.0.0.1 posts the login, then the password change,
// each with an Origin matching its Host. Both are refused.
func TestConsoleRebindingTakeover(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	until := f.now.Add(10 * time.Minute)
	f.configs.UseTemporaryPassword("ada", until)
	f.console.logins.SetTemporary("ada", until)
	_, port, _ := net.SplitHostPort(f.console.Addr().String())
	evil := "rebind.attacker.example:" + port
	cl := &http.Client{Timeout: 10 * time.Second}
	for _, tc := range []struct{ path, body string }{
		{"/api/login", `{"username": "ada", "password": "pw"}`},
		{accountPath, `{"current_password": "pw", "password": "taken"}`},
		{"/api/m/admin/logs/cleanup", `{}`},
	} {
		req, _ := http.NewRequest("POST", f.url+tc.path, strings.NewReader(tc.body))
		req.Host = evil
		req.Header.Set("Origin", "http://"+evil)
		req.Header.Set("Content-Type", "application/json")
		resp, err := cl.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "Host") {
			t.Errorf("POST %s from a rebound name: %d %s", tc.path, resp.StatusCode, body)
		}
	}
	if user, _ := f.configs.TemporaryPassword(); user != "ada" {
		t.Errorf("the temporary password changed: %q", user)
	}
}

// TestConsolePasswordChecksAtOnce: wrong current passwords sent at once
// check no more than the address may fail before it is locked out.
func TestConsolePasswordChecksAtOnce(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			code, _, _ := b.do("POST", accountPath, `{"current_password": "wrong", "password": "new"}`)
			mu.Lock()
			codes[code]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if codes[http.StatusForbidden] > login.MaxFailures || codes[http.StatusForbidden]+codes[http.StatusTooManyRequests] != 20 {
		t.Errorf("answers %v: more than %d checked", codes, login.MaxFailures)
	}
}

// TestAdminTLS serves the Admin API over TLS, with a token, as an address
// off loopback must be; plain HTTP gets no config.
func TestAdminTLS(t *testing.T) {
	dir := t.TempDir()
	cert, err := certtest.Write(dir, "localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(token, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, doc("v1"), &config.Admin{})
	s, err := Start(&config.Admin{Address: "127.0.0.1:0", TokenFile: token, CertFile: cert.CertFile, KeyFile: cert.KeyFile}, f.configs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Shutdown(t.Context())
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: certtest.Pool(cert)}}
	defer tr.CloseIdleConnections()
	req, _ := http.NewRequest("GET", "https://"+s.Addr().String()+"/config", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	resp, err := (&http.Client{Timeout: 10 * time.Second, Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.TLS == nil {
		t.Errorf("over TLS: %d %v", resp.StatusCode, resp.TLS)
	}
	for _, path := range []string{"/config", "/logs"} {
		req, _ = http.NewRequest("GET", "http://"+s.Addr().String()+path, nil)
		req.Header.Set("Authorization", "Bearer s3cret")
		if resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Errorf("plain HTTP answered %s", path)
			}
		}
	}

	if _, err := Start(&config.Admin{Address: "127.0.0.1:0", CertFile: cert.CertFile, KeyFile: filepath.Join(dir, "missing")}, f.configs, nil); err == nil || !strings.Contains(err.Error(), "admin:") {
		t.Errorf("a missing key: %v", err)
	}
}

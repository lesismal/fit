package admin

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// withAccess is doc("one") with members added at its top level, as
// `"access": {...}`.
func withAccess(members string) string {
	d := doc("one")
	if members == "" {
		return d
	}
	return strings.TrimSuffix(d, "}") + ", " + members + "}"
}

// reaches reports whether an HTTP GET of url, on a connection of its own,
// is answered; a connection closed as it is accepted is not.
func reaches(t *testing.T, url string) bool {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return true
}

func (f *fixture) apply(t *testing.T, data string) {
	t.Helper()
	if _, err := f.configs.Apply([]byte(data), SourceFile); err != nil {
		t.Fatal(err)
	}
}

func TestAdminAccess(t *testing.T) {
	// No access anywhere: served, as before.
	f := start(t, withAccess(""), &config.Admin{})
	if !reaches(t, f.api+"/versions") {
		t.Fatal("the Admin API is not reached without access")
	}

	// admin.access denies the client: its connections are closed, and the
	// API keeps accepting.
	f.apply(t, withAccess(`"admin": {"access": {"deny": ["127.0.0.1"]}}`))
	for range 3 {
		if reaches(t, f.api+"/versions") {
			t.Fatal("a denied client reached the Admin API")
		}
	}
	// An allow list that holds another address refuses it too.
	f.apply(t, withAccess(`"admin": {"access": {"allow": ["10.0.0.0/8"]}}`))
	if reaches(t, f.api+"/versions") {
		t.Fatal("a client outside admin.access's allow reached the Admin API")
	}

	// The global access applies to the Admin API, and follows the config
	// applied, without a restart.
	f.apply(t, withAccess(`"access": {"deny": ["127.0.0.0/8"]}`))
	if reaches(t, f.api+"/versions") {
		t.Fatal("a client the global access denies reached the Admin API")
	}
	// admin.access's allow decides first: local access is kept whatever
	// the global access says.
	f.apply(t, withAccess(`"access": {"allow": ["10.0.0.0/8"]}, "admin": {"access": {"allow": ["127.0.0.1", "::1"]}}`))
	if !reaches(t, f.api+"/versions") {
		t.Fatal("admin.access's allow did not keep local access")
	}
	f.apply(t, withAccess(""))
	if !reaches(t, f.api+"/versions") {
		t.Fatal("the Admin API is not reached once access is gone")
	}
}

func TestAdminAccessFile(t *testing.T) {
	list := filepath.Join(t.TempDir(), "deny.txt")
	if err := os.WriteFile(list, []byte("# none of ours\n192.0.2.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := start(t, withAccess(`"admin": {"access": {"deny_files": [`+jsonString(list)+`]}}`), &config.Admin{})
	if !reaches(t, f.api+"/versions") {
		t.Fatal("a client the deny file does not hold was refused")
	}
	// The file is watched, as certificate files are.
	g := f.configs.rt.Current()
	_, watched := g.CertFiles[list]
	g.Release()
	if !watched {
		t.Fatalf("%s is not among the files watched", list)
	}
	// Applied again, as a change to a watched file has it, the new list
	// takes effect.
	if err := os.WriteFile(list, []byte("127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.configs.Reapply(SourceCertificates); err != nil {
		t.Fatal(err)
	}
	if reaches(t, f.api+"/versions") {
		t.Fatal("a client the changed deny file holds reached the Admin API")
	}
	// A file that cannot be read refuses the config.
	if _, err := f.configs.Apply([]byte(withAccess(`"admin": {"access": {"allow_files": ["/nonexistent/allow.txt"]}}`)), SourceFile); err == nil {
		t.Fatal("a config whose admin.access file is missing was applied")
	}
}

func TestAdminAccessUnixSocket(t *testing.T) {
	_, err := config.Load([]byte(withAccess(`"admin": {"address": "unix//tmp/fit-admin.sock", "access": {"allow": ["127.0.0.1"]}}`)))
	if err == nil || !strings.Contains(err.Error(), "Unix socket") {
		t.Fatalf("admin.access on a Unix socket: %v", err)
	}
	// The global access does not shut out a Unix socket's peers, which
	// have no address.
	dir, err := os.MkdirTemp("", "fit")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "a.sock")
	start(t, withAccess(`"access": {"allow": ["10.0.0.0/8"]}`), &config.Admin{Address: config.AdminUnixPrefix + sock})
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}}}
	resp, err := c.Get("http://fit/versions")
	if err != nil {
		t.Fatalf("a Unix socket's peer was refused: %v", err)
	}
	resp.Body.Close()
}

func TestConsoleAccess(t *testing.T) {
	f := startConsole(t, &config.Console{})
	if !reaches(t, f.url+"/") {
		t.Fatal("the console is not reached without access")
	}
	// The global access applies to the console, and follows the config
	// applied, without a restart.
	f.apply(t, withAccess(`"access": {"deny": ["127.0.0.1"]}`))
	for range 3 {
		if reaches(t, f.url+"/") {
			t.Fatal("a client the global access denies reached the console")
		}
	}
	f.apply(t, withAccess(""))
	if !reaches(t, f.url+"/") {
		t.Fatal("the console is not reached once access is gone")
	}

	// admin.console.access decides first, both ways.
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	console := func(access string) string {
		return `"admin": {"console": {"users": [{"username": "ada", "password_hash": ` + jsonString(string(hash)) + `}], "access": ` + access + `}}`
	}
	f.apply(t, withAccess(console(`{"deny": ["127.0.0.1"]}`)))
	if reaches(t, f.url+"/") {
		t.Fatal("a client admin.console.access denies reached the console")
	}
	f.apply(t, withAccess(`"access": {"deny": ["127.0.0.1"]}, `+console(`{"allow": ["127.0.0.1"]}`)))
	if !reaches(t, f.url+"/") {
		t.Fatal("admin.console.access's allow did not keep local access")
	}
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

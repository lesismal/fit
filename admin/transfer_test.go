package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
)

// TestTemporaryPassword: a session with the temporary password may only
// change it, the Admin API refuses everything meanwhile, and the password
// stops working at its time.
func TestTemporaryPassword(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	until := f.now.Add(10 * time.Minute)
	f.configs.UseTemporaryPassword("ada", until)
	f.console.logins.SetTemporary("ada", until)

	if resp, body := f.get(t, "/config"); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "temporary password") {
		t.Errorf("the Admin API before the change: %d %s", resp.StatusCode, body)
	}
	b := f.browser(t)
	code, body, _ := b.do("POST", "/api/login", `{"username": "ada", "password": "pw"}`)
	if code != http.StatusOK || !strings.Contains(body, `"must_change_password":true`) || !strings.Contains(body, `"password_expires"`) {
		t.Fatalf("login: %d %s", code, body)
	}
	var s struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &s)
	b.csrf = s.CSRF
	for _, path := range []string{"/api/m/admin/config", "/api/m/admin/schema"} {
		if code, body, _ := b.do("GET", path, ""); code != http.StatusForbidden || !strings.Contains(body, `"must_change_password":true`) {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
	for _, path := range []string{"/api/session", "/api/workbench", usersPath} {
		if code, body, _ := b.do("GET", path, ""); code != http.StatusOK {
			t.Errorf("GET %s: %d %s", path, code, body)
		}
	}
	if code, body, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "pw"}`); code != http.StatusBadRequest || !strings.Contains(body, "must not be the temporary one") {
		t.Errorf("the same password: %d %s", code, body)
	}
	// Another browser, too late: the login and the change are refused.
	f.advance(11 * time.Minute)
	late := f.browser(t)
	if code, body, _ := late.do("POST", "/api/login", `{"username": "ada", "password": "pw"}`); code != http.StatusUnauthorized || !strings.Contains(body, `"password_expired":true`) {
		t.Errorf("a late login: %d %s", code, body)
	}
	if code, body, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "new-pw"}`); code != http.StatusForbidden || !strings.Contains(body, "expired") {
		t.Errorf("a late change: %d %s", code, body)
	}
	f.advance(-11 * time.Minute)
	if code, body, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "new-pw"}`); code != http.StatusOK {
		t.Fatalf("the change: %d %s", code, body)
	}
	// The same session goes on, with everything open; so does the API.
	if code, body, _ := b.do("GET", "/api/session", ""); code != http.StatusOK || strings.Contains(body, "must_change") {
		t.Errorf("the session after the change: %d %s", code, body)
	}
	if code, body, _ := b.do("GET", "/api/m/admin/config", ""); code != http.StatusOK {
		t.Errorf("the config after the change: %d %s", code, body)
	}
	if user, _ := f.configs.TemporaryPassword(); user != "" {
		t.Errorf("still temporary: %q", user)
	}
	if resp, body := f.get(t, "/config"); resp.StatusCode != http.StatusOK {
		t.Errorf("the Admin API after the change: %d %s", resp.StatusCode, body)
	}
}

// TestExportImport exports the config and a page's members in every format,
// and imports them back.
func TestExportImport(t *testing.T) {
	f, st, _ := startStoreConsole(t)
	f.configs.UseSealer(st)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatal(code)
	}
	const exportPath, importPath = "/api/m/admin/config/export", "/api/m/admin/config/import"
	prev := "one"
	for _, format := range []string{"json", "yaml", "conf"} {
		code, body, h := b.do("POST", exportPath, `{"format": "`+format+`"}`)
		if code != http.StatusOK || !strings.Contains(body, prev) || !strings.Contains(h.Get("Content-Disposition"), "fit-config-v") {
			t.Fatalf("export %s: %d %s %v", format, code, body, h)
		}
		changed := strings.Replace(body, prev, format, 1)
		prev = format
		code, res, _ := b.do("POST", importPath+"?format="+format, changed, "Content-Type", "text/plain")
		if code != http.StatusOK {
			t.Fatalf("import %s: %d %s\n%s", format, code, res, changed)
		}
		if got := f.served(t); got != format {
			t.Errorf("served after the %s import: %q", format, got)
		}
	}
	// A page's members: only routes, from a file that sets more.
	code, body, h := b.do("POST", exportPath, `{"format": "json", "members": ["routes"]}`)
	if code != http.StatusOK || strings.Contains(body, "listeners") || !strings.Contains(h.Get("Content-Disposition"), "fit-routes-v") {
		t.Fatalf("export routes: %d %s %v", code, body, h)
	}
	whole := strings.Replace(doc("routes"), `"127.0.0.1:0"`, `"bad address"`, 1)
	if code, res, _ := b.do("POST", importPath+"?format=json&members=routes", whole, "Content-Type", "text/plain"); code != http.StatusOK {
		t.Fatalf("import routes: %d %s", code, res)
	}
	if got := f.served(t); got != "routes" {
		t.Errorf("served after the routes import: %q", got)
	}
	if code, _, _ := b.do("POST", exportPath, `{"format": "json", "members": ["admin"]}`); code != http.StatusBadRequest {
		t.Errorf("export admin: %d", code)
	}
	if code, _, _ := b.do("POST", exportPath, `{"format": "toml"}`); code != http.StatusBadRequest {
		t.Errorf("export toml: %d", code)
	}

	// fitstore: with the running store's key, and with a password.
	for _, pw := range []string{"", "export-pw"} {
		code, body, h := b.do("POST", exportPath, `{"format": "store", "password": "`+pw+`"}`)
		if code != http.StatusOK || !strings.HasPrefix(body, "FITSTORE") || h.Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("export store %q: %d %.20q %v", pw, code, body, h)
		}
		key := pw
		if key == "" {
			key = "secret"
		}
		data, _, err := store.Unseal([]byte(body), key)
		if err != nil || !strings.Contains(string(data), "routes") {
			t.Fatalf("the export opens: %v %s", err, data)
		}
		sealed, err := store.Seal(key, []byte(doc("sealed-"+pw)), nil)
		if err != nil {
			t.Fatal(err)
		}
		hdr := []string{"Content-Type", "application/octet-stream"}
		if pw != "" {
			hdr = append(hdr, StorePasswordHeader, pw)
		}
		if code, res, _ := b.do("POST", importPath+"?format=store", string(sealed), hdr...); code != http.StatusOK {
			t.Fatalf("import store %q: %d %s", pw, code, res)
		}
		if got := f.served(t); got != "sealed-"+pw {
			t.Errorf("served after the store import: %q", got)
		}
	}
	sealed, _ := store.Seal("other", []byte(doc("x")), nil)
	if code, res, _ := b.do("POST", importPath+"?format=store", string(sealed), "Content-Type", "application/octet-stream"); code != http.StatusBadRequest || !strings.Contains(res, "does not open") {
		t.Errorf("a store with another password: %d %s", code, res)
	}
	// A config that does not load is not applied.
	if code, res, _ := b.do("POST", importPath+"?format=json", `{"listeners": []}`, "Content-Type", "text/plain"); code != http.StatusBadRequest || !strings.Contains(res, "listener") {
		t.Errorf("a bad import: %d %s", code, res)
	}
	// Every import was saved to the store.
	saved, err := st.Read()
	if err != nil || !strings.Contains(string(saved), "sealed-export-pw") {
		t.Errorf("the store: %v %s", err, saved)
	}
}

func TestMergeConfig(t *testing.T) {
	got, err := MergeConfig([]byte(`{"a": {"x": 1, "y": [1, 2]}, "b": 1, "c": "file"}`), []byte(`{"a": {"y": [3], "z": true}, "c": "store", "d": null}`))
	if err != nil {
		t.Fatal(err)
	}
	var v, want any
	_ = json.Unmarshal(got, &v)
	_ = json.Unmarshal([]byte(`{"a": {"x": 1, "y": [3], "z": true}, "b": 1, "c": "store", "d": null}`), &want)
	if g, _ := json.Marshal(v); string(g) != mustJSON(want) {
		t.Errorf("merged: %s", got)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func TestWithDefaultConsole(t *testing.T) {
	out, temp, err := WithDefaultConsole([]byte(`{"listeners": []}`), "HASH")
	if err != nil || temp != DefaultConsoleUser {
		t.Fatalf("%v %q", err, temp)
	}
	var c struct {
		Admin config.Admin `json:"admin"`
	}
	if err := json.Unmarshal(out, &c); err != nil || c.Admin.Console == nil || len(c.Admin.Console.Users) != 1 || c.Admin.Console.Users[0].PasswordHash != "HASH" {
		t.Fatalf("%v %s", err, out)
	}
	// A config with users of its own keeps them, and no password is
	// temporary.
	in := `{"admin": {"address": "127.0.0.1:0", "console": {"users": [{"username": "ada", "password_hash": "H"}]}}}`
	out, temp, err = WithDefaultConsole([]byte(in), "HASH")
	if err != nil || temp != "" || !strings.Contains(string(out), `"ada"`) || strings.Contains(string(out), "HASH") {
		t.Errorf("%v %q %s", err, temp, out)
	}
	if _, _, err := WithDefaultConsole([]byte(`{"admin": 1}`), "HASH"); err == nil {
		t.Error("admin not an object")
	}
}

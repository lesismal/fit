package admin

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
)

// TestConsolePasswordBodies: a password change whose body is not the JSON
// it takes, or names no user, is refused before anything is checked.
func TestConsolePasswordBodies(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for _, c := range []struct {
		name, path, body, want string
		status                 int
	}{
		{"an account change that is not JSON", accountPath, `{"current_password": `, "the body is not the JSON expected", http.StatusBadRequest},
		{"a reset that is not JSON", usersPassURL, `[`, "the body is not the JSON expected", http.StatusBadRequest},
		{"a reset of nobody", usersPassURL, `{"password": "new"}`, "the user name and the new password", http.StatusBadRequest},
	} {
		if code, body, _ := b.do("POST", c.path, c.body); code != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
	if code, body, _ := b.do("POST", usersPassURL, `{"username": "bob"}`, "Content-Type", "text/plain"); code != http.StatusUnsupportedMediaType {
		t.Errorf("a reset sent as text: %d %s", code, body)
	}
}

// TestConsoleResetStoreFails: a reset the store cannot keep leaves the
// other user's password as it was.
func TestConsoleResetStoreFails(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	f.configs.UseConsoleUsers(nil, func([]store.User) error { return errors.New("the disk is full") })
	code, body, _ := b.do("POST", usersPassURL, `{"username": "bob", "password": "new-bob"}`)
	if code != http.StatusInternalServerError || !strings.Contains(body, "saving the password: the disk is full") {
		t.Fatalf("a store that cannot be written: %d %s", code, body)
	}
	if f.logged("set the password of") {
		t.Errorf("a reset that failed was logged: %q", f.logLines())
	}
	if code := f.browser(t).login("bob", "bobpw"); code != http.StatusOK {
		t.Errorf("bob's password after a failed reset: %d", code)
	}
}

// TestConsoleProxyBadPath: a path the Admin API cannot be asked for, as one
// with a control character, is refused by the console.
func TestConsoleProxyBadPath(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	if code, body, _ := b.do("GET", "/api/m/admin/config%0A", ""); code != http.StatusBadRequest || !strings.Contains(body, "control character") {
		t.Errorf("a path with a newline: %d %s", code, body)
	}
}

// TestClientHost: an address without a port is the host itself.
func TestClientHost(t *testing.T) {
	for addr, want := range map[string]string{"192.0.2.1:4000": "192.0.2.1", "[::1]:80": "::1", "@": "@", "": ""} {
		if got := clientHost(&http.Request{RemoteAddr: addr}); got != want {
			t.Errorf("the host of %q: %q, want %q", addr, got, want)
		}
	}
}

// TestConsoleStartsTemporary: a console started while a user's password is
// temporary asks that user to change it.
func TestConsoleStartsTemporary(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	until := time.Now().Add(time.Hour)
	f.configs.UseTemporaryPassword("ada", until)
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := StartConsole(testEngine(t), &config.Console{Address: "127.0.0.1:0",
		Users: []config.ConsoleUser{{Username: "ada", PasswordHash: string(hash)}}}, &config.Admin{}, f.configs, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Shutdown(context.Background()) })
	if got, temp := cs.logins.Temporary("ada"); !temp || !got.Equal(until) {
		t.Errorf("ada's password: temporary %v until %v, want %v", temp, got, until)
	}
}

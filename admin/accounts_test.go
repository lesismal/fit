package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
)

// The console's account endpoints.
const (
	usersPath    = "/api/m/admin/users"
	accountPath  = "/api/m/admin/account/password"
	usersPassURL = "/api/m/admin/users/password"
)

// startStoreConsole starts a console whose Configs keep the password hashes
// changed from it beside the config in a store, as fit run does with
// admin.store. It returns the store, and adminJSON, the bootstrap file's
// admin as fit run reads it.
func startStoreConsole(t *testing.T) (*consoleFixture, *store.Store, []byte) {
	t.Helper()
	adaHash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	bobHash, err := bcrypt.GenerateFromPassword([]byte("bobpw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	users := []config.ConsoleUser{
		{Username: "ada", PasswordHash: string(adaHash)},
		{Username: "bob", PasswordHash: string(bobHash)},
	}
	f := startConsole(t, &config.Console{Users: users})
	adminJSON, err := json.Marshal(map[string]any{"console": map[string]any{"users": users}})
	if err != nil {
		t.Fatal(err)
	}
	st, _, err := store.Open(filepath.Join(t.TempDir(), "fit.store"), "secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write([]byte(doc("one"))); err != nil {
		t.Fatal(err)
	}
	f.configs.UseStore(nil, st.Write)
	f.configs.UseConsoleUsers(st.Users(), st.SetUsers)
	return f, st, adminJSON
}

// consoleFromAdmin returns the console a bootstrap admin JSON describes,
// with the users merged into it as fit run does at a restart.
func consoleFromAdmin(t *testing.T, adminJSON []byte, users []store.User) *config.Console {
	t.Helper()
	var a struct {
		Console *config.Console `json:"console"`
	}
	if err := json.Unmarshal(WithConsoleUsers(adminJSON, users), &a); err != nil || a.Console == nil {
		t.Fatalf("the admin with the store's users: %v, %s", err, adminJSON)
	}
	return a.Console
}

// TestConsoleUsers lists the users who may log in: with no store, the pages
// are told that passwords cannot change.
func TestConsoleUsers(t *testing.T) {
	f := startConsole(t, &config.Console{})
	if code, _, _ := f.browser(t).do("GET", usersPath, ""); code != http.StatusUnauthorized {
		t.Errorf("without a login: %d", code)
	}
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	code, body, _ := b.do("GET", usersPath, "")
	var got usersBody
	if err := json.Unmarshal([]byte(body), &got); code != http.StatusOK || err != nil {
		t.Fatalf("users: %d %s", code, body)
	}
	if got.User != "ada" || got.Store || len(got.Users) != 1 || got.Users[0] != "ada" {
		t.Errorf("users: %+v", got)
	}
}

// TestConsolePasswordChange changes a user's own password: the wrong current
// password is refused, the right one changes it, the user's other sessions
// end, and the store keeps the hash so that a restart logs in with it.
func TestConsolePasswordChange(t *testing.T) {
	f, st, adminJSON := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	other := f.browser(t)
	if code := other.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("the second browser's login: %d", code)
	}
	if code, body, _ := b.do("GET", usersPath, ""); code != http.StatusOK || !strings.Contains(body, `"store":true`) {
		t.Errorf("users with a store: %d %s", code, body)
	}

	// A request without the session's CSRF token changes nothing.
	csrf := b.csrf
	b.csrf = ""
	if code, _, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`); code != http.StatusForbidden {
		t.Errorf("without the CSRF token: %d", code)
	}
	b.csrf = csrf

	// The body's fields, and a password bcrypt does not take, are refused
	// before anything is checked.
	for _, body := range []string{
		`{"current_password": "pw"}`,
		`{"password": "new"}`,
		`{"current_password": "pw", "password": "` + strings.Repeat("a", 73) + `"}`,
	} {
		if code, answer, _ := b.do("POST", accountPath, body); code != http.StatusBadRequest {
			t.Errorf("%.60s: %d %s", body, code, answer)
		}
	}

	// The wrong current password is refused, and the old one still logs in.
	if code, body, _ := b.do("POST", accountPath, `{"current_password": "wrong", "password": "new"}`); code != http.StatusForbidden || !strings.Contains(body, "the current password is wrong") {
		t.Fatalf("a wrong current password: %d %s", code, body)
	}
	if code := f.browser(t).login("ada", "pw"); code != http.StatusOK {
		t.Errorf("the old password after a refused change: %d", code)
	}

	// The right one changes it.
	code, body, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`)
	if code != http.StatusOK || strings.TrimSpace(body) != `{"changed":true,"user":"ada"}` {
		t.Fatalf("the change: %d %s", code, body)
	}
	if !f.logged(`console: "ada" changed the password of "ada"`) {
		t.Errorf("logs: %q", f.logLines())
	}
	// The user's other sessions end; the one that changed the password
	// stays, and so does the login with the new password.
	if code, _, _ := other.do("GET", "/api/session", ""); code != http.StatusUnauthorized {
		t.Errorf("another session: %d", code)
	}
	if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusOK {
		t.Errorf("the session that changed the password: %d", code)
	}
	if code := f.browser(t).login("ada", "pw"); code != http.StatusUnauthorized {
		t.Errorf("the old password logs in: %d", code)
	}
	if code := f.browser(t).login("ada", "new"); code != http.StatusOK {
		t.Errorf("the new password: %d", code)
	}
	if users, _ := f.configs.ConsoleUsers(); len(users) != 1 || users[0].Username != "ada" ||
		bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("new")) != nil {
		t.Errorf("the configs' users: %v", users)
	}

	// The store keeps the hash, and a console started from the merged
	// bootstrap admin logs in with the new password.
	opened, data, err := store.Open(st.Path(), "secret")
	if err != nil || string(data) != doc("one") {
		t.Fatalf("the store: %v, %q", err, data)
	}
	users := opened.Users()
	if len(users) != 1 || users[0].Username != "ada" || users[0].PasswordHash == "" {
		t.Fatalf("the store's users: %v", users)
	}
	if bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("new")) != nil {
		t.Error("the store does not hold the new password's hash")
	}
	restarted := startConsole(t, consoleFromAdmin(t, adminJSON, users))
	if code := restarted.browser(t).login("ada", "new"); code != http.StatusOK {
		t.Errorf("the new password after a restart: %d", code)
	}
}

// TestConsolePasswordReset resets another user's password: the target logs
// in with it, its sessions end, and the store keeps the hash.
func TestConsolePasswordReset(t *testing.T) {
	f, st, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	bob := f.browser(t)
	if code := bob.login("bob", "bobpw"); code != http.StatusOK {
		t.Fatalf("bob's login: %d", code)
	}

	// An unknown user, and a password bcrypt does not take.
	if code, body, _ := b.do("POST", usersPassURL, `{"username": "nobody", "password": "x"}`); code != http.StatusBadRequest || !strings.Contains(body, `there is no console user named \"nobody\"`) {
		t.Errorf("an unknown user: %d %s", code, body)
	}
	if code, _, _ := b.do("POST", usersPassURL, `{"username": "bob"}`); code != http.StatusBadRequest {
		t.Errorf("no new password: %d", code)
	}

	code, body, _ := b.do("POST", usersPassURL, `{"username": "bob", "password": "newbob"}`)
	if code != http.StatusOK || strings.TrimSpace(body) != `{"changed":true,"username":"bob"}` {
		t.Fatalf("the reset: %d %s", code, body)
	}
	if !f.logged(`console: "ada" set the password of "bob"`) {
		t.Errorf("logs: %q", f.logLines())
	}
	if code, _, _ := bob.do("GET", "/api/session", ""); code != http.StatusUnauthorized {
		t.Errorf("the target's session: %d", code)
	}
	if code := f.browser(t).login("bob", "newbob"); code != http.StatusOK {
		t.Errorf("the target's new password: %d", code)
	}
	// The store keeps bob's hash, and ada's sessions stay.
	opened, _, err := store.Open(st.Path(), "secret")
	if err != nil {
		t.Fatal(err)
	}
	users := opened.Users()
	if len(users) != 1 || users[0].Username != "bob" || bcrypt.CompareHashAndPassword([]byte(users[0].PasswordHash), []byte("newbob")) != nil {
		t.Errorf("the store's users: %v", users)
	}
	if code, _, _ := b.do("GET", "/api/session", ""); code != http.StatusOK {
		t.Errorf("the caller's session: %d", code)
	}
}

// TestConsolePasswordNoStore: without a store, a change is refused, so that
// nothing is lost at the next restart.
func TestConsolePasswordNoStore(t *testing.T) {
	f := startConsole(t, &config.Console{})
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for _, tc := range []struct{ path, body string }{
		{accountPath, `{"current_password": "pw", "password": "new"}`},
		{usersPassURL, `{"username": "ada", "password": "new"}`},
	} {
		code, body, _ := b.do("POST", tc.path, tc.body)
		if code != http.StatusConflict || !strings.Contains(body, errNoConsoleStore) {
			t.Errorf("%s without a store: %d %s", tc.path, code, body)
		}
	}
	// Nothing changed here.
	if code := f.browser(t).login("ada", "pw"); code != http.StatusOK {
		t.Errorf("the password: %d", code)
	}
}

// TestConsolePasswordStoreFails: a change the store cannot keep leaves the
// login as it was.
func TestConsolePasswordStoreFails(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	f.configs.UseConsoleUsers(nil, func([]store.User) error { return errors.New("the disk is full") })
	code, body, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`)
	if code != http.StatusInternalServerError || !strings.Contains(body, "saving the password: the disk is full") {
		t.Fatalf("a store that cannot be written: %d %s", code, body)
	}
	if f.logged("changed the password of") {
		t.Errorf("a change that failed was logged: %q", f.logLines())
	}
	if code := f.browser(t).login("ada", "pw"); code != http.StatusOK {
		t.Errorf("the password after a failed save: %d", code)
	}
	if code := f.browser(t).login("ada", "new"); code != http.StatusUnauthorized {
		t.Errorf("the password a failed save would have set: %d", code)
	}
}

// TestConsolePasswordLockout: the wrong current password counts as a failed
// login, and locks the address out of every password check.
func TestConsolePasswordLockout(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for i := range login.MaxFailures - 1 {
		if code, _, _ := b.do("POST", accountPath, `{"current_password": "wrong", "password": "new"}`); code != http.StatusForbidden {
			t.Fatalf("failure %d: %d", i+1, code)
		}
	}
	code, body, h := b.do("POST", accountPath, `{"current_password": "wrong", "password": "new"}`)
	if code != http.StatusTooManyRequests || h.Get("Retry-After") == "" {
		t.Fatalf("the failure that locks the address out: %d %s %v", code, body, h)
	}
	if code, _, _ := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`); code != http.StatusTooManyRequests {
		t.Errorf("the right current password while locked out: %d", code)
	}
}

// TestConsolePasswordResetLockedOut: a reset is refused while the address is
// locked out, as a change of a user's own password is.
func TestConsolePasswordResetLockedOut(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for range login.MaxFailures {
		f.console.logins.Failed("127.0.0.1")
	}
	if code, _, _ := b.do("POST", usersPassURL, `{"username": "bob", "password": "newbob"}`); code != http.StatusTooManyRequests {
		t.Errorf("a reset while locked out: %d", code)
	}
}

// TestSetConsoleUsersWithoutStore: with no store, a change keeps nothing.
func TestSetConsoleUsersWithoutStore(t *testing.T) {
	f := startConsole(t, &config.Console{})
	if err := f.configs.SetConsoleUsers([]store.User{{Username: "ada", PasswordHash: "x"}}); err == nil {
		t.Error("users kept with no store")
	}
	if users, stored := f.configs.ConsoleUsers(); stored || len(users) != 0 {
		t.Errorf("users with no store: %v %v", users, stored)
	}
}

// TestPasswordProblem checks what a new password may be, and the messages
// the API answers when it may not.
func TestPasswordProblem(t *testing.T) {
	for _, tc := range []struct {
		password, want string
	}{
		{"", "empty"},
		{strings.Repeat("a", 72), ""},
		{strings.Repeat("a", 73), "72 bytes"},
		{"pw", ""},
	} {
		if got := passwordProblem(tc.password); !strings.Contains(got, tc.want) {
			t.Errorf("%d bytes: %q, want %q", len(tc.password), got, tc.want)
		}
	}
}

// TestWithConsoleUsers merges the store's hashes into the bootstrap admin,
// keeping its member order, and refusing nothing it cannot read.
func TestWithConsoleUsers(t *testing.T) {
	adminJSON := []byte(`{"address": "127.0.0.1:0",
  "console": {"address": "127.0.0.1:0", "users": [
    {"username": "ada", "password_hash": "old-ada"},
    {"username": "bob", "password_hash": "old-bob"}]}}`)
	users := []store.User{{Username: "bob", PasswordHash: "new-bob"}, {Username: "nobody", PasswordHash: "new-nobody"}}
	got := WithConsoleUsers(adminJSON, users)
	var merged struct {
		Address string `json:"address"`
		Console struct {
			Users []store.User `json:"users"`
		} `json:"console"`
	}
	if err := json.Unmarshal(got, &merged); err != nil {
		t.Fatalf("the merged admin: %v, %s", err, got)
	}
	want := []store.User{{Username: "ada", PasswordHash: "old-ada"}, {Username: "bob", PasswordHash: "new-bob"}}
	if merged.Address != "127.0.0.1:0" || len(merged.Console.Users) != 2 ||
		merged.Console.Users[0] != want[0] || merged.Console.Users[1] != want[1] {
		t.Errorf("the merged admin: %+v", merged)
	}
	// The members keep their order.
	text := string(got)
	if from, to := strings.Index(text, `"address"`), strings.Index(text, `"console"`); from < 0 || to < from {
		t.Errorf("the member order: %s", text)
	}
	if ada, bob := strings.Index(text, `"ada"`), strings.Index(text, `"bob"`); ada < 0 || bob < ada {
		t.Errorf("the users' order: %s", text)
	}
	// Nothing to merge, or nothing to merge it into: the admin is unchanged.
	for _, tc := range []struct {
		name  string
		admin []byte
		users []store.User
	}{
		{"no users", adminJSON, nil},
		{"nothing to replace", adminJSON, []store.User{{Username: "nobody", PasswordHash: "x"}}},
		{"no console", []byte(`{"address": "127.0.0.1:0"}`), users},
		{"not JSON", []byte(`address: :8080`), users},
		{"not an object", []byte(`[1]`), users},
		{"console not an object", []byte(`{"console": true}`), users},
		{"users not an array", []byte(`{"console": {"users": {}}}`), users},
		{"a user not an object", []byte(`{"console": {"users": [1]}}`), users},
		{"a name not a string", []byte(`{"console": {"users": [{"username": 1}]}}`), users},
	} {
		if got := string(WithConsoleUsers(tc.admin, tc.users)); got != string(tc.admin) {
			t.Errorf("%s: changed to %s", tc.name, got)
		}
	}
}

package admin

import (
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/login"
	fitruntime "github.com/lesismal/fit/runtime"
)

// TestConfigsConsoleUsers: without a store the console users cannot
// change; a failed write keeps the old list; a password replaces the
// user's own hash or adds the user.
func TestConfigsConsoleUsers(t *testing.T) {
	data := []byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}]}`)
	c, err := config.Load(data)
	if err != nil {
		t.Fatal(err)
	}
	cs := NewConfigs(new(fitruntime.Runtime), nil, fitruntime.Env{}, c, data, SourceFile, 1)
	const noStore = "admin: no store keeps the console users"
	if err := cs.SetConsoleUsers([]store.User{{Username: "ada"}}); err == nil || err.Error() != noStore {
		t.Errorf("users without a store: %v", err)
	}
	if err := cs.SetConsolePassword("ada", "h"); err == nil || err.Error() != noStore {
		t.Errorf("a password without a store: %v", err)
	}

	var saved []store.User
	var saveErr error
	cs.UseConsoleUsers([]store.User{{Username: "ada", PasswordHash: "a1"}, {Username: "bob", PasswordHash: "b1"}}, func(u []store.User) error {
		if saveErr != nil {
			return saveErr
		}
		saved = u
		return nil
	})
	saveErr = errors.New("disk full")
	if err := cs.SetConsoleUsers(nil); err != saveErr {
		t.Errorf("a failed write: %v", err)
	}
	if err := cs.SetConsolePassword("ada", "a2"); err != saveErr {
		t.Errorf("a failed password write: %v", err)
	}
	if users, stored := cs.ConsoleUsers(); !stored || len(users) != 2 || users[0].PasswordHash != "a1" {
		t.Errorf("after failed writes: %v %v", users, stored)
	}

	saveErr = nil
	if err := cs.SetConsolePassword("bob", "b2"); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetConsolePassword("cy", "c1"); err != nil {
		t.Fatal(err)
	}
	want := []store.User{{Username: "ada", PasswordHash: "a1"}, {Username: "bob", PasswordHash: "b2"}, {Username: "cy", PasswordHash: "c1"}}
	if users, _ := cs.ConsoleUsers(); len(users) != 3 || users[0] != want[0] || users[1] != want[1] || users[2] != want[2] || len(saved) != 3 {
		t.Errorf("after the passwords: %v, saved %v", users, saved)
	}
	if err := cs.SetConsoleUsers(want[:1]); err != nil {
		t.Fatal(err)
	}
	if users, _ := cs.ConsoleUsers(); len(users) != 1 || users[0] != want[0] || len(saved) != 1 {
		t.Errorf("after setting the users: %v, saved %v", users, saved)
	}
}

// TestAccountPasswordBusy refuses a password change while another check
// from the same address is under way.
func TestAccountPasswordBusy(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	tk, _, _ := f.console.logins.Admit("127.0.0.1")
	if tk == nil {
		t.Fatal("not admitted")
	}
	code, body, h := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "another password check from this address is under way") || h.Get("Retry-After") != "2" {
		t.Errorf("a change while a check is under way: %d %s %v", code, body, h)
	}
	tk.Done(false)
	if code, body, _ := b.do("POST", accountPath, `{"current_password": "wrong", "password": "new"}`); code != http.StatusForbidden {
		t.Errorf("once the check is done: %d %s", code, body)
	}
}

// inTicketLockedOut reports whether the clock is asked by a ticket's
// LockedOut, the check a queued password check makes before it runs.
func inTicketLockedOut() bool {
	pcs := make([]uintptr, 8)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		fr, more := frames.Next()
		if strings.HasSuffix(fr.Function, "login.(*Ticket).LockedOut") {
			return true
		}
		if !more {
			return false
		}
	}
}

// TestAccountPasswordLockedOutWhileQueued refuses a password change whose
// address was locked out while it waited for the console's pool.
func TestAccountPasswordLockedOutWhileQueued(t *testing.T) {
	f, _, _ := startStoreConsole(t)
	// The ticket's own look at the lockout comes from a time it still
	// holds: as if the address was locked out after it was admitted.
	f.console.logins.SetClock(func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		if inTicketLockedOut() {
			return f.now.Add(-2 * time.Second)
		}
		return f.now
	})
	b := f.browser(t)
	if code := b.login("ada", "pw"); code != http.StatusOK {
		t.Fatalf("login: %d", code)
	}
	for range login.MaxFailures {
		f.console.logins.Failed("127.0.0.1")
	}
	f.advance(login.Lockout + time.Second)
	code, body, h := b.do("POST", accountPath, `{"current_password": "pw", "password": "new"}`)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "too many failed password checks") || h.Get("Retry-After") == "" {
		t.Errorf("a change locked out in the queue: %d %s %v", code, body, h)
	}
	if users, _ := f.configs.ConsoleUsers(); len(users) != 0 {
		t.Errorf("the change was saved: %v", users)
	}
}

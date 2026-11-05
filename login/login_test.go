package login

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

func newLogins(t *testing.T) (*Logins, *time.Time) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	l := New(Options{Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}},
		Idle: time.Minute, Lifetime: time.Hour, MaxSessions: 2})
	now := time.Now()
	l.SetClock(func() time.Time { return now })
	return l, &now
}

func TestCheck(t *testing.T) {
	l, _ := newLogins(t)
	ctx := context.Background()
	for _, c := range []struct {
		user, password string
		ok             bool
	}{{"admin", "secret", true}, {"admin", "wrong", false}, {"nobody", "secret", false}, {"", "", false}} {
		if ok, err := l.Check(ctx, c.user, c.password); err != nil || ok != c.ok {
			t.Errorf("%q %q: %v %v", c.user, c.password, ok, err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	for range Checks {
		l.checks <- struct{}{}
	}
	if _, err := l.Check(cctx, "admin", "secret"); err == nil {
		t.Error("a check waiting past its context's end")
	}
}

func TestSessions(t *testing.T) {
	l, now := newLogins(t)
	token, s := l.Start("admin")
	if got, ok := l.Lookup(token); !ok || got.User != "admin" || !CSRFOK(got, s.CSRF) || CSRFOK(got, "x") {
		t.Fatalf("lookup: %+v %v", got, ok)
	}
	if _, ok := l.Lookup(""); ok {
		t.Error("an empty token")
	}
	// Idle too long.
	*now = now.Add(2 * time.Minute)
	if _, ok := l.Lookup(token); ok {
		t.Error("an idle session lives")
	}
	// Too many: the oldest goes.
	a, _ := l.Start("admin")
	*now = now.Add(time.Second)
	b, _ := l.Start("admin")
	*now = now.Add(time.Second)
	c, _ := l.Start("admin")
	if _, ok := l.Lookup(a); ok {
		t.Error("the oldest session lives past max_sessions")
	}
	for _, tk := range []string{b, c} {
		if _, ok := l.Lookup(tk); !ok {
			t.Error("a newer session ended")
		}
	}
	if _, ok := l.End(b); !ok {
		t.Error("End found no session")
	}
	if _, ok := l.Lookup(b); ok {
		t.Error("an ended session lives")
	}
	// Past its lifetime, however busy.
	for range 70 {
		*now = now.Add(time.Minute - time.Second)
		l.Lookup(c)
	}
	if _, ok := l.Lookup(c); ok {
		t.Error("a session lives past its lifetime")
	}
}

func TestLockout(t *testing.T) {
	l, now := newLogins(t)
	for range MaxFailures - 1 {
		l.Failed("10.0.0.1")
	}
	if l.LockedOut("10.0.0.1") > 0 {
		t.Error("locked out early")
	}
	l.Failed("10.0.0.1")
	if l.LockedOut("10.0.0.1") <= 0 || l.LockedOut("10.0.0.2") > 0 {
		t.Error("lockout")
	}
	*now = now.Add(Lockout + time.Second)
	if l.LockedOut("10.0.0.1") > 0 {
		t.Error("still locked out")
	}
	for i := range MaxAddresses + 10 {
		l.Failed(time.Duration(i).String())
	}
	if n := l.Addresses(); n > MaxAddresses {
		t.Errorf("%d addresses kept", n)
	}
}

func TestCookieName(t *testing.T) {
	for _, tc := range []struct {
		addr net.Addr
		want string
	}{
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 2020}, "fit_console_2020"},
		{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}, "fit_console"},
		{nil, "fit_console"},
		{&net.UnixAddr{Name: "/run/fit.sock", Net: "unix"}, "fit_console"},
	} {
		if got := CookieName("fit_console", tc.addr); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// TestUsersAndPasswords: the users may be read, and a password set or added
// from a console, which Check uses from then on.
func TestUsersAndPasswords(t *testing.T) {
	l, _ := newLogins(t)
	if got := l.Users(); !slices.Equal(got, []string{"admin"}) {
		t.Errorf("users: %v", got)
	}
	if !l.HasUser("admin") || l.HasUser("nobody") || l.HasUser("") {
		t.Error("HasUser")
	}
	ctx := context.Background()
	hash, _ := bcrypt.GenerateFromPassword([]byte("new"), bcrypt.MinCost)
	l.SetPassword("admin", hash)
	if ok, err := l.Check(ctx, "admin", "new"); err != nil || !ok {
		t.Errorf("the new password: %v %v", ok, err)
	}
	if ok, _ := l.Check(ctx, "admin", "secret"); ok {
		t.Error("the old password still checks")
	}
	bobHash, _ := bcrypt.GenerateFromPassword([]byte("bob"), bcrypt.MinCost)
	l.SetPassword("bob", bobHash)
	if got := l.Users(); !slices.Equal(got, []string{"admin", "bob"}) {
		t.Errorf("users after an addition: %v", got)
	}
	if ok, _ := l.Check(ctx, "bob", "bob"); !ok {
		t.Error("a user added by its password does not check")
	}
}

// TestEndUserSessions: a user's sessions end, but the one whose token is
// kept; an empty token keeps none.
func TestEndUserSessions(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	l := New(Options{Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}},
		Idle: time.Minute, Lifetime: time.Hour, MaxSessions: 8})
	now := time.Now()
	l.SetClock(func() time.Time { return now })
	l.SetPassword("bob", hash)
	mine, _ := l.Start("admin")
	other, _ := l.Start("admin")
	third, _ := l.Start("admin")
	bobs, _ := l.Start("bob")
	if n := l.EndUserSessions("admin", mine); n != 2 {
		t.Errorf("ended %d sessions, want 2", n)
	}
	for _, tc := range []struct {
		name  string
		token string
		live  bool
	}{{"the caller's", mine, true}, {"another", other, false}, {"a third", third, false}, {"another user's", bobs, true}} {
		if _, ok := l.Lookup(tc.token); ok != tc.live {
			t.Errorf("%s session: live %v, want %v", tc.name, ok, tc.live)
		}
	}
	if n := l.EndUserSessions("admin", ""); n != 1 {
		t.Errorf("ended %d sessions, want 1", n)
	}
	if n := l.EndUserSessions("nobody", ""); n != 0 {
		t.Errorf("ended %d sessions of an unknown user", n)
	}
	if _, ok := l.Lookup(bobs); !ok {
		t.Error("another user's session ended")
	}
}

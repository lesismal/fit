package login

import (
	"context"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

func newLogins(t *testing.T) (*Logins, *time.Time) {
	t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	l, err := New(Options{Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}},
		Idle: time.Minute, Lifetime: time.Hour, MaxSessions: 2})
	if err != nil {
		t.Fatal(err)
	}
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

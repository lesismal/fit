package console

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/login"
)

// inTicketLockedOut reports whether the clock is asked by a ticket's
// LockedOut, the check a queued login makes before it runs.
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

// TestConsoleLoginLockedOutWhileQueued refuses a login whose address was
// locked out while it waited for the login pool, even with the right
// password.
func TestConsoleLoginLockedOutWhileQueued(t *testing.T) {
	engine := newEngine(t)
	c, err := Start(engine, options(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	var mu sync.Mutex
	now := time.Now()
	// The ticket's own look at the lockout comes from a time the lockout
	// still holds: as if the address was locked out after it was admitted.
	c.logins.SetClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if inTicketLockedOut() {
			return now.Add(-2 * time.Second)
		}
		return now
	})
	for range login.MaxFailures {
		c.logins.Failed("127.0.0.1")
	}
	mu.Lock()
	now = now.Add(login.Lockout + time.Second)
	mu.Unlock()
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
	if code, body := b.do("POST", "/api/login", `{"username":"admin","password":"secret"}`); code != 429 || !strings.Contains(body, "too many failed logins") {
		t.Errorf("a login locked out in the queue: %d %s", code, body)
	}
	// It started no session.
	if code, _ := b.do("GET", "/api/session", ""); code != 401 {
		t.Errorf("a session after the refused login: %d", code)
	}
}

// TestConsoleNoModules answers 404 for a module's endpoint on a console
// that serves no modules.
func TestConsoleNoModules(t *testing.T) {
	engine := newEngine(t)
	c, err := Start(engine, options(t, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Stop(context.Background())
	b := newBrowser(t, strings.TrimSuffix(c.URL(), "/"), nil)
	b.login()
	if code, body := b.do("GET", "/api/m/admin/config", ""); code != 404 || !strings.Contains(body, `no module \"admin\" runs here`) {
		t.Errorf("a module's endpoint: %d %s", code, body)
	}
}

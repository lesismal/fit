// Package login keeps the logins of fit's web pages: users with bcrypt
// hashes, sessions with idle and absolute ends, a CSRF token for each, and
// addresses refused for a while after failing too often. The Admin API's
// console and the consoles of the agent and tunnel roles share it; it knows
// nothing of the server that carries the requests.
//
// Checking a password takes bcrypt's time, tens of milliseconds on purpose:
// Check blocks, and is never called on a fib worker (rule 4). At most Checks
// run at once.
package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// Failed logins: how many from one address before it is refused, within what
// window, for how long, and how many addresses are remembered; how many
// passwords are checked at once.
const (
	MaxFailures  = 5
	Window       = 15 * time.Minute
	Lockout      = 15 * time.Minute
	MaxAddresses = 4096
	Checks       = 2
)

// Options are the users and the limits of a set of logins.
type Options struct {
	Users       []config.ConsoleUser
	Idle        time.Duration // how long a session lasts without a request
	Lifetime    time.Duration // how long a session lasts at most
	MaxSessions int           // the most sessions kept; one more ends the oldest
}

// OptionsOf returns the options c, a console's config, gives.
func OptionsOf(c *config.Console) Options {
	return Options{Users: c.Users, Idle: c.Idle(), Lifetime: c.Lifetime(), MaxSessions: c.Sessions()}
}

// Session is a user logged in.
type Session struct {
	User          string
	CSRF          string
	Created, Seen time.Time
}

// Logins are the users who may log in and the sessions of those who did.
// Its methods are safe to call from any goroutine.
type Logins struct {
	users    map[string][]byte // user name to bcrypt hash
	dummy    []byte            // a hash to check unknown names against
	idle     time.Duration
	lifetime time.Duration
	max      int
	now      func() time.Time

	checks chan struct{} // a slot for each password checked at once

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]*Session // by the hash of their token
	fails    map[string]*failures           // by client address
}

type failures struct {
	n            int
	first, until time.Time
}

// New returns the logins o describes, with no session yet.
func New(o Options) (*Logins, error) {
	l := &Logins{
		users: map[string][]byte{}, idle: o.Idle, lifetime: o.Lifetime, max: max(o.MaxSessions, 1),
		now: time.Now, checks: make(chan struct{}, Checks),
		sessions: map[[sha256.Size]byte]*Session{}, fails: map[string]*failures{},
	}
	cost := bcrypt.DefaultCost
	for _, u := range o.Users {
		l.users[u.Username] = []byte(u.PasswordHash)
		if n, err := bcrypt.Cost([]byte(u.PasswordHash)); err == nil {
			cost = n
		}
	}
	// An unknown name costs what a known one does.
	var err error
	if l.dummy, err = bcrypt.GenerateFromPassword([]byte(rand.Text()), cost); err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	return l, nil
}

// SetClock replaces the clock, for tests.
func (l *Logins) SetClock(now func() time.Time) { l.now = now }

// Check reports whether password is user's, waiting for a slot among the
// checks under way until ctx ends, when it gives ctx's error. It blocks for
// bcrypt's time: never call it on a fib worker.
func (l *Logins) Check(ctx context.Context, user, password string) (bool, error) {
	select {
	case l.checks <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	hash, known := l.users[user]
	if !known {
		hash = l.dummy
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(password))
	<-l.checks
	return err == nil && known, nil
}

// Start starts a session for user and returns its token, ending the oldest
// when there are too many.
func (l *Logins) Start(user string) (string, Session) {
	token := rand.Text() + rand.Text() // 52 base32 characters: 260 bits
	csrf := make([]byte, 24)
	_, _ = rand.Read(csrf)
	now := l.now()
	s := &Session{User: user, CSRF: base64.RawURLEncoding.EncodeToString(csrf), Created: now, Seen: now}
	l.mu.Lock()
	defer l.mu.Unlock()
	for len(l.sessions) >= l.max {
		var oldest [sha256.Size]byte
		var at time.Time
		for k, v := range l.sessions {
			if at.IsZero() || v.Created.Before(at) {
				oldest, at = k, v.Created
			}
		}
		delete(l.sessions, oldest)
	}
	l.sessions[sha256.Sum256([]byte(token))] = s
	return token, *s
}

// Lookup returns the live session of token, ending one that has lasted too
// long or been idle too long.
func (l *Logins) Lookup(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	key := sha256.Sum256([]byte(token))
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessions[key]
	if s == nil {
		return Session{}, false
	}
	if now.Sub(s.Seen) > l.idle || now.Sub(s.Created) > l.lifetime {
		delete(l.sessions, key)
		return Session{}, false
	}
	s.Seen = now
	return *s, true
}

// CSRFOK reports whether got is s's CSRF token.
func CSRFOK(s Session, got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.CSRF)) == 1
}

// End ends the session of token, and returns it if there was one.
func (l *Logins) End(token string) (Session, bool) {
	key := sha256.Sum256([]byte(token))
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessions[key]
	delete(l.sessions, key)
	if s == nil {
		return Session{}, false
	}
	return *s, true
}

// LockedOut returns how long addr is still refused logins.
func (l *Logins) LockedOut(addr string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.fails[addr]; f != nil {
		return f.until.Sub(l.now())
	}
	return 0
}

// Failed counts a failed login from addr, refusing it logins for a while
// once it has failed too often.
func (l *Logins) Failed(addr string) {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[addr]
	if f == nil || now.Sub(f.first) > Window && now.After(f.until) {
		if f == nil && len(l.fails) >= MaxAddresses {
			l.forgetFailures(now)
		}
		f = &failures{first: now}
		l.fails[addr] = f
	}
	if f.n++; f.n >= MaxFailures {
		f.until = now.Add(Lockout)
	}
}

// Addresses returns how many addresses' failures are remembered.
func (l *Logins) Addresses() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}

// forgetFailures makes room in the table of failures: the addresses no
// longer counted or refused go, or else the one that began failing first.
func (l *Logins) forgetFailures(now time.Time) {
	var oldest string
	var at time.Time
	for k, f := range l.fails {
		if now.Sub(f.first) > Window && now.After(f.until) {
			delete(l.fails, k)
		} else if at.IsZero() || f.first.Before(at) {
			oldest, at = k, f.first
		}
	}
	if len(l.fails) >= MaxAddresses {
		delete(l.fails, oldest)
	}
}

// CookieName is a console's session cookie: name with the port addr listens
// on, since a browser sends a host's cookies to every port of it, and two
// consoles on one host would otherwise end each other's logins.
func CookieName(name string, addr net.Addr) string {
	if a, ok := addr.(*net.TCPAddr); ok && a.Port != 0 {
		return name + "_" + strconv.Itoa(a.Port)
	}
	return name
}

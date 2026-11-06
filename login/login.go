// Package login keeps the logins of fit's web pages: users with bcrypt
// hashes, sessions with idle and absolute ends, a CSRF token for each, and
// addresses and users refused for a while after failing too often, for
// longer each time they go on failing, up to a cap. The Admin API's
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
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
)

// Failed logins: how many from one address before it is refused, within what
// window, for how long the first time, and at most when its lockouts double
// as it goes on failing; how many addresses are remembered; how many
// passwords are checked at once. An address that has failed nothing for a
// Window since its last failure and its lockout is forgotten.
const (
	MaxFailures  = 5
	Window       = 15 * time.Minute
	Lockout      = 15 * time.Minute
	MaxLockout   = time.Hour
	MaxAddresses = 4096
	Checks       = 2
)

// Failed checks of one user's password, from any address: how many in a
// row, with no right one between, before the user's checks are slowed, and
// for how long after that one, doubling with each failure after it, up to
// MaxUserBackoff. A right password ends the count, and so does a Window
// with no failure once the wait is over. The names of no user share one
// count. A user is never refused for longer than MaxUserBackoff after a
// failure, so that guessing from many addresses cannot keep the user out
// for long, and slows to one guess every MaxUserBackoff.
const (
	UserFailures   = 10
	UserBackoff    = 2 * time.Second
	MaxUserBackoff = 5 * time.Minute
)

// unknownUser is the key the failures of names that are no user's are
// counted under: one count for them all, so that their table stays as small
// as the users.
const unknownUser = "\x00"

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
	// MustChange is a session of a user whose password is temporary, as
	// SetTemporary made it: it may change the password and nothing else,
	// until Expires; changing it clears MustChange.
	MustChange bool
	Expires    time.Time
}

// Logins are the users who may log in and the sessions of those who did.
// Its methods are safe to call from any goroutine.
type Logins struct {
	usersMu  sync.RWMutex         // held for a brief read by Check, and for a change of users
	users    map[string][]byte    // user name to bcrypt hash
	temp     map[string]time.Time // users whose password is temporary, to when it lasts
	dummy    []byte               // a hash to check unknown names against
	idle     time.Duration
	lifetime time.Duration
	max      int
	now      func() time.Time

	checks chan struct{} // a slot for each password checked at once

	mu       sync.Mutex
	sessions map[[sha256.Size]byte]*Session // by the hash of their token
	fails    map[string]*failures           // by Key of the client address, at most MaxAddresses
	// userFails are the failures of each user's password, by its name, or
	// unknownUser: no more than the users and one.
	userFails map[string]*userFailures
	// noRoomUntil is when fails, full of addresses locked out, may have
	// room again; zero once a check ends.
	noRoomUntil time.Time
}

// failures are an address's failed checks: how many since first, the last
// one, until when it is refused, how many lockouts it has earned since it
// was last quiet for a Window, and whether a check of it is under way.
type failures struct {
	n                  int
	first, last, until time.Time
	lockouts           int
	busy               bool
}

// userFailures are a user's failed checks in a row: how many, the last one,
// and until when its checks are refused.
type userFailures struct {
	n           int
	last, until time.Time
}

// New returns the logins o describes, with no session yet.
func New(o Options) *Logins {
	l := &Logins{
		users: map[string][]byte{}, temp: map[string]time.Time{}, idle: o.Idle, lifetime: o.Lifetime, max: max(o.MaxSessions, 1),
		now: time.Now, checks: make(chan struct{}, Checks),
		sessions: map[[sha256.Size]byte]*Session{}, fails: map[string]*failures{}, userFails: map[string]*userFailures{},
	}
	cost := bcrypt.DefaultCost
	for _, u := range o.Users {
		l.users[u.Username] = []byte(u.PasswordHash)
		if n, err := bcrypt.Cost([]byte(u.PasswordHash)); err == nil {
			cost = n
		}
	}
	// An unknown name costs what a known one does. The cost is bcrypt's
	// default or one read from a hash, so valid, and the password short:
	// hashing it cannot fail.
	l.dummy, _ = bcrypt.GenerateFromPassword([]byte(rand.Text()), cost)
	return l
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
	l.usersMu.RLock()
	hash, known := l.users[user]
	l.usersMu.RUnlock()
	if !known {
		hash = l.dummy
	}
	err := bcrypt.CompareHashAndPassword(hash, []byte(password))
	<-l.checks
	return err == nil && known, nil
}

// Users returns the users who may log in, sorted.
func (l *Logins) Users() []string {
	l.usersMu.RLock()
	defer l.usersMu.RUnlock()
	names := make([]string, 0, len(l.users))
	for name := range l.users {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// HasUser reports whether user may log in.
func (l *Logins) HasUser(user string) bool {
	l.usersMu.RLock()
	defer l.usersMu.RUnlock()
	_, ok := l.users[user]
	return ok
}

// SetPassword sets user's bcrypt hash, adding the user when it is not there
// yet: a password changed from a console is checked here from then on,
// before fit restarts and reads it from the store. A temporary password is
// temporary no more, and the user's sessions may do everything again.
func (l *Logins) SetPassword(user string, hash []byte) {
	l.usersMu.Lock()
	l.users[user] = slices.Clone(hash)
	_, wasTemp := l.temp[user]
	delete(l.temp, user)
	l.usersMu.Unlock()
	if !wasTemp {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.sessions {
		if s.User == user {
			s.MustChange, s.Expires = false, time.Time{}
		}
	}
}

// SetTemporary makes user's password a temporary one, good until until: a
// session started with it may only change the password (Session.MustChange),
// and after until Expired reports it, so that the console refuses it.
func (l *Logins) SetTemporary(user string, until time.Time) {
	l.usersMu.Lock()
	defer l.usersMu.Unlock()
	l.temp[user] = until
}

// Temporary returns when user's temporary password stops working, and false
// when the password is not temporary.
func (l *Logins) Temporary(user string) (time.Time, bool) {
	l.usersMu.RLock()
	defer l.usersMu.RUnlock()
	until, ok := l.temp[user]
	return until, ok
}

// Expired reports whether user's password is a temporary one past its time:
// a login or a password change with it is refused, until fit restarts.
func (l *Logins) Expired(user string) bool {
	until, ok := l.Temporary(user)
	return ok && !l.now().Before(until)
}

// Start starts a session for user and returns its token, ending the oldest
// when there are too many.
func (l *Logins) Start(user string) (string, Session) {
	token := rand.Text() + rand.Text() // 52 base32 characters: 260 bits
	csrf := make([]byte, 24)
	_, _ = rand.Read(csrf)
	now := l.now()
	s := &Session{User: user, CSRF: base64.RawURLEncoding.EncodeToString(csrf), Created: now, Seen: now}
	if until, ok := l.Temporary(user); ok {
		s.MustChange, s.Expires = true, until
	}
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

// EndUserSessions ends every session of user but the one of keepToken, which
// is the caller's own, and returns how many it ended. An empty keepToken
// keeps none of them.
func (l *Logins) EndUserSessions(user, keepToken string) int {
	var keep [sha256.Size]byte
	if keepToken != "" {
		keep = sha256.Sum256([]byte(keepToken))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	ended := 0
	for key, s := range l.sessions {
		if s.User != user || keepToken != "" && key == keep {
			continue
		}
		delete(l.sessions, key)
		ended++
	}
	return ended
}

// IPv6Prefix is how many leading bits of an IPv6 address name the client
// whose failures are counted: a host is given a /64 to pick its addresses
// from, so counting by the whole address would give it a fresh count with
// each one.
const IPv6Prefix = 64

// Key returns what failures from addr are counted by: an IPv4 address as it
// is (an IPv4-mapped IPv6 one as IPv4), an IPv6 one by its IPv6Prefix, and
// anything else, as a Unix socket's peer, as it is.
func Key(addr string) string {
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return addr
	}
	ip = ip.Unmap().WithZone("")
	if ip.Is4() {
		return ip.String()
	}
	p, _ := ip.Prefix(IPv6Prefix)
	return p.String()
}

// Ticket is a password check admitted from an address, as Admit gives it:
// no other check from the address is admitted until Done. One AdmitUser
// gives is of a user's password too, whose failures it counts.
type Ticket struct {
	l       *Logins
	key     string
	f       *failures
	user    string // the key of the user's failures, when hasUser
	hasUser bool
	done    bool // under l.mu
}

// Admit admits a password check from addr, before it is queued, and returns
// its ticket; Done must follow, whatever comes of the check. One check from
// an address (an IPv6 /64) is admitted at a time, so that every failure is
// counted before the next guess is checked. A refused check returns a nil
// ticket and how long to wait: while addr is locked out, while another check
// from it is under way (busy), and while the table of addresses is full of
// ones locked out or being checked.
func (l *Logins) Admit(addr string) (t *Ticket, wait time.Duration, busy bool) {
	key := Key(addr)
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	f := l.fails[key]
	if f == nil {
		if len(l.fails) >= MaxAddresses && !l.makeRoom(now) {
			return nil, max(l.noRoomUntil.Sub(now), time.Second), false
		}
		f = &failures{}
		l.fails[key] = f
	} else if wait := f.until.Sub(now); wait > 0 {
		return nil, wait, false
	} else if f.busy {
		return nil, time.Second, true
	}
	f.busy = true
	return &Ticket{l: l, key: key, f: f}, 0, false
}

// AdmitUser is Admit for a check of user's password: it is refused, too,
// while the user's checks are slowed (UserFailures), when slowed is set and
// wait is how long they still are; and the ticket counts the check's
// failure against the user as well as the address, and its success, Passed,
// ends the user's count.
func (l *Logins) AdmitUser(addr, user string) (t *Ticket, wait time.Duration, busy, slowed bool) {
	key := unknownUser
	if l.HasUser(user) {
		key = user
	}
	if t, wait, busy = l.Admit(addr); t == nil {
		return nil, wait, busy, false
	}
	t.user, t.hasUser = key, true
	if wait := t.userWait(); wait > 0 {
		t.Done(false)
		return nil, wait, false, true
	}
	return t, 0, false, false
}

// userWait returns how long the ticket's user is still refused checks, or
// 0.
func (t *Ticket) userWait() time.Duration {
	if !t.hasUser {
		return 0
	}
	now := t.l.now()
	t.l.mu.Lock()
	defer t.l.mu.Unlock()
	if u := t.l.userFails[t.user]; u != nil {
		return max(u.until.Sub(now), 0)
	}
	return 0
}

// LockedOut returns how long the ticket's address, or its user, is refused
// logins: a check that waited in a queue asks again before it runs.
func (t *Ticket) LockedOut() time.Duration {
	now := t.l.now()
	t.l.mu.Lock()
	wait := t.f.until.Sub(now)
	t.l.mu.Unlock()
	return max(wait, t.userWait())
}

// Done ends the ticket's check, counting a failure when failed, and lets
// the address be checked again; a check that did not run, or a right
// password, is done with false. Only the first call of Done or Passed
// counts.
func (t *Ticket) Done(failed bool) { t.end(failed, false) }

// Passed ends the ticket's check as one whose password was right: done as
// Done(false) is, and the user's count of failures ends.
func (t *Ticket) Passed() { t.end(false, true) }

func (t *Ticket) end(failed, passed bool) {
	l := t.l
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	f := t.f
	f.busy = false
	l.noRoomUntil = time.Time{}
	if failed {
		l.count(f, now)
		if t.hasUser {
			l.countUser(t.user, now)
		}
		return
	}
	// An address with lockouts to its name is kept, so that a check
	// left before it ran does not forget them.
	if f.n == 0 && f.lockouts == 0 && l.fails[t.key] == f {
		delete(l.fails, t.key)
	}
	if passed && t.hasUser {
		delete(l.userFails, t.user)
	}
}

// countUser counts a failure of the user keyed key, starting its count anew
// once it has been quiet for a Window past its wait, and slows its checks
// once it has failed UserFailures times in a row.
func (l *Logins) countUser(key string, now time.Time) {
	u := l.userFails[key]
	if u == nil {
		u = &userFailures{}
		l.userFails[key] = u
	}
	if !now.Before(u.until) && now.Sub(u.last) > Window {
		u.n = 0
	}
	u.n++
	u.last = now
	if u.n >= UserFailures {
		u.until = now.Add(min(UserBackoff<<min(u.n-UserFailures, 16), MaxUserBackoff))
	}
}

// LockedOut returns how long addr is still refused logins.
func (l *Logins) LockedOut(addr string) time.Duration {
	key := Key(addr)
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.fails[key]; f != nil {
		return f.until.Sub(l.now())
	}
	return 0
}

// Failed counts a failed check from addr, as a ticket's Done does, refusing
// it logins for a while once it has failed too often. It counts nothing
// while a check from addr is under way, or the table has no room for it.
func (l *Logins) Failed(addr string) {
	if t, _, _ := l.Admit(addr); t != nil {
		t.Done(true)
	}
}

// count counts a failure in f, starting its window anew once the last one
// is over, and locks its address out once it has failed MaxFailures times in
// a window: for Lockout the first time, and twice as long each time after,
// up to MaxLockout, until it has been quiet for a Window.
func (l *Logins) count(f *failures, now time.Time) {
	switch {
	case f.over(now):
		f.n, f.lockouts, f.first = 0, 0, now
	case f.n == 0 || now.Sub(f.first) > Window && !now.Before(f.until):
		f.n, f.first = 0, now
	}
	f.last = now
	if f.n++; f.n >= MaxFailures {
		f.until = now.Add(min(Lockout<<min(f.lockouts, 16), MaxLockout))
		f.lockouts++
		f.n = 0
	}
}

// over reports whether f is quiet: its lockout past, and a Window past its
// last failure and its lockout.
func (f *failures) over(now time.Time) bool {
	return !now.Before(f.until) && now.Sub(f.last) > Window && now.Sub(f.until) > Window
}

// Addresses returns how many addresses' failures are remembered.
func (l *Logins) Addresses() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.fails)
}

// makeRoom makes room in the full table of failures, and reports whether it
// did: the addresses no longer counted or refused go, or else the one that
// began failing first of those neither locked out nor being checked. A
// lockout is never forgotten early: with every address locked out or being
// checked, new addresses are refused until the first lockout ends, which
// bounds the guesses a crowd of addresses gets.
func (l *Logins) makeRoom(now time.Time) bool {
	if now.Before(l.noRoomUntil) {
		return false
	}
	var oldest string
	var at, until time.Time
	for k, f := range l.fails {
		switch {
		case f.busy:
		case f.over(now):
			delete(l.fails, k)
		case now.Before(f.until):
			if until.IsZero() || f.until.Before(until) {
				until = f.until
			}
		case at.IsZero() || f.first.Before(at):
			oldest, at = k, f.first
		}
	}
	if len(l.fails) < MaxAddresses {
		return true
	}
	if !at.IsZero() {
		delete(l.fails, oldest)
		return true
	}
	if until.IsZero() {
		until = now.Add(time.Second) // every one is being checked
	}
	l.noRoomUntil = until
	return false
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

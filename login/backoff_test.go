package login

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// fail counts a failed check of user from addr, which must be admitted.
func fail(t *testing.T, l *Logins, addr, user string) {
	t.Helper()
	tk, wait, busy, slowed := l.AdmitUser(addr, user)
	if tk == nil {
		t.Fatalf("a check of %q from %s refused: %v %v %v", user, addr, wait, busy, slowed)
	}
	tk.Done(true)
}

// spread is the i-th of a few addresses, none of which fails often enough
// to be locked out by UserFailures failures among them.
func spread(i int) string { return "10.0.1." + strconv.Itoa(i%3+1) }

// TestUserBackoff: the tenth wrong password in a row for one user, from
// any address, slows the user's checks, longer with each further failure
// up to MaxUserBackoff; the right password works once the wait ends, and
// ends the count.
func TestUserBackoff(t *testing.T) {
	l, now := newLogins(t)
	for i := range UserFailures - 1 {
		fail(t, l, spread(i), "admin")
	}
	if tk, _, _, slowed := l.AdmitUser("10.0.2.1", "admin"); tk == nil || slowed {
		t.Fatal("slowed before the tenth failure")
	} else {
		tk.Done(true) // the tenth
	}
	tk, wait, busy, slowed := l.AdmitUser("10.0.2.2", "admin")
	if tk != nil || !slowed || busy || wait != UserBackoff {
		t.Fatalf("after the tenth failure: %v %v %v %v", tk, wait, busy, slowed)
	}
	// Another user, and another address for another user, are not slowed.
	if tk, _, _, _ := l.AdmitUser("10.0.2.2", "nobody"); tk == nil {
		t.Fatal("an unknown name is slowed by the user's failures")
	} else {
		tk.Done(false)
	}

	// Each failure past the tenth doubles the wait.
	*now = now.Add(UserBackoff)
	fail(t, l, "10.0.3.1", "admin")
	if _, wait, _, slowed := l.AdmitUser("10.0.3.2", "admin"); !slowed || wait != 2*UserBackoff {
		t.Fatalf("after the eleventh failure: %v %v", wait, slowed)
	}
	for i := range 20 {
		*now = now.Add(MaxUserBackoff)
		fail(t, l, "10.0.4."+strconv.Itoa(i/4+1), "admin")
	}
	if _, wait, _, slowed := l.AdmitUser("10.0.5.1", "admin"); !slowed || wait != MaxUserBackoff {
		t.Fatalf("the cap: %v %v", wait, slowed)
	}

	// A check queued while the user was not slowed asks again before it
	// runs, and a check that did not run counts nothing.
	*now = now.Add(MaxUserBackoff)
	queued, _, _, _ := l.AdmitUser("10.0.6.1", "admin")
	if queued == nil || queued.LockedOut() != 0 {
		t.Fatal("not admitted once the wait ended")
	}
	fail(t, l, "10.0.6.2", "admin")
	if queued.LockedOut() <= 0 {
		t.Error("a queued check does not see the user slowed")
	}
	queued.Done(false)
	if _, _, _, slowed := l.AdmitUser("10.0.6.3", "admin"); !slowed {
		t.Error("a check that did not run ended the count")
	}

	// The right password, once the wait ends, works and ends the count.
	*now = now.Add(MaxUserBackoff)
	tk, _, _, _ = l.AdmitUser("10.0.7.1", "admin")
	if ok, _ := l.Check(context.Background(), "admin", "secret"); !ok || tk == nil {
		t.Fatal("the right password")
	}
	tk.Passed()
	tk.Done(true) // only the first ending counts
	fail(t, l, "10.0.7.2", "admin")
	if tk, _, _, slowed := l.AdmitUser("10.0.7.3", "admin"); tk == nil || slowed {
		t.Error("slowed after the right password ended the count")
	} else {
		tk.Done(false)
	}
}

// TestUserBackoffQuiet: a user's count ends after a Window without
// failures; names of no user share one count.
func TestUserBackoffQuiet(t *testing.T) {
	l, now := newLogins(t)
	for i := range UserFailures - 1 {
		fail(t, l, spread(i), "admin")
	}
	*now = now.Add(Window + time.Second)
	fail(t, l, "10.0.2.1", "admin")
	if tk, _, _, slowed := l.AdmitUser("10.0.2.2", "admin"); tk == nil || slowed {
		t.Fatal("the count outlived a quiet window")
	} else {
		tk.Done(false)
	}
	for i := range UserFailures {
		fail(t, l, spread(i), "guess"+strconv.Itoa(i))
	}
	if _, _, _, slowed := l.AdmitUser("10.0.2.3", "yet-another"); !slowed {
		t.Error("names of no user are not counted together")
	}
	if _, _, _, slowed := l.AdmitUser("10.0.2.3", "admin"); slowed {
		t.Error("the user is slowed by names of no user")
	}
}

// TestAddressBackoff: an address locked out goes on being locked out for
// twice as long each time it earns another lockout, up to MaxLockout, and
// starts anew once quiet for a Window; a check left before it ran does not
// forget its lockouts.
func TestAddressBackoff(t *testing.T) {
	l, now := newLogins(t)
	const addr = "10.0.0.9"
	want := Lockout
	for round := range 5 {
		for range MaxFailures {
			l.Failed(addr)
		}
		if got := l.LockedOut(addr); got != want {
			t.Fatalf("lockout %d: %v, want %v", round+1, got, want)
		}
		*now = now.Add(want)
		// A check admitted and left before it ran.
		if tk, _, _ := l.Admit(addr); tk == nil {
			t.Fatalf("not admitted after lockout %d", round+1)
		} else {
			tk.Done(false)
		}
		want = min(2*want, MaxLockout)
	}
	// Quiet for a Window past the lockout: the next lockout is the first's.
	*now = now.Add(Window + time.Second)
	for range MaxFailures {
		l.Failed(addr)
	}
	if got := l.LockedOut(addr); got != Lockout {
		t.Errorf("after a quiet window: %v, want %v", got, Lockout)
	}
}

// TestAdmitForgetsQuiet: a full table forgets the addresses quiet for a
// Window to make room.
func TestAdmitForgetsQuiet(t *testing.T) {
	l, now := newLogins(t)
	for i := range MaxAddresses {
		l.Failed("10.5." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
	}
	*now = now.Add(Window + time.Second)
	tk, _, _ := l.Admit("10.6.0.1")
	if tk == nil {
		t.Fatal("refused with quiet addresses to forget")
	}
	tk.Done(false)
	if n := l.Addresses(); n != 0 {
		t.Errorf("%d addresses kept", n)
	}
}

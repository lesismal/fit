package login

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestAttemptCountsChecksUnderWay: logins made at once from one address are
// reserved against its failures left, so that no more of them are checked
// than may fail before the lockout; 30 at once check 5.
func TestAttemptCountsChecksUnderWay(t *testing.T) {
	l, _ := newLogins(t)
	var dones []func(bool)
	var wg sync.WaitGroup
	var mu sync.Mutex
	refused := 0
	for range 30 {
		wg.Go(func() {
			done, wait := l.Attempt("10.0.0.1")
			mu.Lock()
			defer mu.Unlock()
			if wait > 0 {
				refused++
				return
			}
			dones = append(dones, done)
		})
	}
	wg.Wait()
	if len(dones) != MaxFailures || refused != 30-MaxFailures {
		t.Fatalf("%d checks reserved, %d refused; want %d", len(dones), refused, MaxFailures)
	}
	for _, done := range dones {
		done(true)
		done(true) // once only
	}
	if l.LockedOut("10.0.0.1") <= 0 {
		t.Error("not locked out after the checks failed")
	}
	if _, wait := l.Attempt("10.0.0.1"); wait < Lockout-time.Second {
		t.Errorf("an attempt while locked out waits %v", wait)
	}
}

// TestAttemptSucceeds: a check that does not fail frees its reservation and
// leaves nothing to remember.
func TestAttemptSucceeds(t *testing.T) {
	l, _ := newLogins(t)
	for range 3 * MaxFailures {
		done, wait := l.Attempt("10.0.0.1")
		if wait > 0 {
			t.Fatalf("refused after successes: %v", wait)
		}
		done(false)
	}
	if n := l.Addresses(); n != 0 {
		t.Errorf("%d addresses remembered after successes", n)
	}
	// One that failed before stays remembered.
	l.Failed("10.0.0.1")
	done, _ := l.Attempt("10.0.0.1")
	done(false)
	if n := l.Addresses(); n != 1 {
		t.Errorf("%d addresses remembered", n)
	}
}

// TestAttemptIPv6Prefix: an IPv6 address is counted by its /64, so that one
// host cannot spread its guesses over the addresses of its network; an IPv4
// address mapped into IPv6 is the IPv4 address.
func TestAttemptIPv6Prefix(t *testing.T) {
	l, _ := newLogins(t)
	for i := range MaxFailures {
		l.Failed(fmt.Sprintf("2001:db8:1:2::%x", i+1))
	}
	if l.LockedOut("2001:db8:1:2:ffff::1") <= 0 {
		t.Error("another address of the /64 is not locked out")
	}
	if _, wait := l.Attempt("2001:db8:1:2:abcd::9"); wait <= 0 {
		t.Error("another address of the /64 may log in")
	}
	if l.LockedOut("2001:db8:1:3::1") > 0 {
		t.Error("another /64 is locked out")
	}
	for range MaxFailures {
		l.Failed("::ffff:10.0.0.7")
	}
	if l.LockedOut("10.0.0.7") <= 0 {
		t.Error("an IPv4-mapped address is not its IPv4 address")
	}
	for _, tc := range [][2]string{
		{"10.0.0.1", "10.0.0.1"},
		{"2001:db8::1", "2001:db8::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"pipe", "pipe"},
	} {
		if got := key(tc[0]); got != tc[1] {
			t.Errorf("key(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

// TestAttemptMaxPending: no more than MaxPending checks are reserved at once,
// from every address together.
func TestAttemptMaxPending(t *testing.T) {
	l, _ := newLogins(t)
	var dones []func(bool)
	for i := range MaxPending {
		done, wait := l.Attempt(fmt.Sprintf("10.0.1.%d", i))
		if wait > 0 {
			t.Fatalf("check %d refused", i)
		}
		dones = append(dones, done)
	}
	if _, wait := l.Attempt("10.0.2.1"); wait <= 0 {
		t.Error("a check past MaxPending reserved")
	}
	dones[0](false)
	if done, wait := l.Attempt("10.0.2.1"); wait > 0 {
		t.Error("a check refused after one ended")
	} else {
		done(false)
	}
	// Addresses with checks under way are not forgotten to make room.
	for i := range MaxAddresses + 10 {
		l.Failed(fmt.Sprintf("10.9.%d.%d", i/256, i%256))
	}
	for _, done := range dones[1:] {
		done(true)
	}
	if n := l.Addresses(); n > MaxAddresses {
		t.Errorf("%d addresses kept", n)
	}
	l.mu.Lock()
	pending := l.pending
	l.mu.Unlock()
	if pending != 0 {
		t.Errorf("%d checks still reserved", pending)
	}
}

// TestAttemptGlobalSlow: past MaxGlobalFailures failures from everywhere,
// each address under MaxFailures, logins are checked one per SlowInterval,
// until a window passes with fewer failures.
func TestAttemptGlobalSlow(t *testing.T) {
	l, now := newLogins(t)
	for i := range MaxGlobalFailures {
		l.Failed(fmt.Sprintf("10.3.%d.%d", i/256, i%256))
	}
	done, wait := l.Attempt("10.4.0.1")
	if wait > 0 {
		t.Fatal("the first slowed login is refused")
	}
	done(false)
	if _, wait := l.Attempt("10.4.0.2"); wait <= 0 || wait > SlowInterval {
		t.Errorf("a second login within SlowInterval waits %v", wait)
	}
	*now = now.Add(SlowInterval)
	if done, wait := l.Attempt("10.4.0.2"); wait > 0 {
		t.Error("a login after SlowInterval is refused")
	} else {
		done(false)
	}
	// The next window still remembers the last one's failures.
	*now = now.Add(GlobalWindow + time.Second)
	done, wait = l.Attempt("10.4.0.3")
	if wait > 0 {
		t.Fatal("refused")
	}
	done(false)
	if _, wait := l.Attempt("10.4.0.4"); wait <= 0 {
		t.Error("not slowed in the window after the failures")
	}
	// A window with few failures, and logins go at their pace again.
	*now = now.Add(2*GlobalWindow + time.Second)
	for i := range 3 {
		done, wait := l.Attempt(fmt.Sprintf("10.5.0.%d", i))
		if wait > 0 {
			t.Fatalf("login %d slowed after the failures stopped", i)
		}
		done(false)
	}
}

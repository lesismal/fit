package login

import (
	"strconv"
	"testing"
	"time"
)

func TestKey(t *testing.T) {
	for _, tc := range []struct{ addr, want string }{
		{"10.0.0.1", "10.0.0.1"},
		{"::ffff:10.0.0.1", "10.0.0.1"},
		{"2001:db8:1:2:3:4:5:6", "2001:db8:1:2::/64"},
		{"2001:db8:1:2:ffff::1", "2001:db8:1:2::/64"},
		{"fe80::1%eth0", "fe80::/64"},
		{"@", "@"},
		{"", ""},
	} {
		if got := Key(tc.addr); got != tc.want {
			t.Errorf("Key(%q) = %q, want %q", tc.addr, got, tc.want)
		}
	}
}

// TestAdmitOneAtATime: a second check from an address, or from its IPv6
// /64, is refused while the first is under way, so that parallel guesses
// cannot all pass the lockout before the first failure is counted.
func TestAdmitOneAtATime(t *testing.T) {
	l, _ := newLogins(t)
	a, wait, busy := l.Admit("2001:db8::1")
	if a == nil || wait != 0 || busy {
		t.Fatalf("first check: %v %v %v", a, wait, busy)
	}
	for _, addr := range []string{"2001:db8::1", "2001:db8::ffff:1"} {
		if t2, wait, busy := l.Admit(addr); t2 != nil || !busy || wait <= 0 {
			t.Errorf("%s while a check is under way: %v %v %v", addr, t2, wait, busy)
		}
	}
	if b, _, _ := l.Admit("2001:db8:0:1::1"); b == nil {
		t.Error("another /64 is refused")
	} else {
		b.Done(false)
	}
	a.Done(true)
	a.Done(false) // only the first counts
	if b, _, _ := l.Admit("2001:db8::2"); b == nil {
		t.Error("a check after the first ended is refused")
	} else {
		b.Done(false)
	}
	if l.Addresses() != 1 {
		t.Errorf("%d addresses kept, want the one that failed", l.Addresses())
	}
}

// TestAdmitAttack: guesses fired in parallel from one address, then from
// every address of its /64, are checked one at a time, and the address is
// locked out after MaxFailures of them, not after the queue's worth.
func TestAdmitAttack(t *testing.T) {
	l, _ := newLogins(t)
	checked := 0
	for i := range 100 {
		var held []*Ticket
		for j := range 10 {
			if tk, _, _ := l.Admit("2001:db8::" + strconv.Itoa(i*10+j+1)); tk != nil {
				held = append(held, tk)
			}
		}
		for _, tk := range held {
			checked++
			if tk.LockedOut() > 0 {
				t.Fatal("a check admitted while locked out")
			}
			tk.Done(true)
		}
	}
	if checked != MaxFailures {
		t.Errorf("%d guesses checked, want %d", checked, MaxFailures)
	}
	if l.LockedOut("2001:db8::9999") <= 0 {
		t.Error("the /64 is not locked out")
	}
	if tk, wait, busy := l.Admit("2001:db8::1"); tk != nil || busy || wait <= 0 {
		t.Errorf("a locked-out address: %v %v %v", tk, wait, busy)
	}
}

// TestAdmitFullTable: filling the table with other addresses never ends a
// lockout early; a table full of lockouts refuses new addresses until the
// first ends.
func TestAdmitFullTable(t *testing.T) {
	l, _ := newLogins(t)
	for range MaxFailures {
		l.Failed("10.9.9.9")
	}
	for i := range MaxAddresses + 10 {
		l.Failed("10.0." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
	}
	if l.Addresses() > MaxAddresses {
		t.Errorf("%d addresses kept", l.Addresses())
	}
	if l.LockedOut("10.9.9.9") <= 0 {
		t.Fatal("eviction ended a lockout")
	}

	// Every address locked out: a new one is refused, a while.
	l2, now2 := newLogins(t)
	for i := range MaxAddresses {
		for range MaxFailures {
			l2.Failed("10.1." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
		}
	}
	tk, wait, busy := l2.Admit("10.2.0.1")
	if tk != nil || busy || wait <= 0 || wait > Lockout {
		t.Fatalf("a new address with the table full of lockouts: %v %v %v", tk, wait, busy)
	}
	if tk, _, _ := l2.Admit("10.2.0.2"); tk != nil {
		t.Fatal("a second new address is admitted")
	}
	*now2 = now2.Add(Lockout + time.Second)
	if tk, _, _ := l2.Admit("10.2.0.1"); tk == nil {
		t.Error("a new address is refused once the lockouts ended")
	} else {
		tk.Done(false)
	}

	// Addresses being checked are not evicted either.
	l3, _ := newLogins(t)
	var held []*Ticket
	for i := range MaxAddresses {
		tk, _, _ := l3.Admit("10.3." + strconv.Itoa(i/256) + "." + strconv.Itoa(i%256))
		if tk == nil {
			t.Fatal("refused before the table is full")
		}
		held = append(held, tk)
	}
	if tk, wait, _ := l3.Admit("10.4.0.1"); tk != nil || wait <= 0 {
		t.Error("a table full of checks under way admits another address")
	}
	held[0].Done(false)
	if tk, _, _ := l3.Admit("10.4.0.1"); tk == nil {
		t.Error("refused once a check ended")
	}
}

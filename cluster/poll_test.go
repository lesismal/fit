package cluster

import (
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certpin"
)

// TestPollStates adds a member by password over http and one by token over
// https with its CA, and reads both members' states.
func TestPollStates(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	byPassword, byToken := newFakeMember(t, false), newFakeMember(t, true)
	f.add(consoleInput("a", byPassword.srv.URL))
	in := tokenInput("b", byToken.srv.URL)
	in.TLS.CA = byToken.caPEM()
	in.Labels = map[string]string{"region": "eu"}
	f.add(in)

	for _, name := range []string{"a", "b"} {
		st := f.waitUp(name)
		if st.Node == nil || st.Node.FitVersion != "v1.2.3" || st.Config == nil || st.Config.Version != 7 || st.Config.ETag != `"abc"` {
			t.Fatalf("%s: %+v", name, st)
		}
		if st.Reloads == nil || st.Reloads.Succeeded != 3 || st.Reloads.Failed != 1 || st.Connections.Open != 5 {
			t.Errorf("%s: reloads and connections %+v %+v", name, st.Reloads, st.Connections)
		}
		if st.Upstreams.BackendsDown != 1 || st.Upstreams.Backends != 2 || st.Upstreams.Count != 1 {
			t.Errorf("%s: upstreams %+v", name, st.Upstreams)
		}
		if st.Certificates.Count != 2 || st.Certificates.NearestNames[0] != "soon.example" || st.Certificates.NearestExpiry.Year() != 2027 {
			t.Errorf("%s: certificates %+v", name, st.Certificates)
		}
		if st.Drift == nil || st.Drift.Count != 2 || st.Drift.Info != 1 {
			t.Errorf("%s: drift %+v", name, st.Drift)
		}
		if !st.Reachable || st.Error != nil || st.Seen.IsZero() || st.Failures != 0 {
			t.Errorf("%s: %+v", name, st)
		}
	}
	if st := f.state("b"); st.TLS == nil || st.TLS.Verified != "ca" || st.Labels["region"] != "eu" || st.Access != AccessAdminAPI {
		t.Errorf("b: %+v %+v", st, st.TLS)
	}
	if st := f.state("a"); st.TLS != nil {
		t.Errorf("a over http has no certificate: %+v", st.TLS)
	}
	// The console is logged in to once, and the session kept.
	time.Sleep(200 * time.Millisecond)
	if logins, _, _ := byPassword.counts(); logins != 1 {
		t.Errorf("logins: %d", logins)
	}
	if strings.Contains(f.log.String(), fakePassword) || strings.Contains(f.log.String(), fakeToken) {
		t.Errorf("a credential in the log: %s", f.log.String())
	}
	if !strings.Contains(f.log.String(), `member "a" is up`) {
		t.Errorf("log: %s", f.log.String())
	}
}

// TestFailuresApart reports each way a member fails as its own kind.
func TestFailuresApart(t *testing.T) {
	f := newFixture(t, config.Cluster{Timeout: config.Duration(300 * time.Millisecond), MaxResponseBytes: 1 << 10})
	plain, secure := newFakeMember(t, false), newFakeMember(t, true)
	otherCA, _, err := certpin.SelfSigned("another CA", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}

	wrong := consoleInput("wrong-password", plain.srv.URL)
	wrong.Password = "nope-nope"
	f.add(wrong)
	badToken := tokenInput("wrong-token", plain.srv.URL)
	badToken.Token = "not-the-token"
	f.add(badToken)
	f.add(tokenInput("untrusted", secure.srv.URL))
	pinned := tokenInput("pin", secure.srv.URL)
	pinned.TLS.Fingerprint = strings.Repeat("ab", 32)
	f.add(pinned)
	wrongCA := tokenInput("wrong-ca", secure.srv.URL)
	wrongCA.TLS.CA = string(otherCA)
	f.add(wrongCA)
	f.add(tokenInput("down", downURL(t)))

	st := f.waitKind("wrong-password", KindAuth)
	if st.Status != StatusError || !st.Reachable || !strings.Contains(st.Error.Message, "not tried again") {
		t.Errorf("wrong password: %+v %+v", st, st.Error)
	}
	f.waitKind("wrong-token", KindAuth)
	st = f.waitKind("untrusted", KindTLSUntrusted)
	if st.Error.Certificate == nil || st.Error.Certificate.Fingerprint == "" || st.TLS == nil || st.TLS.Verified != "" {
		t.Errorf("untrusted: %+v", st.Error)
	}
	st = f.waitKind("pin", KindTLSPin)
	if !strings.Contains(st.Error.Message, "not the one pinned") {
		t.Errorf("pin: %+v", st.Error)
	}
	f.waitKind("wrong-ca", KindTLSUntrusted)
	st = f.waitKind("down", KindUnreachable)
	if st.Status != StatusDown || st.Reachable {
		t.Errorf("down: %+v", st)
	}

	// A wrong password is not sent again while its backoff lasts.
	time.Sleep(300 * time.Millisecond)
	if logins, failed, _ := plain.counts(); logins != 1 || failed != 1 {
		t.Errorf("logins %d, failed %d", logins, failed)
	}

	// Pinning the right fingerprint trusts the member.
	sum := secure.srv.Certificate()
	pinned.TLS.Fingerprint = certInfo(sum).Fingerprint
	if _, err := f.m.Update("tester", "pin", pinned); err != nil {
		t.Fatal(err)
	}
	if st := f.waitUp("pin"); st.TLS.Verified != "pin" {
		t.Errorf("pinned: %+v", st.TLS)
	}

	// A member slower than the timeout; one whose answer is too large.
	slow := newFakeMember(t, false)
	slow.set(func(f *fakeMember) { f.delay = 2 * time.Second })
	f.add(tokenInput("slow", slow.srv.URL))
	st = f.waitKind("slow", KindTimeout)
	if st.Status != StatusDown {
		t.Errorf("slow: %+v", st)
	}
	big := newFakeMember(t, false)
	big.set(func(f *fakeMember) { f.statusPad = 4 << 10 })
	f.add(tokenInput("big", big.srv.URL))
	f.waitKind("big", KindTooLarge)
}

// TestConsoleAnswers covers what a console answers besides its state: a
// password to change, logins rate-limited, a fit without /node, and answers
// that are not JSON.
func TestConsoleAnswers(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	temp := newFakeMember(t, false)
	temp.set(func(f *fakeMember) { f.mustChange = true })
	f.add(consoleInput("temp", temp.srv.URL))
	st := f.waitKind("temp", KindPasswordChange)
	if !st.MustChangePassword {
		t.Errorf("temp: %+v", st)
	}
	// The session is logged out at once, and the login not tried again
	// while its backoff lasts.
	waitFor(t, "the logout", func() bool { _, _, out := temp.counts(); return out == 1 })
	time.Sleep(200 * time.Millisecond)
	if logins, _, _ := temp.counts(); logins != 1 {
		t.Errorf("logins: %d", logins)
	}

	limited := newFakeMember(t, false)
	limited.set(func(f *fakeMember) { f.rateLimit = 30 })
	f.add(consoleInput("limited", limited.srv.URL))
	if st := f.waitKind("limited", KindRateLimited); st.Error.RetryAfter != 30 || !strings.Contains(st.Error.Message, "too many failed logins from this address") {
		t.Errorf("limited: %+v", st.Error)
	}
	quiet := newFakeMember(t, false)
	quiet.set(func(f *fakeMember) { f.rateLimit = 7 })
	f.add(consoleInput("quiet", quiet.srv.URL))
	if st := f.waitKind("quiet", KindRateLimited); st.Error.RetryAfter != 7 || !strings.Contains(st.Error.Message, "too many logins") {
		t.Errorf("quiet: %+v", st.Error)
	}

	old := newFakeMember(t, false)
	old.set(func(f *fakeMember) { f.noNode = true })
	f.add(tokenInput("old", old.srv.URL))
	f.waitKind("old", KindUnsupported)

	garbled := newFakeMember(t, false)
	garbled.set(func(f *fakeMember) { f.badJSON = true })
	f.add(tokenInput("garbled", garbled.srv.URL))
	f.waitKind("garbled", KindBadAnswer)
}

// TestRelogin logs in again when the member restarted and forgot the
// session, and recovers a member that comes back.
func TestRelogin(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	m := newFakeMember(t, false)
	f.add(consoleInput("a", m.srv.URL))
	f.waitUp("a")
	m.restart()
	waitFor(t, "a second login", func() bool { logins, _, _ := m.counts(); return logins == 2 })
	if st := f.waitUp("a"); st.Failures != 0 {
		t.Errorf("%+v", st)
	}

	// A member that goes down, and comes back.
	addr := m.srv.Listener.Addr().String()
	m.srv.CloseClientConnections()
	m.srv.Listener.Close()
	f.waitKind("a", KindUnreachable)
	back := newFakeMemberAt(t, addr)
	if back == nil {
		t.Skip("the address was taken meanwhile")
	}
	f.waitUp("a")
	if logins, _, _ := back.counts(); logins != 1 {
		t.Errorf("logins after the restart: %d", logins)
	}
}

// TestConcurrencyCap polls many members, no more of them at once than
// max_concurrency.
func TestConcurrencyCap(t *testing.T) {
	f := newFixture(t, config.Cluster{MaxConcurrency: 3})
	m := newFakeMember(t, false)
	m.set(func(f *fakeMember) { f.delay = 30 * time.Millisecond })
	names := []string{}
	for i := range 12 {
		name := "n" + string(rune('a'+i))
		names = append(names, name)
		f.add(tokenInput(name, m.srv.URL))
	}
	for _, name := range names {
		f.waitUp(name)
	}
	if got := m.maxInflight.Load(); got > 3 || got < 1 {
		t.Errorf("at most %d calls at once, want 1 to 3", got)
	}
}

// TestManagerRestart closes a manager, logging its sessions out, and starts
// another on the same store, which finds the members and polls them.
func TestManagerRestart(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	m := newFakeMember(t, false)
	f.add(consoleInput("a", m.srv.URL))
	f.waitUp("a")
	f.m.Close()
	if _, _, out := m.counts(); out != 1 {
		t.Errorf("logouts: %d", out)
	}
	f.start()
	f.waitUp("a")
	if logins, _, _ := m.counts(); logins != 2 {
		t.Errorf("logins: %d", logins)
	}
	if got := f.m.Members(); len(got) != 1 || !got[0].PasswordSet {
		t.Errorf("%+v", got)
	}
}

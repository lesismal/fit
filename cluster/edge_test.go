package cluster

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
)

// TestAnswers covers the answers of a member the poller reads apart: a
// member that lists no status nor check, one whose answers are refused or
// broken, and logins answered strangely.
func TestAnswers(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	cases := []struct {
		name, kind string
		console    bool
		set        func(f *fakeMember)
	}{
		{"forbidden", KindForbidden, false, func(f *fakeMember) { f.nodeStatus = 403 }},
		{"broken", KindStatus, false, func(f *fakeMember) { f.nodeStatus = 500 }},
		{"rejected", KindAuth, true, func(f *fakeMember) { f.reject = true }},
		{"no-login", KindUnsupported, true, func(f *fakeMember) { f.loginStatus = 404 }},
		{"login-broken", KindStatus, true, func(f *fakeMember) { f.loginStatus = 502 }},
		{"no-csrf", KindBadAnswer, true, func(f *fakeMember) { f.noCSRF = true }},
		{"expired", KindAuth, true, func(f *fakeMember) { f.expired = true }},
	}
	for _, c := range cases {
		m := newFakeMember(t, false)
		m.set(c.set)
		in := tokenInput(c.name, m.srv.URL)
		if c.console {
			in = consoleInput(c.name, m.srv.URL)
		}
		f.add(in)
		st := f.waitKind(c.name, c.kind)
		if strings.Contains(st.Error.Message, fakePassword) || strings.Contains(st.Error.Message, fakeToken) {
			t.Errorf("%s: a credential in %q", c.name, st.Error.Message)
		}
	}
	if st := f.state("expired"); !strings.Contains(st.Error.Message, "expired") {
		t.Errorf("%+v", st.Error)
	}
	if st := f.state("rejected"); st.Error.Status != 401 {
		t.Errorf("%+v", st.Error)
	}

	// A member that lists neither status nor config.check is read for its
	// node alone; one with no roles answers empty lists.
	bare := newFakeMember(t, false)
	bare.set(func(f *fakeMember) { f.noRoles = true })
	f.add(tokenInput("bare", bare.srv.URL))
	st := f.waitUp("bare")
	if st.Reloads != nil || st.Drift != nil || st.Node.Roles == nil || st.Node.Capabilities == nil {
		t.Errorf("%+v", st)
	}
	nocheck := newFakeMember(t, false)
	nocheck.set(func(f *fakeMember) { f.caps = []string{"node", "status"} })
	f.add(tokenInput("nocheck", nocheck.srv.URL))
	if st := f.waitUp("nocheck"); st.Reloads == nil || st.Drift != nil {
		t.Errorf("%+v", st)
	}
}

// TestTestResetsBackoff lets the poller log in at once after a test of the
// saved member worked, rather than wait out a rate limit's backoff.
func TestTestResetsBackoff(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	m := newFakeMember(t, false)
	m.set(func(f *fakeMember) { f.rateLimit = 600 })
	f.add(consoleInput("a", m.srv.URL))
	f.waitKind("a", KindRateLimited)
	m.set(func(f *fakeMember) { f.rateLimit = 0 })
	var res TestResult
	_, body, _ := s.do("POST", "/api/m/cluster/nodes/a/test", "")
	if err := json.Unmarshal([]byte(body), &res); err != nil || !res.OK {
		t.Fatalf("%s %v", body, err)
	}
	f.waitUp("a")
	// The test's session was logged out.
	waitFor(t, "the test's logout", func() bool { _, _, out := m.counts(); return out >= 1 })
}

// TestCloseWaitsLittle closes a manager whose member does not answer its
// logout, without waiting longer than closeWait.
func TestCloseWaitsLittle(t *testing.T) {
	saved := closeWait
	closeWait = 100 * time.Millisecond
	defer func() { closeWait = saved }()
	f := newFixture(t, config.Cluster{})
	m := newFakeMember(t, false)
	f.add(consoleInput("a", m.srv.URL))
	f.waitUp("a")
	m.set(func(f *fakeMember) { f.logoutDelay = 2 * time.Second })
	start := time.Now()
	f.m.Close()
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("Close took %s", d)
	}
}

// TestTransportClosed refuses a call once the transport is closed, and
// tells a TLS failure that is not the certificate's apart.
func TestTransportClosed(t *testing.T) {
	e := testEngine(t)
	mem, _ := tokenInput("a", "https://127.0.0.1:1").member(nil, time.Now())
	tr := newTransport(e, &mem, limits{timeout: time.Second, maxBytes: 1 << 10})
	tr.close()
	var got error
	tr.do("GET", "/node", nil, nil, func(_ *reply, err error) { got = err })
	if !errors.Is(got, errClosed) {
		t.Errorf("%v", got)
	}
	start := time.Now()
	for _, c := range []struct {
		err  error
		kind string
	}{
		{errors.New("tls: handshake failure"), KindTLS},
		{fibhttp.ErrResponseHeaderTooLarge, KindTooLarge},
		{fibhttp.ErrResponseHeaderTimeout, KindTimeout},
		{errors.New("dial tcp: connection refused"), KindUnreachable},
		{&certError{kind: KindTLSPin, msg: "x"}, KindTLSPin},
	} {
		if p := tr.failure(c.err, start); p.Kind != c.kind {
			t.Errorf("%v: %s", c.err, p.Kind)
		}
	}
}

// TestSessionChanges follows a member's console as it changes under the
// manager: it asks for its password to be changed, then restarts with
// another password; and one down before it is logged in to.
func TestSessionChanges(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	m := newFakeMember(t, false)
	f.add(consoleInput("a", m.srv.URL))
	f.waitUp("a")
	m.set(func(f *fakeMember) { f.mustChange = true })
	if st := f.waitKind("a", KindPasswordChange); !st.MustChangePassword {
		t.Errorf("%+v", st)
	}
	m.set(func(f *fakeMember) { f.mustChange, f.expired = false, true })
	m.restart()

	other := newFakeMember(t, false)
	f.add(consoleInput("b", other.srv.URL))
	f.waitUp("b")
	other.restart()
	other.set(func(f *fakeMember) { f.expired = true })
	f.waitKind("b", KindAuth)

	f.add(consoleInput("down", downURL(t)))
	f.waitKind("down", KindUnreachable)

	// A node removed logs in no more.
	n := newNode(f.m, Member{Name: "x", URL: "http://127.0.0.1:1", Access: AccessConsole})
	n.closed.Store(true)
	var prob *Problem
	n.login(func(p *Problem) { prob = p })
	if prob == nil || prob.Kind != KindUnreachable {
		t.Errorf("%+v", prob)
	}
	n.t.close()
}

// TestAuditBounded keeps the latest MaxAudit entries.
func TestAuditBounded(t *testing.T) {
	f := &fixture{t: t, engine: testEngine(t), store: newMemStore(), log: &logBuf{}, set: config.Cluster{PollInterval: config.Duration(time.Hour)}}
	d := document{Version: DocVersion}
	for i := range MaxAudit {
		d.Audit = append(d.Audit, AuditEntry{Action: "old", Detail: strconv.Itoa(i)})
	}
	f.store.sections[Section] = d.marshal()
	f.start()
	f.add(tokenInput("a", "http://127.0.0.1:1"))
	audit := f.m.Audit()
	if len(audit) != MaxAudit || audit[0].Detail != "1" || audit[len(audit)-1].Action != ActionAdd {
		t.Errorf("%d %+v %+v", len(audit), audit[0], audit[len(audit)-1])
	}
}

// TestTestAbandoned ends a test whose browser left, freeing its slot.
func TestTestAbandoned(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	s := f.serve()
	slow := newFakeMember(t, false)
	slow.set(func(f *fakeMember) { f.delay = time.Second })
	s.c.Timeout = 200 * time.Millisecond
	req, _ := http.NewRequest("POST", s.url+"/api/m/cluster/test", strings.NewReader(js(tokenInput("x", slow.srv.URL))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(console.CSRFHeader, s.csrf)
	if resp, err := s.c.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("the test answered before the browser left")
	}
	waitFor(t, "the test's slot", func() bool { return len(f.m.tests) == 0 })

	// A saved member that does not check is not tested.
	g := &fixture{t: t, engine: f.engine, store: newMemStore(), log: &logBuf{}, set: config.Cluster{PollInterval: config.Duration(time.Hour)}}
	g.store.sections[Section] = []byte(`{"members":[{"name":"bad","url":"ftp://x","access":"admin_api","token":"t"}]}`)
	g.start()
	if code, body, _ := g.serve().do("POST", "/api/m/cluster/nodes/bad/test", ""); code != 400 {
		t.Errorf("%d %s", code, body)
	}
}

// TestLoginShared: logins asked for together, while one is under way, wait
// for its answer rather than sending their own, so that a refusal's backoff
// holds for all of them (a read and a poll meeting on a member).
func TestLoginShared(t *testing.T) {
	f := newFixture(t, config.Cluster{})
	m := newFakeMember(t, false)
	m.set(func(f *fakeMember) { f.expired, f.delay = true, 200 * time.Millisecond })
	n := newNode(f.m, Member{Name: "x", URL: m.srv.URL, Access: AccessConsole, Username: fakeUser, Password: fakePassword})
	defer n.t.close()
	const callers = 4
	got := make(chan *Problem, callers)
	for range callers {
		n.login(func(p *Problem) { got <- p })
	}
	var probs []*Problem
	for range callers {
		select {
		case p := <-got:
			probs = append(probs, p)
		case <-time.After(10 * time.Second):
			t.Fatal("a login was never answered")
		}
	}
	for i, p := range probs {
		if p == nil || p.Kind != KindAuth {
			t.Fatalf("%d: %+v", i, p)
		}
		for _, q := range probs[:i] {
			if p == q {
				t.Fatal("two callers were given one Problem to change")
			}
		}
	}
	if logins, _, _ := m.counts(); logins != 1 {
		t.Errorf("%d logins for %d callers", logins, callers)
	}
	// The refusal's backoff holds: another asks nothing.
	n.login(func(p *Problem) { got <- p })
	if p := <-got; p == nil || p.Kind != KindAuth {
		t.Errorf("%+v", p)
	}
	if logins, _, _ := m.counts(); logins != 1 {
		t.Errorf("%d logins after the backoff", logins)
	}
}

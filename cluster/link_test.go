package cluster

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/pebbletest"
)

func init() {
	// Links that notice a dead peer, and link again, in a test's time; set
	// before any test runs, so that no goroutine reads them as they change.
	linkPing, linkDead = 200*time.Millisecond, 1500*time.Millisecond
	linkBackoffMin, linkBackoffMax = 20*time.Millisecond, 300*time.Millisecond
}

// clock is a manager's clock a test moves.
type clock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.off += d
	c.mu.Unlock()
}

// linkFixture is a manager that takes links at addr, with a clock of the
// test's.
func linkFixture(t *testing.T, addr string, set ...func(*config.Cluster)) (*fixture, *clock) {
	t.Helper()
	ck := &clock{}
	f := &fixture{t: t, engine: testEngine(t), store: newMemStore(), log: &logBuf{},
		set: config.Cluster{PollInterval: config.Duration(100 * time.Millisecond), Timeout: config.Duration(5 * time.Second), LinkAddress: addr}}
	for _, fn := range set {
		fn(&f.set)
	}
	f.startWith(ck.now)
	return f, ck
}

func (f *fixture) startWith(now func() time.Time) {
	f.t.Helper()
	m, err := New(Options{Engine: f.engine, Settings: &f.set, Store: f.store, Logf: f.log.logf, Now: now})
	if err != nil {
		f.t.Fatal(err)
	}
	f.m = m
	f.t.Cleanup(m.Close)
}

// linkedMember is a member's link, serving the fake's Admin API in process.
type linkedMember struct {
	l    *MemberLink
	f    *fakeMember
	keep Keeper
	log  *logBuf
	hits atomic.Int64
}

// fakeAPI is the fake's Admin API, as admin.LinkAPI serves it: no token
// asked for.
func (lm *linkedMember) api() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lm.hits.Add(1)
		r.Header.Set("Authorization", "Bearer "+fakeToken)
		lm.f.serve(w, r)
	})
}

func startLinked(t *testing.T, f *fixture, cm *config.ClusterMember, keep Keeper, api http.Handler) *linkedMember {
	t.Helper()
	lm := &linkedMember{f: newFakeMember(t, false), keep: keep, log: &logBuf{}}
	if api == nil {
		api = lm.api()
	}
	if keep == nil {
		lm.keep = MemoryKeeper()
	}
	l, err := StartMember(MemberOptions{Engine: f.engine, Settings: cm, API: api, Keeper: lm.keep,
		Node: MemberNode{ID: "n1", Hostname: "behind-nat", FitVersion: "v9"}, Logf: lm.log.logf})
	if err != nil {
		t.Fatal(err)
	}
	lm.l = l
	t.Cleanup(l.Close)
	return lm
}

func (lm *linkedMember) waitLinked(t *testing.T, want bool) MemberStatus {
	t.Helper()
	var st MemberStatus
	waitFor(t, "the member's link to be "+strconv.FormatBool(want), func() bool { st = lm.l.Status(); return st.Linked == want })
	return st
}

func (lm *linkedMember) waitStopped(t *testing.T) MemberStatus {
	t.Helper()
	var st MemberStatus
	waitFor(t, "the member to stop linking", func() bool { st = lm.l.Status(); return st.Stopped != "" })
	return st
}

func enrollIn(t *testing.T, f *fixture, name string) Enrollment {
	t.Helper()
	e, err := f.m.Enroll("tester", &EnrollInput{Name: name, Labels: map[string]string{"site": "nat"}}, "127.0.0.1:2020")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A member with no port open enrolls with its token, links out, and is
// polled, tested and called through its link as the others are over HTTP;
// its token is used, and refused when presented again.
func TestLinkEnrollAndManage(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	info := f.m.LinkInfo("manager.example:2020")
	if !info.Enabled || info.Fingerprint == "" || !strings.HasPrefix(info.URL, "wss://manager.example:") || !strings.HasSuffix(info.URL, config.ClusterLinkPath) {
		t.Fatalf("%+v", info)
	}
	e := enrollIn(t, f, "edge-1")
	if !strings.HasPrefix(e.Token, config.ClusterEnrollPrefix) || e.Config.Fingerprint != info.Fingerprint ||
		!strings.HasPrefix(e.ConfigLine, `"cluster_member": {"manager":"wss://127.0.0.1:`) || e.Node.Link == nil || !e.Node.Link.TokenWaiting {
		t.Fatalf("%+v", e)
	}
	// Waiting: not enrolled, not linked.
	st := f.waitKind("edge-1", KindNotLinked)
	if st.Link == nil || st.Link.Linked || st.Link.Enrolled || st.Link.EnrollBy.IsZero() || st.Status != StatusDown {
		t.Fatalf("%+v %+v", st, st.Link)
	}
	if strings.Contains(js(f.m.Members()), e.Token[len(config.ClusterEnrollPrefix)+17:]) {
		t.Fatal("the token's secret is in the members' answer")
	}

	keep := MemoryKeeper()
	lm := startLinked(t, f, e.Config, keep, nil)
	ms := lm.waitLinked(t, true)
	if ms.Name != "edge-1" {
		t.Fatalf("%+v", ms)
	}
	st = f.waitUp("edge-1")
	if st.Node == nil || st.Node.ID != lm.f.id || st.Drift == nil || st.Drift.Count != 2 || st.Upstreams == nil || st.Upstreams.BackendsDown != 1 ||
		st.Link == nil || !st.Link.Linked || !st.Link.Enrolled || !st.Link.EnrollBy.IsZero() || st.Link.Host != "behind-nat" || st.TLS != nil {
		t.Fatalf("%+v %+v", st, st.Link)
	}
	data, _ := keep.Load()
	var k kept
	if json.Unmarshal(data, &k) != nil || !strings.HasPrefix(k.Credential, credentialPrefix) || k.Name != "edge-1" || k.Fingerprint != info.Fingerprint {
		t.Fatalf("kept %s", data)
	}
	v := f.m.Members()[0]
	if v.Link == nil || !v.Link.CredentialSet || v.Link.TokenWaiting || v.URL != "" || v.Access != AccessLink {
		t.Fatalf("%+v", v)
	}
	if doc := string(f.store.Section(Section)); strings.Contains(doc, k.Credential[len(credentialPrefix)+17:]) || !strings.Contains(doc, `"credential_hash"`) {
		t.Fatalf("the store keeps %s", doc)
	}
	if a := f.m.Audit(); a[len(a)-1].Action != ActionEnrolled || a[len(a)-1].Node != "edge-1" {
		t.Fatalf("%+v", a)
	}

	// Call reaches its Admin API over the link, as I143's pass-through
	// will.
	got := make(chan *Reply, 1)
	if err := f.m.Call("edge-1", "GET", "/config/check", http.Header{"X-Test": {"1"}, "Authorization": {"Bearer stolen"}}, nil, func(r *Reply, p *Problem) {
		if p != nil {
			t.Error(p)
		}
		got <- r
	}); err != nil {
		t.Fatal(err)
	}
	if r := <-got; r == nil || r.Status != 200 || !strings.Contains(string(r.Body), `"drift":2`) {
		t.Fatalf("%+v", r)
	}
	if err := f.m.Call("nobody", "GET", "/node", nil, nil, nil); err == nil {
		t.Fatal("a call to no member")
	}

	// A test of the member goes over its link too.
	s := f.serve()
	code, body, _ := s.do("POST", "/api/m/cluster/nodes/edge-1/test", "")
	if code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatalf("%d %s", code, body)
	}

	// The token was used: presented again, by another fit, it is refused.
	other := startLinked(t, f, e.Config, nil, nil)
	ost := other.waitStopped(t)
	if !strings.Contains(ost.Stopped, "refused the enrollment token") || !strings.Contains(other.log.String(), "("+ReasonUsed+")") {
		t.Fatalf("%+v\n%s", ost, other.log)
	}
	if lm.l.Status().Name != "edge-1" || !lm.l.Status().Linked {
		t.Fatal("the member's link was taken by a refused one")
	}
	if st := f.state("edge-1"); st.Link.Refused == nil || st.Link.Refused.Reason != ReasonUsed {
		t.Fatalf("%+v", st.Link)
	}

	// Its name and labels change; its link stays.
	if _, err := f.m.Update("tester", "edge-1", &Input{Name: "edge-one", Labels: map[string]string{"site": "b"}}); err != nil {
		t.Fatal(err)
	}
	f.waitState("edge-one", "linked again under its name", func(s State) bool { return s.Status == StatusUp && s.Link != nil && s.Link.Linked })
	if _, err := f.m.Update("tester", "edge-one", &Input{Access: AccessConsole, URL: "http://127.0.0.1:1", Username: "u", Password: "p"}); err != nil {
		t.Fatal(err)
	}
	// Made a member reached at its URL, its credential is gone: the link
	// is revoked, and the member forgets it.
	lm.waitStopped(t)
	if data, _ := keep.Load(); data != nil {
		t.Fatalf("the member keeps %s", data)
	}
}

// A member removed on the manager loses its credential: its link is
// revoked, it forgets the credential, and no link with it is taken again.
func TestLinkRemoveRevokes(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	e := enrollIn(t, f, "gone")
	keep := MemoryKeeper()
	lm := startLinked(t, f, e.Config, keep, nil)
	lm.waitLinked(t, true)
	data, _ := keep.Load()
	if err := f.m.Remove("tester", "gone"); err != nil {
		t.Fatal(err)
	}
	st := lm.waitStopped(t)
	if !strings.Contains(lm.log.String(), "revoked this fit's credential") || st.Linked {
		t.Fatalf("%+v\n%s", st, lm.log)
	}
	if d, _ := keep.Load(); d != nil {
		t.Fatalf("still kept: %s", d)
	}
	// The credential, kept elsewhere, is refused.
	again := MemoryKeeper()
	_ = again.Save(data)
	cm := *e.Config
	cm.Token = ""
	lm2 := startLinked(t, f, &cm, again, nil)
	st = lm2.waitStopped(t)
	if !strings.Contains(lm2.log.String(), "("+ReasonUnknown+")") || !strings.Contains(st.Stopped, "no credential") {
		t.Fatalf("%+v\n%s", st, lm2.log)
	}
}

// An expired token is refused, and so is one replaced by another.
func TestLinkTokenExpiredOrReplaced(t *testing.T) {
	f, ck := linkFixture(t, "127.0.0.1:0")
	e, err := f.m.Enroll("tester", &EnrollInput{Name: "late", TTL: "2m"}, "")
	if err != nil {
		t.Fatal(err)
	}
	ck.add(3 * time.Minute)
	lm := startLinked(t, f, e.Config, nil, nil)
	st := lm.waitStopped(t)
	if !strings.Contains(lm.log.String(), "("+ReasonExpired+")") || st.Linked {
		t.Fatalf("%+v\n%s", st, lm.log)
	}
	f.waitState("late", "to show the refusal", func(s State) bool { return s.Link != nil && s.Link.Refused != nil && s.Link.Refused.Reason == ReasonExpired })

	// A new token for it, and the old one is no member's.
	e2, err := f.m.EnrollAgain("tester", "late", &EnrollInput{TTL: "1h", URL: "wss://example.com:9/cluster/link"}, "")
	if err != nil || e2.Config.Manager != "wss://example.com:9/cluster/link" {
		t.Fatal(err, e2.Config)
	}
	cm := *e2.Config
	cm.Manager = e.Config.Manager
	old := startLinked(t, f, e.Config, nil, nil)
	old.waitStopped(t)
	if !strings.Contains(old.log.String(), "("+ReasonUnknown+")") {
		t.Fatal(old.log)
	}
	lm2 := startLinked(t, f, &cm, nil, nil)
	lm2.waitLinked(t, true)
	f.waitUp("late")
}

// Enrolled again, a member links with its new credential, and the link of
// its old one is revoked.
func TestLinkEnrollAgain(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	e := enrollIn(t, f, "moved")
	first := startLinked(t, f, e.Config, nil, nil)
	first.waitLinked(t, true)
	e2, err := f.m.EnrollAgain("tester", "moved", &EnrollInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	// Until the new token is used, the old credential still links.
	if st := f.waitUp("moved"); !st.Link.Enrolled || st.Link.EnrollBy.IsZero() {
		t.Fatalf("%+v", st.Link)
	}
	second := startLinked(t, f, e2.Config, nil, nil)
	second.waitLinked(t, true)
	first.waitStopped(t)
	f.waitState("moved", "up through the new link", func(s State) bool { return s.Status == StatusUp && s.Link.Linked && s.Link.EnrollBy.IsZero() })
}

// The link survives the manager's restart: the member links again once the
// manager is back, with the credential the store kept.
func TestLinkManagerRestart(t *testing.T) {
	addr := "127.0.0.1:" + strconv.Itoa(pebbletest.FreePort(t))
	f, ck := linkFixture(t, addr)
	fp := f.m.LinkInfo("").Fingerprint
	e := enrollIn(t, f, "steady")
	lm := startLinked(t, f, e.Config, nil, nil)
	lm.waitLinked(t, true)
	f.waitUp("steady")
	f.m.Close()
	lm.waitLinked(t, false)
	f.startWith(ck.now)
	if f.m.LinkInfo("").Fingerprint != fp {
		t.Fatal("the restarted manager's certificate is another")
	}
	lm.waitLinked(t, true)
	f.waitUp("steady")
	if strings.Contains(lm.log.String(), "enrolled with") && strings.Count(lm.log.String(), "enrolled with") != 1 {
		t.Fatal(lm.log)
	}
}

// cutProxy is a network between a member and its manager that a test cuts,
// or that drops what it carries.
type cutProxy struct {
	ln   net.Listener
	to   string
	drop atomic.Bool
	mu   sync.Mutex
	open []net.Conn
}

func newCutProxy(t *testing.T, to string) *cutProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &cutProxy{ln: ln, to: to}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", to)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.open = append(p.open, c, u)
			p.mu.Unlock()
			go p.pipe(c, u)
			go p.pipe(u, c)
		}
	}()
	t.Cleanup(func() { ln.Close(); p.cut() })
	return p
}

func (p *cutProxy) pipe(from, to net.Conn) {
	defer to.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := from.Read(buf)
		if n > 0 && !p.drop.Load() {
			if _, werr := to.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// cut closes every connection it carries.
func (p *cutProxy) cut() {
	p.mu.Lock()
	open := p.open
	p.open = nil
	p.mu.Unlock()
	for _, c := range open {
		c.Close()
	}
}

// The link survives a network cut, and a network that drops what it
// carries without closing anything, which the pings find.
func TestLinkNetworkCut(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	e := enrollIn(t, f, "far")
	addr := strings.TrimPrefix(strings.TrimSuffix(e.Config.Manager, config.ClusterLinkPath), "wss://")
	p := newCutProxy(t, addr)
	cm := *e.Config
	cm.Manager = "wss://" + p.ln.Addr().String() + config.ClusterLinkPath
	lm := startLinked(t, f, &cm, nil, nil)
	lm.waitLinked(t, true)
	f.waitUp("far")

	p.cut()
	lm.waitLinked(t, false)
	lm.waitLinked(t, true)
	f.waitUp("far")

	p.drop.Store(true)
	lm.waitLinked(t, false)
	f.waitKind("far", KindNotLinked)
	p.drop.Store(false)
	lm.waitLinked(t, true)
	f.waitUp("far")
}

// A member links only to the manager whose certificate it pinned, and
// never sends its token to another.
func TestLinkPin(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	e := enrollIn(t, f, "pinned")
	cm := *e.Config
	cm.Fingerprint = strings.Repeat("ab", 32)
	lm := startLinked(t, f, &cm, nil, nil)
	waitFor(t, "a refused certificate", func() bool { return strings.Contains(lm.log.String(), "is not the one pinned") })
	if lm.l.Status().Linked {
		t.Fatal("linked to a certificate not pinned")
	}
	if st := f.state("pinned"); st.Link.Enrolled {
		t.Fatal("the token reached the manager")
	}
}

// The link's calls are bounded: a request too large, an answer too large,
// a member that does not answer in time.
func TestLinkCallBounds(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0", func(c *config.Cluster) {
		c.Timeout = config.Duration(time.Second)
		c.MaxResponseBytes = 64 << 10
	})
	e := enrollIn(t, f, "bounded")
	release := make(chan struct{})
	var slow atomic.Bool
	lm := &linkedMember{}
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			_, _ = w.Write([]byte(strings.Repeat("x", int(f.m.lim.maxBytes)+1)))
		case "/slow":
			if slow.Load() {
				select {
				case <-release:
				case <-r.Context().Done():
				}
			}
			w.WriteHeader(204)
		case "/echo":
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", r.Header.Get("Content-Type"))
			_, _ = w.Write(body)
		default:
			lm.api().ServeHTTP(w, r)
		}
	})
	got := startLinked(t, f, e.Config, nil, api)
	lm.f = got.f
	got.waitLinked(t, true)
	f.waitUp("bounded")

	call := func(method, path string, body []byte) (*Reply, *Problem) {
		ch := make(chan struct {
			r *Reply
			p *Problem
		}, 1)
		if err := f.m.Call("bounded", method, path, http.Header{"Content-Type": {"text/plain"}}, body, func(r *Reply, p *Problem) {
			ch <- struct {
				r *Reply
				p *Problem
			}{r, p}
		}); err != nil {
			t.Fatal(err)
		}
		v := <-ch
		return v.r, v.p
	}
	if r, p := call("POST", "echo", []byte("hello")); p != nil || string(r.Body) != "hello" || r.Header.Get("Content-Type") != "text/plain" {
		t.Fatal(r, p)
	}
	if _, p := call("GET", "/big", nil); p == nil || p.Kind != KindTooLarge {
		t.Fatal(p)
	}
	if _, p := call("POST", "/echo", make([]byte, f.m.lim.maxBytes+1)); p == nil || p.Kind != KindTooLarge {
		t.Fatal(p)
	}
	// A member that does not answer in time: the manager gives up, and
	// tells the member, whose handler sees its request canceled.
	slow.Store(true)
	if _, p := call("GET", "/slow", nil); p == nil || p.Kind != KindTimeout {
		t.Fatal(p)
	}
	close(release)
}

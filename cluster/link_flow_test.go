package cluster

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
)

// within fails the test unless cond holds within d: what should be at once
// is not left to waitFor's patience.
func within(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("%s took longer than %s", what, d)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// deafLink is a link enrolled as the member name, by a raw WebSocket that
// reads nothing from now on, with fill calls of a MiB each waiting for it:
// enough that the link stays above its limit, the sockets full.
func deafLink(t *testing.T, f *fixture, name string, fill int) (r *rawLink, l *mlink, tr *linkTransport, results chan error) {
	t.Helper()
	e := enrollIn(t, f, name)
	tok, err := linktoken.ParseAs(e.Token, config.ClusterEnrollPrefix)
	if err != nil {
		t.Fatal(err)
	}
	r = dialRaw(t, f, e.Config.Manager)
	r.hello(t, modeEnroll, tok, nil)
	m := r.next(t)
	var w welcomeMsg
	if m[0] != msgWelcome || json.Unmarshal(m[1:], &w) != nil {
		t.Fatalf("%q", m)
	}
	cred, _ := linktoken.ParseAs(w.Credential, credentialPrefix)
	waitFor(t, "the link", func() bool { l = f.m.linkOf(cred.ID); return l != nil })
	r.ws.HoldReads(true)
	tr = newLinkTransport(f.m, cred.ID)
	results = make(chan error, maxLinkCalls)
	body := make([]byte, 1<<20)
	for range fill {
		l.call(tr, "POST", "/big", nil, body, func(_ *reply, err error) { results <- err })
	}
	// Above its limit, and staying there.
	for stable := 0; stable < 20; stable++ {
		within(t, 20*time.Second, "the link above its limit", l.flow.isAbove)
		time.Sleep(10 * time.Millisecond)
	}
	return r, l, tr, results
}

// A forced close of a link whose member reads nothing is immediate, its
// calls ended, however much waits for the member. A close with the close
// handshake would wait behind all of it.
func TestLinkForcedCloseOfDeafMember(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0", func(c *config.Cluster) {
		c.MaxResponseBytes = 2 << 20
		c.Timeout = config.Duration(time.Minute)
	})
	_, l, _, results := deafLink(t, f, "deaf", 12)
	// Linked, it is no longer among the connections that have not.
	lc := f.m.link.ln.conns
	lc.mu.Lock()
	pending, conns := lc.pending, len(lc.conns)
	lc.mu.Unlock()
	if pending != 0 || conns != 1 {
		t.Fatalf("%d of %d connections not linked", pending, conns)
	}
	l.closeNow(errors.New("the test's"))
	within(t, 2*time.Second, "the forced close", func() bool { l.mu.Lock(); defer l.mu.Unlock(); return l.state == linkClosed })
	within(t, 2*time.Second, "the manager's note of it", func() bool { return f.m.linkOf(l.credID) == nil })
	for range 12 {
		if err := <-results; !errors.Is(err, errLinkClosed) {
			t.Error(err)
		}
	}
}

// Above its limit, a link holds its new calls, in order, and sends them once
// the member reads again; one that times out waiting is never sent.
func TestLinkHoldsCalls(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0", func(c *config.Cluster) {
		c.MaxResponseBytes = 2 << 20
		c.Timeout = config.Duration(time.Minute)
	})
	r, l, tr, results := deafLink(t, f, "slow", 12)
	short := newLinkTransport(f.m, l.credID)
	short.lim.timeout = 50 * time.Millisecond
	expired := make(chan error, 1)
	l.mu.Lock()
	before := len(l.held)
	l.mu.Unlock()
	l.call(short, "GET", "/expired", nil, nil, func(_ *reply, err error) { expired <- err })
	l.call(tr, "GET", "/held", nil, nil, func(_ *reply, err error) { results <- err })
	l.mu.Lock()
	held := len(l.held) - before
	l.mu.Unlock()
	if held != 2 {
		t.Fatalf("%d calls held", held)
	}
	if err := <-expired; !errors.Is(err, errLinkTimeout) {
		t.Fatal(err)
	}
	// The member reads again: the call held goes out once the queue falls,
	// and the one that timed out does not.
	seen := make(chan string, 2)
	go func() {
		for m := range r.msgs {
			var head requestHead
			if m[0] == msgRequest {
				if _, _, err := decodeCall(m[1:], &head); err == nil && (head.Path == "/held" || head.Path == "/expired") {
					seen <- head.Path
				}
			}
		}
	}()
	r.ws.HoldReads(false)
	select {
	case p := <-seen:
		if p != "/held" {
			t.Fatalf("%q sent", p)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the call held was not sent")
	}
	within(t, 20*time.Second, "the queue's drain", func() bool { return !l.flow.isAbove() })
}

// A manager that reads nothing: the member's answers wait for it, past the
// link's limit, and once the member has not heard from the manager for
// linkDead it ends the link at once, however much waits. A close with the
// close handshake would wait behind all of it.
func TestMemberManagerReadsNothing(t *testing.T) {
	secret := linktoken.New()
	var nonce atomic.Pointer[[]byte]
	var mgr atomic.Pointer[websocket.Connection]
	url, fp, e := fakeManager(t, func(c *websocket.Connection) {
		n := linktoken.Nonce()
		nonce.Store(&n)
		_ = c.WriteBinary(encodeJSON(msgChallenge, challengeMsg{Nonce: n}))
	}, func(c *websocket.Connection, data []byte) {
		if data[0] != msgHello {
			return
		}
		var h helloMsg
		_ = json.Unmarshal(data[1:], &h)
		cred := linktoken.New().StringAs(credentialPrefix)
		proof := linktoken.ListenerProof(secret.Stored(), transcript(h.ID, h.Nonce, *nonce.Load(), h.Mode), "x", cred)
		_ = c.WriteBinary(encodeJSON(msgWelcome, welcomeMsg{Name: "x", Credential: cred, Proof: proof}))
		mgr.Store(c)
	})
	big := strings.Repeat("x", 1<<20)
	api := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(big)) })
	l, err := StartMember(MemberOptions{Engine: e, Settings: &config.ClusterMember{Manager: url, Fingerprint: fp, Token: secret.StringAs(config.ClusterEnrollPrefix)},
		API: api, MaxRequestBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	waitFor(t, "the link", func() bool { return l.Status().Linked && mgr.Load() != nil })
	// The manager asks for a MiB at a time, more than the member takes at
	// once, and reads nothing more: the member answers what it takes, and
	// busy the others.
	c := mgr.Load()
	c.HoldReads(true)
	start := time.Now()
	for i := range 2 * maxLinkCalls {
		_ = c.WriteBinary(encodeCall(msgRequest, uint64(i+1), requestHead{Method: "GET", Path: "/big", Timeout: 60000, Max: 2 << 20}, nil))
	}
	within(t, linkDead+3*time.Second, "the member's forced close", func() bool { return !l.Status().Linked })
	if !strings.Contains(l.Status().Err, errLinkDead.Error()) {
		t.Errorf("%s after %s", l.Status().Err, time.Since(start))
	}
}

// The listener takes links on the link's path only, and none a web page
// opens.
func TestLinkPathAndOrigin(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	url := f.m.LinkInfo("127.0.0.1").URL
	for _, c := range []struct {
		url    string
		header http.Header
		status int
	}{
		{strings.TrimSuffix(url, config.ClusterLinkPath) + "/elsewhere", nil, http.StatusNotFound},
		{url, http.Header{"Origin": {"https://evil.example"}}, http.StatusForbidden},
	} {
		cfg := websocket.DefaultDialerConfig()
		cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}} //nolint:gosec
		done := make(chan *http.Response, 1)
		websocket.NewDialer(f.engine, cfg).Dial(c.url, c.header, nil, func(ws *websocket.Connection, resp *http.Response, err error) {
			if err == nil {
				ws.Abort(nil)
			}
			done <- resp
		})
		if resp := <-done; resp == nil || resp.StatusCode != c.status {
			t.Errorf("%s %v: %+v", c.url, c.header, resp)
		}
	}
}

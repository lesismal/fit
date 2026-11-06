package cluster

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/internal/linktoken"
)

// rawLink is a WebSocket to a link listener that a test drives itself.
type rawLink struct {
	ws     *websocket.Connection
	msgs   chan []byte
	closed chan struct{}
}

func dialRaw(t *testing.T, f *fixture, url string) *rawLink {
	t.Helper()
	r := &rawLink{msgs: make(chan []byte, 64), closed: make(chan struct{})}
	cfg := websocket.DefaultDialerConfig()
	cfg.TLSConfig = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}} //nolint:gosec
	done := make(chan error, 1)
	websocket.NewDialer(f.engine, cfg).Dial(url, nil, websocket.HandlerFuncs{
		Message: func(_ *websocket.Connection, _ websocket.Opcode, data []byte) { r.msgs <- bytes.Clone(data) },
		Close:   func(*websocket.Connection, uint16, string, error) { close(r.closed) },
	}, func(c *websocket.Connection, _ *http.Response, err error) {
		r.ws = c
		done <- err
	})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *rawLink) next(t *testing.T) []byte {
	t.Helper()
	select {
	case m := <-r.msgs:
		return m
	case <-time.After(20 * time.Second):
		t.Fatal("no message")
		return nil
	}
}

func (r *rawLink) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-r.closed:
	case <-time.After(20 * time.Second):
		t.Fatal("the link stayed open")
	}
}

// refusal reads the refusal the link is answered with.
func (r *rawLink) refusal(t *testing.T) refuseMsg {
	t.Helper()
	m := r.next(t)
	var rm refuseMsg
	if m[0] != msgRefuse || json.Unmarshal(m[1:], &rm) != nil {
		t.Fatalf("not a refusal: %q", m)
	}
	r.waitClosed(t)
	return rm
}

// hello answers the link's challenge with a hello of secret in mode,
// changed by edit when not nil.
func (r *rawLink) hello(t *testing.T, mode string, secret linktoken.Secret, edit func(*helloMsg)) {
	t.Helper()
	c := r.next(t)
	var ch challengeMsg
	if c[0] != msgChallenge || json.Unmarshal(c[1:], &ch) != nil {
		t.Fatalf("not a challenge: %q", c)
	}
	nonce := linktoken.Nonce()
	h := helloMsg{Mode: mode, ID: secret.ID, Nonce: nonce, Proof: secret.Prove(transcript(secret.ID, nonce, ch.Nonce, mode))}
	if edit != nil {
		edit(&h)
	}
	if err := r.ws.WriteBinary(encodeJSON(msgHello, h)); err != nil {
		t.Fatal(err)
	}
}

// The listener refuses what is not a member's hello, and hellos of tokens
// and credentials it does not know or whose proofs do not check.
func TestLinkRefusals(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	e := enrollIn(t, f, "r1")
	url := e.Config.Manager
	tok, err := linktoken.ParseAs(e.Token, config.ClusterEnrollPrefix)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, reason string
		send         func(r *rawLink)
	}{
		{"not a hello", ReasonBadHello, func(r *rawLink) { r.next(t); _ = r.ws.WriteBinary([]byte("q")) }},
		{"bad json", ReasonBadHello, func(r *rawLink) { r.next(t); _ = r.ws.WriteBinary([]byte("h{")) }},
		{"bad mode", ReasonBadHello, func(r *rawLink) { r.hello(t, "other", tok, nil) }},
		{"unknown credential", ReasonUnknown, func(r *rawLink) { r.hello(t, modeLink, linktoken.New(), nil) }},
		{"unknown token", ReasonUnknown, func(r *rawLink) { r.hello(t, modeEnroll, linktoken.New(), nil) }},
		{"bad proof", ReasonProof, func(r *rawLink) {
			r.hello(t, modeEnroll, tok, func(h *helloMsg) { h.Proof = bytes.Repeat([]byte{1}, linktoken.ProofSize) })
		}},
	}
	for _, c := range cases {
		r := dialRaw(t, f, url)
		c.send(r)
		if got := r.refusal(t); got.Reason != c.reason {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	if !strings.Contains(f.log.String(), "was refused (proof)") {
		t.Error(f.log)
	}

	// A link that never says hello is closed.
	r := dialRaw(t, f, url)
	r.next(t)
	r.waitClosed(t)

	// A linked member's credential with a bad proof is refused, and a
	// link that sends what is not an answer is closed.
	lm := startLinked(t, f, e.Config, nil, nil)
	lm.waitLinked(t, true)
	data, _ := lm.keep.Load()
	var k kept
	_ = json.Unmarshal(data, &k)
	cred, _ := linktoken.ParseAs(k.Credential, credentialPrefix)
	r = dialRaw(t, f, url)
	r.hello(t, modeLink, cred, func(h *helloMsg) { h.Nonce = linktoken.Nonce() })
	if got := r.refusal(t); got.Reason != ReasonProof {
		t.Errorf("%+v", got)
	}
	r = dialRaw(t, f, url)
	r.hello(t, modeLink, cred, nil)
	if m := r.next(t); m[0] != msgWelcome {
		t.Fatalf("%q", m)
	}
	// It took the member's place: the member is told so, and links again.
	lm.waitLinked(t, false)
	_ = r.ws.WriteBinary([]byte("zz"))
	r.waitClosed(t)
	lm.waitLinked(t, true)
	if !strings.Contains(lm.log.String(), "("+ReasonReplaced+")") {
		t.Error(lm.log)
	}

	// Answers to calls given up on, or that say why there is none.
	r = dialRaw(t, f, url)
	r.hello(t, modeLink, cred, nil)
	r.next(t) // the welcome
	_ = r.ws.WriteBinary(encodeCall(msgResponse, 999, responseHead{Status: 200}, nil))
	for _, kind := range []string{answerBusy, answerTimeout} {
		ch := make(chan *Problem, 1)
		if err := f.m.Call("r1", "GET", "/x", nil, nil, func(_ *Reply, p *Problem) { ch <- p }); err != nil {
			t.Fatal(err)
		}
		var id uint64
		for {
			m := r.next(t)
			if m[0] == msgRequest {
				var head requestHead
				id, _, _ = decodeCall(m[1:], &head)
				if head.Path == "/x" {
					break
				}
			}
		}
		_ = r.ws.WriteBinary(encodeCall(msgResponse, id, responseHead{Error: kind}, nil))
		if p := <-ch; p == nil || kind == answerTimeout && p.Kind != KindTimeout || kind == answerBusy && p.Kind != KindUnreachable {
			t.Errorf("%s: %+v", kind, p)
		}
	}
	_ = r.ws.WriteBinary([]byte{msgResponse, 1})
	r.waitClosed(t)
}

// Too many connections that have not linked are closed as they open.
func TestLinkConnectionBound(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0", func(c *config.Cluster) { c.MaxNodes = 1 })
	addr := f.m.link.ln.addr.String()
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for range maxLinksSlack + 1 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	waitFor(t, "the listener to hold its bound", func() bool {
		f.m.link.ln.conns.mu.Lock()
		defer f.m.link.ln.conns.mu.Unlock()
		return len(f.m.link.ln.conns.conns) == maxLinksSlack+1
	})
	extra, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer extra.Close()
	_ = extra.SetReadDeadline(time.Now().Add(20 * time.Second))
	if _, err := extra.Read(make([]byte, 1)); err == nil {
		t.Fatal("one more connection was kept")
	}
	// The handlers' hooks pass on to what they wrap.
	h := &hooked{}
	lc := &linkConns{Handler: h}
	lc.OnShutdown(nil)
	lc.OnEngineClose(nil)
	lc.linked(nil)
	if h.n != 2 {
		t.Error(h.n)
	}
	(&linkConns{Handler: fib.HandlerFuncs{}}).OnShutdown(nil)
}

type hooked struct {
	fib.HandlerFuncs
	n int
}

func (h *hooked) OnShutdown(*fib.Connection)    { h.n++ }
func (h *hooked) OnEngineClose(*fib.Connection) { h.n++ }

// The enrollment's API and its refusals.
func TestEnrollAPI(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	s := f.serve()
	code, body, _ := s.do("GET", "/api/m/cluster/link", "")
	if code != 200 || !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, `"url":"wss://127.0.0.1:`) {
		t.Fatalf("%d %s", code, body)
	}
	code, body, _ = s.do("POST", "/api/m/cluster/enroll", `{"name":"api-1","ttl":"2h","url":"wss://m.example:7444/cluster/link"}`)
	if code != 201 || !strings.Contains(body, `"config_line":"\"cluster_member\": {\"manager\":\"wss://m.example:7444/cluster/link\"`) {
		t.Fatalf("%d %s", code, body)
	}
	for _, c := range []struct {
		path, body string
		code       int
		want       string
	}{
		{"/api/m/cluster/enroll", `{"name":"api-1"}`, 409, "already named"},
		{"/api/m/cluster/enroll", `{"name":"bad name!"}`, 400, `"field":"name"`},
		{"/api/m/cluster/enroll", `{"name":"x","ttl":"soon"}`, 400, `"field":"ttl"`},
		{"/api/m/cluster/enroll", `{"name":"x","ttl":"1s"}`, 400, "an enrollment token lasts"},
		{"/api/m/cluster/enroll", `{"name":"x","ttl":"90d"}`, 400, "an enrollment token lasts"},
		{"/api/m/cluster/enroll", `{"name":"x","url":"http://x"}`, 400, `"field":"url"`},
		{"/api/m/cluster/enroll", `{`, 400, "not the JSON"},
		{"/api/m/cluster/enroll", `{"name":"` + strings.Repeat("x", MaxBodyBytes) + `"}`, 413, "bytes at most"},
		{"/api/m/cluster/nodes/api-1/enroll", ``, 200, `"token":"fite1.`},
		{"/api/m/cluster/nodes/nobody/enroll", `{}`, 404, "no member"},
	} {
		code, body, _ := s.do("POST", c.path, c.body)
		if code != c.code || !strings.Contains(body, c.want) {
			t.Errorf("%s %s: %d %s", c.path, c.body, code, body)
		}
	}
	if code, body, _ := s.do("POST", "/api/m/cluster/enroll", `{"name":"y"}`, "If-Match", `"0"`); code != 412 {
		t.Errorf("%d %s", code, body)
	}
	// A member reached at its URL is not enrolled; a linked member's
	// update keeps its link, and it cannot be added by URL.
	m := newFakeMember(t, false)
	f.add(tokenInput("by-url", m.srv.URL))
	if code, body, _ := s.do("POST", "/api/m/cluster/nodes/by-url/enroll", `{}`); code != 409 {
		t.Errorf("%d %s", code, body)
	}
	if _, err := f.m.Add("u", &Input{Name: "z", Access: AccessLink}); err == nil || !strings.Contains(err.Error(), "added by enrollment") {
		t.Error(err)
	}
	if v, err := f.m.Update("u", "api-1", &Input{Labels: map[string]string{"k": "v"}}); err != nil || v.Access != AccessLink || v.Link == nil {
		t.Error(v, err)
	}
	if _, err := f.m.Update("u", "api-1", &Input{Access: AccessLink, URL: "https://x"}); err != nil {
		t.Error(err)
	}
	if v := f.m.Members(); v[0].URL != "" {
		t.Error(v[0])
	}
	if code, body, _ := s.do("POST", "/api/m/cluster/test", `{"name":"t","access":"link"}`); code != 400 {
		t.Errorf("%d %s", code, body)
	}

	// The members kept are max_nodes at most.
	g, _ := linkFixture(t, "127.0.0.1:0", func(c *config.Cluster) { c.MaxNodes = 1 })
	enrollIn(t, g, "one")
	if _, err := g.m.Enroll("u", &EnrollInput{Name: "two"}, ""); err == nil || !strings.Contains(err.Error(), "max_nodes") {
		t.Error(err)
	}
	// A manager that takes no links enrolls none.
	h := newFixture(t, config.Cluster{})
	if _, err := h.m.Enroll("u", &EnrollInput{Name: "x"}, ""); !errors.Is(err, errNoLinks) {
		t.Error(err)
	}
	if info := h.m.LinkInfo("h"); info.Enabled || info.MaxTTL == 0 {
		t.Errorf("%+v", info)
	}
	if _, err := h.m.EnrollAgain("u", "x", &EnrollInput{}, ""); !errors.Is(err, errNoLinks) {
		t.Error(err)
	}
	if _, err := f.m.EnrollAgain("u", "nobody", &EnrollInput{TTL: "x"}, ""); err == nil {
		t.Error("a bad ttl")
	}
}

// The listener's certificate is made once and kept; a manager whose
// document cannot be written makes one for as long as it runs, and one
// whose store fails, or whose address is taken, does not start.
func TestLinkCertificateKept(t *testing.T) {
	f, ck := linkFixture(t, "127.0.0.1:0")
	fp := f.m.LinkInfo("").Fingerprint
	f.m.Close()
	f.startWith(ck.now)
	if f.m.LinkInfo("[::1]").Fingerprint != fp || !strings.HasPrefix(f.m.LinkInfo("[::1]").URL, "wss://[::1]:") {
		t.Fatal(f.m.LinkInfo("[::1]"))
	}
	f.m.Close()

	// link_url names the certificate's host.
	g, _ := linkFixture(t, "127.0.0.1:0", func(c *config.Cluster) { c.LinkURL = "wss://manager.example:9/cluster/link" })
	if info := g.m.LinkInfo("x"); info.URL != "wss://manager.example:9/cluster/link" {
		t.Error(info)
	}
	cert, _ := tls.X509KeyPair([]byte(g.m.doc.LinkCert.Cert), []byte(g.m.doc.LinkCert.Key))
	if leaf, _ := certLeaf(cert); leaf == nil || leaf.Subject.CommonName != "manager.example" {
		t.Error(leaf)
	}

	// Read-only: a newer fit's document.
	st := newMemStore()
	_ = st.SetSection(Section, []byte(`{"version":99,"members":[]}`))
	ro := &fixture{t: t, engine: testEngine(t), store: st, log: &logBuf{}, set: config.Cluster{LinkAddress: "127.0.0.1:0"}}
	ro.start()
	if !ro.m.LinkInfo("").Enabled || !strings.Contains(ro.log.String(), "cannot be kept") {
		t.Error(ro.log)
	}

	// The store fails.
	bad := newMemStore()
	bad.fail = errors.New("disk full")
	if _, err := New(Options{Engine: ro.engine, Settings: &config.Cluster{LinkAddress: "127.0.0.1:0"}, Store: bad}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Error(err)
	}
	// A certificate kept that is not one.
	broken := newMemStore()
	_ = broken.SetSection(Section, []byte(`{"version":2,"members":[],"link_cert":{"cert":"x","key":"y"}}`))
	if _, err := New(Options{Engine: ro.engine, Settings: &config.Cluster{LinkAddress: "127.0.0.1:0"}, Store: broken}); err == nil {
		t.Error("a broken certificate")
	}
	// The address is taken.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	if _, err := New(Options{Engine: ro.engine, Settings: &config.Cluster{LinkAddress: taken.Addr().String()}, Store: newMemStore()}); err == nil {
		t.Error("an address taken")
	}
	// A manager that no longer takes links says so of its linked members.
	f2, _ := linkFixture(t, "127.0.0.1:0")
	enrollIn(t, f2, "kept")
	lm := startLinked(t, f2, mustEnrollAgain(t, f2, "kept").Config, nil, nil)
	lm.waitLinked(t, true)
	f2.m.Close()
	f2.set.LinkAddress = ""
	f2.start()
	st2 := f2.waitKind("kept", KindNotLinked)
	if !strings.Contains(st2.Error.Message, "takes no links") {
		t.Error(st2.Error)
	}
}

func mustEnrollAgain(t *testing.T, f *fixture, name string) Enrollment {
	t.Helper()
	e, err := f.m.EnrollAgain("u", name, &EnrollInput{}, "")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func certLeaf(c tls.Certificate) (*x509.Certificate, error) {
	if len(c.Certificate) == 0 {
		return nil, errors.New("none")
	}
	return x509.ParseCertificate(c.Certificate[0])
}

// The member's settings and what it keeps.
func TestMemberSettings(t *testing.T) {
	e := testEngine(t)
	api := http.NotFoundHandler()
	fp := strings.Repeat("ab", 32)
	good := &config.ClusterMember{Manager: "wss://127.0.0.1:1/cluster/link", Fingerprint: fp}
	for _, o := range []MemberOptions{
		{Engine: e, API: api},
		{Settings: good, API: api},
		{Engine: e, Settings: &config.ClusterMember{Manager: good.Manager, Fingerprint: "x"}, API: api},
		{Engine: e, Settings: &config.ClusterMember{Manager: good.Manager, Fingerprint: fp, Token: "nope"}, API: api},
		{Engine: e, Settings: &config.ClusterMember{Manager: good.Manager, Fingerprint: fp, TokenFile: filepath.Join(t.TempDir(), "none")}, API: api},
	} {
		if _, err := StartMember(o); err == nil {
			t.Errorf("%+v started", o)
		}
	}
	// No token and nothing kept: nothing to link with.
	l, err := StartMember(MemberOptions{Engine: e, Settings: good, API: api})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the member to stop", func() bool { return strings.Contains(l.Status().Stopped, "no credential for the manager and no enrollment token") })
	l.Close()
	l.Close()

	// A token file; what is kept is not a credential, or cannot be read.
	tf := filepath.Join(t.TempDir(), "tok")
	secret := linktoken.New()
	_ = os.WriteFile(tf, []byte(secret.StringAs(config.ClusterEnrollPrefix)+"\n"), 0o600)
	logs := &logBuf{}
	k := MemoryKeeper()
	_ = k.Save([]byte(`{"credential":""}`))
	l, err = StartMember(MemberOptions{Engine: e, Settings: &config.ClusterMember{Manager: good.Manager, Fingerprint: fp, TokenFile: tf}, API: api, Keeper: k, Logf: logs.logf})
	if err != nil || l.token == nil || l.token.ID != secret.ID {
		t.Fatal(err)
	}
	l.Close()
	if !strings.Contains(logs.String(), "is not one") {
		t.Error(logs)
	}
	l, _ = StartMember(MemberOptions{Engine: e, Settings: good, API: api, Keeper: failKeeper{}, Logf: logs.logf})
	l.Close()
	if !strings.Contains(logs.String(), "cannot be read") {
		t.Error(logs)
	}
	// A credential kept for another manager, or not of the credentials' form.
	k = MemoryKeeper()
	_ = k.Save([]byte(`{"credential":"fitc1.x","fingerprint":"` + formatFingerprint(bytes.Repeat([]byte{0xab}, 32)) + `"}`))
	logs = &logBuf{}
	l, _ = StartMember(MemberOptions{Engine: e, Settings: good, API: api, Keeper: k, Logf: logs.logf})
	waitFor(t, "the member to stop", func() bool { return l.Status().Stopped != "" })
	l.Close()
	if !strings.Contains(logs.String(), "is not one") {
		t.Error(logs)
	}

	// The keepers.
	path := filepath.Join(t.TempDir(), "fit.cluster-member")
	fk := FileKeeper(path)
	if d, err := fk.Load(); d != nil || err != nil {
		t.Error(d, err)
	}
	if err := fk.Save(nil); err != nil {
		t.Error(err)
	}
	if err := fk.Save([]byte("{}")); err != nil {
		t.Fatal(err)
	}
	if d, _ := fk.Load(); string(d) != "{}" {
		t.Error(string(d))
	}
	if err := fk.Save(nil); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error(err)
	}
	if err := FileKeeper(filepath.Join(path, "no", "dir")).Save([]byte("{}")); err == nil {
		t.Error("saved into no directory")
	}
	if err := FileKeeper(t.TempDir()).Save(nil); err == nil {
		t.Error("removed a directory with files")
	}
	ms := newMemStore()
	sk := StoreKeeper(ms)
	_ = sk.Save([]byte(`{"a":1}`))
	if d, _ := sk.Load(); string(d) != `{"a":1}` {
		t.Error(string(d))
	}
}

type failKeeper struct{}

func (failKeeper) Load() ([]byte, error) { return nil, errors.New("unreadable") }
func (failKeeper) Save([]byte) error     { return errors.New("unwritable") }

// fakeManager is a link listener of a test's, which answers a member as
// the test says, presenting a certificate of its own.
func fakeManager(t *testing.T, onOpen func(c *websocket.Connection), onMessage func(c *websocket.Connection, data []byte)) (url, fingerprint string, e *fib.Engine) {
	t.Helper()
	e = testEngine(t)
	certPEM, keyPEM, err := certpin.SelfSigned("fake")
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := tls.X509KeyPair(certPEM, keyPEM)
	h := websocket.NewHandlerWithConfig(websocket.DefaultConfig(), websocket.HandlerFuncs{
		Open:    func(c *websocket.Connection, _ *http.Request) { onOpen(c) },
		Message: func(c *websocket.Connection, _ websocket.Opcode, data []byte) { onMessage(c, data) },
	})
	i, err := e.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: fibtls.NewServer(&tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}, h)})
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := e.BindingAddrs(i)
	return "wss://" + addrs[0].String() + config.ClusterLinkPath, certpin.Fingerprint(cert), e
}

// A member links only to a manager that proves its token, and leaves one
// that does not speak the link's protocol.
func TestMemberHandshake(t *testing.T) {
	secret := linktoken.New()
	var mode atomic.Int32
	url, fp, e := fakeManager(t, func(c *websocket.Connection) {
		switch mode.Load() {
		case 0: // a challenge that is not one
			_ = c.WriteBinary([]byte("c{"))
		case 1: // a message that is not one
			_ = c.WriteBinary([]byte("?"))
		case 2, 3, 4: // a proper challenge
			_ = c.WriteBinary(encodeJSON(msgChallenge, challengeMsg{Nonce: linktoken.Nonce()}))
		}
	}, func(c *websocket.Connection, data []byte) {
		switch mode.Load() {
		case 2: // a welcome that proves nothing
			_ = c.WriteBinary(encodeJSON(msgWelcome, welcomeMsg{Name: "x", Proof: []byte("no")}))
		case 3: // busy
			_ = c.WriteBinary(encodeJSON(msgRefuse, refuseMsg{Reason: ReasonBusy, Message: "later"}))
		}
	})
	logs := &logBuf{}
	l, err := StartMember(MemberOptions{Engine: e, Settings: &config.ClusterMember{Manager: url, Fingerprint: fp, Token: secret.StringAs(config.ClusterEnrollPrefix)},
		API: http.NotFoundHandler(), Logf: logs.logf})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i, want := range []string{"challenge is not one", "not one", "did not prove the token", "(busy)"} {
		mode.Store(int32(i))
		waitFor(t, want, func() bool { return strings.Contains(logs.String(), want) })
	}
	// No answer at all: the handshake times out.
	mode.Store(4)
	waitFor(t, "a handshake that took too long", func() bool { return strings.Contains(logs.String(), "took too long") })
	if l.Status().Linked {
		t.Fatal("linked")
	}
}

// The member serves the manager's requests on its own workers: a request
// that is not one is answered 400, one past its queue busy, and one the
// manager gives up on is canceled.
func TestMemberServe(t *testing.T) {
	f, _ := linkFixture(t, "127.0.0.1:0")
	e := enrollIn(t, f, "srv")
	block := make(chan struct{})
	canceled := make(chan struct{}, 64)
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/block" {
			select {
			case <-block:
			case <-r.Context().Done():
				canceled <- struct{}{}
			}
		}
		w.WriteHeader(204)
	})
	lm := startLinked(t, f, e.Config, nil, api)
	lm.waitLinked(t, true)
	// Straight to the member's serve: a path that is not one, a body
	// larger than it takes.
	got := make(chan []byte, 8)
	ln := lm.l
	fakeWS := captureWS(t, f, got)
	ln.serve(fakeWS, encodeCall(msgRequest, 1, requestHead{Method: "GET", Path: "nope"}, nil)[1:])
	ln.serve(fakeWS, encodeCall(msgRequest, 2, requestHead{Method: "GET", Path: "/"}, make([]byte, ln.maxBody+1))[1:])
	ln.serve(fakeWS, encodeCall(msgRequest, 3, requestHead{Method: "BAD METHOD", Path: "/"}, nil)[1:])
	ln.serve(fakeWS, []byte{1, 2})
	for range 3 {
		m := <-got
		var head responseHead
		if _, _, err := decodeCall(m[1:], &head); err != nil || head.Status != 400 {
			t.Errorf("%q", m)
		}
	}
	// The workers and the queue taken, one more is busy.
	for i := range memberWorkers + memberQueue {
		ln.serve(fakeWS, encodeCall(msgRequest, uint64(100+i), requestHead{Method: "GET", Path: "/block", Timeout: 60000}, nil)[1:])
	}
	ln.serve(fakeWS, encodeCall(msgRequest, 999, requestHead{Method: "GET", Path: "/block"}, nil)[1:])
	m := <-got
	var head responseHead
	if id, _, _ := decodeCall(m[1:], &head); id != 999 || head.Error != answerBusy {
		t.Errorf("%d %+v", id, head)
	}
	// A cancel ends one; the others end with the member.
	ln.cancel(encodeCancel(100)[1:])
	ln.cancel([]byte{1})
	<-canceled
	close(block)
}

// captureWS is a WebSocket of the manager's whose messages to the member a
// test reads: a raw link that said hello as nobody's.
func captureWS(t *testing.T, f *fixture, got chan []byte) *websocket.Connection {
	t.Helper()
	srv := newWSEcho(t, f, got)
	return srv
}

// newWSEcho is a client WebSocket to a server of the test's, which hands
// what it receives to got.
func newWSEcho(t *testing.T, f *fixture, got chan []byte) *websocket.Connection {
	t.Helper()
	h := websocket.NewHandler(websocket.HandlerFuncs{
		Message: func(_ *websocket.Connection, _ websocket.Opcode, data []byte) { got <- bytes.Clone(data) },
	})
	i, err := f.engine.AddBinding(fib.Binding{Addr: "127.0.0.1:0", Handler: h})
	if err != nil {
		t.Fatal(err)
	}
	addrs, _ := f.engine.BindingAddrs(i)
	fut := websocket.NewDialer(f.engine, websocket.DefaultDialerConfig()).Go("ws://"+addrs[0].String()+"/", nil, nil)
	select {
	case <-fut.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("no WebSocket")
	}
	conn, _, err := fut.Wait()
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

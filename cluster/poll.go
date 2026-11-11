package cluster

import (
	"encoding/json"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The capabilities of a member's GET /node the poller looks for, as package
// admin names them (admin.CapStatus, admin.CapDrift).
const (
	capStatus = "status"
	capCheck  = "config.check"
)

// The paths the poller reads: through a member's console, or its Admin API.
var (
	consolePaths = paths{login: "/api/login", logout: "/api/logout", node: "/api/node", status: "/api/m/admin/status", check: "/api/m/admin/config/check"}
	adminPaths   = paths{node: "/node", status: "/status", check: "/config/check"}
)

type paths struct{ login, logout, node, status, check string }

// The states of a member, State.Status.
const (
	// StatusUnknown: not polled yet.
	StatusUnknown = "unknown"
	// StatusUp: the last poll read the member.
	StatusUp = "up"
	// StatusDown: the last poll did not reach the member, or it did not
	// answer in time.
	StatusDown = "down"
	// StatusError: the last poll reached the member and failed otherwise:
	// its certificate, its credentials, its answers.
	StatusError = "error"
)

// The bounds of the login backoff of a member whose credentials were
// refused: a manager that keeps sending a wrong password would be locked out
// of the member, and lock its users out with it.
const (
	minLoginBackoff = time.Minute
	maxLoginBackoff = 15 * time.Minute
	// maxMessage bounds a Problem's message, much of which may come from
	// the member.
	maxMessage = 512
)

// NodeInfo is what a member's GET /node says of it.
type NodeInfo struct {
	ID            string    `json:"id"`
	Hostname      string    `json:"hostname"`
	FitVersion    string    `json:"fit_version"`
	FibVersion    string    `json:"fib_version"`
	GoVersion     string    `json:"go_version"`
	OS            string    `json:"os"`
	Arch          string    `json:"arch"`
	Started       time.Time `json:"started"`
	UptimeSeconds int64     `json:"uptime_seconds"`
	Roles         []string  `json:"roles"`
	SchemaVersion int       `json:"schema_version"`
	SchemaDigest  string    `json:"schema_digest"`
	Capabilities  []string  `json:"capabilities"`
}

// nodeAnswer is GET /node's answer: NodeInfo and the running config.
type nodeAnswer struct {
	NodeInfo
	ConfigVersion uint64    `json:"config_version"`
	ConfigETag    string    `json:"config_etag"`
	ConfigApplied time.Time `json:"config_applied"`
	ConfigSource  string    `json:"config_source"`
}

// ConfigInfo is the member's running config.
type ConfigInfo struct {
	Version uint64    `json:"version"`
	ETag    string    `json:"etag"`
	Applied time.Time `json:"applied"`
	Source  string    `json:"source"`
}

// Reloads counts the member's configs applied and those that failed.
type Reloads struct {
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
}

// Connections are the member's client connections.
type Connections struct {
	Open    int64  `json:"open"`
	Refused uint64 `json:"refused"`
	// PendingBytes are the bytes queued for clients that their
	// connections have not taken (I150).
	PendingBytes int64 `json:"pending_bytes"`
}

// Upstreams counts the member's upstreams and their backends, those down
// among them.
type Upstreams struct {
	Count        int `json:"count"`
	Backends     int `json:"backends"`
	BackendsDown int `json:"backends_down"`
}

// Certificates are the certificates the member's listeners serve: how many,
// and the one that expires first.
type Certificates struct {
	Count           int       `json:"count"`
	NearestExpiry   time.Time `json:"nearest_expiry,omitzero"`
	NearestNames    []string  `json:"nearest_names,omitempty"`
	NearestListener string    `json:"nearest_listener,omitempty"`
}

// Drift is the member's consistency check: the differences between its
// config and what runs, as GET /config/check counts them.
type Drift struct {
	Count     int       `json:"count"`
	Info      int       `json:"info"`
	CheckedAt time.Time `json:"checked_at"`
}

// statusAnswer is the part of GET /status the poller keeps.
type statusAnswer struct {
	Reloads     Reloads     `json:"reloads"`
	Connections Connections `json:"connections"`
	Upstreams   []struct {
		Backends []struct {
			Up bool `json:"up"`
		} `json:"backends"`
	} `json:"upstreams"`
	Certificates []struct {
		Listener  string    `json:"listener"`
		Names     []string  `json:"names"`
		Issuer    string    `json:"issuer"`
		Serial    string    `json:"serial"`
		NotBefore time.Time `json:"not_before"`
		NotAfter  time.Time `json:"not_after"`
	} `json:"certificates"`
	// Requests are the requests the member answered since it started, by
	// the class of their status, when it counts them (I146, I150).
	Requests *requestCounts `json:"requests"`
}

// checkAnswer is the part of GET /config/check the poller keeps.
type checkAnswer struct {
	CheckedAt time.Time `json:"checked_at"`
	Summary   struct {
		Drift int `json:"drift"`
		Info  int `json:"info"`
	} `json:"summary"`
}

// State is what the manager knows of a member: how its last poll went, and
// what the last poll that read it found, kept while it is down.
type State struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	// Groups are the names of the groups the member is in.
	Groups []string `json:"groups"`
	URL    string   `json:"url"`
	Access string   `json:"access"`
	// Status is StatusUp, StatusDown, StatusError or StatusUnknown, and
	// Since when it became that.
	Status string    `json:"status"`
	Since  time.Time `json:"since"`
	// Error is why the last poll failed; nil after one that succeeded.
	Error *Problem `json:"error,omitempty"`
	// Reachable is whether the last poll reached the member at all.
	Reachable bool `json:"reachable"`
	// LatencyMs is how long its GET /node took, at the last poll that
	// read it.
	LatencyMs int64 `json:"latency_ms"`
	// Polled is when the last poll ended, Seen when the last that
	// succeeded did, and Failures how many in a row have failed since.
	Polled   time.Time `json:"polled,omitzero"`
	Seen     time.Time `json:"seen,omitzero"`
	Failures int       `json:"failures"`
	// MustChangePassword is a member whose console asks for its temporary
	// password to be changed.
	MustChangePassword bool `json:"must_change_password,omitempty"`

	Node         *NodeInfo     `json:"node,omitempty"`
	Config       *ConfigInfo   `json:"config,omitempty"`
	Reloads      *Reloads      `json:"reloads,omitempty"`
	Connections  *Connections  `json:"connections,omitempty"`
	Upstreams    *Upstreams    `json:"upstreams,omitempty"`
	Certificates *Certificates `json:"certificates,omitempty"`
	Drift        *Drift        `json:"drift,omitempty"`
	// TLS is the certificate the member presented at its latest
	// handshake.
	TLS *CertInfo `json:"tls,omitempty"`
	// Link is the link of a member that links out to the manager.
	Link *LinkState `json:"link,omitempty"`
}

// node is a member as the poller reaches it: its transport, its console
// session, and its state.
type node struct {
	m    *Manager
	spec Member // the member as the node was made for it, never changed
	t    caller
	p    paths
	// busy is a poll under way; next is when the next is due, in Unix
	// nanoseconds; closed is a member removed or changed.
	busy   atomic.Bool
	next   atomic.Int64
	closed atomic.Bool

	mu sync.Mutex
	// session is the console's, when logged in.
	session *session
	// loginAfter is when a login may be tried again, after one refused,
	// with loginProblem why; backoff is the wait after the next refusal.
	loginAfter   time.Time
	loginProblem *Problem
	backoff      time.Duration
	state        State
	// logins is the callers waiting on the login under way, nil when none
	// is: a read and a poll arriving together share one login, so that a
	// refusal's backoff is not passed by a second login sent before the
	// first one's answer.
	logins []func(*Problem)

	// ins is what the polls read for the series and the certificates'
	// inventory (I150, insight.go), under a lock of its own.
	ins insight
}

// session is a console's login: its cookies and CSRF token.
type session struct {
	cookies string
	csrf    string
}

func newNode(m *Manager, spec Member) *node {
	n := &node{m: m, spec: spec, p: adminPaths}
	switch spec.Access {
	case AccessLink:
		n.t = newLinkTransport(m, spec.credentialID())
	default:
		n.t = newTransport(m.o.Engine, &spec, m.lim)
	}
	if spec.Access == AccessConsole {
		n.p = consolePaths
	}
	n.state = State{Status: StatusUnknown, Since: m.now()}
	return n
}

// header returns a call's credentials: the session's cookies, or the
// bearer token.
func (n *node) header(s *session, csrf bool) stdhttp.Header {
	h := stdhttp.Header{}
	switch {
	case n.spec.Access == AccessAdminAPI:
		h.Set("Authorization", "Bearer "+n.spec.Token)
	case s != nil:
		h.Set("Cookie", s.cookies)
		if csrf {
			h.Set("X-Fit-Csrf", s.csrf)
		}
	}
	return h
}

// redact takes the member's credentials out of msg, and bounds it.
func (n *node) redact(msg string) string {
	for _, secret := range []string{n.spec.Password, n.spec.Token} {
		if len(secret) >= 4 {
			msg = strings.ReplaceAll(msg, secret, "••••")
		}
	}
	if len(msg) > maxMessage {
		msg = msg[:maxMessage] + "…"
	}
	return msg
}

// poll reads the member once and calls done when it is over, whatever came
// of it.
func (n *node) poll(done func()) {
	p := &poller{n: n, done: done, start: n.m.now()}
	p.begin()
}

// poller is one poll of a member: a chain of calls, each started from the
// callback of the one before, on fib's workers.
type poller struct {
	n       *node
	done    func()
	start   time.Time
	answer  nodeAnswer
	latency time.Duration
	status  *statusAnswer
	check   *checkAnswer
	// finishFn, when set, is told how the poll ended in place of the
	// node's state: a test's.
	finishFn func(*Problem)
}

func (p *poller) begin() { p.readNode() }

func (n *node) currentSession() *session {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.session
}

// get reads path into v and calls next, or finishes the poll with what went
// wrong.
func (p *poller) get(path string, v any, next func()) {
	n := p.n
	n.call("GET", path, nil, nil, func(r *reply, s *session, prob *Problem) {
		if prob != nil {
			p.finish(prob)
			return
		}
		if prob := n.answerProblem(r, path, s); prob != nil {
			p.finish(prob)
			return
		}
		if err := json.Unmarshal(r.body, v); err != nil {
			p.finish(&Problem{Kind: KindBadAnswer, Message: "the member's " + path + " is not the JSON expected: " + err.Error(), Status: r.status})
			return
		}
		if path == n.p.node {
			p.latency = r.took
		}
		next()
	})
}

// call sends method path, with header and body, to the member with its
// credentials, and calls done once with the answer, whatever its status,
// and the console session it was sent with; or with why there is none. A
// member reached by its console is logged in first when the node has no
// session, and again when a call answers 401, as after the member
// restarted or the session's lifetime ended, once a call. header's
// credentials, Host and Content-Length are not sent: the member's own are.
func (n *node) call(method, path string, header stdhttp.Header, body []byte, done func(*reply, *session, *Problem)) {
	if n.spec.Access == AccessConsole && n.currentSession() == nil {
		n.login(func(prob *Problem) {
			if prob != nil {
				done(nil, nil, prob)
				return
			}
			n.send(method, path, header, body, true, done)
		})
		return
	}
	n.send(method, path, header, body, false, done)
}

// dropHeaders are what a caller's header never sends to a member.
var dropHeaders = []string{"Authorization", "Cookie", "X-Fit-Csrf", "Host", "Content-Length", "Connection", "Transfer-Encoding", "Upgrade"}

// send is one try of call; relogged is a call that logged in already.
func (n *node) send(method, path string, header stdhttp.Header, body []byte, relogged bool, done func(*reply, *session, *Problem)) {
	if n.closed.Load() {
		done(nil, nil, &Problem{Kind: KindUnreachable, Message: errClosed.Error()})
		return
	}
	s := n.currentSession()
	h := n.header(s, method != stdhttp.MethodGet && method != stdhttp.MethodHead)
	for k, v := range header {
		k = stdhttp.CanonicalHeaderKey(k)
		if !slices.Contains(dropHeaders, k) {
			h[k] = slices.Clone(v)
		}
	}
	start := n.m.now()
	n.t.do(method, path, h, body, func(r *reply, err error) {
		if err != nil {
			done(nil, s, n.t.failure(err, start))
			return
		}
		r.took = n.m.now().Sub(start)
		if r.status == stdhttp.StatusUnauthorized && n.spec.Access == AccessConsole && !relogged {
			// The session ended: log in again and send it once more.
			n.dropSession(s)
			n.login(func(prob *Problem) {
				if prob != nil {
					done(nil, nil, prob)
					return
				}
				n.send(method, path, header, body, true, done)
			})
			return
		}
		done(r, s, nil)
	})
}

// answerProblem returns what a reply other than 200 means, or nil for 200.
// A console asking for its password to be changed ends the session, so
// that the next poll logs in again with what the manager has then.
func (n *node) answerProblem(r *reply, path string, s *session) *Problem {
	if r.status == stdhttp.StatusOK {
		return nil
	}
	var body struct {
		Error      string `json:"error"`
		MustChange bool   `json:"must_change_password"`
	}
	_ = json.Unmarshal(r.body, &body)
	msg := body.Error
	if msg == "" {
		msg = stdhttp.StatusText(r.status)
	}
	p := &Problem{Kind: KindStatus, Status: r.status, Message: n.redact("GET " + path + ": " + msg)}
	switch {
	case r.status == stdhttp.StatusUnauthorized:
		p.Kind = KindAuth
		if n.spec.Access == AccessAdminAPI {
			p.Message = "the Admin API refused the token: " + n.redact(msg)
		}
	case r.status == stdhttp.StatusForbidden && body.MustChange:
		p.Kind, p.Message = KindPasswordChange, "the member's console asks for the user's temporary password to be changed; change it on the member, then here"
		n.dropSession(s)
	case r.status == stdhttp.StatusForbidden:
		p.Kind = KindForbidden
	case r.status == stdhttp.StatusNotFound && path == n.p.node:
		p.Kind, p.Message = KindUnsupported, "the member serves no "+path+": it is not a fit, or one older than cluster management"
	}
	return p
}

func (p *poller) readNode() {
	p.get(p.n.p.node, &p.answer, func() {
		if slices.Contains(p.answer.Capabilities, capStatus) {
			p.readStatus()
			return
		}
		p.readCheck()
	})
}

func (p *poller) readStatus() {
	st := &statusAnswer{}
	p.get(p.n.p.status, st, func() {
		p.status = st
		p.readCheck()
	})
}

func (p *poller) readCheck() {
	if !slices.Contains(p.answer.Capabilities, capCheck) {
		p.finish(nil)
		return
	}
	ck := &checkAnswer{}
	p.get(p.n.p.check, ck, func() {
		p.check = ck
		p.finish(nil)
	})
}

// finish records how the poll went, and ends it.
func (p *poller) finish(prob *Problem) {
	if p.finishFn != nil {
		p.finishFn(prob)
		return
	}
	n := p.n
	defer p.done()
	if n.closed.Load() {
		return
	}
	now := n.m.now()
	n.mu.Lock()
	st := &n.state
	old := st.Status
	st.Polled = now
	if seen := n.t.certificate(); seen != nil {
		c := *seen
		c.err = nil
		st.TLS = &c
	}
	if prob != nil {
		if prob.At.IsZero() {
			prob.At = now
		}
		prob.Message = n.redact(prob.Message)
		st.Error, st.Failures = prob, st.Failures+1
		st.Reachable = prob.Kind != KindUnreachable && prob.Kind != KindTimeout && prob.Kind != KindNotLinked
		st.Status = StatusError
		if !st.Reachable {
			st.Status = StatusDown
		}
		st.MustChangePassword = prob.Kind == KindPasswordChange
	} else {
		p.fill(st)
		st.Status, st.Error, st.Failures, st.Reachable, st.Seen, st.MustChangePassword = StatusUp, nil, 0, true, now, false
	}
	changed := st.Status != old
	if changed {
		st.Since = now
	}
	status, name := st.Status, n.spec.Name
	n.mu.Unlock()
	if prob == nil {
		// The series and the certificates' inventory (I150).
		n.observe(now, p)
	}
	if changed {
		if prob != nil {
			n.m.logf("cluster: member %q is %s (%s): %s", name, status, prob.Kind, prob.Message)
		} else {
			n.m.logf("cluster: member %q is %s", name, status)
		}
	}
}

// fill sets st from what the poll read.
func (p *poller) fill(st *State) {
	a := &p.answer
	info := a.NodeInfo
	info.Roles, info.Capabilities = nonNil(info.Roles), nonNil(info.Capabilities)
	st.Node = &info
	st.Config = &ConfigInfo{Version: a.ConfigVersion, ETag: a.ConfigETag, Applied: a.ConfigApplied, Source: a.ConfigSource}
	st.LatencyMs = p.latency.Milliseconds()
	st.Reloads, st.Connections, st.Upstreams, st.Certificates, st.Drift = nil, nil, nil, nil, nil
	if s := p.status; s != nil {
		st.Reloads, st.Connections = &s.Reloads, &s.Connections
		up := &Upstreams{Count: len(s.Upstreams)}
		for _, u := range s.Upstreams {
			up.Backends += len(u.Backends)
			for _, b := range u.Backends {
				if !b.Up {
					up.BackendsDown++
				}
			}
		}
		st.Upstreams = up
		c := &Certificates{Count: len(s.Certificates)}
		for _, cert := range s.Certificates {
			if c.NearestExpiry.IsZero() || cert.NotAfter.Before(c.NearestExpiry) {
				c.NearestExpiry, c.NearestNames, c.NearestListener = cert.NotAfter, cert.Names, cert.Listener
			}
		}
		st.Certificates = c
	}
	if ck := p.check; ck != nil {
		st.Drift = &Drift{Count: ck.Summary.Drift, Info: ck.Summary.Info, CheckedAt: ck.CheckedAt}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// loginBody is a console login's answer.
type loginBody struct {
	User       string `json:"user"`
	CSRF       string `json:"csrf"`
	MustChange bool   `json:"must_change_password"`
}

// login logs in to the member's console, keeping the session, and calls
// then with nil, or with why it could not. A login refused is not tried
// again before its backoff ends, nor one the member rate-limits before the
// wait it asks for.
func (n *node) login(then func(*Problem)) {
	if n.closed.Load() {
		then(&Problem{Kind: KindUnreachable, Message: errClosed.Error()})
		return
	}
	now := n.m.now()
	n.mu.Lock()
	if now.Before(n.loginAfter) && n.loginProblem != nil {
		prob := *n.loginProblem
		n.mu.Unlock()
		then(&prob)
		return
	}
	if n.logins != nil {
		n.logins = append(n.logins, then)
		n.mu.Unlock()
		return
	}
	n.logins = []func(*Problem){then}
	n.mu.Unlock()
	done := func(prob *Problem) {
		n.mu.Lock()
		waiting := n.logins
		n.logins = nil
		n.mu.Unlock()
		for _, f := range waiting {
			if prob == nil {
				f(nil)
				continue
			}
			cp := *prob // each caller's own, which it may change
			f(&cp)
		}
	}
	body, _ := json.Marshal(map[string]string{"username": n.spec.Username, "password": n.spec.Password})
	n.t.do("POST", n.p.login, nil, body, func(r *reply, err error) {
		if err != nil {
			done(n.t.failure(err, now))
			return
		}
		prob, s := n.loginResult(r)
		if s != nil && prob != nil {
			// Logged in with a temporary password: the session can do
			// nothing but change it, which the manager does not.
			n.logout(s, nil)
		}
		done(prob)
	})
}

// loginResult reads a login's answer: the session, or why there is none.
// A session whose password must change comes with its Problem.
func (n *node) loginResult(r *reply) (*Problem, *session) {
	now := n.m.now()
	n.mu.Lock()
	defer n.mu.Unlock()
	refuse := func(p *Problem, wait time.Duration) (*Problem, *session) {
		p.At = now
		n.loginAfter, n.loginProblem = now.Add(wait), p
		// The caller's own copy, which it may change without n.mu.
		cp := *p
		return &cp, nil
	}
	switch r.status {
	case stdhttp.StatusOK:
		var b loginBody
		cookies := cookieHeader(r.header)
		if err := json.Unmarshal(r.body, &b); err != nil || b.CSRF == "" || cookies == "" {
			return &Problem{Kind: KindBadAnswer, Status: r.status, Message: "the member's login answered no session: it is not a fit's console"}, nil
		}
		s := &session{cookies: cookies, csrf: b.CSRF}
		if b.MustChange {
			// Asked again after a backoff, as a refused login is, so that
			// the member's log is not filled with the manager's logins.
			n.backoff = min(max(2*n.backoff, minLoginBackoff, n.m.interval), maxLoginBackoff)
			p, _ := refuse(&Problem{Kind: KindPasswordChange, Status: r.status,
				Message: "the member's console asks for the user's temporary password to be changed; change it on the member, then here"}, n.backoff)
			return p, s
		}
		n.loginAfter, n.loginProblem, n.backoff = time.Time{}, nil, 0
		n.session = s
		return nil, nil
	case stdhttp.StatusUnauthorized:
		n.backoff = min(max(2*n.backoff, minLoginBackoff, n.m.interval), maxLoginBackoff)
		var b struct {
			Expired bool `json:"password_expired"`
		}
		_ = json.Unmarshal(r.body, &b)
		msg := "the member's console refused the user name or password"
		if b.Expired {
			msg = "the member's console refused the user's temporary password, which expired without being changed"
		}
		return refuse(&Problem{Kind: KindAuth, Status: r.status, Message: msg + "; not tried again for " + n.backoff.String() + " unless the member is changed or tested"}, n.backoff)
	case stdhttp.StatusTooManyRequests:
		// Too many failed logins from the manager's address or as the
		// user, or another login from the address being checked: the
		// member says which.
		secs, _ := strconv.Atoi(r.header.Get("Retry-After"))
		secs = min(max(secs, 1), int(maxLoginBackoff/time.Second))
		var b struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(r.body, &b)
		if b.Error == "" {
			b.Error = "too many logins"
		}
		return refuse(&Problem{Kind: KindRateLimited, Status: r.status, RetryAfter: secs,
			Message: n.redact("the member's console refuses logins for " + strconv.Itoa(secs) + "s: " + b.Error)}, time.Duration(secs)*time.Second)
	case stdhttp.StatusNotFound:
		return &Problem{Kind: KindUnsupported, Status: r.status, Message: "the member serves no console at " + n.p.login}, nil
	}
	var b struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(r.body, &b)
	return &Problem{Kind: KindStatus, Status: r.status, Message: n.redact("the member's login answered " + strconv.Itoa(r.status) + " " + b.Error)}, nil
}

// cookieHeader returns the cookies a login set, as a Cookie header sends
// them back.
func cookieHeader(h stdhttp.Header) string {
	var parts []string
	for _, c := range (&stdhttp.Response{Header: h}).Cookies() {
		if c.Value != "" && c.MaxAge >= 0 {
			parts = append(parts, c.Name+"="+c.Value)
		}
	}
	return strings.Join(parts, "; ")
}

// dropSession forgets s, the session a call was made with, when it is
// still the node's.
func (n *node) dropSession(s *session) {
	n.mu.Lock()
	if n.session == s {
		n.session = nil
	}
	n.mu.Unlock()
}

// resetLogin lets the next poll log in at once: the member was tested and
// its credentials worked.
func (n *node) resetLogin() {
	n.mu.Lock()
	n.loginAfter, n.loginProblem, n.backoff = time.Time{}, nil, 0
	n.mu.Unlock()
}

// logout ends s on the member, so that it does not count against the
// member's sessions, and calls done, when not nil, once it is over.
func (n *node) logout(s *session, done func()) {
	n.t.do("POST", n.p.logout, n.header(s, true), []byte("{}"), func(*reply, error) {
		if done != nil {
			done()
		}
	})
}

// shutdown ends the node: its calls abandoned, its session logged out, and
// its connections closed; done is called once that is over.
func (n *node) shutdown(done func()) {
	n.closed.Store(true)
	n.mu.Lock()
	s := n.session
	n.session = nil
	n.mu.Unlock()
	// The poll's calls are abandoned; the logout is the last call.
	n.t.abandon()
	if s == nil {
		n.t.close()
		done()
		return
	}
	n.logout(s, func() {
		n.t.close()
		done()
	})
}

// snapshot returns a copy of the node's state, with its member's name,
// labels, URL and access.
func (n *node) snapshot(spec *Member) State {
	n.mu.Lock()
	st := n.state
	n.mu.Unlock()
	st.Name, st.URL, st.Access = spec.Name, spec.URL, spec.Access
	st.Labels = spec.View().Labels
	return st
}

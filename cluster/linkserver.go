package cluster

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	stdhttp "net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	fibtls "github.com/lesismal/fib/tls"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/bindcheck"
	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/internal/linktoken"
)

// KindNotLinked: the member links out to the manager, and has no link now:
// it has not enrolled yet, or its link closed and it has not linked again.
const KindNotLinked = "not_linked"

// The errors of a call over a link.
var (
	errNotLinked    = errors.New("the member has no link to the manager now")
	errLinkClosed   = errors.New("the member's link closed")
	errLinkBusy     = errors.New("the member's link carries as many calls as it takes")
	errLinkTimeout  = fmt.Errorf("the member did not answer in time: %w", errDeadline)
	errLinkTooLarge = errors.New("the member's answer is larger than max_response_bytes")
	errLinkBody     = errors.New("the request is larger than max_response_bytes, which bounds a link's messages both ways")
	errDeadline     = errors.New("deadline exceeded")
)

// linkHub is the manager's side of the links members open to it: its
// listener, and the links open, by credential.
type linkHub struct {
	ln          *linkListener
	cert        tls.Certificate
	fingerprint string // the certificate's, as fit writes pins

	mu    sync.Mutex
	links map[string]*mlink // by credential ID
	seen  map[string]*LinkState
	// jobs are the enrollments, which write the store, on a goroutine of
	// their own.
	jobs     chan func()
	jobsStop chan struct{}
	jobsDone chan struct{}
}

// LinkState is a linked member's link, as its state shows it.
type LinkState struct {
	// Linked is a link open now, since Since; otherwise Since is when the
	// last one closed, zero when there has been none since the manager
	// started.
	Linked bool      `json:"linked"`
	Since  time.Time `json:"since,omitzero"`
	// Remote is the address the link came from, and Host the host name
	// the member gave.
	Remote string `json:"remote,omitempty"`
	Host   string `json:"host,omitempty"`
	// Enrolled is a member that has a credential; EnrollBy, for one that
	// has an enrollment token waiting, is when the token expires.
	Enrolled bool      `json:"enrolled"`
	EnrollBy time.Time `json:"enroll_by,omitzero"`
	// Refused is the last hello refused as this member's, with its reason.
	Refused *Refusal `json:"refused,omitempty"`
}

// Refusal is a hello the manager refused.
type Refusal struct {
	At      time.Time `json:"at"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	Remote  string    `json:"remote,omitempty"`
}

// maxJobs bounds the enrollments waiting for the store.
const maxJobs = 16

// startLinks starts the link listener admin.cluster.link_address names, with
// the certificate the document keeps, made and kept the first time.
func (m *Manager) startLinks() error {
	h := &m.link
	h.links, h.seen = map[string]*mlink{}, map[string]*LinkState{}
	s := m.o.Settings
	if s.LinkAddress == "" {
		return nil
	}
	cert, err := m.linkCertificate()
	if err != nil {
		return fmt.Errorf("cluster: the link listener's certificate: %w", err)
	}
	h.cert, h.fingerprint = cert, formatFingerprint(mustParsePin(certpin.Fingerprint(cert)))
	h.jobs, h.jobsStop, h.jobsDone = make(chan func(), maxJobs), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(h.jobsDone)
		for {
			select {
			case <-h.jobsStop:
				return
			case job := <-h.jobs:
				job()
			}
		}
	}()
	ln, err := listenLinks(m, s.LinkAddress, cert)
	if err != nil {
		close(h.jobsStop)
		<-h.jobsDone
		return err
	}
	h.ln = ln
	return nil
}

func mustParsePin(s string) []byte {
	b, _ := certpin.Parse(s) // certpin.Fingerprint's own
	return b
}

// linkCertificate returns the link listener's certificate: the document's,
// or one made now and kept in it. A manager that cannot write its document
// makes one that lasts until it stops, which Logf says.
func (m *Manager) linkCertificate() (tls.Certificate, error) {
	m.mu.Lock()
	kept, ro := m.doc.LinkCert, m.readOnly
	m.mu.Unlock()
	if kept != nil {
		return tls.X509KeyPair([]byte(kept.Cert), []byte(kept.Key))
	}
	host := "fit-cluster-manager"
	if u := m.o.Settings.LinkURL; u != "" {
		if i := strings.Index(u, "://"); i >= 0 {
			if hp := strings.SplitN(u[i+3:], "/", 2)[0]; hp != "" {
				if h, _, err := net.SplitHostPort(hp); err == nil {
					host = h
				} else {
					host = hp
				}
			}
		}
	}
	certPEM, keyPEM, err := certpin.SelfSigned("fit cluster manager", host)
	if err != nil {
		return tls.Certificate{}, err
	}
	if ro != "" {
		m.logf("cluster: the link listener's certificate cannot be kept, as the document is read-only: members enrolled now cannot link after a restart")
	} else {
		m.wmu.Lock()
		m.mu.Lock()
		d := m.doc
		d.LinkCert = &linkCert{Cert: string(certPEM), Key: string(keyPEM)}
		m.mu.Unlock()
		if err := m.o.Store.SetSection(Section, d.marshal()); err != nil {
			m.wmu.Unlock()
			return tls.Certificate{}, fmt.Errorf("keeping it: %w", err)
		}
		m.mu.Lock()
		d.Version = DocVersion
		m.doc = d
		m.mu.Unlock()
		m.wmu.Unlock()
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// stopLinks closes the listener, every link and the enrollments' goroutine.
func (m *Manager) stopLinks() {
	h := &m.link
	if h.ln == nil {
		return
	}
	h.ln.close()
	h.mu.Lock()
	links := make([]*mlink, 0, len(h.links))
	for _, l := range h.links {
		links = append(links, l)
	}
	h.links = map[string]*mlink{}
	h.mu.Unlock()
	for _, l := range links {
		l.closeNow(errClosed)
	}
	close(h.jobsStop)
	<-h.jobsDone
}

// LinkInfo is where members link to the manager, for the pages.
type LinkInfo struct {
	// Enabled is a manager with admin.cluster.link_address.
	Enabled bool `json:"enabled"`
	// Address is where the listener listens; URL what a member's config
	// names it by; Fingerprint its certificate's, which members pin.
	Address     string `json:"address,omitempty"`
	URL         string `json:"url,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Links is how many links are open.
	Links int `json:"links"`
	// DefaultTTL and MaxTTL bound an enrollment token's lifetime, in
	// seconds.
	DefaultTTL int64 `json:"default_ttl"`
	MaxTTL     int64 `json:"max_ttl"`
}

// LinkInfo returns where members link to the manager; host is the host the
// page was opened at, which names the listener when link_url does not.
func (m *Manager) LinkInfo(host string) LinkInfo {
	h := &m.link
	out := LinkInfo{DefaultTTL: int64(config.DefaultClusterEnrollTTL.Std() / time.Second), MaxTTL: int64(config.MaxClusterEnrollTTL.Std() / time.Second)}
	if h.ln == nil {
		return out
	}
	h.mu.Lock()
	out.Links = len(h.links)
	h.mu.Unlock()
	out.Enabled, out.Address, out.Fingerprint = true, h.ln.addr.String(), h.fingerprint
	out.URL = m.o.Settings.LinkURL
	if out.URL == "" {
		port := ""
		if ta, ok := h.ln.addr.(*net.TCPAddr); ok {
			port = strconv.Itoa(ta.Port)
		}
		hostname := host
		if hn, _, err := net.SplitHostPort(host); err == nil {
			hostname = hn
		}
		if hostname == "" {
			hostname = "localhost"
		}
		out.URL = "wss://" + net.JoinHostPort(strings.Trim(hostname, "[]"), port) + config.ClusterLinkPath
	}
	return out
}

// linkListener is the listener of links: a WebSocket server over TLS on a
// binding of the engine's own.
type linkListener struct {
	m      *Manager
	engine *fib.Engine
	index  int
	addr   net.Addr
	conns  *linkConns
	open   sync.Map // *websocket.Connection → *mlink
}

func listenLinks(m *Manager, address string, cert tls.Certificate) (*linkListener, error) {
	ln := &linkListener{m: m, engine: m.o.Engine, index: -1}
	wsCfg := websocket.DefaultConfig()
	wsCfg.MaxMessageBytes = m.lim.maxBytes + maxHeadBytes + 64
	// No web page opens a link; the path is the link's alone.
	wsCfg.CheckOrigin = func(r *stdhttp.Request) bool {
		return r.URL.Path == config.ClusterLinkPath && r.Header.Get("Origin") == ""
	}
	wsCfg.HTTP.MaxHeaderBytes = 16 << 10
	wsCfg.HTTP.ReadHeaderTimeout = linkHandshake
	h := websocket.NewHandlerWithConfig(wsCfg, websocket.HandlerFuncs{
		Open:    ln.onOpen,
		Message: ln.onMessage,
		Close:   ln.onClose,
		Ping: func(c *websocket.Connection, payload []byte) {
			if l := ln.lookup(c); l != nil {
				l.touch()
			}
			_ = c.Pong(payload)
		},
	})
	th := fibtls.NewServer(&tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}, h)
	th.HandshakeTimeout = linkHandshake
	ln.conns = &linkConns{Handler: th, max: m.maxNodes + maxLinksSlack, maxPending: maxPendingLinks, conns: map[*fib.Connection]bool{}}
	if err := bindcheck.Free("tcp", address); err != nil {
		return nil, fmt.Errorf("cluster: link_address: %w", err)
	}
	var err error
	if ln.index, err = ln.engine.AddBinding(fib.Binding{Addr: address, Handler: ln.conns}); err != nil {
		return nil, fmt.Errorf("cluster: link_address: %w", err)
	}
	if addrs, err := ln.engine.BindingAddrs(ln.index); err == nil && len(addrs) > 0 {
		ln.addr = addrs[0]
	} else {
		ln.addr = &net.TCPAddr{}
	}
	return ln, nil
}

func (ln *linkListener) close() {
	if ln.index >= 0 {
		_ = ln.engine.RemoveBinding(ln.index)
	}
	ln.conns.closeAll()
}

func (ln *linkListener) lookup(c *websocket.Connection) *mlink {
	if v, ok := ln.open.Load(c); ok {
		return v.(*mlink)
	}
	return nil
}

// The states of a link.
const (
	linkHello     = iota // waiting for the member's hello
	linkVerifying        // its hello is being checked, or its enrollment kept
	linkOpen             // the member's: calls go over it
	linkClosed
)

// mlink is a link a member opened, on the manager.
type mlink struct {
	ln     *linkListener
	ws     *websocket.Connection
	raw    *fib.Connection // the connection under it, to close at once
	remote string
	nonce  []byte
	timer  *time.Timer // the handshake's, then the idle one
	// active is when the link last heard from the member, in Unix
	// nanoseconds.
	active atomic.Int64

	mu     sync.Mutex
	state  int
	credID string
	name   string // the member's, when it linked
	host   string
	since  time.Time
	calls  map[uint64]*linkCall
	next   uint64
}

func (ln *linkListener) onOpen(c *websocket.Connection, r *stdhttp.Request) {
	l := &mlink{ln: ln, ws: c, raw: ln.conns.lookup(r.RemoteAddr), remote: r.RemoteAddr, nonce: linktoken.Nonce(), calls: map[uint64]*linkCall{}}
	l.active.Store(time.Now().UnixNano())
	ln.open.Store(c, l)
	l.timer = time.AfterFunc(linkHandshake, func() {
		l.mu.Lock()
		state := l.state
		l.mu.Unlock()
		if state < linkOpen {
			l.closeNow(errors.New("the link's handshake took too long"))
		}
	})
	if err := c.WriteBinary(encodeJSON(msgChallenge, challengeMsg{Nonce: l.nonce})); err != nil {
		l.closeNow(err)
	}
}

func (ln *linkListener) onMessage(c *websocket.Connection, _ websocket.Opcode, data []byte) {
	l := ln.lookup(c)
	if l == nil || len(data) == 0 {
		return
	}
	l.touch()
	l.mu.Lock()
	state := l.state
	if state == linkHello {
		l.state = linkVerifying
	}
	l.mu.Unlock()
	switch {
	case state == linkHello && data[0] == msgHello && len(data) <= maxHelloBytes:
		ln.m.onHello(l, data[1:])
	case state == linkHello:
		l.refuse(ReasonBadHello, "a link starts with a hello", "")
	case state == linkOpen && data[0] == msgResponse:
		l.answer(data[1:])
	case state == linkOpen:
		l.closeNow(errBadMessage)
	}
}

func (ln *linkListener) onClose(c *websocket.Connection, _ uint16, _ string, err error) {
	v, ok := ln.open.LoadAndDelete(c)
	if !ok {
		return
	}
	l := v.(*mlink)
	l.timer.Stop()
	l.mu.Lock()
	wasOpen := l.state == linkOpen
	l.state = linkClosed
	calls := l.calls
	l.calls = map[uint64]*linkCall{}
	l.mu.Unlock()
	for _, k := range calls {
		k.finish(nil, errLinkClosed)
	}
	if wasOpen {
		ln.m.linkClosed(l, err)
	}
}

// touch notes that the member was heard from, and puts off the close of a
// link that hears nothing.
func (l *mlink) touch() {
	l.active.Store(time.Now().UnixNano())
	l.mu.Lock()
	open := l.state == linkOpen
	l.mu.Unlock()
	if open {
		l.timer.Reset(linkDead)
	}
}

// closeNow closes the link's connection at once, without the close
// handshake: a member that hears nothing links again.
func (l *mlink) closeNow(error) {
	if l.raw != nil {
		_ = l.raw.Close()
		return
	}
	_ = l.ws.Close(websocket.CloseGoingAway, "")
}

// The close codes of a link the manager ends.
const (
	closeRefused = 4001
	closeRevoked = 4002
)

// refuse refuses the member's hello, saying why, and closes the link; name,
// when the hello named a member, keeps the refusal in its state.
func (l *mlink) refuse(reason, msg, name string) {
	if name != "" {
		l.ln.m.noteRefusal(name, &Refusal{At: time.Now().UTC(), Reason: reason, Message: msg, Remote: l.remote})
	}
	l.ln.m.logf("cluster: a link from %s was refused (%s): %s", l.remote, reason, msg)
	_ = l.ws.WriteBinary(encodeJSON(msgRefuse, refuseMsg{Reason: reason, Message: msg}))
	_ = l.ws.Close(closeRefused, reason)
}

// revoke tells the member its credential is gone, and closes the link.
func (l *mlink) revoke(reason, msg string) {
	_ = l.ws.WriteBinary(encodeJSON(msgRevoked, refuseMsg{Reason: reason, Message: msg}))
	_ = l.ws.Close(closeRevoked, reason)
}

// onHello checks a member's hello: its credential, or its enrollment
// token, which is kept used on the jobs' goroutine before the welcome
// gives the member its credential.
func (m *Manager) onHello(l *mlink, data []byte) {
	var h helloMsg
	if err := json.Unmarshal(data, &h); err != nil || len(h.Nonce) != linktoken.NonceSize || !config.ValidLinkTokenID(h.ID) {
		l.refuse(ReasonBadHello, "the hello is not one", "")
		return
	}
	l.mu.Lock()
	l.host = boundString(h.Node.Hostname, 255)
	l.mu.Unlock()
	tr := transcript(h.ID, h.Nonce, l.nonce, h.Mode)
	now := m.now()
	switch h.Mode {
	case modeLink:
		name, stored, ok := m.credential(h.ID)
		if !ok {
			l.refuse(ReasonUnknown, "no member has this credential: it was removed, or enrolled again with another token", "")
			return
		}
		if linktoken.Verify(stored, tr, h.Proof) != nil {
			l.refuse(ReasonProof, "the credential's proof does not check", name)
			return
		}
		m.welcome(l, name, h.ID, welcomeMsg{Name: name, Proof: linktoken.ListenerProof(stored, tr, name, "")})
	case modeEnroll:
		name, stored, expires, used, ok := m.enrollToken(h.ID)
		switch {
		case !ok && used != "":
			l.refuse(ReasonUsed, "the enrollment token was used already: make the member a new one", used)
			return
		case !ok:
			l.refuse(ReasonUnknown, "no member has this enrollment token: it was replaced by another, or its member removed", "")
			return
		case !now.Before(expires):
			l.refuse(ReasonExpired, "the enrollment token expired at "+expires.UTC().Format(time.RFC3339)+": make the member a new one", name)
			return
		case linktoken.Verify(stored, tr, h.Proof) != nil:
			l.refuse(ReasonProof, "the enrollment token's proof does not check", name)
			return
		}
		node := h.Node
		job := func() {
			name, secret, err := m.enroll(h.ID, l.remote, &node)
			if err != nil {
				var r refusal
				if errors.As(err, &r) {
					l.refuse(r.reason, r.msg, name)
					return
				}
				l.refuse(ReasonBusy, "the manager could not keep the member's credential: "+err.Error(), name)
				return
			}
			cred := secret.StringAs(credentialPrefix)
			m.welcome(l, name, secret.ID, welcomeMsg{Name: name, Credential: cred, Proof: linktoken.ListenerProof(stored, tr, name, cred)})
		}
		select {
		case m.link.jobs <- job:
		default:
			l.refuse(ReasonBusy, "the manager is enrolling other members: try again", name)
		}
	default:
		l.refuse(ReasonBadHello, "the hello's mode is not one", "")
	}
}

// refusal is an enrollment refused, with its reason.
type refusal struct{ reason, msg string }

func (r refusal) Error() string { return r.msg }

func boundString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// credential returns the member whose credential id is, and its stored
// key.
func (m *Manager) credential(id string) (name string, stored []byte, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.doc.Members {
		mem := &m.doc.Members[i]
		if mem.Access == AccessLink && mem.Link != nil && mem.Link.CredentialID == id && mem.Link.CredentialHash != "" {
			stored, err := linktoken.StoredOf(mem.Link.CredentialHash)
			return mem.Name, stored, err == nil
		}
	}
	return "", nil, false
}

// enrollToken returns the member whose enrollment token id is, the token's
// stored key and expiry; or, when none has it, used names the member that
// used it.
func (m *Manager) enrollToken(id string) (name string, stored []byte, expires time.Time, used string, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.doc.Members {
		mem := &m.doc.Members[i]
		if mem.Access != AccessLink || mem.Link == nil {
			continue
		}
		if mem.Link.TokenID == id && mem.Link.TokenHash != "" {
			stored, err := linktoken.StoredOf(mem.Link.TokenHash)
			return mem.Name, stored, mem.Link.TokenExpires, "", err == nil
		}
		if mem.Link.UsedTokenID == id {
			used = mem.Name
		}
	}
	return "", nil, time.Time{}, used, false
}

// enroll uses the enrollment token id: its member is given a new
// credential, kept in the store in place of the token, which is never taken
// again. It writes the store: on the jobs' goroutine.
func (m *Manager) enroll(id, remote string, node *helloNode) (string, linktoken.Secret, error) {
	var name string
	secret := linktoken.New()
	err := m.change("(enrollment)", func(d *document) (AuditEntry, error) {
		now := m.now().UTC()
		for i := range d.Members {
			mem := &d.Members[i]
			if mem.Access != AccessLink || mem.Link == nil {
				continue
			}
			if mem.Link.TokenID != id || mem.Link.TokenHash == "" {
				if mem.Link.UsedTokenID == id {
					return AuditEntry{}, refusal{ReasonUsed, "the enrollment token was used already: make the member a new one"}
				}
				continue
			}
			name = mem.Name
			if !now.Before(mem.Link.TokenExpires) {
				return AuditEntry{}, refusal{ReasonExpired, "the enrollment token expired: make the member a new one"}
			}
			l := mem.Link
			l.CredentialID, l.CredentialHash, l.Enrolled, l.UsedTokenID = secret.ID, secret.Hash(), now, id
			l.TokenID, l.TokenHash, l.TokenMade, l.TokenExpires = "", "", time.Time{}, time.Time{}
			mem.Updated = now
			detail := "from " + remote
			if node.Hostname != "" {
				detail += ", host " + strconv.Quote(boundString(node.Hostname, 255))
			}
			return AuditEntry{Action: ActionEnrolled, Node: name, Detail: detail}, nil
		}
		return AuditEntry{}, refusal{ReasonUnknown, "no member has this enrollment token: it was replaced by another, or its member removed"}
	})
	if err == nil {
		m.logf("cluster: the member %q enrolled from %s", name, remote)
	}
	return name, secret, err
}

// welcome opens l as the link of the member name's credential credID, and
// sends the member w. A link the credential held before is closed; a member
// removed or enrolled again meanwhile is told its credential is gone.
func (m *Manager) welcome(l *mlink, name, credID string, w welcomeMsg) {
	m.mu.Lock()
	valid := false
	if i := index(&m.doc, name); i >= 0 && m.doc.Members[i].credentialID() == credID {
		valid = true
	}
	switch {
	case m.closed:
		m.mu.Unlock()
		l.closeNow(errClosed)
		return
	case !valid:
		m.mu.Unlock()
		l.revoke(ReasonRevoked, "the member was removed, or enrolled again, on the manager")
		return
	}
	// The welcome goes before any call, which the poke below may start.
	if err := l.ws.WriteBinary(encodeJSON(msgWelcome, w)); err != nil {
		m.mu.Unlock()
		l.closeNow(err)
		return
	}
	h := &m.link
	now := m.now().UTC()
	l.mu.Lock()
	l.state, l.credID, l.name, l.since = linkOpen, credID, name, now
	host := l.host
	l.mu.Unlock()
	l.timer.Reset(linkDead)
	l.ln.conns.linked(l.raw)
	h.mu.Lock()
	old := h.links[credID]
	h.links[credID] = l
	st := h.seenLocked(name)
	st.Linked, st.Since, st.Remote, st.Host, st.Refused = true, now, l.remote, host, nil
	h.mu.Unlock()
	n := m.nodes[name]
	m.mu.Unlock()
	if old != nil && old != l {
		_ = old.ws.WriteBinary(encodeJSON(msgRefuse, refuseMsg{Reason: ReasonReplaced, Message: "another link of the member took this one's place"}))
		old.closeNow(nil)
	}
	m.logf("cluster: the member %q linked from %s", name, l.remote)
	if n != nil {
		// Read it now rather than at its next poll.
		n.next.Store(0)
		m.poke()
	}
}

// linkClosed forgets l, whose link closed.
func (m *Manager) linkClosed(l *mlink, err error) {
	h := &m.link
	l.mu.Lock()
	credID, name := l.credID, l.name
	l.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.doc.Members {
		if m.doc.Members[i].credentialID() == credID {
			name = m.doc.Members[i].Name
		}
	}
	h.mu.Lock()
	if h.links[credID] != l {
		h.mu.Unlock()
		return
	}
	delete(h.links, credID)
	if st := h.seen[name]; st != nil {
		st.Linked, st.Since = false, m.now().UTC()
	}
	h.mu.Unlock()
	msg := "closed"
	if err != nil {
		msg = err.Error()
	}
	m.logf("cluster: the member %q's link from %s closed: %s", name, l.remote, msg)
}

// seenLocked returns the link state kept of name, made when there is none;
// h.mu is held.
func (h *linkHub) seenLocked(name string) *LinkState {
	st := h.seen[name]
	if st == nil {
		st = &LinkState{}
		h.seen[name] = st
	}
	return st
}

// noteRefusal keeps r, a hello refused as the member name's.
func (m *Manager) noteRefusal(name string, r *Refusal) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if index(&m.doc, name) < 0 {
		return
	}
	h := &m.link
	h.mu.Lock()
	h.seenLocked(name).Refused = r
	h.mu.Unlock()
}

// syncLinks closes the links whose credential no member has any more, and
// forgets the states of members gone; m.mu is held.
func (m *Manager) syncLinks() {
	h := &m.link
	creds, names := map[string]bool{}, map[string]bool{}
	for i := range m.doc.Members {
		mem := &m.doc.Members[i]
		names[mem.Name] = true
		if id := mem.credentialID(); id != "" && mem.Access == AccessLink {
			creds[id] = true
		}
	}
	var gone []*mlink
	h.mu.Lock()
	for id, l := range h.links {
		if !creds[id] {
			gone = append(gone, l)
			delete(h.links, id)
		}
	}
	for name := range h.seen {
		if !names[name] {
			delete(h.seen, name)
		}
	}
	h.mu.Unlock()
	for _, l := range gone {
		l.mu.Lock()
		name := l.name
		l.mu.Unlock()
		m.logf("cluster: the member %q's link from %s is closed: its credential is gone", name, l.remote)
		// Not under m.mu, which the link's close takes.
		go l.revoke(ReasonRevoked, "the member was removed, or enrolled again, on the manager")
	}
}

// linkStateOf returns the state of mem's link; m.mu is held.
func (m *Manager) linkStateOf(mem *Member) *LinkState {
	h := &m.link
	st := LinkState{}
	h.mu.Lock()
	if s := h.seen[mem.Name]; s != nil {
		st = *s
		if s.Refused != nil {
			r := *s.Refused
			st.Refused = &r
		}
	}
	l := h.links[mem.credentialID()]
	h.mu.Unlock()
	st.Linked = l != nil
	if l != nil {
		l.mu.Lock()
		st.Since, st.Remote, st.Host = l.since, l.remote, l.host
		l.mu.Unlock()
	}
	if mem.Link != nil {
		st.Enrolled = mem.Link.CredentialHash != ""
		if mem.Link.TokenHash != "" {
			st.EnrollBy = mem.Link.TokenExpires
		}
	}
	return &st
}

// linkOf returns the link open for the credential id, or nil.
func (m *Manager) linkOf(id string) *mlink {
	if id == "" {
		return nil
	}
	h := &m.link
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.links[id]
}

// linkCall is a call over a link.
type linkCall struct {
	id    uint64
	l     *mlink
	t     *linkTransport
	done  func(*reply, error)
	timer *time.Timer
	once  sync.Once
}

func (k *linkCall) finish(r *reply, err error) {
	k.once.Do(func() {
		k.timer.Stop()
		k.t.untrack(k)
		k.done(r, err)
	})
}

// take removes the call id from l, returning it; nil when it was taken
// already.
func (l *mlink) take(id uint64) *linkCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := l.calls[id]
	delete(l.calls, id)
	return k
}

// call sends a request to the member over l.
func (l *mlink) call(t *linkTransport, method, path string, header stdhttp.Header, body []byte, done func(*reply, error)) {
	if int64(len(body)) > t.lim.maxBytes {
		done(nil, errLinkBody)
		return
	}
	k := &linkCall{l: l, t: t, done: done}
	l.mu.Lock()
	switch {
	case l.state != linkOpen:
		l.mu.Unlock()
		done(nil, errLinkClosed)
		return
	case len(l.calls) >= maxLinkCalls:
		l.mu.Unlock()
		done(nil, errLinkBusy)
		return
	}
	l.next++
	k.id = l.next
	l.calls[k.id] = k
	k.timer = time.AfterFunc(t.lim.timeout, func() {
		if l.take(k.id) == k {
			_ = l.ws.WriteBinary(encodeCancel(k.id))
			k.finish(nil, errLinkTimeout)
		}
	})
	l.mu.Unlock()
	if !t.track(k) {
		if l.take(k.id) == k {
			k.finish(nil, errClosed)
		}
		return
	}
	head := requestHead{Method: method, Path: path, Header: header, Timeout: t.lim.timeout.Milliseconds(), Max: t.lim.maxBytes}
	if err := l.ws.WriteBinary(encodeCall(msgRequest, k.id, head, body)); err != nil {
		if l.take(k.id) == k {
			k.finish(nil, errLinkClosed)
		}
	}
}

// answer takes a member's answer to a call.
func (l *mlink) answer(data []byte) {
	var head responseHead
	id, body, err := decodeCall(data, &head)
	if err != nil {
		l.closeNow(err)
		return
	}
	k := l.take(id)
	if k == nil {
		return // given up on
	}
	switch head.Error {
	case "":
		k.finish(&reply{status: head.Status, header: head.Header, body: append([]byte(nil), body...)}, nil)
	case answerTooLarge:
		k.finish(nil, errLinkTooLarge)
	case answerTimeout:
		k.finish(nil, errLinkTimeout)
	default:
		k.finish(nil, errLinkBusy)
	}
}

// linkTransport is a linked member's caller: its calls go over the link
// its credential holds, whichever is open when they start.
type linkTransport struct {
	m      *Manager
	credID string
	lim    limits

	mu     sync.Mutex
	calls  map[*linkCall]struct{}
	closed bool
}

func newLinkTransport(m *Manager, credID string) *linkTransport {
	return &linkTransport{m: m, credID: credID, lim: m.lim, calls: map[*linkCall]struct{}{}}
}

func (t *linkTransport) do(method, path string, header stdhttp.Header, body []byte, done func(*reply, error)) {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		done(nil, errClosed)
		return
	}
	l := t.m.linkOf(t.credID)
	if l == nil {
		done(nil, errNotLinked)
		return
	}
	l.call(t, method, path, header, body, done)
}

// track counts k among the transport's calls; false once it is closed.
func (t *linkTransport) track(k *linkCall) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.calls[k] = struct{}{}
	return true
}

func (t *linkTransport) untrack(k *linkCall) {
	t.mu.Lock()
	delete(t.calls, k)
	t.mu.Unlock()
}

func (t *linkTransport) certificate() *CertInfo { return nil }

func (t *linkTransport) abandon() {
	t.mu.Lock()
	calls := make([]*linkCall, 0, len(t.calls))
	for k := range t.calls {
		calls = append(calls, k)
	}
	t.mu.Unlock()
	for _, k := range calls {
		if k.l.take(k.id) == k {
			_ = k.l.ws.WriteBinary(encodeCancel(k.id))
			k.finish(nil, errClosed)
		}
	}
}

// close abandons the calls in flight; the link stays the member's.
func (t *linkTransport) close() {
	t.mu.Lock()
	t.closed = true
	t.mu.Unlock()
	t.abandon()
}

func (t *linkTransport) failure(err error, _ time.Time) *Problem {
	p := &Problem{Kind: KindUnreachable, Message: err.Error()}
	switch {
	case errors.Is(err, errNotLinked):
		p.Kind, p.Message = KindNotLinked, t.m.notLinkedMessage(t.credID)
	case errors.Is(err, errDeadline):
		p.Kind = KindTimeout
	case errors.Is(err, errLinkTooLarge), errors.Is(err, errLinkBody):
		p.Kind = KindTooLarge
	}
	return p
}

// notLinkedMessage says why the member of the credential id has no link.
func (m *Manager) notLinkedMessage(id string) string {
	if id == "" {
		return "the member has not enrolled yet: paste the config line its enrollment token came with into its config, and start it"
	}
	if m.link.ln == nil {
		return "the manager takes no links: set admin.cluster.link_address"
	}
	return "the member has no link to the manager now: it links again, with backoff, once it reaches the manager"
}

// linkConns wraps the link listener's handler: it counts the connections,
// at most max in all and maxPending that have not linked, and keeps the fib
// connection under each WebSocket by its remote address (fib's
// websocket.Connection does not give it; fib API request A52), so that a
// dead link is closed at once.
type linkConns struct {
	fib.Handler
	max, maxPending int
	byAddr          sync.Map // remote address → *fib.Connection
	closed          atomic.Bool

	mu      sync.Mutex
	conns   map[*fib.Connection]bool // true: not linked yet
	pending int
}

func (t *linkConns) OnOpen(c *fib.Connection) {
	t.mu.Lock()
	ok := !t.closed.Load() && len(t.conns) < t.max && t.pending < t.maxPending
	if ok {
		t.conns[c] = true
		t.pending++
	}
	t.mu.Unlock()
	if !ok {
		_ = c.Close()
		return
	}
	if a := c.RemoteAddr(); a != nil {
		t.byAddr.Store(a.String(), c)
	}
	t.Handler.OnOpen(c)
}

func (t *linkConns) OnClose(c *fib.Connection, err error) {
	t.mu.Lock()
	pending, ok := t.conns[c]
	delete(t.conns, c)
	if pending {
		t.pending--
	}
	t.mu.Unlock()
	if !ok {
		return // closed as it opened
	}
	if a := c.RemoteAddr(); a != nil {
		t.byAddr.CompareAndDelete(a.String(), c)
	}
	t.Handler.OnClose(c, err)
}

// linked takes c off the connections that have not linked.
func (t *linkConns) linked(c *fib.Connection) {
	if c == nil {
		return
	}
	t.mu.Lock()
	if t.conns[c] {
		t.conns[c] = false
		t.pending--
	}
	t.mu.Unlock()
}

func (t *linkConns) lookup(addr string) *fib.Connection {
	if v, ok := t.byAddr.Load(addr); ok {
		return v.(*fib.Connection)
	}
	return nil
}

func (t *linkConns) OnShutdown(c *fib.Connection) {
	if h, ok := t.Handler.(fib.ShutdownHandler); ok {
		h.OnShutdown(c)
	}
}

func (t *linkConns) OnEngineClose(c *fib.Connection) {
	if h, ok := t.Handler.(fib.EngineCloseHandler); ok {
		h.OnEngineClose(c)
	}
}

// closeAll closes every connection, and every one opened from now on.
func (t *linkConns) closeAll() {
	t.closed.Store(true)
	t.byAddr.Range(func(_, v any) bool {
		_ = v.(*fib.Connection).Close()
		return true
	})
}

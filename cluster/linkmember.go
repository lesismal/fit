package cluster

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/certpin"
	"github.com/lesismal/fit/internal/linktoken"
)

// MemberSection is the store's section a member keeps its credential in,
// and MemberFileSuffix what the file beside the config that keeps it
// without a store ends with, as fit.cluster-member beside fit.json.
const (
	MemberSection    = "cluster_member"
	MemberFileSuffix = ".cluster-member"
)

// The bounds of a member's side of its link.
const (
	// memberWorkers serve the manager's requests, at most memberQueue
	// waiting; one more is answered busy at once.
	memberWorkers = 2
	memberQueue   = 16
	// maxMemberTimeout bounds a request's time on the member, whatever
	// the manager asks.
	maxMemberTimeout = 5 * time.Minute
)

// Keeper keeps a member's credential where it outlives a restart.
type Keeper interface {
	// Load returns what Save kept, or nil when nothing is.
	Load() ([]byte, error)
	// Save keeps data, or forgets what is kept when data is nil.
	Save(data []byte) error
}

// StoreKeeper keeps the credential in the store's section MemberSection.
func StoreKeeper(st Store) Keeper { return storeKeeper{st} }

type storeKeeper struct{ st Store }

func (k storeKeeper) Load() ([]byte, error)  { return k.st.Section(MemberSection), nil }
func (k storeKeeper) Save(data []byte) error { return k.st.SetSection(MemberSection, data) }

// FileKeeper keeps the credential in the file path, mode 0600.
func FileKeeper(path string) Keeper { return fileKeeper(path) }

type fileKeeper string

func (k fileKeeper) Load() ([]byte, error) {
	data, err := os.ReadFile(string(k))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return data, err
}

func (k fileKeeper) Save(data []byte) error {
	path := string(k)
	if data == nil {
		err := os.Remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if err = errors.Join(werr, f.Sync(), f.Close()); err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// MemoryKeeper keeps the credential until fit stops.
func MemoryKeeper() Keeper { return &memKeeper{} }

type memKeeper struct {
	mu   sync.Mutex
	data []byte
}

func (k *memKeeper) Load() ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return bytes.Clone(k.data), nil
}

func (k *memKeeper) Save(data []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.data = bytes.Clone(data)
	return nil
}

// kept is a member's credential as it keeps it: for the manager whose
// certificate Fingerprint is, given for the enrollment token TokenID.
type kept struct {
	Manager     string    `json:"manager"`
	Fingerprint string    `json:"fingerprint"`
	TokenID     string    `json:"token_id"`
	Credential  string    `json:"credential"`
	Name        string    `json:"name"`
	Enrolled    time.Time `json:"enrolled"`
}

// MemberNode is who the member says it is in its hello.
type MemberNode struct {
	ID, Hostname, FitVersion string
}

// MemberOptions are what a member's link to its manager needs.
type MemberOptions struct {
	// Engine is the running engine the link is dialed on.
	Engine *fib.Engine
	// Settings is admin.cluster_member.
	Settings *config.ClusterMember
	// API serves the manager's requests: the Admin API in process
	// (admin.LinkAPI). Its handlers may block: they run on the link's own
	// workers.
	API stdhttp.Handler
	// MaxRequestBytes bounds a request's body, as the Admin API's
	// max_config_bytes does; 4 MiB when 0.
	MaxRequestBytes int64
	// Keeper keeps the credential; MemoryKeeper when nil.
	Keeper Keeper
	// Node is who the member is.
	Node MemberNode
	// Logf says how the link goes; never a token or a credential.
	Logf func(format string, args ...any)
}

// MemberStatus is how a member's link goes.
type MemberStatus struct {
	// Linked is a link open now, as Name, the member's name on the
	// manager, since Since.
	Linked bool
	Name   string
	Since  time.Time
	// Stopped says why the member no longer links, or "".
	Stopped string
	// Err is why the last link, or the last try, ended.
	Err string
}

// MemberLink is a member's link to its manager: dialed, enrolled once,
// and dialed again with backoff whenever it ends, until Close.
type MemberLink struct {
	o       MemberOptions
	url     string
	pin     []byte
	fp      string // the pin, as fit writes pins
	token   *linktoken.Secret
	maxBody int64

	stop    chan struct{}
	done    chan struct{}
	jobs    chan func()
	workers sync.WaitGroup
	once    sync.Once

	mu       sync.Mutex
	kept     *kept
	badToken string // an enrollment token the manager refused for good
	status   MemberStatus
	inflight map[uint64]context.CancelFunc
}

// StartMember starts linking to the manager o.Settings names, and returns
// at once: the link is dialed, and dialed again, on a goroutine of its own.
// It reads token_file and what the keeper keeps; an error is a setting
// that cannot be used.
func StartMember(o MemberOptions) (*MemberLink, error) {
	s := o.Settings
	switch {
	case s == nil:
		return nil, errors.New("cluster: admin.cluster_member is not set")
	case o.Engine == nil || o.API == nil:
		return nil, errors.New("cluster: no engine, or no API to serve the manager")
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Keeper == nil {
		o.Keeper = MemoryKeeper()
	}
	if o.MaxRequestBytes <= 0 {
		o.MaxRequestBytes = 4 << 20
	}
	pin, err := certpin.Parse(s.Fingerprint)
	if err != nil {
		return nil, fmt.Errorf("cluster: admin.cluster_member.fingerprint: %w", err)
	}
	l := &MemberLink{o: o, url: s.Manager, pin: pin, fp: formatFingerprint(pin), maxBody: o.MaxRequestBytes,
		stop: make(chan struct{}), done: make(chan struct{}), jobs: make(chan func(), memberQueue), inflight: map[uint64]context.CancelFunc{}}
	tok := s.Token
	if s.TokenFile != "" {
		data, err := os.ReadFile(s.TokenFile)
		if err != nil {
			return nil, fmt.Errorf("cluster: admin.cluster_member.token_file: %w", err)
		}
		tok = strings.TrimSpace(string(data))
	}
	if tok != "" {
		secret, err := linktoken.ParseAs(tok, config.ClusterEnrollPrefix)
		if err != nil {
			return nil, fmt.Errorf("cluster: admin.cluster_member's token: %s", config.CheckClusterEnrollToken(tok))
		}
		l.token = &secret
	}
	if data, err := o.Keeper.Load(); err != nil {
		o.Logf("cluster: the credential kept for the manager cannot be read (%v): this fit enrolls again", err)
	} else if len(data) > 0 {
		var k kept
		if err := json.Unmarshal(data, &k); err != nil || k.Credential == "" {
			o.Logf("cluster: the credential kept for the manager is not one: this fit enrolls again")
		} else {
			l.kept = &k
		}
	}
	for range memberWorkers {
		l.workers.Go(func() {
			for {
				select {
				case <-l.stop:
					return
				case job := <-l.jobs:
					job()
				}
			}
		})
	}
	go l.run()
	return l, nil
}

// Close stops linking, closes the link, and waits for the link's goroutines
// to end, the requests under way canceled.
func (l *MemberLink) Close() {
	l.once.Do(func() {
		close(l.stop)
		<-l.done
		l.mu.Lock()
		for _, cancel := range l.inflight {
			cancel()
		}
		l.mu.Unlock()
		l.workers.Wait()
	})
}

// Status returns how the link goes.
func (l *MemberLink) Status() MemberStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

func (l *MemberLink) logf(format string, args ...any) { l.o.Logf(format, args...) }

// errNothing is a member with nothing to link with.
type errNothing string

func (e errNothing) Error() string { return string(e) }

// choose picks how to link: with the credential kept for this manager,
// unless the config's token is another; or with the config's token.
func (l *MemberLink) choose() (mode string, secret linktoken.Secret, err error) {
	l.mu.Lock()
	k, bad := l.kept, l.badToken
	l.mu.Unlock()
	if k != nil && k.Fingerprint == l.fp && (l.token == nil || l.token.ID == k.TokenID) {
		s, err := linktoken.ParseAs(k.Credential, credentialPrefix)
		if err == nil {
			return modeLink, s, nil
		}
		l.logf("cluster: the credential kept for the manager is not one: this fit enrolls again")
	}
	if l.token != nil && l.token.ID != bad {
		return modeEnroll, *l.token, nil
	}
	if l.token != nil {
		return "", linktoken.Secret{}, errNothing("the manager refused the enrollment token, and this fit has no credential of its own: make it a new token on the manager's Nodes page, set it as admin.cluster_member.token, and restart fit")
	}
	return "", linktoken.Secret{}, errNothing("this fit has no credential for the manager and no enrollment token: make it a token on the manager's Nodes page (Add by enrollment), set it as admin.cluster_member.token, and restart fit")
}

// run links, and links again, until the member stops linking.
func (l *MemberLink) run() {
	defer close(l.done)
	backoff := linkBackoffMin
	for {
		mode, secret, err := l.choose()
		if err != nil {
			l.mu.Lock()
			l.status.Stopped, l.status.Linked = err.Error(), false
			l.mu.Unlock()
			l.logf("cluster: not linking to the manager: %v", err)
			<-l.stop
			return
		}
		out := l.attempt(mode, secret)
		switch out.kind {
		case outStop:
			return
		case outLinked:
			backoff = linkBackoffMin
		case outForget:
			if mode == modeEnroll {
				l.mu.Lock()
				l.badToken = secret.ID
				l.mu.Unlock()
			} else {
				l.forget()
			}
			continue
		}
		l.mu.Lock()
		l.status.Linked, l.status.Err = false, out.err
		l.mu.Unlock()
		wait := backoff/2 + rand.N(backoff/2+1)
		l.logf("cluster: the link to the manager at %s ended: %s; linking again in %s", l.url, out.err, wait.Round(time.Millisecond))
		select {
		case <-l.stop:
			return
		case <-time.After(wait):
		}
		backoff = min(2*backoff, linkBackoffMax)
	}
}

// forget forgets the credential kept: the manager no longer knows it.
func (l *MemberLink) forget() {
	l.mu.Lock()
	l.kept = nil
	l.mu.Unlock()
	if err := l.o.Keeper.Save(nil); err != nil {
		l.logf("cluster: the credential the manager no longer knows could not be forgotten: %v", err)
	}
}

// The ends of an attempt.
const (
	outRetry  = iota // try again after the backoff
	outLinked        // the link was open, and closed: try again soon
	outForget        // the token or credential is refused for good
	outStop          // the member stops linking
)

type outcome struct {
	kind int
	err  string
}

// The events of an attempt, from the link's callbacks.
type memberEvent struct {
	kind byte // a message's type, or evDialed and evClosed
	ws   *websocket.Connection
	data []byte
	err  error
}

const (
	evDialed = 'D'
	evClosed = 'C'
)

// attempt dials the manager, says hello in mode with secret, and serves
// the manager's requests until the link closes or the member stops.
func (l *MemberLink) attempt(mode string, secret linktoken.Secret) outcome {
	// Room for the handshake's messages, and for the calls that follow
	// the welcome before the link is taken as open, which the manager
	// bounds by maxLinkCalls.
	events := make(chan memberEvent, maxLinkCalls+8)
	var raw atomic.Pointer[fib.Connection]
	var active atomic.Int64
	active.Store(time.Now().UnixNano())
	var open atomic.Bool
	push := func(ev memberEvent) {
		select {
		case events <- ev:
		default:
			// More than a handshake sends: not a manager.
			if c := raw.Load(); c != nil {
				_ = c.Close()
			}
		}
	}
	handler := websocket.HandlerFuncs{
		Message: func(c *websocket.Connection, _ websocket.Opcode, data []byte) {
			active.Store(time.Now().UnixNano())
			if len(data) == 0 {
				return
			}
			if open.Load() {
				switch data[0] {
				case msgRequest:
					l.serve(c, data[1:])
					return
				case msgCancel:
					l.cancel(data[1:])
					return
				}
			}
			push(memberEvent{kind: data[0], ws: c, data: bytes.Clone(data[1:])})
		},
		Close: func(_ *websocket.Connection, code uint16, reason string, err error) {
			if err == nil && code != 0 {
				err = fmt.Errorf("closed by the manager (%d %s)", code, reason)
			}
			select {
			case events <- memberEvent{kind: evClosed, err: err}:
			default:
			}
		},
		Pong: func(*websocket.Connection, []byte) { active.Store(time.Now().UnixNano()) },
	}
	cfg := websocket.DefaultDialerConfig()
	cfg.HandshakeTimeout = linkHandshake
	cfg.MaxMessageBytes = l.maxBody + maxHeadBytes + 64
	cfg.TLSConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
		// The manager's certificate is the one pinned, whatever its names
		// and dates: it is checked below in place of crypto/tls's checks.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("the manager sent no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if !bytes.Equal(sum[:], l.pin) {
				return fmt.Errorf("the manager's certificate %s is not the one pinned (%s)", formatFingerprint(sum[:]), l.fp)
			}
			return nil
		},
	}
	cfg.Dial = func(e *fib.Engine, network, addr string, timeout time.Duration, h fib.Handler, done func(*fib.Connection, error)) error {
		return e.DialWithHandler(network, addr, timeout, h, func(c *fib.Connection, err error) {
			if c != nil {
				raw.Store(c)
			}
			done(c, err)
		})
	}
	closeRaw := func() {
		if c := raw.Load(); c != nil {
			_ = c.Close()
		}
	}
	websocket.NewDialer(l.o.Engine, cfg).Dial(l.url, stdhttp.Header{"User-Agent": {"fit-cluster-member"}}, handler,
		func(_ *websocket.Connection, _ *stdhttp.Response, err error) {
			if err != nil {
				select {
				case events <- memberEvent{kind: evDialed, err: err}:
				default:
				}
			}
		})
	handshake := time.NewTimer(linkHandshake + time.Second)
	defer handshake.Stop()
	var tick <-chan time.Time
	var ws *websocket.Connection
	var nonce, managerNonce []byte
	for {
		select {
		case <-l.stop:
			closeRaw()
			return outcome{kind: outStop}
		case <-handshake.C:
			closeRaw()
			return outcome{kind: outRetry, err: "the handshake with the manager took too long"}
		case <-tick:
			if time.Since(time.Unix(0, active.Load())) > linkDead {
				closeRaw()
				continue
			}
			_ = ws.Ping(nil)
		case ev := <-events:
			switch ev.kind {
			case evDialed:
				return outcome{kind: outRetry, err: "the manager cannot be reached: " + ev.err.Error()}
			case evClosed:
				l.cancelAll()
				msg := "the link closed"
				if ev.err != nil {
					msg = ev.err.Error()
				}
				if open.Load() {
					return outcome{kind: outLinked, err: msg}
				}
				return outcome{kind: outRetry, err: msg}
			case msgChallenge:
				var ch challengeMsg
				if ws != nil || json.Unmarshal(ev.data, &ch) != nil || len(ch.Nonce) != linktoken.NonceSize {
					closeRaw()
					return outcome{kind: outRetry, err: "the manager's challenge is not one"}
				}
				ws, managerNonce, nonce = ev.ws, ch.Nonce, linktoken.Nonce()
				hello := helloMsg{Mode: mode, ID: secret.ID, Nonce: nonce, Proof: secret.Prove(transcript(secret.ID, nonce, managerNonce, mode)),
					Node: helloNode{ID: l.o.Node.ID, Hostname: l.o.Node.Hostname, FitVersion: l.o.Node.FitVersion}}
				if err := ws.WriteBinary(encodeJSON(msgHello, hello)); err != nil {
					closeRaw()
					return outcome{kind: outRetry, err: err.Error()}
				}
			case msgWelcome:
				var w welcomeMsg
				if ws == nil || open.Load() || json.Unmarshal(ev.data, &w) != nil ||
					secret.VerifyListener(transcript(secret.ID, nonce, managerNonce, mode), w.Proof, w.Name, w.Credential) != nil {
					closeRaw()
					return outcome{kind: outRetry, err: "the manager did not prove the token: it is not the manager that made it"}
				}
				if mode == modeEnroll && !l.enrolled(secret.ID, &w) {
					closeRaw()
					return outcome{kind: outRetry, err: "the manager's credential is not one"}
				}
				handshake.Stop()
				open.Store(true)
				ticker := time.NewTicker(linkPing)
				defer ticker.Stop()
				tick = ticker.C
				l.mu.Lock()
				l.status = MemberStatus{Linked: true, Name: w.Name, Since: time.Now().UTC()}
				l.mu.Unlock()
				l.logf("cluster: linked to the manager at %s as the member %q", l.url, w.Name)
			case msgRequest, msgCancel:
				// Sent right after the welcome, before the link was
				// taken as open.
				if !open.Load() {
					closeRaw()
					return outcome{kind: outRetry, err: "the manager sent a call before its welcome"}
				}
				if ev.kind == msgRequest {
					l.serve(ev.ws, ev.data)
				} else {
					l.cancel(ev.data)
				}
			case msgRefuse, msgRevoked:
				var r refuseMsg
				_ = json.Unmarshal(ev.data, &r)
				closeRaw()
				l.cancelAll()
				msg := "the manager refused the link (" + r.Reason + "): " + r.Message
				if ev.kind == msgRevoked {
					msg = "the manager revoked this fit's credential (" + r.Reason + "): " + r.Message
				}
				switch r.Reason {
				case ReasonUnknown, ReasonProof, ReasonUsed, ReasonExpired, ReasonRevoked:
					l.logf("cluster: %s", msg)
					return outcome{kind: outForget, err: msg}
				}
				if open.Load() {
					return outcome{kind: outLinked, err: msg}
				}
				return outcome{kind: outRetry, err: msg}
			default:
				closeRaw()
				return outcome{kind: outRetry, err: "the manager sent a message that is not one"}
			}
		}
	}
}

// enrolled keeps the credential the welcome w gives for the enrollment
// token tokenID, reporting whether it is one.
func (l *MemberLink) enrolled(tokenID string, w *welcomeMsg) bool {
	if _, err := linktoken.ParseAs(w.Credential, credentialPrefix); err != nil {
		return false
	}
	k := &kept{Manager: l.url, Fingerprint: l.fp, TokenID: tokenID, Credential: w.Credential, Name: w.Name, Enrolled: time.Now().UTC()}
	data, _ := json.Marshal(k) // strings and a time
	l.mu.Lock()
	l.kept = k
	l.mu.Unlock()
	if err := l.o.Keeper.Save(data); err != nil {
		l.logf("cluster: the credential the manager gave cannot be kept (%v): this fit links with it until it stops, and then needs a new enrollment token", err)
	}
	l.logf("cluster: enrolled with the manager at %s as the member %q", l.url, w.Name)
	return true
}

// serve takes a request of the manager's, on a fib worker: it waits for a
// worker of the link's own, or is answered busy at once.
func (l *MemberLink) serve(c *websocket.Connection, data []byte) {
	var head requestHead
	id, body, err := decodeCall(data, &head)
	if err != nil {
		return
	}
	if !strings.HasPrefix(head.Path, "/") || int64(len(body)) > l.maxBody {
		_ = c.WriteBinary(encodeCall(msgResponse, id, responseHead{Status: stdhttp.StatusBadRequest}, []byte(`{"error":"the request is not one the link takes"}`)))
		return
	}
	timeout := min(max(time.Duration(head.Timeout)*time.Millisecond, time.Second), maxMemberTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	l.mu.Lock()
	l.inflight[id] = cancel
	l.mu.Unlock()
	body = bytes.Clone(body)
	job := func() {
		defer l.done1(id, cancel)
		req, err := stdhttp.NewRequestWithContext(ctx, head.Method, "http://cluster-link"+head.Path, bytes.NewReader(body))
		if err != nil {
			_ = c.WriteBinary(encodeCall(msgResponse, id, responseHead{Status: stdhttp.StatusBadRequest}, []byte(`{"error":"the request is not one the link takes"}`)))
			return
		}
		for k, v := range head.Header {
			req.Header[k] = v
		}
		req.RemoteAddr = "cluster-manager:0"
		w := &capWriter{header: stdhttp.Header{}, status: stdhttp.StatusOK, max: head.Max}
		l.o.API.ServeHTTP(w, req)
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			// The manager gave up on it.
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			_ = c.WriteBinary(encodeCall(msgResponse, id, responseHead{Error: answerTimeout}, nil))
		case w.over:
			_ = c.WriteBinary(encodeCall(msgResponse, id, responseHead{Error: answerTooLarge}, nil))
		default:
			_ = c.WriteBinary(encodeCall(msgResponse, id, responseHead{Status: w.status, Header: w.header}, w.body.Bytes()))
		}
	}
	select {
	case l.jobs <- job:
	default:
		l.done1(id, cancel)
		_ = c.WriteBinary(encodeCall(msgResponse, id, responseHead{Error: answerBusy}, nil))
	}
}

// done1 forgets the request id, done.
func (l *MemberLink) done1(id uint64, cancel context.CancelFunc) {
	cancel()
	l.mu.Lock()
	delete(l.inflight, id)
	l.mu.Unlock()
}

// cancel cancels the request a cancel message names.
func (l *MemberLink) cancel(data []byte) {
	if len(data) != 8 {
		return
	}
	id := uint64(data[0])<<56 | uint64(data[1])<<48 | uint64(data[2])<<40 | uint64(data[3])<<32 |
		uint64(data[4])<<24 | uint64(data[5])<<16 | uint64(data[6])<<8 | uint64(data[7])
	l.mu.Lock()
	cancel := l.inflight[id]
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelAll cancels every request under way: the link that carried them
// closed.
func (l *MemberLink) cancelAll() {
	l.mu.Lock()
	for _, cancel := range l.inflight {
		cancel()
	}
	l.mu.Unlock()
}

// capWriter is an answer held whole, up to max bytes; over says it was
// larger.
type capWriter struct {
	header stdhttp.Header
	status int
	wrote  bool
	body   bytes.Buffer
	max    int64
	over   bool
}

func (w *capWriter) Header() stdhttp.Header { return w.header }

func (w *capWriter) WriteHeader(status int) {
	if !w.wrote {
		w.status, w.wrote = status, true
	}
}

func (w *capWriter) Write(p []byte) (int, error) {
	w.wrote = true
	if w.max > 0 && int64(w.body.Len()+len(p)) > w.max {
		w.over = true
		return 0, errLinkTooLarge
	}
	return w.body.Write(p)
}

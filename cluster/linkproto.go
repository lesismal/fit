package cluster

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	stdhttp "net/http"
	"sync"
	"time"

	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/internal/linktoken"
)

// A member that links out to the manager (admin.cluster_member) opens a
// WebSocket to the manager's link listener (admin.cluster.link_address),
// over TLS whose certificate the member pinned when it enrolled. Every
// message is binary, its first byte its type:
//
//	c  challenge  manager → member  {"nonce"}, as the link opens
//	h  hello      member → manager  {"mode", "id", "nonce", "proof", "node"}
//	w  welcome    manager → member  {"name", "credential", "proof"}
//	r  refuse     manager → member  {"reason", "message"}; the link closes
//	g  revoked    manager → member  {"reason", "message"}: the member's
//	              credential is gone; it forgets it, and the link closes
//	q  request    manager → member  id, head {"m", "p", "h", "t", "x"}, body
//	s  response   member → manager  id, head {"s", "h", "e"}, body
//	x  cancel     manager → member  id
//
// The hello proves a token with package linktoken's handshake: in mode
// "enroll" the enrollment token the manager made for the member, used
// once; in mode "link" the credential the manager gave it for that token.
// The transcript binds the protocol's label, the token's ID, both nonces
// and the mode; the welcome proves the token back over the same transcript
// and what the welcome says, so that a member links only to the manager
// that made its token, whatever the TLS. A request's id is the manager's;
// its answer carries it back, and a cancel ends a request the manager gave
// up on. Pings keep the link alive both ways.
const (
	msgChallenge = 'c'
	msgHello     = 'h'
	msgWelcome   = 'w'
	msgRefuse    = 'r'
	msgRevoked   = 'g'
	msgRequest   = 'q'
	msgResponse  = 's'
	msgCancel    = 'x'
)

// The modes of a hello.
const (
	modeEnroll = "enroll"
	modeLink   = "link"
)

// linkLabel is the label of the link's handshake transcripts.
const linkLabel = "fit cluster link v1"

// credentialPrefix starts a member's credential, as the member keeps it.
const credentialPrefix = "fitc1."

// The reasons a manager refuses a hello, or revokes a link.
const (
	// ReasonUnknown: no member has that token or credential.
	ReasonUnknown = "unknown"
	// ReasonProof: the proof does not check.
	ReasonProof = "proof"
	// ReasonUsed: the enrollment token was used already.
	ReasonUsed = "used"
	// ReasonExpired: the enrollment token expired.
	ReasonExpired = "expired"
	// ReasonBusy: the manager could not take the member now; it tries
	// again later.
	ReasonBusy = "busy"
	// ReasonRevoked: the member was removed on the manager, or enrolled
	// again with another token: its credential is gone.
	ReasonRevoked = "revoked"
	// ReasonReplaced: another link with the same credential took this
	// one's place.
	ReasonReplaced = "replaced"
	// ReasonBadHello: the hello is not one.
	ReasonBadHello = "bad_hello"
)

// The bounds of the link.
const (
	// maxHelloBytes bounds a handshake's message.
	maxHelloBytes = 4 << 10
	// maxLinkCalls is how many calls one link carries at once; one more
	// fails at once.
	maxLinkCalls = 16
	// maxHeadBytes bounds a request's or an answer's head.
	maxHeadBytes = 64 << 10
	// maxPendingLinks is how many connections the link listener holds that
	// have not said hello yet; maxLinksSlack the connections it holds
	// beside one per member.
	maxPendingLinks = 32
	maxLinksSlack   = 16
)

// The link's timing, which tests shorten.
var (
	// linkPing is how often a member pings the manager; a link that hears
	// nothing for linkDead is closed by either side.
	linkPing = 15 * time.Second
	linkDead = 45 * time.Second
	// linkHandshake bounds a link's handshake, from its connect to the
	// welcome.
	linkHandshake = 10 * time.Second
	// linkBackoffMin and linkBackoffMax bound a member's wait before it
	// links again.
	linkBackoffMin = time.Second
	linkBackoffMax = time.Minute
)

type challengeMsg struct {
	Nonce []byte `json:"nonce"`
}

// helloMsg is a member's hello; Node says who it is, for the manager's log
// and pages.
type helloMsg struct {
	Mode  string    `json:"mode"`
	ID    string    `json:"id"`
	Nonce []byte    `json:"nonce"`
	Proof []byte    `json:"proof"`
	Node  helloNode `json:"node"`
}

type helloNode struct {
	ID         string `json:"id,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	FitVersion string `json:"fit_version,omitempty"`
}

type welcomeMsg struct {
	Name       string `json:"name"`
	Credential string `json:"credential,omitempty"`
	Proof      []byte `json:"proof"`
}

type refuseMsg struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// transcript is the handshake's transcript of a token id, the member's and
// the manager's nonces, in mode.
func transcript(id string, member, manager []byte, mode string) linktoken.Transcript {
	return linktoken.Transcript{Label: linkLabel, Token: id, Dialer: member, Listener: manager, Extra: []string{mode}}
}

// encodeJSON is a message of type t holding v.
func encodeJSON(t byte, v any) []byte {
	data, _ := json.Marshal(v) // the messages' own types
	return append([]byte{t}, data...)
}

// requestHead is a request's head: its method, path (with its query),
// header, the manager's timeout in milliseconds and the largest answer it
// takes.
type requestHead struct {
	Method  string         `json:"m"`
	Path    string         `json:"p"`
	Header  stdhttp.Header `json:"h,omitempty"`
	Timeout int64          `json:"t"`
	Max     int64          `json:"x"`
}

// responseHead is an answer's head: its status and header, or, with Error,
// why there is none.
type responseHead struct {
	Status int            `json:"s"`
	Header stdhttp.Header `json:"h,omitempty"`
	Error  string         `json:"e,omitempty"`
}

// The errors of a responseHead.
const (
	answerTooLarge = "too_large"
	answerBusy     = "busy"
	answerTimeout  = "timeout"
)

// encodeCall is a request's or an answer's message: its type, id, head and
// body.
func encodeCall(t byte, id uint64, head any, body []byte) []byte {
	h, _ := json.Marshal(head) // the heads' own types
	out := make([]byte, 0, 1+8+4+len(h)+len(body))
	out = append(out, t)
	out = binary.BigEndian.AppendUint64(out, id)
	out = binary.BigEndian.AppendUint32(out, uint32(len(h)))
	out = append(out, h...)
	return append(out, body...)
}

var errBadMessage = errors.New("cluster: a link's message is not one")

// decodeCall reads a request's or an answer's message, after its type: its
// id, head into head, and its body, which is data's.
func decodeCall(data []byte, head any) (uint64, []byte, error) {
	if len(data) < 12 {
		return 0, nil, errBadMessage
	}
	id := binary.BigEndian.Uint64(data)
	n := binary.BigEndian.Uint32(data[8:])
	if n > maxHeadBytes || int(n) > len(data)-12 {
		return 0, nil, errBadMessage
	}
	if err := json.Unmarshal(data[12:12+n], head); err != nil {
		return 0, nil, errBadMessage
	}
	return id, data[12+n:], nil
}

// encodeCancel is a cancel of the request id.
func encodeCancel(id uint64) []byte {
	return binary.BigEndian.AppendUint64([]byte{msgCancel}, id)
}

// linkFlow is one side's sending on a link, paced by what the peer reads
// (fib's WebSocket send backpressure, A53): a message goes out without
// waiting, and once more than the connection's PendingWriteLimit (fib's
// default, 64 KiB) waits for the peer, the side is above its limit until
// what waits falls to half (OnWritable). What a side does meanwhile is its
// own: the manager holds its new calls, the member its reads for a while,
// and answers busy what it reads. Nothing is refused for being above: a
// message sent then still queues.
type linkFlow struct {
	ws *websocket.Connection
	// hold, when not zero, holds the connection's reads for at most that
	// long once the side goes above its limit, once each time.
	hold time.Duration
	// drained runs, outside the lock, when the side falls back to half its
	// limit, or the link ends.
	drained func()

	mu    sync.Mutex
	above bool
	gen   uint64 // the times the side went above, ending a hold of an earlier one
	timer *time.Timer
}

// send sends the message b.
func (f *linkFlow) send(b []byte) error {
	above, err := f.ws.WriteMessageNoWait(websocket.Binary, b)
	if err == nil && above {
		f.over()
	}
	return err
}

// isAbove reports whether the side is above its limit.
func (f *linkFlow) isAbove() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.above
}

// over notes that the side went above its limit, unless it is already.
func (f *linkFlow) over() {
	f.mu.Lock()
	if f.above {
		f.mu.Unlock()
		return
	}
	f.above = true
	f.gen++
	gen := f.gen
	if f.hold > 0 {
		f.ws.HoldReads(true)
		f.timer = time.AfterFunc(f.hold, func() {
			f.mu.Lock()
			if f.above && f.gen == gen {
				f.ws.HoldReads(false)
			}
			f.mu.Unlock()
		})
	}
	f.mu.Unlock()
	if !f.ws.OnWritable(func(error) { f.writable(gen) }) {
		f.writable(gen) // drained already
	}
}

// writable ends the time above the limit that began as gen: what waits fell
// to half, or the link ended.
func (f *linkFlow) writable(gen uint64) {
	f.mu.Lock()
	if !f.above || f.gen != gen {
		f.mu.Unlock()
		return
	}
	f.above = false
	if f.timer != nil {
		f.timer.Stop()
		f.timer = nil
		f.ws.HoldReads(false)
	}
	f.mu.Unlock()
	if f.drained != nil {
		f.drained()
	}
}

// Package websockify is the websockify handler: it bridges a WebSocket to a
// TCP service, as websockify does for noVNC, web terminals and game
// clients, which a browser can reach only over a WebSocket.
//
// The handshake is taken over HTTP/1.1, or as an extended CONNECT over h2
// and h3. The target is fixed by the handler's params: an address, a named
// upstream, or a table of tokens that a query parameter picks from, as
// websockify's token plugin does; the client never names one itself. The
// target is dialed through fib's engine before the WebSocket is accepted,
// so a target that is down, or a token not listed, is an HTTP answer the
// client can tell apart. Once both sides are open, each binary message the
// client sends goes to the target as its bytes, and each read from the
// target goes to the client as a binary message; with the base64
// subprotocol, as websockify offers it, both ways are text messages of
// base64 instead.
//
// Nothing waits. Each side's data is sent on from the callback that
// received it, and a side's reads are held while the other has more than
// watermark of it unsent: the target's reads while the client is slow to
// take its messages (WriteMessageNoWait and OnWritable), the client's while
// the target is slow to take its bytes (NotifyOutput). Both connections'
// own write backpressure is off, as fib's relays have it, since the bridge
// paces each by the other.
package websockify

import (
	"errors"
	"hash/maphash"
	"net"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"
	"github.com/lesismal/fib/websocket"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/upstream"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".websockify", New: func() any { return new(Websockify) },
		Doc: "Bridges a WebSocket to a TCP service, as websockify does for noVNC and web terminals: the client's binary messages go to the target as bytes and the target's bytes come back as binary messages, or as text messages of base64 with the base64 subprotocol. The target is an address, a named upstream, or one a token in the query picks; never one the client names."})
}

// The defaults of the handler's params.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultIdleTimeout    = 10 * time.Minute
	DefaultMaxConns       = 1024
	DefaultMaxFrameBytes  = 1 << 20
	DefaultTokenParam     = "token"
	// MaxFrameBytes bounds max_frame_bytes.
	MaxFrameBytes = 64 << 20
)

// watermark is how much may wait to go to one side before the other side's
// reads are held: the WebSocket's PendingWriteLimit, and the target's
// NotifyOutput limit.
const watermark = 64 << 10

// maxAttempts bounds the addresses of an upstream a bridge's dial is tried
// on.
const maxAttempts = 3

// subprotocols are those offered, as websockify offers them. A client that
// offers none gets binary.
var subprotocols = []string{"binary", "base64"}

// Websockify is the handler websockify.
type Websockify struct {
	Address        string            `json:"address,omitempty" doc:"The TCP service every WebSocket is bridged to, a host and a port, as \"127.0.0.1:5900\". One of address, upstream and tokens."`
	Upstream       string            `json:"upstream,omitempty" doc:"The upstream whose addresses the WebSockets are bridged to, picked by its load_balancing; a connect that fails is tried on the next address, up to 3, and its connect_timeout, deny_internal, max_fails and health checks apply. One of address, upstream and tokens."`
	Tokens         map[string]string `json:"tokens,omitempty" doc:"Targets by token, as websockify's token plugin keeps them, each a host and a port: {\"desk1\": \"10.0.0.5:5900\"}. The query parameter token_param names a request's token; a request without one, or with one not listed, is answered 403. One of address, upstream and tokens."`
	TokenParam     string            `json:"token_param,omitempty" default:"\"token\"" doc:"With tokens, the query parameter that names a request's token, as in /websockify?token=desk1. token when left out."`
	Origins        []string          `json:"origins,omitempty" doc:"The origins whose pages may open the WebSocket, as \"https://example.com\", or \"*\" for any; a request with another Origin is answered 403, and one without any, which is not from a browser, is let through. When left out, only a page of the host the request is for (its Origin names the request's Host), which keeps other sites from opening it with the user's cookies."`
	ConnectTimeout config.Duration   `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long connecting to the target may take; past it the request is answered 504. With upstream, the upstream's connect_timeout applies instead. 10s when left out or 0."`
	IdleTimeout    config.Duration   `json:"idle_timeout,omitempty" default:"\"10m\"" doc:"How long a bridge may carry nothing either way before it is closed: the WebSocket with 1001 and the target's connection with it. 10m when left out or 0."`
	MaxConns       int               `json:"max_conns,omitempty" min:"0" default:"1024" doc:"The most WebSockets the handler bridges, or is opening, at once; one past it is answered 503. 1024 when left out or 0."`
	MaxFrameBytes  int64             `json:"max_frame_bytes,omitempty" min:"0" max:"67108864" default:"1048576" doc:"The largest WebSocket frame the client may send, which is what a browser sends a message as; a larger one closes the WebSocket with 1009. Messages are passed on frame by frame, so a message of many frames may be larger. 1 MiB when left out or 0."`
	Name           string            `json:"name,omitempty" doc:"The bridge's name in the metrics, the label bridge of the fit_websockify_ families. The address, the upstream's name, or tokens when left out."`

	engine  *fib.Engine
	graph   module.Holder
	pool    *upstream.Pool
	tokens  map[string]string
	param   string
	origins map[string]struct{}
	any     bool // origins has "*"
	connect time.Duration
	idle    time.Duration
	max     int64
	tries   int
	ws      *websocket.ServerHandler
	st      *stats
	conns   atomic.Int64
	seed    maphash.Seed
	shards  [shardCount]shard
}

// shardCount is how many shards a handler's bridges are kept in.
const shardCount = 32

// shard keeps some of a handler's bridges, for a drain. A bridge's
// WebSocket carries the bridge as its attachment, for its callbacks.
type shard struct {
	mu  sync.Mutex
	all map[*bridge]struct{}
}

// Provision checks the params and builds the WebSocket handler.
func (w *Websockify) Provision(ctx *module.Context) error {
	set := 0
	if w.Address != "" {
		set++
		if err := checkAddress(w.Address); err != nil {
			return ctx.Errorf("address", "%v", err)
		}
	}
	if w.Upstream != "" {
		set++
		pool, ok := ctx.Upstream(w.Upstream)
		if !ok {
			return ctx.Errorf("upstream", "there is no upstream named %q", w.Upstream)
		}
		w.pool = pool
		w.tries = min(maxAttempts, len(pool.Backends()))
	}
	if len(w.Tokens) > 0 {
		set++
		for token, addr := range w.Tokens {
			if token == "" {
				return ctx.Errorf("tokens", "a token is empty")
			}
			if err := checkAddress(addr); err != nil {
				return ctx.Params.Field("tokens").Field(token).Errorf("%v", err)
			}
		}
		w.tokens = w.Tokens
	}
	if set != 1 {
		return ctx.Errorf("", "set one of address, upstream and tokens")
	}
	w.param = w.TokenParam
	if w.param == "" {
		w.param = DefaultTokenParam
	}
	for _, o := range w.Origins {
		if o == "*" {
			w.any = true
			continue
		}
		o = strings.ToLower(strings.TrimSuffix(o, "/"))
		if !strings.Contains(o, "://") {
			return ctx.Errorf("origins", "%q is not an origin, as https://example.com", o)
		}
		if w.origins == nil {
			w.origins = map[string]struct{}{}
		}
		w.origins[o] = struct{}{}
	}
	w.connect = orDefault(w.ConnectTimeout.Std(), DefaultConnectTimeout)
	w.idle = orDefault(w.IdleTimeout.Std(), DefaultIdleTimeout)
	w.max = int64(w.MaxConns)
	if w.max <= 0 {
		w.max = DefaultMaxConns
	}
	frame := w.MaxFrameBytes
	if frame <= 0 {
		frame = DefaultMaxFrameBytes
	}
	w.engine, w.graph = ctx.Engine(), ctx.Graph()
	w.seed = maphash.MakeSeed()
	// The origin is checked before the target is dialed, so the handshake
	// needs no second look.
	w.ws = websocket.NewHandlerWithConfig(websocket.Config{MaxMessageBytes: frame, Subprotocols: subprotocols,
		CheckOrigin: func(*stdhttp.Request) bool { return true }, PendingWriteLimit: watermark}, (*wsHandler)(w))
	name := w.Name
	switch {
	case name != "":
	case w.Address != "":
		name = w.Address
	case w.Upstream != "":
		name = w.Upstream
	default:
		name = "tokens"
	}
	w.st = takeStats(statsKey{engine: w.engine, name: name})
	return nil
}

// checkAddress reports whether addr is a host and a numeric port.
func checkAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if n, err := strconv.Atoi(port); err != nil || n <= 0 || n > 65535 {
		return errors.New("the port of " + strconv.Quote(addr) + " is not a number from 1 to 65535")
	}
	if host == "" {
		return errors.New(strconv.Quote(addr) + " has no host")
	}
	return nil
}

func orDefault(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

// Cleanup gives back the handler's metrics, once its last bridge is over.
func (w *Websockify) Cleanup() {
	if w.st != nil {
		giveStats(w.st)
		w.st = nil
	}
}

// Drain closes the bridges still open, and gives up those being opened.
func (w *Websockify) Drain() {
	var open []*bridge
	for i := range w.shards {
		s := &w.shards[i]
		s.mu.Lock()
		for b := range s.all {
			open = append(open, b)
		}
		s.mu.Unlock()
	}
	for _, b := range open {
		b.abandon(errDrained)
	}
}

// The answers to a request that does not get a bridge.
var (
	errDrained = errors.New("websockify: the config was drained")
	errIdle    = errors.New("websockify: the bridge was idle past its timeout")
)

// ServeHTTP checks a handshake, picks its target and dials it. It returns
// while the target is dialed, and the dial's callback answers the request.
func (w *Websockify) ServeHTTP(c *fibhttp.Context) {
	r := c.Request
	if !isHandshake(r) {
		w.st.refused.Add(1)
		h := stdhttp.Header{"Content-Type": {"text/plain; charset=utf-8"}, "Sec-Websocket-Version": {"13"}}
		if r.ProtoMajor == 1 {
			h["Upgrade"] = []string{"websocket"}
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusUpgradeRequired, Header: h,
			Body: []byte("426 Upgrade Required: a WebSocket handshake is expected\n")})
		return
	}
	if !w.originAllowed(r) {
		w.refuse(c, stdhttp.StatusForbidden)
		return
	}
	addr := w.Address
	if w.tokens != nil {
		var ok bool
		if addr, ok = w.tokens[queryValue(r.URL.RawQuery, w.param)]; !ok {
			w.refuse(c, stdhttp.StatusForbidden)
			return
		}
	}
	if w.engine == nil || w.conns.Add(1) > w.max {
		if w.engine != nil {
			w.conns.Add(-1)
		}
		w.st.busy.Add(1)
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", statusBody(stdhttp.StatusServiceUnavailable))
		return
	}
	b := &bridge{w: w, c: c, addr: addr}
	// The request is held until the WebSocket is open, or answered; the
	// config until the bridge is over.
	c.Retain()
	w.graph.Retain()
	b.add()
	c.OnCancel(func(error) { b.abandon(errClientGone) })
	b.dial()
}

// errClientGone is why a bridge whose client left before it opened is given
// up.
var errClientGone = errors.New("websockify: the client went away")

// refuse answers a request whose origin or token is refused.
func (w *Websockify) refuse(c *fibhttp.Context, status int) {
	w.st.refused.Add(1)
	_ = c.Respond(status, "text/plain; charset=utf-8", statusBody(status))
}

// statusBody is the body of a plain answer of status.
func statusBody(status int) []byte {
	return []byte(strconv.Itoa(status) + " " + stdhttp.StatusText(status) + "\n")
}

// isHandshake reports whether r asks to open a WebSocket: a GET with
// Upgrade: websocket over HTTP/1.1, or an extended CONNECT for websocket
// over h2 and h3. Upgrade checks the rest of it.
func isHandshake(r *stdhttp.Request) bool {
	if r.ProtoMajor >= 2 {
		return r.Method == stdhttp.MethodConnect && strings.EqualFold(r.Header.Get(":protocol"), "websocket")
	}
	return r.Method == stdhttp.MethodGet && hasToken(r.Header["Upgrade"], "websocket") &&
		hasToken(r.Header["Connection"], "upgrade")
}

// hasToken reports whether a comma-separated field's values include token,
// ignoring case.
func hasToken(values []string, token string) bool {
	for _, value := range values {
		for item := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(item), token) {
				return true
			}
		}
	}
	return false
}

// originAllowed reports whether r's Origin may open the WebSocket.
func (w *Websockify) originAllowed(r *stdhttp.Request) bool {
	origin := r.Header["Origin"]
	switch {
	case len(origin) == 0 || w.any:
		return true
	case w.origins == nil:
		return websocket.CheckSameOrigin(r)
	case len(origin) != 1:
		return false
	}
	_, ok := w.origins[strings.ToLower(origin[0])]
	return ok
}

// queryValue returns the first value of the query parameter name in raw,
// unescaped, or "" when there is none or it cannot be unescaped.
func queryValue(raw, name string) string {
	for part := range strings.SplitSeq(raw, "&") {
		key, value, _ := strings.Cut(part, "=")
		if key != name {
			continue
		}
		v, err := url.QueryUnescape(value)
		if err != nil {
			return ""
		}
		return v
	}
	return ""
}

// shardOf returns the shard b is kept in.
func shardOf(w *Websockify, b *bridge) *shard {
	return &w.shards[maphash.Comparable(w.seed, b)%shardCount]
}

// wsHandler is the handler of the WebSockets a Websockify bridges.
type wsHandler Websockify

// OnOpen readies the WebSocket of the bridge it carries. It runs before
// Upgrade returns, and before any frame of the client's.
func (h *wsHandler) OnOpen(conn *websocket.Connection, _ *stdhttp.Request) {
	b := conn.Attachment().(*bridge)
	b.b64 = conn.Subprotocol() == "base64"
	conn.SetWriteBackpressure(false)
}

// OnMessage is never called for a data message: OnFrame takes them.
func (h *wsHandler) OnMessage(*websocket.Connection, websocket.Opcode, []byte) {}

// OnFrame passes a frame the client sent to the target.
func (h *wsHandler) OnFrame(conn *websocket.Connection, _ websocket.Opcode, fin bool, payload []byte) {
	conn.Attachment().(*bridge).fromClient(conn, fin, payload)
}

// OnClose ends the target's side once what the client sent has gone.
func (h *wsHandler) OnClose(conn *websocket.Connection, _ uint16, _ string, _ error) {
	conn.Attachment().(*bridge).clientClosed()
}

// add keeps b among the handler's bridges, for a drain.
func (b *bridge) add() {
	s := shardOf(b.w, b)
	s.mu.Lock()
	if s.all == nil {
		s.all = map[*bridge]struct{}{}
	}
	s.all[b] = struct{}{}
	s.mu.Unlock()
}

// remove lets go of b.
func (b *bridge) remove() {
	s := shardOf(b.w, b)
	s.mu.Lock()
	delete(s.all, b)
	s.mu.Unlock()
}

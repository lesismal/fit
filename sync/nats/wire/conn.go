// Package natswire is a client of NATS, the text protocol of nats-server
// 2.x, and of JetStream's API over it: connections over TCP or TLS,
// authenticated with a token, a user and password, an NKey or a JWT and
// its NKey (a creds file), kept alive with PINGs and reconnected across a
// cluster's servers, those it names in INFO among them; subscriptions,
// publishes with headers, requests through an inbox, and JetStream's
// streams, pull consumers, acknowledgements and publishes with their
// acknowledgements. It is sync's own, written in fit as decision 036 has
// the protocols be. A Client is safe for use by several goroutines; it
// reads on a goroutine of its own, and writes on another, never on a fib
// worker. Lines and payloads are bounded: a line by MaxLine, a message by
// the server's max_payload.
package natswire

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The defaults of a Config's durations.
const (
	DefaultDialTimeout   = 10 * time.Second
	DefaultPingInterval  = 30 * time.Second
	DefaultReconnectWait = time.Second
	// MaxLine bounds a protocol line: an INFO, a MSG's line, an -ERR.
	MaxLine = 64 << 10
	// maxPingsOut is how many PINGs may go unanswered before the
	// connection is taken for dead.
	maxPingsOut = 2
	// defaultMaxPayload is the server's when its INFO says none.
	defaultMaxPayload = 1 << 20
)

// The client's errors.
var (
	ErrClosed       = errors.New("nats: the client is closed")
	ErrDisconnected = errors.New("nats: disconnected from the server")
	ErrMaxPayload   = errors.New("nats: the message is larger than the server's max_payload")
	ErrNoResponders = errors.New("nats: no responders")
	ErrBadSubject   = errors.New("nats: a subject is empty or holds white space")
)

// Config is how a Client connects.
type Config struct {
	// Servers are host:port each; those the servers' INFO names are added.
	Servers []string
	// TLS, when not nil, is upgraded to after the server's INFO; its
	// ServerName is each server's host when empty.
	TLS *tls.Config
	// Name is the client's name, in the server's monitoring.
	Name string
	// Token, User and Password, NKey (a seed) and JWT (with NKey's seed)
	// authenticate; the server's nonce is signed with NKey's key.
	Token          string
	User, Password string
	NKey           *KeyPair
	JWT            string
	// DialTimeout bounds connecting, TLS and the CONNECT's answer.
	DialTimeout time.Duration
	// PingInterval is how often an idle connection is PINGed; two PINGs
	// unanswered close it, and the client connects again.
	PingInterval time.Duration
	// ReconnectWait is how long the client waits after trying every server
	// in vain before it tries them again.
	ReconnectWait time.Duration
}

// Info is what a server's INFO says of it.
type Info struct {
	ServerID     string   `json:"server_id"`
	Version      string   `json:"version"`
	Headers      bool     `json:"headers"`
	MaxPayload   int      `json:"max_payload"`
	AuthRequired bool     `json:"auth_required"`
	TLSRequired  bool     `json:"tls_required"`
	Nonce        string   `json:"nonce"`
	ConnectURLs  []string `json:"connect_urls"`
	JetStream    bool     `json:"jetstream"`
	LameDuck     bool     `json:"ldm"`
}

// Header is a message's headers, each name's values.
type Header map[string][]string

// Get returns name's first value, the name matched without regard to case.
func (h Header) Get(name string) string {
	if v, ok := h[name]; ok && len(v) > 0 {
		return v[0]
	}
	for k, v := range h {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// Msg is a message the client received.
type Msg struct {
	Subject, Reply string
	Header         Header
	Data           []byte
	// Status and Description are a status message's, as JetStream's 404
	// No Messages; 0 for a message.
	Status      int
	Description string
}

// Client is a connection to a NATS server, made again to another of the
// cluster's when it breaks.
type Client struct {
	cfg  Config
	life context.Context
	stop context.CancelFunc
	done <-chan struct{}
	kick chan struct{}
	// dropped counts messages a full subscription did not take.
	dropped atomic.Int64

	mu        sync.Mutex
	conn      net.Conn
	bw        *bufio.Writer
	info      Info
	servers   []string
	cur       int
	closed    bool
	gen       uint64
	subs      map[uint64]*Sub
	sid       uint64
	pongs     []chan error
	pingsOut  int
	errs      uint64 // -ERRs seen
	lastErr   error
	inbox     string
	inboxes   map[string]*Inbox
	token     uint64
	writeTime time.Duration
}

// Dial connects to the first of cfg.Servers that answers, and keeps the
// connection, or another, until Close.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if len(cfg.Servers) == 0 {
		return nil, errors.New("nats: no servers")
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = DefaultDialTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = DefaultPingInterval
	}
	if cfg.ReconnectWait <= 0 {
		cfg.ReconnectWait = DefaultReconnectWait
	}
	var rnd [12]byte
	_, _ = rand.Read(rnd[:]) // crypto/rand's Read does not fail
	c := &Client{cfg: cfg, kick: make(chan struct{}, 1), servers: append([]string(nil), cfg.Servers...),
		subs: map[uint64]*Sub{}, inbox: "_INBOX." + hex.EncodeToString(rnd[:]) + ".", inboxes: map[string]*Inbox{}, writeTime: cfg.DialTimeout}
	c.life, c.stop = context.WithCancel(context.Background())
	c.done = c.life.Done()
	c.sid = 1
	c.subs[1] = &Sub{c: c, sid: 1, subject: c.inbox + "*"}
	var errs []error
	for i, addr := range c.servers {
		conn, br, info, err := c.connect(ctx, addr)
		if err != nil {
			errs = append(errs, err)
			if ctx.Err() != nil {
				break
			}
			continue
		}
		c.cur = i
		c.attach(conn, info)
		go c.read(conn, br)
		go c.write()
		return c, nil
	}
	c.stop()
	return nil, errors.Join(errs...)
}

// Info returns the INFO of the server the client is connected to.
func (c *Client) Info() Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

// Servers returns the servers the client knows, its config's and those
// the servers named.
func (c *Client) Servers() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.servers...)
}

// Connected reports whether the client has a connection now.
func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil
}

// Dropped returns how many messages full subscriptions dropped.
func (c *Client) Dropped() int64 { return c.dropped.Load() }

// connect dials addr, reads its INFO, upgrades to TLS and authenticates,
// waiting for the PONG after CONNECT.
func (c *Client) connect(ctx context.Context, addr string) (net.Conn, *bufio.Reader, Info, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.DialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, Info{}, fmt.Errorf("nats: %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()
	info, br, err := c.handshake(ctx, conn, addr)
	if err != nil {
		conn.Close()
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (%w)", err, ctx.Err())
		}
		return nil, nil, Info{}, fmt.Errorf("nats: %s: %w", addr, err)
	}
	if !stop() {
		conn.Close()
		return nil, nil, Info{}, fmt.Errorf("nats: %s: %w", addr, ctx.Err())
	}
	return info.conn, br, info.Info, nil
}

type handshook struct {
	Info
	conn net.Conn
}

func (c *Client) handshake(ctx context.Context, conn net.Conn, addr string) (handshook, *bufio.Reader, error) {
	br := bufio.NewReaderSize(conn, 32<<10)
	line, err := readLine(br)
	if err != nil {
		return handshook{}, nil, err
	}
	op, arg, _ := strings.Cut(string(line), " ")
	if !strings.EqualFold(op, "INFO") {
		return handshook{}, nil, fmt.Errorf("the server's first line is not INFO: %.80q", line)
	}
	var info Info
	if err := json.Unmarshal([]byte(arg), &info); err != nil {
		return handshook{}, nil, fmt.Errorf("its INFO: %w", err)
	}
	if c.cfg.TLS != nil {
		tc := c.cfg.TLS.Clone()
		if tc.ServerName == "" {
			tc.ServerName, _, _ = net.SplitHostPort(addr)
		}
		t := tls.Client(conn, tc)
		if err := t.HandshakeContext(ctx); err != nil {
			return handshook{}, nil, fmt.Errorf("TLS: %w", err)
		}
		conn, br = t, bufio.NewReaderSize(t, 32<<10)
	} else if info.TLSRequired {
		return handshook{}, nil, errors.New("the server requires TLS, and tls is not set")
	}
	connect := map[string]any{"verbose": false, "pedantic": false, "lang": "go", "version": "fit", "protocol": 1,
		"headers": true, "no_responders": true, "tls_required": c.cfg.TLS != nil}
	if c.cfg.Name != "" {
		connect["name"] = c.cfg.Name
	}
	if c.cfg.Token != "" {
		connect["auth_token"] = c.cfg.Token
	}
	if c.cfg.User != "" {
		connect["user"], connect["pass"] = c.cfg.User, c.cfg.Password
	}
	if c.cfg.NKey != nil {
		if c.cfg.JWT != "" {
			connect["jwt"] = c.cfg.JWT
		} else {
			connect["nkey"] = c.cfg.NKey.Public()
		}
		connect["sig"] = c.cfg.NKey.Sign([]byte(info.Nonce))
	}
	b, _ := json.Marshal(connect)
	b = append(append([]byte("CONNECT "), b...), "\r\nPING\r\n"...)
	if _, err := conn.Write(b); err != nil {
		return handshook{}, nil, err
	}
	for {
		line, err := readLine(br)
		if err != nil {
			return handshook{}, nil, err
		}
		op, arg, _ := strings.Cut(string(line), " ")
		switch strings.ToUpper(op) {
		case "PONG":
			if info.MaxPayload <= 0 {
				info.MaxPayload = defaultMaxPayload
			}
			return handshook{info, conn}, br, nil
		case "-ERR":
			return handshook{}, nil, fmt.Errorf("the server refused the connection: %s", strings.Trim(arg, "' "))
		case "INFO":
			_ = json.Unmarshal([]byte(arg), &info)
		}
	}
}

// readLine reads a line, without its CRLF, of at most MaxLine bytes.
func readLine(br *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := br.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > MaxLine {
			return nil, fmt.Errorf("a line longer than %d bytes", MaxLine)
		}
		if err == nil {
			return bytes.TrimRight(line, "\r\n"), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}

// attach makes conn the client's, subscribing its subscriptions again.
func (c *Client) attach(conn net.Conn, info Info) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn, c.bw, c.info, c.pingsOut = conn, bufio.NewWriterSize(conn, 32<<10), info, 0
	c.gen++
	c.learn(info)
	for _, s := range c.subs {
		c.writeSub(s)
	}
	c.flushLocked()
}

// learn adds the servers info names.
func (c *Client) learn(info Info) {
	for _, u := range info.ConnectURLs {
		known := false
		for _, s := range c.servers {
			known = known || s == u
		}
		if !known {
			c.servers = append(c.servers, u)
		}
	}
}

// read reads the connection's messages until it breaks, then connects
// again, until Close.
func (c *Client) read(conn net.Conn, br *bufio.Reader) {
	for {
		err := c.readLoop(br)
		c.lost(conn, err)
		if conn, br = c.reconnect(); conn == nil {
			return
		}
	}
}

// lost drops a broken connection: its PONGs and inboxes waiting fail.
func (c *Client) lost(conn net.Conn, err error) {
	conn.Close()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		c.conn, c.bw = nil, nil
	}
	c.lastErr = err
	for _, p := range c.pongs {
		p <- ErrDisconnected
	}
	c.pongs = nil
	for k, ib := range c.inboxes {
		close(ib.lost)
		delete(c.inboxes, k)
		if ib.sid != 0 {
			delete(c.subs, ib.sid)
		}
	}
}

// reconnect tries the servers, from the one after the last, until one
// answers or the client is closed; nil when it is.
func (c *Client) reconnect() (net.Conn, *bufio.Reader) {
	for {
		c.mu.Lock()
		servers, start := append([]string(nil), c.servers...), c.cur+1
		c.mu.Unlock()
		for i := range servers {
			n := (start + i) % len(servers)
			conn, br, info, err := c.connect(c.life, servers[n])
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				if conn != nil {
					conn.Close()
				}
				return nil, nil
			}
			c.mu.Unlock()
			if err == nil {
				c.mu.Lock()
				c.cur = n
				c.mu.Unlock()
				c.attach(conn, info)
				return conn, br
			}
		}
		t := time.NewTimer(c.cfg.ReconnectWait)
		select {
		case <-t.C:
		case <-c.done:
			t.Stop()
			return nil, nil
		}
	}
}

// readLoop reads the server's lines and messages until an error.
func (c *Client) readLoop(br *bufio.Reader) error {
	for {
		line, err := readLine(br)
		if err != nil {
			return err
		}
		op, arg, _ := strings.Cut(string(line), " ")
		switch strings.ToUpper(op) {
		case "MSG", "HMSG":
			m, sid, err := c.readMsg(br, strings.ToUpper(op) == "HMSG", arg)
			if err != nil {
				return err
			}
			c.deliver(sid, m)
		case "PING":
			c.mu.Lock()
			if c.bw != nil {
				c.bw.WriteString("PONG\r\n")
				c.flushLocked()
			}
			c.mu.Unlock()
		case "PONG":
			c.mu.Lock()
			c.pingsOut = 0
			if len(c.pongs) > 0 {
				c.pongs[0] <- nil
				c.pongs = c.pongs[1:]
			}
			c.mu.Unlock()
		case "-ERR":
			msg := strings.Trim(arg, "' ")
			c.mu.Lock()
			c.errs++
			c.lastErr = fmt.Errorf("nats: the server: %s", msg)
			c.mu.Unlock()
			if !strings.Contains(strings.ToLower(msg), "permissions violation") {
				return c.lastErr
			}
		case "INFO":
			var info Info
			if json.Unmarshal([]byte(arg), &info) == nil {
				c.mu.Lock()
				c.learn(info)
				c.mu.Unlock()
				if info.LameDuck {
					return errors.New("nats: the server is shutting down (lame duck mode)")
				}
			}
		case "+OK":
		default:
			return fmt.Errorf("nats: an unknown line from the server: %.80q", line)
		}
	}
}

// readMsg reads a MSG's or HMSG's payload, by the line's arguments:
// subject sid [reply] [header-size] size.
func (c *Client) readMsg(br *bufio.Reader, headers bool, arg string) (*Msg, uint64, error) {
	f := strings.Fields(arg)
	want := 3
	if headers {
		want = 4
	}
	if len(f) != want && len(f) != want+1 {
		return nil, 0, fmt.Errorf("nats: a malformed message line: %.80q", arg)
	}
	m := &Msg{Subject: f[0]}
	sid, err1 := strconv.ParseUint(f[1], 10, 64)
	if len(f) == want+1 {
		m.Reply = f[2]
	}
	total, err2 := strconv.Atoi(f[len(f)-1])
	hdr := 0
	var err3 error
	if headers {
		hdr, err3 = strconv.Atoi(f[len(f)-2])
	}
	c.mu.Lock()
	most := c.info.MaxPayload
	c.mu.Unlock()
	if err := errors.Join(err1, err2, err3); err != nil || total < 0 || hdr < 0 || hdr > total || total > most+MaxLine {
		return nil, 0, fmt.Errorf("nats: a malformed or oversized message line: %.80q", arg)
	}
	buf := make([]byte, total+2)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, 0, err
	}
	if !bytes.HasSuffix(buf, []byte("\r\n")) {
		return nil, 0, errors.New("nats: a message's payload does not end in CRLF")
	}
	if headers {
		if err := parseHeader(buf[:hdr], m); err != nil {
			return nil, 0, err
		}
	}
	m.Data = buf[hdr:total]
	return m, sid, nil
}

// parseHeader reads NATS/1.0 headers, and a status after the version.
func parseHeader(b []byte, m *Msg) error {
	lines := strings.Split(string(b), "\r\n")
	first := lines[0]
	if !strings.HasPrefix(first, "NATS/1.0") {
		return fmt.Errorf("nats: headers without NATS/1.0: %.80q", first)
	}
	if st := strings.TrimSpace(first[len("NATS/1.0"):]); st != "" {
		code, desc, _ := strings.Cut(st, " ")
		n, err := strconv.Atoi(code)
		if err != nil {
			return fmt.Errorf("nats: a status %q", st)
		}
		m.Status, m.Description = n, desc
	}
	for _, l := range lines[1:] {
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			return fmt.Errorf("nats: a header line %.80q", l)
		}
		if m.Header == nil {
			m.Header = Header{}
		}
		k = strings.TrimSpace(k)
		m.Header[k] = append(m.Header[k], strings.TrimSpace(v))
	}
	return nil
}

// appendHeader writes h as NATS/1.0 headers.
func appendHeader(b []byte, h Header) []byte {
	b = append(b, "NATS/1.0\r\n"...)
	for k, vs := range h {
		for _, v := range vs {
			b = append(append(append(append(b, k...), ": "...), v...), "\r\n"...)
		}
	}
	return append(b, "\r\n"...)
}

// deliver hands m to its subscription, never waiting: a full one drops it.
func (c *Client) deliver(sid uint64, m *Msg) {
	c.mu.Lock()
	s := c.subs[sid]
	var ib *Inbox
	if sid == 1 && strings.HasPrefix(m.Subject, c.inbox) {
		ib = c.inboxes[m.Subject[len(c.inbox):]]
	}
	c.mu.Unlock()
	var ch chan *Msg
	switch {
	case ib != nil:
		ch = ib.ch
	case s != nil && sid != 1:
		ch = s.ch
	default:
		return
	}
	select {
	case ch <- m:
	default:
		c.dropped.Add(1)
	}
}

// write flushes what is written when kicked, and PINGs the server every
// PingInterval, until Close.
func (c *Client) write() {
	t := time.NewTicker(c.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-c.kick:
			c.mu.Lock()
			c.flushLocked()
			c.mu.Unlock()
		case <-t.C:
			c.mu.Lock()
			if c.conn != nil {
				if c.pingsOut >= maxPingsOut {
					c.conn.Close() // the reader sees it, and connects again
				} else {
					c.pingsOut++
					c.bw.WriteString("PING\r\n")
					c.flushLocked()
				}
			}
			c.mu.Unlock()
		}
	}
}

// flushLocked writes out what is buffered; an error closes the connection,
// which the reader then sees. c.mu is held.
func (c *Client) flushLocked() {
	if c.bw == nil || c.bw.Buffered() == 0 {
		return
	}
	c.conn.SetWriteDeadline(time.Now().Add(c.writeTime))
	if err := c.bw.Flush(); err != nil {
		c.conn.Close()
	}
}

// writing returns the writer, or why there is none.
func (c *Client) writing() error {
	if c.closed {
		return ErrClosed
	}
	if c.bw == nil {
		return ErrDisconnected
	}
	return nil
}

func validSubject(s string) bool {
	return s != "" && !strings.ContainsAny(s, " \t\r\n")
}

// Publish sends data to subject, with reply as the subject answers go to
// and headers when hdr is not nil. It is buffered, and written out soon;
// Flush waits until the server has it.
func (c *Client) Publish(subject, reply string, hdr Header, data []byte) error {
	if !validSubject(subject) || reply != "" && !validSubject(reply) {
		return ErrBadSubject
	}
	var h []byte
	if hdr != nil {
		h = appendHeader(nil, hdr)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writing(); err != nil {
		return err
	}
	if len(h)+len(data) > c.info.MaxPayload {
		return fmt.Errorf("%w: %d bytes, the server takes %d", ErrMaxPayload, len(h)+len(data), c.info.MaxPayload)
	}
	w := c.bw
	if h != nil {
		w.WriteString("HPUB ")
	} else {
		w.WriteString("PUB ")
	}
	w.WriteString(subject)
	if reply != "" {
		w.WriteString(" " + reply)
	}
	if h != nil {
		w.WriteString(" " + strconv.Itoa(len(h)))
	}
	w.WriteString(" " + strconv.Itoa(len(h)+len(data)) + "\r\n")
	w.Write(h)
	w.Write(data)
	w.WriteString("\r\n")
	c.kickLocked()
	return nil
}

// kickLocked has the writer flush soon.
func (c *Client) kickLocked() {
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

// Flush writes out what is buffered and waits for the server's PONG to a
// PING after it: the server has read everything before. An -ERR the
// server sent meanwhile is its error.
func (c *Client) Flush(ctx context.Context) error {
	ch := make(chan error, 1)
	c.mu.Lock()
	if err := c.writing(); err != nil {
		c.mu.Unlock()
		return err
	}
	errs := c.errs
	c.pongs = append(c.pongs, ch)
	c.bw.WriteString("PING\r\n")
	c.flushLocked()
	c.mu.Unlock()
	select {
	case err := <-ch:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrClosed
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.errs != errs {
		return c.lastErr
	}
	return nil
}

// Close closes the connection and stops connecting again.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	c.stop()
	if c.conn != nil {
		c.flushLocked()
		c.conn.Close()
	}
	return nil
}

// Sub is a subscription.
type Sub struct {
	c              *Client
	sid            uint64
	subject, queue string
	ch             chan *Msg
}

func (c *Client) writeSub(s *Sub) {
	if c.bw == nil {
		return
	}
	c.bw.WriteString("SUB " + s.subject)
	if s.queue != "" {
		c.bw.WriteString(" " + s.queue)
	}
	c.bw.WriteString(" " + strconv.FormatUint(s.sid, 10) + "\r\n")
}

// Subscribe subscribes to subject, in queue's group when queue is not
// empty, its messages sent to ch, which several subscriptions may share:
// a message ch has no room for is dropped (Dropped counts them). It is
// subscribed again on each new connection.
func (c *Client) Subscribe(subject, queue string, ch chan *Msg) (*Sub, error) {
	if !validSubject(subject) || queue != "" && !validSubject(queue) {
		return nil, ErrBadSubject
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	c.sid++
	s := &Sub{c: c, sid: c.sid, subject: subject, queue: queue, ch: ch}
	c.subs[s.sid] = s
	c.writeSub(s)
	c.flushLocked()
	return s, nil
}

// Unsubscribe ends the subscription.
func (s *Sub) Unsubscribe() {
	c := s.c
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.subs[s.sid]; !ok {
		return
	}
	delete(c.subs, s.sid)
	if c.bw != nil {
		c.bw.WriteString("UNSUB " + strconv.FormatUint(s.sid, 10) + "\r\n")
		c.flushLocked()
	}
}

// Inbox is a subject of the client's own that answers come to.
type Inbox struct {
	c       *Client
	subject string
	token   string
	ch      chan *Msg
	lost    chan struct{}
	// sid is the inbox's own subscription, 0 when its answers come
	// through the client's wildcard one.
	sid uint64
}

// NewInbox makes an inbox holding up to n answers not yet taken. It is
// lost when the connection is: Next then returns ErrDisconnected.
func (c *Client) NewInbox(n int) (*Inbox, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writing(); err != nil {
		return nil, err
	}
	c.token++
	tok := strconv.FormatUint(c.token, 36)
	ib := &Inbox{c: c, subject: c.inbox + tok, token: tok, ch: make(chan *Msg, max(n, 1)), lost: make(chan struct{})}
	c.inboxes[tok] = ib
	return ib, nil
}

// newSubInbox makes an inbox of a subscription of its own, for answers
// whose subject is not the inbox's, as the messages a pull consumer sends
// keep their own subjects.
func (c *Client) newSubInbox(n int) (*Inbox, error) {
	ib, err := c.NewInbox(n)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sid++
	ib.sid = c.sid
	s := &Sub{c: c, sid: ib.sid, subject: ib.subject, ch: ib.ch}
	c.subs[s.sid] = s
	c.writeSub(s)
	return ib, nil
}

// Subject is where answers to the inbox go.
func (ib *Inbox) Subject() string { return ib.subject }

// Next returns the next answer, waiting until ctx ends or the connection
// is lost.
func (ib *Inbox) Next(ctx context.Context) (*Msg, error) {
	select {
	case m := <-ib.ch:
		return m, nil
	default:
	}
	select {
	case m := <-ib.ch:
		return m, nil
	case <-ib.lost:
		return nil, ErrDisconnected
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ib.c.done:
		return nil, ErrClosed
	}
}

// Close stops taking answers.
func (ib *Inbox) Close() {
	c := ib.c
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.inboxes, ib.token)
	if _, ok := c.subs[ib.sid]; ok && ib.sid != 0 {
		delete(c.subs, ib.sid)
		if c.bw != nil {
			c.bw.WriteString("UNSUB " + strconv.FormatUint(ib.sid, 10) + "\r\n")
			c.kickLocked()
		}
	}
}

// Request publishes data to subject and returns its first answer: a
// status 503 is ErrNoResponders.
func (c *Client) Request(ctx context.Context, subject string, hdr Header, data []byte) (*Msg, error) {
	ib, err := c.NewInbox(1)
	if err != nil {
		return nil, err
	}
	defer ib.Close()
	if err := c.Publish(subject, ib.Subject(), hdr, data); err != nil {
		return nil, err
	}
	m, err := ib.Next(ctx)
	if err != nil {
		return nil, fmt.Errorf("nats: %s: %w", subject, err)
	}
	if m.Status == 503 {
		return nil, fmt.Errorf("%w on %s", ErrNoResponders, subject)
	}
	return m, nil
}

// Package natstest is a NATS server in the test's process, for the tests
// of sync's NATS client and connectors: the text protocol (INFO, CONNECT
// with a token, a user and password, an NKey or a JWT signed with its
// NKey, PING, PONG, SUB, UNSUB, PUB, HPUB, MSG, HMSG, -ERR), subjects with
// wildcards and queue groups, and a JetStream of streams in memory with
// their duplicate windows and durable pull consumers, enough of its API
// for the client. A Hook makes it misbehave.
package natstest

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Action is what a Hook has the server do with a client's operation.
type Action int

// The actions.
const (
	Normal  Action = iota
	Drop           // ignore it
	Close          // close the connection
	Fatal          // answer -ERR 'fatal', and close
	Deny           // answer -ERR 'Permissions Violation ...', and go on
	Silence        // stop answering anything on the connection
)

// Options are a server's.
type Options struct {
	TLS        *tls.Config
	Token      string
	User, Pass string
	// NKey is a user's public key ("U...") that may connect, signing the
	// nonce; JWTs are taken from any key, their sub claim the key.
	NKey       string
	JWT        bool
	MaxPayload int
	// ConnectURLs are in the INFO sent.
	ConnectURLs []string
	// NoHeaders leaves headers out of INFO.
	NoHeaders bool
}

// Server is a fake NATS server.
type Server struct {
	opts   Options
	ln     net.Listener
	hookFn atomic.Pointer[func(op, subject string) Action]

	mu      sync.Mutex
	conns   map[*conn]bool
	streams map[string]*Stream
	closed  bool
}

// Start starts a server on a loopback port.
func Start(opts Options) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	if opts.MaxPayload == 0 {
		opts.MaxPayload = 1 << 20
	}
	s := &Server{opts: opts, ln: ln, conns: map[*conn]bool{}, streams: map[string]*Stream{}}
	go s.accept()
	return s, nil
}

// Addr is the server's host:port.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the server and closes its connections.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	for c := range s.conns {
		c.nc.Close()
	}
	s.mu.Unlock()
	s.ln.Close()
}

// Kick closes every client's connection.
func (s *Server) Kick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.nc.Close()
	}
}

// Conns returns how many clients are connected.
func (s *Server) Conns() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

// SendInfo sends every client an INFO line of info, as JSON.
func (s *Server) SendInfo(info map[string]any) {
	b, _ := json.Marshal(info)
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.send([]byte("INFO " + string(b) + "\r\n"))
	}
}

func (s *Server) accept() {
	for {
		nc, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.serve(nc)
	}
}

type conn struct {
	s      *Server
	nc     net.Conn
	wmu    sync.Mutex
	subs   map[string]*sub
	silent bool
}

type sub struct {
	c              *conn
	sid            string
	subject, queue string
}

func (c *conn) send(b []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if !c.silent {
		c.nc.Write(b)
	}
}

const nonce = "fake-nonce"

func (s *Server) serve(nc net.Conn) {
	defer nc.Close()
	info := map[string]any{"server_id": "FAKE", "version": "2.11.0", "headers": !s.opts.NoHeaders, "max_payload": s.opts.MaxPayload,
		"jetstream": true, "auth_required": s.opts.Token != "" || s.opts.User != "" || s.opts.NKey != "" || s.opts.JWT,
		"tls_required": s.opts.TLS != nil, "connect_urls": s.opts.ConnectURLs}
	if s.opts.NKey != "" || s.opts.JWT {
		info["nonce"] = nonce
	}
	b, _ := json.Marshal(info)
	if _, err := nc.Write([]byte("INFO " + string(b) + "\r\n")); err != nil {
		return
	}
	if s.opts.TLS != nil {
		t := tls.Server(nc, s.opts.TLS)
		if t.Handshake() != nil {
			return
		}
		nc = t
	}
	c := &conn{s: s, nc: nc, subs: map[string]*sub{}}
	br := bufio.NewReader(nc)
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	op, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
	if op != "CONNECT" || s.hook("CONNECT", "") == Fatal || !s.auth(arg) {
		nc.Write([]byte("-ERR 'Authorization Violation'\r\n"))
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.conns[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
	}()
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		op, arg, _ := strings.Cut(strings.TrimRight(line, "\r\n"), " ")
		f := strings.Fields(arg)
		subject := ""
		if len(f) > 0 {
			subject = f[0]
		}
		var hdr, data []byte
		if op == "PUB" || op == "HPUB" {
			n, _ := strconv.Atoi(f[len(f)-1])
			if n > s.opts.MaxPayload {
				c.send([]byte("-ERR 'Maximum Payload Violation'\r\n"))
				return
			}
			buf := make([]byte, n+2)
			if _, err := io.ReadFull(br, buf); err != nil {
				return
			}
			data = buf[:n]
			if op == "HPUB" {
				h, _ := strconv.Atoi(f[len(f)-2])
				hdr, data = data[:h], data[h:]
				f = f[:len(f)-1]
			}
			f = f[:len(f)-1]
		}
		switch s.hook(op, subject) {
		case Drop:
			continue
		case Close:
			return
		case Fatal:
			c.send([]byte("-ERR 'fatal'\r\n"))
			return
		case Deny:
			c.send([]byte("-ERR 'Permissions Violation for Publish to " + subject + "'\r\n"))
			continue
		case Silence:
			c.wmu.Lock()
			c.silent = true
			c.wmu.Unlock()
		}
		switch op {
		case "PING":
			c.send([]byte("PONG\r\n"))
		case "SUB":
			sb := &sub{c: c, sid: f[len(f)-1], subject: f[0]}
			if len(f) == 3 {
				sb.queue = f[1]
			}
			s.mu.Lock()
			c.subs[sb.sid] = sb
			s.mu.Unlock()
		case "UNSUB":
			s.mu.Lock()
			delete(c.subs, f[0])
			s.mu.Unlock()
		case "PUB", "HPUB":
			reply := ""
			if len(f) == 2 {
				reply = f[1]
			}
			s.publish(f[0], reply, hdr, data)
		}
	}
}

// SetHook has hook asked about each operation a client sends: op is PUB,
// HPUB, SUB, UNSUB, PING or CONNECT, subject the subject. nil removes it.
func (s *Server) SetHook(hook func(op, subject string) Action) {
	if hook == nil {
		s.hookFn.Store(nil)
		return
	}
	s.hookFn.Store(&hook)
}

func (s *Server) hook(op, subject string) Action {
	if h := s.hookFn.Load(); h != nil {
		return (*h)(op, subject)
	}
	return Normal
}

// auth checks a CONNECT's credentials.
func (s *Server) auth(arg string) bool {
	var c struct {
		Token string `json:"auth_token"`
		User  string `json:"user"`
		Pass  string `json:"pass"`
		NKey  string `json:"nkey"`
		JWT   string `json:"jwt"`
		Sig   string `json:"sig"`
	}
	if json.Unmarshal([]byte(arg), &c) != nil {
		return false
	}
	o := s.opts
	switch {
	case o.Token != "":
		return c.Token == o.Token
	case o.User != "":
		return c.User == o.User && c.Pass == o.Pass
	case o.NKey != "":
		return c.NKey == o.NKey && verify(c.NKey, c.Sig)
	case o.JWT:
		parts := strings.Split(c.JWT, ".")
		if len(parts) != 3 {
			return false
		}
		claims, err := base64.RawURLEncoding.DecodeString(parts[1])
		var cl struct {
			Sub string `json:"sub"`
		}
		return err == nil && json.Unmarshal(claims, &cl) == nil && verify(cl.Sub, c.Sig)
	}
	return true
}

// verify checks sig, the nonce signed, against an NKey's public key.
func verify(public, sig string) bool {
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(public)
	if err != nil || len(raw) != 35 {
		return false
	}
	s, err := base64.RawURLEncoding.DecodeString(sig)
	return err == nil && ed25519.Verify(ed25519.PublicKey(raw[1:33]), []byte(nonce), s)
}

// Match reports whether subject matches pattern, with * for a token and >
// for the rest.
func Match(pattern, subject string) bool {
	p, t := strings.Split(pattern, "."), strings.Split(subject, ".")
	for i, x := range p {
		if x == ">" {
			return len(t) > i
		}
		if i >= len(t) || x != "*" && x != t[i] {
			return false
		}
	}
	return len(p) == len(t)
}

// publish routes a message: to JetStream's API, an acknowledgement, a
// stream, and the subscriptions that match.
func (s *Server) publish(subject, reply string, hdr, data []byte) {
	switch {
	case strings.HasPrefix(subject, "$JS.API."):
		s.api(strings.TrimPrefix(subject, "$JS.API."), reply, data)
		return
	case strings.HasPrefix(subject, "$JS.ACK."):
		s.ack(subject)
		return
	}
	st := s.streamOf(subject)
	if st != nil {
		ack := s.store(st, subject, hdr, data)
		if reply != "" {
			s.Deliver(reply, "", nil, ack)
		}
	}
	if s.Deliver(subject, reply, hdr, data) == 0 && st == nil && reply != "" {
		s.status(reply, 503, "")
	}
}

// Deliver sends a message to the subscriptions subject matches, one of
// each queue group, and returns how many it went to.
func (s *Server) Deliver(subject, reply string, hdr, data []byte) int {
	return s.deliverAs(subject, subject, reply, hdr, data)
}

// deliverAs sends a message to the subscriptions to matches, with the
// subject subject, as a pull consumer's messages go to its inbox.
func (s *Server) deliverAs(to, subject, reply string, hdr, data []byte) int {
	s.mu.Lock()
	var subs []*sub
	queues := map[string]bool{}
	for c := range s.conns {
		for _, sb := range c.subs {
			if Match(sb.subject, to) && (sb.queue == "" || !queues[sb.queue]) {
				queues[sb.queue] = sb.queue != ""
				subs = append(subs, sb)
			}
		}
	}
	s.mu.Unlock()
	for _, sb := range subs {
		var b []byte
		r := ""
		if reply != "" {
			r = " " + reply
		}
		if hdr != nil {
			b = fmt.Appendf(nil, "HMSG %s %s%s %d %d\r\n", subject, sb.sid, r, len(hdr), len(hdr)+len(data))
			b = append(b, hdr...)
		} else {
			b = fmt.Appendf(nil, "MSG %s %s%s %d\r\n", subject, sb.sid, r, len(data))
		}
		b = append(append(b, data...), "\r\n"...)
		sb.c.send(b)
	}
	return len(subs)
}

// status sends a status message to an inbox.
func (s *Server) status(to string, code int, desc string) {
	s.Deliver(to, "", fmt.Appendf(nil, "NATS/1.0 %d %s\r\n\r\n", code, desc), nil)
}

// Msg is a message a stream holds.
type Msg struct {
	Seq     uint64
	Subject string
	Header  []byte
	Data    []byte
	Time    time.Time
}

// Stream is a stream in memory.
type Stream struct {
	Name      string
	Subjects  []string
	Msgs      []Msg
	ids       map[string]uint64
	consumers map[string]*consumer
}

type consumer struct {
	cfg       map[string]any
	ackPolicy string
	filters   []string
	next      uint64 // the next stream sequence to deliver
	dseq      uint64
	pending   map[uint64]time.Time // delivered, not acknowledged
	ackWait   time.Duration
	acked     uint64
	waiting   []*pull
}

type pull struct {
	reply string
	left  int
	timer *time.Timer
}

// AddStream makes a stream of subjects.
func (s *Server) AddStream(name string, subjects ...string) *Stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := &Stream{Name: name, Subjects: subjects, ids: map[string]uint64{}, consumers: map[string]*consumer{}}
	s.streams[name] = st
	return st
}

// Messages returns a copy of a stream's messages.
func (s *Server) Messages(stream string) []Msg {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.streams[stream]; st != nil {
		return append([]Msg(nil), st.Msgs...)
	}
	return nil
}

// Pending returns how many messages a consumer has delivered and not had
// acknowledged, and its acknowledgement floor.
func (s *Server) Pending(stream, name string) (int, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.streams[stream]; st != nil && st.consumers[name] != nil {
		c := st.consumers[name]
		return len(c.pending), c.acked
	}
	return -1, 0
}

// RemoveConsumer deletes a consumer, answering its waiting pulls 409.
func (s *Server) RemoveConsumer(stream, name string) {
	s.mu.Lock()
	st := s.streams[stream]
	c := st.consumers[name]
	delete(st.consumers, name)
	var waiting []*pull
	if c != nil {
		waiting, c.waiting = c.waiting, nil
		for _, p := range waiting {
			if p.timer != nil {
				p.timer.Stop()
			}
		}
	}
	s.mu.Unlock()
	for _, p := range waiting {
		s.status(p.reply, 409, "Consumer Deleted")
	}
}

func (s *Server) streamOf(subject string) *Stream {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.streams {
		for _, p := range st.Subjects {
			if Match(p, subject) {
				return st
			}
		}
	}
	return nil
}

// store keeps a message, unless its Nats-Msg-Id was seen, and serves the
// waiting pulls; it returns the PubAck.
func (s *Server) store(st *Stream, subject string, hdr, data []byte) []byte {
	id := ""
	for _, l := range strings.Split(string(hdr), "\r\n") {
		if k, v, ok := strings.Cut(l, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Nats-Msg-Id") {
			id = strings.TrimSpace(v)
		}
	}
	s.mu.Lock()
	if seq, ok := st.ids[id]; ok && id != "" {
		s.mu.Unlock()
		return fmt.Appendf(nil, `{"stream":%q,"seq":%d,"duplicate":true}`, st.Name, seq)
	}
	seq := uint64(len(st.Msgs)) + 1
	st.Msgs = append(st.Msgs, Msg{Seq: seq, Subject: subject, Header: bytes.Clone(hdr), Data: bytes.Clone(data), Time: time.Now()})
	if id != "" {
		st.ids[id] = seq
	}
	cs := make([]*consumer, 0, len(st.consumers))
	names := []string{}
	for n, c := range st.consumers {
		cs = append(cs, c)
		names = append(names, n)
	}
	s.mu.Unlock()
	for i, c := range cs {
		s.serveWaiting(st, names[i], c)
	}
	return fmt.Appendf(nil, `{"stream":%q,"seq":%d}`, st.Name, seq)
}

// api answers a JetStream API request.
func (s *Server) api(op, reply string, data []byte) {
	answer := func(v any) {
		b, _ := json.Marshal(v)
		s.Deliver(reply, "", nil, b)
	}
	apiErr := func(code, errCode int, desc string) {
		answer(map[string]any{"error": map[string]any{"code": code, "err_code": errCode, "description": desc}})
	}
	t := strings.Split(op, ".")
	s.mu.Lock()
	var st *Stream
	if len(t) >= 3 {
		st = s.streams[t[2]]
		if t[0] == "CONSUMER" && t[1] == "MSG" && len(t) == 5 {
			st = s.streams[t[3]]
		}
	}
	s.mu.Unlock()
	switch {
	case len(t) == 3 && t[0] == "STREAM" && t[1] == "CREATE":
		var cfg struct {
			Subjects []string `json:"subjects"`
		}
		json.Unmarshal(data, &cfg)
		st := s.AddStream(t[2], cfg.Subjects...)
		answer(s.streamInfo(st))
	case st == nil:
		apiErr(404, 10059, "stream not found")
	case len(t) == 3 && t[0] == "STREAM" && t[1] == "INFO":
		answer(s.streamInfo(st))
	case len(t) == 4 && t[0] == "CONSUMER" && t[1] == "CREATE":
		var req struct {
			Config map[string]any `json:"config"`
		}
		json.Unmarshal(data, &req)
		answer(s.createConsumer(st, t[3], req.Config))
	case len(t) == 4 && t[0] == "CONSUMER" && (t[1] == "INFO" || t[1] == "DELETE"):
		s.mu.Lock()
		c := st.consumers[t[3]]
		s.mu.Unlock()
		switch {
		case c == nil:
			apiErr(404, 10014, "consumer not found")
		case t[1] == "DELETE":
			s.RemoveConsumer(st.Name, t[3])
			answer(map[string]any{"success": true})
		default:
			answer(s.consumerInfo(st, t[3], c))
		}
	case len(t) == 5 && t[0] == "CONSUMER" && t[1] == "MSG" && t[2] == "NEXT":
		s.mu.Lock()
		c := st.consumers[t[4]]
		s.mu.Unlock()
		if c == nil {
			s.status(reply, 404, "No Messages") // as an old server answers
			return
		}
		var req struct {
			Batch   int   `json:"batch"`
			Expires int64 `json:"expires"`
			NoWait  bool  `json:"no_wait"`
		}
		json.Unmarshal(data, &req)
		p := &pull{reply: reply, left: max(req.Batch, 1)}
		s.mu.Lock()
		c.waiting = append(c.waiting, p)
		s.mu.Unlock()
		s.serveWaiting(st, t[4], c)
		s.mu.Lock()
		still := len(c.waiting) > 0 && c.waiting[len(c.waiting)-1] == p
		if still && req.NoWait {
			c.waiting = c.waiting[:len(c.waiting)-1]
		}
		if still && !req.NoWait {
			p.timer = time.AfterFunc(time.Duration(max(req.Expires, int64(time.Millisecond))), func() {
				s.mu.Lock()
				found := false
				for i, w := range c.waiting {
					if w == p {
						c.waiting = append(c.waiting[:i], c.waiting[i+1:]...)
						found = true
						break
					}
				}
				s.mu.Unlock()
				if found {
					s.status(reply, 408, "Request Timeout")
				}
			})
		}
		s.mu.Unlock()
		if still && req.NoWait {
			s.status(reply, 404, "No Messages")
		}
	default:
		apiErr(400, 10003, "bad request")
	}
}

func (s *Server) streamInfo(st *Stream) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first, last uint64
	if n := len(st.Msgs); n > 0 {
		first, last = st.Msgs[0].Seq, st.Msgs[n-1].Seq
	}
	return map[string]any{"config": map[string]any{"name": st.Name, "subjects": st.Subjects},
		"state": map[string]any{"messages": len(st.Msgs), "first_seq": first, "last_seq": last}}
}

func (s *Server) createConsumer(st *Stream, name string, cfg map[string]any) map[string]any {
	c := &consumer{cfg: cfg, pending: map[uint64]time.Time{}, next: 1, ackWait: 30 * time.Second}
	c.ackPolicy, _ = cfg["ack_policy"].(string)
	if f, ok := cfg["filter_subject"].(string); ok {
		c.filters = []string{f}
	}
	if fs, ok := cfg["filter_subjects"].([]any); ok {
		for _, f := range fs {
			c.filters = append(c.filters, f.(string))
		}
	}
	if w, ok := cfg["ack_wait"].(float64); ok && w > 0 {
		c.ackWait = time.Duration(w)
	}
	s.mu.Lock()
	switch cfg["deliver_policy"] {
	case "new":
		c.next = uint64(len(st.Msgs)) + 1
	case "by_start_sequence":
		c.next = uint64(cfg["opt_start_seq"].(float64))
	}
	c.acked = c.next - 1
	if old := st.consumers[name]; old != nil {
		c = old // an update keeps the state
	}
	st.consumers[name] = c
	s.mu.Unlock()
	return s.consumerInfo(st, name, c)
}

func (s *Server) consumerInfo(st *Stream, name string, c *consumer) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var left uint64
	for _, m := range st.Msgs {
		if m.Seq >= c.next && c.matches(m.Subject) {
			left++
		}
	}
	return map[string]any{"stream_name": st.Name, "name": name, "config": c.cfg, "num_pending": left,
		"num_ack_pending": len(c.pending), "ack_floor": map[string]any{"stream_seq": c.acked},
		"delivered": map[string]any{"stream_seq": c.next - 1, "consumer_seq": c.dseq}}
}

func (c *consumer) matches(subject string) bool {
	if len(c.filters) == 0 {
		return true
	}
	for _, f := range c.filters {
		if Match(f, subject) {
			return true
		}
	}
	return false
}

// serveWaiting hands a consumer's waiting pulls what it has to deliver:
// messages whose acknowledgement wait passed first, then new ones.
func (s *Server) serveWaiting(st *Stream, name string, c *consumer) {
	type out struct {
		to  string
		msg Msg
		ack string
	}
	var outs []out
	var done []*pull
	s.mu.Lock()
	for len(c.waiting) > 0 {
		p := c.waiting[0]
		var m *Msg
		for seq, at := range c.pending {
			if time.Since(at) >= c.ackWait {
				m = &st.Msgs[seq-1]
				break
			}
		}
		for m == nil && c.next <= uint64(len(st.Msgs)) {
			if cand := &st.Msgs[c.next-1]; c.matches(cand.Subject) {
				m = cand
			}
			c.next++
		}
		if m == nil {
			break
		}
		c.dseq++
		if c.ackPolicy != "none" {
			c.pending[m.Seq] = time.Now()
		}
		outs = append(outs, out{p.reply, *m, fmt.Sprintf("$JS.ACK.%s.%s.1.%d.%d.%d.0", st.Name, name, m.Seq, c.dseq, m.Time.UnixNano())})
		if p.left--; p.left == 0 {
			c.waiting = c.waiting[1:]
			done = append(done, p)
		}
	}
	s.mu.Unlock()
	for _, p := range done {
		if p.timer != nil {
			p.timer.Stop()
		}
	}
	for _, o := range outs {
		hdr := o.msg.Header
		if len(hdr) == 0 {
			hdr = nil
		}
		s.deliverAs(o.to, o.msg.Subject, o.ack, hdr, o.msg.Data)
	}
}

// ack takes a $JS.ACK subject's acknowledgement.
func (s *Server) ack(subject string) {
	t := strings.Split(subject, ".")
	if len(t) != 9 {
		return
	}
	seq, _ := strconv.ParseUint(t[5], 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[t[2]]
	if st == nil || st.consumers[t[3]] == nil {
		return
	}
	c := st.consumers[t[3]]
	for p := range c.pending {
		if p == seq || c.ackPolicy == "all" && p < seq {
			delete(c.pending, p)
		}
	}
	c.acked = c.next - 1
	for p := range c.pending {
		c.acked = min(c.acked, p-1)
	}
}

// NKeyPublic writes an ed25519 public key as a user's NKey.
func NKeyPublic(pub ed25519.PublicKey) string {
	raw := append([]byte{20 << 3}, pub...)
	var crc uint16
	for _, b := range raw {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	raw = binary.LittleEndian.AppendUint16(raw, crc)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
}

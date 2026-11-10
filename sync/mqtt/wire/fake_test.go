package mqttwire

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"hash"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBroker is an MQTT server in the test's process, of both versions:
// sessions kept by client ID, with their subscriptions and the messages of
// QoS 1 and 2 not acknowledged, sent again on the session's next
// connection; publishes routed to the subscriptions they match. Its knobs
// make it refuse, answer with reason codes, use topic aliases, or not
// answer pings; raw writes any bytes to a connection.
type fakeBroker struct {
	t  *testing.T
	ln net.Listener

	mu       sync.Mutex
	sessions map[string]*fakeSession
	conns    []*fakeConn
	users    map[string]string // nil: anyone
	refuse   byte              // CONNACK's code, when not 0
	connack  func(w *propsWriter)
	// codes are the PUBACK's or PUBREC's reason codes, by topic.
	codes    map[string]byte
	deny     map[string]bool // filters SUBACK refuses
	noPing   bool
	noSuback bool
	holdAcks bool // PUBACK, PUBREC and PUBCOMP are not sent
	aliases  bool // messages sent use topic aliases, when the client takes them
	subIDs   bool // messages sent carry a subscription identifier
	// got are the messages clients published, once received whole.
	got     []*Message
	gotFlow []string // the client's packets, by name
	connect chan *fakeConn
	// denyUnsub are the filters UNSUBACK refuses; noUnsuback holds it.
	denyUnsub  map[string]bool
	noUnsuback bool
	// unsubAs is the packet type UNSUBSCRIBE is answered with, when not 0;
	// unsubCodes, when not nil, the UNSUBACK's codes.
	unsubAs    byte
	unsubCodes []byte
	// scram are the users and passwords of enhanced authentication, by
	// SCRAM; nil refuses it. badSig sends a wrong server signature;
	// noFinal none; holdReauth leaves a re-authentication unanswered.
	scram      map[string]string
	badSig     bool
	noFinal    bool
	holdReauth bool
}

type fakeSession struct {
	id       string
	subs     map[string]byte
	inflight []*fakeOut
	nextID   uint16
	conn     *fakeConn
	// rec are the client's QoS 2 publishes PUBREC'd, until PUBREL.
	rec map[uint16]*Message
}

type fakeOut struct {
	m     *Message
	id    uint16
	stage int  // QoS 2: 1 once the client's PUBREC came
	sent  bool // sent once
}

type fakeConn struct {
	b         *fakeBroker
	nc        net.Conn
	version   byte
	sess      *fakeSession
	clean     bool
	user      string
	keepAlive uint16
	props     props
	wmu       sync.Mutex
	aliases   map[uint16]string // the client's
	sent      map[string]uint16 // ours
	closed    chan struct{}
	disc      chan byte // the DISCONNECT's reason code
	will      *Will     // published unless DISCONNECT, normal, ends it
	auth      *scramServer
}

func newFakeBroker(t *testing.T, tc *tls.Config) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tc != nil {
		ln = tls.NewListener(ln, tc)
	}
	b := &fakeBroker{t: t, ln: ln, sessions: map[string]*fakeSession{}, codes: map[string]byte{}, deny: map[string]bool{},
		connect: make(chan *fakeConn, 16), denyUnsub: map[string]bool{}}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go b.serve(nc)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		b.mu.Lock()
		cs := b.conns
		b.mu.Unlock()
		for _, c := range cs {
			c.nc.Close()
		}
	})
	return b
}

func (b *fakeBroker) addr() string { return b.ln.Addr().String() }

func (b *fakeBroker) set(f func(b *fakeBroker)) {
	b.mu.Lock()
	f(b)
	b.mu.Unlock()
}

// published returns the messages clients published.
func (b *fakeBroker) published() []*Message {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*Message(nil), b.got...)
}

func (b *fakeBroker) flow() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.gotFlow...)
}

// connected reports whether the session id has a connection.
func (b *fakeBroker) connected(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.sessions[id]
	return s != nil && s.conn != nil
}

func (b *fakeBroker) inflight(id string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.sessions[id]; s != nil {
		return len(s.inflight)
	}
	return 0
}

func (c *fakeConn) write(p []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.Write(p)
}

func (b *fakeBroker) serve(nc net.Conn) {
	c := &fakeConn{b: b, nc: nc, aliases: map[uint16]string{}, sent: map[string]uint16{}, closed: make(chan struct{}), disc: make(chan byte, 1)}
	b.mu.Lock()
	b.conns = append(b.conns, c)
	b.mu.Unlock()
	defer func() {
		nc.Close()
		b.mu.Lock()
		if c.sess != nil && c.sess.conn == c {
			c.sess.conn = nil
			if c.clean {
				delete(b.sessions, c.sess.id)
			}
		}
		will := c.will
		b.mu.Unlock()
		if will != nil {
			b.take(&will.Message)
		}
		close(c.closed)
	}()
	br := bufio.NewReader(nc)
	first, body, err := readPacket(br, 1<<20)
	if err != nil || first != typeConnect<<4 {
		return
	}
	if !c.connect(br, body) {
		return
	}
	select {
	case b.connect <- c:
	default:
	}
	for {
		first, body, err := readPacket(br, 1<<24)
		if err != nil {
			return
		}
		if !c.handle(first, body) {
			return
		}
	}
}

// connect reads CONNECT and answers it.
func (c *fakeConn) connect(br *bufio.Reader, body []byte) bool {
	b := c.b
	r := &reader{b: body}
	if r.string() != "MQTT" {
		return false
	}
	c.version = r.byte()
	flags := r.byte()
	c.keepAlive = r.u16()
	if c.version == Version5 {
		c.props = r.props()
	}
	id := r.string()
	var will *Will
	if flags&0x04 != 0 {
		will = &Will{Message: Message{QoS: flags >> 3 & 3, Retain: flags&0x20 != 0}}
		if c.version == Version5 {
			p := r.props()
			will.PayloadUTF8, will.ContentType, will.ResponseTopic, will.CorrelationData, will.UserProperties =
				p.payloadUTF8, p.contentType, p.responseTopic, p.correlation, p.user
			will.MessageExpiry = time.Duration(p.expiry) * time.Second
			will.Delay = time.Duration(p.willDelay) * time.Second
		}
		will.Topic = r.string()
		will.Payload = r.binary()
	}
	var user, pass string
	if flags&0x80 != 0 {
		user = r.string()
	}
	if flags&0x40 != 0 {
		pass = r.string()
	}
	if r.err != nil || r.left() > 0 {
		b.t.Errorf("a malformed CONNECT: %v", r.err)
		return false
	}
	c.clean, c.user = flags&2 != 0, user
	var final []byte
	if c.props.authMethod != "" {
		var ok bool
		if final, ok = c.authenticate(br, c.props.authMethod, c.props.authData); !ok {
			return false
		}
	}
	b.mu.Lock()
	code := b.refuse
	if code == 0 && b.users != nil && final == nil && (b.users[user] != pass || user == "") {
		code = 4
		if c.version == Version5 {
			code = 0x86
		}
	}
	var w propsWriter
	if b.connack != nil {
		b.connack(&w)
	}
	if final != nil {
		w.str(propAuthMethod, c.props.authMethod)
		w.bin(propAuthData, final)
	}
	if code != 0 {
		b.mu.Unlock()
		c.writeConnack(false, code, &w)
		return false
	}
	if id == "" {
		id = "auto-" + time.Now().Format("150405.000000000")
		if c.version == Version5 {
			w.str(propAssignedClientID, id)
		}
	}
	s := b.sessions[id]
	present := s != nil && !c.clean
	if s == nil || c.clean {
		s = &fakeSession{id: id, subs: map[string]byte{}, rec: map[uint16]*Message{}}
		b.sessions[id] = s
	}
	if old := s.conn; old != nil {
		old.nc.Close()
	}
	s.conn, c.sess, c.will = c, s, will
	resend := append([]*fakeOut(nil), s.inflight...)
	b.mu.Unlock()
	c.writeConnack(present, 0, &w)
	for _, o := range resend {
		if o.stage == 1 {
			c.write([]byte{typePubrel<<4 | 2, 2, byte(o.id >> 8), byte(o.id)})
		} else {
			c.sendPublish(o, true)
		}
	}
	return true
}

func (c *fakeConn) writeConnack(present bool, code byte, w *propsWriter) {
	var flags byte
	if present {
		flags = 1
	}
	body := []byte{flags, code}
	if c.version == Version5 {
		body = w.appendTo(body)
	}
	c.write(packet(typeConnack<<4, body))
}

// sendPublish sends a message of the session to the client.
func (c *fakeConn) sendPublish(o *fakeOut, dup bool) {
	m := o.m
	first := typePublish<<4 | m.QoS<<1
	if m.Retain {
		first |= 1
	}
	c.b.mu.Lock()
	if dup && o.sent {
		first |= 8
	}
	o.sent = true
	topic, alias := m.Topic, uint16(0)
	if c.b.aliases && c.props.aliasMax > 0 {
		if a, ok := c.sent[m.Topic]; ok {
			topic, alias = "", a
		} else if len(c.sent) < int(c.props.aliasMax) {
			alias = uint16(len(c.sent) + 1)
			c.sent[m.Topic] = alias
		}
	}
	subIDs := c.b.subIDs
	c.b.mu.Unlock()
	body := appendString(nil, topic)
	if m.QoS > 0 {
		body = appendUint16(body, o.id)
	}
	if c.version == Version5 {
		var w propsWriter
		if m.PayloadUTF8 {
			w.byte(propPayloadFormat, 1)
		}
		if m.MessageExpiry > 0 {
			w.u32(propMessageExpiry, uint32(m.MessageExpiry/time.Second))
		}
		if alias != 0 {
			w.u16(propTopicAlias, alias)
		}
		if m.ResponseTopic != "" {
			w.str(propResponseTopic, m.ResponseTopic)
		}
		if m.CorrelationData != nil {
			w.bin(propCorrelationData, m.CorrelationData)
		}
		w.user(m.UserProperties)
		if m.ContentType != "" {
			w.str(propContentType, m.ContentType)
		}
		if subIDs {
			w.b = appendVarint(append(w.b, propSubscriptionID), 300)
		}
		body = w.appendTo(body)
	}
	c.write(packet(first, append(body, m.Payload...)))
}

// route hands m to every session subscribed to its topic.
func (b *fakeBroker) route(m *Message) {
	b.mu.Lock()
	var sends []func()
	for _, s := range b.sessions {
		q, ok := byte(0), false
		for f, fq := range s.subs {
			if MatchTopic(f, m.Topic) {
				q, ok = max(q, fq), true
			}
		}
		if !ok {
			continue
		}
		cp := *m
		cp.QoS = min(q, m.QoS)
		o := &fakeOut{m: &cp}
		if cp.QoS > 0 {
			s.nextID++
			o.id = s.nextID
			s.inflight = append(s.inflight, o)
		}
		if c := s.conn; c != nil {
			sends = append(sends, func() { c.sendPublish(o, false) })
		}
	}
	b.mu.Unlock()
	for _, f := range sends {
		f()
	}
}

// handle takes a client's packet; false ends the connection.
func (c *fakeConn) handle(first byte, body []byte) bool {
	b := c.b
	r := &reader{b: body}
	typ := first >> 4
	b.mu.Lock()
	b.gotFlow = append(b.gotFlow, typeNames[typ])
	hold := b.holdAcks
	b.mu.Unlock()
	switch typ {
	case typePublish:
		m := &Message{QoS: first >> 1 & 3, Retain: first&1 == 1}
		m.Topic = r.string()
		var id uint16
		if m.QoS > 0 {
			id = r.u16()
		}
		if c.version == Version5 {
			p := r.props()
			m.PayloadUTF8, m.ContentType, m.ResponseTopic, m.CorrelationData, m.UserProperties =
				p.payloadUTF8, p.contentType, p.responseTopic, p.correlation, p.user
			m.MessageExpiry = time.Duration(p.expiry) * time.Second
			if p.alias != 0 {
				if m.Topic == "" {
					m.Topic = c.aliases[p.alias]
				} else {
					c.aliases[p.alias] = m.Topic
				}
			}
		}
		m.Payload = r.take(r.left())
		if r.err != nil {
			b.t.Errorf("a malformed PUBLISH")
			return false
		}
		b.mu.Lock()
		code := b.codes[m.Topic]
		b.mu.Unlock()
		switch m.QoS {
		case 0:
			b.take(m)
		case 1:
			if code < 0x80 {
				b.take(m)
			}
			if !hold {
				c.writeAck(typePuback, id, code)
			}
		case 2:
			if code < 0x80 {
				b.mu.Lock()
				c.sess.rec[id] = m
				b.mu.Unlock()
			}
			if !hold {
				c.writeAck(typePubrec, id, code)
			}
		}
	case typePubrel:
		id := r.u16()
		b.mu.Lock()
		m := c.sess.rec[id]
		delete(c.sess.rec, id)
		b.mu.Unlock()
		if m != nil {
			b.take(m)
		}
		if !hold {
			c.writeAck(typePubcomp, id, 0)
		}
	case typePuback, typePubcomp, typePubrec:
		id := r.u16()
		b.mu.Lock()
		s := c.sess
		for i, o := range s.inflight {
			if o.id != id {
				continue
			}
			if typ == typePubrec {
				o.stage = 1
				break
			}
			s.inflight = append(s.inflight[:i], s.inflight[i+1:]...)
			break
		}
		b.mu.Unlock()
		if typ == typePubrec {
			c.write([]byte{typePubrel<<4 | 2, 2, byte(id >> 8), byte(id)})
		}
	case typeSubscribe:
		id := r.u16()
		if c.version == Version5 {
			r.props()
		}
		var codes []byte
		b.mu.Lock()
		for r.left() > 0 && r.err == nil {
			f := r.string()
			o := r.byte()
			if b.deny[f] {
				codes = append(codes, 0x87)
				continue
			}
			c.sess.subs[f] = o & 3
			codes = append(codes, o&3)
		}
		no := b.noSuback
		b.mu.Unlock()
		if no {
			return true
		}
		body := appendUint16(nil, id)
		if c.version == Version5 {
			var w propsWriter
			w.str(propReasonString, "as asked")
			body = w.appendTo(body)
		}
		c.write(packet(typeSuback<<4, append(body, codes...)))
	case typePingreq:
		b.mu.Lock()
		no := b.noPing
		b.mu.Unlock()
		if !no {
			c.write([]byte{typePingresp << 4, 0})
		}
	case typeUnsub:
		id := r.u16()
		if c.version == Version5 {
			r.props()
		}
		var codes []byte
		b.mu.Lock()
		for r.left() > 0 && r.err == nil {
			f := r.string()
			_, had := c.sess.subs[f]
			switch {
			case b.denyUnsub[f]:
				codes = append(codes, 0x87)
			case had:
				delete(c.sess.subs, f)
				codes = append(codes, 0)
			default:
				codes = append(codes, 0x11)
			}
		}
		no, as := b.noUnsuback, b.unsubAs
		if b.unsubCodes != nil {
			codes = b.unsubCodes
		}
		b.mu.Unlock()
		if no {
			return true
		}
		if as == 0 {
			as = typeUnsuback
		}
		body := appendUint16(nil, id)
		if c.version == Version5 {
			body = append((&propsWriter{}).appendTo(body), codes...)
		} else if as != typeUnsuback {
			body = append(body, 0)
		}
		c.write(packet(as<<4, body))
	case typeAuth:
		code, p, err := authOf(body)
		if err != nil {
			b.t.Errorf("a malformed AUTH")
			return false
		}
		b.mu.Lock()
		hold := b.holdReauth
		b.mu.Unlock()
		if hold {
			return true
		}
		if code == 0x19 {
			s, first, ok := c.b.scramStart(p.authMethod, p.authData)
			if !ok {
				c.write([]byte{typeDisconnect << 4, 1, 0x8C})
				return false
			}
			c.auth = s
			c.write(authPacket(0x18, p.authMethod, first))
			return true
		}
		final, ok := c.auth.finish(p.authData)
		if !ok {
			c.write([]byte{typeDisconnect << 4, 1, 0x87})
			return false
		}
		var w propsWriter
		w.str(propAuthMethod, p.authMethod)
		w.bin(propAuthData, final)
		c.write(packet(typeAuth<<4, w.appendTo([]byte{0})))
	case typeDisconnect:
		code := byte(0)
		if len(body) > 0 {
			code = body[0]
		}
		if code == 0 {
			b.mu.Lock()
			c.will = nil
			b.mu.Unlock()
		}
		c.disc <- code
		return false
	}
	return true
}

// authenticate runs CONNECT's SCRAM exchange, and returns the server's
// final message for the CONNACK; false when it answered with a refusal.
func (c *fakeConn) authenticate(br *bufio.Reader, method string, data []byte) ([]byte, bool) {
	s, first, ok := c.b.scramStart(method, data)
	if !ok {
		c.writeConnack(false, 0x8C, &propsWriter{})
		return nil, false
	}
	c.write(authPacket(0x18, method, first))
	pf, body, err := readPacket(br, 1<<20)
	if err != nil || pf != typeAuth<<4 {
		return nil, false
	}
	_, p, _ := authOf(body)
	final, ok := s.finish(p.authData)
	if !ok {
		c.writeConnack(false, 0x87, &propsWriter{})
		return nil, false
	}
	return final, true
}

// scramServer is the server's side of a SCRAM exchange.
type scramServer struct {
	b              *fakeBroker
	h              func() hash.Hash
	password       string
	bare, srvFirst string
	salt           []byte
}

func (b *fakeBroker) scramStart(method string, data []byte) (*scramServer, []byte, bool) {
	a, err := NewSCRAM(method, "x", "")
	b.mu.Lock()
	users := b.scram
	b.mu.Unlock()
	msg, ok := strings.CutPrefix(string(data), "n,,")
	if err != nil || users == nil || !ok {
		return nil, nil, false
	}
	var user, nonce string
	for _, f := range strings.Split(msg, ",") {
		if v, ok := strings.CutPrefix(f, "n="); ok {
			user = v
		}
		if v, ok := strings.CutPrefix(f, "r="); ok {
			nonce = v
		}
	}
	pw, known := users[user]
	if !known {
		pw = "\x00unknown"
	}
	s := &scramServer{b: b, h: a.(*scram).hash, password: pw, bare: msg, salt: []byte("fake-salt")}
	s.srvFirst = "r=" + nonce + "srv,s=" + base64.StdEncoding.EncodeToString(s.salt) + ",i=64"
	return s, []byte(s.srvFirst), true
}

// finish checks the client's final message, and makes the server's.
func (s *scramServer) finish(data []byte) ([]byte, bool) {
	msg := string(data)
	without, proof64, ok := strings.Cut(msg, ",p=")
	if !ok {
		return nil, false
	}
	salted, _ := pbkdf2.Key(s.h, s.password, s.salt, 64, s.h().Size())
	mac := func(key []byte, m string) []byte {
		h := hmac.New(s.h, key)
		h.Write([]byte(m))
		return h.Sum(nil)
	}
	auth := s.bare + "," + s.srvFirst + "," + without
	clientKey := mac(salted, "Client Key")
	h := s.h()
	h.Write(clientKey)
	sig := mac(h.Sum(nil), auth)
	proof, _ := base64.StdEncoding.DecodeString(proof64)
	if len(proof) != len(sig) {
		return nil, false
	}
	for i := range proof {
		proof[i] ^= sig[i]
	}
	if !hmac.Equal(proof, clientKey) {
		return nil, false
	}
	s.b.mu.Lock()
	bad, none := s.b.badSig, s.b.noFinal
	s.b.mu.Unlock()
	if none {
		return []byte{}, true
	}
	ss := mac(mac(salted, "Server Key"), auth)
	if bad {
		ss[0] ^= 1
	}
	return []byte("v=" + base64.StdEncoding.EncodeToString(ss)), true
}

func (c *fakeConn) writeAck(typ byte, id uint16, code byte) {
	first := typ << 4
	if c.version == Version5 && code != 0 {
		var w propsWriter
		w.str(propReasonString, "no")
		c.write(packet(first, w.appendTo([]byte{byte(id >> 8), byte(id), code})))
		return
	}
	c.write([]byte{first, 2, byte(id >> 8), byte(id)})
}

func (b *fakeBroker) take(m *Message) {
	b.mu.Lock()
	b.got = append(b.got, m)
	b.mu.Unlock()
	b.route(m)
}

// waitConn returns the next connection taken.
func (b *fakeBroker) waitConn(t *testing.T) *fakeConn {
	t.Helper()
	select {
	case c := <-b.connect:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
	}
	return nil
}

func dial(t *testing.T, cfg Config) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// next returns the client's next message, failing after a while.
func next(t *testing.T, c *Client) *Message {
	t.Helper()
	select {
	case m := <-c.Messages():
		return m
	case <-c.Done():
		t.Fatalf("the connection ended: %v", c.Err())
	case <-time.After(5 * time.Second):
		t.Fatal("no message")
	}
	return nil
}

// none checks the client gets no message for a short while.
func none(t *testing.T, c *Client) {
	t.Helper()
	select {
	case m := <-c.Messages():
		t.Fatalf("a message %s %q", m.Topic, m.Payload)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// testCert makes a certificate for name, its own CA.
func testCert(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// rawServer is a listener whose connections the test drives itself.
type rawServer struct {
	ln    net.Listener
	conns chan net.Conn
}

func newRawServer(t *testing.T) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rawServer{ln: ln, conns: make(chan net.Conn, 4)}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.conns <- nc
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return s
}

func (s *rawServer) addr() string { return s.ln.Addr().String() }

// accept takes the next connection, reads its CONNECT and answers it with
// connack's bytes (none when nil).
func (s *rawServer) accept(t *testing.T, connack []byte) (net.Conn, *bufio.Reader) {
	t.Helper()
	var nc net.Conn
	select {
	case nc = <-s.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("no connection")
	}
	t.Cleanup(func() { nc.Close() })
	br := bufio.NewReader(nc)
	if _, _, err := readPacket(br, 1<<20); err != nil {
		t.Fatal(err)
	}
	if connack != nil {
		nc.Write(connack)
	}
	return nc, br
}

// rawClient dials s, answering CONNECT with connack, and returns the
// client and the server's side.
func rawClient(t *testing.T, s *rawServer, cfg Config, connack []byte) (*Client, net.Conn, *bufio.Reader) {
	t.Helper()
	cfg.Address = s.addr()
	type res struct {
		c   *Client
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := Dial(context.Background(), cfg)
		ch <- res{c, err}
	}()
	nc, br := s.accept(t, connack)
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() { r.c.Close() })
	return r.c, nc, br
}

// readNext reads the client's next packet on the server's side.
func readNext(t *testing.T, nc net.Conn, br *bufio.Reader) (byte, []byte) {
	t.Helper()
	nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	first, body, err := readPacket(br, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	return first, body
}

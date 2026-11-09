package pulsarwire

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// fields are a protocol buffer's fields, as the fake broker reads the
// client's commands.
type fields map[int][]pbField

type pbField struct {
	u uint64
	b []byte
}

func parseFields(t *testing.T, b []byte) fields {
	t.Helper()
	fs := fields{}
	r := pbReader{b: b}
	for f, ok := r.next(); ok; f, ok = r.next() {
		switch r.wt {
		case wireVarint:
			fs[f] = append(fs[f], pbField{u: r.uint()})
		case wireBytes:
			fs[f] = append(fs[f], pbField{b: r.bytes()})
		default:
			t.Errorf("wire type %d", r.wt)
			r.skip()
		}
	}
	if r.err != nil {
		t.Errorf("a command that does not parse: %v", r.err)
	}
	return fs
}

func (fs fields) u(f int) uint64 {
	if v := fs[f]; len(v) > 0 {
		return v[0].u
	}
	return 0
}

func (fs fields) s(f int) string {
	if v := fs[f]; len(v) > 0 {
		return string(v[0].b)
	}
	return ""
}

func (fs fields) has(f int) bool { return len(fs[f]) > 0 }

const fakeLedger = 7

// fakeEntry is a stored message, as the producer sent it.
type fakeEntry struct {
	meta, data []byte
	n          int
}

type fakeSub struct {
	topic   string
	conn    *fakeConn
	id      uint64
	permits int
	next    int
	durable bool
	name    string
}

// fakeBroker is a Pulsar broker in the test's process, over a loopback
// listener, keeping each topic's entries in memory. Hooks change its
// answers.
type fakeBroker struct {
	t    *testing.T
	ln   net.Listener
	addr string

	mu         sync.Mutex
	topics     map[string][]fakeEntry
	partitions map[string]int
	lastSeq    map[string]int64 // by topic and producer name
	acked      map[string]int   // durable subscriptions' entries acknowledged, by topic and name
	subs       []*fakeSub
	conns      []*fakeConn
	token      string
	maxMessage int32
	// hook, when it returns true, has handled the command.
	hook func(c *fakeConn, typ int, fs fields, payload []byte) bool
	// lookup, when set, answers a lookup.
	lookup func(c *fakeConn, topic string, req uint64, fs fields) bool
	seen   map[int]int
	acks   []uint64
}

type fakeConn struct {
	b       *fakeBroker
	nc      net.Conn
	wmu     sync.Mutex
	proxyTo string
	// producers are the topics of the producers made on the connection.
	producers map[uint64]string
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
	b := &fakeBroker{t: t, ln: ln, addr: ln.Addr().String(), topics: map[string][]fakeEntry{}, partitions: map[string]int{},
		lastSeq: map[string]int64{}, acked: map[string]int{}, seen: map[int]int{}}
	t.Cleanup(func() {
		ln.Close()
		b.mu.Lock()
		for _, c := range b.conns {
			c.nc.Close()
		}
		b.mu.Unlock()
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			c := &fakeConn{b: b, nc: nc, producers: map[uint64]string{}}
			b.mu.Lock()
			b.conns = append(b.conns, c)
			b.mu.Unlock()
			go c.serve()
		}
	}()
	return b
}

func (b *fakeBroker) setHook(h func(c *fakeConn, typ int, fs fields, payload []byte) bool) {
	b.mu.Lock()
	b.hook = h
	b.mu.Unlock()
}

func (b *fakeBroker) setLookup(l func(c *fakeConn, topic string, req uint64, fs fields) bool) {
	b.mu.Lock()
	b.lookup = l
	b.mu.Unlock()
}

func (b *fakeBroker) url() string { return "pulsar://" + b.addr }

func (b *fakeBroker) count(typ int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen[typ]
}

// closeConns drops every connection.
func (b *fakeBroker) closeConns() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.conns {
		c.nc.Close()
	}
}

func (c *fakeConn) send(f []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.nc.Write(f)
}

func (c *fakeConn) cmd(t int, in pb) { c.send(frame(base(t, in))) }

func (c *fakeConn) fail(req uint64, code int32, msg string) {
	c.cmd(typeError, pb(nil).uint(1, req).int(2, int64(code)).str(3, msg))
}

func (c *fakeConn) serve() {
	b := c.b
	br := bufio.NewReader(c.nc)
	for {
		cmd, rest, err := readFrame(br, 64<<20)
		if err != nil {
			return
		}
		top := parseFields(b.t, cmd)
		typ := int(top.u(1))
		var fs fields
		if top.has(typ) {
			fs = parseFields(b.t, top[typ][0].b)
		} else {
			fs = fields{}
		}
		b.mu.Lock()
		b.seen[typ]++
		hook := b.hook
		b.mu.Unlock()
		if hook != nil && hook(c, typ, fs, rest) {
			continue
		}
		c.handle(typ, fs, rest)
	}
}

func (c *fakeConn) handle(typ int, fs fields, rest []byte) {
	b := c.b
	switch typ {
	case typeConnect:
		if b.token != "" && (fs.s(5) != "token" || fs.s(3) != b.token) {
			c.fail(0, 3, "bad token")
			return
		}
		c.proxyTo = fs.s(6)
		in := pb(nil).str(1, "fake").int(2, protocolVersion)
		if b.maxMessage > 0 {
			in = in.int(3, int64(b.maxMessage))
		}
		c.cmd(typeConnected, in)
	case typePing:
		c.send(frame(pongCmd))
	case typePong, typeAuthResponse:
	case typePartitionedMetadata:
		b.mu.Lock()
		n := b.partitions[fs.s(1)]
		b.mu.Unlock()
		c.cmd(typePartitionedMetadataResponse, pb(nil).uint(1, uint64(n)).uint(2, fs.u(2)))
	case typeLookup:
		b.mu.Lock()
		lookup := b.lookup
		b.mu.Unlock()
		if lookup != nil && lookup(c, fs.s(1), fs.u(2), fs) {
			return
		}
		c.cmd(typeLookupResponse, pb(nil).str(1, b.url()).str(2, "pulsar+ssl://"+b.addr).uint(3, lookupConnect).uint(4, fs.u(2)))
	case typeProducer:
		name := fs.s(4)
		if name == "" {
			name = "fake-producer"
		}
		b.mu.Lock()
		last, ok := b.lastSeq[fs.s(1)+"|"+name]
		c.producers[fs.u(2)] = fs.s(1)
		b.mu.Unlock()
		if !ok {
			last = -1
		}
		c.cmd(typeProducerSuccess, pb(nil).uint(1, fs.u(3)).str(2, name).int(3, last))
	case typeSend:
		meta, data, err := splitPayload(rest)
		if err != nil {
			b.t.Errorf("send: %v", err)
			return
		}
		m, _ := decodeMetadata(meta, false)
		topic := c.producerTopic(fs.u(1))
		seq, highest := fs.u(2), fs.u(6)
		key := topic + "|" + m.producerName
		b.mu.Lock()
		last, ok := b.lastSeq[key]
		if ok && int64(seq) <= last {
			b.mu.Unlock()
			c.cmd(typeSendReceipt, pb(nil).uint(1, fs.u(1)).uint(2, seq).messageID(3, MessageID{Ledger: -1, Entry: -1, Partition: -1, BatchIndex: -1}))
			return
		}
		b.lastSeq[key] = int64(highest)
		b.topics[topic] = append(b.topics[topic], fakeEntry{meta: meta, data: data, n: int(m.numMessages)})
		entry := len(b.topics[topic]) - 1
		b.mu.Unlock()
		c.cmd(typeSendReceipt, pb(nil).uint(1, fs.u(1)).uint(2, seq).messageID(3, MessageID{Ledger: fakeLedger, Entry: int64(entry), Partition: -1, BatchIndex: -1}))
		b.deliver()
	case typeSubscribe:
		s := &fakeSub{topic: fs.s(1), conn: c, id: fs.u(4), durable: fs.u(8) != 0, name: fs.s(2)}
		b.mu.Lock()
		n := len(b.topics[s.topic])
		switch {
		case s.durable:
			if a, ok := b.acked[s.topic+"|"+s.name]; ok {
				s.next = a + 1
			} else if fs.u(13) == uint64(Latest) {
				s.next = n
			}
		case fs.has(9):
			id, _ := decodeMessageID(fs[9][0].b)
			switch {
			case id.Ledger < 0:
			case id.Ledger == LatestID.Ledger:
				s.next = n
			case id.BatchIndex >= 0:
				s.next = int(id.Entry)
			default:
				s.next = int(id.Entry) + 1
			}
		}
		if s.durable {
			b.acked[s.topic+"|"+s.name] = s.next - 1
		}
		b.subs = append(b.subs, s)
		b.mu.Unlock()
		c.cmd(typeSuccess, pb(nil).uint(1, fs.u(5)))
	case typeFlow:
		b.mu.Lock()
		for _, s := range b.subs {
			if s.conn == c && s.id == fs.u(1) {
				s.permits += int(fs.u(2))
			}
		}
		b.mu.Unlock()
		b.deliver()
	case typeAck:
		id, _ := decodeMessageID(fs[3][0].b)
		b.mu.Lock()
		b.acks = append(b.acks, fs.u(1))
		for _, s := range b.subs {
			if s.conn == c && s.id == fs.u(1) && s.durable {
				b.acked[s.topic+"|"+s.name] = int(id.Entry)
			}
		}
		b.mu.Unlock()
	case typeGetLastMessageID:
		b.mu.Lock()
		var topic string
		for _, s := range b.subs {
			if s.conn == c && s.id == fs.u(1) {
				topic = s.topic
			}
		}
		n := len(b.topics[topic])
		b.mu.Unlock()
		id := MessageID{Ledger: fakeLedger, Entry: int64(n - 1), Partition: -1, BatchIndex: -1}
		c.cmd(typeGetLastMessageIDResponse, pb(nil).messageID(1, id).uint(2, fs.u(2)))
	case typeCloseProducer:
		c.cmd(typeSuccess, pb(nil).uint(1, fs.u(2)))
	case typeCloseConsumer:
		b.mu.Lock()
		for i, s := range b.subs {
			if s.conn == c && s.id == fs.u(1) {
				b.subs = append(b.subs[:i], b.subs[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		c.cmd(typeSuccess, pb(nil).uint(1, fs.u(2)))
	default:
		b.t.Errorf("the fake broker got command %d", typ)
	}
}

func (c *fakeConn) producerTopic(id uint64) string {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	return c.producers[id]
}

// deliver sends each subscription the entries its permits let it have.
func (b *fakeBroker) deliver() {
	type out struct {
		c *fakeConn
		f []byte
	}
	var outs []out
	b.mu.Lock()
	for _, s := range b.subs {
		entries := b.topics[s.topic]
		for s.permits > 0 && s.next < len(entries) {
			e := entries[s.next]
			id := MessageID{Ledger: fakeLedger, Entry: int64(s.next), Partition: -1, BatchIndex: -1}
			outs = append(outs, out{s.conn, payloadFrame(base(typeMessage, pb(nil).uint(1, s.id).messageID(2, id)), e.meta, e.data)})
			s.permits -= e.n
			s.next++
		}
	}
	b.mu.Unlock()
	for _, o := range outs {
		o.c.send(o.f)
	}
}

// testCert makes a self-signed certificate for 127.0.0.1, and a pool that
// trusts it.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

func newClient(t *testing.T, cfg Config) *Client {
	t.Helper()
	cl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for range 500 {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

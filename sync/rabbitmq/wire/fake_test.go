package amqpwire

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBroker is an AMQP 0-9-1 broker in the test's process: it negotiates,
// authenticates PLAIN and EXTERNAL, opens channels, declares queues it
// knows, consumes, takes publishes and confirms them, as hooks change.
type fakeBroker struct {
	t  *testing.T
	ln net.Listener

	mechs     string
	user      string
	pass      string
	frameMax  uint32
	heartbeat uint16
	// noStart has the broker never send connection.start.
	noStart bool
	// hook, when it returns true, has handled a method.
	hook func(fc *fakeConn, ch uint16, id uint32, d *dec) bool

	mu         sync.Mutex
	queues     map[string]uint32
	published  []fakeMsg
	acks       []fakeAck
	heartbeats int
	conns      []*fakeConn
	tuned      [3]uint32 // tune-ok's channel max, frame max, heartbeat
	props      Table     // the client's properties
}

type fakeMsg struct {
	exchange, key string
	mandatory     bool
	props         Properties
	body          []byte
}

type fakeAck struct {
	tag                     uint64
	multiple, nack, requeue bool
}

type fakeConn struct {
	b       *fakeBroker
	nc      net.Conn
	br      *bufio.Reader
	wmu     sync.Mutex
	confirm bool
	seq     uint64
}

func newFake(t *testing.T, tc *tls.Config) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tc != nil {
		ln = tls.NewListener(ln, tc)
	}
	b := &fakeBroker{t: t, ln: ln, mechs: "AMQPLAIN PLAIN", user: "u", pass: "p", frameMax: 131072, queues: map[string]uint32{"q": 3}}
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
			fc := &fakeConn{b: b, nc: nc, br: bufio.NewReader(nc)}
			b.mu.Lock()
			b.conns = append(b.conns, fc)
			b.mu.Unlock()
			go fc.serve()
		}
	}()
	return b
}

func (b *fakeBroker) addr() string { return b.ln.Addr().String() }

func (b *fakeBroker) cfg() Config {
	return Config{Addrs: []string{b.addr()}, Username: "u", Password: "p", Timeout: 2 * time.Second, Heartbeat: -1}
}

func (b *fakeBroker) conn() *fakeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.conns[len(b.conns)-1]
}

func (fc *fakeConn) raw(p []byte) {
	fc.wmu.Lock()
	defer fc.wmu.Unlock()
	fc.nc.Write(p)
}

func (fc *fakeConn) send(ch uint16, m enc) { fc.raw(appendFrame(nil, frameMethod, ch, m)) }

// deliver sends a message to the client, its body in frames of at most
// split bytes.
func (fc *fakeConn) deliver(ch uint16, head enc, p Properties, body []byte, split int) {
	b := appendFrame(nil, frameMethod, ch, head)
	b = appendFrame(b, frameHeader, ch, p.header(uint64(len(body))))
	for rest := body; len(rest) > 0; {
		n := min(split, len(rest))
		b = appendFrame(b, frameBody, ch, rest[:n])
		rest = rest[n:]
	}
	fc.raw(b)
}

func deliverMethod(tag uint64, redelivered bool, key string) enc {
	return method(basicDeliver).short("ctag").u64(tag).bits(redelivered).short("ex").short(key)
}

func (fc *fakeConn) read() (frame, error) { return readFrame(fc.br, 1<<20) }

func (fc *fakeConn) expect(id uint32) *dec {
	for {
		f, err := fc.read()
		if err != nil {
			return nil
		}
		if f.typ != frameMethod {
			continue
		}
		d := &dec{b: f.payload}
		if got := d.u32(); got == id {
			return d
		}
		return nil
	}
}

func (fc *fakeConn) serve() {
	b := fc.b
	defer fc.nc.Close()
	var hdr [8]byte
	if _, err := io.ReadFull(fc.br, hdr[:]); err != nil || string(hdr[:]) != "AMQP\x00\x00\x09\x01" {
		return
	}
	if b.noStart {
		io.Copy(io.Discard, fc.br)
		return
	}
	if hook := b.getHook(); hook != nil && hook(fc, 0, 0, nil) {
		// The hook speaks for the broker from the start.
		return
	}
	fc.send(0, method(connectionStart).u8(0).u8(9).table(Table{"product": "fake"}).long(b.mechs).long("en_US"))
	d := fc.expect(connectionStartOK)
	if d == nil {
		return
	}
	props := d.table(0)
	mech, resp := d.short(), d.long()
	b.mu.Lock()
	b.props = props
	b.mu.Unlock()
	ok := mech == "PLAIN" && resp == "\x00"+b.user+"\x00"+b.pass || mech == "EXTERNAL"
	if !ok {
		fc.send(0, method(connectionClose).u16(403).short("ACCESS_REFUSED").u32(0))
		return
	}
	fc.send(0, method(connectionTune).u16(2047).u32(b.frameMax).u16(b.heartbeat))
	if d = fc.expect(connectionTuneOK); d == nil {
		return
	}
	b.mu.Lock()
	b.tuned = [3]uint32{uint32(d.u16()), d.u32(), uint32(d.u16())}
	b.mu.Unlock()
	if fc.expect(connectionOpen) == nil {
		return
	}
	fc.send(0, method(connectionOpenOK).short(""))
	var cur *fakeMsg
	var size uint64
	for {
		f, err := fc.read()
		if err != nil {
			return
		}
		switch f.typ {
		case frameHeartbeat:
			b.mu.Lock()
			b.heartbeats++
			b.mu.Unlock()
			continue
		case frameHeader:
			size, cur.props, _ = readHeader(f.payload)
			if size == 0 {
				fc.published(cur)
				cur = nil
			}
			continue
		case frameBody:
			cur.body = append(cur.body, f.payload...)
			if uint64(len(cur.body)) == size {
				fc.published(cur)
				cur = nil
			}
			continue
		}
		d := &dec{b: f.payload}
		id := d.u32()
		if hook := b.getHook(); hook != nil && hook(fc, f.channel, id, d) {
			continue
		}
		switch id {
		case channelOpen:
			fc.send(f.channel, method(channelOpenOK).long(""))
		case basicQos:
			fc.send(f.channel, method(basicQosOK))
		case basicConsume:
			d.u16()
			q := d.short()
			tag := d.short()
			if tag == "" {
				tag = "amq.ctag-" + q
			}
			fc.send(f.channel, method(basicConsumeOK).short(tag))
		case queueDeclare:
			d.u16()
			q := d.short()
			passive := d.u8()&1 != 0
			b.mu.Lock()
			n, found := b.queues[q]
			if !found && !passive {
				b.queues[q] = 0
			}
			b.mu.Unlock()
			if !found && passive {
				fc.send(f.channel, method(channelClose).u16(404).short("NOT_FOUND - no queue '"+q+"'").u32(queueDeclare))
				continue
			}
			fc.send(f.channel, method(queueDeclareOK).short(q).u32(n).u32(1))
		case confirmSelect:
			fc.confirm = true
			fc.send(f.channel, method(confirmSelectOK))
		case basicPublish:
			d.u16()
			cur = &fakeMsg{exchange: d.short(), key: d.short()}
			cur.mandatory = d.u8()&1 != 0
		case basicAck, basicNack:
			a := fakeAck{tag: d.u64(), nack: id == basicNack}
			bits := d.u8()
			a.multiple, a.requeue = bits&1 != 0, bits&2 != 0
			b.mu.Lock()
			b.acks = append(b.acks, a)
			b.mu.Unlock()
		case channelFlowOK, channelCloseOK:
		case connectionClose:
			fc.send(0, method(connectionCloseOK))
			return
		}
	}
}

// published keeps a message, returns it when its key is unroutable, and
// confirms it.
func (fc *fakeConn) published(m *fakeMsg) {
	b := fc.b
	b.mu.Lock()
	b.published = append(b.published, *m)
	b.mu.Unlock()
	if m.mandatory && m.key == "unroutable" {
		fc.deliver(1, method(basicReturn).u16(312).short("NO_ROUTE").short(m.exchange).short(m.key), m.props, m.body, 4000)
	}
	if fc.confirm {
		fc.seq++
		if m.key == "nack" {
			fc.send(1, method(basicNack).u64(fc.seq).bits(false, false))
		} else if m.key != "hold" {
			fc.send(1, method(basicAck).u64(fc.seq).bits(false))
		}
	}
}

func (b *fakeBroker) snapshot() ([]fakeMsg, []fakeAck, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]fakeMsg(nil), b.published...), append([]fakeAck(nil), b.acks...), b.heartbeats
}

// eventually waits for cond, failing the test after two seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("never: %s", what)
}

func dialT(t *testing.T, cfg Config) *Conn {
	t.Helper()
	c, err := Dial(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// testCert makes a self-signed certificate for 127.0.0.1.
func testCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k, Leaf: leaf}, pool
}

func contains(err error, s string) bool { return err != nil && strings.Contains(err.Error(), s) }

func (b *fakeBroker) setHook(h func(fc *fakeConn, ch uint16, id uint32, d *dec) bool) {
	b.mu.Lock()
	b.hook = h
	b.mu.Unlock()
}

func (b *fakeBroker) getHook() func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.hook
}

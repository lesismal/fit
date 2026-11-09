package pulsarwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	for _, comp := range []Compression{None, LZ4, Zlib, Zstd, Snappy} {
		t.Run(comp.String(), func(t *testing.T) {
			b := newFakeBroker(t, nil)
			cl := newClient(t, Config{URL: b.url()})
			ctx := ctxT(t)
			p, err := cl.CreateProducer(ctx, "persistent://public/default/t", "", comp)
			if err != nil {
				t.Fatal(err)
			}
			if p.Name() != "fake-producer" || p.LastSequenceID() != -1 || p.MaxMessageSize() != defaultMaxMessage {
				t.Fatalf("%s %d %d", p.Name(), p.LastSequenceID(), p.MaxMessageSize())
			}
			body := bytes.Repeat([]byte("abc"), 1000)
			at := time.UnixMilli(1_700_000_000_000).UTC()
			msgs := []ProducerMessage{
				{Key: []byte("k1"), Payload: body, Properties: []KeyValue{{"a", "1"}, {"b", "2"}}, EventTime: at},
				{Key: []byte{0xff, 0x00}, Payload: []byte("bin")},
				{Key: []byte("gone"), Null: true},
				{Payload: nil},
			}
			ch, err := p.Send(msgs)
			if err != nil {
				t.Fatal(err)
			}
			if r := <-ch; r.Err != nil || r.ID.Entry != 0 || r.ID.Ledger != fakeLedger {
				t.Fatalf("%+v", r)
			}
			cs, err := cl.Subscribe(ctx, "persistent://public/default/t", SubscribeOptions{Subscription: "r", Start: &EarliestID})
			if err != nil {
				t.Fatal(err)
			}
			if cs.Topic() != "persistent://public/default/t" {
				t.Fatal(cs.Topic())
			}
			var got []Message
			for range 4 {
				m, err := cs.Receive(ctx)
				if err != nil {
					t.Fatal(err)
				}
				got = append(got, m)
			}
			if string(got[0].Key) != "k1" || !bytes.Equal(got[0].Payload, body) || len(got[0].Properties) != 2 ||
				got[0].Properties[1] != (KeyValue{"b", "2"}) || !got[0].EventTime.Equal(at) || got[0].ProducerName != "fake-producer" ||
				got[0].PublishTime.IsZero() || got[0].SequenceID != 0 {
				t.Errorf("%+v", got[0])
			}
			if !bytes.Equal(got[1].Key, []byte{0xff, 0}) || string(got[1].Payload) != "bin" || got[1].SequenceID != 1 {
				t.Errorf("%+v", got[1])
			}
			if got[2].Payload != nil || string(got[2].Key) != "gone" {
				t.Errorf("%+v", got[2])
			}
			if got[3].Payload == nil || got[3].Key != nil || !got[3].EventTime.IsZero() {
				t.Errorf("%+v", got[3])
			}
			for i, m := range got {
				if m.ID.BatchIndex != int32(i) || m.ID.BatchSize != 4 || m.ID.Entry != 0 {
					t.Errorf("%d: %v", i, m.ID)
				}
			}
			if err := cs.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if _, _, err := cs.TryReceive(); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
			if err := p.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := p.Send(msgs); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
		})
	}
}

func send(t *testing.T, p *Producer, payloads ...string) MessageID {
	t.Helper()
	var msgs []ProducerMessage
	for _, s := range payloads {
		msgs = append(msgs, ProducerMessage{Payload: []byte(s)})
	}
	ch, err := p.Send(msgs)
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	return r.ID
}

func TestReaderStarts(t *testing.T) {
	b := newFakeBroker(t, nil)
	cl := newClient(t, Config{URL: b.url()})
	ctx := ctxT(t)
	p, err := cl.CreateProducer(ctx, "t", "me", None)
	if err != nil {
		t.Fatal(err)
	}
	send(t, p, "a", "b")
	send(t, p, "c")
	if _, err := p.Send(nil); err == nil {
		t.Fatal("an empty send")
	}
	read := func(start MessageID, n int) []string {
		cs, err := cl.Subscribe(ctx, "t", SubscribeOptions{Subscription: "r", Start: &start, ReceiverQueue: 1})
		if err != nil {
			t.Fatal(err)
		}
		defer cs.Close(ctx)
		var out []string
		for range n {
			m, err := cs.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(m.Payload))
		}
		if _, ok, _ := cs.TryReceive(); ok {
			t.Error("more")
		}
		lid, err := cs.LastMessageID(ctx)
		if err != nil || lid.Entry != 1 {
			t.Errorf("%v %v", lid, err)
		}
		return out
	}
	if got := read(MessageID{Ledger: fakeLedger, Entry: 0, BatchIndex: 0}, 3); strings.Join(got, "") != "abc" {
		t.Error(got)
	}
	if got := read(MessageID{Ledger: fakeLedger, Entry: 0, BatchIndex: -1}, 1); strings.Join(got, "") != "c" {
		t.Error(got)
	}
	if got := read(LatestID, 0); len(got) != 0 {
		t.Error(got)
	}
	// A durable subscription resumes after what it acknowledged.
	cs, err := cl.Subscribe(ctx, "t", SubscribeOptions{Subscription: "d", Durable: true, Type: Failover, Initial: Earliest, ConsumerName: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := cs.Receive(ctx)
	if err := cs.AckCumulative(MessageID{Ledger: fakeLedger, Entry: m.ID.Entry, BatchIndex: -1}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ack", func() bool { return b.count(typeAck) == 1 })
	cs.Close(ctx)
	cs, err = cl.Subscribe(ctx, "t", SubscribeOptions{Subscription: "d", Durable: true})
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := cs.Receive(ctx); string(m.Payload) != "c" {
		t.Errorf("%s", m.Payload)
	}
	cs2, err := cl.Subscribe(ctx, "t", SubscribeOptions{Subscription: "new", Durable: true, Initial: Latest})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := cs2.TryReceive(); ok {
		t.Error("latest read something")
	}
}

func TestPartitionsAndLookups(t *testing.T) {
	b := newFakeBroker(t, nil)
	other := newFakeBroker(t, nil)
	b.partitions["p"] = 3
	cl := newClient(t, Config{URL: "pulsar://127.0.0.1:1," + b.addr, DialTimeout: time.Second})
	ctx := ctxT(t)
	if n, err := cl.PartitionedMetadata(ctx, "p"); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	// A redirect to the other broker, which answers with a connect.
	b.setLookup(func(c *fakeConn, topic string, req uint64, fs fields) bool {
		switch topic {
		case "redirect":
			c.cmd(typeLookupResponse, pb(nil).str(1, other.url()).uint(3, lookupRedirect).uint(4, req).bool(5, true))
		case "proxy":
			c.cmd(typeLookupResponse, pb(nil).str(1, "pulsar://broker-7:6650").uint(3, lookupConnect).uint(4, req).bool(8, true))
		case "fail":
			c.cmd(typeLookupResponse, pb(nil).uint(3, lookupFailed).uint(4, req).int(6, 11).str(7, "no such topic"))
		case "bad":
			c.cmd(typeLookupResponse, pb(nil).str(1, "http://x").uint(3, lookupConnect).uint(4, req))
		case "loop":
			c.cmd(typeLookupResponse, pb(nil).str(1, b.url()).uint(3, lookupRedirect).uint(4, req))
		default:
			return false
		}
		return true
	})
	other.setLookup(func(c *fakeConn, topic string, req uint64, fs fields) bool {
		if fs.u(3) != 1 {
			t.Error("not authoritative")
		}
		return false
	})
	p, err := cl.CreateProducer(ctx, "redirect", "", None)
	if err != nil {
		t.Fatal(err)
	}
	send(t, p, "x")
	if len(other.topics["redirect"]) != 1 {
		t.Fatal("not on the other broker")
	}
	p, err = cl.CreateProducer(ctx, "proxy", "", None)
	if err != nil {
		t.Fatal(err)
	}
	send(t, p, "y")
	b.mu.Lock()
	proxied := false
	for _, c := range b.conns {
		proxied = proxied || c.proxyTo == "broker-7:6650"
	}
	b.mu.Unlock()
	if !proxied {
		t.Error("no CONNECT to the proxy with the broker")
	}
	if _, err := cl.CreateProducer(ctx, "fail", "", None); !errors.Is(err, ErrTopicNotFound) || !strings.Contains(err.Error(), "TopicNotFound: no such topic") {
		t.Errorf("%v", err)
	}
	if _, err := cl.Subscribe(ctx, "bad", SubscribeOptions{}); err == nil {
		t.Error("a bad broker URL")
	}
	if _, err := cl.CreateProducer(ctx, "loop", "", None); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Error(err)
	}
	b.partitions["x"] = 0
	b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool {
		if typ == typePartitionedMetadata {
			c.cmd(typePartitionedMetadataResponse, pb(nil).uint(2, fs.u(2)).uint(3, 1).int(4, 4).str(5, "denied"))
			return true
		}
		return false
	})
	if _, err := cl.PartitionedMetadata(ctx, "x"); !errors.Is(err, ErrAuthorization) {
		t.Error(err)
	}
	cl.Close()
	if _, err := cl.PartitionedMetadata(ctx, "x"); !errors.Is(err, ErrClosed) {
		t.Error(err)
	}
	dead := newClient(t, Config{URL: "pulsar://127.0.0.1:1", DialTimeout: time.Second})
	if _, err := dead.CreateProducer(ctx, "t", "", None); err == nil {
		t.Error("dialed nothing")
	}
	if _, err := dead.Subscribe(ctx, "t", SubscribeOptions{}); err == nil {
		t.Error("dialed nothing")
	}
}

func TestAuthAndTLS(t *testing.T) {
	cert, pool := testCert(t)
	b := newFakeBroker(t, &tls.Config{Certificates: []tls.Certificate{cert}})
	b.token = "secret"
	// The broker challenges once in the handshake, and again later.
	challenges := 0
	b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool {
		switch typ {
		case typeConnect:
			if challenges == 0 {
				challenges++
				c.cmd(typeAuthChallenge, pb(nil).str(1, "fake").bytes(2, pb(nil).str(1, "token").bytes(2, []byte("x"))).int(3, 19))
				return true
			}
		case typeAuthResponse:
			if fs.s(1) != "fit-sync" {
				t.Error(fs.s(1))
			}
			if challenges == 1 {
				challenges++
				c.handle(typeConnect, fields{3: {{b: []byte("secret")}}, 5: {{b: []byte("token")}}}, nil)
			}
			return true
		}
		return false
	})
	cl := newClient(t, Config{URL: "pulsar+ssl://" + b.addr, TLS: &tls.Config{RootCAs: pool}, Token: "secret"})
	ctx := ctxT(t)
	if _, err := cl.PartitionedMetadata(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	c := b.conns[0]
	b.mu.Unlock()
	c.cmd(typeAuthChallenge, pb(nil).bytes(2, pb(nil).str(1, "token")))
	eventually(t, "the second answer", func() bool { return b.count(typeAuthResponse) == 2 })

	bad := newClient(t, Config{URL: "pulsar+ssl://" + b.addr, TLS: &tls.Config{RootCAs: pool}, Token: "wrong"})
	if _, err := bad.PartitionedMetadata(ctx, "t"); !errors.Is(err, ErrAuthentication) {
		t.Error(err)
	}
	untrusted := newClient(t, Config{URL: "pulsar+ssl://" + b.addr})
	if _, err := untrusted.PartitionedMetadata(ctx, "t"); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Error(err)
	}
}

func TestHandshakeFailures(t *testing.T) {
	ctx := ctxT(t)
	for name, answer := range map[string]func(c *fakeConn){
		"close":    func(c *fakeConn) { c.nc.Close() },
		"junk":     func(c *fakeConn) { c.send(frame([]byte{0xff})) },
		"other":    func(c *fakeConn) { c.cmd(typePong, nil) },
		"big":      func(c *fakeConn) { c.send([]byte{0x7f, 0, 0, 0}) },
		"bad auth": func(c *fakeConn) { c.cmd(typeAuthChallenge, pb(nil).bytes(2, []byte{0xff})) },
	} {
		b := newFakeBroker(t, nil)
		b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool {
			if typ == typeConnect {
				answer(c)
				return true
			}
			return false
		})
		cl := newClient(t, Config{URL: b.url()})
		if _, err := cl.PartitionedMetadata(ctx, "t"); err == nil {
			t.Errorf("%s: connected", name)
		}
	}
	// A challenge whose answer cannot be written.
	b := newFakeBroker(t, nil)
	b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool {
		if typ == typeConnect {
			c.cmd(typeAuthChallenge, pb(nil).bytes(2, pb(nil).str(1, "token")))
			c.nc.Close()
			return true
		}
		return false
	})
	cl := newClient(t, Config{URL: b.url()})
	if _, err := cl.PartitionedMetadata(ctx, "t"); err == nil {
		t.Error("connected")
	}
	// A listener that never answers CONNECT: the dial timeout.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	cl = newClient(t, Config{URL: "pulsar://" + ln.Addr().String(), DialTimeout: 100 * time.Millisecond})
	if _, err := cl.PartitionedMetadata(ctx, "t"); err == nil {
		t.Error("connected")
	}
}

func TestProducerFailures(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.maxMessage = 2000
	cl := newClient(t, Config{URL: b.url(), RequestTimeout: 300 * time.Millisecond})
	ctx := ctxT(t)
	p, err := cl.CreateProducer(ctx, "t", "me", None)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Send([]ProducerMessage{{Payload: make([]byte, 3000)}}); !errors.Is(err, ErrTooLarge) {
		t.Fatal(err)
	}
	// Deduplication: a second producer of the name carries on.
	send(t, p, "a", "b")
	p2, err := cl.CreateProducer(ctx, "t", "me", None)
	if err != nil || p2.LastSequenceID() != 1 {
		t.Fatal(p2.LastSequenceID(), err)
	}
	// A producer the broker says is busy.
	b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool {
		switch typ {
		case typeProducer:
			if fs.s(4) == "busy" {
				c.fail(fs.u(3), 16, "")
				return true
			}
			if fs.s(4) == "wait" {
				// Not ready, then ready.
				c.cmd(typeProducerSuccess, pb(nil).uint(1, fs.u(3)).str(2, "wait").bool(6, false))
				c.cmd(typeProducerSuccess, pb(nil).uint(1, fs.u(3)).str(2, "wait").int(3, -1))
				return true
			}
			if fs.s(4) == "slow" {
				return true
			}
		case typeSend:
			switch c.producerTopic(fs.u(1)) {
			case "err":
				c.cmd(typeSendError, pb(nil).uint(1, fs.u(1)).uint(2, fs.u(2)).int(3, 2).str(4, "disk"))
				return true
			case "closed":
				c.cmd(typeCloseProducer, pb(nil).uint(1, fs.u(1)).uint(2, 1))
				return true
			}
		}
		return false
	})
	if _, err := cl.CreateProducer(ctx, "t", "busy", None); !errors.Is(err, ErrProducerBusy) || err.Error() == "" {
		t.Error(err)
	}
	if p, err := cl.CreateProducer(ctx, "t", "wait", None); err != nil || p.Name() != "wait" {
		t.Error(err)
	}
	if _, err := cl.CreateProducer(ctx, "t", "slow", None); err == nil || !strings.Contains(err.Error(), "no answer") {
		t.Error(err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, err := cl.CreateProducer(short, "t", "slow", None); !errors.Is(err, context.DeadlineExceeded) {
		t.Error(err)
	}
	for _, topic := range []string{"err", "closed"} {
		p, err := cl.CreateProducer(ctx, topic, "", None)
		if err != nil {
			t.Fatal(err)
		}
		ch, err := p.Send([]ProducerMessage{{Payload: []byte("x")}})
		if err != nil {
			t.Fatal(err)
		}
		r := <-ch
		if r.Err == nil {
			t.Errorf("%s: no error", topic)
		}
		if _, err := p.Send([]ProducerMessage{{Payload: []byte("x")}}); err == nil {
			t.Errorf("%s: sent after", topic)
		}
	}
	// The connection lost: a send in flight fails, and so does closing.
	p3, err := cl.CreateProducer(ctx, "lost", "", None)
	if err != nil {
		t.Fatal(err)
	}
	b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool { return typ == typeSend })
	ch, err := p3.Send([]ProducerMessage{{Payload: []byte("x")}})
	if err != nil {
		t.Fatal(err)
	}
	b.closeConns()
	if r := <-ch; r.Err == nil {
		t.Error("no error")
	}
	if err := p3.Close(ctx); err != nil {
		t.Error(err)
	}
	if _, err := p3.Send([]ProducerMessage{{Payload: []byte("x")}}); err == nil {
		t.Error("sent")
	}
}

func TestConsumerFailures(t *testing.T) {
	b := newFakeBroker(t, nil)
	cl := newClient(t, Config{URL: b.url(), RequestTimeout: time.Second})
	ctx := ctxT(t)
	sub := func(topic string) *Consumer {
		cs, err := cl.Subscribe(ctx, topic, SubscribeOptions{Subscription: "s", Start: &EarliestID})
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	conn := func() *fakeConn {
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.conns[len(b.conns)-1]
	}
	msgCmd := func(id uint64, entry int64) []byte {
		return base(typeMessage, pb(nil).uint(1, id).messageID(2, MessageID{Ledger: 1, Entry: entry}).uint(3, 2))
	}
	cs := sub("t")
	m := metadata{producerName: "p", numMessages: 1}
	// A marker: nothing received, its permit given back.
	conn().send(payloadFrame(msgCmd(cs.id, 2), append(m.encode(), pb(nil).uint(20, 1)...), nil))
	// A message that is not a batch.
	plain := pb(nil).str(1, "p").uint(2, 5).uint(3, 1000).str(6, "key")
	conn().send(payloadFrame(msgCmd(cs.id, 3), plain, []byte("single")))
	got, err := cs.Receive(ctx)
	if err != nil || string(got.Payload) != "single" || string(got.Key) != "key" || got.ID.BatchIndex != -1 || got.Redelivery != 2 || got.SequenceID != 5 {
		t.Fatalf("%+v %v", got, err)
	}
	// A batch with its first message compacted out, and the broker's entry
	// metadata in front.
	payload := buildBatch([]ProducerMessage{{Payload: []byte("x")}, {Payload: []byte("y")}}, 0)
	first := binary.BigEndian.Uint32(payload)
	sm := append(append([]byte(nil), payload[4:4+first]...), pb(nil).bool(4, true)...)
	payload = append(binary.BigEndian.AppendUint32(nil, uint32(len(sm))), append(sm, payload[4+first:]...)...)
	fr := payloadFrame(msgCmd(cs.id, 4), (&metadata{producerName: "p", numMessages: 2}).encode(), payload)
	at := 8 + int(binary.BigEndian.Uint32(fr[4:]))
	entry := append(binary.BigEndian.AppendUint16(nil, magicBrokerEntry), 0, 0, 0, 2, 0x08, 0x01)
	fr = append(append(append([]byte(nil), fr[:at]...), entry...), fr[at:]...)
	binary.BigEndian.PutUint32(fr, uint32(len(fr)-4))
	conn().send(fr)
	got, err = cs.Receive(ctx)
	if err != nil || string(got.Payload) != "y" || got.ID.BatchIndex != 1 || got.ID.BatchSize != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	// What the client cannot read fails the consumer; a frame that breaks
	// the protocol, the connection and every consumer on it.
	tail := func(id uint64, rest ...byte) []byte {
		f := append(frame(msgCmd(id, 9)), rest...)
		binary.BigEndian.PutUint32(f, uint32(len(f)-4))
		return f
	}
	for name, mk := range map[string]func(id uint64) []byte{
		"encrypted": func(id uint64) []byte {
			return payloadFrame(msgCmd(id, 9), append(m.encode(), pb(nil).bytes(13, nil)...), nil)
		},
		"chunked": func(id uint64) []byte {
			return payloadFrame(msgCmd(id, 9), append(m.encode(), pb(nil).uint(27, 3)...), nil)
		},
		"zlib": func(id uint64) []byte {
			return payloadFrame(msgCmd(id, 9), (&metadata{compression: Zlib, uncompressedSize: 5}).encode(), []byte("junk"))
		},
		"cmd size":  func(uint64) []byte { return []byte{0, 0, 0, 8, 0, 0, 0, 9, 0, 0, 0, 0} },
		"checksum":  func(id uint64) []byte { return tail(id, 0x0e, 0x01, 0, 0, 0, 0, 0, 0, 0, 0) },
		"cut crc":   func(id uint64) []byte { return tail(id, 0x0e, 0x01, 0) },
		"no meta":   func(id uint64) []byte { return tail(id, 0) },
		"meta size": func(id uint64) []byte { return tail(id, 0, 0, 0, 9) },
		"entry":     func(id uint64) []byte { return tail(id, 0x0e, 0x02, 0, 0, 0, 9) },
		"bad meta":  func(id uint64) []byte { return tail(id, 0, 0, 0, 1, 0xff) },
		"batch": func(id uint64) []byte {
			return payloadFrame(msgCmd(id, 9), (&metadata{numMessages: 5}).encode(), []byte("ab"))
		},
		"command": func(uint64) []byte { return frame([]byte{0x08}) },
	} {
		cs := sub("t")
		conn().send(mk(cs.id))
		if _, err := cs.Receive(ctx); err == nil {
			t.Errorf("%s: received", name)
		}
	}
	// The broker closes a consumer; a topic ends.
	cs = sub("t")
	conn().cmd(typeCloseConsumer, pb(nil).uint(1, cs.id).uint(2, 0))
	if _, err := cs.Receive(ctx); !errors.Is(err, ErrClosed) {
		t.Error(err)
	}
	cs = sub("t")
	conn().cmd(typeReachedEndOfTopic, pb(nil).uint(1, cs.id))
	if _, err := cs.Receive(ctx); !errors.Is(err, ErrEndOfTopic) {
		t.Error(err)
	}
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	cs = sub("t")
	if _, err := cs.Receive(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Error(err)
	}
	// A subscription refused.
	b.setHook(func(c *fakeConn, typ int, fs fields, _ []byte) bool {
		if typ == typeSubscribe {
			c.fail(fs.u(5), 5, "busy")
			return true
		}
		return false
	})
	if _, err := cl.Subscribe(ctx, "t", SubscribeOptions{}); err == nil {
		t.Error("subscribed")
	}
}

func TestOverPermits(t *testing.T) {
	b := newFakeBroker(t, nil)
	cl := newClient(t, Config{URL: b.url()})
	ctx := ctxT(t)
	cs, err := cl.Subscribe(ctx, "t", SubscribeOptions{Subscription: "s", ReceiverQueue: 1})
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	c := b.conns[0]
	b.mu.Unlock()
	f := payloadFrame(base(typeMessage, pb(nil).uint(1, cs.id)), (&metadata{producerName: "p"}).encode(), []byte("x"))
	for range 10010 {
		c.send(f)
	}
	eventually(t, "the consumer fails", func() bool {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		return cs.err != nil
	})
}

func TestKeepAlive(t *testing.T) {
	b := newFakeBroker(t, nil)
	cl := newClient(t, Config{URL: b.url(), KeepAlive: 30 * time.Millisecond})
	ctx := ctxT(t)
	if _, err := cl.PartitionedMetadata(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a ping", func() bool { return b.count(typePing) > 0 })
	b.mu.Lock()
	c := b.conns[0]
	b.mu.Unlock()
	c.send(frame(pingCmd))
	eventually(t, "a pong", func() bool { return b.count(typePong) > 0 })
	// The broker goes quiet: the connection is closed, and a request on it
	// fails.
	b.setHook(func(*fakeConn, int, fields, []byte) bool { return true })
	cl.mu.Lock()
	var cn *conn
	for _, x := range cl.conns {
		cn = x
	}
	cl.mu.Unlock()
	eventually(t, "the connection closed", func() bool { return cn.failure() != nil })
	if _, err := cn.request(ctx, "x", func(uint64) []byte { return pingCmd }); err == nil {
		t.Error("a request on a closed connection")
	}
	if err := cn.write(frame(pingCmd)); err == nil {
		t.Error("a write on a closed connection")
	}
	if err := cn.add(func() {}); err == nil {
		t.Error("added")
	}
	// A new connection is dialed for the next request.
	b.setHook(nil)
	if _, err := cl.PartitionedMetadata(ctx, "t"); err != nil {
		t.Fatal(err)
	}
}

func TestRequestWhileClosing(t *testing.T) {
	b := newFakeBroker(t, nil)
	cl := newClient(t, Config{URL: b.url()})
	ctx := ctxT(t)
	b.setHook(func(c *fakeConn, typ int, _ fields, _ []byte) bool {
		if typ == typePartitionedMetadata {
			c.nc.Close()
			return true
		}
		return false
	})
	if _, err := cl.PartitionedMetadata(ctx, "t"); err == nil {
		t.Error("answered")
	}
	// A consumer and a producer on a connection that closes.
	b.setHook(nil)
	cs, err := cl.Subscribe(ctx, "t", SubscribeOptions{Subscription: "s"})
	if err != nil {
		t.Fatal(err)
	}
	b.closeConns()
	if _, err := cs.Receive(ctx); err == nil {
		t.Error("received")
	}
	if err := cs.Close(ctx); err != nil {
		t.Error(err)
	}
	if err := cs.AckCumulative(MessageID{}); err == nil {
		t.Error("acknowledged")
	}
	if _, err := cs.LastMessageID(ctx); err == nil {
		t.Error("asked")
	}
}

func TestProto(t *testing.T) {
	// Skipping fields of every wire type, and failing on bad ones.
	b := pb(nil).uint(1, 5)
	b = append(b.tag(2, wire64), make([]byte, 8)...)
	b = append(pb(b).tag(3, wire32), make([]byte, 4)...)
	b = pb(b).str(4, "x")
	r := pbReader{b: b}
	n := 0
	for _, ok := r.next(); ok; _, ok = r.next() {
		r.skip()
		n++
	}
	if n != 4 || r.err != nil {
		t.Fatal(n, r.err)
	}
	for _, bad := range [][]byte{
		{0x80},                            // a cut tag
		{0x00},                            // field 0
		{0x09, 1, 2},                      // a cut fixed64
		{0x0b},                            // wire type 3
		{0x12, 5, 1},                      // a cut string
		{0x08, 0x80},                      // a cut varint
		{0x0a, 0x01},                      // bytes read as a varint below
		append(pb(nil).tag(1, wire32), 1), // a cut fixed32
	} {
		r := pbReader{b: bad}
		for _, ok := r.next(); ok; _, ok = r.next() {
			r.skip()
		}
		r2 := pbReader{b: bad}
		if _, ok := r2.next(); ok {
			r2.uint()
		}
		r3 := pbReader{b: bad}
		if _, ok := r3.next(); ok {
			r3.bytes()
		}
		if r.err == nil && r2.err == nil && r3.err == nil {
			t.Errorf("%x parsed", bad)
		}
	}
	if _, err := decodeCommand(nil); err == nil {
		t.Error("an empty command")
	}
	if _, err := decodeCommand(base(typeSendReceipt, pb(nil).bytes(3, []byte{0xff}))); err == nil {
		t.Error("a bad message ID")
	}
	if _, err := decodeCommand(base(typeAuthChallenge, pb(nil).bytes(2, pb(nil).bytes(5, nil)))); err != nil {
		t.Error(err)
	}
	if _, err := decodeCommand(pb(nil).uint(1, typePing).uint(99, 1)); err != nil {
		t.Error(err)
	}
	if _, err := decodeMetadata(pb(nil).bytes(4, []byte{0xff}), false); err == nil {
		t.Error("a bad property")
	}
	if kv, err := decodeKeyValue(pb(nil).str(1, "k").uint(3, 1)); err != nil || kv.Key != "k" {
		t.Error(kv, err)
	}
	if m, err := decodeMessageID(pb(nil).uint(1, 1).uint(9, 1)); err != nil || m.Ledger != 1 {
		t.Error(m, err)
	}
	// Errors and names.
	if (&ServerError{Code: 99}).Error() != "pulsar: error 99" || ErrTopicNotFound.Error() != "pulsar: TopicNotFound" {
		t.Error((&ServerError{Code: 99}).Error())
	}
	if errors.Is(&ServerError{Code: 3}, ErrClosed) {
		t.Error("Is")
	}
	if Compression(9).String() != "compression(9)" || Zstd.String() != "zstd" {
		t.Error(Compression(9).String())
	}
	if c, err := ParseCompression(""); c != None || err != nil {
		t.Error(c, err)
	}
	if c, err := ParseCompression("LZ4"); c != LZ4 || err != nil {
		t.Error(c, err)
	}
	if _, err := ParseCompression("brotli"); err == nil {
		t.Error("brotli")
	}
	a := MessageID{Ledger: 1, Entry: 2, BatchIndex: 3}
	if a.String() != "1:2:0:3" {
		t.Error(a.String())
	}
	for _, c := range []struct {
		x, y MessageID
		want int
	}{
		{a, a, 0},
		{a, MessageID{Ledger: 2}, -1},
		{MessageID{Ledger: 2}, a, 1},
		{a, MessageID{Ledger: 1, Entry: 3}, -1},
		{a, MessageID{Ledger: 1, Entry: 2, BatchIndex: -1}, -1},
		{MessageID{Ledger: 1, Entry: 2, BatchIndex: 4}, a, 1},
	} {
		if got := c.x.Compare(c.y); got != c.want {
			t.Errorf("%v vs %v: %d", c.x, c.y, got)
		}
	}
}

func TestCompression(t *testing.T) {
	src := bytes.Repeat([]byte("compress me "), 500)
	for _, c := range []Compression{None, LZ4, Zlib, Zstd, Snappy} {
		out, err := decompress(c, compress(c, src), len(src), 1<<20)
		if err != nil || !bytes.Equal(out, src) {
			t.Errorf("%v: %v", c, err)
		}
		if c == None {
			continue
		}
		if _, err := decompress(c, compress(c, src), len(src)-1, 1<<20); err == nil {
			t.Errorf("%v: a wrong size", c)
		}
		if _, err := decompress(c, compress(c, src), len(src), 10); err == nil {
			t.Errorf("%v: past the bound", c)
		}
		if _, err := decompress(c, []byte("junk"), 4, 1<<20); err == nil {
			t.Errorf("%v: junk", c)
		}
	}
	if _, err := decompress(Compression(9), src, 1, 10); !errors.Is(err, ErrUnsupported) {
		t.Error(err)
	}
}

func TestParseURL(t *testing.T) {
	for in, want := range map[string]string{
		"pulsar://a":             "a:6650",
		"pulsar+ssl://a":         "a:6651",
		"pulsar://a:1,b:2/":      "a:1 b:2",
		"pulsar://[::1]:6650":    "[::1]:6650",
		"pulsar+ssl://h.x:7,h.y": "h.x:7 h.y:6651",
	} {
		hosts, _, err := parseURL(in)
		if err != nil || strings.Join(hosts, " ") != want {
			t.Errorf("%s: %v %v", in, hosts, err)
		}
	}
	for _, bad := range []string{"http://a", "pulsar://", "pulsar://a/b", "pulsar://u@a"} {
		if _, err := New(Config{URL: bad}); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
	cl, err := New(Config{URL: "pulsar+ssl://a"})
	if err != nil || cl.cfg.TLS == nil || cl.cfg.ClientVersion != "fit-sync" {
		t.Error(err)
	}
}

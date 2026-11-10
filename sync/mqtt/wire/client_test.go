package mqttwire

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

var versions = []byte{Version311, Version5}

func vname(v byte) string {
	if v == Version311 {
		return "3.1.1"
	}
	return "5"
}

// TestPubSub publishes at each QoS to a subscriber of each version: every
// message arrives once, at the QoS of the publish and subscription's
// least, and the broker lets go of each once acknowledged.
func TestPubSub(t *testing.T) {
	for _, v := range versions {
		t.Run(vname(v), func(t *testing.T) {
			b := newFakeBroker(t, nil)
			sub := dial(t, Config{Address: b.addr(), Version: v, ClientID: "sub"})
			codes, err := sub.Subscribe(context.Background(), Subscription{Filter: "a/+", QoS: 2}, Subscription{Filter: "b/#", QoS: 1})
			if err != nil || !bytes.Equal(codes, []byte{2, 1}) {
				t.Fatalf("%v %v", codes, err)
			}
			pub := dial(t, Config{Address: b.addr(), Version: v, CleanStart: true})
			ctx := context.Background()
			for q := byte(0); q <= 2; q++ {
				if err := pub.Publish(ctx, &Message{Topic: fmt.Sprintf("a/%d", q), QoS: q, Payload: []byte{q}}); err != nil {
					t.Fatal(err)
				}
				m := next(t, sub)
				if m.Topic != fmt.Sprintf("a/%d", q) || m.QoS != q || !bytes.Equal(m.Payload, []byte{q}) || m.Dup || m.Retain {
					t.Fatalf("%+v", m)
				}
				if err := sub.Ack(m); err != nil {
					t.Fatal(err)
				}
			}
			if err := pub.Publish(ctx, &Message{Topic: "b/c/d", QoS: 2, Retain: true}); err != nil {
				t.Fatal(err)
			}
			if m := next(t, sub); m.QoS != 1 || !m.Retain || m.Topic != "b/c/d" {
				t.Fatalf("%+v", m)
			}
			none(t, sub)
			// The QoS 1 message of b/c/d is not acknowledged; the others are.
			waitFor(t, "acknowledgements", func() bool { return b.inflight("sub") == 1 })
			flow := strings.Join(b.flow(), " ")
			if !strings.Contains(flow, "PUBREC") || !strings.Contains(flow, "PUBREL") || !strings.Contains(flow, "PUBACK") {
				t.Fatal(flow)
			}
			if len(b.published()) != 4 {
				t.Fatal(b.published())
			}
		})
	}
}

// TestSessionResumed: messages not acknowledged come again, as duplicates,
// on the session's next connection, and those published while the client
// was away come then too; a clean start drops them.
func TestSessionResumed(t *testing.T) {
	for _, v := range versions {
		t.Run(vname(v), func(t *testing.T) {
			b := newFakeBroker(t, nil)
			cfg := Config{Address: b.addr(), Version: v, ClientID: "s", SessionExpiry: time.Hour}
			sub := dial(t, cfg)
			if sub.SessionPresent() {
				t.Fatal("a session at first")
			}
			if _, err := sub.Subscribe(context.Background(), Subscription{Filter: "t", QoS: 2}); err != nil {
				t.Fatal(err)
			}
			pub := dial(t, Config{Address: b.addr(), Version: v, ClientID: "p", CleanStart: true})
			for i := range 3 {
				pub.Publish(context.Background(), &Message{Topic: "t", QoS: byte(1 + i%2), Payload: []byte{byte(i)}})
			}
			m0 := next(t, sub)
			next(t, sub)
			next(t, sub)
			sub.Ack(m0)
			waitFor(t, "the first acknowledged", func() bool { return b.inflight("s") == 2 })
			sub.Close()
			if err := sub.Close(); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(sub.Err(), ErrClosed) {
				t.Fatal(sub.Err())
			}
			waitFor(t, "the session let go", func() bool { return !b.connected("s") })
			pub.Publish(context.Background(), &Message{Topic: "t", QoS: 1, Payload: []byte{3}})
			sub = dial(t, cfg)
			if !sub.SessionPresent() {
				t.Fatal("no session")
			}
			for i := 1; i <= 3; i++ {
				m := next(t, sub)
				if m.Payload[0] != byte(i) || m.Dup != (i < 3) {
					t.Fatalf("%d: %+v", i, m)
				}
				sub.Ack(m)
			}
			waitFor(t, "all acknowledged", func() bool { return b.inflight("s") == 0 })
			sub.Close()
			cfg.CleanStart = true
			if sub = dial(t, cfg); sub.SessionPresent() {
				t.Fatal("a session after a clean start")
			}
		})
	}
}

// TestQoS2Once: a QoS 2 message the server sends again before it is
// acknowledged is not handed out again, nor after its PUBREC; PUBREL is
// answered PUBCOMP.
func TestQoS2Once(t *testing.T) {
	for _, v := range versions {
		t.Run(vname(v), func(t *testing.T) {
			s := newRawServer(t)
			c, nc, br := rawClient(t, s, Config{Version: v, ClientID: "x"}, connackOK(v))
			pub := func(dup bool) {
				first := typePublish<<4 | 2<<1
				if dup {
					first |= 8
				}
				body := appendUint16(appendString(nil, "q"), 7)
				if v == Version5 {
					body = append(body, 0)
				}
				nc.Write(packet(first, append(body, "x"...)))
			}
			pub(false)
			pub(true)
			m := next(t, c)
			none(t, c)
			if err := c.Ack(m); err != nil {
				t.Fatal(err)
			}
			if first, body := readNext(t, nc, br); first != typePubrec<<4 || !bytes.Equal(body, []byte{0, 7}) {
				t.Fatalf("%x %x", first, body)
			}
			pub(true)
			none(t, c)
			if first, _ := readNext(t, nc, br); first != typePubrec<<4 {
				t.Fatalf("%x", first)
			}
			nc.Write([]byte{typePubrel<<4 | 2, 2, 0, 7})
			if first, body := readNext(t, nc, br); first != typePubcomp<<4 || !bytes.Equal(body, []byte{0, 7}) {
				t.Fatalf("%x %x", first, body)
			}
			// Once released, the identifier is a new message's.
			pub(false)
			next(t, c)
		})
	}
}

func connackOK(v byte) []byte {
	if v == Version5 {
		return []byte{typeConnack << 4, 3, 0, 0, 0}
	}
	return []byte{typeConnack << 4, 2, 0, 0}
}

// TestProperties carries every MQTT 5 property of a message from a
// publisher to a subscriber, and leaves them out in 3.1.1.
func TestProperties(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.subIDs = true })
	sub := dial(t, Config{Address: b.addr(), ClientID: "sub"})
	sub.Subscribe(context.Background(), Subscription{Filter: "p", QoS: 1, NoLocal: true, RetainAsPublished: true, RetainHandling: 1})
	sub3 := dial(t, Config{Address: b.addr(), ClientID: "sub3", Version: Version311})
	sub3.Subscribe(context.Background(), Subscription{Filter: "p", QoS: 1})
	pub := dial(t, Config{Address: b.addr(), CleanStart: true})
	in := &Message{Topic: "p", QoS: 1, Payload: []byte("héllo"), PayloadUTF8: true, MessageExpiry: 90 * time.Second,
		ContentType: "text/plain", ResponseTopic: "r", CorrelationData: []byte{1, 2},
		UserProperties: []UserProperty{{"a", "1"}, {"a", "2"}, {"b", ""}}}
	if err := pub.Publish(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	m := next(t, sub)
	m.id, in.id = 0, 0
	in.SubscriptionIDs = []int{300}
	if !reflect.DeepEqual(m, in) {
		t.Fatalf("%+v\n%+v", m, in)
	}
	m3 := next(t, sub3)
	if m3.ContentType != "" || m3.UserProperties != nil || string(m3.Payload) != "héllo" {
		t.Fatalf("%+v", m3)
	}
	// A 3.1.1 publisher sends none.
	pub3 := dial(t, Config{Address: b.addr(), CleanStart: true, Version: Version311})
	pub3.Publish(context.Background(), in)
	if m := next(t, sub); m.ContentType != "" || m.UserProperties != nil {
		t.Fatalf("%+v", m)
	}
}

// TestTopicAliases: the client sends aliases for as many topics as the
// server takes, and reads those the server sends.
func TestTopicAliases(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) {
		b.aliases = true
		b.connack = func(w *propsWriter) { w.u16(propTopicAliasMaximum, 2) }
	})
	sub := dial(t, Config{Address: b.addr(), ClientID: "sub", TopicAliasMaximum: 1})
	sub.Subscribe(context.Background(), Subscription{Filter: "#"})
	pub := dial(t, Config{Address: b.addr(), CleanStart: true})
	topics := []string{"x", "y", "z", "x", "y", "z", "x"}
	for _, tp := range topics {
		if err := pub.Publish(context.Background(), &Message{Topic: tp, Payload: []byte(tp)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tp := range topics {
		if m := next(t, sub); m.Topic != tp || string(m.Payload) != tp {
			t.Fatalf("%q: %+v", tp, m)
		}
	}
	got := b.published()
	for i, tp := range topics {
		if got[i].Topic != tp {
			t.Fatalf("%d: %q", i, got[i].Topic)
		}
	}
	if len(pub.aliases) != 2 {
		t.Fatal(pub.aliases)
	}
}

// TestBadAliases: an alias past the client's most, or one never set, ends
// the connection with reason codes.
func TestBadAliases(t *testing.T) {
	for _, tc := range []struct {
		topic string
		alias uint16
		code  byte
	}{{"t", 3, 0x94}, {"", 1, 0x82}} {
		s := newRawServer(t)
		c, nc, br := rawClient(t, s, Config{TopicAliasMaximum: 2, ClientID: "x"}, connackOK(Version5))
		var w propsWriter
		w.u16(propTopicAlias, tc.alias)
		nc.Write(packet(typePublish<<4, w.appendTo(appendString(nil, tc.topic))))
		if first, body := readNext(t, nc, br); first != typeDisconnect<<4 || body[0] != tc.code {
			t.Fatalf("%x %x", first, body)
		}
		<-c.Done()
	}
}

// TestAuth: refusals of both versions are ConnectErrors, of credentials.
func TestAuth(t *testing.T) {
	for _, v := range versions {
		t.Run(vname(v), func(t *testing.T) {
			b := newFakeBroker(t, nil)
			b.set(func(b *fakeBroker) { b.users = map[string]string{"u": "pw"} })
			c := dial(t, Config{Address: b.addr(), Version: v, ClientID: "a", Username: "u", Password: "pw"})
			if conn := b.waitConn(t); conn.user != "u" {
				t.Fatal(conn.user)
			}
			c.Close()
			for _, cfg := range []Config{{Username: "u", Password: "no"}, {}} {
				cfg.Address, cfg.Version, cfg.ClientID = b.addr(), v, "a"
				_, err := Dial(context.Background(), cfg)
				var ce *ConnectError
				if !errors.As(err, &ce) || !ce.Auth() || !strings.Contains(err.Error(), "bad user name or password") {
					t.Fatalf("%v", err)
				}
			}
		})
	}
	// A v5 password alone is sent.
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.users = map[string]string{"": "pw"} })
	if _, err := Dial(context.Background(), Config{Address: b.addr(), ClientID: "a", Password: "pw"}); err == nil {
		t.Fatal("no user name passed")
	}
}

// TestConnectErrors reads refusals of every kind, and their text.
func TestConnectErrors(t *testing.T) {
	for _, tc := range []struct {
		v    byte
		code byte
		want string
		auth bool
	}{
		{Version311, 1, "unacceptable protocol version", false}, {Version311, 5, "not authorized", true},
		{Version311, 9, "unknown return code", false}, {Version5, 0x87, "not authorized", true},
		{Version5, 0x8C, "bad authentication method", true}, {Version5, 0x85, "client identifier not valid", false},
		{Version5, 0x7F, "unknown reason", false},
	} {
		b := newFakeBroker(t, nil)
		b.set(func(b *fakeBroker) {
			b.refuse = tc.code
			b.connack = func(w *propsWriter) { w.str(propReasonString, "because") }
		})
		_, err := Dial(context.Background(), Config{Address: b.addr(), Version: tc.v, ClientID: "a"})
		var ce *ConnectError
		if !errors.As(err, &ce) || ce.Code != tc.code || ce.Auth() != tc.auth || !strings.Contains(err.Error(), tc.want) ||
			(tc.v == Version5) != strings.Contains(err.Error(), "because") {
			t.Fatalf("%d %x: %v", tc.v, tc.code, err)
		}
	}
}

// TestConfigErrors: what CONNECT cannot carry fails before connecting.
func TestConfigErrors(t *testing.T) {
	for _, cfg := range []Config{
		{Version: 3}, {Version: Version311}, {Version: Version311, ClientID: "a", Password: "p"},
		{ClientID: "a\x00"}, {ClientID: "a", KeepAlive: 70000 * time.Second},
	} {
		cfg.Address = "127.0.0.1:1"
		if _, err := Dial(context.Background(), cfg); err == nil || strings.Contains(err.Error(), "connect to") {
			t.Fatalf("%+v: %v", cfg, err)
		}
	}
	if _, err := Dial(context.Background(), Config{Address: "127.0.0.1:1", ClientID: "a"}); err == nil {
		t.Fatal("connected to nothing")
	}
}

// TestConnectFlow reads what CONNECT carries, and a session expiry and
// keep-alive the server sets.
func TestConnectFlow(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.connack = func(w *propsWriter) { w.u16(propServerKeepAlive, 0) } })
	c := dial(t, Config{Address: b.addr(), ClientID: "a", KeepAlive: 1500 * time.Millisecond, SessionExpiry: 90 * time.Minute,
		ReceiveMaximum: 100000, MaxPacketSize: 1 << 30, TopicAliasMaximum: 1 << 20})
	conn := b.waitConn(t)
	if conn.keepAlive != 2 || conn.props.sessionExpiry != 5400 || conn.props.receiveMax != 65535 ||
		conn.props.maxPacket != maxRemaining+5 || conn.props.aliasMax != 65535 {
		t.Fatalf("%d %+v", conn.keepAlive, conn.props)
	}
	if c.cfg.KeepAlive != 0 {
		t.Fatal(c.cfg.KeepAlive)
	}
	// A server-assigned client ID, in 3.1.1 with a clean start too.
	dial(t, Config{Address: b.addr(), CleanStart: true, Version: Version311})
	dial(t, Config{Address: b.addr()})
}

// TestKeepAlive pings and stays up while answered, and ends the
// connection on a ping not answered.
func TestKeepAlive(t *testing.T) {
	b := newFakeBroker(t, nil)
	c := dial(t, Config{Address: b.addr(), ClientID: "a", KeepAlive: time.Second})
	time.Sleep(1300 * time.Millisecond)
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	if !strings.Contains(strings.Join(b.flow(), " "), "PINGREQ") {
		t.Fatal(b.flow())
	}
	b.set(func(b *fakeBroker) { b.noPing = true })
	select {
	case <-c.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("still up")
	}
	if !errors.Is(c.Err(), ErrKeepAlive) {
		t.Fatal(c.Err())
	}
}

// TestKeepAliveWhileFull: a server's answer that waits behind messages not
// taken is not a keep-alive missed.
func TestKeepAliveWhileFull(t *testing.T) {
	s := newRawServer(t)
	c, nc, br := rawClient(t, s, Config{ClientID: "a", KeepAlive: time.Second, ReceiveMaximum: 1}, connackOK(Version5))
	for range 2 {
		nc.Write(packet(typePublish<<4, append(appendString(nil, "t"), 0)))
	}
	if first, _ := readNext(t, nc, br); first != typePingreq<<4 {
		t.Fatalf("%x", first)
	}
	time.Sleep(1600 * time.Millisecond)
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	nc.Write([]byte{typePingresp << 4, 0})
	next(t, c)
	next(t, c)
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

// TestPublishRefused: reason codes of 0x80 and over are errors; below,
// as no matching subscribers, are not.
func TestPublishRefused(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.codes = map[string]byte{"bad": 0x90, "quiet": 0x10} })
	c := dial(t, Config{Address: b.addr(), ClientID: "a"})
	for q := byte(1); q <= 2; q++ {
		err := c.Publish(context.Background(), &Message{Topic: "bad", QoS: q})
		var re *ReasonError
		if !errors.As(err, &re) || re.Code != 0x90 || re.Reason != "no" || !strings.Contains(err.Error(), "topic name invalid: no") {
			t.Fatalf("%v", err)
		}
		if err := c.Publish(context.Background(), &Message{Topic: "quiet", QoS: q}); err != nil {
			t.Fatal(err)
		}
	}
	if s := (&ReasonError{Packet: "PUBACK", Code: 0x97}).Error(); s != "mqtt: PUBACK 0x97 quota exceeded" {
		t.Fatal(s)
	}
	waitFor(t, "the inflight let go", func() bool { return len(c.sem) == 0 })
}

// TestServerLimits: QoS and retain the server does not take, and packets
// past its size, fail the publish alone.
func TestServerLimits(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) {
		b.connack = func(w *propsWriter) {
			w.byte(propMaximumQoS, 1)
			w.byte(propRetainAvailable, 0)
			w.u32(propMaximumPacketSize, 100)
			w.u16(propReceiveMaximum, 1)
		}
	})
	c := dial(t, Config{Address: b.addr(), ClientID: "a"})
	ctx := context.Background()
	for _, m := range []*Message{{Topic: "t", QoS: 2}, {Topic: "t", Retain: true}} {
		if err := c.Publish(ctx, m); !errors.Is(err, ErrNotSupported) {
			t.Fatal(err)
		}
	}
	for _, m := range []*Message{{Topic: "t", QoS: 3}, {Topic: "+"}} {
		if err := c.Publish(ctx, m); err == nil {
			t.Fatal("published")
		}
	}
	for q := byte(0); q <= 1; q++ {
		if err := c.Publish(ctx, &Message{Topic: "t", QoS: q, Payload: make([]byte, 100)}); !errors.Is(err, ErrPacketTooLarge) {
			t.Fatal(err)
		}
	}
	if err := c.Publish(ctx, &Message{Topic: "t", QoS: 1, Payload: make([]byte, 50)}); err != nil {
		t.Fatal(err)
	}
	if c.cfg.MaxInflight != 1 {
		t.Fatal(c.cfg.MaxInflight)
	}
}

// TestInflight: publishes past MaxInflight wait for room, ctx bounding the
// wait; Wait is bounded by its ctx too.
func TestInflight(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.holdAcks = true })
	c := dial(t, Config{Address: b.addr(), ClientID: "a", MaxInflight: 2})
	bg := context.Background()
	d1, err := c.Send(bg, &Message{Topic: "t", QoS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(bg, &Message{Topic: "t", QoS: 2}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(bg, 30*time.Millisecond)
	defer cancel()
	if _, err := c.Send(ctx, &Message{Topic: "t", QoS: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := d1.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// QoS 0 is not bounded.
	if err := c.Publish(bg, &Message{Topic: "t"}); err != nil {
		t.Fatal(err)
	}
	// The connection lost fails the publishes under way, and those that
	// wait for room.
	waiting := make(chan error, 1)
	go func() {
		_, err := c.Send(bg, &Message{Topic: "t", QoS: 1})
		waiting <- err
	}()
	time.Sleep(10 * time.Millisecond)
	conn := b.waitConn(t)
	conn.nc.Close()
	if err := d1.Wait(bg); err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatal(err)
	}
	if err := <-waiting; err == nil {
		t.Fatal("sent")
	}
	if _, err := c.Send(bg, &Message{Topic: "t", QoS: 1}); err == nil {
		t.Fatal("sent")
	}
	if err := c.Publish(bg, &Message{Topic: "t"}); err == nil {
		t.Fatal("sent")
	}
	if _, err := c.Subscribe(bg, Subscription{Filter: "t"}); err == nil {
		t.Fatal("subscribed")
	}
	if err := c.Ack(&Message{QoS: 1}); err == nil {
		t.Fatal("acknowledged")
	}
	c.Ack(&Message{QoS: 2})
	if err := c.Ack(&Message{}); err != nil {
		t.Fatal(err)
	}
}

// TestSubscribeErrors: refused filters, filters not valid, a SUBACK not
// coming.
func TestSubscribeErrors(t *testing.T) {
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.deny = map[string]bool{"no": true} })
	c := dial(t, Config{Address: b.addr(), ClientID: "a"})
	bg := context.Background()
	codes, err := c.Subscribe(bg, Subscription{Filter: "ok"}, Subscription{Filter: "no"})
	var re *ReasonError
	if !errors.As(err, &re) || re.Code != 0x87 || re.Reason != "as asked" || !bytes.Equal(codes, []byte{0, 0x87}) {
		t.Fatalf("%v %v", codes, err)
	}
	for _, subs := range [][]Subscription{nil, {{Filter: "a/#/b"}}, {{Filter: "a", QoS: 3}}, {{Filter: "a", RetainHandling: 3}}} {
		if _, err := c.Subscribe(bg, subs...); err == nil {
			t.Fatalf("%v subscribed", subs)
		}
	}
	b.set(func(b *fakeBroker) { b.noSuback = true })
	ctx, cancel := context.WithTimeout(bg, 20*time.Millisecond)
	defer cancel()
	if _, err := c.Subscribe(ctx, Subscription{Filter: "x"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// A SUBACK of another count.
	s := newRawServer(t)
	c, nc, br := rawClient(t, s, Config{Version: Version311, ClientID: "a"}, connackOK(Version311))
	go func() {
		readNext(t, nc, br)
		nc.Write([]byte{typeSuback << 4, 3, 0, 1, 0})
	}()
	if _, err := c.Subscribe(bg, Subscription{Filter: "a"}, Subscription{Filter: "b"}); err == nil || !strings.Contains(err.Error(), "1 codes for 2") {
		t.Fatal(err)
	}
}

// TestAcks covers PUBACK, PUBREC and PUBCOMP a client does not wait for.
func TestAcks(t *testing.T) {
	s := newRawServer(t)
	c, nc, br := rawClient(t, s, Config{ClientID: "a"}, connackOK(Version5))
	bg := context.Background()
	d, err := c.Send(bg, &Message{Topic: "t", QoS: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, body := readNext(t, nc, br)
	id := body[3:5]
	// A PUBACK of a QoS 2 publish, and a PUBCOMP before the PUBREC, are
	// let be.
	nc.Write(append([]byte{typePuback << 4, 2}, id...))
	nc.Write(append([]byte{typePubcomp << 4, 2}, id...))
	nc.Write(append([]byte{typePubrec << 4, 2}, id...))
	if first, b := readNext(t, nc, br); first != typePubrel<<4|2 || !bytes.Equal(b, id) {
		t.Fatalf("%x %x", first, b)
	}
	// A PUBREC again is answered PUBREL again; one of nothing, PUBREL
	// of reason 0x92.
	nc.Write(append([]byte{typePubrec << 4, 2}, id...))
	if first, b := readNext(t, nc, br); first != typePubrel<<4|2 || !bytes.Equal(b, id) {
		t.Fatalf("%x %x", first, b)
	}
	nc.Write([]byte{typePubrec << 4, 2, 0x55, 0x55})
	if first, b := readNext(t, nc, br); first != typePubrel<<4|2 || !bytes.Equal(b, []byte{0x55, 0x55, 0x92}) {
		t.Fatalf("%x %x", first, b)
	}
	// PUBCOMP with a reason code and properties.
	nc.Write(append(append([]byte{typePubcomp << 4, 4}, id...), 0, 0))
	if err := d.Wait(bg); err != nil {
		t.Fatal(err)
	}
	// PUBREC 0x80 ends a QoS 2 publish without PUBREL.
	d, _ = c.Send(bg, &Message{Topic: "t", QoS: 2})
	_, body = readNext(t, nc, br)
	nc.Write(append(append([]byte{typePubrec << 4, 3}, body[3:5]...), 0x80))
	if err := d.Wait(bg); err == nil || !strings.Contains(err.Error(), "PUBREC 0x80 unspecified error") {
		t.Fatal(err)
	}
	// v3.1.1 PUBREL to a PUBREC of nothing has no reason code.
	if p := pubrel(Version311, 1, 0x92); len(p) != 4 {
		t.Fatal(p)
	}
}

// TestBadPackets: what the server sends that MQTT does not allow ends the
// connection, with a DISCONNECT of its reason in MQTT 5.
func TestBadPackets(t *testing.T) {
	pub := func(flags byte, body []byte) []byte { return packet(typePublish<<4|flags, body) }
	for i, tc := range []struct {
		v    byte
		p    []byte
		code byte // 0: no DISCONNECT expected
		want string
	}{
		{Version5, []byte{0x30, 0xFF, 0xFF, 0xFF, 0xFF, 0x01}, 0x81, "malformed"},
		{Version5, pub(6, appendString(nil, "t")), 0x81, "QoS 3"},
		{Version5, pub(2, append(appendString(nil, "t"), 0, 0, 0)), 0x82, "identifier 0"},
		{Version5, pub(0, append(appendString(nil, "+"), 0)), 0x82, "wildcard"},
		{Version5, pub(0, append(appendString(nil, "t"), 4, 0x0B, 1, 0x0B, 0)), 0x82, "subscription identifier 0"},
		{Version5, pub(0, append(appendString(nil, "t"), 1, 0x7E)), 0x81, "malformed"},
		{Version5, pub(0, append(appendString(nil, "t"), 5, 0x26, 0, 1, 'a', 0)), 0x81, "malformed"},
		{Version5, pub(0, append(appendString(nil, "t"), 2, 0x01)), 0x81, "malformed"},
		{Version5, pub(0, append(appendString(nil, "t\x00"), 0)), 0x81, "malformed"},
		{Version5, pub(0, []byte{0, 5, 'a'}), 0x81, "malformed"},
		{Version5, []byte{typePuback << 4, 1, 0}, 0x81, "PUBACK"},
		{Version5, []byte{typePuback << 4, 5, 0, 1, 0, 1, 0}, 0x81, "PUBACK"},
		{Version5, []byte{typePubrel << 4, 2, 0, 1}, 0x81, "PUBREL"},
		{Version5, []byte{typePubrel<<4 | 2, 1, 0}, 0x81, "PUBREL"},
		{Version5, []byte{typeSuback << 4, 3, 0, 1, 0}, 0x81, "SUBACK"},
		{Version5, []byte{typePingresp<<4 | 1, 0}, 0x81, "flags"},
		{Version5, []byte{typeConnack << 4, 2, 0, 0}, 0x82, "CONNACK"},
		{Version5, []byte{typeAuth << 4, 0}, 0x82, "AUTH"},
		{Version311, []byte{typeDisconnect << 4, 0}, 0, "DISCONNECT"},
		{Version5, []byte{typeDisconnect << 4, 0}, 0, "DISCONNECT 0x00"},
		{Version5, []byte{typeDisconnect << 4, 5, 0x8B, 3, 0x1F, 0, 0}, 0, "server shutting down"},
		{Version5, []byte{0x30, 0x80, 0x80, 0x80, 0x80}, 0x81, "malformed"},
	} {
		s := newRawServer(t)
		c, nc, br := rawClient(t, s, Config{Version: tc.v, ClientID: "a"}, connackOK(tc.v))
		nc.Write(tc.p)
		if tc.code != 0 {
			if first, body := readNext(t, nc, br); first != typeDisconnect<<4 || body[0] != tc.code {
				t.Fatalf("%d: %x %x", i, first, body)
			}
		}
		<-c.Done()
		if err := c.Err(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%d: %v", i, err)
		}
	}
}

// TestPacketSizes: a packet past MaxPacketSize ends the connection; one at
// it does not.
func TestPacketSizes(t *testing.T) {
	for _, v := range versions {
		s := newRawServer(t)
		c, nc, br := rawClient(t, s, Config{Version: v, ClientID: "a", MaxPacketSize: 64}, connackOK(v))
		body := appendString(nil, "t")
		if v == Version5 {
			body = append(body, 0)
		}
		ok := packet(typePublish<<4, append(body, make([]byte, 62-len(body))...))
		if len(ok) != 64 {
			t.Fatal(len(ok))
		}
		nc.Write(ok)
		next(t, c)
		nc.Write(packet(typePublish<<4, append(body, make([]byte, 63-len(body))...)))
		if v == Version5 {
			if first, b := readNext(t, nc, br); first != typeDisconnect<<4 || b[0] != 0x95 {
				t.Fatalf("%x %x", first, b)
			}
		}
		<-c.Done()
		if !errors.Is(c.Err(), ErrPacketTooLarge) {
			t.Fatal(c.Err())
		}
	}
}

// TestDialErrors: no answer, another answer, a malformed CONNACK, ctx
// ending.
func TestDialErrors(t *testing.T) {
	s := newRawServer(t)
	try := func(cfg Config, ctx context.Context, answer []byte) error {
		cfg.Address, cfg.ClientID = s.addr(), "a"
		ch := make(chan error, 1)
		go func() {
			_, err := Dial(ctx, cfg)
			ch <- err
		}()
		nc, _ := s.accept(t, answer)
		if answer != nil {
			nc.Close()
		}
		return <-ch
	}
	bg := context.Background()
	if err := try(Config{Timeout: 50 * time.Millisecond}, bg, nil); err == nil {
		t.Fatal("connected")
	}
	ctx, cancel := context.WithCancel(bg)
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	if err := try(Config{}, ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, answer := range [][]byte{
		{typePingresp << 4, 0}, {typeConnack << 4, 1, 0}, {typeConnack << 4, 2, 2, 0}, {typeConnack << 4, 3, 0, 0, 0}, {},
	} {
		v := Version311
		if len(answer) == 5 {
			v = Version5
			answer = []byte{typeConnack << 4, 2, 0, 0} // no properties
		}
		if err := try(Config{Version: v}, bg, answer); err == nil {
			t.Fatalf("%x: connected", answer)
		}
	}
	// A refused write of CONNECT.
	dialed := func(ctx context.Context, network, addr string) (net.Conn, error) {
		a, b := net.Pipe()
		b.Close()
		return a, nil
	}
	if _, err := Dial(bg, Config{Address: "x:1", ClientID: "a", Dial: dialed}); err == nil || !strings.Contains(err.Error(), "send CONNECT") {
		t.Fatal(err)
	}
}

// TestTLS connects over TLS, the server's certificate checked.
func TestTLS(t *testing.T) {
	cert, pool := testCert(t, "mqtt.test")
	b := newFakeBroker(t, &tls.Config{Certificates: []tls.Certificate{cert}})
	c := dial(t, Config{Address: b.addr(), ClientID: "a", TLS: &tls.Config{RootCAs: pool, ServerName: "mqtt.test"}})
	if _, err := c.Subscribe(context.Background(), Subscription{Filter: "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(context.Background(), Config{Address: b.addr(), ClientID: "a", TLS: &tls.Config{RootCAs: pool}}); err == nil ||
		!strings.Contains(err.Error(), "TLS") {
		t.Fatal(err)
	}
}

// TestCloseDisconnects: Close sends DISCONNECT, normal; a takeover closes
// the old connection.
func TestCloseDisconnects(t *testing.T) {
	for _, v := range versions {
		b := newFakeBroker(t, nil)
		c := dial(t, Config{Address: b.addr(), ClientID: "a", Version: v})
		conn := b.waitConn(t)
		c.Close()
		select {
		case code := <-conn.disc:
			if code != 0 {
				t.Fatal(code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no DISCONNECT")
		}
	}
	b := newFakeBroker(t, nil)
	c := dial(t, Config{Address: b.addr(), ClientID: "a"})
	dial(t, Config{Address: b.addr(), ClientID: "a"})
	<-c.Done()
	if errors.Is(c.Err(), ErrClosed) {
		t.Fatal(c.Err())
	}
}

// TestPacketIDs: identifiers skip those in use, wrap past 65535, and run
// out.
func TestPacketIDs(t *testing.T) {
	c := &Client{pending: map[uint16]*Delivery{1: {}}, subs: map[uint16]chan subResult{2: nil}}
	if id, _ := c.allocID(); id != 3 {
		t.Fatal(id)
	}
	c.nextID = 65535
	if id, _ := c.allocID(); id != 3 {
		t.Fatal(id)
	}
	for i := range 65536 {
		c.pending[uint16(i)] = &Delivery{}
	}
	if _, err := c.allocID(); err == nil {
		t.Fatal("an identifier")
	}
	// The same on Send and Subscribe.
	b := newFakeBroker(t, nil)
	cl := dial(t, Config{Address: b.addr(), ClientID: "a"})
	cl.mu.Lock()
	for i := range 65536 {
		cl.pending[uint16(i)] = &Delivery{}
	}
	cl.mu.Unlock()
	if _, err := cl.Send(context.Background(), &Message{Topic: "t", QoS: 1}); err == nil {
		t.Fatal("sent")
	}
	if _, err := cl.Subscribe(context.Background(), Subscription{Filter: "t"}); err == nil {
		t.Fatal("subscribed")
	}
	cl.mu.Lock()
	cl.pending = map[uint16]*Delivery{}
	cl.mu.Unlock()
}

// TestSendAfterFail: a Send that took room after the connection failed
// gives it back.
func TestSendAfterFail(t *testing.T) {
	b := newFakeBroker(t, nil)
	c := dial(t, Config{Address: b.addr(), ClientID: "a"})
	c.mu.Lock()
	c.err = errors.New("gone")
	c.mu.Unlock()
	if _, err := c.Send(context.Background(), &Message{Topic: "t", QoS: 1}); err == nil || err.Error() != "gone" {
		t.Fatal(err)
	}
	if len(c.sem) != 0 {
		t.Fatal(len(c.sem))
	}
	c.mu.Lock()
	c.err = nil
	c.mu.Unlock()
}

// TestTopics checks names, filters and matching.
func TestTopics(t *testing.T) {
	for name, ok := range map[string]bool{"a": true, "a/b/": true, "/": true, "$SYS/x": true, "": false, "a+": false,
		"a/#": false, "a\x00": false, "\xff": false, strings.Repeat("a", 65536): false} {
		if (ValidTopicName(name) == nil) != ok {
			t.Errorf("name %q", name)
		}
	}
	for f, ok := range map[string]bool{"a": true, "#": true, "+": true, "a/+/b": true, "a/#": true, "+/+": true,
		"$share/g/a/#": true, "": false, "a/#/b": false, "a+": false, "a/b#": false, "\xff": false,
		"$share/g": false, "$share//a": false, "$share/g+/a": false, "$share/g/": false} {
		if (ValidTopicFilter(f) == nil) != ok {
			t.Errorf("filter %q", f)
		}
	}
	for _, tc := range []struct {
		f, t string
		ok   bool
	}{
		{"a/b", "a/b", true}, {"a/+", "a/b", true}, {"a/+", "a/b/c", false}, {"a/#", "a", true}, {"a/#", "a/b/c", true},
		{"#", "a/b", true}, {"#", "$SYS/a", false}, {"+/a", "$SYS/a", false}, {"$SYS/#", "$SYS/a", true},
		{"+/+", "/x", true}, {"a/b", "a", false}, {"a", "a/b", false}, {"$share/g/a/+", "a/b", true}, {"a/b/c", "a/x/c", false},
	} {
		if MatchTopic(tc.f, tc.t) != tc.ok {
			t.Errorf("%q %q", tc.f, tc.t)
		}
	}
}

// TestProps reads every property MQTT 5 has, the ones kept and the ones
// let go.
func TestProps(t *testing.T) {
	var w propsWriter
	w.byte(propPayloadFormat, 1)
	w.u32(propMessageExpiry, 5)
	w.str(propContentType, "c")
	w.str(propResponseTopic, "r")
	w.bin(propCorrelationData, []byte{9})
	w.b = appendVarint(append(w.b, propSubscriptionID), 268435455)
	w.u32(propSessionExpiry, 6)
	w.str(propAssignedClientID, "id")
	w.u16(propServerKeepAlive, 7)
	w.str(propAuthMethod, "m")
	w.bin(propAuthData, []byte{1})
	w.byte(propRequestProblemInfo, 1)
	w.u32(propWillDelay, 1)
	w.byte(propRequestRespInfo, 1)
	w.str(propResponseInfo, "i")
	w.str(propServerReference, "s")
	w.str(propReasonString, "why")
	w.u16(propReceiveMaximum, 8)
	w.u16(propTopicAliasMaximum, 9)
	w.u16(propTopicAlias, 10)
	w.byte(propMaximumQoS, 1)
	w.byte(propRetainAvailable, 0)
	w.user([]UserProperty{{"k", "v"}})
	w.u32(propMaximumPacketSize, 11)
	w.byte(propWildcardAvailable, 1)
	w.byte(propSubIDAvailable, 1)
	w.byte(propSharedAvailable, 1)
	r := &reader{b: w.appendTo(nil)}
	p := r.props()
	if r.err != nil || r.left() != 0 {
		t.Fatal(r.err)
	}
	want := props{payloadUTF8: true, expiry: 5, hasExpiry: true, contentType: "c", responseTopic: "r", correlation: []byte{9},
		subIDs: []int{268435455}, sessionExpiry: 6, assignedID: "id", serverKeepAlive: 7, hasKeepAlive: true, reason: "why",
		receiveMax: 8, aliasMax: 9, alias: 10, maxQoS: 1, hasMaxQoS: true, hasRetain: true, subIDsAvailable: 1, hasSubIDs: true, maxPacket: 11,
		user: []UserProperty{{"k", "v"}}, authMethod: "m", authData: []byte{1}, willDelay: 1}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("%+v", p)
	}
	// Lengths that overrun.
	for _, b := range [][]byte{{5, 1}, {0x80, 0x80, 0x80, 0x80, 1}, {2, 0x01}, {}} {
		r := &reader{b: b}
		r.props()
		if r.err == nil {
			t.Errorf("%x read", b)
		}
	}
	r = &reader{b: []byte{0, 3, 'a'}}
	if r.binary() != nil || r.err == nil {
		t.Fatal("read")
	}
	r = &reader{b: []byte{1}}
	if r.u32() != 0 || r.u16() != 0 || r.err == nil {
		t.Fatal("read")
	}
	if packetName(typeAuth<<4) != "AUTH" {
		t.Fatal(packetName(typeAuth << 4))
	}
}

// TestReadPacket: truncated packets.
func TestReadPacket(t *testing.T) {
	for _, b := range [][]byte{{}, {0x30}, {0x30, 0x80}, {0x30, 2, 0}} {
		s := newRawServer(t)
		c, nc, _ := rawClient(t, s, Config{ClientID: "a"}, connackOK(Version5))
		nc.Write(b)
		nc.Close()
		<-c.Done()
		if !errors.Is(c.Err(), io.ErrUnexpectedEOF) {
			t.Fatalf("%x: %v", b, c.Err())
		}
	}
}

// failConn fails every write after its first.
type failConn struct {
	net.Conn
	writes int
}

func (c *failConn) Write(p []byte) (int, error) {
	if c.writes++; c.writes > 1 {
		return 0, errors.New("broken pipe")
	}
	return c.Conn.Write(p)
}

// TestLostWhileWaiting: a write that fails, a subscribe waiting for its
// SUBACK and a message waiting for room end with the connection.
func TestLostWhileWaiting(t *testing.T) {
	b := newFakeBroker(t, nil)
	var d net.Dialer
	fc := Config{Address: b.addr(), ClientID: "a", Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		nc, err := d.DialContext(ctx, network, addr)
		return &failConn{Conn: nc}, err
	}}
	c := dial(t, fc)
	b.waitConn(t)
	if _, err := c.Subscribe(context.Background(), Subscription{Filter: "t"}); err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatal(err)
	}
	b.set(func(b *fakeBroker) { b.noSuback = true })
	c = dial(t, Config{Address: b.addr(), ClientID: "b"})
	conn := b.waitConn(t)
	go func() { time.Sleep(20 * time.Millisecond); conn.nc.Close() }()
	if _, err := c.Subscribe(context.Background(), Subscription{Filter: "t"}); err == nil {
		t.Fatal("subscribed")
	}
	s := newRawServer(t)
	c, nc, _ := rawClient(t, s, Config{ClientID: "a", ReceiveMaximum: 1}, connackOK(Version5))
	for range 3 {
		nc.Write(packet(typePublish<<4, append(appendString(nil, "t"), 0)))
	}
	time.Sleep(20 * time.Millisecond)
	c.Close()
	<-c.Done()
}

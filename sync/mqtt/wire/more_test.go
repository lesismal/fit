package mqttwire

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestUnsubscribe: a filter unsubscribed stops delivering, the others go
// on; version 5 says which filters had no subscription, and which the
// server refused.
func TestUnsubscribe(t *testing.T) {
	ctx := context.Background()
	for _, v := range versions {
		t.Run(vname(v), func(t *testing.T) {
			b := newFakeBroker(t, nil)
			sub := dial(t, Config{Address: b.addr(), Version: v, ClientID: "sub"})
			if _, err := sub.Subscribe(ctx, Subscription{Filter: "a/+", QoS: 1}, Subscription{Filter: "b/#", QoS: 1}); err != nil {
				t.Fatal(err)
			}
			codes, err := sub.Unsubscribe(ctx, "b/#", "c")
			want := []byte{0, 0x11}
			if v == Version311 {
				want = []byte{0, 0}
			}
			if err != nil || !bytes.Equal(codes, want) {
				t.Fatalf("%v %v", codes, err)
			}
			pub := dial(t, Config{Address: b.addr(), Version: v, CleanStart: true})
			for _, topic := range []string{"b/x", "a/1"} {
				if err := pub.Publish(ctx, &Message{Topic: topic, QoS: 1}); err != nil {
					t.Fatal(err)
				}
			}
			if m := next(t, sub); m.Topic != "a/1" {
				t.Fatal(m.Topic)
			}
			b.set(func(b *fakeBroker) { b.denyUnsub["a/+"] = true })
			_, err = sub.Unsubscribe(ctx, "a/+")
			var re *ReasonError
			if v == Version5 && (!errors.As(err, &re) || re.Packet != "UNSUBACK" || re.Code != 0x87) {
				t.Fatal(err)
			}
		})
	}
}

// TestUnsubscribeErrors: what cannot be unsubscribed, answers of the
// wrong kind or count, and an answer that does not come.
func TestUnsubscribeErrors(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t, nil)
	c := dial(t, Config{Address: b.addr(), ClientID: "a"})
	if _, err := c.Unsubscribe(ctx); err == nil {
		t.Fatal("no filters taken")
	}
	if _, err := c.Unsubscribe(ctx, "a/#/b"); err == nil {
		t.Fatal("a bad filter taken")
	}
	b.set(func(b *fakeBroker) { b.unsubCodes = []byte{0, 0} })
	if _, err := c.Unsubscribe(ctx, "a"); err == nil || !strings.Contains(err.Error(), "2 codes for 1") {
		t.Fatal(err)
	}
	for _, v := range versions {
		b.set(func(b *fakeBroker) { b.unsubCodes, b.unsubAs = []byte{0}, typeSuback })
		c := dial(t, Config{Address: b.addr(), Version: v, ClientID: "w"})
		if _, err := c.Unsubscribe(ctx, "a"); err == nil || !strings.Contains(err.Error(), "answered with SUBACK") {
			t.Fatal(err)
		}
	}
	b.set(func(b *fakeBroker) { b.unsubAs, b.unsubCodes, b.noUnsuback = 0, nil, true })
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := c.Unsubscribe(tctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	b.set(func(b *fakeBroker) { b.unsubCodes, b.noUnsuback = []byte{}, false })
	if _, err := c.Unsubscribe(ctx, "a"); err == nil {
		t.Fatal("an UNSUBACK of no codes taken")
	}
	<-c.Done()
	if !errors.Is(c.Err(), errMalformed) {
		t.Fatal(c.Err())
	}
	if _, err := c.Unsubscribe(ctx, "a"); err == nil {
		t.Fatal("unsubscribed on a closed connection")
	}
}

// TestWill: a connection lost publishes its will, with its QoS, retain
// flag and version 5's properties and delay; Close does not.
func TestWill(t *testing.T) {
	ctx := context.Background()
	for _, v := range versions {
		t.Run(vname(v), func(t *testing.T) {
			b := newFakeBroker(t, nil)
			sub := dial(t, Config{Address: b.addr(), Version: v, ClientID: "sub"})
			if _, err := sub.Subscribe(ctx, Subscription{Filter: "will/#", QoS: 2}); err != nil {
				t.Fatal(err)
			}
			will := &Will{Message: Message{Topic: "will/a", Payload: []byte("gone"), QoS: 1, Retain: true, PayloadUTF8: true,
				MessageExpiry: time.Minute, ContentType: "text/plain", ResponseTopic: "r", CorrelationData: []byte{1},
				UserProperties: []UserProperty{{"k", "v"}}}, Delay: 1500 * time.Millisecond}
			b.waitConn(t)
			dial(t, Config{Address: b.addr(), Version: v, ClientID: "a", Will: will})
			conn := b.waitConn(t)
			conn.nc.Close()
			m := next(t, sub)
			if m.Topic != "will/a" || string(m.Payload) != "gone" || m.QoS != 1 || !m.Retain {
				t.Fatalf("%+v", m)
			}
			if v == Version5 && (m.ContentType != "text/plain" || !m.PayloadUTF8 || m.ResponseTopic != "r" || !bytes.Equal(m.CorrelationData, []byte{1}) ||
				!reflect.DeepEqual(m.UserProperties, will.UserProperties) || m.MessageExpiry != time.Minute) {
				t.Fatalf("%+v", m)
			}
			if got := conn.will.Delay; v == Version5 && got != 2*time.Second || v == Version311 && got != 0 {
				t.Fatal(got)
			}
			sub.Ack(m)
			c2 := dial(t, Config{Address: b.addr(), Version: v, ClientID: "b", Will: &Will{Message: Message{Topic: "will/b"}}})
			c2.Close()
			none(t, sub)
		})
	}
}

// TestWillErrors: a will CONNECT cannot carry fails before connecting.
func TestWillErrors(t *testing.T) {
	for _, w := range []*Will{{}, {Message: Message{Topic: "a/+"}}, {Message: Message{Topic: "a", QoS: 3}},
		{Message: Message{Topic: "a", Payload: make([]byte, 70000)}}} {
		_, err := Dial(context.Background(), Config{Address: "127.0.0.1:1", ClientID: "a", Will: w})
		if err == nil || strings.Contains(err.Error(), "connect to") {
			t.Fatalf("%+v: %v", w, err)
		}
	}
}

// TestSCRAMVectors checks the exchanges of RFC 5802 (SHA-1) and RFC 7677
// (SHA-256).
func TestSCRAMVectors(t *testing.T) {
	for _, tc := range []struct {
		method, nonce, first, final, server string
	}{
		{SCRAMSHA1, "fyko+d2lbbFgONRv9qkxdawL", "r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j,s=QSXCR+Q6sek8bf92,i=4096",
			"c=biws,r=fyko+d2lbbFgONRv9qkxdawL3rfcNHYJY1ZVvWVs7j,p=v0X8v3Bz2T0CJGbJQyF0X+HI4Ts=", "v=rmF9pqV8S7suAoZWja4dJRkFsKQ="},
		{SCRAMSHA256, "rOprNGfwEbeRWgbNEkqO", "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096",
			"c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ=",
			"v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="},
	} {
		a, err := NewSCRAM(tc.method, "user", "pencil")
		if err != nil || a.Method() != tc.method {
			t.Fatal(err)
		}
		a.(*scram).nonce = func() string { return tc.nonce }
		x, first, err := a.Start()
		if err != nil || string(first) != "n,,n=user,r="+tc.nonce {
			t.Fatalf("%s %v", first, err)
		}
		final, err := x.Next([]byte(tc.first))
		if err != nil || string(final) != tc.final {
			t.Fatalf("%s: %s %v", tc.method, final, err)
		}
		if err := x.Done([]byte(tc.server)); err != nil {
			t.Fatal(err)
		}
		if err := x.Done([]byte("v=AAAA")); err == nil {
			t.Fatal("a wrong signature taken")
		}
	}
}

// TestSCRAMErrors: the methods and users NewSCRAM refuses, and server
// messages an exchange refuses.
func TestSCRAMErrors(t *testing.T) {
	if _, err := NewSCRAM("SCRAM-MD5", "u", "p"); err == nil {
		t.Fatal("an unknown method taken")
	}
	if _, err := NewSCRAM(SCRAMSHA512, "", "p"); err == nil {
		t.Fatal("no user taken")
	}
	a, _ := NewSCRAM(SCRAMSHA512, "a=b,c", "p")
	a.(*scram).nonce = func() string { return "abc" }
	x, first, _ := a.Start()
	if string(first) != "n,,n=a=3Db=2Cc,r=abc" {
		t.Fatal(string(first))
	}
	if err := x.Done([]byte("v=")); err == nil {
		t.Fatal("done before the proof")
	}
	for _, msg := range []string{"", "r", "e=other-error", "r=abcd,s=c2FsdA==,i=1,m=ext", "r=xyz1,s=c2FsdA==,i=1",
		"r=abc,s=c2FsdA==,i=1", "r=abcd,s=!,i=1", "r=abcd,i=1", "r=abcd,s=c2FsdA==,i=x", "r=abcd,s=c2FsdA==,i=0",
		"r=abcd,s=c2FsdA==,i=99999999"} {
		if _, err := x.Next([]byte(msg)); !errors.Is(err, errSCRAM) {
			t.Fatalf("%q: %v", msg, err)
		}
	}
	if _, err := x.Next([]byte("r=abcd,s=c2FsdA==,i=2,r=ignored")); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Next([]byte("r=abcd,s=c2FsdA==,i=2")); err == nil {
		t.Fatal("a second server-first taken")
	}
	for _, msg := range []string{"e=invalid-proof", "x=1", "v=!!"} {
		if err := x.Done([]byte(msg)); !errors.Is(err, errSCRAM) {
			t.Fatalf("%q: %v", msg, err)
		}
	}
}

// TestEnhancedAuth authenticates by SCRAM with the fake broker, and
// re-authenticates; a wrong password, a server that does not know it, and
// a method it does not take are refused.
func TestEnhancedAuth(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.scram = map[string]string{"u": "pw"} })
	for _, m := range []string{SCRAMSHA1, SCRAMSHA256, SCRAMSHA512} {
		a, _ := NewSCRAM(m, "u", "pw")
		c := dial(t, Config{Address: b.addr(), ClientID: "a", Username: "u", Auth: a})
		if err := c.Reauthenticate(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Subscribe(ctx, Subscription{Filter: "t"}); err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	wrong, _ := NewSCRAM(SCRAMSHA256, "u", "no")
	_, err := Dial(ctx, Config{Address: b.addr(), ClientID: "a", Auth: wrong})
	var ce *ConnectError
	if !errors.As(err, &ce) || ce.Code != 0x87 || !ce.Auth() {
		t.Fatal(err)
	}
	right, _ := NewSCRAM(SCRAMSHA256, "u", "pw")
	b.set(func(b *fakeBroker) { b.badSig = true })
	if _, err := Dial(ctx, Config{Address: b.addr(), ClientID: "a", Auth: right}); !errors.Is(err, errSCRAM) ||
		!strings.Contains(err.Error(), "authenticate with") {
		t.Fatal(err)
	}
	b.set(func(b *fakeBroker) { b.badSig, b.noFinal = false, true })
	if _, err := Dial(ctx, Config{Address: b.addr(), ClientID: "a", Auth: right}); !errors.Is(err, errSCRAM) {
		t.Fatal(err)
	}
	b.set(func(b *fakeBroker) { b.noFinal, b.scram = false, nil })
	if _, err := Dial(ctx, Config{Address: b.addr(), ClientID: "a", Auth: right}); !errors.As(err, &ce) || ce.Code != 0x8C {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, Config{Address: b.addr(), ClientID: "a", Version: Version311, Auth: right}); err == nil ||
		strings.Contains(err.Error(), "connect to") {
		t.Fatal(err)
	}
	if _, err := Dial(ctx, Config{Address: b.addr(), ClientID: "a", Auth: failAuth{}}); err == nil || err.Error() != "no start" {
		t.Fatal(err)
	}
}

// failAuth is an Authenticator that cannot start.
type failAuth struct{}

func (failAuth) Method() string                       { return "FAIL" }
func (failAuth) Start() (AuthExchange, []byte, error) { return nil, nil, errors.New("no start") }

// TestReauthErrors: re-authentication without a method, two at once, one
// that does not finish, a server's refusal, and one the server's checks
// fail.
func TestReauthErrors(t *testing.T) {
	ctx := context.Background()
	b := newFakeBroker(t, nil)
	b.set(func(b *fakeBroker) { b.scram = map[string]string{"u": "pw"} })
	plain := dial(t, Config{Address: b.addr(), ClientID: "p"})
	if err := plain.Reauthenticate(ctx); err == nil {
		t.Fatal("re-authenticated with no method")
	}
	a, _ := NewSCRAM(SCRAMSHA256, "u", "pw")
	c := dial(t, Config{Address: b.addr(), ClientID: "a", Auth: a})
	b.set(func(b *fakeBroker) { b.holdReauth = true })
	tctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if err := c.Reauthenticate(tctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := c.Reauthenticate(ctx); err == nil || !strings.Contains(err.Error(), "under way") {
		t.Fatal(err)
	}
	c.Close()
	if err := c.Reauthenticate(ctx); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	// The password changed: the server's DISCONNECT ends the connection.
	b.set(func(b *fakeBroker) { b.holdReauth = false })
	c = dial(t, Config{Address: b.addr(), ClientID: "a", Auth: a})
	b.set(func(b *fakeBroker) { b.scram = map[string]string{"u": "new"} })
	var re *ReasonError
	if err := c.Reauthenticate(ctx); !errors.As(err, &re) || re.Code != 0x87 {
		t.Fatal(err)
	}
	b.set(func(b *fakeBroker) { b.scram = map[string]string{"u": "pw"} })
	c = dial(t, Config{Address: b.addr(), ClientID: "a", Auth: a})
	b.set(func(b *fakeBroker) { b.badSig = true })
	if err := c.Reauthenticate(ctx); !errors.Is(err, errSCRAM) {
		t.Fatal(err)
	}
	b.set(func(b *fakeBroker) { b.badSig = false })
	c = dial(t, Config{Address: b.addr(), ClientID: "a", Auth: onceAuth{a, new(int)}})
	if err := c.Reauthenticate(ctx); err == nil || err.Error() != "no start" {
		t.Fatal(err)
	}
	// A write that fails.
	var d net.Dialer
	fc := Config{Address: b.addr(), ClientID: "f", Auth: a, Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
		nc, err := d.DialContext(ctx, network, addr)
		return &failConn{Conn: nc, writes: -1}, err
	}}
	c = dial(t, fc)
	if err := c.Reauthenticate(ctx); err == nil || !strings.Contains(err.Error(), "broken pipe") {
		t.Fatal(err)
	}
}

// onceAuth is an Authenticator whose first Start works, and the next fail.
type onceAuth struct {
	Authenticator
	n *int
}

func (a onceAuth) Start() (AuthExchange, []byte, error) {
	if *a.n++; *a.n > 1 {
		return nil, nil, errors.New("no start")
	}
	return a.Authenticator.Start()
}

// rawAuth is the Authenticator of the raw server's tests: its data is
// "1", "2", and it takes "ok" as the end.
type rawAuth struct{}

func (rawAuth) Method() string                       { return "RAW" }
func (rawAuth) Start() (AuthExchange, []byte, error) { return rawExchange{}, nil, nil }

type rawExchange struct{}

func (rawExchange) Next(data []byte) ([]byte, error) {
	if string(data) == "bad" {
		return nil, errors.New("bad step")
	}
	return []byte("2"), nil
}

func (rawExchange) Done(data []byte) error {
	if string(data) != "ok" {
		return errors.New("not ok")
	}
	return nil
}

func rawAuthPacket(code byte, method string, data string) []byte {
	return authPacket(code, method, []byte(data))
}

// TestAuthFlow drives the AUTH exchanges from a raw server: CONNECT's,
// with an AUTH of the wrong code, method, or shape, and a
// re-authentication's.
func TestAuthFlow(t *testing.T) {
	ok := func(data string) []byte {
		var w propsWriter
		w.str(propAuthMethod, "RAW")
		w.bin(propAuthData, []byte(data))
		return packet(typeConnack<<4, w.appendTo([]byte{0, 0}))
	}
	for _, tc := range []struct {
		answer []byte
		want   string
	}{
		{rawAuthPacket(0x00, "RAW", ""), "not continue"},
		{rawAuthPacket(0x18, "OTHER", ""), "not continue"},
		{rawAuthPacket(0x18, "RAW", "bad"), "bad step"},
		{packet(typeAuth<<4, []byte{0x18, 9}), "malformed"},
		{packet(typeAuth<<4, nil), "not continue"},
		{ok("no"), "not ok"},
	} {
		s := newRawServer(t)
		errc := make(chan error, 1)
		go func() {
			_, err := Dial(context.Background(), Config{Address: s.addr(), ClientID: "a", Auth: rawAuth{}})
			errc <- err
		}()
		nc, _ := s.accept(t, tc.answer)
		if err := <-errc; err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%x: %v", tc.answer, err)
		}
		nc.Close()
	}
	// A whole exchange, then a re-authentication of two steps.
	s := newRawServer(t)
	type res struct {
		c   *Client
		err error
	}
	ch := make(chan res, 1)
	go func() {
		c, err := Dial(context.Background(), Config{Address: s.addr(), ClientID: "a", Auth: rawAuth{}})
		ch <- res{c, err}
	}()
	nc, br := s.accept(t, rawAuthPacket(0x18, "RAW", "1"))
	if first, body := readNext(t, nc, br); first != typeAuth<<4 {
		t.Fatal(first)
	} else if code, p, _ := authOf(body); code != 0x18 || string(p.authData) != "2" || p.authMethod != "RAW" {
		t.Fatal(code, p)
	}
	nc.Write(ok("ok"))
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	c := r.c
	defer c.Close()
	errc := make(chan error, 1)
	go func() { errc <- c.Reauthenticate(context.Background()) }()
	if first, body := readNext(t, nc, br); first != typeAuth<<4 || body[0] != 0x19 {
		t.Fatal(first, body)
	}
	nc.Write(rawAuthPacket(0x18, "RAW", "1"))
	if first, _ := readNext(t, nc, br); first != typeAuth<<4 {
		t.Fatal(first)
	}
	nc.Write(rawAuthPacket(0x00, "RAW", "ok"))
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	// AUTH no re-authentication asked for, of a code a server does not
	// send, malformed, a step that fails: each ends the connection.
	for _, tc := range []struct {
		reauth bool
		answer []byte
		code   byte
	}{
		{false, rawAuthPacket(0x18, "RAW", ""), 0x82},
		{true, rawAuthPacket(0x19, "RAW", ""), 0x82},
		{true, packet(typeAuth<<4, []byte{0x18, 9}), 0x81},
		{true, rawAuthPacket(0x18, "RAW", "bad"), 0x80},
	} {
		s := newRawServer(t)
		go func() {
			c, err := Dial(context.Background(), Config{Address: s.addr(), ClientID: "a", Auth: rawAuth{}})
			ch <- res{c, err}
		}()
		nc, br := s.accept(t, nil)
		nc.Write(rawAuthPacket(0x18, "RAW", "1"))
		readNext(t, nc, br)
		nc.Write(ok("ok"))
		r := <-ch
		if r.err != nil {
			t.Fatal(r.err)
		}
		if tc.reauth {
			go func() { errc <- r.c.Reauthenticate(context.Background()) }()
			readNext(t, nc, br)
		}
		nc.Write(tc.answer)
		if first, body := readNext(t, nc, br); first != typeDisconnect<<4 || body[0] != tc.code {
			t.Fatalf("%x: %x %x", tc.answer, first, body)
		}
		if tc.reauth {
			if err := <-errc; err == nil {
				t.Fatal("re-authenticated")
			}
		}
		r.c.Close()
	}
	// A 3.1.1 server that sends AUTH.
	s = newRawServer(t)
	c311, nc311, _ := rawClient(t, s, Config{ClientID: "a", Version: Version311}, connackOK(Version311))
	nc311.Write(rawAuthPacket(0x18, "RAW", ""))
	<-c311.Done()
	if !strings.Contains(c311.Err().Error(), "AUTH") {
		t.Fatal(c311.Err())
	}
}

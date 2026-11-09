package natswire

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/sync/nats/natstest"
)

func server(t *testing.T, o natstest.Options) *natstest.Server {
	t.Helper()
	s, err := natstest.Start(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
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

func tctx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func eventually(t *testing.T, f func() bool) {
	t.Helper()
	for range 500 {
		if f() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("not in time")
}

func TestNKeys(t *testing.T) {
	seed := make([]byte, 32)
	seed[0] = 7
	kp, err := ParseSeed(SeedOf(seed))
	if err != nil {
		t.Fatal(err)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if kp.Public() != natstest.NKeyPublic(priv.Public().(ed25519.PublicKey)) || !strings.HasPrefix(kp.Public(), "U") {
		t.Fatal(kp.Public())
	}
	sig, _ := base64.RawURLEncoding.DecodeString(kp.Sign([]byte("n")))
	if !ed25519.Verify(priv.Public().(ed25519.PublicKey), []byte("n"), sig) {
		t.Fatal("signature")
	}
	if !strings.HasPrefix(SeedOf(seed), "SU") {
		t.Fatal(SeedOf(seed))
	}
	bad := []byte(SeedOf(seed))
	if bad[10] == 'B' {
		bad[10] = 'C'
	} else {
		bad[10] = 'B'
	}
	raw := []byte{prefixUser, 0}
	raw = append(raw, seed...)
	raw = binary.LittleEndian.AppendUint16(raw, crc16(raw))
	for _, s := range []string{"!!", "AAAA", string(bad), b32.EncodeToString(raw)} {
		if _, err := ParseSeed(s); err == nil {
			t.Errorf("%q parsed", s)
		}
	}
	creds := "-----BEGIN NATS USER JWT-----\neyJ.x.y\n------END NATS USER JWT------\n\n-----BEGIN USER NKEY SEED-----\n" + SeedOf(seed) + "\n------END USER NKEY SEED------\n"
	jwt, s, err := ParseCreds([]byte(creds))
	if err != nil || jwt != "eyJ.x.y" || s != SeedOf(seed) {
		t.Fatal(jwt, s, err)
	}
	for _, c := range []string{"", "-----BEGIN NATS USER JWT-----\nj\n", "-----BEGIN NATS USER JWT-----\n------END\n"} {
		if _, _, err := ParseCreds([]byte(c)); err == nil {
			t.Errorf("%q parsed", c)
		}
	}
}

func jwtOf(kp *KeyPair) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"ed25519-nkey"}`)) + "." + enc([]byte(`{"sub":"`+kp.Public()+`"}`)) + ".sig"
}

func TestAuth(t *testing.T) {
	seed := make([]byte, 32)
	kp, _ := ParseSeed(SeedOf(seed))
	other, _ := ParseSeed(SeedOf(append([]byte{1}, seed[1:]...)))
	cases := []struct {
		o       natstest.Options
		ok, bad Config
	}{
		{natstest.Options{Token: "t"}, Config{Token: "t"}, Config{Token: "u"}},
		{natstest.Options{User: "u", Pass: "p"}, Config{User: "u", Password: "p", Name: "me"}, Config{User: "u", Password: "x"}},
		{natstest.Options{NKey: kp.Public()}, Config{NKey: kp}, Config{NKey: other}},
		{natstest.Options{JWT: true}, Config{NKey: kp, JWT: jwtOf(kp)}, Config{NKey: other, JWT: jwtOf(kp)}},
	}
	for i, c := range cases {
		s := server(t, c.o)
		c.ok.Servers, c.bad.Servers = []string{s.Addr()}, []string{s.Addr()}
		cl := dial(t, c.ok)
		if !cl.Connected() || cl.Info().ServerID != "FAKE" || cl.Info().MaxPayload != 1<<20 {
			t.Fatalf("%d: %+v", i, cl.Info())
		}
		if _, err := Dial(tctx(t), c.bad); err == nil || !strings.Contains(err.Error(), "Authorization") {
			t.Errorf("%d: %v", i, err)
		}
	}
}

func TestTLS(t *testing.T) {
	stls, ca := natstest.TLS()
	s := server(t, natstest.Options{TLS: stls})
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	c := dial(t, Config{Servers: []string{s.Addr()}, TLS: &tls.Config{RootCAs: roots}})
	if err := c.Flush(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := Dial(tctx(t), Config{Servers: []string{s.Addr()}}); err == nil || !strings.Contains(err.Error(), "requires TLS") {
		t.Fatal(err)
	}
	if _, err := Dial(tctx(t), Config{Servers: []string{s.Addr()}, TLS: &tls.Config{}}); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatal(err)
	}
}

func TestPubSub(t *testing.T) {
	s := server(t, natstest.Options{})
	c := dial(t, Config{Servers: []string{s.Addr()}})
	subc, qc := make(chan *Msg, 4), make(chan *Msg, 1)
	sub, err := c.Subscribe("a.*", "", subc)
	if err != nil {
		t.Fatal(err)
	}
	q, _ := c.Subscribe("a.>", "grp", qc)
	if err := c.Publish("a.b", "r.x", Header{"K": {"v1", "v2"}}, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if err := c.Publish("a.c", "", nil, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(tctx(t)); err != nil {
		t.Fatal(err)
	}
	m := <-subc
	if m.Subject != "a.b" || m.Reply != "r.x" || string(m.Data) != "hi" || m.Header.Get("k") != "v1" || len(m.Header["K"]) != 2 || m.Header.Get("none") != "" {
		t.Fatalf("%+v", m)
	}
	if m := <-subc; string(m.Data) != "two" || m.Header != nil {
		t.Fatalf("%+v", m)
	}
	eventually(t, func() bool { return c.Dropped() == 1 }) // q holds one of two
	q.Unsubscribe()
	q.Unsubscribe()
	sub.Unsubscribe()
	for _, bad := range [][2]string{{"", ""}, {"a b", ""}, {"a", "b c"}} {
		if err := c.Publish(bad[0], bad[1], nil, nil); !errors.Is(err, ErrBadSubject) {
			t.Error(err)
		}
		if _, err := c.Subscribe(bad[0], bad[1], nil); !errors.Is(err, ErrBadSubject) {
			t.Error(err)
		}
	}
	if err := c.Publish("a", "", nil, make([]byte, 2<<20)); !errors.Is(err, ErrMaxPayload) {
		t.Fatal(err)
	}
	if _, err := c.Request(tctx(t), "nobody", nil, nil); !errors.Is(err, ErrNoResponders) {
		t.Fatal(err)
	}
	// A responder.
	rc := make(chan *Msg, 1)
	c.Subscribe("svc", "", rc)
	go func() {
		m := <-rc
		c.Publish(m.Reply, "", nil, append([]byte("re:"), m.Data...))
	}()
	if m, err := c.Request(tctx(t), "svc", nil, []byte("q")); err != nil || string(m.Data) != "re:q" {
		t.Fatal(m, err)
	}
	c.Close()
	c.Close()
	if err := c.Publish("a", "", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := c.Subscribe("a", "", nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := c.Flush(tctx(t)); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := c.NewInbox(1); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

func TestErrors(t *testing.T) {
	s := server(t, natstest.Options{})
	var action atomic.Int32
	s.SetHook(func(op, subject string) natstest.Action {
		if op == "PUB" && subject == "x" {
			return natstest.Action(action.Load())
		}
		return natstest.Normal
	})
	c := dial(t, Config{Servers: []string{s.Addr()}, ReconnectWait: 10 * time.Millisecond})
	action.Store(int32(natstest.Deny))
	c.Publish("x", "", nil, nil)
	if err := c.Flush(tctx(t)); err == nil || !strings.Contains(err.Error(), "Permissions") {
		t.Fatal(err)
	}
	if err := c.Flush(tctx(t)); err != nil {
		t.Fatal(err)
	}
	// A request dropped times out.
	action.Store(int32(natstest.Drop))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.Request(ctx, "x", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// A fatal error: the client connects again.
	ib, _ := c.NewInbox(1)
	action.Store(int32(natstest.Fatal))
	c.Publish("x", "", nil, nil)
	if _, err := ib.Next(tctx(t)); !errors.Is(err, ErrDisconnected) {
		t.Fatal(err)
	}
	action.Store(int32(natstest.Normal))
	eventually(t, c.Connected)
	if err := c.Flush(tctx(t)); err != nil {
		t.Fatal(err)
	}
	// A flush waiting when the connection breaks.
	action.Store(int32(natstest.Silence))
	c.Publish("x", "", nil, nil)
	done := make(chan error, 1)
	go func() { done <- c.Flush(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	action.Store(int32(natstest.Normal))
	s.Kick()
	if err := <-done; !errors.Is(err, ErrDisconnected) {
		t.Fatal(err)
	}
	eventually(t, c.Connected)
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := c.Flush(ctx2); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestReconnect(t *testing.T) {
	a := server(t, natstest.Options{})
	b := server(t, natstest.Options{ConnectURLs: []string{"127.0.0.1:1"}})
	c := dial(t, Config{Servers: []string{a.Addr(), b.Addr()}, ReconnectWait: 10 * time.Millisecond, PingInterval: 20 * time.Millisecond})
	subc := make(chan *Msg, 4)
	c.Subscribe("s", "", subc)
	a.Close()
	eventually(t, func() bool { return b.Conns() == 1 })
	if len(c.Servers()) != 3 {
		t.Fatal(c.Servers())
	}
	c.Publish("s", "", nil, []byte("after"))
	if m := <-subc; string(m.Data) != "after" {
		t.Fatal(m)
	}
	// Lame duck mode: to another server (none left but b and the dead).
	gen := func() uint64 {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.gen
	}
	g := gen()
	b.SendInfo(map[string]any{"ldm": true})
	eventually(t, func() bool { return gen() > g })
	// Pings go unanswered: the connection is taken for dead.
	var pings atomic.Int32
	b.SetHook(func(op, _ string) natstest.Action {
		if op == "PING" && pings.Add(1) <= 2 {
			return natstest.Drop
		}
		return natstest.Normal
	})
	g = gen()
	eventually(t, func() bool { return gen() > g })
	b.SetHook(nil)
	b.Close()
	time.Sleep(50 * time.Millisecond)
	if c.Connected() {
		t.Fatal("connected to nothing")
	}
	if err := c.Publish("s", "", nil, nil); !errors.Is(err, ErrDisconnected) {
		t.Fatal(err)
	}
	if _, err := c.Subscribe("later", "", subc); err != nil {
		t.Fatal(err)
	}
	c.Close()
}

func TestDialFails(t *testing.T) {
	if _, err := Dial(tctx(t), Config{}); err == nil {
		t.Fatal("no servers")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	if _, err := Dial(tctx(t), Config{Servers: []string{ln.Addr().String()}, DialTimeout: 50 * time.Millisecond}); err == nil {
		t.Fatal("no INFO")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Dial(ctx, Config{Servers: []string{ln.Addr().String(), "127.0.0.1:1"}}); err == nil {
		t.Fatal("cancelled")
	}
}

// raw serves each connection a script: INFO, then, after CONNECT and
// PING, PONG and the script's lines.
func raw(t *testing.T, info string, script ...string) string {
	t.Helper()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { ln.Close() })
	n := 0
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s := script[min(n, len(script)-1)]
			n++
			go func() {
				defer c.Close()
				c.Write([]byte(info))
				br := bufio.NewReader(c)
				br.ReadString('\n')
				br.ReadString('\n')
				c.Write([]byte(s))
				if !strings.HasSuffix(s, "\n") {
					return
				}
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if l == "PING\r\n" {
						c.Write([]byte("PONG\r\n"))
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestHandshakeFails(t *testing.T) {
	for _, c := range []struct{ info, reply, want string }{
		{"HELLO\r\n", "", "not INFO"},
		{"INFO {\r\n", "", "INFO"},
		{"INFO {}\r\n", "-ERR 'nope'\r\n", "nope"},
		{"INFO {}\r\n", "+OK\r\nINFO {\"max_payload\":5}\r\n", "deadline"},
		{"INFO {}\r\n", "+OK", "EOF"},
		{"INFO {}\r\n" + strings.Repeat("x", MaxLine+10) + "\r\n", "", "longer"},
	} {
		addr := raw(t, c.info, c.reply)
		if _, err := Dial(tctx(t), Config{Servers: []string{addr}, DialTimeout: 200 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v", c.info+c.reply, err)
		}
	}
}

func TestBadLines(t *testing.T) {
	for _, line := range []string{
		"BOGUS\r\n",
		"MSG a\r\n",
		"MSG a x 1\r\nz\r\n",
		"MSG a 2 999999999\r\n",
		"MSG a 2 1\r\nzz\r\n",
		"HMSG a 2 5 4\r\n",
		"HMSG a 2 4 4\r\nXX\r\n\r\n",
		"HMSG a 2 16 16\r\nNATS/1.0 abc\r\n\r\n\r\n",
		"HMSG a 2 21 21\r\nNATS/1.0\r\nbadline\r\n\r\n\r\n",
		"MSG a 2 5\r\nab",
	} {
		addr := raw(t, "INFO {}\r\n", "PONG\r\n"+line)
		c := dial(t, Config{Servers: []string{addr}, ReconnectWait: time.Hour})
		// The line may break the connection before the inbox is made.
		ib, err := c.NewInbox(1)
		if err == nil {
			_, err = ib.Next(tctx(t))
		}
		if !errors.Is(err, ErrDisconnected) {
			t.Errorf("%q: %v", line, err)
		}
		c.mu.Lock()
		err = c.lastErr
		c.mu.Unlock()
		if err == nil {
			t.Errorf("%q: no error", line)
		}
		c.Close()
	}
	// A message to no one, a server PING, +OK and a message with a status.
	addr := raw(t, "INFO {}\r\n", "PONG\r\nMSG a 99 1\r\nx\r\nPING\r\n+OK\r\nHMSG a 1 24 24\r\nNATS/1.0 503 \r\nK:  v\r\n\r\n\r\n")
	c := dial(t, Config{Servers: []string{addr}})
	if err := c.Flush(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var m Msg
	if err := parseHeader([]byte("NATS/1.0 404 No Messages\r\nA: b\r\n\r\n"), &m); err != nil || m.Status != 404 || m.Description != "No Messages" || m.Header.Get("A") != "b" {
		t.Fatal(m, err)
	}
}

func TestJetStream(t *testing.T) {
	s := server(t, natstest.Options{})
	c := dial(t, Config{Servers: []string{s.Addr()}})
	ctx := tctx(t)
	if _, err := c.StreamInfo(ctx, "S"); !IsAPIError(err, ErrCodeStreamNotFound) || !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
	si, err := c.CreateStream(ctx, StreamConfig{Name: "S", Subjects: []string{"s.>"}})
	if err != nil || si.Config.Name != "S" {
		t.Fatal(si, err)
	}
	for i := range 3 {
		p, err := c.PublishJS("s.a", Header{"Nats-Msg-Id": {"id" + string(rune('0'+i))}}, []byte("m"))
		if err != nil {
			t.Fatal(err)
		}
		if a, err := p.Wait(ctx); err != nil || a.Seq != uint64(i+1) || a.Duplicate {
			t.Fatal(a, err)
		}
	}
	p, _ := c.PublishJS("s.a", Header{"Nats-Msg-Id": {"id0"}}, []byte("m"))
	if a, err := p.Wait(ctx); err != nil || !a.Duplicate {
		t.Fatal(a, err)
	}
	p, _ = c.PublishJS("nostream", nil, nil)
	if _, err := p.Wait(ctx); !errors.Is(err, ErrNoResponders) {
		t.Fatal(err)
	}
	p, _ = c.PublishJS("$JS.API.STREAM.INFO.NONE", nil, nil)
	if _, err := p.Wait(ctx); !IsAPIError(err, ErrCodeStreamNotFound) {
		t.Fatal(err)
	}
	if si, err := c.StreamInfo(ctx, "S"); err != nil || si.State.LastSeq != 3 || si.State.Msgs != 3 {
		t.Fatal(si, err)
	}
	if _, err := c.ConsumerInfo(ctx, "S", "C"); !IsAPIError(err, ErrCodeConsumerNotFound) {
		t.Fatal(err)
	}
	ci, err := c.CreateConsumer(ctx, "S", ConsumerConfig{Durable: "C", AckPolicy: "all", DeliverPolicy: "by_start_sequence", OptStartSeq: 2, FilterSubject: "s.>"})
	if err != nil || ci.NumPending != 2 {
		t.Fatal(ci, err)
	}
	msgs, err := c.Fetch(ctx, "S", "C", FetchRequest{Batch: 5, NoWait: true})
	if err != nil || len(msgs) != 2 {
		t.Fatal(msgs, err)
	}
	meta, err := ParseMeta(msgs[1].Reply)
	if err != nil || meta.Stream != "S" || meta.Consumer != "C" || meta.StreamSeq != 3 || meta.ConsumerSeq != 2 || meta.Timestamp.IsZero() {
		t.Fatal(meta, err)
	}
	if err := c.Ack(msgs[1].Reply); err != nil {
		t.Fatal(err)
	}
	c.Flush(ctx)
	if n, floor := s.Pending("S", "C"); n != 0 || floor != 3 {
		t.Fatal(n, floor)
	}
	// Expires with nothing: 408, no messages.
	if msgs, err := c.Fetch(ctx, "S", "C", FetchRequest{Batch: 1, Expires: 20 * time.Millisecond}); err != nil || len(msgs) != 0 {
		t.Fatal(msgs, err)
	}
	// A message arriving while a pull waits.
	go func() {
		time.Sleep(20 * time.Millisecond)
		c.Publish("s.b", "", nil, []byte("late"))
	}()
	if msgs, err := c.Fetch(ctx, "S", "C", FetchRequest{Batch: 1, Expires: time.Second}); err != nil || len(msgs) != 1 || string(msgs[0].Data) != "late" {
		t.Fatal(msgs, err)
	}
	// A consumer deleted under a waiting pull: 409.
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.RemoveConsumer("S", "C")
	}()
	var se *StatusError
	if _, err := c.Fetch(ctx, "S", "C", FetchRequest{Batch: 1, Expires: time.Second}); !errors.As(err, &se) || se.Status != 409 || !strings.Contains(err.Error(), "409") {
		t.Fatal(err)
	}
	c.CreateConsumer(ctx, "S", ConsumerConfig{Durable: "D", AckPolicy: "explicit"})
	if err := c.DeleteConsumer(ctx, "S", "D"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteConsumer(ctx, "S", "D"); !IsAPIError(err, ErrCodeConsumerNotFound) {
		t.Fatal(err)
	}
	// A pull that ctx ends with some messages, or none.
	s.SetHook(func(op, subject string) natstest.Action {
		if strings.HasPrefix(subject, "$JS.API.CONSUMER.MSG") {
			return natstest.Drop
		}
		return natstest.Normal
	})
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := c.Fetch(short, "S", "C", FetchRequest{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	s.SetHook(nil)
	// Bad names, bad answers.
	for _, err := range []error{
		func() error { _, err := c.StreamInfo(ctx, "a.b"); return err }(),
		func() error { _, err := c.CreateStream(ctx, StreamConfig{Name: "*"}); return err }(),
		func() error { _, err := c.ConsumerInfo(ctx, "S", ">"); return err }(),
		func() error { _, err := c.CreateConsumer(ctx, "S", ConsumerConfig{}); return err }(),
		c.DeleteConsumer(ctx, "", "x"),
		func() error { _, err := c.Fetch(ctx, "S", "a b", FetchRequest{}); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "name") {
			t.Error(err)
		}
	}
	if err := decodeAnswer([]byte("nope"), nil); err == nil {
		t.Fatal("not JSON")
	}
	for _, r := range []string{"x", "$JS.ACK.S.C.1.2.3.4", "$JS.ACK.S.C.1.2.3.4.5.6", "$JS.ACK.S.C.1.x.3.4.5"} {
		if _, err := ParseMeta(r); err == nil {
			t.Errorf("%q parsed", r)
		}
	}
	if m, err := ParseMeta("$JS.ACK.dom.hash.S.C.1.2.3.4.5.tok"); err != nil || m.Stream != "S" || m.Pending != 5 {
		t.Fatal(m, err)
	}
	pa, _ := c.PublishJS("s.a", nil, nil)
	pa.Cancel()
	// A pull that ctx ends with some of its batch; one the connection's
	// loss ends.
	c.CreateConsumer(ctx, "S", ConsumerConfig{Durable: "E", AckPolicy: "none", DeliverPolicy: "new"})
	c.CreateConsumer(ctx, "S", ConsumerConfig{Durable: "E", AckPolicy: "none", DeliverPolicy: "new"}) // an update
	c.Publish("s.z", "", nil, []byte("z"))
	c.Flush(ctx)
	part, cancelPart := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancelPart()
	if msgs, err := c.Fetch(part, "S", "E", FetchRequest{Batch: 3, Expires: 5 * time.Second}); err != nil || len(msgs) != 1 {
		t.Fatal(msgs, err)
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		s.Kick()
	}()
	if _, err := c.Fetch(ctx, "S", "E", FetchRequest{Batch: 1, Expires: 5 * time.Second}); !errors.Is(err, ErrDisconnected) {
		t.Fatal(err)
	}
	eventually(t, c.Connected)
	var x struct{}
	if err := c.api(ctx, "STREAM.FOO.S", nil, &x); !IsAPIError(err, 10003) {
		t.Fatal(err)
	}
	if _, err := c.CreateConsumer(ctx, "NONE", ConsumerConfig{Durable: "E"}); !IsAPIError(err, ErrCodeStreamNotFound) {
		t.Fatal(err)
	}
	gone, cancelGone := context.WithCancel(ctx)
	cancelGone()
	if _, err := c.CreateStream(gone, StreamConfig{Name: "T"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := c.Request(ctx, "a b", nil, nil); !errors.Is(err, ErrBadSubject) {
		t.Fatal(err)
	}
	c.Ack("$JS.ACK.S.NONE.1.1.1.1.1")
	c.Ack("$JS.ACK.S")
	c.Flush(ctx)
	if s.Messages("NONE") != nil {
		t.Fatal("a stream of none")
	}
	c.Close()
	if _, err := c.PublishJS("s.a", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := c.Fetch(ctx, "S", "C", FetchRequest{}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if _, err := c.Request(ctx, "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
}

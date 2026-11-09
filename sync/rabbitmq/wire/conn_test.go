package amqpwire

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestConsume(t *testing.T) {
	b := newFake(t, nil)
	b.frameMax = 4096
	cfg := b.cfg()
	cfg.Name = "fit-test"
	cfg.Deliveries = 4
	c := dialT(t, cfg)
	if c.FrameMax() != 4096 || c.Heartbeat() != 0 {
		t.Fatalf("frame max %d, heartbeat %v", c.FrameMax(), c.Heartbeat())
	}
	if b.props["connection_name"] != "fit-test" {
		t.Errorf("client properties %v", b.props)
	}
	if err := c.Qos(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	tag, err := c.Consume(context.Background(), "q", "", Table{"x-stream-offset": int64(5)})
	if err != nil || tag != "amq.ctag-q" {
		t.Fatalf("consume: %q %v", tag, err)
	}
	q, err := c.QueueDeclare(context.Background(), "q", true, true, nil)
	if err != nil || q.Messages != 3 || q.Name != "q" {
		t.Fatalf("declare: %+v %v", q, err)
	}
	fc := b.conn()
	body := bytes.Repeat([]byte("x"), 10000)
	props := Properties{MessageID: "m1", Headers: Table{"h": "v"}, Timestamp: time.Unix(1700000000, 0).UTC(), DeliveryMode: 2}
	fc.deliver(1, deliverMethod(1, false, "k1"), props, body, 4000)
	fc.deliver(1, deliverMethod(2, true, "k2"), Properties{}, nil, 1)
	d1, d2 := <-c.Deliveries(), <-c.Deliveries()
	if d1.DeliveryTag != 1 || d1.RoutingKey != "k1" || d1.Exchange != "ex" || !bytes.Equal(d1.Body, body) ||
		!reflect.DeepEqual(d1.Properties, props) || d1.ConsumerTag != "ctag" {
		t.Errorf("delivery 1: %+v", d1.Properties)
	}
	if d2.DeliveryTag != 2 || !d2.Redelivered || len(d2.Body) != 0 {
		t.Errorf("delivery 2: %+v", d2)
	}
	if err := c.Ack(2, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Nack(3, false, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "acks", func() bool { _, a, _ := b.snapshot(); return len(a) == 2 })
	_, acks, _ := b.snapshot()
	if !reflect.DeepEqual(acks, []fakeAck{{tag: 2, multiple: true}, {tag: 3, nack: true, requeue: true}}) {
		t.Errorf("acks %+v", acks)
	}
	// A queue that is not there closes the channel, and so the connection.
	if _, err := c.QueueDeclare(context.Background(), "none", true, false, nil); !isNotFound(err) {
		t.Fatalf("declare none: %v", err)
	}
	<-c.Done()
	if _, ok := <-c.Deliveries(); ok {
		t.Error("deliveries open")
	}
	if err := c.Qos(context.Background(), 1); err == nil {
		t.Error("a call on a closed connection")
	}
	c.Close()
}

func isNotFound(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == CodeNotFound && !e.Connection && e.Method == queueDeclare && contains(e, "channel")
}

func TestPublish(t *testing.T) {
	b := newFake(t, nil)
	b.frameMax = 4096
	c := dialT(t, b.cfg())
	ctx := context.Background()
	// Without confirms, no sequence numbers.
	if seq, err := c.Publish(ctx, "", "a", false, Properties{}, []byte("x")); err != nil || seq != 0 {
		t.Fatalf("publish: %d %v", seq, err)
	}
	if err := c.ConfirmSelect(ctx); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("y"), 9000)
	var seqs []uint64
	for _, k := range []string{"a", "nack", "unroutable", "b"} {
		seq, err := c.Publish(ctx, "ex", k, true, Properties{MessageID: k, DeliveryMode: 2}, big)
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	if !reflect.DeepEqual(seqs, []uint64{1, 2, 3, 4}) {
		t.Fatalf("seqs %v", seqs)
	}
	nacked, err := c.WaitConfirms(ctx, 1, 4)
	if err != nil || !reflect.DeepEqual(nacked, []uint64{2}) {
		t.Fatalf("confirms: %v %v", nacked, err)
	}
	rets := c.TakeReturns()
	if len(rets) != 1 || rets[0].Code != 312 || rets[0].Properties.MessageID != "unroutable" || len(rets[0].Body) != 9000 {
		t.Fatalf("returns %+v", rets)
	}
	if c.TakeReturns() != nil {
		t.Error("returns taken twice")
	}
	pub, _, _ := b.snapshot()
	if len(pub) != 5 || !bytes.Equal(pub[1].body, big) || pub[1].exchange != "ex" || !pub[1].mandatory || pub[1].props.DeliveryMode != 2 {
		t.Fatalf("published %d", len(pub))
	}
	// A multiple ack confirms those before it.
	s1, _ := c.Publish(ctx, "", "hold", false, Properties{}, nil)
	s2, _ := c.Publish(ctx, "", "hold", false, Properties{}, nil)
	go b.conn().send(1, method(basicAck).u64(s2).bits(true))
	if n, err := c.WaitConfirms(ctx, s1, s2); err != nil || n != nil {
		t.Fatalf("multiple: %v %v", n, err)
	}
	// Too large, and names too long.
	if _, err := c.Publish(ctx, "", "k", false, Properties{}, make([]byte, DefaultMaxBody+1)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, err := c.Publish(ctx, string(make([]byte, 256)), "k", false, Properties{}, nil); err == nil {
		t.Error("a long exchange")
	}
	// A confirm that never comes times out; a cancelled wait ends.
	s3, _ := c.Publish(ctx, "", "hold", false, Properties{}, nil)
	short, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.WaitConfirms(short, s3, s3); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	c.timeout.Store(int64(50 * time.Millisecond))
	if _, err := c.WaitConfirms(ctx, s3, s3); !contains(err, "confirmed none") {
		t.Errorf("timeout: %v", err)
	}
	// The connection lost while waiting.
	go func() { time.Sleep(20 * time.Millisecond); b.conn().nc.Close() }()
	c.timeout.Store(int64(2 * time.Second))
	if _, err := c.WaitConfirms(ctx, s3, s3); err == nil {
		t.Error("lost connection")
	}
	if _, err := c.WaitConfirms(ctx, s3, s3); err == nil {
		t.Error("failed connection")
	}
	if _, err := c.Publish(ctx, "", "k", false, Properties{}, nil); err == nil {
		t.Error("publish on a failed connection")
	}
}

func TestBlocked(t *testing.T) {
	b := newFake(t, nil)
	c := dialT(t, b.cfg())
	ctx := context.Background()
	fc := b.conn()
	fc.send(0, method(connectionBlocked).short("low on memory"))
	eventually(t, "blocked", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.blocked != "" })
	done := make(chan error, 1)
	go func() { _, err := c.Publish(ctx, "", "k", false, Properties{}, nil); done <- err }()
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("published while blocked: %v", err)
	default:
	}
	fc.send(0, method(connectionUnblocked))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// channel.flow stops it too, and is answered.
	fc.send(1, method(channelFlow).bits(false))
	eventually(t, "stopped", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.stopped })
	c.timeout.Store(int64(50 * time.Millisecond))
	if _, err := c.Publish(ctx, "", "k", false, Properties{}, nil); !errors.Is(err, ErrBlocked) || !contains(err, "channel.flow") {
		t.Errorf("flow: %v", err)
	}
	short, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.Publish(short, "", "k", false, Properties{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	fc.send(1, method(channelFlow).bits(true))
	eventually(t, "flowing", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return !c.stopped })
	c.timeout.Store(int64(2 * time.Second))
	if _, err := c.Publish(ctx, "", "k", false, Properties{}, nil); err != nil {
		t.Fatal(err)
	}
	fc.send(0, method(connectionBlocked).short("disk"))
	eventually(t, "blocked", func() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.blocked != "" })
	go func() { time.Sleep(30 * time.Millisecond); fc.nc.Close() }()
	if _, err := c.Publish(ctx, "", "k", false, Properties{}, nil); err == nil {
		t.Error("published on a lost connection")
	}
}

func TestExternalTLS(t *testing.T) {
	cert, pool := testCert(t)
	b := newFake(t, &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool})
	b.mechs = "PLAIN EXTERNAL"
	cfg := b.cfg()
	cfg.External = true
	cfg.TLS = &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{cert}}
	c := dialT(t, cfg)
	if err := c.Qos(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	// Without the CA, the handshake fails.
	cfg.TLS = &tls.Config{}
	if _, err := Dial(context.Background(), cfg); err == nil {
		t.Error("an unknown CA")
	}
}

func TestDialErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := Dial(ctx, Config{}); err == nil {
		t.Error("no addresses")
	}
	b := newFake(t, nil)
	cfg := b.cfg()
	// A node that refuses is passed over.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	dead.Close()
	cfg.Addrs = []string{dead.Addr().String(), b.addr()}
	dialT(t, cfg)

	cfg = b.cfg()
	cfg.Password = "wrong"
	var ae *Error
	if _, err := Dial(ctx, cfg); !errors.As(err, &ae) || ae.Code != CodeAccessRefused || !ae.Connection || !contains(ae, "connection") {
		t.Errorf("wrong password: %v", err)
	}
	cfg = b.cfg()
	cfg.External = true
	if _, err := Dial(ctx, cfg); !contains(err, "does not take EXTERNAL") {
		t.Errorf("no EXTERNAL: %v", err)
	}
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		fc.send(0, method(connectionStart).u8(0).u8(9).table(nil).long("PLAIN").long("en_US"))
		fc.expect(connectionStartOK)
		fc.raw(appendFrame(nil, frameHeartbeat, 0, nil))
		fc.send(0, method(connectionSecure).long("challenge"))
		return true
	})
	if _, err := Dial(ctx, b.cfg()); !contains(err, "challenge") {
		t.Errorf("secure: %v", err)
	}
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		fc.send(0, method(connectionTune).u16(0).u32(0).u16(0))
		return true
	})
	if _, err := Dial(ctx, b.cfg()); !contains(err, "was expected") {
		t.Errorf("out of order: %v", err)
	}
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		fc.raw(appendFrame(nil, frameBody, 0, []byte("x")))
		return true
	})
	if _, err := Dial(ctx, b.cfg()); !errors.Is(err, errBadFrame) {
		t.Errorf("a body first: %v", err)
	}
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		fc.send(0, method(connectionStart).u8(0).u8(9).u32(10))
		return true
	})
	if _, err := Dial(ctx, b.cfg()); !errors.Is(err, errBadFrame) {
		t.Errorf("a short start: %v", err)
	}
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		fc.raw([]byte("AMQP\x00\x00\x09\x01"))
		return true
	})
	if _, err := Dial(ctx, b.cfg()); err == nil {
		t.Error("a protocol header back")
	}
	// The channel refused.
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		if id == channelOpen {
			fc.send(1, method(channelClose).u16(530).short("NOT_ALLOWED").u32(channelOpen))
			return true
		}
		return false
	})
	if _, err := Dial(ctx, b.cfg()); !errors.As(err, &ae) || ae.Code != 530 {
		t.Errorf("channel refused: %v", err)
	}
	b.setHook(nil)
	// No answer within the timeout, and a context cancelled.
	b.noStart = true
	cfg = b.cfg()
	cfg.Timeout = 100 * time.Millisecond
	if _, err := Dial(ctx, cfg); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("silent broker: %v", err)
	}
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := Dial(cctx, b.cfg()); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	// A dialer of the caller's.
	cfg = b.cfg()
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("no route") }
	if _, err := Dial(ctx, cfg); !contains(err, "no route") {
		t.Errorf("dialer: %v", err)
	}
	// A write that fails while opening.
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) {
		a, z := net.Pipe()
		z.Close()
		return a, nil
	}
	if _, err := Dial(ctx, cfg); err == nil {
		t.Error("a closed pipe")
	}
}

func TestHeartbeats(t *testing.T) {
	b := newFake(t, nil)
	b.heartbeat = 1
	cfg := b.cfg()
	cfg.Heartbeat = 0
	c := dialT(t, cfg)
	if c.Heartbeat() != time.Second || b.tuned[2] != 1 {
		t.Fatalf("heartbeat %v, tuned %v", c.Heartbeat(), b.tuned)
	}
	eventually(t, "a heartbeat sent", func() bool { _, _, n := b.snapshot(); return n > 0 })
	// The broker's heartbeats keep it; silence ends it.
	b.conn().raw(appendFrame(nil, frameHeartbeat, 0, nil))
	select {
	case <-c.Done():
		if !contains(c.Err(), "no heartbeat") {
			t.Errorf("err %v", c.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a silent broker kept")
	}
	// The lower of the two is agreed.
	b.heartbeat = 60
	cfg.Heartbeat = 10 * time.Second
	if c := dialT(t, cfg); c.Heartbeat() != 10*time.Second {
		t.Errorf("heartbeat %v", c.Heartbeat())
	}
	cfg.Heartbeat = 0
	b.heartbeat = 0
	if c := dialT(t, cfg); c.Heartbeat() != 0 {
		t.Errorf("heartbeat %v", c.Heartbeat())
	}
}

// TestBadFrames has the broker send what the protocol does not allow,
// each ending the connection.
func TestBadFrames(t *testing.T) {
	head := deliverMethod(1, false, "k")
	hdr := func(size uint64) []byte { return appendFrame(nil, frameHeader, 1, (&Properties{}).header(size)) }
	cases := map[string][]byte{
		"frame end":       []byte{frameMethod, 0, 1, 0, 0, 0, 1, 0, 0},
		"too large":       {frameMethod, 0, 1, 0, 0x10, 0, 0},
		"unknown type":    appendFrame(nil, 9, 1, nil),
		"short method":    appendFrame(nil, frameMethod, 1, []byte{1}),
		"other channel":   appendFrame(nil, frameMethod, 7, method(basicQosOK)),
		"lone body":       appendFrame(nil, frameBody, 1, []byte("x")),
		"lone header":     hdr(1),
		"body past size":  append(append(appendFrame(nil, frameMethod, 1, head), hdr(1)...), appendFrame(nil, frameBody, 1, []byte("xy"))...),
		"method in body":  append(appendFrame(nil, frameMethod, 1, head), appendFrame(nil, frameMethod, 1, head)...),
		"body over max":   append(appendFrame(nil, frameMethod, 1, head), hdr(DefaultMaxBody+1)...),
		"bad header":      append(appendFrame(nil, frameMethod, 1, head), appendFrame(nil, frameHeader, 1, []byte{0, 61, 0, 0})...),
		"header class":    append(appendFrame(nil, frameMethod, 1, head), appendFrame(nil, frameHeader, 1, enc(nil).u16(50).u16(0).u64(0).u16(0))...),
		"more flags":      append(appendFrame(nil, frameMethod, 1, head), appendFrame(nil, frameHeader, 1, enc(nil).u16(60).u16(0).u64(0).u16(1))...),
		"cancel":          appendFrame(nil, frameMethod, 1, method(basicCancel).short("ctag").bits(true)),
		"channel close":   appendFrame(nil, frameMethod, 1, method(channelClose).u16(406).short("PRECONDITION_FAILED").u32(0)),
		"connection gone": appendFrame(nil, frameMethod, 0, method(connectionClose).u16(320).short("CONNECTION_FORCED").u32(0)),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			b := newFake(t, nil)
			c := dialT(t, b.cfg())
			b.conn().raw(raw)
			select {
			case <-c.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("the connection survived")
			}
			if c.Err() == nil {
				t.Error("no error")
			}
		})
	}
}

func TestCalls(t *testing.T) {
	b := newFake(t, nil)
	c := dialT(t, b.cfg())
	fc := b.conn()
	// A late answer of no call is dropped; an answer of another method
	// fails the connection.
	fc.send(1, method(basicQosOK))
	eventually(t, "a stray answer", func() bool { return len(c.replies) == 1 })
	c.timeout.Store(int64(50 * time.Millisecond))
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool { return id == basicQos })
	if err := c.Qos(context.Background(), 1); !contains(err, "no answer") {
		t.Errorf("silent: %v", err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.timeout.Store(int64(time.Second))
	if err := c.Qos(cctx, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		if id == basicQos {
			fc.send(1, method(confirmSelectOK))
			return true
		}
		return false
	})
	if err := c.Qos(context.Background(), 1); !contains(err, "was expected") {
		t.Errorf("wrong answer: %v", err)
	}
	if err := c.Ack(1, false); err == nil {
		t.Error("ack on a failed connection")
	}
}

func TestCallErrors(t *testing.T) {
	b := newFake(t, nil)
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool {
		switch id {
		case basicConsume:
			fc.send(1, method(basicConsumeOK))
		case queueDeclare:
			fc.send(1, method(queueDeclareOK).short("q"))
		case confirmSelect:
			fc.send(1, method(channelClose).u16(406).short("no").u32(0))
		default:
			return false
		}
		return true
	})
	c := dialT(t, b.cfg())
	ctx := context.Background()
	if _, err := c.Consume(ctx, "q", "t", nil); err == nil {
		t.Error("a short consume-ok")
	}
	if _, err := c.QueueDeclare(ctx, "q", false, true, nil); err == nil {
		t.Error("a short declare-ok")
	}
	if err := c.ConfirmSelect(ctx); err == nil {
		t.Error("confirm refused")
	}
	if _, err := c.Consume(ctx, "q", "t", nil); err == nil {
		t.Error("consume on a closed channel")
	}
	if _, err := c.QueueDeclare(ctx, "q", false, true, nil); err == nil {
		t.Error("declare on a closed channel")
	}
}

func TestClose(t *testing.T) {
	b := newFake(t, nil)
	c := dialT(t, b.cfg())
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(c.Err(), ErrClosed) {
		t.Errorf("err %v", c.Err())
	}
	c.Close()
	// A broker that never answers close: closed after the timeout.
	b.setHook(func(fc *fakeConn, ch uint16, id uint32, d *dec) bool { return id == connectionClose })
	cfg := b.cfg()
	cfg.Timeout = 50 * time.Millisecond
	c = dialT(t, cfg)
	c.Close()
	if !errors.Is(c.Err(), ErrClosed) {
		t.Errorf("err %v", c.Err())
	}
}

// TestDeliveriesHeld has deliveries wait for their reader while the
// connection closes.
func TestDeliveriesHeld(t *testing.T) {
	b := newFake(t, nil)
	cfg := b.cfg()
	cfg.Deliveries = 1
	c := dialT(t, cfg)
	fc := b.conn()
	for i := uint64(1); i <= 3; i++ {
		fc.deliver(1, deliverMethod(i, false, "k"), Properties{}, []byte("x"), 10)
	}
	time.Sleep(50 * time.Millisecond)
	c.Close()
	for range c.Deliveries() {
	}
}

func TestCodec(t *testing.T) {
	tm := time.Unix(1700000000, 0).UTC()
	in := Table{
		"nil": nil, "t": true, "f": false, "b": int8(-1), "B": uint8(2), "s": int16(-3), "u": uint16(4),
		"I": int32(-5), "i": uint32(6), "l": int64(-7), "f32": float32(1.5), "f64": 2.5,
		"D": Decimal{Scale: 2, Value: 314}, "S": "str", "x": []byte{1, 2}, "T": tm,
		"F": Table{"in": "side"}, "A": []any{"a", int64(1), []any{}},
	}
	e := enc(nil).table(in)
	d := &dec{b: e}
	out := d.table(0)
	if d.err != nil || !reflect.DeepEqual(in, out) {
		t.Fatalf("table:\n%v\n%v %v", in, out, d.err)
	}
	// int and anything else.
	d = &dec{b: enc(nil).table(Table{"n": 7, "o": struct{ A int }{1}})}
	if out := d.table(0); out["n"] != int64(7) || out["o"] != "{1}" {
		t.Errorf("others: %v", out)
	}
	// Too deep, unknown types, short data.
	deep := Table{}
	for i := 0; i < maxDepth+2; i++ {
		deep = Table{"d": deep}
	}
	if d := (&dec{b: enc(nil).table(deep)}); d.table(0) == nil || d.err == nil {
		t.Error("too deep")
	}
	arr := []any{}
	for i := 0; i < maxDepth+2; i++ {
		arr = []any{arr}
	}
	if d := (&dec{b: enc(nil).table(Table{"a": arr})}); d.table(0) == nil || d.err == nil {
		t.Error("arrays too deep")
	}
	for name, b := range map[string][]byte{
		"unknown": enc(nil).u32(3).short("k").u8('Z'),
		"short":   enc(nil).u32(10),
		"array":   enc(nil).u32(6).short("k").u8('A').u32(9),
		"length":  {0, 0},
	} {
		d := &dec{b: b}
		if d.table(0); d.err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	d = &dec{}
	if d.u8() != 0 || d.u16() != 0 || d.u32() != 0 || d.u64() != 0 || d.take(-1) != nil {
		t.Error("reads past the end")
	}
	// Properties, every one.
	p := Properties{ContentType: "a", ContentEncoding: "b", Headers: Table{"h": "v"}, DeliveryMode: 2, Priority: 3,
		CorrelationID: "c", ReplyTo: "r", Expiration: "60000", MessageID: "m", Timestamp: tm, Type: "t", UserID: "u", AppID: "app"}
	size, got, err := readHeader(p.header(42))
	if err != nil || size != 42 || !reflect.DeepEqual(got, p) {
		t.Fatalf("properties: %+v %v", got, err)
	}
	// A cluster id is read past.
	h := enc(nil).u16(60).u16(0).u64(0).u16(flagClusterID).short("c")
	if _, _, err := readHeader(h); err != nil {
		t.Error(err)
	}
	if enc(nil).short(string(make([]byte, 300)))[0] != 255 {
		t.Error("a long short string")
	}
	// Frames.
	r := bufio.NewReader(bytes.NewReader(appendFrame(nil, frameMethod, 3, []byte("abc"))))
	if f, err := readFrame(r, 4096); err != nil || f.channel != 3 || string(f.payload) != "abc" {
		t.Errorf("frame %+v %v", f, err)
	}
	r = bufio.NewReader(bytes.NewReader([]byte{1, 0, 0, 0, 0, 0, 5, 1}))
	if _, err := readFrame(r, 4096); err == nil {
		t.Error("a cut frame")
	}
}

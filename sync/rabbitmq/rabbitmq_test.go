package syncrabbitmq

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

// certFiles writes a self-signed certificate and its key as PEM files.
func certFiles(t *testing.T) (string, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fit"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	kd, _ := x509.MarshalECPrivateKey(k)
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600)
	return cert, key
}

func TestProvision(t *testing.T) {
	cert, key := certFiles(t)
	bad := filepath.Join(t.TempDir(), "bad.pem")
	os.WriteFile(bad, []byte("not a certificate"), 0o644)
	t.Setenv("FIT_RABBITMQ_TEST_PW", "secret")
	q := []string{"q"}
	for _, p := range []map[string]any{
		{"addresses": []string{"nohost"}, "queues": q},
		{"addresses": []string{}, "queues": q},
		{"queues": []string{}},
		{"queues": []string{"a", "a", ""}},
		{"queues": q, "streams": q},
		{"queues": q, "username": ""},
		{"queues": q, "password_env": "1bad"},
		{"queues": q, "password_env": "FIT_RABBITMQ_TEST_UNSET"},
		{"queues": q, "auth": "EXTERNAL"},
		{"queues": q, "auth": "EXTERNAL", "tls": true, "cert_file": cert, "key_file": key, "username": "u"},
		{"queues": q, "auth": "GSSAPI"},
		{"queues": q, "ca_file": bad},
		{"queues": q, "tls": true, "ca_file": bad},
		{"queues": q, "tls": true, "ca_file": filepath.Join(t.TempDir(), "none")},
		{"queues": q, "tls": true, "cert_file": cert},
		{"queues": q, "tls": true, "cert_file": bad, "key_file": bad},
		{"queues": q, "codec": map[string]any{"module": "nope"}},
		{"queues": q, "codec": map[string]any{"module": "rabbitmq"}},
	} {
		if _, err := load(t, fitsync.Sources, "rabbitmq", conn(p)); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	s := mustLoad(t, fitsync.Sources, "rabbitmq", map[string]any{"addresses": []string{"r:5671"}, "queues": q, "streams": []string{"s"},
		"auth": "EXTERNAL", "tls": true, "cert_file": cert, "key_file": key, "ca_file": cert, "server_name": "rabbit",
		"timeout": "5s", "heartbeat": "10s", "frame_max": 8192, "max_message_bytes": 1000, "prefetch": 10, "vhost": "v"}).(*Source)
	if !s.cfg.External || s.cfg.TLS.ServerName != "rabbit" || s.cfg.Timeout != 5*time.Second || s.cfg.Heartbeat != 10*time.Second ||
		s.cfg.FrameMax != 8192 || s.cfg.MaxBody != 1000 || s.cfg.Deliveries != 20 || s.cfg.Vhost != "v" || len(s.cfg.TLS.Certificates) != 1 {
		t.Fatalf("%+v", s.cfg)
	}
	k := source(t, &fake{}, map[string]any{"queues": q, "codec": map[string]any{"module": "json"}})
	if k.cfg.Password != "secret" || k.cfg.Username != "u" || k.cfg.Deliveries != DefaultPrefetch {
		t.Fatalf("%+v", k.cfg)
	}
	for _, p := range []map[string]any{
		{"routing_key": "{other}"}, {"exchange": strings.Repeat("x", 256)}, {"codec": map[string]any{"module": "nope"}},
	} {
		if _, err := load(t, fitsync.Sinks, "rabbitmq", conn(p)); err == nil {
			t.Errorf("sink %v loaded", p)
		}
	}
}

func delivery(tag uint64, body string, h amqpwire.Table) *amqpwire.Delivery {
	return &amqpwire.Delivery{ConsumerTag: "ctag-q", DeliveryTag: tag, Exchange: "ex", RoutingKey: "rk", Body: []byte(body),
		Properties: amqpwire.Properties{Headers: h, MessageID: "m", Timestamp: time.Unix(1700000000, 0).UTC(), Priority: 1}}
}

// collect runs s, keeping what it emits, until stop is called.
type collected struct {
	mu   sync.Mutex
	recs []*fitsync.Record
}

func (c *collected) emit(r *fitsync.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, r)
	c.mu.Unlock()
	return nil
}

func (c *collected) n() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.recs)
}

func (c *collected) get(i int) *fitsync.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recs[i]
}

func TestSource(t *testing.T) {
	f := &fake{}
	s := source(t, f, map[string]any{"queues": []string{"q"}})
	var got collected
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.Run(ctx, &fitsync.Run{Emit: got.emit}) }()
	eventually(t, "dialled", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.conns) == 1 })
	c := f.last()
	c.deliveries <- delivery(1, "one", amqpwire.Table{"fit-key": "k1", "n": int32(3)})
	c.deliveries <- delivery(2, "two", amqpwire.Table{"fit-key": []byte{0xff}})
	c.deliveries <- delivery(3, "three", nil)
	eventually(t, "records", func() bool { return got.n() == 3 })
	r := got.get(0)
	if r.Source != "rabbitmq" || r.Namespace != "q" || r.Op != fitsync.OpMessage || r.Key.Text() != "k1" ||
		!r.CommitTime.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("record %+v", r)
	}
	for name, want := range map[string]string{"value": "one", "exchange": "ex", "routing_key": "rk", "queue": "q", "message_id": "m"} {
		if v, ok := r.After.Get(name); !ok || v.Text() != want {
			t.Errorf("%s: %v", name, v)
		}
	}
	if h, _ := r.After.Get("headers"); h.Fields()[1].Value.AsInt() != 3 {
		t.Errorf("headers %v", h)
	}
	if got.get(1).Key.Kind() != fitsync.KindBytes || !got.get(2).Key.IsNull() {
		t.Errorf("keys %v %v", got.get(1).Key, got.get(2).Key)
	}
	if c.qos != DefaultPrefetch || len(c.consumes) != 1 || c.consumes[0].queue != "q" {
		t.Errorf("qos %d consumes %+v", c.qos, c.consumes)
	}
	// Acknowledged acknowledges up to the tag, every one before it with it,
	// on the session's goroutine; old and stale positions do nothing.
	s.Acknowledged(got.get(1).Position)
	eventually(t, "ack", func() bool { return len(c.ackList()) == 1 })
	s.Acknowledged(got.get(0).Position)
	s.Acknowledged(fitsync.Position(`{"run":"other","tag":9}`))
	s.Acknowledged(fitsync.Position(`not json`))
	s.Acknowledged(got.get(2).Position)
	eventually(t, "acks", func() bool { return len(c.ackList()) == 2 })
	if acks := c.ackList(); !reflect.DeepEqual(acks, []fakeAck{{2, true}, {3, true}}) {
		t.Errorf("acks %v", acks)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// The session stays, for late acknowledgements, until the next run or
	// Cleanup.
	if c.closed {
		t.Error("closed at the run's end")
	}
	s.Cleanup()
	if !c.closed {
		t.Error("not closed by Cleanup")
	}
	s.Acknowledged(got.get(2).Position)
}

func TestSourceLost(t *testing.T) {
	f := &fake{}
	s := source(t, f, map[string]any{"queues": []string{"q"}})
	errc := make(chan error, 1)
	go func() {
		errc <- s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }})
	}()
	eventually(t, "dialled", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.conns) == 1 })
	f.last().fail(errors.New("connection reset"))
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatal(err)
	}
	// Emit's error stops the run.
	stop := errors.New("stopping")
	go func() {
		errc <- s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }})
	}()
	eventually(t, "dialled again", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.conns) == 2 })
	f.last().deliveries <- delivery(1, "x", nil)
	if err := <-errc; !errors.Is(err, stop) {
		t.Fatal(err)
	}
	// An ack that fails ends the session's goroutine.
	c := f.last()
	c.ackHook = func(uint64, bool) error { return errors.New("broken") }
	s.mu.Lock()
	run := s.sess.run
	s.mu.Unlock()
	s.Acknowledged(fitsync.Position(`{"run":"` + run + `","tag":1}`))
	eventually(t, "a failed ack", func() bool { return len(c.ackList()) == 1 })
	s.Cleanup()
}

func TestSourceErrors(t *testing.T) {
	ctx := context.Background()
	run := &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }}
	f := &fake{dialErr: errors.New("refused")}
	s := source(t, f, map[string]any{"queues": []string{"q"}, "streams": []string{"s"}})
	if err := s.Run(ctx, run); err == nil || err.Error() != "refused" {
		t.Errorf("dial: %v", err)
	}
	if err := s.Test(ctx); err == nil {
		t.Error("test dial")
	}
	if err := s.Run(ctx, &fitsync.Run{From: fitsync.Position("{"), Emit: run.Emit}); err == nil {
		t.Error("a bad checkpoint")
	}
	f.dialErr = nil
	for _, setup := range []func(c *fakeClient){
		func(c *fakeClient) { c.qosErr = errors.New("qos") },
		func(c *fakeClient) { c.consumeErr = errors.New("consume") },
	} {
		f.setup = setup
		if err := s.Run(ctx, run); err == nil {
			t.Error("no error")
		}
	}
	// Only the stream's consume fails.
	f.setup = func(c *fakeClient) {
		c.ackHook = nil
	}
	n := 0
	f.setup = func(c *fakeClient) {
		n = 0
		c.consumeErr = nil
	}
	s2 := source(t, f, map[string]any{"streams": []string{"s"}})
	f.setup = func(c *fakeClient) { c.consumeErr = errors.New("no stream") }
	if err := s2.Run(ctx, run); err == nil || !strings.Contains(err.Error(), "stream") {
		t.Errorf("stream: %v", err)
	}
	_ = n
	f.setup = func(c *fakeClient) { c.declareErr = errors.New("NOT_FOUND") }
	if err := s.Test(ctx); err == nil || !strings.Contains(err.Error(), "queue q") {
		t.Errorf("test: %v", err)
	}
	f.setup = nil
	if err := s.Test(ctx); err != nil {
		t.Error(err)
	}
	s.Cleanup()
	s2.Cleanup()
}

func TestStream(t *testing.T) {
	f := &fake{}
	s := source(t, f, map[string]any{"streams": []string{"s"}, "stream_start": "next", "metadata_field": "_meta",
		"codec": map[string]any{"module": "json"}, "namespace": "ns"})
	var got collected
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	from := fitsync.Position(`{"run":"old","tag":7,"streams":{"s":10}}`)
	go func() { errc <- s.Run(ctx, &fitsync.Run{From: from, Emit: got.emit}) }()
	eventually(t, "dialled", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.conns) == 1 })
	c := f.last()
	stream := func(tag uint64, off int64, body string) *amqpwire.Delivery {
		d := delivery(tag, body, amqpwire.Table{"x-stream-offset": off})
		d.ConsumerTag = "ctag-s"
		return d
	}
	c.deliveries <- stream(1, 9, `{"id":9}`) // before the offset asked: acknowledged, not emitted
	c.deliveries <- stream(2, 10, `{"id":10}`)
	c.deliveries <- stream(3, 11, `not json`)
	eventually(t, "records", func() bool { return got.n() == 2 })
	cancel()
	<-errc
	if args := c.consumes[0].args; args["x-stream-offset"] != int64(10) {
		t.Errorf("args %v", args)
	}
	if acks := c.ackList(); !reflect.DeepEqual(acks, []fakeAck{{1, false}}) {
		t.Errorf("acks %v", acks)
	}
	r := got.get(0)
	if r.Namespace != "ns" {
		t.Fatalf("record %+v", r)
	}
	if m, ok := r.After.Get("_meta"); !ok || len(m.Fields()) < 4 {
		t.Errorf("metadata %v", r.After)
	}
	var p position
	json.Unmarshal(got.get(1).Position, &p)
	if p.Tag != 3 || p.Streams["s"] != 12 {
		t.Errorf("position %+v", p)
	}
	if v, _ := got.get(1).After.Get("error"); v.IsNull() {
		t.Errorf("a codec's error: %v", got.get(1).After)
	}
	// Without a checkpoint, stream_start.
	go func() { errc <- s.Run(context.Background(), &fitsync.Run{Emit: got.emit}) }()
	eventually(t, "dialled", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.conns) == 2 })
	f.last().fail(errors.New("gone"))
	<-errc
	if args := f.last().consumes[0].args; args["x-stream-offset"] != "next" {
		t.Errorf("args %v", args)
	}
	s.Cleanup()
}

func TestJob(t *testing.T) {
	f := &fake{}
	s := source(t, f, map[string]any{"queues": []string{"q"}, "idle_end": "50ms"})
	// A codec making two records of "two", none of "none", one otherwise.
	s.codec = codecFunc{decode: func(data []byte, emit func(*fitsync.Record)) error {
		switch string(data) {
		case "none":
		case "two":
			emit(&fitsync.Record{Op: fitsync.OpInsert, Namespace: "own", Tx: fitsync.Tx{ID: "codec-tx"}})
			emit(&fitsync.Record{Op: fitsync.OpInsert})
		default:
			emit(&fitsync.Record{Op: fitsync.OpMessage, After: fitsync.Map()})
		}
		return nil
	}}
	f.setup = func(c *fakeClient) {
		c.deliveries <- delivery(1, "two", nil)
		c.deliveries <- delivery(2, "none", nil)
		c.deliveries <- delivery(3, "one", nil)
	}
	var mu sync.Mutex
	var recs []*fitsync.Record
	emit := func(r *fitsync.Record) error {
		mu.Lock()
		recs = append(recs, r)
		mu.Unlock()
		if r.Position != nil {
			// The sinks have it.
			go s.Acknowledged(r.Position)
		}
		return nil
	}
	if err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: emit}); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || recs[0].Tx.ID != "codec-tx" || !recs[0].Tx.First || recs[1].Tx.ID != "q@1" || !recs[1].Tx.Last ||
		recs[0].Namespace != "own" || recs[1].Namespace != "q" || recs[2].Tx.ID != "" {
		t.Fatalf("records %+v", recs)
	}
	acks := f.last().ackList()
	if acks[0] != (fakeAck{2, false}) || acks[len(acks)-1] != (fakeAck{3, true}) {
		t.Errorf("acks %v", acks)
	}
	// A job whose acknowledgements never come ends with ctx, or with the
	// connection.
	f.setup = func(c *fakeClient) { c.deliveries <- delivery(1, "one", nil) }
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx, &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return nil }}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctx: %v", err)
	}
	errc := make(chan error, 1)
	go func() {
		errc <- s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return nil }})
	}()
	time.Sleep(150 * time.Millisecond)
	f.last().fail(errors.New("gone"))
	if err := <-errc; err == nil {
		t.Error("lost while draining")
	}
	s.Cleanup()
}

func TestSink(t *testing.T) {
	f := &fake{}
	s := sink(t, f, map[string]any{"exchange": "ex", "routing_key": "r.{namespace}.{key}", "content_type": "application/json"})
	ctx := context.Background()
	if err := s.Write(ctx, nil); err != nil {
		t.Fatal(err)
	}
	batch := []*fitsync.Record{
		{Namespace: "a", Op: fitsync.OpInsert, Key: fitsync.String("k"), After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.String("v1")}),
			Position: fitsync.Position(`{"n":1}`), CommitTime: time.Unix(5, 0)},
		{Namespace: "b", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(2)}), Position: fitsync.Position(`{"n":2}`)},
		{Namespace: "c", Op: fitsync.OpMessage, After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.Bytes([]byte{1})})},
	}
	if err := s.Write(ctx, batch); err != nil {
		t.Fatal(err)
	}
	c := f.last()
	if !c.confirm || len(c.pubs) != 3 {
		t.Fatalf("pubs %+v", c.pubs)
	}
	ids := fitsync.RecordIDs(batch)
	p := c.pubs[0]
	if p.exchange != "ex" || p.key != "r.a.k" || !p.mandatory || p.body != "v1" || p.props.MessageID != ids[0] || p.props.DeliveryMode != 2 ||
		p.props.Headers["fit-key"] != "k" || p.props.Headers["fit-op"] != "insert" || p.props.ContentType != "application/json" ||
		!p.props.Timestamp.Equal(time.Unix(5, 0)) {
		t.Errorf("pub 0 %+v", p)
	}
	if k := c.pubs[1].props.Headers["fit-key"]; k != `{"id":2}` || c.pubs[1].key != `r.b.{"id":2}` {
		t.Errorf("pub 1 key %v %q", k, c.pubs[1].key)
	}
	if _, ok := c.pubs[2].props.Headers["fit-key"]; ok || c.pubs[2].key != "r.c." {
		t.Errorf("pub 2 %+v", c.pubs[2])
	}
	// A nack fails from its record; a return fails its record for good,
	// whichever comes first.
	c.nackKeys["r.b.x"] = true
	c.returnKeys["r.a.x"] = true
	recs := func(ns ...string) []*fitsync.Record {
		var b []*fitsync.Record
		for _, n := range ns {
			b = append(b, &fitsync.Record{Namespace: n, Op: fitsync.OpInsert, Key: fitsync.String("x")})
		}
		return b
	}
	var be *fitsync.BatchError
	if err := s.Write(ctx, recs("c", "b", "a")); !errors.As(err, &be) || be.Index != 1 || fitsync.IsPermanent(err) {
		t.Errorf("nack: %v", err)
	}
	if err := s.Write(ctx, recs("c", "a", "b")); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "no queue") {
		t.Errorf("return: %v", err)
	}
	// Too large, for good; a broken publish, from there.
	c.publishErr = amqpwire.ErrTooLarge
	if err := s.Write(ctx, recs("c")); !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
		t.Errorf("too large: %v", err)
	}
	c.publishErr, c.failAt = errors.New("blocked"), len(c.pubs)+1
	if err := s.Write(ctx, recs("c", "c")); !errors.As(err, &be) || be.Index != 1 || fitsync.IsPermanent(err) {
		t.Errorf("publish: %v", err)
	}
	c.publishErr = nil
	// A routing key too long and a codec that fails: the record, for good.
	long := recs(strings.Repeat("n", 300))
	if err := s.Write(ctx, long); !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Errorf("long key: %v", err)
	}
	codec := s.codec
	s.codec = codecFunc{encode: func([]byte, []*fitsync.Record) ([]byte, error) { return nil, errors.New("cannot") }}
	if err := s.Write(ctx, recs("c")); !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Errorf("codec: %v", err)
	}
	s.codec = codec
	// Confirms lost: the connection is dropped and made again.
	c.waitErr = errors.New("lost")
	if err := s.Write(ctx, recs("c")); err == nil || err.Error() != "lost" {
		t.Errorf("wait: %v", err)
	}
	if !c.closed {
		t.Error("kept a broken connection")
	}
	if err := s.Write(ctx, recs("c")); err != nil || f.dials != 2 {
		t.Errorf("again: %v, %d dials", err, f.dials)
	}
	// A connection that ended is made again; a failure with the connection
	// gone drops it.
	f.last().fail(errors.New("gone"))
	if err := s.Write(ctx, recs("c")); err != nil || f.dials != 3 {
		t.Errorf("after loss: %v, %d dials", err, f.dials)
	}
	c = f.last()
	c.publishErr, c.failAt = errors.New("broken"), 1
	c.err = errors.New("broken")
	if err := s.Write(ctx, recs("c", "c")); err == nil || !c.closed {
		t.Errorf("broken: %v", err)
	}
	f.dialErr = errors.New("refused")
	if err := s.Write(ctx, recs("c")); err == nil {
		t.Error("dial")
	}
	if err := s.Test(ctx); err == nil {
		t.Error("test dial")
	}
	f.dialErr = nil
	f.setup = func(c *fakeClient) { c.confirmErr = errors.New("no confirms") }
	if err := s.Write(ctx, recs("c")); err == nil {
		t.Error("confirm.select")
	}
	if err := s.Test(ctx); err == nil {
		t.Error("test confirm")
	}
	f.setup = nil
	if err := s.Test(ctx); err != nil {
		t.Error(err)
	}
	s.Cleanup()
}

func TestSinkOptions(t *testing.T) {
	f := &fake{}
	s := sink(t, f, map[string]any{"transient": true, "unroutable": "drop"})
	if err := s.Write(context.Background(), []*fitsync.Record{{Namespace: "q", Op: fitsync.OpInsert}}); err != nil {
		t.Fatal(err)
	}
	p := f.last().pubs[0]
	if p.key != "q" || p.exchange != "" || p.mandatory || p.props.DeliveryMode != 1 {
		t.Errorf("pub %+v", p)
	}
	s.Cleanup()
}

func TestValues(t *testing.T) {
	tm := time.Unix(1, 0).UTC()
	v := table(amqpwire.Table{"b": true, "i8": int8(-1), "u8": uint8(1), "i16": int16(2), "u16": uint16(3), "i32": int32(4), "u32": uint32(5),
		"i64": int64(6), "f32": float32(1.5), "f64": 2.5, "d": amqpwire.Decimal{Scale: 2, Value: -5}, "s": "x", "x": []byte{1}, "t": tm,
		"a": []any{"y", nil}, "m": amqpwire.Table{"k": "v"}})
	want := map[string]string{"b": "true", "i8": "-1", "u8": "1", "i16": "2", "u16": "3", "i32": "4", "u32": "5", "i64": "6", "f32": "1.5", "f64": "2.5", "d": "-0.05", "s": "x"}
	for k, w := range want {
		if got, _ := v.Get(k); fitsync.TextOf(got) != w {
			t.Errorf("%s: %q, not %q", k, fitsync.TextOf(got), w)
		}
	}
	if a, _ := v.Get("a"); len(a.Elems()) != 2 || !a.Elems()[1].IsNull() {
		t.Errorf("array %v", a)
	}
	if m, _ := v.Get("m"); m.Kind() != fitsync.KindMap {
		t.Errorf("map %v", m)
	}
	for d, w := range map[amqpwire.Decimal]string{{Scale: 0, Value: 12}: "12", {Scale: 3, Value: 12345}: "12.345"} {
		if got := decimal(d); got != w {
			t.Errorf("%v: %s", d, got)
		}
	}
	if text("\xff").Kind() != fitsync.KindBytes || text("a").Kind() != fitsync.KindString {
		t.Error("text")
	}
}

func TestNewWire(t *testing.T) {
	// The real client, of a node that is not there.
	if _, err := dialer(nil).dial(context.Background(), amqpwire.Config{Addrs: []string{"127.0.0.1:1"}, Timeout: time.Second}); err == nil || !strings.HasPrefix(err.Error(), "rabbitmq: ") {
		t.Errorf("dial: %v", err)
	}
}

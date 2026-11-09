package syncmqtt

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
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// pemFiles writes a self-signed certificate and its key as PEM files.
func pemFiles(t *testing.T) (cert, key string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mqtt.test"}, DNSNames: []string{"mqtt.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(k)
	dir := t.TempDir()
	cert, key = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}), 0o600)
	return cert, key
}

func TestProvision(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.pem")
	os.WriteFile(junk, []byte("not a certificate"), 0o644)
	cert, key := pemFiles(t)
	t.Setenv("FIT_MQTT_TEST_PW", "secret")
	base := func(extra map[string]any) map[string]any {
		p := map[string]any{"servers": []string{"b:1883"}, "client_id": "c", "topics": []string{"a/#"}}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	for _, p := range []map[string]any{
		{"servers": []string{"nohost"}, "client_id": "c", "topics": []string{"a"}},
		{"servers": []string{}, "client_id": "c", "topics": []string{"a"}},
		{"servers": []string{"b:1"}, "client_id": "", "topics": []string{"a"}},
		{"servers": []string{"b:1"}, "client_id": "c", "topics": []string{}},
		base(map[string]any{"topics": []string{"a", "a"}}),
		base(map[string]any{"topics": []string{"a/#/b"}}),
		base(map[string]any{"keep_alive": "70000s"}),
		base(map[string]any{"password_env": "1bad"}),
		base(map[string]any{"password_env": "FIT_MQTT_TEST_UNSET"}),
		base(map[string]any{"password_env": "FIT_MQTT_TEST_PW", "version": "3.1.1"}),
		base(map[string]any{"ca_file": junk}),
		base(map[string]any{"tls": true, "ca_file": junk}),
		base(map[string]any{"tls": true, "ca_file": filepath.Join(dir, "none")}),
		base(map[string]any{"tls": true, "cert_file": cert}),
		base(map[string]any{"tls": true, "cert_file": junk, "key_file": junk}),
		base(map[string]any{"codec": map[string]any{"module": "nope"}}),
		base(map[string]any{"codec": map[string]any{"module": "mqtt_test_not_codec"}}),
		base(map[string]any{"qos": 3}),
	} {
		if _, err := load(t, fitsync.Sources, "mqtt", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	s := mustLoad(t, fitsync.Sources, "mqtt", base(map[string]any{"username": "u", "password_env": "FIT_MQTT_TEST_PW", "tls": true,
		"ca_file": cert, "cert_file": cert, "key_file": key, "server_name": "k", "timeout": "5s", "keep_alive": "10s", "qos": 0,
		"version": "3.1.1", "session_expiry": "1h", "receive_maximum": 10, "max_packet_size": 1000, "codec": map[string]any{"module": "json"}})).(*Source)
	c := s.cfg
	if c.Password != "secret" || c.Username != "u" || c.TLS.ServerName != "k" || len(c.TLS.Certificates) != 1 || c.Timeout != 5*time.Second ||
		c.KeepAlive != 10*time.Second || c.Version != mqttwire.Version311 || c.SessionExpiry != time.Hour || c.ReceiveMaximum != 10 ||
		c.MaxPacketSize != 1000 || c.ClientID != "c" || c.CleanStart || s.qos != 0 || s.codec == nil {
		t.Fatalf("%+v", c)
	}
	s = mustLoad(t, fitsync.Sources, "mqtt", base(nil)).(*Source)
	c = s.cfg
	if c.Version != mqttwire.Version5 || c.SessionExpiry != DefaultSessionExpiry || c.ReceiveMaximum != DefaultReceiveMaximum ||
		c.Timeout != DefaultTimeout || c.KeepAlive != DefaultKeepAlive || c.TLS != nil || s.qos != 1 || s.codec != nil {
		t.Fatalf("%+v", c)
	}
	// A v5 password without a user name.
	mustLoad(t, fitsync.Sources, "mqtt", base(map[string]any{"password_env": "FIT_MQTT_TEST_PW"}))
}

// collect is a Run whose records are kept.
type collect struct {
	mu   sync.Mutex
	recs []*fitsync.Record
	err  error
	ch   chan *fitsync.Record
}

func newCollect() *collect { return &collect{ch: make(chan *fitsync.Record, 100)} }

func (c *collect) run(once bool) *fitsync.Run {
	return &fitsync.Run{Once: once, Emit: func(r *fitsync.Record) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.err != nil {
			return c.err
		}
		c.recs = append(c.recs, r)
		c.ch <- r
		return nil
	}}
}

func (c *collect) next(t *testing.T) *fitsync.Record {
	t.Helper()
	select {
	case r := <-c.ch:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no record")
	}
	return nil
}

// started runs src on a goroutine; its error comes on the channel.
func started(src *Source, r *fitsync.Run) (context.CancelFunc, chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, r) }()
	return cancel, done
}

func waitErr(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	return nil
}

func waitAcks(t *testing.T, c *fakeClient, n int) []*mqttwire.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if a := c.acked(); len(a) >= n {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d acknowledgements, not %d", len(c.acked()), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestSourceAcks: messages are acknowledged only once the pipeline says
// the sinks have them, in order, and positions of another run do nothing.
func TestSourceAcks(t *testing.T) {
	n := newFakeNet()
	src := source(t, n, map[string]any{"qos": 2})
	col := newCollect()
	cancel, done := started(src, col.run(false))
	c := n.last(t, 1)
	if c.cfg.ClientID != "fit-test" || c.cfg.CleanStart || len(c.subs) != 1 || c.subs[0] != (mqttwire.Subscription{Filter: "t/#", QoS: 2, RetainHandling: 1}) {
		t.Fatalf("%+v %+v", c.cfg, c.subs)
	}
	ms := []*mqttwire.Message{{Topic: "t/a", QoS: 1, Payload: []byte("1")}, {Topic: "t/b", QoS: 0}, {Topic: "t/c", QoS: 2, Retain: true}}
	for _, m := range ms {
		c.msgs <- m
	}
	var recs []*fitsync.Record
	for range ms {
		recs = append(recs, col.next(t))
	}
	r := recs[0]
	want := fitsync.Map(fitsync.Field{Name: "topic", Value: fitsync.String("t/a")}, fitsync.Field{Name: "payload", Value: fitsync.Bytes([]byte("1"))},
		fitsync.Field{Name: "qos", Value: fitsync.Int(1)}, fitsync.Field{Name: "retain", Value: fitsync.Bool(false)})
	if r.Source != "mqtt" || r.Namespace != "t/a" || r.Op != fitsync.OpMessage || !r.Key.Equal(fitsync.String("t/a")) ||
		!r.After.Equal(want) || r.CommitTime.IsZero() || r.Tx.ID != "" {
		t.Fatalf("%+v", r)
	}
	time.Sleep(10 * time.Millisecond)
	if len(c.acked()) != 0 {
		t.Fatal("acknowledged before the sinks")
	}
	src.Acknowledged(recs[1].Position)
	if a := waitAcks(t, c, 2); a[0] != ms[0] || a[1] != ms[1] {
		t.Fatal(a)
	}
	// Another run's position, or not a position, does nothing.
	var p position
	json.Unmarshal(recs[2].Position, &p)
	p.Run = "other"
	other, _ := json.Marshal(p)
	src.Acknowledged(other)
	src.Acknowledged(fitsync.Position("nope"))
	time.Sleep(10 * time.Millisecond)
	if len(c.acked()) != 2 {
		t.Fatal(c.acked())
	}
	src.Acknowledged(recs[2].Position)
	waitAcks(t, c, 3)
	// ctx ending keeps the connection up: what the sinks write then is
	// acknowledged still, until the next run.
	c.msgs <- &mqttwire.Message{Topic: "t/d", QoS: 1}
	last := col.next(t)
	cancel()
	if err := waitErr(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.isClosed() {
		t.Fatal("closed")
	}
	src.Acknowledged(last.Position)
	waitAcks(t, c, 4)
	_, done = started(src, col.run(false))
	c2 := n.last(t, 2)
	if !c.isClosed() || c2.isClosed() {
		t.Fatal("not the old connection closed")
	}
	src.Acknowledged(last.Position)
	c2.lose(errors.New("gone"))
	if err := waitErr(t, done); err == nil || err.Error() != "gone" {
		t.Fatal(err)
	}
	if !c2.isClosed() {
		t.Fatal("not closed")
	}
	src.Cleanup()
}

// TestSourceStopAnswers: acknowledgements not yet sent when the connection
// is closed are sent first.
func TestSourceStopAnswers(t *testing.T) {
	n := newFakeNet()
	src := source(t, n, map[string]any{})
	col := newCollect()
	cancel, done := started(src, col.run(false))
	c := n.last(t, 1)
	c.msgs <- &mqttwire.Message{Topic: "t", QoS: 1}
	r := col.next(t)
	cancel()
	waitErr(t, done)
	src.mu.Lock()
	st := src.cur
	src.release(st, 1)
	src.mu.Unlock()
	st.ackMu.Lock() // the acker waits
	go func() { time.Sleep(10 * time.Millisecond); st.ackMu.Unlock() }()
	src.stop()
	if len(c.acked()) != 1 || r.Position == nil {
		t.Fatal(c.acked())
	}
	// A connection lost fails the answers quietly.
	st = &session{c: c, quit: make(chan struct{}), ackq: []*pending{{m: &mqttwire.Message{QoS: 1}}}}
	src.answer(st)
}

// TestSourceCodec: a codec's records, a message of none acknowledged with
// the ones around it, a payload it fails kept with its error.
func TestSourceCodec(t *testing.T) {
	n := newFakeNet()
	src := source(t, n, map[string]any{"namespace": "ns"})
	src.codec = testCodec{}
	src.now = func() time.Time { return time.Unix(9, 0) }
	col := newCollect()
	cancel, done := started(src, col.run(false))
	defer func() { cancel(); waitErr(t, done) }()
	c := n.last(t, 1)
	for _, p := range []string{"none", "two", "none", "bad", "none"} {
		c.msgs <- &mqttwire.Message{Topic: "t/x", QoS: 1, Payload: []byte(p)}
	}
	a, b, bad := col.next(t), col.next(t), col.next(t)
	if a.Tx.ID == "" || a.Tx.ID != b.Tx.ID || !a.Tx.First || a.Tx.Last || !b.Tx.Last || a.Position != nil || b.Position == nil {
		t.Fatalf("%+v %+v", a.Tx, b.Tx)
	}
	if a.Namespace != "ns" || b.Namespace != "ns" || !a.Key.Equal(fitsync.String("t/x")) || !b.Key.Equal(fitsync.Int(2)) ||
		!a.CommitTime.Equal(time.Unix(9, 0)) || !b.CommitTime.Equal(time.Unix(5, 0)) {
		t.Fatalf("%+v %+v", a, b)
	}
	if e, _ := bad.After.Get("error"); e.Text() != "bad payload" {
		t.Fatalf("%+v", bad)
	}
	// The first message, of no record, is acknowledged at once; the third
	// with the second, the fifth with the fourth.
	waitAcks(t, c, 1)
	src.Acknowledged(b.Position)
	waitAcks(t, c, 3)
	src.Acknowledged(bad.Position)
	if a := waitAcks(t, c, 5); string(a[4].Payload) != "none" {
		t.Fatal(a)
	}
	// Without namespace, the codec's namespace is kept.
	src.Namespace = ""
	c.msgs <- &mqttwire.Message{Topic: "t/y", QoS: 1, Payload: []byte("two")}
	if a, b := col.next(t), col.next(t); a.Namespace != "t/y" || b.Namespace != "own" {
		t.Fatalf("%s %s", a.Namespace, b.Namespace)
	}
}

// TestSourceJob reads until idle, then ends once every record read is
// acknowledged.
func TestSourceJob(t *testing.T) {
	n := newFakeNet()
	src := source(t, n, map[string]any{"idle": "20ms"})
	col := newCollect()
	_, done := started(src, col.run(true))
	c := n.last(t, 1)
	c.msgs <- &mqttwire.Message{Topic: "t", QoS: 1}
	r := col.next(t)
	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("ended before the acknowledgement: %v", err)
	default:
	}
	src.Acknowledged(r.Position)
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
	if len(c.acked()) != 1 || !c.isClosed() {
		t.Fatal(c.acked())
	}
	// ctx ending, or the connection lost, while it waits.
	for _, lose := range []bool{false, true} {
		dialed := n.dialed()
		cancel, done := started(src, col.run(true))
		c := n.last(t, dialed+1)
		c.msgs <- &mqttwire.Message{Topic: "t", QoS: 1}
		col.next(t)
		time.Sleep(40 * time.Millisecond)
		if lose {
			c.lose(errors.New("gone"))
		} else {
			cancel()
		}
		if err := waitErr(t, done); err == nil {
			t.Fatal("no error")
		}
		cancel()
	}
	// A job of nothing ends after idle.
	_, done = started(src, col.run(true))
	if err := waitErr(t, done); err != nil {
		t.Fatal(err)
	}
}

// TestSourceBeforeSuback: the session's messages that come before the
// SUBACK are taken while the source waits for it, and a job's idle starts
// only once subscribed.
func TestSourceBeforeSuback(t *testing.T) {
	n := newFakeNet()
	n.subGate = make(chan struct{})
	src := source(t, n, map[string]any{"idle": "10ms"})
	col := newCollect()
	_, done := started(src, col.run(true))
	c := n.last(t, 1)
	for range 3 {
		c.msgs <- &mqttwire.Message{Topic: "t", QoS: 1}
	}
	var last *fitsync.Record
	for range 3 {
		last = col.next(t)
	}
	time.Sleep(30 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("ended before subscribing: %v", err)
	default:
	}
	close(n.subGate)
	src.Acknowledged(last.Position)
	if err := waitErr(t, done); err != nil || len(c.acked()) != 3 {
		t.Fatal(err, c.acked())
	}
}

// TestSourceErrors: no broker, a subscription refused, Emit failing.
func TestSourceErrors(t *testing.T) {
	n := newFakeNet()
	n.dialErr["a:1"] = errors.New("refused a")
	n.dialErr["b:1"] = errors.New("refused b")
	src := source(t, n, map[string]any{"servers": []string{"a:1", "b:1"}})
	if err := src.Run(context.Background(), newCollect().run(false)); err == nil || !strings.Contains(err.Error(), "refused a\nrefused b") {
		t.Fatal(err)
	}
	if err := src.Test(context.Background()); err == nil {
		t.Fatal("tested")
	}
	// The first refused, the second taken.
	delete(n.dialErr, "b:1")
	n.subErr = errors.New("not granted")
	if err := src.Run(context.Background(), newCollect().run(false)); err == nil || err.Error() != "not granted" {
		t.Fatal(err)
	}
	if c := n.last(t, 1); c.cfg.Address != "b:1" || !c.isClosed() {
		t.Fatal(c.cfg.Address)
	}
	if err := src.Test(context.Background()); err == nil {
		t.Fatal("tested")
	}
	c := n.last(t, 2)
	if !c.cfg.CleanStart || !strings.HasPrefix(c.cfg.ClientID, "fit-test-test-") || c.cfg.SessionExpiry != 0 || !c.isClosed() {
		t.Fatalf("%+v", c.cfg)
	}
	n.subErr = nil
	if err := src.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	col := newCollect()
	col.err = errors.New("stopping")
	_, done := started(src, col.run(false))
	n.last(t, 4).msgs <- &mqttwire.Message{Topic: "t"}
	if err := waitErr(t, done); err == nil || err.Error() != "stopping" {
		t.Fatal(err)
	}
	// A dial that ctx ends tries no other server.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n.dialErr["a:1"], n.dialErr["b:1"] = errors.New("a"), errors.New("b")
	if _, err := connect(ctx, src.cfg, []string{"a:1", "b:1"}, n.dial); err == nil || err.Error() != "a" {
		t.Fatal(err)
	}
	if testID("x")[:7] != "x-test-" || testID("")[:9] != "fit-test-" {
		t.Fatal(testID(""))
	}
}

// TestPlainRow holds a message's MQTT 5 properties.
func TestPlainRow(t *testing.T) {
	v := plainRow(&mqttwire.Message{Topic: "t", Payload: []byte{1}, QoS: 2, Retain: true, ContentType: "c", ResponseTopic: "r",
		CorrelationData: []byte{2}, UserProperties: []mqttwire.UserProperty{{Key: "a", Value: "1"}, {Key: "b", Value: "x"}, {Key: "a", Value: "2"}, {Key: "a", Value: "3"}}})
	b, err := (&fitsync.Record{After: v}).AppendJSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"content_type":"c"`, `"response_topic":"r"`, `"correlation_data":{"bytes":"Ag=="}`,
		`"user_properties":{"map":{"a":["1","2","3"],"b":"x"}}`, `"qos":{"int":2},"retain":true`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("%s lacks %s", b, want)
		}
	}
}

// TestWireClient dials the real client, through the connectors' adapter.
func TestWireClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		buf := make([]byte, 512)
		nc.Read(buf)                       // CONNECT
		nc.Write([]byte{0x20, 3, 0, 0, 0}) // CONNACK
		for _, err := nc.Read(buf); err == nil; _, err = nc.Read(buf) {
		}
	}()
	c, err := connect(context.Background(), mqttwire.Config{ClientID: "x"}, []string{ln.Addr().String()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w, err := c.Send(context.Background(), &mqttwire.Message{Topic: "t"})
	if err != nil || w.Wait(context.Background()) != nil {
		t.Fatal(err)
	}
	if _, err := c.Send(context.Background(), &mqttwire.Message{Topic: "+"}); err == nil {
		t.Fatal("sent")
	}
	if _, err := connect(context.Background(), mqttwire.Config{ClientID: "x", Timeout: time.Second}, []string{"127.0.0.1:1"}, nil); err == nil {
		t.Fatal("connected")
	}
}

package kafkawire

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProduceFetchEveryCodec(t *testing.T) {
	fc := newFakeCluster(t, 3, nil)
	fc.addTopic("t", 3)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	m, err := c.Metadata(ctx)
	if err != nil || len(m.Brokers) != 3 || m.ClusterID != "fake" || m.ControllerID != 1 || m.Brokers[0].Rack != "r1" {
		t.Fatalf("%+v, %v", m, err)
	}
	if tm := m.Topic("t"); tm == nil || len(tm.Partitions) != 3 || tm.Partitions[0].Partition != 0 || tm.Partitions[2].Leader != 3 {
		t.Fatalf("%+v", tm)
	}
	if m.Topic("none") != nil {
		t.Fatal("a topic not asked")
	}
	for round, codec := range codecs {
		var batches []ProduceBatch
		for p := range int32(3) {
			batches = append(batches, ProduceBatch{Topic: "t", Partition: p, Records: testRecords(20)})
		}
		res, err := c.Produce(ctx, ProduceOptions{Compression: codec}, batches...)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res {
			if r.BaseOffset != int64(round*20) || r.Err != nil {
				t.Fatalf("%+v", r)
			}
		}
	}
	var fps []FetchPartition
	for p := range int32(3) {
		fps = append(fps, FetchPartition{Topic: "t", Partition: p, Offset: 10})
	}
	res, err := c.Fetch(ctx, FetchOptions{}, fps...)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if len(r.Records) != 90 || r.NextOffset != 100 || r.Records[0].Offset != 10 || r.HighWatermark != 100 {
			t.Fatalf("partition %d: %d records, next %d", r.Partition, len(r.Records), r.NextOffset)
		}
	}
	// At the end: the broker waits, then nothing.
	res, err = c.Fetch(ctx, FetchOptions{MaxWait: 20 * time.Millisecond}, FetchPartition{Topic: "t", Partition: 1, Offset: 100})
	if err != nil || len(res[0].Records) != 0 || res[0].NextOffset != 100 {
		t.Fatalf("%+v, %v", res, err)
	}
	// Past it: OFFSET_OUT_OF_RANGE, not retried.
	res, err = c.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Partition: 1, Offset: 1000})
	if !errors.Is(err, ErrOffsetOutOfRange) || !errors.Is(res[0].Err, ErrOffsetOutOfRange) {
		t.Fatal(err)
	}
	// Offsets.
	or, err := c.ListOffsets(ctx, true, OffsetQuery{Topic: "t", Partition: 0, Timestamp: OffsetEarliest},
		OffsetQuery{Topic: "t", Partition: 1, Timestamp: OffsetLatest})
	if err != nil || or[0].Offset != 0 || or[1].Offset != 100 {
		t.Fatalf("%+v, %v", or, err)
	}
	if off, err := c.OffsetAt(ctx, "t", 2, time.Now()); err != nil || off != 50 {
		t.Fatalf("%d, %v", off, err)
	}
	if n, err := c.Partitions(ctx, "t"); err != nil || n != 3 {
		t.Fatalf("%d, %v", n, err)
	}
	// One partition each, to their leaders' fetch connections.
	if off, err := c.ProduceRecords(ctx, ProduceOptions{Acks: AcksLeader}, "t", 0, Record{Value: []byte("one")}); err != nil || off != 100 {
		t.Fatalf("%d, %v", off, err)
	}
}

func TestAcksNone(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	off, err := c.ProduceRecords(ctx, ProduceOptions{Acks: AcksNone}, "t", 0, Record{Value: []byte("x")})
	if err != nil || off != -1 {
		t.Fatalf("%d, %v", off, err)
	}
	res, err := c.Fetch(ctx, FetchOptions{MaxWait: time.Second}, FetchPartition{Topic: "t", Partition: 0})
	if err != nil || len(res[0].Records) != 1 {
		t.Fatalf("%+v, %v", res, err)
	}
}

func TestLeaderMoves(t *testing.T) {
	fc := newFakeCluster(t, 3, nil)
	fc.addTopic("t", 1)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	for i := range 6 {
		if _, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{Value: []byte(fmt.Sprint(i))}); err != nil {
			t.Fatal(err)
		}
		res, err := c.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Partition: 0, Offset: int64(i)})
		if err != nil || len(res[0].Records) != 1 || string(res[0].Records[0].Value) != fmt.Sprint(i) {
			t.Fatalf("%d: %+v, %v", i, res, err)
		}
		if off, err := c.Offset(ctx, "t", 0, OffsetLatest); err != nil || off != int64(i+1) {
			t.Fatalf("%d: %d, %v", i, off, err)
		}
		fc.setLeader("t", 0, int32(i%3+1)) // the client finds out from NOT_LEADER
	}
	// A partition with no leader for a while, then one.
	fc.setLeader("t", 0, -1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		fc.setLeader("t", 0, 2)
	}()
	if _, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{Value: []byte("after")}); err != nil {
		t.Fatal(err)
	}
	// A leader no broker of the metadata is.
	fc.setLeader("t", 0, 9)
	short := fakeClient(t, fc, func(c *Config) { c.RetryFor = 50 * time.Millisecond })
	if _, err := short.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{}); !errors.Is(err, ErrLeaderNotAvailable) {
		t.Fatal(err)
	}
	// A partition's error with no leader.
	fc.mu.Lock()
	fc.topics["t"][0].leader, fc.topics["t"][0].err = -1, ErrReplicaNotAvailable
	fc.mu.Unlock()
	if _, err := short.Offset(ctx, "t", 0, OffsetLatest); !errors.Is(err, ErrReplicaNotAvailable) {
		t.Fatal(err)
	}
	// A partition the topic does not have.
	if _, err := short.Offset(ctx, "t", 5, OffsetLatest); !errors.Is(err, ErrUnknownTopicOrPartition) {
		t.Fatal(err)
	}
	if _, err := short.Partitions(ctx, "nope"); !errors.Is(err, ErrUnknownTopicOrPartition) {
		t.Fatal(err)
	}
}

func TestBrokerFailures(t *testing.T) {
	fc := newFakeCluster(t, 2, nil)
	fc.addTopic("t", 2)
	c := fakeClient(t, fc, func(c *Config) { c.Brokers = fc.addrs() })
	ctx := testCtx(t)
	// The first produce to broker 1 loses its connection, the next its
	// answer (a late reply, past the request timeout), then works.
	var n atomic.Int32
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api != apiProduce || b.id != 1 {
			return fakeNormal, nil
		}
		switch n.Add(1) {
		case 1:
			return fakeClose, nil
		case 2:
			return fakeBadCorr, nil
		}
		return fakeNormal, nil
	})
	if _, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{Value: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if n.Load() != 3 {
		t.Fatalf("%d produce requests", n.Load())
	}
	// No answer at all: the request timeout fails the connection.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiListOffsets {
			return fakeDrop, nil
		}
		return fakeNormal, nil
	})
	short := fakeClient(t, fc, func(c *Config) { c.RequestTimeout = 50 * time.Millisecond; c.RetryFor = 100 * time.Millisecond })
	var ce *ConnError
	if _, err := short.Offset(ctx, "t", 0, OffsetLatest); !errors.As(err, &ce) || !strings.Contains(err.Error(), "no answer") {
		t.Fatal(err)
	}
	// A canceled context abandons the call.
	cctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := c.Offset(cctx, "t", 0, OffsetLatest); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fc.setHook(nil)
	// A broker answering what the client cannot read.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		switch api {
		case apiProduce, apiFetch, apiListOffsets:
			return fakeReply, []byte{0, 0, 0, 1}
		}
		return fakeNormal, nil
	})
	if _, err := short.ProduceRecords(ctx, ProduceOptions{}, "t", 1, Record{}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if _, err := short.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Partition: 1}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if _, err := short.Offset(ctx, "t", 1, OffsetLatest); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	// Answers that leave the partition out.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		var e encoder
		switch api {
		case apiProduce:
			e.int32(0)
			e.int32(0)
		case apiFetch:
			e.int32(0)
			e.int16(0)
			e.int32(0)
			e.int32(0)
		case apiListOffsets:
			e.int32(0)
			e.int32(0)
		default:
			return fakeNormal, nil
		}
		return fakeReply, e.b
	})
	if _, err := short.ProduceRecords(ctx, ProduceOptions{}, "t", 1, Record{}); !errors.As(err, &ce) || !strings.Contains(err.Error(), "missing") {
		t.Fatal(err)
	}
	if _, err := short.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Partition: 1}); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	if _, err := short.Offset(ctx, "t", 1, OffsetLatest); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	// A fetch session error for the whole answer.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api != apiFetch {
			return fakeNormal, nil
		}
		var e encoder
		e.int32(0)
		e.int16(int16(ErrUnknownServerError))
		e.int32(0)
		e.int32(0)
		return fakeReply, e.b
	})
	if _, err := short.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Partition: 1}); !errors.Is(err, ErrUnknownServerError) {
		t.Fatal(err)
	}
	// A produce error with the broker's message.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api != apiProduce {
			return fakeNormal, nil
		}
		var e encoder
		e.int32(1)
		e.string("t")
		e.int32(1)
		e.int32(1)
		e.int16(int16(ErrMessageTooLarge))
		e.int64(-1)
		e.int64(-1)
		e.int64(-1)
		e.int32(1)
		e.int32(0)
		e.nullableString(nil)
		msg := "too big"
		e.nullableString(&msg)
		e.int32(0)
		return fakeReply, e.b
	})
	if _, err := short.ProduceRecords(ctx, ProduceOptions{}, "t", 1, Record{}); !errors.Is(err, ErrMessageTooLarge) || !strings.Contains(err.Error(), "too big") {
		t.Fatal(err)
	}
	fc.setHook(nil)
	// A response larger than the client takes.
	tiny := fakeClient(t, fc, func(c *Config) { c.MaxResponseBytes = 16; c.RetryFor = time.Millisecond })
	if _, err := tiny.Metadata(ctx); !errors.As(err, &ce) || !strings.Contains(err.Error(), "limit") {
		t.Fatal(err)
	}
	// Brokers that are down: the next one is tried.
	fc.mu.Lock()
	fc.brokers[0].down = true
	fc.mu.Unlock()
	two := fakeClient(t, fc, func(c *Config) { c.Brokers = fc.addrs() })
	for range 5 {
		if _, err := two.Metadata(ctx, "t"); err != nil {
			t.Fatal(err)
		}
		two.Close()
		two = fakeClient(t, fc, func(c *Config) { c.Brokers = fc.addrs() })
	}
	none := fakeClient(t, fc, func(c *Config) { c.Brokers = []string{"127.0.0.1:1"}; c.DialTimeout = time.Second })
	if _, err := none.Metadata(ctx); !errors.As(err, &ce) {
		t.Fatal(err)
	}
}

func TestClientClose(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	// A fetch waiting on the broker when the client closes.
	done := make(chan error, 1)
	go func() {
		_, err := c.Fetch(ctx, FetchOptions{MaxWait: 10 * time.Second}, FetchPartition{Topic: "t", Partition: 0})
		done <- err
	}()
	for fc.count(apiFetch) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	c.Close()
	if err := <-done; err == nil {
		t.Fatal("a fetch outlived its client")
	}
	if _, err := c.Metadata(ctx); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
}

func TestDialedWhileClosing(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	var c *Client
	c = fakeClient(t, fc, func(cfg *Config) {
		cfg.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c.Close() // closed while the dial is in progress
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	})
	if _, err := c.Metadata(testCtx(t)); !errors.Is(err, errClosed) {
		t.Fatal(err)
	}
}

func TestConcurrentDials(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	var dials atomic.Int32
	gate := make(chan struct{})
	c := fakeClient(t, fc, func(cfg *Config) {
		cfg.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			<-gate
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}
	})
	ctx := testCtx(t)
	errs := make(chan error, 4)
	for range 4 {
		go func() { _, err := c.Metadata(ctx, "t"); errs <- err }()
	}
	time.Sleep(20 * time.Millisecond)
	close(gate)
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if dials.Load() != 1 {
		t.Fatalf("%d dials", dials.Load())
	}
	// A waiter whose context ends.
	gate2 := make(chan struct{})
	c2 := fakeClient(t, fc, func(cfg *Config) {
		cfg.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			<-gate2
			return nil, errors.New("refused")
		}
	})
	go c2.Metadata(ctx)
	time.Sleep(20 * time.Millisecond)
	cctx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := c2.conn(cctx, fc.addrs()[0], false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// A waiter sees the dial fail.
	go func() { time.Sleep(20 * time.Millisecond); close(gate2) }()
	if _, err := c2.conn(ctx, fc.addrs()[0], false); err == nil {
		t.Fatal("a failed dial shared as a success")
	}
}

func TestSASL(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	fc.set(func() { fc.users = map[string]string{"fit": "secret", "a=b,c": "pw"} })
	ctx := testCtx(t)
	for _, mech := range []string{SASLPlain, SASLScramSHA256, SASLScramSHA512} {
		for _, user := range []string{"fit", "a=b,c"} {
			pw := fc.users[user]
			c := fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: mech, Username: user, Password: pw} })
			if _, err := c.Metadata(ctx, "t"); err != nil {
				t.Fatalf("%s %s: %v", mech, user, err)
			}
		}
		c := fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: mech, Username: "fit", Password: "wrong"} })
		if _, err := c.Metadata(ctx, "t"); !errors.Is(err, ErrSASLAuthenticationFailed) || !strings.Contains(err.Error(), "Authentication failed") {
			t.Fatalf("%s: %v", mech, err)
		}
	}
	// A broker without SASL: the handshake is refused.
	fc.set(func() { fc.users = nil })
	c := fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: SASLPlain, Username: "fit", Password: "x"} })
	if _, err := c.Metadata(ctx); !errors.Is(err, ErrUnsupportedSASLMechanism) {
		t.Fatal(err)
	}
	if _, err := New(Config{Brokers: []string{"a:1"}, SASL: &SASL{Mechanism: "GSSAPI"}}); err == nil {
		t.Fatal("GSSAPI taken")
	}
	bc := &brokerConn{}
	if err := bc.authenticateMech(&SASL{Mechanism: "OTHER"}, nil); err == nil {
		t.Fatal("an unknown mechanism taken")
	}
	// A session with a lifetime: the connection is made again before its end.
	fc.set(func() { fc.users = map[string]string{"fit": "secret"} })
	fc.set(func() { fc.lifetime = 100 })
	c = fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: SASLScramSHA256, Username: "fit", Password: "secret"} })
	for range 3 {
		if _, err := c.Metadata(ctx, "t"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(120 * time.Millisecond)
	}
	if n := fc.count(apiSaslHandshake); n < 3 {
		t.Fatalf("%d handshakes", n)
	}
	// SaslAuthenticate's answer unreadable, or a version not spoken.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiSaslAuthenticate || api == apiSaslHandshake {
			return fakeReply, []byte{0}
		}
		return fakeNormal, nil
	})
	c = fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: SASLPlain, Username: "fit", Password: "secret"} })
	var ce *ConnError
	if _, err := c.Metadata(ctx); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	fc.setHook(nil)
	fc.set(func() { fc.versions = map[int16][2]int16{apiSaslAuthenticate: {2, 2}} })
	c = fakeClient(t, fc, func(c *Config) { c.SASL = &SASL{Mechanism: SASLPlain, Username: "fit", Password: "secret"} })
	if _, err := c.Metadata(ctx); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal(err)
	}
}

func TestTLS(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	fc := newFakeCluster(t, 1, &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
	fc.addTopic("t", 1)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	ctx := testCtx(t)
	c := fakeClient(t, fc, func(c *Config) { c.TLS = &tls.Config{RootCAs: pool} })
	if _, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{Value: []byte("tls")}); err != nil {
		t.Fatal(err)
	}
	bad := fakeClient(t, fc, func(c *Config) { c.TLS = &tls.Config{}; c.RetryFor = time.Millisecond })
	var ve *tls.CertificateVerificationError
	if _, err := bad.Metadata(ctx); !errors.As(err, &ve) {
		t.Fatal(err)
	}
}

func TestVersions(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	ctx := testCtx(t)
	// The oldest versions the client speaks.
	fc.set(func() {
		fc.versions = map[int16][2]int16{apiProduce: {0, 3}, apiFetch: {0, 4}, apiListOffsets: {0, 1}, apiMetadata: {0, 1}}
	})
	c := fakeClient(t, fc, nil)
	if _, err := c.ProduceRecords(ctx, ProduceOptions{Compression: Gzip}, "t", 0, testRecords(5)...); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ProduceRecords(ctx, ProduceOptions{Compression: Zstd}, "t", 0, testRecords(5)...); !errors.Is(err, ErrUnsupportedCompressionType) {
		t.Fatal(err)
	}
	res, err := c.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Partition: 0})
	if err != nil || len(res[0].Records) != 5 {
		t.Fatalf("%+v, %v", res, err)
	}
	if off, err := c.Offset(ctx, "t", 0, OffsetLatest); err != nil || off != 5 {
		t.Fatal(off, err)
	}
	// Versions the client does not speak.
	fc.set(func() {
		fc.versions = map[int16][2]int16{apiProduce: {9, 12}, apiFetch: {12, 17}, apiListOffsets: {6, 9}, apiMetadata: {9, 12}, apiCreateTopics: {5, 7}}
	})
	c = fakeClient(t, fc, func(c *Config) { c.RetryFor = time.Millisecond })
	if _, err := c.Metadata(ctx); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal(err)
	}
	fc.set(func() {
		fc.versions = map[int16][2]int16{apiProduce: {9, 12}, apiFetch: {12, 17}, apiListOffsets: {6, 9}, apiCreateTopics: {5, 7}}
	})
	c = fakeClient(t, fc, func(c *Config) { c.RetryFor = time.Millisecond })
	for _, err := range []error{
		func() error { _, err := c.ProduceRecords(ctx, ProduceOptions{}, "t", 0, Record{}); return err }(),
		func() error { _, err := c.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t"}); return err }(),
		func() error { _, err := c.Offset(ctx, "t", 0, OffsetLatest); return err }(),
		c.CreateTopics(ctx, time.Second, TopicSpec{Name: "x"}),
	} {
		if !errors.Is(err, ErrUnsupportedVersion) {
			t.Fatal(err)
		}
	}
	// ApiVersions with an error, and unreadable; Metadata unreadable.
	fc.set(func() { fc.versions = nil })
	fresh := func() *Client { return fakeClient(t, fc, func(c *Config) { c.RetryFor = time.Millisecond }) }
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiApiVersions {
			return fakeReply, []byte{0, byte(ErrUnsupportedVersion), 0, 0, 0, 0}
		}
		return fakeNormal, nil
	})
	if _, err := fresh().Metadata(ctx); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal(err)
	}
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiApiVersions {
			return fakeReply, []byte{0, 0, 0, 0, 0, 9}
		}
		return fakeNormal, nil
	})
	var ce *ConnError
	if _, err := fresh().Metadata(ctx); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiMetadata {
			return fakeReply, []byte{0, 0, 0, 0, 0, 9}
		}
		return fakeNormal, nil
	})
	if _, err := fresh().Metadata(ctx); !errors.As(err, &ce) {
		t.Fatal(err)
	}
}

func TestReadCommittedFetch(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	now := time.Now()
	add := func(pid int64, attrs int16, v string) int64 {
		b, _ := appendBatch(nil, []Record{{Value: []byte(v)}}, None, 0, producer{id: pid}, now)
		binaryAttrs(b, attrs)
		return fc.appendRaw("t", 0, b)
	}
	first := add(7, attrTransactional, "aborted")
	add(8, attrTransactional, "committed")
	add(-1, 0, "plain")
	fc.set(func() { fc.topics["t"][0].aborted = []abortedTxn{{producerID: 7, firstOffset: first}} })
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	for _, rc := range []bool{true, false} {
		res, err := c.Fetch(ctx, FetchOptions{ReadCommitted: rc}, FetchPartition{Topic: "t"})
		if err != nil {
			t.Fatal(err)
		}
		var vs []string
		for _, r := range res[0].Records {
			vs = append(vs, string(r.Value))
		}
		want := "committed,plain"
		if !rc {
			want = "aborted," + want
		}
		if strings.Join(vs, ",") != want {
			t.Fatalf("read committed %v: %v", rc, vs)
		}
	}
	// A corrupt batch in the answer.
	b, _ := appendBatch(nil, []Record{{Value: []byte("x")}}, None, 0, noProducer, now)
	b[len(b)-1] ^= 1
	fc.appendRaw("t", 0, b)
	if _, err := c.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t", Offset: 3}); err == nil || !strings.Contains(err.Error(), "CRC") {
		t.Fatal(err)
	}
}

func binaryAttrs(b []byte, attrs int16) {
	b[21], b[22] = byte(attrs>>8), byte(attrs)
	putCRC(b)
}

func TestAdmin(t *testing.T) {
	fc := newFakeCluster(t, 2, nil)
	fc.set(func() { fc.controller = 2 })
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	if err := c.CreateTopics(ctx, time.Second, TopicSpec{Name: "a", Partitions: 4, ReplicationFactor: 2, Configs: map[string]string{"x": "1", "a": "2"}}, TopicSpec{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Partitions(ctx, "a"); err != nil || n != 4 {
		t.Fatal(n, err)
	}
	if err := c.CreateTopics(ctx, time.Second, TopicSpec{Name: "a"}); !errors.Is(err, ErrTopicAlreadyExists) || !strings.Contains(err.Error(), "already exists") {
		t.Fatal(err)
	}
	if err := c.DeleteTopics(ctx, time.Second, "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteTopics(ctx, time.Second, "a"); !errors.Is(err, ErrUnknownTopicOrPartition) {
		t.Fatal(err)
	}
	// The controller moves: NOT_CONTROLLER until the metadata says where.
	var moved atomic.Bool
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiCreateTopics && !moved.Swap(true) {
			fc.set(func() { fc.controller = 1 })
		}
		return fakeNormal, nil
	})
	if err := c.CreateTopics(ctx, time.Second, TopicSpec{Name: "c"}); err != nil {
		t.Fatal(err)
	}
	// An unreadable answer; a controller in no broker list.
	fc.setHook(func(b *fakeBroker, api, v int16, body []byte) (fakeAction, []byte) {
		if api == apiDeleteTopics {
			return fakeReply, []byte{0, 0, 0, 0, 0, 0, 0, 5}
		}
		return fakeNormal, nil
	})
	short := fakeClient(t, fc, func(c *Config) { c.RetryFor = time.Millisecond })
	var ce *ConnError
	if err := short.DeleteTopics(ctx, time.Second, "c"); !errors.As(err, &ce) {
		t.Fatal(err)
	}
	fc.setHook(nil)
	fc.set(func() { fc.controller = 9 })
	if err := short.DeleteTopics(ctx, time.Second, "c"); err != nil {
		t.Fatal(err) // sent to the first broker, which forwards
	}
}

func TestArguments(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("no brokers taken")
	}
	if _, err := New(Config{Brokers: []string{"nohost"}}); err == nil {
		t.Fatal("a broker without a port taken")
	}
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	c := fakeClient(t, fc, nil)
	ctx := testCtx(t)
	one := []Record{{}}
	if _, err := c.Produce(ctx, ProduceOptions{}, ProduceBatch{Topic: "t", Records: one}, ProduceBatch{Topic: "t", Records: one}); err == nil {
		t.Fatal("two batches of one partition taken")
	}
	if _, err := c.Produce(ctx, ProduceOptions{}, ProduceBatch{Topic: "t"}); err == nil {
		t.Fatal("an empty batch taken")
	}
	if _, err := c.Produce(ctx, ProduceOptions{Compression: Compression(7)}, ProduceBatch{Topic: "t", Records: []Record{{}}}); err == nil {
		t.Fatal("codec 7 taken")
	}
	if _, err := c.Fetch(ctx, FetchOptions{}, FetchPartition{Topic: "t"}, FetchPartition{Topic: "t"}); err == nil {
		t.Fatal("a partition fetched twice")
	}
	if _, err := c.ListOffsets(ctx, false, OffsetQuery{Topic: "t"}, OffsetQuery{Topic: "t"}); err == nil {
		t.Fatal("a partition asked twice")
	}
	if Acks(5).wire() != -1 || AcksLeader.wire() != 1 || AcksNone.wire() != 0 {
		t.Fatal("acks")
	}
	if (Broker{Host: "::1", Port: 9}).Addr() != "[::1]:9" {
		t.Fatal("addr")
	}
}

func TestMetadataAge(t *testing.T) {
	fc := newFakeCluster(t, 1, nil)
	fc.addTopic("t", 1)
	fc.addTopic("u", 1)
	c := fakeClient(t, fc, func(c *Config) { c.MetadataMaxAge = 30 * time.Millisecond })
	ctx := testCtx(t)
	for _, topic := range []string{"t", "u", "t"} {
		if _, err := c.Offset(ctx, topic, 0, OffsetLatest); err != nil {
			t.Fatal(err)
		}
	}
	n := fc.count(apiMetadata)
	time.Sleep(40 * time.Millisecond)
	if _, err := c.Offset(ctx, "t", 0, OffsetLatest); err != nil {
		t.Fatal(err)
	}
	if fc.count(apiMetadata) != n+1 {
		t.Fatalf("%d metadata requests, want %d", fc.count(apiMetadata), n+1)
	}
	// Both topics are asked about again, and kept.
	c.mu.Lock()
	ok := c.meta.Topic("u") != nil
	c.mu.Unlock()
	if !ok {
		t.Fatal("a topic dropped from the metadata")
	}
}

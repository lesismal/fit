package synctest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/rabbitmq"
	amqpwire "github.com/lesismal/fit/sync/rabbitmq/wire"
)

// rabbitServer is a RabbitMQ node the tests use, from
// FIT_SYNC_RABBITMQ_ADDRS and FIT_SYNC_RABBITMQ_TLS_ADDRS
// (version=host:port,...), with FIT_SYNC_RABBITMQ_CA, FIT_SYNC_RABBITMQ_USER
// and FIT_SYNC_RABBITMQ_PASSWORD, as test/sync/rabbitmq.sh prints them.
type rabbitServer struct {
	name, addr, tlsAddr, ca, user, password string
	// cert and key are a client certificate of the CA, CN=fit, for
	// EXTERNAL (FIT_SYNC_RABBITMQ_CERT, FIT_SYNC_RABBITMQ_KEY).
	cert, key string
}

func rabbitServers(t *testing.T) []rabbitServer {
	t.Helper()
	addrs := os.Getenv("FIT_SYNC_RABBITMQ_ADDRS")
	if addrs == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_RABBITMQ_ADDRS is not set")
		}
		t.Skip("no RabbitMQ: FIT_SYNC_RABBITMQ_ADDRS is not set (eval \"$(test/sync/rabbitmq.sh up)\" sets it)")
	}
	tlsAddrs := map[string]string{}
	for _, kv := range strings.Split(os.Getenv("FIT_SYNC_RABBITMQ_TLS_ADDRS"), ",") {
		if v, a, ok := strings.Cut(kv, "="); ok {
			tlsAddrs[v] = a
		}
	}
	var out []rabbitServer
	for _, kv := range strings.Split(addrs, ",") {
		v, a, _ := strings.Cut(kv, "=")
		out = append(out, rabbitServer{name: "rabbitmq-" + v, addr: a, tlsAddr: tlsAddrs[v], ca: os.Getenv("FIT_SYNC_RABBITMQ_CA"),
			user: os.Getenv("FIT_SYNC_RABBITMQ_USER"), password: os.Getenv("FIT_SYNC_RABBITMQ_PASSWORD"),
			cert: os.Getenv("FIT_SYNC_RABBITMQ_CERT"), key: os.Getenv("FIT_SYNC_RABBITMQ_KEY")})
	}
	return out
}

func eachRabbit(t *testing.T, f func(t *testing.T, s rabbitServer)) {
	for _, s := range rabbitServers(t) {
		t.Run(s.name, func(t *testing.T) { f(t, s) })
	}
}

// conn connects to s, in confirm mode.
func (s rabbitServer) conn(t *testing.T) *amqpwire.Conn {
	t.Helper()
	c, err := amqpwire.Dial(hangGuard(t), amqpwire.Config{Addrs: []string{s.addr}, Username: s.user, Password: s.password, Deliveries: 1000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.ConfirmSelect(hangGuard(t)); err != nil {
		t.Fatal(err)
	}
	return c
}

// queue declares a queue of a new name and kind (classic, quorum, stream).
func (s rabbitServer) queue(t *testing.T, c *amqpwire.Conn, kind string) string {
	t.Helper()
	name := fmt.Sprintf("fit-%s-%d", kind, time.Now().UnixNano())
	if _, err := c.QueueDeclare(hangGuard(t), name, false, true, amqpwire.Table{"x-queue-type": kind}); err != nil {
		t.Fatal(err)
	}
	return name
}

// publish sends bodies to queue, each confirmed.
func publish(t *testing.T, c *amqpwire.Conn, queue string, bodies []string) {
	t.Helper()
	ctx := hangGuard(t)
	var first, last uint64
	for i, b := range bodies {
		seq, err := c.Publish(ctx, "", queue, false, amqpwire.Properties{DeliveryMode: 2}, []byte(b))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = seq
		}
		last = seq
	}
	if n, err := c.WaitConfirms(ctx, first, last); err != nil || n != nil {
		t.Fatalf("confirms: %v %v", n, err)
	}
}

// drain consumes queue until it has been quiet a second, acknowledging
// each message, and returns their bodies.
func (s rabbitServer) drain(t *testing.T, queue string) []string {
	t.Helper()
	c := s.conn(t)
	ctx := hangGuard(t)
	if err := c.Qos(ctx, 500); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Consume(ctx, queue, "", nil); err != nil {
		t.Fatal(err)
	}
	var out []string
	for {
		select {
		case d := <-c.Deliveries():
			out = append(out, string(d.Body))
			c.Ack(d.DeliveryTag, false)
		case <-time.After(time.Second):
			return out
		}
	}
}

func (s rabbitServer) params(extra map[string]any) map[string]any {
	p := map[string]any{"addresses": []string{s.addr}, "username": s.user, "password_env": "FIT_TEST_SYNC_RABBITMQ_PASSWORD"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func numbers(n, from int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = strconv.Itoa(from + i)
	}
	return out
}

// TestRabbitMQQueueAcrossKills drains a quorum queue into another with the
// rabbitmq source and sink, killed at random points and started again:
// every message reaches the copy (a replay may copy some twice), and the
// source queue ends empty, each message acknowledged only once the copy
// had it.
func TestRabbitMQQueueAcrossKills(t *testing.T) {
	eachRabbit(t, func(t *testing.T, s rabbitServer) {
		t.Setenv("FIT_TEST_SYNC_RABBITMQ_PASSWORD", s.password)
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 11))
		c := s.conn(t)
		src, dst := s.queue(t, c, "quorum"), s.queue(t, c, "quorum")
		const n = 3000
		publish(t, c, src, numbers(n, 0))
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
			"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
			"source": map[string]any{"module": "rabbitmq", "params": s.params(map[string]any{"queues": []string{src}, "prefetch": 10 + rnd.IntN(500), "idle_end": "2s"})},
			"sinks": []any{map[string]any{"name": "r", "batch": map[string]any{"max_records": 1 + rnd.IntN(200), "max_wait": "1ms"},
				"sink": map[string]any{"module": "rabbitmq", "params": s.params(map[string]any{"routing_key": dst})}}}})
		kills := runKilled(t, cfg, rnd, 8)
		got := s.drain(t, dst)
		seen := map[string]bool{}
		for _, b := range got {
			seen[b] = true
		}
		for _, b := range numbers(n, 0) {
			if !seen[b] {
				t.Fatalf("message %s lost: %d copied", b, len(got))
			}
		}
		q, err := c.QueueDeclare(hangGuard(t), src, true, false, nil)
		if err != nil || q.Messages != 0 {
			t.Fatalf("the source queue: %+v %v", q, err)
		}
		t.Logf("%d messages copied as %d across %d kills", n, len(got), kills)
	})
}

// TestRabbitMQStreamResumes reads a stream queue in two jobs: the second
// reads only what came after the first, from the offset in the
// checkpoint.
func TestRabbitMQStreamResumes(t *testing.T) {
	eachRabbit(t, func(t *testing.T, s rabbitServer) {
		t.Setenv("FIT_TEST_SYNC_RABBITMQ_PASSWORD", s.password)
		c := s.conn(t)
		st := s.queue(t, c, "stream")
		publish(t, c, st, numbers(200, 0))
		store := "rabbitmq-stream-" + st
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "checkpoint_interval": "1ms",
			"source": map[string]any{"module": "rabbitmq", "params": s.params(map[string]any{"streams": []string{st}, "prefetch": 50, "idle_end": "2s"})},
			"sinks":  []any{map[string]any{"name": "m", "sink": map[string]any{"module": "memory", "params": map[string]any{"store": store}}}}})
		run := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			if st, err := fitsync.RunPipeline(ctx, cfg.Sync, "p", fitsync.Options{}); err != nil || st.State != "finished" {
				t.Fatalf("%v: %+v", err, st)
			}
		}
		run()
		publish(t, c, st, numbers(100, 200))
		run()
		var got []string
		for _, r := range fitsync.MemoryStore(store).Records() {
			v, _ := r.After.Get("value")
			got = append(got, v.Text())
		}
		if want := numbers(300, 0); strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("read %d: %v", len(got), got)
		}
	})
}

// TestRabbitMQConfirms writes a batch over TLS: once Write returns, the
// queue holds all of it; a record no queue takes fails for good.
func TestRabbitMQConfirms(t *testing.T) {
	eachRabbit(t, func(t *testing.T, s rabbitServer) {
		if s.tlsAddr == "" {
			t.Skip("no TLS address")
		}
		t.Setenv("FIT_TEST_SYNC_RABBITMQ_PASSWORD", s.password)
		c := s.conn(t)
		q := s.queue(t, c, "classic")
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
			"sinks": []any{map[string]any{"name": "r", "sink": map[string]any{"module": "rabbitmq",
				"params": s.params(map[string]any{"addresses": []string{s.tlsAddr}, "tls": true, "ca_file": s.ca, "routing_key": q})}}}})
		m, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { module.Cleanup(m) })
		sink := m.(fitsync.Sink)
		if err := sink.(fitsync.Tester).Test(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
		var batch []*fitsync.Record
		for i := range 500 {
			batch = append(batch, fitsync.GenerateRecord("ns", int64(i), 500, 1))
		}
		if err := sink.Write(hangGuard(t), batch); err != nil {
			t.Fatal(err)
		}
		info, err := c.QueueDeclare(hangGuard(t), q, true, false, nil)
		if err != nil || info.Messages != 500 {
			t.Fatalf("after the batch: %+v %v", info, err)
		}
		got := s.drain(t, q)
		cfg2 := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
			"sinks": []any{map[string]any{"name": "r", "sink": map[string]any{"module": "rabbitmq",
				"params": s.params(map[string]any{"routing_key": "fit-nowhere-{namespace}"})}}}})
		m2, err := module.Load(fitsync.Sinks, cfg2.Sync.Pipelines[0].Sinks[0].Sink, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { module.Cleanup(m2) })
		var be *fitsync.BatchError
		if err := m2.(fitsync.Sink).Write(hangGuard(t), batch[:3]); !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
			t.Fatalf("unroutable: %v", err)
		}
		// The CA checked: a connection without it fails.
		if _, err := amqpwire.Dial(hangGuard(t), amqpwire.Config{Addrs: []string{s.tlsAddr}, Username: s.user, Password: s.password,
			TLS: &tls.Config{RootCAs: x509.NewCertPool()}, Timeout: 5 * time.Second}); err == nil {
			t.Error("TLS without the CA")
		}
		if len(got) != 500 {
			t.Errorf("drained %d", len(got))
		}
	})
}

// TestRabbitMQExternal authenticates with EXTERNAL, the TLS client
// certificate as the credential: the sink's connection test and a batch
// over it, then the source reading the batch back over it, its lag the
// queue's ready messages.
func TestRabbitMQExternal(t *testing.T) {
	eachRabbit(t, func(t *testing.T, s rabbitServer) {
		if s.tlsAddr == "" || s.cert == "" {
			t.Skip("no TLS address or client certificate")
		}
		c := s.conn(t)
		q := s.queue(t, c, "quorum")
		ext := map[string]any{"addresses": []string{s.tlsAddr}, "tls": true, "ca_file": s.ca, "auth": "EXTERNAL",
			"cert_file": s.cert, "key_file": s.key}
		sinkParams := map[string]any{"routing_key": q}
		for k, v := range ext {
			sinkParams[k] = v
		}
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
			"sinks": []any{map[string]any{"name": "r", "sink": map[string]any{"module": "rabbitmq", "params": sinkParams}}}})
		m, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { module.Cleanup(m) })
		sink := m.(fitsync.Sink)
		if err := sink.(fitsync.Tester).Test(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
		var batch []*fitsync.Record
		for i := range 100 {
			batch = append(batch, fitsync.GenerateRecord("ns", int64(i), 100, 1))
		}
		if err := sink.Write(hangGuard(t), batch); err != nil {
			t.Fatal(err)
		}
		srcParams := map[string]any{"queues": []string{q}, "prefetch": 10}
		for k, v := range ext {
			srcParams[k] = v
		}
		cfg2 := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "rabbitmq", "params": srcParams},
			"sinks": []any{map[string]any{"name": "n", "sink": map[string]any{"module": "stdout"}}}})
		sm, err := module.Load(fitsync.Sources, cfg2.Sync.Pipelines[0].Source, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { module.Cleanup(sm) })
		src := sm.(fitsync.Source)
		ctx, cancel := context.WithCancel(hangGuard(t))
		defer cancel()
		got := make(chan *fitsync.Record, 200)
		lags := make(chan fitsync.SourceLag, 100)
		errc := make(chan error, 1)
		go func() {
			errc <- src.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error { got <- r; return nil },
				Lag: func(l fitsync.SourceLag) {
					select {
					case lags <- l:
					default:
					}
				}})
		}()
		// Nothing is acknowledged, so the prefetch holds the rest ready.
		for range 10 {
			select {
			case <-got:
			case err := <-errc:
				t.Fatal(err)
			}
		}
		select {
		case l := <-lags:
			if l.Unit != fitsync.LagMessages || l.Behind < 80 || l.Behind > 100 {
				t.Errorf("lag %+v", l)
			}
		case <-time.After(15 * time.Second):
			t.Error("no lag reported")
		}
		cancel()
		<-errc
	})
}

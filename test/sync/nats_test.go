package synctest

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/nats"
	natswire "github.com/lesismal/fit/sync/nats/wire"
)

// natsCluster is a nats-server cluster of three nodes with JetStream, from
// FIT_SYNC_NATS_210 and FIT_SYNC_NATS_211 (test/sync/nats.sh), its user
// FIT_SYNC_NATS_USER with FIT_SYNC_NATS_PASSWORD.
type natsCluster struct {
	name           string
	servers        []string
	containers     []string
	user, password string
}

func natsClusters(t *testing.T) []natsCluster {
	t.Helper()
	var out []natsCluster
	for _, v := range []string{"210", "211"} {
		s := os.Getenv("FIT_SYNC_NATS_" + v)
		if s == "" {
			if os.Getenv("FIT_SYNC_INTEROP") == "required" {
				t.Fatalf("FIT_SYNC_NATS_%s is not set", v)
			}
			continue
		}
		out = append(out, natsCluster{name: "nats-" + v, servers: strings.Split(s, ","),
			containers: strings.Split(os.Getenv("FIT_SYNC_NATS_CONTAINERS_"+v), ","),
			user:       os.Getenv("FIT_SYNC_NATS_USER"), password: os.Getenv("FIT_SYNC_NATS_PASSWORD")})
	}
	if len(out) == 0 {
		t.Skip("no NATS: FIT_SYNC_NATS_210 and FIT_SYNC_NATS_211 are not set (test/sync/nats.sh sets them)")
	}
	return out
}

func (n natsCluster) client(t *testing.T) *natswire.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := natswire.Dial(ctx, natswire.Config{Servers: n.servers, User: n.user, Password: n.password, ReconnectWait: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// params are a connector's connection params, the password in an
// environment variable of the test's.
func (n natsCluster) params(t *testing.T, extra map[string]any) map[string]any {
	t.Setenv("FIT_SYNC_NATS_TEST_PASSWORD", n.password)
	p := map[string]any{"servers": n.servers, "username": n.user, "password_env": "FIT_SYNC_NATS_TEST_PASSWORD"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// stream makes a stream of three replicas taking subjects prefix.>, a
// new one each run, removed at the end, retried while the cluster elects
// its leaders.
func (n natsCluster) stream(t *testing.T, c *natswire.Client, prefix string) string {
	t.Helper()
	name := fmt.Sprintf("%s_%d", strings.ToUpper(prefix), time.Now().UnixNano())
	deadline := time.Now().Add(time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err := c.CreateStream(ctx, natswire.StreamConfig{Name: name, Subjects: []string{name + ".>"}, Storage: "file", Replicas: 3,
			Duplicates: int64(5 * time.Minute)})
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c.Request(ctx, natswire.APIPrefix+"STREAM.DELETE."+name, nil, nil)
	})
	return name
}

// natsRead reads a stream whole, through an ephemeral-like durable
// consumer of its own, the messages' data in order.
func natsRead(t *testing.T, c *natswire.Client, stream string) []string {
	t.Helper()
	ctx := hangGuard(t)
	cons := "read" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := c.CreateConsumer(ctx, stream, natswire.ConsumerConfig{Durable: cons, AckPolicy: "none", DeliverPolicy: "all"}); err != nil {
		t.Fatal(err)
	}
	defer c.DeleteConsumer(ctx, stream, cons)
	si, err := c.StreamInfo(ctx, stream)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for uint64(len(out)) < si.State.Msgs {
		msgs, err := c.Fetch(ctx, stream, cons, natswire.FetchRequest{Batch: 500, Expires: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			out = append(out, string(m.Data))
		}
	}
	return out
}

// TestNATSStreamAcrossKills copies a stream to another through the nats
// source and sink, killed at random points and started again from the
// checkpoint, while a node of the cluster restarts: the copy holds each
// message once, the duplicate window dropping what a replay sends again.
func TestNATSStreamAcrossKills(t *testing.T) {
	for _, n := range natsClusters(t) {
		t.Run(n.name, func(t *testing.T) {
			seed := uint64(time.Now().UnixNano())
			t.Logf("seed %d", seed)
			rnd := rand.New(rand.NewPCG(seed, 11))
			c := n.client(t)
			src, dst := n.stream(t, c, "src"), n.stream(t, c, "dst")
			const total = 20000
			ctx := hangGuard(t)
			var want []string
			for i := range total {
				d := "m" + strconv.Itoa(i)
				want = append(want, d)
				pa, err := c.PublishJS(src+".k"+strconv.Itoa(i%7), nil, []byte(d))
				if err != nil {
					t.Fatal(err)
				}
				if i%100 == 99 || i == total-1 {
					if _, err := pa.Wait(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					pa.Cancel()
				}
			}
			if si, err := c.StreamInfo(ctx, src); err != nil || si.State.Msgs != total {
				t.Fatal(si, err)
			}
			source := n.params(t, map[string]any{"stream": src, "consumer": "fit", "create_consumer": true, "ack_wait": "2s",
				"batch": 1 + rnd.IntN(300), "max_wait": "200ms"})
			sink := n.params(t, map[string]any{"subject": dst + ".{namespace}", "max_in_flight": 1 + rnd.IntN(200)})
			cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
				"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
				"source": map[string]any{"module": "nats", "params": source},
				"sinks": []any{map[string]any{"name": "n", "batch": map[string]any{"max_records": 1 + rnd.IntN(100), "max_wait": "1ms"},
					"sink": map[string]any{"module": "nats", "params": sink}}}})
			// A node restarts while the pipeline runs.
			restarted := make(chan error, 1)
			go func() {
				time.Sleep(time.Duration(200+rnd.IntN(800)) * time.Millisecond)
				node := n.containers[rnd.IntN(len(n.containers))]
				restarted <- exec.Command("docker", "restart", "-t", "1", node).Run()
			}()
			kills := runKilled(t, cfg, rnd, 12)
			if err := <-restarted; err != nil {
				t.Fatalf("docker restart: %v", err)
			}
			got := natsRead(t, c, dst)
			if len(got) != total {
				t.Fatalf("%d messages in the copy, not %d", len(got), total)
			}
			seen := map[string]bool{}
			for _, d := range got {
				seen[d] = true
			}
			for _, d := range want {
				if !seen[d] {
					t.Fatalf("%s is not in the copy", d)
				}
			}
			t.Logf("%d messages copied once across %d kills", total, kills)
		})
	}
}

// natsSink loads the sink nats of params.
func natsSink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Helper()
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "n", "sink": map[string]any{"module": "nats", "params": params}}}})
	m, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(fitsync.Sink)
}

// TestNATSBatchSentTwice writes a batch with the nats sink twice: the
// stream stores it once.
func TestNATSBatchSentTwice(t *testing.T) {
	for _, n := range natsClusters(t) {
		t.Run(n.name, func(t *testing.T) {
			c := n.client(t)
			dst := n.stream(t, c, "twice")
			s := natsSink(t, n.params(t, map[string]any{"subject": dst + ".{namespace}"}))
			batch := make([]*fitsync.Record, 50)
			for i := range batch {
				batch[i] = fitsync.GenerateRecord("gen", int64(i), 50, 5)
			}
			ctx := hangGuard(t)
			for range 2 {
				if err := s.Write(ctx, batch); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Write(ctx, batch[10:]); err != nil {
				t.Fatal(err)
			}
			si, err := c.StreamInfo(ctx, dst)
			if err != nil || si.State.Msgs != 50 {
				t.Fatal(si, err)
			}
		})
	}
}

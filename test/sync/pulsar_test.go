package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/pulsar"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
)

// pulsarServer is a Pulsar standalone the tests use, with deduplication
// on: its service URL and its admin (web service) URL, from
// FIT_SYNC_PULSAR_3_URL and FIT_SYNC_PULSAR_3_ADMIN, and the same of
// FIT_SYNC_PULSAR_4_, which test/sync/pulsar.sh prints.
type pulsarServer struct{ name, url, admin string }

func pulsarServers(t *testing.T) []pulsarServer {
	t.Helper()
	var out []pulsarServer
	for _, v := range []string{"3", "4"} {
		url, admin := os.Getenv("FIT_SYNC_PULSAR_"+v+"_URL"), os.Getenv("FIT_SYNC_PULSAR_"+v+"_ADMIN")
		if url == "" || admin == "" {
			continue
		}
		out = append(out, pulsarServer{name: "pulsar" + v, url: url, admin: admin})
	}
	if len(out) == 0 {
		// Required, one at least: a light run (the matrix's) starts one.
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("neither FIT_SYNC_PULSAR_3_URL nor FIT_SYNC_PULSAR_4_URL is set")
		}
		t.Skip("no Pulsar: FIT_SYNC_PULSAR_3_URL and FIT_SYNC_PULSAR_4_URL are not set (eval \"$(test/sync/pulsar.sh up)\" sets them)")
	}
	return out
}

// pulsarTopic makes a topic of public/default with n partitions (none when
// 0), named for the test, and returns its full name.
func pulsarTopic(t *testing.T, srv pulsarServer, n int) string {
	t.Helper()
	name := fmt.Sprintf("fit-sync-%d", rand.Uint64())
	path := "/admin/v2/persistent/public/default/" + name
	body := ""
	if n > 0 {
		path += "/partitions"
		body = strconv.Itoa(n)
	}
	req, _ := http.NewRequest(http.MethodPut, srv.admin+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("PUT %s: %s %s", path, resp.Status, b)
	}
	return "persistent://public/default/" + name
}

func pulsarClient(t *testing.T, srv pulsarServer) *pulsarwire.Client {
	t.Helper()
	c, err := pulsarwire.New(pulsarwire.Config{URL: srv.url, RequestTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// pulsarReadAll reads every message of a partition (or a topic that is
// not partitioned) as it is now.
func pulsarReadAll(t *testing.T, ctx context.Context, c *pulsarwire.Client, topic string) []pulsarwire.Message {
	t.Helper()
	cs, err := c.Subscribe(ctx, topic, pulsarwire.SubscribeOptions{Subscription: fmt.Sprintf("check-%d", rand.Uint64()), Start: &pulsarwire.EarliestID})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close(ctx)
	last, err := cs.LastMessageID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []pulsarwire.Message
	for last.Entry >= 0 {
		m, err := cs.Receive(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
		if m.ID.Ledger == last.Ledger && m.ID.Entry == last.Entry && (m.ID.BatchSize == 0 || m.ID.BatchIndex == m.ID.BatchSize-1) {
			break
		}
	}
	return out
}

func pulsarSend(t *testing.T, ctx context.Context, p *pulsarwire.Producer, msgs []pulsarwire.ProducerMessage) {
	t.Helper()
	ch, err := p.Send(msgs)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-ch:
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// TestPulsarProduceConsume sends batches in each compression and reads
// them back, as a reader and through a durable subscription acknowledged
// cumulatively.
func TestPulsarProduceConsume(t *testing.T) {
	for _, srv := range pulsarServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			ctx := hangGuard(t)
			c := pulsarClient(t, srv)
			topic := pulsarTopic(t, srv, 0)
			p, err := c.CreateProducer(ctx, topic, "", pulsarwire.None)
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, comp := range []pulsarwire.Compression{pulsarwire.None, pulsarwire.LZ4, pulsarwire.Zlib, pulsarwire.Zstd, pulsarwire.Snappy} {
				p.Close(ctx)
				if p, err = c.CreateProducer(ctx, topic, "", comp); err != nil {
					t.Fatal(err)
				}
				var msgs []pulsarwire.ProducerMessage
				for i := range 50 {
					v := fmt.Sprintf("%v-%d-%s", comp, i, strings.Repeat("x", i*10))
					want = append(want, v)
					msgs = append(msgs, pulsarwire.ProducerMessage{Key: []byte{byte(i), 0xff}, Payload: []byte(v), Properties: []pulsarwire.KeyValue{{Key: "i", Value: strconv.Itoa(i)}}})
				}
				pulsarSend(t, ctx, p, msgs)
			}
			got := pulsarReadAll(t, ctx, c, topic)
			if len(got) != len(want) {
				t.Fatalf("%d of %d", len(got), len(want))
			}
			for i, m := range got {
				if string(m.Payload) != want[i] || len(m.Key) != 2 || m.Key[1] != 0xff || m.Properties[0].Value != strconv.Itoa(i%50) {
					t.Fatalf("%d: %+v", i, m)
				}
			}
			cs, err := c.Subscribe(ctx, topic, pulsarwire.SubscribeOptions{Subscription: "durable", Durable: true, Initial: pulsarwire.Earliest})
			if err != nil {
				t.Fatal(err)
			}
			for range 75 {
				m, err := cs.Receive(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if m.ID.BatchIndex == m.ID.BatchSize-1 {
					cs.AckCumulative(m.ID)
				}
			}
			cs.Close(ctx)
			cs, err = c.Subscribe(ctx, topic, pulsarwire.SubscribeOptions{Subscription: "durable", Durable: true})
			if err != nil {
				t.Fatal(err)
			}
			if m, err := cs.Receive(ctx); err != nil || string(m.Payload) != want[50] {
				t.Fatalf("%v %s", err, m.Payload)
			}
		})
	}
}

// TestPulsarAcrossKills copies a partitioned topic of keyed messages, each
// key's numbered in order, to another through the pulsar source and sink,
// killed at random points and started again from the checkpoint, as a
// reader and through a subscription: every message reaches the copy, and
// each key's first copies come in their order. A replay may add copies
// after them: the source's positions are of every partition, as the
// interleaving of a run has them, so the fit-ids of a message read again
// differ, and the sink cannot tell it stored them (TestPulsarBatchTwice
// has a source whose positions repeat).
func TestPulsarAcrossKills(t *testing.T) {
	for _, srv := range pulsarServers(t) {
		for _, mode := range []string{"reader", "subscription"} {
			t.Run(srv.name+"/"+mode, func(t *testing.T) {
				seed := uint64(time.Now().UnixNano())
				t.Logf("seed %d", seed)
				rnd := rand.New(rand.NewPCG(seed, 13))
				ctx := hangGuard(t)
				c := pulsarClient(t, srv)
				src, dst := pulsarTopic(t, srv, 4), pulsarTopic(t, srv, 3)
				const keys, per = 40, 50
				producers := make([]*pulsarwire.Producer, 4)
				for i := range producers {
					var err error
					if producers[i], err = c.CreateProducer(ctx, fmt.Sprintf("%s-partition-%d", src, i), "", pulsarwire.LZ4); err != nil {
						t.Fatal(err)
					}
				}
				next := make([]int, keys)
				for sent := 0; sent < keys*per; {
					byPart := map[int][]pulsarwire.ProducerMessage{}
					for range 100 {
						k := rnd.IntN(keys)
						if next[k] == per {
							continue
						}
						byPart[k%4] = append(byPart[k%4], pulsarwire.ProducerMessage{Key: []byte("k" + strconv.Itoa(k)), Payload: []byte(strconv.Itoa(next[k]))})
						next[k]++
						sent++
					}
					for p, msgs := range byPart {
						pulsarSend(t, ctx, producers[p], msgs)
					}
				}
				source := map[string]any{"url": srv.url, "topics": []string{src}, "mode": mode}
				if mode == "subscription" {
					source["subscription"] = "fit-copy"
				}
				sink := map[string]any{"url": srv.url, "topic": dst, "producer_name": fmt.Sprintf("fit-%d", rnd.Uint64()),
					"compression": []string{"none", "lz4", "zlib", "zstd", "snappy"}[rnd.IntN(5)], "max_batch_messages": 1 + rnd.IntN(50)}
				cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
					"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
					"source": map[string]any{"module": "pulsar", "params": source},
					"sinks": []any{map[string]any{"name": "k", "batch": map[string]any{"max_records": 1 + rnd.IntN(100), "max_wait": "1ms"},
						"sink": map[string]any{"module": "pulsar", "params": sink}}}})
				kills := runKilled(t, cfg, rnd, 10)

				got := map[string][]int{}
				total := 0
				for p := range 3 {
					for _, m := range pulsarReadAll(t, ctx, c, fmt.Sprintf("%s-partition-%d", dst, p)) {
						n, _ := strconv.Atoi(string(m.Payload))
						got[string(m.Key)] = append(got[string(m.Key)], n)
						total++
					}
				}
				for k := range keys {
					seq := got["k"+strconv.Itoa(k)]
					want := 0
					for _, n := range seq {
						if n == want {
							want++
						} else if n > want {
							t.Fatalf("key k%d: %d came before %d: %v", k, n, want, seq)
						}
					}
					if want != per {
						t.Fatalf("key k%d: %d of %d messages: %v", k, want, per, seq)
					}
				}
				t.Logf("%d messages copied as %d across %d kills", keys*per, total, kills)
			})
		}
	}
}

// TestPulsarBatchTwice writes a batch with the pulsar sink, then the same
// batch again with a sink made anew, as after a crash: it is stored once.
// With dedup off it is stored twice.
func TestPulsarBatchTwice(t *testing.T) {
	for _, srv := range pulsarServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			ctx := hangGuard(t)
			c := pulsarClient(t, srv)
			topic := pulsarTopic(t, srv, 2)
			var batch []*fitsync.Record
			for n := range int64(300) {
				batch = append(batch, fitsync.GenerateRecord("g", n, 300, 3))
			}
			write := func(params map[string]any) {
				b, _ := json.Marshal(params)
				v, err := config.ParseValue(b, "params")
				if err != nil {
					t.Fatal(err)
				}
				m, err := module.Load(fitsync.Sinks, config.ModuleRef{Module: "pulsar", Params: v}, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer module.Cleanup(m)
				if err := m.(fitsync.Sink).Write(ctx, batch); err != nil {
					t.Fatal(err)
				}
			}
			count := func() int {
				n := 0
				for p := range 2 {
					n += len(pulsarReadAll(t, ctx, c, fmt.Sprintf("%s-partition-%d", topic, p)))
				}
				return n
			}
			params := map[string]any{"url": srv.url, "topic": topic, "codec": map[string]any{"module": "json"}, "max_batch_messages": 40}
			write(params)
			write(params)
			if n := count(); n != len(batch) {
				t.Fatalf("%d messages of %d", n, len(batch))
			}
			params["dedup"], params["producer_name"] = "off", "other"
			write(params)
			if n := count(); n != 2*len(batch) {
				t.Fatalf("%d messages of %d", n, 2*len(batch))
			}
		})
	}
}

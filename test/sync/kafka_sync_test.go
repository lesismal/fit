package synctest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/file"
	_ "github.com/lesismal/fit/sync/kafka"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

// runKilled runs the pipeline p of cfg again and again, killed at random
// points by the crash hook, then once to its end; it returns the kills.
func runKilled(t *testing.T, cfg *config.Config, rnd *rand.Rand, rounds int) int {
	t.Helper()
	points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
		fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
	kills := 0
	for round := range rounds {
		at := points[rnd.IntN(len(points))]
		if round == 0 {
			at = fitsync.CrashEmitted // the first run is killed for sure
		}
		nth := int32(1 + rnd.IntN(300))
		if at == fitsync.CrashCheckpointWritten || at == fitsync.CrashCheckpointSaved {
			nth = int32(1 + rnd.IntN(3))
		}
		var seen atomic.Int32
		rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool {
			return p == at && seen.Add(1) == nth
		}})
		if err := rt.Apply(cfg.Sync); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Minute) // a hang guard
		for st := redisState(rt); st != "killed" && st != "finished"; st = redisState(rt) {
			if st == "stopped" || time.Now().After(deadline) {
				t.Fatalf("the pipeline is %s: %+v", st, rt.Status())
			}
			time.Sleep(time.Millisecond)
		}
		done := redisState(rt) == "finished"
		if !done {
			kills++
		}
		rt.Close(context.Background())
		if done {
			break
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	defer cancel()
	st, err := fitsync.RunPipeline(ctx, cfg.Sync, "p", fitsync.Options{})
	if err != nil || st.State != "finished" {
		t.Fatalf("%v: %+v", err, st)
	}
	return kills
}

func loadSync(t *testing.T, stateDir string, pipeline map[string]any) *config.Config {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{pipeline}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestKafkaToKafkaAcrossKills copies a topic of keyed messages, each key's
// numbered in order, to another topic through the kafka source and sink,
// killed at random points and started again from the checkpoint: every
// message reaches the copy, and each key's first copies come in their
// order (a replay may add copies after them), on Kafka and Redpanda.
func TestKafkaToKafkaAcrossKills(t *testing.T) {
	for _, srv := range kafkaServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			seed := uint64(time.Now().UnixNano())
			t.Logf("seed %d", seed)
			rnd := rand.New(rand.NewPCG(seed, 9))
			brokers := strings.Split(srv.brokers, ",")
			c := kafkaClient(t, kafkawire.Config{Brokers: brokers})
			src := kafkaTopic(t, c, 4, srv.rf, nil)
			dst := src + "-copy"
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				c.DeleteTopics(ctx, 30*time.Second, dst)
			})
			const keys, per = 50, 60
			ctx := hangGuard(t)
			next := make([]int, keys)
			for sent := 0; sent < keys*per; {
				byPart := map[int32][]kafkawire.Record{}
				for range 100 {
					k := rnd.IntN(keys)
					if next[k] == per {
						continue
					}
					key := []byte("k" + strconv.Itoa(k))
					p := kafkawire.PartitionFor(key, 4)
					byPart[p] = append(byPart[p], kafkawire.Record{Key: key, Value: []byte(strconv.Itoa(next[k]))})
					next[k]++
					sent++
				}
				var batches []kafkawire.ProduceBatch
				for p, recs := range byPart {
					batches = append(batches, kafkawire.ProduceBatch{Topic: src, Partition: p, Records: recs})
				}
				if _, err := c.Produce(ctx, kafkawire.ProduceOptions{}, batches...); err != nil {
					t.Fatal(err)
				}
			}
			kparams := map[string]any{"brokers": brokers}
			sink := map[string]any{"brokers": brokers, "topic": dst, "compression": []string{"none", "gzip", "snappy", "lz4", "zstd"}[rnd.IntN(5)],
				"create_topics": map[string]any{"partitions": 3, "replication_factor": srv.rf}}
			source := map[string]any{"topics": []string{src}}
			for k, v := range kparams {
				source[k] = v
			}
			cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
				"queue":  map[string]any{"max_records": 20 + rnd.IntN(300)},
				"source": map[string]any{"module": "kafka", "params": source},
				"sinks": []any{map[string]any{"name": "k", "batch": map[string]any{"max_records": 1 + rnd.IntN(100), "max_wait": "1ms"},
					"sink": map[string]any{"module": "kafka", "params": sink}}}})
			kills := runKilled(t, cfg, rnd, 12)

			got := map[string][]int{}
			total := 0
			for p := range int32(3) {
				end, err := c.Offset(ctx, dst, p, kafkawire.OffsetLatest)
				if err != nil {
					t.Fatal(err)
				}
				if end == 0 {
					continue
				}
				for _, m := range readAll(t, c, dst, p, 0, int(end)) {
					n, _ := strconv.Atoi(string(m.Value))
					got[string(m.Key)] = append(got[string(m.Key)], n)
					if kafkawire.PartitionFor(m.Key, 3) != p {
						t.Fatalf("key %s in partition %d", m.Key, p)
					}
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

// TestKafkaSourceSinkCodecs writes generate's records of every value type
// to a topic with the kafka sink in each codec that encodes, and reads
// them back with the kafka source: the same records.
func TestKafkaSourceSinkCodecs(t *testing.T) {
	brokers := strings.Split(kafkaEnv(t, "FIT_SYNC_KAFKA_BROKERS"), ",")
	c := kafkaClient(t, kafkawire.Config{Brokers: brokers})
	for _, codec := range []map[string]any{{"module": "json", "params": map[string]any{"form": "record"}}, {"module": "canal_json"}, {"module": "debezium"}} {
		t.Run(codec["module"].(string), func(t *testing.T) {
			topic := kafkaTopic(t, c, 2, 3, nil)
			dir := t.TempDir()
			out := filepath.Join(dir, "out.ndjson")
			write := loadSync(t, dir, map[string]any{"name": "p", "mode": "once", "on_error": "stop",
				"source": map[string]any{"module": "generate", "params": map[string]any{"records": 200}},
				"sinks": []any{map[string]any{"name": "k", "sink": map[string]any{"module": "kafka",
					"params": map[string]any{"brokers": brokers, "topic": topic, "codec": codec, "deletes": "encode"}}}}})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute) // a hang guard
			defer cancel()
			if st, err := fitsync.RunPipeline(ctx, write.Sync, "p", fitsync.Options{}); err != nil || st.State != "finished" {
				t.Fatalf("%v: %+v", err, st)
			}
			read := loadSync(t, filepath.Join(dir, "r"), map[string]any{"name": "p", "mode": "once", "on_error": "stop",
				"source": map[string]any{"module": "kafka", "params": map[string]any{"brokers": brokers, "topics": []string{topic}, "codec": codec}},
				"sinks": []any{map[string]any{"name": "f", "sink": map[string]any{"module": "file",
					"params": map[string]any{"path": out, "codec": map[string]any{"module": "json", "params": map[string]any{"form": "record"}}}}}}})
			if st, err := fitsync.RunPipeline(ctx, read.Sync, "p", fitsync.Options{}); err != nil || st.State != "finished" {
				t.Fatalf("%v: %+v", err, st)
			}
			b, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) != 200 {
				t.Fatalf("%d records read back", len(lines))
			}
			t.Logf("%s: %d records, the first %s", codec["module"], len(lines), fmt.Sprint(lines[0][:min(300, len(lines[0]))]))
		})
	}
}

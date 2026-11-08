package synctest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

// kafkaEnv returns the variable, skipping the test when it is unset
// (test/sync/kafka.sh prints them; test/sync/run.sh sets them).
func kafkaEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatalf("%s is not set", name)
		}
		t.Skipf("no Kafka: %s is not set (test/sync/kafka.sh prints it)", name)
	}
	return v
}

func kafkaClient(t *testing.T, cfg kafkawire.Config) *kafkawire.Client {
	t.Helper()
	c, err := kafkawire.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func kafkaTopic(t *testing.T, c *kafkawire.Client, partitions int32, rf int16, configs map[string]string) string {
	t.Helper()
	name := fmt.Sprintf("fit-%s-%d", strings.NewReplacer("/", "-", "_", "-").Replace(t.Name()), time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := c.CreateTopics(ctx, 30*time.Second, kafkawire.TopicSpec{Name: name, Partitions: partitions, ReplicationFactor: rf, Configs: configs}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.DeleteTopics(ctx, 30*time.Second, name)
	})
	return name
}

// readAll fetches a partition from offset until it has n records.
func readAll(t *testing.T, c *kafkawire.Client, topic string, partition int32, offset int64, n int) []kafkawire.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out []kafkawire.Record
	for len(out) < n {
		res, err := c.Fetch(ctx, kafkawire.FetchOptions{MaxWait: 200 * time.Millisecond, PartitionMaxBytes: 64 << 10},
			kafkawire.FetchPartition{Topic: topic, Partition: partition, Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, res[0].Records...)
		offset = res[0].NextOffset
	}
	return out
}

type kafkaServer struct {
	name     string
	brokers  string
	rf       int16
	recompat bool // the broker writes a topic's compression.type
}

func kafkaServers(t *testing.T) []kafkaServer {
	var s []kafkaServer
	if b := os.Getenv("FIT_SYNC_KAFKA_BROKERS"); b != "" {
		s = append(s, kafkaServer{"kafka", b, 3, true})
	}
	if b := os.Getenv("FIT_SYNC_REDPANDA_BROKERS"); b != "" {
		s = append(s, kafkaServer{"redpanda", b, 1, false})
	}
	if len(s) == 0 {
		kafkaEnv(t, "FIT_SYNC_KAFKA_BROKERS")
	}
	return s
}

var kafkaCodecs = []kafkawire.Compression{kafkawire.None, kafkawire.Gzip, kafkawire.Snappy, kafkawire.LZ4, kafkawire.Zstd}

// TestKafkaProduceFetch writes and reads every codec on Kafka and Redpanda:
// batches of each codec to three partitions, read back whole, with the
// offsets ListOffsets gives. On Kafka, each codec is also a topic's
// compression.type, so the broker writes it from batches of another
// codec and the client reads what the broker wrote.
func TestKafkaProduceFetch(t *testing.T) {
	for _, srv := range kafkaServers(t) {
		t.Run(srv.name, func(t *testing.T) {
			c := kafkaClient(t, kafkawire.Config{Brokers: strings.Split(srv.brokers, ",")})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			m, err := c.Metadata(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(m.Brokers) != int(srv.rf) {
				t.Fatalf("%d brokers", len(m.Brokers))
			}
			for _, topicCodec := range kafkaCodecs {
				cfg := map[string]string{}
				if srv.recompat && topicCodec != kafkawire.None {
					cfg["compression.type"] = topicCodec.String()
				}
				topic := kafkaTopic(t, c, 3, srv.rf, cfg)
				if n, err := c.Partitions(ctx, topic); err != nil || n != 3 {
					t.Fatalf("%d partitions, %v", n, err)
				}
				var want [3][]string
				for round, codec := range kafkaCodecs {
					var batches []kafkawire.ProduceBatch
					for p := range int32(3) {
						var recs []kafkawire.Record
						for i := range 200 {
							v := fmt.Sprintf("%s/%s/%d/%d %s", topicCodec, codec, p, i, strings.Repeat("x", i%50))
							recs = append(recs, kafkawire.Record{Key: []byte(strconv.Itoa(i)), Value: []byte(v),
								Headers: []kafkawire.Header{{Key: "round", Value: []byte(strconv.Itoa(round))}}})
							want[p] = append(want[p], v)
						}
						batches = append(batches, kafkawire.ProduceBatch{Topic: topic, Partition: p, Records: recs})
					}
					res, err := c.Produce(ctx, kafkawire.ProduceOptions{Compression: codec}, batches...)
					if err != nil {
						t.Fatalf("produce %v into a %v topic: %v", codec, topicCodec, err)
					}
					for p, r := range res {
						if r.BaseOffset != int64(round*200) {
							t.Fatalf("partition %d: base offset %d, want %d", p, r.BaseOffset, round*200)
						}
					}
				}
				for p := range int32(3) {
					got := readAll(t, c, topic, p, 0, len(want[p]))
					if len(got) != len(want[p]) {
						t.Fatalf("partition %d: %d records, want %d", p, len(got), len(want[p]))
					}
					for i, r := range got {
						if string(r.Value) != want[p][i] || r.Offset != int64(i) || string(r.Key) != strconv.Itoa(i%200) ||
							len(r.Headers) != 1 || string(r.Headers[0].Value) != strconv.Itoa(i/200) {
							t.Fatalf("%v topic, partition %d, record %d: %q at %d", topicCodec, p, i, r.Value, r.Offset)
						}
					}
					res, err := c.ListOffsets(ctx, false,
						kafkawire.OffsetQuery{Topic: topic, Partition: p, Timestamp: kafkawire.OffsetEarliest})
					if err != nil || res[0].Offset != 0 {
						t.Fatalf("earliest %+v, %v", res, err)
					}
					if off, err := c.Offset(ctx, topic, p, kafkawire.OffsetLatest); err != nil || off != int64(len(want[p])) {
						t.Fatalf("latest %d, %v", off, err)
					}
					if off, err := c.OffsetAt(ctx, topic, p, time.Now().Add(-time.Hour)); err != nil || off != 0 {
						t.Fatalf("an hour ago %d, %v", off, err)
					}
				}
				// Past the end: OFFSET_OUT_OF_RANGE, for the caller.
				res, err := c.Fetch(ctx, kafkawire.FetchOptions{}, kafkawire.FetchPartition{Topic: topic, Partition: 0, Offset: 1 << 40})
				if !errors.Is(err, kafkawire.ErrOffsetOutOfRange) || !errors.Is(res[0].Err, kafkawire.ErrOffsetOutOfRange) {
					t.Fatalf("past the end: %v", err)
				}
			}
			// A topic that does not exist: retried, then refused.
			short := kafkaClient(t, kafkawire.Config{Brokers: strings.Split(srv.brokers, ","), RetryFor: time.Second})
			if _, err := short.ProduceRecords(ctx, kafkawire.ProduceOptions{}, "fit-no-such-topic", 0, kafkawire.Record{Value: []byte("x")}); !errors.Is(err, kafkawire.ErrUnknownTopicOrPartition) {
				t.Fatalf("no such topic: %v", err)
			}
		})
	}
}

// TestKafkaSASLAndTLS authenticates with each mechanism, over SASL_PLAINTEXT
// and over SASL_SSL with the CA, and with a wrong password.
func TestKafkaSASLAndTLS(t *testing.T) {
	sasl := kafkaEnv(t, "FIT_SYNC_KAFKA_SASL_BROKERS")
	user, password := kafkaEnv(t, "FIT_SYNC_KAFKA_USER"), kafkaEnv(t, "FIT_SYNC_KAFKA_PASSWORD")
	pem, err := os.ReadFile(kafkaEnv(t, "FIT_SYNC_KAFKA_CA"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	type tcase struct {
		name, brokers, mech string
		tls                 bool
	}
	cases := []tcase{
		{"plain", sasl, kafkawire.SASLPlain, false},
		{"scram256", sasl, kafkawire.SASLScramSHA256, false},
		{"scram512", sasl, kafkawire.SASLScramSHA512, false},
		{"tls-scram512", kafkaEnv(t, "FIT_SYNC_KAFKA_TLS_BROKERS"), kafkawire.SASLScramSHA512, true},
		{"tls-plain", kafkaEnv(t, "FIT_SYNC_KAFKA_TLS_BROKERS"), kafkawire.SASLPlain, true},
	}
	if rp := os.Getenv("FIT_SYNC_REDPANDA_SASL_BROKERS"); rp != "" {
		cases = append(cases, tcase{"redpanda-scram256", rp, kafkawire.SASLScramSHA256, false},
			tcase{"redpanda-plain", rp, kafkawire.SASLPlain, false})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := kafkawire.Config{Brokers: strings.Split(tc.brokers, ","),
				SASL: &kafkawire.SASL{Mechanism: tc.mech, Username: user, Password: password}}
			if tc.tls {
				cfg.TLS = &tls.Config{RootCAs: pool}
			}
			c := kafkaClient(t, cfg)
			rf := int16(3)
			if strings.HasPrefix(tc.name, "redpanda") {
				rf = 1
			}
			topic := kafkaTopic(t, c, 1, rf, nil)
			off, err := c.ProduceRecords(ctx, kafkawire.ProduceOptions{Compression: kafkawire.Zstd}, topic, 0,
				kafkawire.Record{Key: []byte("k"), Value: []byte(tc.name)})
			if err != nil || off != 0 {
				t.Fatalf("produce: %d, %v", off, err)
			}
			if got := readAll(t, c, topic, 0, 0, 1); string(got[0].Value) != tc.name {
				t.Fatalf("%q", got[0].Value)
			}
			cfg.SASL = &kafkawire.SASL{Mechanism: tc.mech, Username: user, Password: "wrong"}
			bad := kafkaClient(t, cfg)
			if _, err := bad.Metadata(ctx); !errors.Is(err, kafkawire.ErrSASLAuthenticationFailed) {
				t.Fatalf("a wrong password: %v", err)
			}
		})
	}
	// TLS checks the certificate: no CA, no connection.
	c := kafkaClient(t, kafkawire.Config{Brokers: strings.Split(kafkaEnv(t, "FIT_SYNC_KAFKA_TLS_BROKERS"), ","),
		TLS: &tls.Config{}, SASL: &kafkawire.SASL{Mechanism: kafkawire.SASLPlain, Username: user, Password: password}})
	var x509Err *tls.CertificateVerificationError
	if _, err := c.Metadata(ctx); !errors.As(err, &x509Err) {
		t.Fatalf("an unknown CA: %v", err)
	}
}

// TestKafkaLeaderChange produces and fetches without a pause while the
// partitions' leaders are stopped and started again, one after another:
// every record acknowledged is read, in order per partition, and the
// reader follows each new leader. A produce retried after a lost answer may
// write its records twice, which the check allows (at least once).
func TestKafkaLeaderChange(t *testing.T) {
	brokers := kafkaEnv(t, "FIT_SYNC_KAFKA_BROKERS")
	containers := map[int32]string{}
	for _, kv := range strings.Split(kafkaEnv(t, "FIT_SYNC_KAFKA_CONTAINERS"), ",") {
		k, v, _ := strings.Cut(kv, "=")
		id, _ := strconv.Atoi(k)
		containers[int32(id)] = v
	}
	cfg := kafkawire.Config{Brokers: strings.Split(brokers, ","), RetryFor: 2 * time.Minute, RequestTimeout: 15 * time.Second}
	c := kafkaClient(t, cfg)
	const parts = 2
	topic := kafkaTopic(t, c, parts, 3, map[string]string{"min.insync.replicas": "2"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var acked [parts][]int // sequence numbers acknowledged, in order
	var mu sync.Mutex
	stop := make(chan struct{})
	var produced atomic.Int64
	var wg sync.WaitGroup
	var prodErr, fetchErr error
	var failed atomic.Bool
	wg.Add(1)
	go func() { // the producer: batches of 50 to both partitions
		defer wg.Done()
		p := kafkaClient(t, cfg)
		seq := 0
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			var batches []kafkawire.ProduceBatch
			var seqs [parts][]int
			for part := range int32(parts) {
				var recs []kafkawire.Record
				for range 50 {
					recs = append(recs, kafkawire.Record{Value: []byte(strconv.Itoa(seq))})
					seqs[part] = append(seqs[part], seq)
					seq++
				}
				batches = append(batches, kafkawire.ProduceBatch{Topic: topic, Partition: part, Records: recs})
			}
			codec := kafkaCodecs[i%len(kafkaCodecs)]
			res, err := p.Produce(ctx, kafkawire.ProduceOptions{Compression: codec}, batches...)
			mu.Lock()
			for part, r := range res {
				if r.Err == nil {
					acked[part] = append(acked[part], seqs[part]...)
				}
			}
			mu.Unlock()
			if err != nil {
				mu.Lock()
				prodErr = err
				mu.Unlock()
				failed.Store(true)
				return
			}
			produced.Add(1)
		}
	}()
	var read [parts][]int
	readOffsets := [parts]int64{}
	wg.Add(1)
	go func() { // the reader, on a client of its own
		defer wg.Done()
		f := kafkaClient(t, cfg)
		for {
			select {
			case <-stop:
				return
			default:
			}
			var fp []kafkawire.FetchPartition
			for part := range int32(parts) {
				fp = append(fp, kafkawire.FetchPartition{Topic: topic, Partition: part, Offset: readOffsets[part]})
			}
			res, err := f.Fetch(ctx, kafkawire.FetchOptions{MaxWait: 100 * time.Millisecond}, fp...)
			if err != nil {
				mu.Lock()
				fetchErr = err
				mu.Unlock()
				failed.Store(true)
				return
			}
			for part, r := range res {
				for _, rec := range r.Records {
					n, _ := strconv.Atoi(string(rec.Value))
					read[part] = append(read[part], n)
				}
				readOffsets[part] = r.NextOffset
			}
		}
	}()

	// Stop each partition's leader in turn (a graceful stop, then a kill),
	// and start it again, with records flowing all along.
	waitProduced := func(n int64) {
		target := produced.Load() + n
		for produced.Load() < target && !failed.Load() {
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitProduced(5)
	for round, how := range []string{"stop", "kill"} {
		m, err := c.Metadata(ctx, topic)
		if err != nil {
			t.Fatal(err)
		}
		leader := m.Topic(topic).Partitions[round%parts].Leader
		name := containers[leader]
		t.Logf("%s the leader of partition %d, broker %d (%s)", how, round%parts, leader, name)
		if out, err := exec.Command("docker", how, name).CombinedOutput(); err != nil {
			t.Fatalf("docker %s %s: %v %s", how, name, err, out)
		}
		waitProduced(20)
		if out, err := exec.Command("docker", "start", name).CombinedOutput(); err != nil {
			t.Fatalf("docker start %s: %v %s", name, err, out)
		}
		// Wait for it to be back among the in-sync replicas.
		for {
			m, err := c.Metadata(ctx, topic)
			if err == nil && len(m.Topic(topic).Partitions[round%parts].ISR) == 3 {
				break
			}
			if ctx.Err() != nil {
				t.Fatal("the broker did not come back in sync")
			}
			time.Sleep(time.Second)
		}
		waitProduced(10)
	}
	close(stop)
	wg.Wait()
	if prodErr != nil {
		t.Fatalf("produce: %v", prodErr)
	}
	if fetchErr != nil {
		t.Fatalf("fetch: %v", fetchErr)
	}
	// Read the rest, then check every acknowledged record is there, the
	// first copies in order.
	for part := range int32(parts) {
		end, err := c.Offset(ctx, topic, part, kafkawire.OffsetLatest)
		if err != nil {
			t.Fatal(err)
		}
		if n := int(end - readOffsets[part]); n > 0 {
			for _, r := range readAll(t, c, topic, part, readOffsets[part], n) {
				v, _ := strconv.Atoi(string(r.Value))
				read[part] = append(read[part], v)
			}
		}
		seen := map[int]bool{}
		var firsts []int
		for _, v := range read[part] {
			if !seen[v] {
				seen[v] = true
				firsts = append(firsts, v)
			}
		}
		for _, v := range acked[part] {
			if !seen[v] {
				t.Fatalf("partition %d: record %d acknowledged and lost", part, v)
			}
		}
		for i := 1; i < len(firsts); i++ {
			if firsts[i] < firsts[i-1] {
				t.Fatalf("partition %d: %d after %d", part, firsts[i], firsts[i-1])
			}
		}
		t.Logf("partition %d: %d acknowledged, %d read (%d duplicates)", part, len(acked[part]), len(read[part]), len(read[part])-len(firsts))
	}
}

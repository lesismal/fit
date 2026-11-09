package synckafka

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

func msg(key, value string) kafkawire.Record {
	r := kafkawire.Record{Value: []byte(value), Timestamp: time.UnixMilli(1000)}
	if key != "" {
		r.Key = []byte(key)
	}
	return r
}

func TestProvision(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, []byte("not a certificate"), 0o644)
	t.Setenv("FIT_KAFKA_TEST_PW", "secret")
	bad := []map[string]any{
		{"brokers": []string{"nohost"}, "topics": []string{"a"}},
		{"brokers": []string{}, "topics": []string{"a"}},
		{"brokers": []string{"b:1"}, "topics": []string{"a", "a", "bad topic"}},
		{"brokers": []string{"b:1"}, "topics": []string{}},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "username": "u"},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "sasl": "GSSAPI"},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "sasl": "PLAIN"},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "sasl": "PLAIN", "username": "u", "password_env": "1bad"},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "sasl": "PLAIN", "username": "u", "password_env": "FIT_KAFKA_TEST_UNSET"},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "ca_file": ca},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "tls": true, "ca_file": ca},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "tls": true, "ca_file": filepath.Join(dir, "none")},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "tls": true, "cert_file": ca},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "tls": true, "cert_file": ca, "key_file": ca},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "codec": map[string]any{"module": "nope"}},
		{"brokers": []string{"b:1"}, "topics": []string{"a"}, "codec": map[string]any{"module": "canal_json", "params": map[string]any{"time_zone": "No/Zone"}}},
	}
	for _, p := range bad {
		if _, err := load(t, fitsync.Sources, "kafka", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	s := mustLoad(t, fitsync.Sources, "kafka", map[string]any{"brokers": []string{"b:1"}, "topics": []string{"a"}, "sasl": "SCRAM-SHA-512",
		"username": "u", "password_env": "FIT_KAFKA_TEST_PW", "tls": true, "server_name": "k", "timeout": "5s", "client_id": "me"}).(*Source)
	if s.cfg.SASL.Password != "secret" || s.cfg.TLS.ServerName != "k" || s.cfg.RequestTimeout != 5*time.Second || s.cfg.ClientID != "me" {
		t.Fatalf("%+v", s.cfg)
	}
	for _, p := range []map[string]any{
		{"topic": "{other}"}, {"topic": "bad topic"}, {"compression": "brotli"},
		{"create_topics": map[string]any{"replication_factor": -1}},
		{"codec": map[string]any{"module": "nope"}},
	} {
		p["brokers"] = []string{"b:1"}
		if _, err := load(t, fitsync.Sinks, "kafka", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	for acks, want := range map[string]kafkawire.Acks{"all": kafkawire.AcksAll, "leader": kafkawire.AcksLeader, "none": kafkawire.AcksNone} {
		k := mustLoad(t, fitsync.Sinks, "kafka", map[string]any{"brokers": []string{"b:1"}, "acks": acks, "compression": "zstd", "topic": "x.{namespace}"}).(*Sink)
		if k.opts.Acks != want || k.opts.Compression != kafkawire.Zstd {
			t.Fatalf("%+v", k.opts)
		}
	}
	// The real client is made of the config.
	h := holder{cfg: s.cfg}
	c, err := h.get()
	if err != nil {
		t.Fatal(err)
	}
	if c2, _ := h.get(); c2 != c {
		t.Fatal("a second client")
	}
	h.close()
	h.close()
	h = holder{cfg: kafkawire.Config{}}
	if _, err := h.get(); err == nil {
		t.Fatal("a client of no brokers")
	}
}

func TestSourceStream(t *testing.T) {
	f := newFake()
	f.addTopic("a", 2)
	f.addTopic("b", 1)
	for i := range 5 {
		f.append("a", int32(i%2), msg("k"+string(rune('0'+i)), "v"))
	}
	f.append("a", 0, kafkawire.Record{Key: []byte("gone")}, kafkawire.Record{}, kafkawire.Record{Key: []byte{0xff}, Value: []byte("x")})
	f.append("b", 0, msg("", "b0"))
	s := source(t, f, map[string]any{"topics": []string{"a", "b"}})
	recs, err := collect(t, s, nil, false, 8)
	if err != nil {
		t.Fatal(err)
	}
	var del, bin int
	for _, r := range recs {
		if r.Source != "kafka" {
			t.Fatalf("%+v", r)
		}
		if r.Op == fitsync.OpDelete {
			del++
			if r.Key.Text() != "gone" || r.Namespace != "a" {
				t.Fatalf("%+v", r)
			}
		}
		if r.Key.Kind() == fitsync.KindBytes {
			bin++
		}
	}
	if del != 1 || bin != 1 {
		t.Fatalf("%d deletes, %d binary keys", del, bin)
	}
	var last position
	for _, r := range recs {
		if r.Position != nil {
			json.Unmarshal(r.Position, &last)
		}
	}
	if last.Offsets["a"]["0"] != 6 || last.Offsets["a"]["1"] != 2 || last.Offsets["b"]["0"] != 1 {
		t.Fatalf("%+v", last)
	}
	// From the checkpoint: nothing more, then one added.
	from, _ := json.Marshal(last)
	f.append("a", 1, msg("n", "new"))
	recs, err = collect(t, s, from, false, 1)
	if err != nil || len(recs) != 1 || recs[0].Key.Text() != "n" {
		t.Fatalf("%v %+v", err, recs)
	}
	if f.closed != 0 {
		t.Fatal("closed while held")
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceOnce(t *testing.T) {
	f := newFake()
	f.addTopic("a", 3)
	for i := range 10 {
		f.append("a", int32(i%3), msg("", "v"))
	}
	f.onFetch = func(n int) {
		if n == 1 {
			f.append("a", 0, msg("", "after the end"))
		}
	}
	s := source(t, f, map[string]any{"topics": []string{"a"}, "tombstones": "skip"})
	recs, err := collect(t, s, nil, true, 0)
	if err != nil || len(recs) != 10 {
		t.Fatalf("%v: %d records", err, len(recs))
	}
	// Start latest: only what comes after.
	s2 := source(t, f, map[string]any{"topics": []string{"a"}, "start": "latest", "namespace": "ns", "read_uncommitted": true,
		"max_wait": "10ms", "partition_max_bytes": 100})
	f.onFetch = func(n int) {
		if n == 3 {
			f.append("a", 2, msg("", "late"), kafkawire.Record{Key: []byte("tomb")})
		}
	}
	recs, err = collect(t, s2, nil, false, 1)
	if err != nil || recs[0].Namespace != "ns" || recs[0].After.Fields()[0].Value.Text() != "late" {
		t.Fatalf("%v %+v", err, recs)
	}
	o := s2.fetchOptions()
	if o.MaxWait != 10*time.Millisecond || o.PartitionMaxBytes != 100 || o.ReadCommitted {
		t.Fatalf("%+v", o)
	}
}

func TestSourceDecode(t *testing.T) {
	f := newFake()
	f.addTopic("cdc", 1)
	canal := `{"database":"d","table":"t","pkNames":["id"],"type":"INSERT","mysqlType":{"id":"int"},"data":[{"id":"1"},{"id":"2"}]}`
	f.append("cdc", 0, msg("", canal), msg("", "not json"), msg("k", `{"type":"TIDB_WATERMARK"}`))
	s := source(t, f, map[string]any{"topics": []string{"cdc"}, "codec": map[string]any{"module": "canal_json"}})
	recs, err := collect(t, s, nil, true, 0)
	if err != nil || len(recs) != 3 {
		t.Fatalf("%v %d", err, len(recs))
	}
	if recs[0].Tx.ID != "cdc/0@0" || !recs[0].Tx.First || recs[0].Tx.Last || !recs[1].Tx.Last || recs[0].Namespace != "d.t" || recs[1].Position == nil {
		t.Fatalf("%+v %+v", recs[0], recs[1])
	}
	if e, _ := recs[2].After.Get("error"); e.Text() == "" || recs[2].Op != fitsync.OpMessage {
		t.Fatalf("%+v", recs[2])
	}
	// Each record is named by its topic, partition, offset and place in
	// its message, whatever the position.
	if o := (fitsync.Origin{Stream: "cdc", N: [4]uint64{0, 0, 1}}); recs[1].Origin != o || recs[2].Origin.N[1] != 1 || recs[2].Origin.N[2] != 0 {
		t.Fatalf("%+v %+v", recs[1].Origin, recs[2].Origin)
	}
	// Debezium's key events give the key.
	f.addTopic("dbz", 1)
	f.append("dbz", 0, msg(`{"id":7}`, `{"op":"c","after":{"id":7},"source":{"db":"d","table":"t"}}`),
		msg(`[`, `{"op":"c","after":{"id":8},"source":{"db":"d","table":"t"}}`))
	s = source(t, f, map[string]any{"topics": []string{"dbz"}, "codec": map[string]any{"module": "debezium"}})
	recs, _ = collect(t, s, nil, true, 0)
	if js(t, recs[0].Key) != `{"map":{"id":{"int":7}}}` || recs[1].Key.Text() != "[" {
		t.Fatalf("%s %s", js(t, recs[0].Key), js(t, recs[1].Key))
	}
	s.SetCodec(&Raw{})
}

func TestSourceErrors(t *testing.T) {
	f := newFake()
	f.addTopic("a", 1)
	f.append("a", 0, msg("", "0"), msg("", "1"), msg("", "2"))
	f.start["a"][0] = 2
	from := fitsync.Position(`{"offsets":{"a":{"0":1}}}`)
	s := source(t, f, map[string]any{"topics": []string{"a"}})
	if _, err := collect(t, s, from, true, 0); err == nil || !strings.Contains(err.Error(), "offset_reset") {
		t.Fatalf("%v", err)
	}
	for reset, want := range map[string]int{"earliest": 1, "latest": 0} {
		s := source(t, f, map[string]any{"topics": []string{"a"}, "offset_reset": reset})
		recs, err := collect(t, s, from, true, 0)
		if err != nil || len(recs) != want {
			t.Fatalf("%s: %v %d", reset, err, len(recs))
		}
	}
	if _, err := collect(t, s, fitsync.Position(`[`), true, 0); err == nil {
		t.Fatal("a bad checkpoint read")
	}
	f.fetchErr = func(string, int32, int64) error { return kafkawire.ErrNotLeaderOrFollower }
	if _, err := collect(t, s, nil, false, 0); !errors.Is(err, kafkawire.ErrNotLeaderOrFollower) {
		t.Fatalf("%v", err)
	}
	f.fetchErr = nil
	f.listErr = errors.New("list")
	if _, err := collect(t, s, nil, false, 0); err == nil {
		t.Fatal("no error from list offsets")
	}
	s3 := source(t, f, map[string]any{"topics": []string{"a"}, "offset_reset": "earliest"})
	if _, err := collect(t, s3, from, true, 0); err == nil {
		t.Fatal("no error from list offsets at the end")
	}
	if _, err := collect(t, s3, fitsync.Position(`{"offsets":{"a":{"0":0}}}`), true, 0); err == nil {
		t.Fatal("no error from list offsets at a reset")
	}
	f.listErr = nil
	f.partsErr = errors.New("parts")
	if _, err := collect(t, s, nil, false, 0); err == nil {
		t.Fatal("no error from partitions")
	}
	f.partsErr = nil
	// Emit refusing stops the source.
	stop := errors.New("stop")
	if err := s.Run(context.Background(), &fitsync.Run{Once: true, Emit: func(*fitsync.Record) error { return stop }}); err != stop {
		t.Fatalf("%v", err)
	}
	f.dialErr = errors.New("dial")
	s4 := source(t, f, map[string]any{"topics": []string{"a"}})
	if _, err := collect(t, s4, nil, true, 0); err == nil {
		t.Fatal("no dial error")
	}
	if err := s4.Test(context.Background()); err == nil {
		t.Fatal("no dial error in a test")
	}
	f.dialErr = nil
	s5 := source(t, f, map[string]any{"topics": []string{"missing"}})
	if err := s5.Test(context.Background()); err == nil {
		t.Fatal("a missing topic tested")
	}
	f.metaErr = errors.New("meta")
	if err := s5.Test(context.Background()); err == nil {
		t.Fatal("no metadata error")
	}
	f.metaErr = nil
	if err := testTopics(context.Background(), s5.cfg, func(kafkawire.Config) (client, error) { return noTopics{f}, nil }, []string{"a"}); err == nil {
		t.Fatal("a topic missing from the metadata tested")
	}
	// A cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }}); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
}

// noTopics answers metadata without the topics asked.
type noTopics struct{ *fake }

func (noTopics) Metadata(context.Context, ...string) (*kafkawire.Metadata, error) {
	return &kafkawire.Metadata{}, nil
}

func TestSourcePartitionsAdded(t *testing.T) {
	f := newFake()
	f.addTopic("a", 1)
	f.append("a", 0, msg("", "0"))
	s := source(t, f, map[string]any{"topics": []string{"a"}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	defer cancel()
	var got []string
	r := &fitsync.Run{Emit: func(rec *fitsync.Record) error {
		got = append(got, rec.After.Fields()[0].Value.Text())
		if len(got) == 1 {
			f.mu.Lock()
			f.topics["a"] = append(f.topics["a"], []kafkawire.Record{{Value: []byte("p1"), Offset: 0}})
			f.start["a"] = append(f.start["a"], 0)
			f.mu.Unlock()
			partitionsCheck = 0
		}
		if len(got) == 2 {
			cancel()
		}
		return nil
	}}
	defer func() { partitionsCheck = partitionsEvery }()
	var lags []int64
	r.Lag = func(l fitsync.SourceLag) { lags = append(lags, l.Behind) }
	s.Run(ctx, r)
	if strings.Join(got, ",") != "0,p1" || len(lags) == 0 {
		t.Fatalf("%v %v", got, lags)
	}
	partitionsCheck = 0
	f.partsErr = errors.New("parts")
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	defer cancel2()
	n := 0
	err := s.Run(ctx2, &fitsync.Run{From: fitsync.Position(`{"offsets":{"a":{"0":0,"1":0}}}`), Emit: func(*fitsync.Record) error {
		n++
		return nil
	}})
	if err == nil {
		t.Fatal("no error")
	}
}

func TestSink(t *testing.T) {
	f := newFake()
	f.addTopic("t", 3)
	k := sink(t, f, map[string]any{"max_batch_bytes": 80})
	k.now = func() time.Time { return time.UnixMilli(42) }
	row := func(i int64) fitsync.Value {
		return fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.String("v" + string(rune('0'+i)))})
	}
	key := func(i int64) fitsync.Value { return fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(i)}) }
	batch := []*fitsync.Record{
		{Namespace: "t", Op: fitsync.OpInsert, Key: key(1), After: row(1)},
		{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.String("s"), After: row(2), CommitTime: time.UnixMilli(7)},
		{Namespace: "t", Op: fitsync.OpDelete, Key: key(1)},
		{Namespace: "t", Op: fitsync.OpDelete, Before: row(3)},
		{Namespace: "t", Op: fitsync.OpMessage, Key: fitsync.Bytes([]byte{1}), After: row(4)},
		{Namespace: "t", Op: fitsync.OpMessage, Key: fitsync.Int(5), After: row(5)},
	}
	if err := k.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	var all []kafkawire.Record
	for p := range 3 {
		all = append(all, f.topics["t"][p]...)
	}
	if len(all) != 6 {
		t.Fatalf("%d messages", len(all))
	}
	p1 := kafkawire.PartitionFor([]byte(`{"id":1}`), 3)
	log := f.topics["t"][p1]
	if string(log[0].Key) != `{"id":1}` || string(log[0].Value) != "v1" || log[1].Value != nil || log[0].Timestamp.UnixMilli() != 42 {
		t.Fatalf("%+v", log)
	}
	if len(f.produced) < 2 {
		t.Fatalf("max_batch_bytes did not split: %d produces", len(f.produced))
	}
	// Errors: a bad topic, a partition failing, too large, permanent.
	var be *fitsync.BatchError
	err := k.Write(context.Background(), []*fitsync.Record{{Namespace: "bad topic", After: row(1)}})
	if !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if err := k.Write(context.Background(), []*fitsync.Record{{Namespace: "missing", After: row(1)}}); !errors.As(err, &be) || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	f.produceErr = func(topic string, p int32, n int) error {
		if n > 1 {
			return kafkawire.ErrMessageTooLarge
		}
		return nil
	}
	k.maxSize = 1 << 20
	if err := k.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	f.produceErr = func(topic string, p int32, n int) error { return kafkawire.ErrMessageTooLarge }
	err = k.Write(context.Background(), batch[:2])
	if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	f.produceErr = func(topic string, p int32, n int) error {
		if p == kafkawire.PartitionFor([]byte("s"), 3) {
			return kafkawire.ErrNotEnoughReplicas
		}
		return nil
	}
	err = k.Write(context.Background(), batch)
	if !errors.As(err, &be) || be.Index != 1 || fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	f.produceErr = nil
	// The codec failing, and a key that does not encode.
	k2 := sink(t, f, map[string]any{"codec": map[string]any{"module": "ticdc_simple"}, "deletes": "encode"})
	if err := k2.Write(context.Background(), batch[:1]); !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if err := k.Write(context.Background(), []*fitsync.Record{{Namespace: "t", Key: fitsync.Float(math.Inf(1)), After: row(1)}}); err == nil {
		t.Log("an infinite key encoded")
	}
	// A cancelled context, and a client that fails to dial.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.produceErr = func(string, int32, int) error { return context.Canceled }
	if err := k.Write(ctx, batch[:1]); err == nil {
		t.Fatal("written")
	}
	f.produceErr = nil
	f.dialErr = errors.New("dial")
	k3 := sink(t, f, map[string]any{})
	if err := k3.Write(context.Background(), batch[:1]); err == nil {
		t.Fatal("written without a client")
	}
	if err := k3.Test(context.Background()); err == nil {
		t.Fatal("tested without a client")
	}
	f.dialErr = nil
	if err := k.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	k4 := sink(t, f, map[string]any{"topic": "nope"})
	if err := k4.Test(context.Background()); err == nil {
		t.Fatal("a missing topic tested")
	}
	if k4.topicOf(batch[0]) != "nope" {
		t.Fatal(k4.topicOf(batch[0]))
	}
}

func TestSinkCreateTopics(t *testing.T) {
	f := newFake()
	k := sink(t, f, map[string]any{"topic": "x.{namespace}", "create_topics": map[string]any{"partitions": 4, "configs": map[string]string{"cleanup.policy": "compact"}}})
	if err := k.Write(context.Background(), []*fitsync.Record{{Namespace: "n", Key: fitsync.String("a"), After: fitsync.Map()}}); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || f.created[0].Name != "x.n" || f.created[0].Partitions != 4 || f.created[0].ReplicationFactor != -1 {
		t.Fatalf("%+v", f.created)
	}
	k2 := sink(t, f, map[string]any{"create_topics": map[string]any{}})
	k2.Write(context.Background(), []*fitsync.Record{{Namespace: "m", After: fitsync.Map()}})
	if f.created[1].Partitions != -1 {
		t.Fatalf("%+v", f.created)
	}
	f.createErr = kafkawire.ErrTopicAlreadyExists
	if err := k2.Write(context.Background(), []*fitsync.Record{{Namespace: "o", After: fitsync.Map()}}); err == nil {
		t.Fatal("written to a topic not there")
	}
	f.createErr = errors.New("create")
	if err := k2.Write(context.Background(), []*fitsync.Record{{Namespace: "p", After: fitsync.Map()}}); err == nil {
		t.Fatal("written")
	}
	f.createErr = nil
	f.metaErr = errors.New("meta")
	if err := k2.Write(context.Background(), []*fitsync.Record{{Namespace: "q", After: fitsync.Map()}}); err == nil {
		t.Fatal("written")
	}
	f.metaErr = nil
	f.addTopic("empty", 0)
	if err := k2.Write(context.Background(), []*fitsync.Record{{Namespace: "empty", After: fitsync.Map()}}); err == nil {
		t.Fatal("written to no partitions")
	}
	f.partsErr = errors.New("parts")
	f.addTopic("r", 1)
	if err := k2.Write(context.Background(), []*fitsync.Record{{Namespace: "r", After: fitsync.Map()}}); err == nil {
		t.Fatal("written")
	}
	f.partsErr = nil
	// A topic whose metadata has an error of its own.
	k3 := sink(t, f, map[string]any{})
	k3.dial = func(kafkawire.Config) (client, error) { return topicErr{f}, nil }
	if err := k3.Write(context.Background(), []*fitsync.Record{{Namespace: "t", After: fitsync.Map()}}); err == nil {
		t.Fatal("written")
	}
}

type topicErr struct{ *fake }

func (topicErr) Metadata(_ context.Context, topics ...string) (*kafkawire.Metadata, error) {
	return &kafkawire.Metadata{Topics: []kafkawire.TopicMetadata{{Name: topics[0], Err: kafkawire.ErrTopicAuthorizationFailed}}}, nil
}

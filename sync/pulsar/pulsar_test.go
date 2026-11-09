package syncpulsar

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pulsarwire "github.com/lesismal/fit/sync/pulsar/wire"
)

const (
	topicT = "persistent://public/default/t"
	topicP = "persistent://public/default/p"
	part0  = topicP + "-partition-0"
	part1  = topicP + "-partition-1"
)

func pm(key, value string) pulsarwire.ProducerMessage {
	m := pulsarwire.ProducerMessage{Payload: []byte(value)}
	if key != "" {
		m.Key = []byte(key)
	}
	return m
}

func TestProvision(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	os.WriteFile(ca, []byte("not a certificate"), 0o644)
	t.Setenv("FIT_PULSAR_TEST_TOKEN", "jwt")
	bad := []map[string]any{
		{"url": "http://x", "topics": []string{"a"}},
		{"url": "pulsar://b", "topics": []string{}},
		{"url": "pulsar://b", "topics": []string{"a", "a"}},
		{"url": "pulsar://b", "topics": []string{"bad topic"}},
		{"url": "pulsar://b", "topics": []string{"a/b"}},
		{"url": "pulsar://b", "topics": []string{"http://a/b/c"}},
		{"url": "pulsar://b", "topics": []string{"a"}, "token_env": "1bad"},
		{"url": "pulsar://b", "topics": []string{"a"}, "token_env": "FIT_PULSAR_TEST_UNSET"},
		{"url": "pulsar://b", "topics": []string{"a"}, "ca_file": ca},
		{"url": "pulsar+ssl://b", "topics": []string{"a"}, "ca_file": ca},
		{"url": "pulsar+ssl://b", "topics": []string{"a"}, "ca_file": filepath.Join(dir, "none")},
		{"url": "pulsar+ssl://b", "topics": []string{"a"}, "cert_file": ca},
		{"url": "pulsar+ssl://b", "topics": []string{"a"}, "cert_file": ca, "key_file": ca},
		{"url": "pulsar://b", "topics": []string{"a"}, "mode": "subscription"},
		{"url": "pulsar://b", "topics": []string{"a"}, "subscription": "s"},
		{"url": "pulsar://b", "topics": []string{"a"}, "codec": map[string]any{"module": "nope"}},
		{"url": "pulsar://b", "topics": []string{"a"}, "codec": map[string]any{"module": "pulsar"}},
	}
	for _, p := range bad {
		if _, err := load(t, fitsync.Sources, "pulsar", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	s := mustLoad(t, fitsync.Sources, "pulsar", map[string]any{"url": "pulsar+ssl://b", "topics": []string{"a", "x/y/b", "non-persistent://x/y/c"},
		"token_env": "FIT_PULSAR_TEST_TOKEN", "server_name": "n", "timeout": "5s", "codec": map[string]any{"module": "json"}}).(*Source)
	if s.cfg.Token != "jwt" || s.cfg.TLS.ServerName != "n" || s.cfg.RequestTimeout != 5*time.Second ||
		strings.Join(s.topics, " ") != topicT[:len(topicT)-1]+"a persistent://x/y/b non-persistent://x/y/c" {
		t.Fatalf("%+v %v", s.cfg, s.topics)
	}
	for _, p := range []map[string]any{
		{"topic": "{other}"}, {"topic": "bad topic"}, {"compression": "brotli"}, {"producer_name": "a b"},
		{"codec": map[string]any{"module": "nope"}},
	} {
		p["url"] = "pulsar://b"
		if _, err := load(t, fitsync.Sinks, "pulsar", p); err == nil {
			t.Errorf("%v loaded", p)
		}
	}
	k := mustLoad(t, fitsync.Sinks, "pulsar", map[string]any{"url": "pulsar://b", "compression": "zstd", "topic": "x-{namespace}",
		"max_batch_bytes": 10, "max_batch_messages": 2}).(*Sink)
	if k.compression != pulsarwire.Zstd || k.maxBytes != 10 || k.maxMsgs != 2 {
		t.Fatalf("%+v", k)
	}
	if k := mustLoad(t, fitsync.Sinks, "pulsar", map[string]any{"url": "pulsar://b"}).(*Sink); k.compression != pulsarwire.LZ4 {
		t.Fatal(k.compression)
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
	h = holder{cfg: pulsarwire.Config{URL: "x"}}
	if _, err := h.get(); err == nil {
		t.Fatal("a client of no URL")
	}
	// The real client, of a broker that is not there.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w, _ := newWire(pulsarwire.Config{URL: "pulsar://127.0.0.1:1", DialTimeout: time.Second})
	defer w.Close()
	if _, err := w.CreateProducer(ctx, "t", "", pulsarwire.None); err == nil {
		t.Error("a producer")
	}
	if _, err := w.Subscribe(ctx, "t", pulsarwire.SubscribeOptions{}); err == nil {
		t.Error("a consumer")
	}
}

func TestSourceReader(t *testing.T) {
	f := newFake()
	f.partitions[topicP] = 2
	f.add(part0, "x", pm("k0", "a"), pm("k1", "b"))
	f.add(part1, "x", pm("", "c"))
	f.add(part0, "x", pulsarwire.ProducerMessage{Key: []byte("gone"), Null: true})
	f.add(part0, "x", pulsarwire.ProducerMessage{Null: true}) // no key: nothing
	f.add(topicT, "x", pulsarwire.ProducerMessage{Key: []byte{0xff}, Payload: []byte("bin"), Properties: []pulsarwire.KeyValue{{Key: "h", Value: "v"}},
		EventTime: time.UnixMilli(5000)})
	s := source(t, f, map[string]any{"topics": []string{"p", "t"}})
	recs, err := collect(t, s, nil, false, 5)
	if err != nil {
		t.Fatal(err)
	}
	var del, bin int
	for _, r := range recs {
		if r.Source != "pulsar" {
			t.Fatalf("%+v", r)
		}
		if r.Op == fitsync.OpDelete {
			del++
			if r.Key.Text() != "gone" || r.Namespace != "p" {
				t.Fatalf("%+v", r)
			}
		}
		if r.Key.Kind() == fitsync.KindBytes {
			bin++
			props, _ := r.After.Get("properties")
			if v, _ := props.Get("h"); v.Text() != "v" || r.Namespace != "t" || !r.CommitTime.Equal(time.UnixMilli(5000)) {
				t.Fatalf("%+v", r)
			}
			if _, ok := r.After.Get("event_time"); !ok {
				t.Fatal("no event_time")
			}
		}
		if r.Op == fitsync.OpMessage {
			if v, _ := r.After.Get("topic"); !strings.HasPrefix(v.Text(), "persistent://") {
				t.Fatalf("%+v", r)
			}
		}
	}
	if del != 1 || bin != 1 {
		t.Fatalf("%d deletes, %d binary keys", del, bin)
	}
	from := lastPosition(recs)
	var pos position
	json.Unmarshal(from, &pos)
	if pos.IDs[part0] != (msgID{1, 2, -1}) || pos.IDs[part1] != (msgID{1, 0, -1}) || pos.IDs[topicT] != (msgID{1, 0, -1}) {
		t.Fatalf("%+v", pos)
	}
	// From the checkpoint: nothing more, then one added.
	f.add(part1, "x", pm("n", "new"))
	recs, err = collect(t, s, from, false, 1)
	if err != nil || len(recs) != 1 || recs[0].Key.Text() != "n" {
		t.Fatalf("%v %+v", err, recs)
	}
	// From the middle of a batch: the rest of it.
	mid, _ := json.Marshal(position{IDs: map[string]msgID{part0: {1, 0, 0}}})
	recs, err = collect(t, s, mid, false, 1)
	if err != nil || recs[0].Key.Text() != "k1" {
		t.Fatalf("%v %+v", err, recs)
	}
	// start latest: only what comes after.
	s = source(t, f, map[string]any{"topics": []string{"t"}, "start": "latest"})
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.add(topicT, "x", pm("late", "z"))
	}()
	recs, err = collect(t, s, nil, false, 1)
	if err != nil || recs[0].Key.Text() != "late" {
		t.Fatalf("%v %+v", err, recs)
	}
}

func TestSourceOnce(t *testing.T) {
	f := newFake()
	f.partitions[topicP] = 2
	f.add(part0, "x", pm("a", "1"), pm("b", "2"))
	f.add(part0, "x", pm("c", "3"))
	s := source(t, f, map[string]any{"topics": []string{"p"}})
	f.onSubscribe = func(string, pulsarwire.SubscribeOptions) error {
		// Added after the job's end is read: not read.
		go f.add(part0, "x", pm("late", "4"))
		f.onSubscribe = nil
		return nil
	}
	recs, err := collect(t, s, nil, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("%d records", len(recs))
	}
	// A checkpoint at the end: done at once.
	f2 := newFake()
	f2.add(topicT, "x", pm("a", "1"))
	s = source(t, f2, map[string]any{"topics": []string{"t"}})
	at, _ := json.Marshal(position{IDs: map[string]msgID{topicT: {1, 0, -1}}})
	if recs, err := collect(t, s, at, true, 0); err != nil || len(recs) != 0 {
		t.Fatalf("%v %d", err, len(recs))
	}
	// A batch whose end is in the job: read to its last message.
	f3 := newFake()
	f3.add(topicT, "x", pm("a", "1"), pm("b", "2"))
	s = source(t, f3, map[string]any{"topics": []string{"t"}})
	if recs, err := collect(t, s, nil, true, 0); err != nil || len(recs) != 2 {
		t.Fatalf("%v %d", err, len(recs))
	}
	f3.lastErr = errors.New("no last")
	if _, err := collect(t, s, nil, true, 0); err == nil {
		t.Fatal("no error")
	}
}

func TestSourceSubscription(t *testing.T) {
	f := newFake()
	f.add(topicT, "x", pm("a", "1"), pm("b", "2"))
	f.add(topicT, "x", pm("c", "3"))
	s := source(t, f, map[string]any{"topics": []string{"t"}, "mode": "subscription", "subscription": "sub", "subscription_type": "failover"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var recs []*fitsync.Record
	got := make(chan struct{}, 10)
	done := make(chan error)
	go func() {
		done <- s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
			recs = append(recs, r)
			got <- struct{}{}
			return nil
		}})
	}()
	for range 3 {
		<-got
	}
	// Acknowledged in the middle of a batch: the entry before it, none
	// here; then the whole of the first entry, then the second.
	s.Acknowledged(recs[0].Position)
	s.Acknowledged(fitsync.Position("not json"))
	s.Acknowledged(recs[1].Position)
	waitAcks := func(n int) {
		t.Helper()
		for range 500 {
			f.mu.Lock()
			l := len(f.acks)
			f.mu.Unlock()
			if l >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("not %d acks: %v", n, f.acks)
	}
	waitAcks(1)
	s.Acknowledged(recs[1].Position) // again: nothing new
	s.Acknowledged(recs[2].Position)
	waitAcks(2)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.acks[0].Entry != 0 || f.acks[1].Entry != 1 || len(f.acks) != 2 {
		t.Fatalf("%v", f.acks)
	}
	// A restart: the checkpoint acknowledged again, and passed over.
	f.acked = map[string]int64{}
	f.acks = nil
	mid, _ := json.Marshal(position{IDs: map[string]msgID{topicT: {1, 1, -1}}})
	f.add(topicT, "x", pm("d", "4"))
	out, err := collect(t, s, mid, false, 1)
	if err != nil || out[0].Key.Text() != "d" || len(f.acks) != 1 || f.acks[0].Entry != 1 {
		t.Fatalf("%v %+v %v", err, out, f.acks)
	}
	// An acknowledgement that fails stops the run.
	f.ackErr = errors.New("ack lost")
	at, _ := json.Marshal(position{IDs: map[string]msgID{topicT: {1, 3, -1}}})
	if _, err := collect(t, s, at, false, 0); err == nil || !strings.Contains(err.Error(), "ack lost") {
		t.Fatal(err)
	}
	// A batch read in part: the entry before it.
	f.ackErr = nil
	p := &part{name: "x", cs: &fakeConsumer{f: f}}
	if err := (&reader{s: s}).ackPart(p, msgID{1, 5, 2}); err != nil || p.acked.Entry != 4 {
		t.Fatal(err, p.acked)
	}
	// Mode reader ignores acknowledgements.
	r := source(t, f, map[string]any{"topics": []string{"t"}})
	r.Acknowledged(at)
	if r.ackPos != nil {
		t.Fatal("kept")
	}
}

// twoCodec decodes each value into two records, or fails on "bad".
type twoCodec struct{}

func (twoCodec) Decode(data []byte, emit func(*fitsync.Record)) error {
	if string(data) == "bad" {
		return errors.New("bad value")
	}
	for range 2 {
		emit(&fitsync.Record{Op: fitsync.OpInsert, Namespace: "db.t", After: fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.Bytes(data)})})
	}
	return nil
}

func (twoCodec) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	for _, r := range recs {
		if r.Op == fitsync.OpUpdate {
			return nil, errors.New("no updates")
		}
		v, _ := r.After.Get("v")
		b = append(b, v.Text()...)
	}
	return b, nil
}

func TestSourceDecode(t *testing.T) {
	f := newFake()
	f.add(topicT, "x", pm("k", "row"), pm("", "bad"), pulsarwire.ProducerMessage{Key: []byte("d"), Null: true})
	s := source(t, f, map[string]any{"topics": []string{"t"}, "metadata_field": "_meta", "tombstones": "skip"})
	s.codec = twoCodec{}
	recs, err := collect(t, s, nil, false, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || !recs[0].Tx.First || !recs[1].Tx.Last || recs[0].Tx.ID == "" || recs[0].Namespace != "db.t" || recs[0].Position != nil || recs[1].Position == nil {
		t.Fatalf("%+v", recs)
	}
	if meta, ok := recs[0].After.Get("_meta"); !ok || meta.Kind() != fitsync.KindMap {
		t.Fatalf("%+v", recs[0].After)
	}
	if v, _ := recs[2].After.Get("error"); v.Text() != "bad value" || recs[2].Tx.ID != "" {
		t.Fatalf("%+v", recs[2])
	}
	if _, ok := recs[2].After.Get("message_id"); !ok {
		t.Fatalf("%+v", recs[2].After)
	}
	s = source(t, f, map[string]any{"topics": []string{"t"}, "namespace": "ns"})
	recs, _ = collect(t, s, nil, false, 3)
	if recs[0].Namespace != "ns" || recs[2].Op != fitsync.OpDelete {
		t.Fatalf("%+v", recs)
	}
}

func TestSourceErrors(t *testing.T) {
	f := newFake()
	f.add(topicT, "x", pm("a", "1"))
	s := source(t, f, map[string]any{"topics": []string{"t"}})
	for name, set := range map[string]func(){
		"dial":      func() { f.dialErr = errors.New("no route") },
		"metadata":  func() { f.metaErr = errors.New("no metadata") },
		"subscribe": func() { f.subErr = errors.New("no subscription") },
		"receive":   func() { f.recvErr = errors.New("connection lost") },
	} {
		*f = fake{partitions: f.partitions, logs: f.logs, entries: f.entries, acked: f.acked}
		set()
		s.cl.close()
		if _, err := collect(t, s, nil, false, 0); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	f = newFake()
	f.add(topicT, "x", pm("a", "1"))
	s = source(t, f, map[string]any{"topics": []string{"t"}})
	if _, err := collect(t, s, fitsync.Position("{"), false, 0); err == nil {
		t.Error("a bad checkpoint")
	}
	stop := errors.New("stopping")
	if err := s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }}); !errors.Is(err, stop) {
		t.Error(err)
	}
	if err := s.Test(context.Background()); err != nil {
		t.Error(err)
	}
	f.metaErr = errors.New("denied")
	if err := s.Test(context.Background()); err == nil {
		t.Error("tested")
	}
	f.dialErr = errors.New("no route")
	if err := s.Test(context.Background()); err == nil {
		t.Error("tested")
	}
}

func TestSourcePartitionsAdded(t *testing.T) {
	old := partitionsCheck
	partitionsCheck = 10 * time.Millisecond
	defer func() { partitionsCheck = old }()
	f := newFake()
	f.partitions[topicP] = 1
	f.add(part0, "x", pm("a", "1"))
	s := source(t, f, map[string]any{"topics": []string{"p", "t"}})
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		f.partitions[topicP] = 2
		f.mu.Unlock()
		f.add(part1, "x", pm("b", "2"))
	}()
	recs, err := collect(t, s, nil, false, 2)
	if err != nil || recs[1].Key.Text() != "b" {
		t.Fatalf("%v %+v", err, recs)
	}
	// The check failing stops the run.
	f.mu.Lock()
	f.metaErr = nil
	f.mu.Unlock()
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		f.metaErr = errors.New("gone")
		f.mu.Unlock()
	}()
	if _, err := collect(t, s, nil, false, 0); err == nil {
		t.Fatal("no error")
	}
}

func TestJavaStringHash(t *testing.T) {
	for s, want := range map[string]int32{"": 0, "key": 106079, "hello": 99162322, "ü": 252, "𝄞": 1772394,
		"a long key that overflows": javaRef("a long key that overflows")} {
		if got := javaStringHash(s); got != want {
			t.Errorf("%q: %d, want %d", s, got, want)
		}
	}
}

// javaRef is Java's String.hashCode, written plainly, for a string of
// ASCII.
func javaRef(s string) int32 {
	var h int32
	for i := 0; i < len(s); i++ {
		h = h*31 + int32(s[i])
	}
	return h & 0x7fffffff
}

func TestSink(t *testing.T) {
	f := newFake()
	f.partitions[topicP] = 3
	s := sink(t, f, map[string]any{"topic": "{namespace}", "max_batch_messages": 2})
	batch := []*fitsync.Record{
		{Namespace: "p", Op: fitsync.OpInsert, Key: fitsync.String("key"), After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.String("v1")}), Position: fitsync.Position(`{"n":1}`)},
		{Namespace: "p", Op: fitsync.OpInsert, Key: fitsync.String("key"), After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.String("v2")}), Position: fitsync.Position(`{"n":2}`)},
		{Namespace: "p", Op: fitsync.OpInsert, Key: fitsync.String("key"), After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.String("v3")}), Position: fitsync.Position(`{"n":3}`)},
		{Namespace: "p", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(1)}), Position: fitsync.Position(`{"n":4}`)},
		{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "value", Value: fitsync.Bytes([]byte{1})}), CommitTime: time.UnixMilli(77)},
		{Namespace: "t", Op: fitsync.OpDelete, Before: fitsync.Map()},
	}
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	// "key" hashes to 106079, partition 2 of 3: two batches, by
	// max_batch_messages.
	part2 := topicP + "-partition-2"
	keyed := 0
	for _, m := range f.logs[part2] {
		if string(m.Key) == "key" {
			keyed++
		}
	}
	base := f.count(part2)
	if keyed != 3 || f.entries[part2] < 2 {
		t.Fatalf("%v", f.entries)
	}
	m := f.logs[part2][0]
	if string(m.Payload) != "v1" || string(m.Key) != "key" || m.ProducerName != "fit-sync" || m.Properties[0].Key != "fit-id" || len(m.Properties[0].Value) != 32 {
		t.Fatalf("%+v", m)
	}
	// {"id":1} as JSON: a tombstone.
	n := 0
	for _, p := range []string{part0, part1, part2} {
		for _, m := range f.logs[p] {
			if string(m.Key) == `{"id":1}` {
				n++
				if m.Payload != nil {
					t.Fatalf("%+v", m)
				}
			}
		}
	}
	if n != 1 || f.count(topicT) != 2 || !f.logs[topicT][0].EventTime.Equal(time.UnixMilli(77)) || f.logs[topicT][1].Payload == nil {
		t.Fatalf("%d %+v", n, f.logs[topicT])
	}
	// Written again: nothing more stored.
	if err := s.Write(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if f.count(part2) != base || f.count(topicT) != 2 {
		t.Fatal("stored twice")
	}
	// A sink made anew reads back what its producer stored last.
	s2 := sink(t, f, map[string]any{"max_batch_messages": 2})
	if err := s2.Write(context.Background(), batch[1:]); err != nil {
		t.Fatal(err)
	}
	if f.count(part2) != base {
		t.Fatalf("stored again: %d", f.count(part2))
	}
	// But not what another producer stored, nor with dedup off.
	s3 := sink(t, f, map[string]any{"producer_name": "other"})
	if err := s3.Write(context.Background(), batch[2:3]); err != nil {
		t.Fatal(err)
	}
	s4 := sink(t, f, map[string]any{"dedup": "off", "deletes": "encode", "topic": "fixed"})
	if err := s4.Write(context.Background(), batch[3:4]); err != nil {
		t.Fatal(err)
	}
	if f.count(part2) != base+1 || f.count("persistent://public/default/fixed") != 1 {
		t.Fatalf("%d", f.count(part2))
	}
	if err := s4.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The read-back of a topic the producer has nothing in yet, and of one
	// whose last entry is a single message.
	f.add(topicP+"-partition-1", "fit-sync", pulsarwire.ProducerMessage{Payload: []byte("x"), Properties: []pulsarwire.KeyValue{{Key: "other", Value: "1"}}})
	s5 := sink(t, f, map[string]any{"topic": "fresh"})
	if err := s5.Write(context.Background(), batch[4:5]); err != nil {
		t.Fatal(err)
	}
	s5.Cleanup()
}

func TestSinkErrors(t *testing.T) {
	ctx := context.Background()
	rec := &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "v", Value: fitsync.String("x")})}
	f := newFake()
	s := sink(t, f, map[string]any{})
	var be *fitsync.BatchError
	// A topic Pulsar does not take: permanent.
	if err := s.Write(ctx, []*fitsync.Record{{Namespace: "a b"}}); !errors.As(err, &be) || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	// A key JSON cannot write.
	if err := s.Write(ctx, []*fitsync.Record{{Namespace: "t", Key: fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.NaN())})}}); !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	s.codec = twoCodec{}
	if err := s.Write(ctx, []*fitsync.Record{rec, {Namespace: "t", Op: fitsync.OpUpdate}}); !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	for name, set := range map[string]func(){
		"dial":     func() { f.dialErr = errors.New("no route") },
		"metadata": func() { f.metaErr = errors.New("no metadata") },
		"producer": func() { f.producerErr = errors.New("busy") },
		"subscribe": func() {
			f.subErr = errors.New("denied")
		},
		"last": func() { f.lastErr = errors.New("no last") },
		"read back": func() {
			// The last entry never comes.
			f.onSubscribe = func(topic string, o pulsarwire.SubscribeOptions) error {
				if o.Start.Ledger != pulsarwire.LatestID.Ledger {
					f.recvErr = errors.New("lost")
				}
				return nil
			}
		},
		"second subscribe": func() {
			f.onSubscribe = func(topic string, o pulsarwire.SubscribeOptions) error {
				if o.Start.Ledger != pulsarwire.LatestID.Ledger {
					return errors.New("denied")
				}
				return nil
			}
		},
		"send now": func() {
			f.sendErr = func(string, bool) error { return errors.New("closed") }
		},
		"too large": func() {
			f.sendErr = func(_ string, now bool) error {
				if now {
					return pulsarwire.ErrTooLarge
				}
				return nil
			}
		},
		"send result": func() {
			f.sendErr = func(_ string, now bool) error {
				if !now {
					return errors.New("persistence")
				}
				return nil
			}
		},
	} {
		f = newFake()
		f.add(topicT, "fit-sync", pm("", "old"))
		set()
		s = sink(t, f, map[string]any{})
		recs := []*fitsync.Record{rec, rec}
		if name == "too large" {
			recs = recs[:1]
		}
		err := s.Write(ctx, recs)
		if err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if name == "too large" && !fitsync.IsPermanent(err) {
			t.Errorf("%s: %v", name, err)
		}
		if name == "send result" && len(s.producers) != 0 {
			t.Errorf("%s: the producer kept", name)
		}
	}
	// A context that ends while the receipts are awaited.
	f = newFake()
	block := make(chan struct{})
	f.sendErr = func(_ string, now bool) error {
		if !now {
			<-block
		}
		return nil
	}
	s = sink(t, f, map[string]any{"dedup": "off"})
	cctx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(20 * time.Millisecond)
		close(block)
	}()
	cancel()
	if err := s.Write(cctx, []*fitsync.Record{rec}); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Tests failing.
	f.dialErr = errors.New("no route")
	if err := s.Test(ctx); err == nil {
		t.Error("tested")
	}
	f.dialErr, f.metaErr = nil, errors.New("denied")
	s.Topic = "t"
	if err := s.Test(ctx); err == nil {
		t.Error("tested")
	}
}

func TestStoredWindow(t *testing.T) {
	sp := &sinkProducer{stored: map[string]bool{}}
	for i := range storedWindow + 2 {
		sp.store(strconv.Itoa(i))
	}
	sp.store(strconv.Itoa(storedWindow + 1))
	if len(sp.stored) != storedWindow || sp.stored["0"] {
		t.Fatal(len(sp.stored))
	}
}

func TestSmallParts(t *testing.T) {
	for _, bad := range []string{strings.Repeat("a", 256), "ÿ", "a b"} {
		if validName(bad, true) {
			t.Errorf("%q valid", bad)
		}
	}
	if !validName("a%20b", true) || validName("a%20b", false) {
		t.Error("%")
	}
	// A job's partition past its end, and partitions not all done.
	rd := &reader{s: &Source{}, run: &fitsync.Run{}}
	end := pulsarwire.MessageID{Ledger: 1, Entry: 0, BatchIndex: -1}
	p := &part{end: &end}
	if err := rd.message(p, pulsarwire.Message{ID: pulsarwire.MessageID{Ledger: 1, Entry: 1, BatchIndex: -1}, Payload: []byte("x")}); err != nil || !p.done {
		t.Fatal(err, p.done)
	}
	rd.parts = []*part{p, {}}
	if rd.allDone() {
		t.Error("all done")
	}
	// An acknowledgement that fails.
	f := newFake()
	f.ackErr = errors.New("lost")
	if err := rd.ackPart(&part{cs: &fakeConsumer{f: f}}, msgID{1, 1, -1}); err == nil {
		t.Error("acknowledged")
	}
}

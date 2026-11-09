package synckafka

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

// fake is an in-memory Kafka: topics of partitions of records.
type fake struct {
	mu     sync.Mutex
	topics map[string][][]kafkawire.Record
	start  map[string][]int64 // log start offsets
	closed int

	dialErr, metaErr, partsErr, listErr, createErr error
	// fetchErr and produceErr, when set, give a partition's error.
	fetchErr   func(topic string, p int32, offset int64) error
	produceErr func(topic string, p int32, n int) error
	// fetchCalls counts fetches; onFetch runs before each.
	fetchCalls int
	onFetch    func(n int)
	created    []kafkawire.TopicSpec
	produced   [][]kafkawire.ProduceBatch
}

func newFake() *fake {
	return &fake{topics: map[string][][]kafkawire.Record{}, start: map[string][]int64{}}
}

func (f *fake) dial(kafkawire.Config) (client, error) {
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	return f, nil
}

func (f *fake) addTopic(name string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.topics[name] = make([][]kafkawire.Record, n)
	f.start[name] = make([]int64, n)
}

func (f *fake) append(topic string, p int32, recs ...kafkawire.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range recs {
		r.Offset = int64(len(f.topics[topic][p]))
		f.topics[topic][p] = append(f.topics[topic][p], r)
	}
}

func (f *fake) Metadata(ctx context.Context, topics ...string) (*kafkawire.Metadata, error) {
	if f.metaErr != nil {
		return nil, f.metaErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	m := &kafkawire.Metadata{}
	for _, t := range topics {
		tm := kafkawire.TopicMetadata{Name: t}
		if ps, ok := f.topics[t]; !ok {
			tm.Err = kafkawire.ErrUnknownTopicOrPartition
		} else {
			for i := range ps {
				tm.Partitions = append(tm.Partitions, kafkawire.PartitionMetadata{Partition: int32(i)})
			}
		}
		m.Topics = append(m.Topics, tm)
	}
	return m, nil
}

func (f *fake) Partitions(ctx context.Context, topic string) (int32, error) {
	if f.partsErr != nil {
		return 0, f.partsErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ps, ok := f.topics[topic]
	if !ok {
		return 0, kafkawire.ErrUnknownTopicOrPartition
	}
	return int32(len(ps)), nil
}

func (f *fake) ListOffsets(ctx context.Context, committed bool, qs ...kafkawire.OffsetQuery) ([]kafkawire.OffsetResult, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]kafkawire.OffsetResult, len(qs))
	for i, q := range qs {
		out[i] = kafkawire.OffsetResult{Topic: q.Topic, Partition: q.Partition}
		switch q.Timestamp {
		case kafkawire.OffsetEarliest:
			out[i].Offset = f.start[q.Topic][q.Partition]
		default:
			out[i].Offset = int64(len(f.topics[q.Topic][q.Partition]))
		}
	}
	return out, nil
}

func (f *fake) Fetch(ctx context.Context, opts kafkawire.FetchOptions, parts ...kafkawire.FetchPartition) ([]kafkawire.FetchResult, error) {
	f.mu.Lock()
	f.fetchCalls++
	n, hook := f.fetchCalls, f.onFetch
	f.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]kafkawire.FetchResult, len(parts))
	got := false
	for i, p := range parts {
		log := f.topics[p.Topic][p.Partition]
		fr := kafkawire.FetchResult{Topic: p.Topic, Partition: p.Partition, NextOffset: p.Offset,
			HighWatermark: int64(len(log)), LogStartOffset: f.start[p.Topic][p.Partition]}
		if f.fetchErr != nil {
			fr.Err = f.fetchErr(p.Topic, p.Partition, p.Offset)
		}
		if fr.Err == nil && (p.Offset < fr.LogStartOffset || p.Offset > int64(len(log))) {
			fr.Err = kafkawire.ErrOffsetOutOfRange
		}
		if fr.Err == nil && p.Offset < int64(len(log)) {
			end := min(int64(len(log)), p.Offset+3)
			fr.Records = append([]kafkawire.Record(nil), log[p.Offset:end]...)
			fr.NextOffset = end
			got = true
		}
		out[i] = fr
	}
	if !got {
		f.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-time.After(time.Millisecond):
		}
		f.mu.Lock()
	}
	return out, nil
}

func (f *fake) Produce(ctx context.Context, opts kafkawire.ProduceOptions, batches ...kafkawire.ProduceBatch) ([]kafkawire.ProduceResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.produced = append(f.produced, batches)
	out := make([]kafkawire.ProduceResult, len(batches))
	var first error
	for i, b := range batches {
		out[i] = kafkawire.ProduceResult{Topic: b.Topic, Partition: b.Partition}
		if f.produceErr != nil {
			if err := f.produceErr(b.Topic, b.Partition, len(b.Records)); err != nil {
				out[i].Err = err
				if first == nil {
					first = err
				}
				continue
			}
		}
		out[i].BaseOffset = int64(len(f.topics[b.Topic][b.Partition]))
		for _, r := range b.Records {
			r.Offset = int64(len(f.topics[b.Topic][b.Partition]))
			f.topics[b.Topic][b.Partition] = append(f.topics[b.Topic][b.Partition], r)
		}
	}
	return out, first
}

func (f *fake) CreateTopics(ctx context.Context, timeout time.Duration, topics ...kafkawire.TopicSpec) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.mu.Lock()
	f.created = append(f.created, topics...)
	f.mu.Unlock()
	for _, t := range topics {
		n := int(t.Partitions)
		if n <= 0 {
			n = 2
		}
		f.addTopic(t.Name, n)
	}
	return nil
}

func (f *fake) Close() error {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
	return nil
}

// load provisions a module of ns from params.
func load(t *testing.T, ns, name string, params map[string]any) (any, error) {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, config.ModuleRef{Module: name, Params: v}, nil)
	if err == nil {
		t.Cleanup(func() { module.Cleanup(m) })
	}
	return m, err
}

func mustLoad(t *testing.T, ns, name string, params map[string]any) any {
	t.Helper()
	m, err := load(t, ns, name, params)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func source(t *testing.T, f *fake, params map[string]any) *Source {
	t.Helper()
	if params["brokers"] == nil {
		params["brokers"] = []string{"b:9092"}
	}
	s := mustLoad(t, fitsync.Sources, "kafka", params).(*Source)
	s.dial = f.dial
	return s
}

func sink(t *testing.T, f *fake, params map[string]any) *Sink {
	t.Helper()
	if params["brokers"] == nil {
		params["brokers"] = []string{"b:9092"}
	}
	s := mustLoad(t, fitsync.Sinks, "kafka", params).(*Sink)
	s.dial = f.dial
	return s
}

// collect runs a source until it ends, or until it has n records and then
// is stopped; it returns the records.
func collect(t *testing.T, s fitsync.Source, from fitsync.Position, once bool, n int) ([]*fitsync.Record, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	defer cancel()
	var out []*fitsync.Record
	err := s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
		out = append(out, r)
		if n > 0 && len(out) >= n {
			cancel()
		}
		return nil
	}})
	if errors.Is(err, context.Canceled) && n > 0 && len(out) >= n {
		err = nil
	}
	return out, err
}

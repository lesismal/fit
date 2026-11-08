package kafkawire

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

// Acks is how many replicas must have a produced batch before the broker
// answers.
type Acks int8

const (
	// AcksAll waits for every in-sync replica (the default).
	AcksAll Acks = iota
	// AcksLeader waits for the leader alone.
	AcksLeader
	// AcksNone has no answer: the call returns once the batch is
	// written to the connection, with no offset.
	AcksNone
)

func (a Acks) wire() int16 {
	switch a {
	case AcksLeader:
		return 1
	case AcksNone:
		return 0
	}
	return -1
}

// ProduceOptions are a produce call's settings.
type ProduceOptions struct {
	Acks        Acks
	Compression Compression
	// Level is the codec's level: gzip 1 to 9, zstd 1 to 22; 0 is the
	// codec's default.
	Level int
	// Timeout is how long the broker waits for the replicas (30s).
	Timeout time.Duration
}

// ProduceBatch is records for one partition, sent as one record batch.
type ProduceBatch struct {
	Topic     string
	Partition int32
	Records   []Record
}

// ProduceResult is a batch's outcome: the offset of its first record (-1
// with AcksNone), the broker's time with LogAppendTime, or its error.
type ProduceResult struct {
	Topic         string
	Partition     int32
	BaseOffset    int64
	LogAppendTime time.Time
	Err           error
}

// Produce writes each batch to its partition's leader, the leaders in
// parallel, and retries a batch the broker refuses for a retriable reason
// (a new leader, too few in-sync replicas) or whose connection failed. A
// retry may write a batch twice when the first answer was lost. The
// results are in the batches' order, at most one batch per partition; the error is the first batch's
// error, if any.
func (c *Client) Produce(ctx context.Context, opts ProduceOptions, batches ...ProduceBatch) ([]ProduceResult, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	results := make([]ProduceResult, len(batches))
	parts := make([]part, len(batches))
	encoded := make([][]byte, len(batches))
	now := time.Now()
	seen := make(map[part]bool, len(batches))
	for i, b := range batches {
		if seen[part{b.Topic, b.Partition}] {
			return results, fmt.Errorf("kafka: two batches for %s/%d in one produce", b.Topic, b.Partition)
		}
		seen[part{b.Topic, b.Partition}] = true
		results[i] = ProduceResult{Topic: b.Topic, Partition: b.Partition, BaseOffset: -1}
		parts[i] = part{b.Topic, b.Partition}
		var err error
		if encoded[i], err = appendBatch(nil, b.Records, opts.Compression, opts.Level, noProducer, now); err != nil {
			return results, fmt.Errorf("kafka: %s/%d: %w", b.Topic, b.Partition, err)
		}
	}
	errs := c.routed(ctx, false, parts, func(ctx context.Context, bc *brokerConn, idx []int) []error {
		return c.produceOn(ctx, bc, opts, parts, encoded, idx, results)
	})
	var first error
	for i, err := range errs {
		if err != nil {
			results[i].Err = err
			if first == nil {
				first = fmt.Errorf("kafka: produce to %s/%d: %w", parts[i].topic, parts[i].partition, err)
			}
		}
	}
	return results, first
}

// ProduceRecords writes records to one partition as one batch and returns
// the offset of the first.
func (c *Client) ProduceRecords(ctx context.Context, opts ProduceOptions, topic string, partition int32, records ...Record) (int64, error) {
	r, err := c.Produce(ctx, opts, ProduceBatch{Topic: topic, Partition: partition, Records: records})
	if err != nil {
		return -1, err
	}
	return r[0].BaseOffset, nil
}

func (c *Client) produceOn(ctx context.Context, bc *brokerConn, opts ProduceOptions, parts []part, encoded [][]byte, idx []int, results []ProduceResult) []error {
	v, err := bc.version(apiProduce)
	if err != nil {
		return fill(len(idx), err)
	}
	if opts.Compression == Zstd && v < 7 {
		return fill(len(idx), fmt.Errorf("kafka: broker %s cannot take zstd (Produce v%d): %w", bc.addr, v, ErrUnsupportedCompressionType))
	}
	// Group by topic, keeping each topic's partitions in order.
	var topics []string
	byTopic := map[string][]int{}
	for _, i := range idx {
		t := parts[i].topic
		if _, ok := byTopic[t]; !ok {
			topics = append(topics, t)
		}
		byTopic[t] = append(byTopic[t], i)
	}
	var e encoder
	e.int16(-1) // no transactional ID
	e.int16(opts.Acks.wire())
	e.int32(int32(opts.Timeout / time.Millisecond))
	e.int32(int32(len(topics)))
	for _, t := range topics {
		e.string(t)
		e.int32(int32(len(byTopic[t])))
		for _, i := range byTopic[t] {
			e.int32(parts[i].partition)
			e.bytes(encoded[i])
		}
	}
	if opts.Acks == AcksNone {
		if _, err := bc.send(apiProduce, v, e.b, false, c.cfg.RequestTimeout); err != nil {
			return fill(len(idx), err)
		}
		return make([]error, len(idx))
	}
	resp, err := bc.roundTrip(ctx, apiProduce, v, e.b, c.cfg.RequestTimeout+opts.Timeout)
	if err != nil {
		return fill(len(idx), err)
	}
	where := indexOf(parts, idx)
	errs := fill(len(idx), &ConnError{Addr: bc.addr, Err: errors.New("Produce: a partition missing from the answer")})
	pos := func(i int) int { return slices.Index(idx, i) }
	d := decoder{b: resp}
	for range d.arrayLen() {
		t := d.string()
		for range d.arrayLen() {
			p := d.int32()
			code := Error(d.int16())
			base := d.int64()
			appendTime := d.int64()
			if v >= 5 {
				d.int64() // log start offset
			}
			var msg *string
			if v >= 8 {
				for range d.arrayLen() {
					d.int32()
					d.nullableString()
				}
				msg = d.nullableString()
			}
			i, ok := where[part{t, p}]
			if !ok || d.err != nil {
				continue
			}
			if code != 0 {
				if msg != nil && *msg != "" {
					errs[pos(i)] = fmt.Errorf("%s: %w", *msg, code)
				} else {
					errs[pos(i)] = code
				}
				continue
			}
			errs[pos(i)] = nil
			results[i].BaseOffset = base
			if appendTime >= 0 {
				results[i].LogAppendTime = time.UnixMilli(appendTime)
			}
		}
	}
	if d.err != nil {
		return fill(len(idx), &ConnError{Addr: bc.addr, Err: fmt.Errorf("Produce: %w", d.err)})
	}
	return errs
}

package kafkawire

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// The timestamps ListOffsets takes besides a time: the next offset to be
// written (or, read committed, the last stable offset) and the first
// offset in the log.
const (
	OffsetLatest   int64 = -1
	OffsetEarliest int64 = -2
)

// OffsetQuery asks a partition for the first offset whose record's time is
// at or after Timestamp (milliseconds since the epoch), or for
// OffsetLatest or OffsetEarliest.
type OffsetQuery struct {
	Topic     string
	Partition int32
	Timestamp int64
}

// OffsetResult is the answer to an OffsetQuery: the offset (-1 when no
// record is that recent) and its record's time.
type OffsetResult struct {
	Topic     string
	Partition int32
	Offset    int64
	Timestamp int64
	Err       error
}

// ListOffsets asks the partitions' leaders for offsets, the leaders in
// parallel, retrying as Produce does. The results are in the queries'
// order; the error is the first query's error, if any.
func (c *Client) ListOffsets(ctx context.Context, readCommitted bool, queries ...OffsetQuery) ([]OffsetResult, error) {
	results := make([]OffsetResult, len(queries))
	ps := make([]part, len(queries))
	seen := make(map[part]bool, len(queries))
	for i, q := range queries {
		ps[i] = part{q.Topic, q.Partition}
		results[i] = OffsetResult{Topic: q.Topic, Partition: q.Partition, Offset: -1, Timestamp: -1}
		if seen[ps[i]] {
			return results, fmt.Errorf("kafka: %s/%d asked twice in one ListOffsets", q.Topic, q.Partition)
		}
		seen[ps[i]] = true
	}
	errs := c.routed(ctx, false, ps, func(ctx context.Context, bc *brokerConn, idx []int) []error {
		return c.listOffsetsOn(ctx, bc, readCommitted, queries, idx, results)
	})
	var first error
	for i, err := range errs {
		if err != nil {
			results[i].Err = err
			if first == nil {
				first = fmt.Errorf("kafka: list offsets of %s/%d: %w", queries[i].Topic, queries[i].Partition, err)
			}
		}
	}
	return results, first
}

// Offset returns one partition's offset for ts (a time in milliseconds,
// OffsetLatest or OffsetEarliest), read uncommitted.
func (c *Client) Offset(ctx context.Context, topic string, partition int32, ts int64) (int64, error) {
	r, err := c.ListOffsets(ctx, false, OffsetQuery{Topic: topic, Partition: partition, Timestamp: ts})
	if err != nil {
		return -1, err
	}
	return r[0].Offset, nil
}

// OffsetAt returns one partition's first offset at or after t.
func (c *Client) OffsetAt(ctx context.Context, topic string, partition int32, t time.Time) (int64, error) {
	return c.Offset(ctx, topic, partition, t.UnixMilli())
}

func (c *Client) listOffsetsOn(ctx context.Context, bc *brokerConn, readCommitted bool, queries []OffsetQuery, idx []int, results []OffsetResult) []error {
	v, err := bc.version(apiListOffsets)
	if err != nil {
		return fill(len(idx), err)
	}
	var topics []string
	byTopic := map[string][]int{}
	for _, i := range idx {
		t := queries[i].Topic
		if _, ok := byTopic[t]; !ok {
			topics = append(topics, t)
		}
		byTopic[t] = append(byTopic[t], i)
	}
	var e encoder
	e.int32(-1)
	if v >= 2 {
		if readCommitted {
			e.int8(1)
		} else {
			e.int8(0)
		}
	}
	e.int32(int32(len(topics)))
	for _, t := range topics {
		e.string(t)
		e.int32(int32(len(byTopic[t])))
		for _, i := range byTopic[t] {
			e.int32(queries[i].Partition)
			if v >= 4 {
				e.int32(-1) // current leader epoch
			}
			e.int64(queries[i].Timestamp)
		}
	}
	resp, err := bc.roundTrip(ctx, apiListOffsets, v, e.b, c.cfg.RequestTimeout)
	if err != nil {
		return fill(len(idx), err)
	}
	where := make(map[part]int, len(idx))
	for j, i := range idx {
		where[part{queries[i].Topic, queries[i].Partition}] = j
	}
	errs := fill(len(idx), &ConnError{Addr: bc.addr, Err: errors.New("ListOffsets: a partition missing from the answer")})
	d := decoder{b: resp}
	if v >= 2 {
		d.int32()
	}
	for range d.arrayLen() {
		t := d.string()
		for range d.arrayLen() {
			p := d.int32()
			code := Error(d.int16())
			ts := d.int64()
			off := d.int64()
			if v >= 4 {
				d.int32()
			}
			j, ok := where[part{t, p}]
			if !ok || d.err != nil {
				continue
			}
			if code != 0 {
				errs[j] = code
				continue
			}
			errs[j] = nil
			results[idx[j]].Offset, results[idx[j]].Timestamp = off, ts
		}
	}
	if d.err != nil {
		return fill(len(idx), &ConnError{Addr: bc.addr, Err: fmt.Errorf("ListOffsets: %w", d.err)})
	}
	return errs
}

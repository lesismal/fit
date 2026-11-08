package kafkawire

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
)

// FetchOptions are a fetch call's settings.
type FetchOptions struct {
	// MaxWait is how long a broker waits for MinBytes (500ms).
	MaxWait time.Duration
	// MinBytes is what a broker waits for before answering (1).
	MinBytes int32
	// MaxBytes bounds an answer of one broker (50 MiB), and
	// PartitionMaxBytes a partition's part of it (1 MiB); a partition's
	// first batch comes whole whatever its size.
	MaxBytes, PartitionMaxBytes int32
	// ReadCommitted reads only committed transactions' records, up to the
	// last stable offset; otherwise every record up to the high
	// watermark.
	ReadCommitted bool
}

// FetchPartition is a partition to read from Offset on.
type FetchPartition struct {
	Topic     string
	Partition int32
	Offset    int64
}

// FetchResult is what a partition gave: its records at or after the
// offset asked, and NextOffset, the offset to ask for next (past the last
// whole batch read, which may be past the last record, or the offset
// asked when nothing came). Err is the partition's error:
// OFFSET_OUT_OF_RANGE when the offset is not in the log, for the caller
// to decide where to start.
type FetchResult struct {
	Topic            string
	Partition        int32
	Records          []Record
	NextOffset       int64
	HighWatermark    int64
	LastStableOffset int64
	LogStartOffset   int64
	Err              error
}

// Fetch reads the partitions from their leaders, the leaders in parallel,
// each broker waiting up to MaxWait for data, and retries partitions whose
// leader moved or whose connection failed (without waiting, once another
// partition has records). Fetches go on connections of their own, so that
// a broker's wait holds no produce or metadata request. The results are in
// the partitions' order; the error is the first partition's error, if any.
func (c *Client) Fetch(ctx context.Context, opts FetchOptions, parts ...FetchPartition) ([]FetchResult, error) {
	if opts.MaxWait <= 0 {
		opts.MaxWait = 500 * time.Millisecond
	}
	if opts.MinBytes <= 0 {
		opts.MinBytes = 1
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 50 << 20
	}
	if opts.PartitionMaxBytes <= 0 {
		opts.PartitionMaxBytes = 1 << 20
	}
	results := make([]FetchResult, len(parts))
	ps := make([]part, len(parts))
	seen := make(map[part]bool, len(parts))
	for i, p := range parts {
		ps[i] = part{p.Topic, p.Partition}
		results[i] = FetchResult{Topic: p.Topic, Partition: p.Partition, NextOffset: p.Offset,
			HighWatermark: -1, LastStableOffset: -1, LogStartOffset: -1}
		if seen[ps[i]] {
			return results, fmt.Errorf("kafka: %s/%d asked twice in one fetch", p.Topic, p.Partition)
		}
		seen[ps[i]] = true
	}
	var got atomic.Bool
	errs := c.routed(ctx, true, ps, func(ctx context.Context, bc *brokerConn, idx []int) []error {
		o := opts
		if got.Load() {
			o.MaxWait, o.MinBytes = 0, 1
		}
		errs := c.fetchOn(ctx, bc, o, parts, idx, results)
		for j, i := range idx {
			if errs[j] == nil && len(results[i].Records) > 0 {
				got.Store(true)
			}
		}
		return errs
	})
	var first error
	for i, err := range errs {
		if err != nil {
			results[i].Err = err
			if first == nil {
				first = fmt.Errorf("kafka: fetch from %s/%d: %w", parts[i].Topic, parts[i].Partition, err)
			}
		}
	}
	return results, first
}

func (c *Client) fetchOn(ctx context.Context, bc *brokerConn, opts FetchOptions, parts []FetchPartition, idx []int, results []FetchResult) []error {
	v, err := bc.version(apiFetch)
	if err != nil {
		return fill(len(idx), err)
	}
	var topics []string
	byTopic := map[string][]int{}
	for _, i := range idx {
		t := parts[i].Topic
		if _, ok := byTopic[t]; !ok {
			topics = append(topics, t)
		}
		byTopic[t] = append(byTopic[t], i)
	}
	var e encoder
	e.int32(-1) // replica ID: a consumer
	e.int32(int32(opts.MaxWait / time.Millisecond))
	e.int32(opts.MinBytes)
	e.int32(opts.MaxBytes)
	if opts.ReadCommitted {
		e.int8(1)
	} else {
		e.int8(0)
	}
	if v >= 7 {
		e.int32(0)  // session ID: none
		e.int32(-1) // session epoch: a full fetch, no session
	}
	e.int32(int32(len(topics)))
	for _, t := range topics {
		e.string(t)
		e.int32(int32(len(byTopic[t])))
		for _, i := range byTopic[t] {
			e.int32(parts[i].Partition)
			if v >= 9 {
				e.int32(-1) // current leader epoch: not checked
			}
			e.int64(parts[i].Offset)
			if v >= 5 {
				e.int64(-1) // log start offset: a consumer's is none
			}
			e.int32(opts.PartitionMaxBytes)
		}
	}
	if v >= 7 {
		e.int32(0) // forgotten topics
	}
	if v >= 11 {
		e.string("") // rack
	}
	resp, err := bc.roundTrip(ctx, apiFetch, v, e.b, c.cfg.RequestTimeout+opts.MaxWait)
	if err != nil {
		return fill(len(idx), err)
	}
	where := make(map[part]int, len(idx))
	pos := make(map[int]int, len(idx))
	for j, i := range idx {
		where[part{parts[i].Topic, parts[i].Partition}] = i
		pos[i] = j
	}
	errs := fill(len(idx), &ConnError{Addr: bc.addr, Err: errors.New("Fetch: a partition missing from the answer")})
	d := decoder{b: resp}
	d.int32() // throttle
	if v >= 7 {
		code := Error(d.int16())
		d.int32() // session ID
		if d.err == nil && code != 0 {
			return fill(len(idx), code)
		}
	}
	for range d.arrayLen() {
		t := d.string()
		for range d.arrayLen() {
			p := d.int32()
			code := Error(d.int16())
			hw := d.int64()
			lso := d.int64()
			lstart := int64(-1)
			if v >= 5 {
				lstart = d.int64()
			}
			var aborted []abortedTxn
			if n := d.int32(); n > 0 {
				if int(n) > d.remaining()/16 {
					d.fail(errShort)
				} else {
					aborted = make([]abortedTxn, n)
					for k := range aborted {
						aborted[k] = abortedTxn{producerID: d.int64(), firstOffset: d.int64()}
					}
				}
			}
			if v >= 11 {
				d.int32() // preferred read replica: the leader is read
			}
			set := d.bytes()
			if d.err != nil {
				break
			}
			i, ok := where[part{t, p}]
			if !ok {
				continue
			}
			j := pos[i]
			r := &results[i]
			r.HighWatermark, r.LastStableOffset, r.LogStartOffset = hw, lso, lstart
			if code != 0 {
				errs[j] = code
				continue
			}
			slices.SortFunc(aborted, func(a, b abortedTxn) int { return cmp.Compare(a.firstOffset, b.firstOffset) })
			br := batchReader{maxBatchBytes: c.cfg.MaxBatchBytes, dec: &c.dec}
			if opts.ReadCommitted {
				br.aborted = aborted
			}
			recs, next, err := br.read(set, parts[i].Offset, nil)
			if err != nil {
				errs[j] = err
				continue
			}
			r.Records, r.NextOffset, errs[j] = recs, next, nil
		}
	}
	if d.err != nil {
		return fill(len(idx), &ConnError{Addr: bc.addr, Err: fmt.Errorf("Fetch: %w", d.err)})
	}
	return errs
}

package syncloki

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/bits"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/s2"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/pb"
	"github.com/lesismal/fit/sync/internal/tmpl"
)

// entry is a record as the sink pushes it.
type entry struct {
	tenant string
	stream string   // the labels, as Loki writes a stream's: {a="x", b="y"}
	pairs  []string // the labels' names and values, in order
	ts     time.Time
	line   []byte   // in the scratch's arena, for the Write
	meta   []string // the metadata's names and values
	size   int
	err    error
}

// scratch is what a Write reuses from the one before: the entries, the
// arena their lines are encoded into, the labels' values, the streams of
// a push, and the bodies.
type scratch struct {
	entries []entry
	arena   []byte
	vals    []string
	one     [1]*fitsync.Record
	sts     []stream
	by      map[string]int
	body    []byte
	packed  []byte
}

// maxKept bounds a scratch buffer kept for the next Write.
const maxKept = 32 << 20

var scratches = sync.Pool{New: func() any { return new(scratch) }}

// release returns sc to the pool, holding no record's strings.
func release(sc *scratch) {
	clear(sc.entries)
	sc.entries = sc.entries[:0]
	clear(sc.vals)
	all := sc.sts[:cap(sc.sts)]
	for i := range all {
		all[i].labels, all[i].pairs = "", nil
	}
	clear(sc.by)
	if cap(sc.arena) > maxKept {
		sc.arena = nil
	}
	if cap(sc.body) > maxKept || cap(sc.packed) > maxKept {
		sc.body, sc.packed = nil, nil
	}
	scratches.Put(sc)
}

var escaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// escape escapes a label's value as Loki's stream selector has it.
func escape(s string) string {
	if !strings.ContainsAny(s, "\\\"\n") {
		return s
	}
	return escaper.Replace(s)
}

// labelsOf makes r's stream: prev's (the record before's) when r's labels'
// values are its, so that a run of one stream makes it once.
func (s *Sink) labelsOf(r *fitsync.Record, sc *scratch, prev *entry) (string, []string) {
	vals := sc.vals[:0]
	for _, l := range s.labels {
		vals = append(vals, l.tmpl.Render(r))
	}
	sc.vals = vals
	if prev != nil && prev.pairs != nil {
		k, same := 0, true
		for i, v := range vals {
			if v == "" {
				continue
			}
			if k+1 >= len(prev.pairs) || prev.pairs[k] != s.labels[i].name || prev.pairs[k+1] != v {
				same = false
				break
			}
			k += 2
		}
		if same && k == len(prev.pairs) {
			return prev.stream, prev.pairs
		}
	}
	var pairs []string
	var sb strings.Builder
	sb.WriteByte('{')
	for i, v := range vals {
		if v == "" {
			continue
		}
		if len(pairs) > 0 {
			sb.WriteString(", ")
		}
		pairs = append(pairs, s.labels[i].name, v)
		sb.WriteString(s.labels[i].name)
		sb.WriteString(`="`)
		sb.WriteString(escape(v))
		sb.WriteByte('"')
	}
	sb.WriteByte('}')
	return sb.String(), pairs
}

var newline = []byte{'\n'}

// build makes r's entry, now being the time of a record that has none, its
// line encoded into sc's arena; prev is the entry before it, if any.
func (s *Sink) build(r *fitsync.Record, now time.Time, sc *scratch, prev *entry) entry {
	var e entry
	e.tenant = s.Tenant
	row := tmpl.Row(r)
	if s.TenantField != "" {
		if v, ok := row.Get(s.TenantField); ok && !v.IsNull() {
			e.tenant = fitsync.TextOf(v)
			if strings.ContainsAny(e.tenant, "\r\n|") || e.tenant == "" {
				return entry{err: fitsync.Permanent(fmt.Errorf("the tenant %q has a line break or |", e.tenant))}
			}
		}
	}
	e.stream, e.pairs = s.labelsOf(r, sc, prev)
	if len(e.pairs) == 0 {
		return entry{err: fitsync.Permanent(errors.New("the record's labels are all empty: Loki needs one"))}
	}
	for _, m := range s.meta {
		if v := m.tmpl.Render(r); v != "" {
			e.meta = append(e.meta, m.name, v)
		}
	}
	start := len(sc.arena)
	sc.one[0] = r
	b, err := s.codec.Encode(sc.arena, sc.one[:])
	sc.one[0] = nil
	if err != nil {
		return entry{err: fitsync.Permanent(err)}
	}
	// The arena grows by appending: a line encoded before keeps the array
	// it was encoded into.
	sc.arena = b
	e.line = bytes.TrimSuffix(b[start:len(b):len(b)], newline)
	e.ts = r.CommitTime
	if s.TimestampField != "" {
		if v, ok := row.Get(s.TimestampField); ok {
			if t, ok := tmpl.TimeOf(v); ok {
				e.ts = t
			}
		}
	}
	if e.ts.IsZero() {
		e.ts = now
	}
	e.size = len(e.line) + len(e.stream) + 16
	for _, m := range e.meta {
		e.size += len(m) + 2
	}
	if int64(e.size) > s.maxRequest() {
		return entry{err: fitsync.Permanent(fmt.Errorf("the line is %d bytes, more than max_request_bytes", len(e.line)))}
	}
	return e
}

// Write pushes the batch in order, a request for each run of one tenant's
// records up to max_request_bytes. It returns nil once every record is
// pushed, or a BatchError at the first one that is not: those before it
// are pushed.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	sc := scratches.Get().(*scratch)
	defer release(sc)
	now := time.Now()
	sc.arena = sc.arena[:0]
	entries := slices.Grow(sc.entries[:0], len(batch))
	for i, r := range batch {
		var prev *entry
		if i > 0 {
			prev = &entries[i-1]
		}
		entries = append(entries, s.build(r, now.Add(time.Duration(i)), sc, prev))
	}
	sc.entries = entries
	limit := s.maxRequest()
	for i := 0; i < len(entries); {
		if entries[i].err != nil {
			return &fitsync.BatchError{Index: i, Err: entries[i].err}
		}
		j, size := i, int64(0)
		for j < len(entries) && entries[j].err == nil && entries[j].tenant == entries[i].tenant {
			if j > i && size+int64(entries[j].size) > limit {
				break
			}
			size += int64(entries[j].size)
			j++
		}
		if bad, err := s.pushRun(ctx, sc, entries[i:j]); err != nil {
			return &fitsync.BatchError{Index: i + bad, Err: err}
		}
		i = j
	}
	return nil
}

// pushRun pushes a run; one Loki refuses (400, or 413) is split in two,
// each half pushed, until the one entry it refuses is found. It returns
// the index, in the run, of the entry that failed, those before it pushed.
func (s *Sink) pushRun(ctx context.Context, sc *scratch, es []entry) (int, error) {
	err := s.push(ctx, sc, es)
	if err == nil {
		return 0, nil
	}
	if !fibrest.IsStatus(err, stdhttp.StatusBadRequest, stdhttp.StatusRequestEntityTooLarge) {
		return 0, err
	}
	if len(es) == 1 {
		return 0, fitsync.Permanent(err)
	}
	h := len(es) / 2
	if bad, err := s.pushRun(ctx, sc, es[:h]); err != nil {
		return bad, err
	}
	bad, err := s.pushRun(ctx, sc, es[h:])
	if err != nil {
		return h + bad, err
	}
	return 0, nil
}

// stream is a stream's entries in a push, by their index, in time order.
type stream struct {
	labels string
	pairs  []string
	idx    []int32
}

// streams groups es by stream, the streams in the order they first come,
// each one's entries in time order (those of one time as they came), as
// Loki takes them best.
func (sc *scratch) streams(es []entry) []stream {
	sts := sc.sts[:0]
	if sc.by == nil {
		sc.by = map[string]int{}
	}
	clear(sc.by)
	last := -1
	for i := range es {
		e := &es[i]
		k := last
		if k < 0 || sts[k].labels != e.stream {
			var ok bool
			if k, ok = sc.by[e.stream]; !ok {
				k = len(sts)
				if k < cap(sts) {
					sts = sts[:k+1]
					sts[k].idx = sts[k].idx[:0]
				} else {
					sts = append(sts, stream{})
				}
				sts[k].labels, sts[k].pairs = e.stream, e.pairs
				sc.by[e.stream] = k
			}
			last = k
		}
		sts[k].idx = append(sts[k].idx, int32(i))
	}
	byTime := func(a, b int32) int { return es[a].ts.Compare(es[b].ts) }
	for _, st := range sts {
		if !slices.IsSortedFunc(st.idx, byTime) {
			slices.SortStableFunc(st.idx, byTime)
		}
	}
	sc.sts = sts
	return sts
}

// push sends one request of es, all of one tenant.
func (s *Sink) push(ctx context.Context, sc *scratch, es []entry) error {
	return s.send(ctx, sc, es[0].tenant, sc.streams(es), es)
}

// send pushes sts, of es, as tenant.
func (s *Sink) send(ctx context.Context, sc *scratch, tenant string, sts []stream, es []entry) error {
	h := stdhttp.Header{}
	if tenant != "" {
		h.Set("X-Scope-OrgID", tenant)
	}
	var body []byte
	if s.Encoding == "json" {
		h.Set("Content-Type", "application/json")
		sc.body = appendJSON(sc.body[:0], sts, es)
		body = sc.body
	} else {
		h.Set("Content-Type", "application/x-protobuf")
		sc.body = appendProto(sc.body[:0], sts, es)
		sc.packed = s2.EncodeSnappy(sc.packed[:0], sc.body)
		body = sc.packed
	}
	resp, err := s.client.DoRetry(ctx, fibrest.Request{Method: stdhttp.MethodPost, Path: pushPath, Header: h, Body: body}, 0)
	if ctx.Err() != nil {
		// The client may be reading the body still: it is not reused.
		sc.body, sc.packed = nil, nil
	}
	if err != nil {
		return err
	}
	if resp.Status/100 != 2 {
		return fibrest.StatusError("the push", resp)
	}
	return nil
}

// varLen is the length of v's varint.
func varLen(v uint64) int { return (bits.Len64(v|1) + 6) / 7 }

// fieldLen is the length of a length-delimited field of n bytes, its
// number below 16.
func fieldLen(n int) int { return 1 + varLen(uint64(n)) + n }

// tsLen is the length of an entry's timestamp message.
func tsLen(e *entry) int {
	n := 0
	if sec := e.ts.Unix(); sec != 0 {
		n += 1 + varLen(uint64(sec))
	}
	if ns := e.ts.Nanosecond(); ns != 0 {
		n += 1 + varLen(uint64(ns))
	}
	return n
}

// metaLen is the length of a structured metadata's message.
func metaLen(name, value string) int {
	n := 0
	if name != "" {
		n += fieldLen(len(name))
	}
	if value != "" {
		n += fieldLen(len(value))
	}
	return n
}

// entryLen is the length of an entry's message.
func entryLen(e *entry) int {
	n := fieldLen(tsLen(e))
	if len(e.line) > 0 {
		n += fieldLen(len(e.line))
	}
	for k := 0; k+1 < len(e.meta); k += 2 {
		n += fieldLen(metaLen(e.meta[k], e.meta[k+1]))
	}
	return n
}

// appendProto appends a PushRequest of the streams (logproto's: streams 1;
// a stream's labels 1 and entries 2; an entry's timestamp 1, line 2 and
// structured metadata 3, each a name 1 and a value 2), each message's
// length reckoned before it, so that nothing is moved.
func appendProto(b []byte, sts []stream, es []entry) []byte {
	for _, st := range sts {
		n := 0
		if st.labels != "" {
			n += fieldLen(len(st.labels))
		}
		for _, i := range st.idx {
			n += fieldLen(entryLen(&es[i]))
		}
		b = slices.Grow(b, fieldLen(n))
		b = pb.AppendVarint(pb.AppendTag(b, 1, pb.Bytes), uint64(n))
		b = pb.AppendString(b, 1, st.labels)
		for _, i := range st.idx {
			b = appendEntry(b, &es[i])
		}
	}
	return b
}

// appendEntry appends an entry's message.
func appendEntry(b []byte, e *entry) []byte {
	b = pb.AppendVarint(pb.AppendTag(b, 2, pb.Bytes), uint64(entryLen(e)))
	b = pb.AppendVarint(pb.AppendTag(b, 1, pb.Bytes), uint64(tsLen(e)))
	b = pb.AppendInt(b, 1, e.ts.Unix())
	b = pb.AppendInt(b, 2, int64(e.ts.Nanosecond()))
	if len(e.line) > 0 {
		b = pb.AppendBytes(b, 2, e.line)
	}
	for k := 0; k+1 < len(e.meta); k += 2 {
		b = pb.AppendVarint(pb.AppendTag(b, 3, pb.Bytes), uint64(metaLen(e.meta[k], e.meta[k+1])))
		b = pb.AppendString(b, 1, e.meta[k])
		b = pb.AppendString(b, 2, e.meta[k+1])
	}
	return b
}

// appendJSON appends the JSON push of the streams.
func appendJSON(b []byte, sts []stream, es []entry) []byte {
	b = append(b, `{"streams":[`...)
	for i, st := range sts {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, `{"stream":{`...)
		for k := 0; k+1 < len(st.pairs); k += 2 {
			if k > 0 {
				b = append(b, ',')
			}
			b = append(fitsync.AppendJSONString(b, st.pairs[k]), ':')
			b = fitsync.AppendJSONString(b, st.pairs[k+1])
		}
		b = append(b, `},"values":[`...)
		for n, x := range st.idx {
			e := &es[x]
			if n > 0 {
				b = append(b, ',')
			}
			b = append(b, `["`...)
			b = strconv.AppendInt(b, e.ts.UnixNano(), 10)
			b = append(b, `",`...)
			b = fitsync.AppendJSONString(b, string(e.line))
			if len(e.meta) > 0 {
				b = append(b, ",{"...)
				for k := 0; k+1 < len(e.meta); k += 2 {
					if k > 0 {
						b = append(b, ',')
					}
					b = append(fitsync.AppendJSONString(b, e.meta[k]), ':')
					b = fitsync.AppendJSONString(b, e.meta[k+1])
				}
				b = append(b, '}')
			}
			b = append(b, ']')
		}
		b = append(b, "]}"...)
	}
	return append(b, "]}"...)
}

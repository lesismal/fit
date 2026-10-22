package observe

import (
	"bytes"
	"context"
	hexenc "encoding/hex"
	mathrand "math/rand/v2"
	stdhttp "net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/lesismal/fit/config"
)

// Traces follow W3C Trace Context: a request's traceparent field names its
// trace and the caller's span, and says whether the caller sampled it. fit
// decides by it, or by telemetry.traces.sample_ratio for a request without
// one, and for a request it samples it starts a server span of its own,
// sends a traceparent naming that span on to the upstream, and, once the
// response is finished, writes the span as OTLP JSON into a buffer that a
// goroutine of its own exports. A request it does not sample costs a field
// lookup: its traceparent, if any, goes on to the upstream as it came, with
// the rest of its fields. tracestate, which fit adds nothing to, goes on as
// it came either way. See docs/decisions/015-opentelemetry-without-the-sdk.md.
//
// As the access log's, the buffer is split in shards, each with its own
// lock, by the request's Entry; a shard hands what it holds to the exporter
// once it holds spanFlushBytes. Spans past max_queue_bytes are dropped and
// counted rather than waited for.

// spanFlushBytes is how many bytes of spans make an export before the
// interval is up.
const spanFlushBytes = 256 << 10

// Trace is a request's place in a trace: the trace, the span fit began for
// it, and the caller's span, whose IDs are all zero when it had none.
type Trace struct {
	TraceID [16]byte
	SpanID  [8]byte
	Parent  [8]byte
}

// Traceparent returns the traceparent field naming t's span, sampled.
func (t *Trace) Traceparent() string {
	var b [55]byte
	copy(b[:], "00-")
	hexenc.Encode(b[3:35], t.TraceID[:])
	b[35] = '-'
	hexenc.Encode(b[36:52], t.SpanID[:])
	copy(b[52:], "-01")
	return string(b[:])
}

// parseTraceparent reads a traceparent field: version, trace ID, parent ID
// and flags, as version 00 has them, and as a later version begins with
// them. ok is false for one that is not valid, which is then not followed.
func parseTraceparent(v string) (trace [16]byte, parent [8]byte, sampled, ok bool) {
	if len(v) < 55 || v[2] != '-' || v[35] != '-' || v[52] != '-' || len(v) > 55 && (v[:2] == "00" || v[55] != '-') {
		return trace, parent, false, false
	}
	var version, flags [1]byte
	if !lowerHex(v[:2]) || !lowerHex(v[3:35]) || !lowerHex(v[36:52]) || !lowerHex(v[53:55]) {
		return trace, parent, false, false
	}
	hexenc.Decode(version[:], []byte(v[:2]))
	hexenc.Decode(trace[:], []byte(v[3:35]))
	hexenc.Decode(parent[:], []byte(v[36:52]))
	hexenc.Decode(flags[:], []byte(v[53:55]))
	if version[0] == 0xff || trace == [16]byte{} || parent == [8]byte{} {
		return trace, parent, false, false
	}
	return trace, parent, flags[0]&1 == 1, true
}

// sampledFlag reports whether a traceparent's flags, where version 00 has
// them, say sampled, without checking the rest.
func sampledFlag(v string) bool {
	if len(v) < 55 {
		return false
	}
	c := v[54]
	return c == '1' || c == '3' || c == '5' || c == '7' || c == '9' || c == 'b' || c == 'd' || c == 'f'
}

func lowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// Tracer samples requests, records their spans and exports them over
// OTLP/HTTP. It is safe for concurrent use.
type Tracer struct {
	threshold uint64 // a request without a sampled caller is sampled when a random uint64 is below it; 0 never, and every one when all is set
	all       bool
	maxBytes  int64
	interval  time.Duration
	exp       *exporter

	shards []spanShard
	queued atomic.Int64 // bytes in the shards
	ready  chan struct{}

	dropped  atomic.Int64 // spans dropped for want of room
	exported atomic.Int64 // spans the receiver took
	failed   atomic.Int64 // spans in exports that failed

	taken [][]byte // the buffers an export took, by shard, to give back; the exporter's alone

	stop chan struct{}
	done chan struct{}
	once sync.Once
}

type spanShard struct {
	mu    sync.Mutex
	buf   []byte // spans, each followed by a comma
	spare []byte // a buffer an export gave back, for the next
	spans int
	_     [8]byte
}

// NewTracer returns a tracer that samples and exports as c says, and starts
// its exporter; Close stops it. logf hears exports that fail.
func NewTracer(c *config.Telemetry, logf func(format string, args ...any)) (*Tracer, error) {
	exp, err := newExporter(c, "/v1/traces", logf)
	if err != nil {
		return nil, err
	}
	tr := c.Traces
	t := &Tracer{maxBytes: tr.QueueBytes(), interval: tr.Every(), exp: exp,
		shards: make([]spanShard, metricShards), ready: make(chan struct{}, 1),
		stop: make(chan struct{}), done: make(chan struct{})}
	switch r := tr.SampleRatio; {
	case r >= 1:
		t.all = true
	case r > 0:
		t.threshold = uint64(r * (1 << 63) * 2)
	}
	go t.run()
	return t, nil
}

// Sample decides whether r is traced, and if so returns its place in the
// trace: under the caller's trace when its traceparent says it sampled
// it, or a new trace, as the sample ratio has it, when it has none or one
// that is not valid. A caller that did not sample it is followed.
func (t *Tracer) Sample(r *stdhttp.Request) (Trace, bool) {
	var tc Trace
	if v := r.Header["Traceparent"]; len(v) == 1 {
		if !t.all && t.threshold == 0 && !sampledFlag(v[0]) {
			// Not sampled whether it is valid or not: nothing to read.
			return tc, false
		}
		trace, parent, sampled, ok := parseTraceparent(v[0])
		if ok {
			if !sampled {
				return tc, false
			}
			tc.TraceID, tc.Parent = trace, parent
			putUint64(tc.SpanID[:], nonZero())
			return tc, true
		}
	}
	if !t.all && (t.threshold == 0 || mathrand.Uint64() >= t.threshold) {
		return tc, false
	}
	putUint64(tc.TraceID[:8], mathrand.Uint64())
	putUint64(tc.TraceID[8:], nonZero())
	putUint64(tc.SpanID[:], nonZero())
	return tc, true
}

func nonZero() uint64 {
	for {
		if v := mathrand.Uint64(); v != 0 {
			return v
		}
	}
}

func putUint64(b []byte, v uint64) {
	for i := range 8 {
		b[i] = byte(v >> (56 - 8*i))
	}
}

// End records the span of e's request r, answered with status and a body of
// size bytes, ending now. It never waits for the exporter: a span that does
// not fit is dropped.
func (t *Tracer) End(e *Entry, r *stdhttp.Request, status int, size int64) {
	var stack [1024]byte
	span := appendSpan(stack[:0], e, r, status, size, time.Now())
	sh := &t.shards[pickShard(uintptr(unsafe.Pointer(e)))]
	sh.mu.Lock()
	if t.queued.Load()+int64(len(span)) > t.maxBytes {
		sh.mu.Unlock()
		t.dropped.Add(1)
		return
	}
	if sh.buf == nil {
		sh.buf, sh.spare = sh.spare, nil
	}
	sh.buf = append(sh.buf, span...)
	sh.spans++
	queued := t.queued.Add(int64(len(span)))
	full := len(sh.buf) >= spanFlushBytes
	sh.mu.Unlock()
	if queued >= spanFlushBytes || full {
		select {
		case t.ready <- struct{}{}:
		default:
		}
	}
}

// appendSpan appends e's span as an OTLP JSON object, and a comma.
func appendSpan(b []byte, e *Entry, r *stdhttp.Request, status int, size int64, end time.Time) []byte {
	tc := e.Trace()
	b = append(b, `{"traceId":"`...)
	b = hexenc.AppendEncode(b, tc.TraceID[:])
	b = append(b, `","spanId":"`...)
	b = hexenc.AppendEncode(b, tc.SpanID[:])
	if tc.Parent != [8]byte{} {
		b = append(b, `","parentSpanId":"`...)
		b = hexenc.AppendEncode(b, tc.Parent[:])
	}
	b = append(b, `","name":`...)
	b = appendString(b, r.Method)
	if e.Route != "" {
		// "METHOD route": the route's string, its opening quote taken out,
		// after the method's, its closing one taken out.
		b[len(b)-1] = ' '
		start := len(b)
		b = appendString(b, e.Route)
		copy(b[start:], b[start+1:])
		b = b[:len(b)-1]
	}
	b = append(b, `,"kind":2,"startTimeUnixNano":"`...)
	b = strconv.AppendInt(b, e.Start.UnixNano(), 10)
	b = append(b, `","endTimeUnixNano":"`...)
	b = strconv.AppendInt(b, end.UnixNano(), 10)
	b = append(b, `","attributes":[`...)
	b = appendAttr(b, "http.request.method", r.Method, true)
	b = appendAttr(b, "url.path", r.URL.Path, false)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	b = appendAttr(b, "url.scheme", scheme, false)
	b = appendAttr(b, "server.address", r.Host, false)
	b = appendAttr(b, "client.address", clientIP(r.RemoteAddr), false)
	b = appendAttr(b, "network.protocol.version", protocolVersion(r), false)
	b = appendIntAttr(b, "http.response.status_code", int64(status))
	b = appendIntAttr(b, "http.response.body.size", size)
	b = appendAttr(b, "fit.listener", e.Listener, false)
	if e.Route != "" {
		b = appendAttr(b, "fit.route", e.Route, false)
	}
	if e.Upstream != "" {
		b = appendAttr(b, "fit.upstream.address", e.Upstream, false)
	}
	b = append(b, ']')
	if status >= 500 {
		b = append(b, `,"status":{"code":2}`...)
	}
	return append(b, "},"...)
}

func appendAttr(b []byte, key, value string, first bool) []byte {
	if !first {
		b = append(b, ',')
	}
	b = append(b, `{"key":"`...)
	b = append(b, key...)
	b = append(b, `","value":{"stringValue":`...)
	b = appendString(b, value)
	return append(b, "}}"...)
}

func appendIntAttr(b []byte, key string, v int64) []byte {
	b = append(b, `,{"key":"`...)
	b = append(b, key...)
	b = append(b, `","value":{"intValue":"`...)
	b = strconv.AppendInt(b, v, 10)
	return append(b, `"}}`...)
}

func protocolVersion(r *stdhttp.Request) string {
	switch r.ProtoMajor {
	case 2:
		return "2"
	case 3:
		return "3"
	}
	if r.ProtoMinor == 0 {
		return "1.0"
	}
	return "1.1"
}

// Dropped, Exported and Failed count the spans dropped for want of room,
// those exported, and those in exports that failed.
func (t *Tracer) Dropped() int64  { return t.dropped.Load() }
func (t *Tracer) Exported() int64 { return t.exported.Load() }
func (t *Tracer) Failed() int64   { return t.failed.Load() }

// run exports the spans every interval, and sooner when enough wait, until
// Close.
func (t *Tracer) run() {
	defer close(t.done)
	tick := time.NewTicker(t.interval)
	defer tick.Stop()
	for {
		select {
		case <-t.stop:
			t.export()
			return
		case <-tick.C:
		case <-t.ready:
		}
		t.export()
	}
}

// export takes what the shards hold and sends it as one request.
func (t *Tracer) export() {
	// A body of its own each time: the client may still read one after
	// the request is answered.
	var body bytes.Buffer
	if t.taken == nil {
		t.taken = make([][]byte, len(t.shards))
	}
	spans := 0
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		buf, n := sh.buf, sh.spans
		sh.buf, sh.spans = nil, 0
		t.queued.Add(-int64(len(buf)))
		sh.mu.Unlock()
		t.taken[i] = buf
		if n == 0 {
			continue
		}
		if spans == 0 {
			body.WriteString(`{"resourceSpans":[{"resource":`)
			body.Write(t.exp.resource)
			body.WriteString(`,"scopeSpans":[{"scope":{"name":"fit"},"spans":[`)
		}
		body.Write(buf)
		spans += n
	}
	// The buffers go back to their shards, for spans to come.
	defer func() {
		for i, buf := range t.taken {
			if buf == nil || cap(buf) > 2*spanFlushBytes {
				continue
			}
			sh := &t.shards[i]
			sh.mu.Lock()
			if sh.spare == nil {
				sh.spare = buf[:0]
			}
			sh.mu.Unlock()
			t.taken[i] = nil
		}
	}()
	if spans == 0 {
		return
	}
	body.Truncate(body.Len() - 1) // the last span's comma
	body.WriteString("]}]}]}")
	b := body.Bytes()
	if err := t.exp.post(context.Background(), b); err != nil {
		t.failed.Add(int64(spans))
		t.exp.report("traces", err)
		return
	}
	t.exp.report("traces", nil)
	t.exported.Add(int64(spans))
}

// Close exports what waits and stops the exporter.
func (t *Tracer) Close() error {
	t.once.Do(func() { close(t.stop) })
	<-t.done
	return nil
}

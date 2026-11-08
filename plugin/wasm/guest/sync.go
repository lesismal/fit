//go:build wasip1

package guest

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unsafe"
)

// The sync side of the ABI: a plugin registers, in an init function, what
// runs for the calls it takes (OnTransform, OnDecode, OnEncode, OnPoll,
// OnWrite), and reads its settings with Config and logs with Log, as a
// request plugin does. Records cross as their canonical JSON (see
// docs/decisions/036-sync.md): Record holds a record's parts, and Row and
// the value helpers read and make its typed values.

//go:wasmimport fit_sync input
func syncInput(buf, bufLen uint32) int32

//go:wasmimport fit_sync record_emit
func recordEmit(index int32, rec, recLen uint32)

//go:wasmimport fit_sync fail
func syncFail(index int32, msg, msgLen uint32)

//go:wasmimport fit_sync output
func syncOutput(buf, bufLen uint32)

//go:wasmimport fit_sync checkpoint
func syncCheckpoint(buf, bufLen uint32)

//go:wasmimport fit_sync http_request
func httpRequest(req, reqLen uint32) int32

//go:wasmimport fit_sync http_response
func httpResponse(part, buf, bufLen uint32) int32

//go:wasmimport fit_sync http_header
func httpHeader(name, nameLen, buf, bufLen uint32) int32

//go:wasmimport fit_sync sleep
func syncSleep(ms uint32)

// What the exports return.
const (
	statusOK       = 0
	statusCaughtUp = 1
	statusFailed   = 2
)

// Tx is a record's transaction.
type Tx struct {
	ID    string `json:"id,omitempty"`
	First bool   `json:"first,omitempty"`
	Last  bool   `json:"last,omitempty"`
}

// Record is a record's canonical JSON, its values left as JSON for Row and
// the value helpers to read.
type Record struct {
	Source     string          `json:"source,omitempty"`
	Namespace  string          `json:"namespace,omitempty"`
	Op         string          `json:"op,omitempty"`
	Key        json.RawMessage `json:"key,omitempty"`
	Before     json.RawMessage `json:"before,omitempty"`
	After      json.RawMessage `json:"after,omitempty"`
	CommitTime string          `json:"commit_time,omitempty"`
	Tx         *Tx             `json:"tx,omitempty"`
	Position   json.RawMessage `json:"position,omitempty"`
}

func bytesPtr(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

// input returns the call's input, n bytes long.
func input(n uint32) []byte {
	b := make([]byte, n)
	if n > 0 {
		p, l := bytesPtr(b)
		syncInput(p, l)
	}
	return b
}

func batch(n uint32) ([]Record, error) { return parseBatch(input(n)) }

// MarshalJSON returns r's canonical JSON.
func (r *Record) MarshalJSON() ([]byte, error) { return r.appendJSON(nil), nil }

// emitBuf is where Emit writes a record, for the host to read.
var emitBuf []byte

// failed reports err as the call's failure.
func failed(err error) int32 {
	Fail(-1, err.Error())
	return statusFailed
}

// Emit hands on r as what the input's record i becomes, for a transform;
// as a record of the bytes, for a decode, or of the poll, for a source,
// with i 0. A transform emits in order: i never goes back.
func Emit(i int, r *Record) {
	emitBuf = r.appendJSON(emitBuf[:0])
	p, n := bytesPtr(emitBuf)
	recordEmit(int32(i), p, n)
}

// Fail fails the input's record i, for msg: a transform's record is
// dropped and goes as on_error says; a sink's is where its write failed,
// those before it written. i -1 fails the whole call.
func Fail(i int, msg string) {
	p, n := str(msg)
	syncFail(int32(i), p, n)
}

var (
	onTransform func([]Record) error
	onDecode    func([]byte) error
	onEncode    func([]Record) ([]byte, error)
	onPoll      func([]byte) ([]byte, bool, error)
	onWrite     func([]Record) error
)

var errNoHandler = errors.New("the plugin registered nothing for this call")

// OnTransform sets fn to run over each batch of a transform: it calls Emit
// with what each record becomes, nothing to drop it, several to split it,
// and Fail for a record it refuses. An error fails the whole batch.
func OnTransform(fn func(batch []Record) error) { onTransform = fn }

// OnDecode sets fn to turn bytes into records, which it hands to Emit with
// index 0.
func OnDecode(fn func(data []byte) error) { onDecode = fn }

// OnEncode sets fn to turn records into bytes.
func OnEncode(fn func(batch []Record) ([]byte, error)) { onEncode = fn }

// OnPoll sets fn to read a source's next records from checkpoint, empty at
// the start: it hands them to Emit with index 0, and returns the checkpoint
// after them, nil to keep it, and whether it has caught up, so that fit
// waits before polling again, or stops the pipeline in mode once.
func OnPoll(fn func(checkpoint []byte) (next []byte, caughtUp bool, err error)) { onPoll = fn }

// OnWrite sets fn to write a sink's batch. A *RecordError says which
// record failed, those before it written; any other error that none was.
func OnWrite(fn func(batch []Record) error) { onWrite = fn }

// RecordError is a write's error about one record of its batch.
type RecordError struct {
	Index int
	Msg   string
}

func (e *RecordError) Error() string { return e.Msg }

//go:wasmexport fit_sync_transform
func fitSyncTransform(n uint32) int32 {
	if onTransform == nil {
		return failed(errNoHandler)
	}
	recs, err := batch(n)
	if err == nil {
		err = onTransform(recs)
	}
	if err != nil {
		return failed(err)
	}
	return statusOK
}

//go:wasmexport fit_sync_decode
func fitSyncDecode(n uint32) int32 {
	if onDecode == nil {
		return failed(errNoHandler)
	}
	if err := onDecode(input(n)); err != nil {
		return failed(err)
	}
	return statusOK
}

//go:wasmexport fit_sync_encode
func fitSyncEncode(n uint32) int32 {
	if onEncode == nil {
		return failed(errNoHandler)
	}
	recs, err := batch(n)
	var out []byte
	if err == nil {
		out, err = onEncode(recs)
	}
	if err != nil {
		return failed(err)
	}
	if len(out) > 0 {
		p, l := bytesPtr(out)
		syncOutput(p, l)
	}
	return statusOK
}

//go:wasmexport fit_sync_poll
func fitSyncPoll(n uint32) int32 {
	if onPoll == nil {
		return failed(errNoHandler)
	}
	next, caughtUp, err := onPoll(input(n))
	if err != nil {
		return failed(err)
	}
	if next != nil {
		p, l := bytesPtr(next)
		syncCheckpoint(p, l)
	}
	if caughtUp {
		return statusCaughtUp
	}
	return statusOK
}

//go:wasmexport fit_sync_write
func fitSyncWrite(n uint32) int32 {
	if onWrite == nil {
		return failed(errNoHandler)
	}
	recs, err := batch(n)
	if err == nil {
		err = onWrite(recs)
	}
	var re *RecordError
	if errors.As(err, &re) {
		Fail(re.Index, re.Msg)
		return statusFailed
	}
	if err != nil {
		return failed(err)
	}
	return statusOK
}

// Request is an HTTP request a source or a sink makes, to a host its
// allow_hosts names.
type Request struct {
	Method  string            `json:"method,omitempty"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

// Response is its answer.
type Response struct {
	Status int
	Body   []byte
}

// Do sends req through fit's HTTP client and returns its answer, or the
// error that kept it from one. A host not allowed fails the whole call.
func Do(req Request) (*Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	p, n := bytesPtr(b)
	status := httpRequest(p, n)
	if status < 0 {
		msg, _ := get(func(b, bn uint32) int32 { return httpResponse(1, b, bn) })
		return nil, errors.New(msg)
	}
	body, _ := get(func(b, bn uint32) int32 { return httpResponse(0, b, bn) })
	return &Response{Status: int(status), Body: []byte(body)}, nil
}

// HTTPHeader returns the field name of the last response Do had.
func HTTPHeader(name string) (string, bool) {
	p, n := str(name)
	return get(func(b, bn uint32) int32 { return httpHeader(p, n, b, bn) })
}

// Sleep waits d, for a source or a sink, within its call's timeout.
func Sleep(d time.Duration) { syncSleep(uint32(d.Milliseconds())) }

// Field is a field of a Row.
type Field struct {
	Name  string
	Value json.RawMessage
}

// Row is a map value's fields, in order, as {"map": {...}} holds them.
type Row []Field

// ParseRow reads v, a map value or null. The fields' values are slices of
// v.
func ParseRow(v json.RawMessage) (Row, error) {
	if len(v) == 0 || string(v) == "null" {
		return nil, nil
	}
	var inner []byte
	if err := members(v, func(name string, val []byte) error {
		if name == "map" {
			inner = val
		}
		return nil
	}); err != nil || inner == nil {
		return nil, errors.New("not a map value")
	}
	var row Row
	err := members(inner, func(name string, val []byte) error {
		row = append(row, Field{Name: name, Value: val})
		return nil
	})
	return row, err
}

// Get returns the field name.
func (r Row) Get(name string) (json.RawMessage, bool) {
	for _, f := range r {
		if f.Name == name {
			return f.Value, true
		}
	}
	return nil, false
}

// Set sets the field name to v, adding it at the end when it is new.
func (r *Row) Set(name string, v json.RawMessage) {
	for i, f := range *r {
		if f.Name == name {
			(*r)[i].Value = v
			return
		}
	}
	*r = append(*r, Field{name, v})
}

// Delete removes the field name.
func (r *Row) Delete(name string) {
	for i, f := range *r {
		if f.Name == name {
			*r = append((*r)[:i], (*r)[i+1:]...)
			return
		}
	}
}

// Value returns r as a map value.
func (r Row) Value() json.RawMessage {
	b := []byte(`{"map":{`)
	for i, f := range r {
		if i > 0 {
			b = append(b, ',')
		}
		b = appendQuote(b, f.Name)
		b = append(append(b, ':'), f.Value...)
	}
	return append(b, "}}"...)
}

// Int returns i as a value.
func Int(i int64) json.RawMessage { return []byte(`{"int":` + strconv.FormatInt(i, 10) + `}`) }

// String returns s as a value.
func String(s string) json.RawMessage { return appendQuote(nil, s) }

// Bool returns b as a value.
func Bool(b bool) json.RawMessage { return []byte(strconv.FormatBool(b)) }

// Null is the null value.
func Null() json.RawMessage { return []byte("null") }

// AsInt returns v's integer, an int or a uint's.
func AsInt(v json.RawMessage) (int64, bool) {
	var n int64
	ok := false
	_ = members(v, func(name string, val []byte) error {
		var err error
		switch name {
		case "int":
			n, err = strconv.ParseInt(string(val), 10, 64)
		case "uint":
			var u uint64
			u, err = strconv.ParseUint(string(val), 10, 64)
			n = int64(u)
		default:
			return nil
		}
		ok = err == nil
		return nil
	})
	return n, ok
}

// AsString returns v's string.
func AsString(v json.RawMessage) (string, bool) {
	i := skipSpace(v, 0)
	if i >= len(v) || v[i] != '"' {
		return "", false
	}
	return unquote(v[i:]), true
}

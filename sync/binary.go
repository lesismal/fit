package fitsync

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"
)

// The binary form of records, which WASM plugins that ask for it read and
// write instead of the canonical JSON (planning decision 8, I165b): what
// the JSON form says with names and quotes it says with lengths and tags,
// so that neither side scans text, and the host decodes a call's records
// out of one copy of what the plugin handed back.
//
// A str is a uvarint length and its bytes. A value is a tag byte, 0x10 plus
// its Kind, and what the kind holds:
//
//	0x10 null   nothing          0x16 string   str
//	0x11 bool   a byte, 0 or 1   0x17 bytes    str
//	0x12 int    a zigzag varint  0x18 time     varint Unix seconds, uvarint
//	0x13 uint   a uvarint                      nanoseconds, varint offset
//	0x14 float  8 bytes, its                   seconds east of UTC
//	            IEEE bits, LE    0x19 date     varint year, month and day
//	0x15 decimal str                           bytes
//	0x1A json   str, the document
//	0x1B array  the length of the rest, 4 bytes LE; a uvarint count, the
//	            values
//	0x1C map    the length of the rest, 4 bytes LE; a uvarint count, each
//	            field's name (a str) and value
//	0x1F        str, a value in its canonical JSON
//
// so that a plugin passes over any value without reading into it.
// The tags are control characters no JSON text starts with, so a plugin
// may tell a value of either form by its first byte; 0x1F lets one hand on
// a value it wrote as JSON. A record is
//
//	op         a byte, its Op (0 for none)
//	flags      a byte: 1 the transaction's first, 2 its last
//	source, namespace   strs
//	key, before, after  values (null when it has none)
//	commit_time         str, RFC 3339 with nanoseconds, empty for none
//	tx id, position     strs, the position JSON, empty for none
//
// A batch is a uvarint count and its records; what a plugin emits is any
// number of a uvarint index, the input's record it comes from, and a
// record.

// The tags of the binary form.
const (
	binaryTag       = 0x10
	binaryCanonical = 0x1F
)

// maxBinaryDepth bounds how deep arrays and maps may nest in the binary
// form.
const maxBinaryDepth = 1000

func appendStr(b []byte, s string) []byte {
	return append(binary.AppendUvarint(b, uint64(len(s))), s...)
}

// AppendBinary appends v in the binary form to b.
func (v Value) AppendBinary(b []byte) []byte {
	b = append(b, binaryTag+byte(v.kind))
	switch v.kind {
	case KindBool:
		b = append(b, byte(v.num))
	case KindInt:
		b = binary.AppendVarint(b, int64(v.num))
	case KindUint:
		b = binary.AppendUvarint(b, v.num)
	case KindFloat:
		b = binary.LittleEndian.AppendUint64(b, v.num)
	case KindDecimal, KindString, KindBytes, KindJSON:
		b = appendStr(b, v.str)
	case KindTime:
		_, off := v.tm.Zone()
		b = binary.AppendVarint(b, v.tm.Unix())
		b = binary.AppendUvarint(b, uint64(v.tm.Nanosecond()))
		b = binary.AppendVarint(b, int64(off))
	case KindDate:
		y, m, d := v.tm.Date()
		b = append(binary.AppendVarint(b, int64(y)), byte(m), byte(d))
	case KindArray:
		i := len(b)
		b = binary.AppendUvarint(append(b, 0, 0, 0, 0), uint64(len(v.elems)))
		for _, e := range v.elems {
			b = e.AppendBinary(b)
		}
		binary.LittleEndian.PutUint32(b[i:], uint32(len(b)-i-4))
	case KindMap:
		i := len(b)
		b = binary.AppendUvarint(append(b, 0, 0, 0, 0), uint64(len(v.fields)))
		for _, f := range v.fields {
			b = f.Value.AppendBinary(appendStr(b, f.Name))
		}
		binary.LittleEndian.PutUint32(b[i:], uint32(len(b)-i-4))
	}
	return b
}

// Flags of a record in the binary form.
const (
	binaryTxFirst = 1 << iota
	binaryTxLast
)

// AppendBinary appends r in the binary form to b.
func (r *Record) AppendBinary(b []byte) []byte { return r.appendBinary(b, nil) }

// span is where a part of a record is in a batch.
type span struct{ a, b int }

// recordSpans are where a record's values, and its commit time's and
// position's text, are in a batch.
type recordSpans struct {
	vals    [3]span
	ct, pos span
}

// appendBinary appends r to b, noting where its parts are in sp, unless it
// is nil.
func (r *Record) appendBinary(b []byte, sp *recordSpans) []byte {
	var flags byte
	if r.Tx.First {
		flags |= binaryTxFirst
	}
	if r.Tx.Last {
		flags |= binaryTxLast
	}
	b = append(b, byte(r.Op), flags)
	b = appendStr(appendStr(b, r.Source), r.Namespace)
	if sp == nil {
		sp = new(recordSpans) // not kept: on the stack
	}
	for k, v := range [3]*Value{&r.Key, &r.Before, &r.After} {
		a := len(b)
		b = v.AppendBinary(b)
		sp.vals[k] = span{a, len(b)}
	}
	if r.CommitTime.IsZero() {
		b = append(b, 0)
		sp.ct = span{len(b), len(b)}
	} else {
		// RFC 3339 with nanoseconds is shorter than 128 bytes, a uvarint
		// of one byte.
		i := len(b)
		b = r.CommitTime.AppendFormat(append(b, 0), time.RFC3339Nano)
		b[i] = byte(len(b) - i - 1)
		sp.ct = span{i + 1, len(b)}
	}
	b = appendStr(appendStr(b, r.Tx.ID), string(r.Position))
	sp.pos = span{len(b) - len(r.Position), len(b)}
	return b
}

// BinaryBatch is a batch in the binary form that knows where its records'
// parts are in it, so that a BinaryReader reading the records a plugin
// made of it (RecordOf) takes a value, a commit time or a position the
// plugin handed back as it was from the batch's record rather than reading
// it again: the values are not changed once made, so the records share
// them. The zero BinaryBatch is ready for Reset.
type BinaryBatch struct {
	// Buf is the batch, as AppendBatchBinary writes it.
	Buf   []byte
	recs  []*Record
	spans []recordSpans
}

// Reset writes recs into Buf, over what it held; nil lets go of the
// records it held.
func (bb *BinaryBatch) Reset(recs []*Record) {
	b := binary.AppendUvarint(bb.Buf[:0], uint64(len(recs)))
	if cap(bb.spans) < len(recs) {
		bb.spans = make([]recordSpans, len(recs))
	}
	bb.spans = bb.spans[:len(recs)]
	for i, r := range recs {
		b = r.appendBinary(b, &bb.spans[i])
	}
	bb.Buf, bb.recs = b, recs
}

// AppendBatchBinary appends recs to b in the binary form, as WASM plugins
// that ask for it read a batch.
func AppendBatchBinary(b []byte, recs []*Record) []byte {
	b = binary.AppendUvarint(b, uint64(len(recs)))
	for _, r := range recs {
		b = r.AppendBinary(b)
	}
	return b
}

// BinaryReader reads records in the binary form. Reset copies what it is
// to read once; the records' strings, positions and bytes are slices of
// that copy, and the records, their maps' fields and their arrays' values
// are carved out of blocks the reader allocates as they fill, so that a
// call's records cost a few allocations, not some for each. A record holds
// its block, and its copy, until every record of them is gone. The zero
// BinaryReader is ready for Reset.
type BinaryReader struct {
	buf    []byte
	s      string // buf, as a string
	i      int
	recs   []Record
	fields []Field
	vals   []Value
}

// Blocks a BinaryReader carves out of.
const (
	readerRecords = 64
	readerFields  = 512
	readerValues  = 64
)

var errShort = errors.New("binary records cut short")

// Reset makes r read a copy of data.
func (r *BinaryReader) Reset(data []byte) {
	r.buf, r.s, r.i = nil, "", 0
	if len(data) > 0 {
		// Never written once copied, so the strings may share it.
		r.buf = append([]byte(nil), data...)
		r.s = unsafe.String(&r.buf[0], len(r.buf))
	}
}

// More reports whether there is more to read.
func (r *BinaryReader) More() bool { return r.i < len(r.s) }

// Uvarint reads a uvarint, as a batch's count and an emitted record's
// index are written.
func (r *BinaryReader) Uvarint() (uint64, error) {
	var u uint64
	for shift := uint(0); shift < 64; shift += 7 {
		if r.i >= len(r.s) {
			return 0, errShort
		}
		c := r.s[r.i]
		r.i++
		if c < 0x80 {
			if shift == 63 && c > 1 {
				break
			}
			return u | uint64(c)<<shift, nil
		}
		u |= uint64(c&0x7f) << shift
	}
	return 0, errors.New("a uvarint past 64 bits")
}

func (r *BinaryReader) varint() (int64, error) {
	u, err := r.Uvarint()
	return int64(u>>1) ^ -int64(u&1), err
}

func (r *BinaryReader) readByte() (byte, error) {
	if r.i >= len(r.s) {
		return 0, errShort
	}
	c := r.s[r.i]
	r.i++
	return c, nil
}

// span reads a str and returns where it is.
func (r *BinaryReader) span() (int, int, error) {
	n, err := r.Uvarint()
	if err != nil {
		return 0, 0, err
	}
	if n > uint64(len(r.s)-r.i) {
		return 0, 0, errShort
	}
	start := r.i
	r.i += int(n)
	return start, r.i, nil
}

func (r *BinaryReader) str() (string, error) {
	a, b, err := r.span()
	return r.s[a:b], err
}

// count reads a count of things each at least min bytes long, which what
// is left must hold.
func (r *BinaryReader) count(min int) (int, error) {
	n, err := r.Uvarint()
	if err != nil {
		return 0, err
	}
	if n > uint64((len(r.s)-r.i)/min) {
		return 0, errShort
	}
	return int(n), nil
}

// Record reads a record, made anew as ParseRecord's are: a transform may
// emit it as one it made with Clone.
func (r *BinaryReader) Record() (*Record, error) { return r.RecordOf(nil, -1) }

// RecordOf reads a record a plugin made of bb's record i: its values,
// commit time and position that are as bb's record had them are that
// record's. A bb nil, or an i not among its records, is Record.
func (r *BinaryReader) RecordOf(bb *BinaryBatch, i int) (*Record, error) {
	if len(r.recs) == 0 {
		r.recs = make([]Record, readerRecords)
	}
	rec := &r.recs[0]
	r.recs = r.recs[1:]
	var src *from
	if bb != nil && i >= 0 && i < len(bb.recs) {
		src = &from{bb.Buf, bb.recs[i], &bb.spans[i]}
	}
	if err := r.record(rec, src); err != nil {
		*rec = Record{}
		return nil, err
	}
	rec.made = true
	return rec, nil
}

// from is the record of a batch a record read was made of.
type from struct {
	buf []byte
	rec *Record
	sp  *recordSpans
}

// same reports whether what is at the reader starts with the bytes of s in
// f's batch, and passes over them if so.
func (r *BinaryReader) same(f *from, s span) bool {
	n := s.b - s.a
	if n > len(r.s)-r.i || r.s[r.i:r.i+n] != unsafe.String(unsafe.SliceData(f.buf[s.a:]), n) {
		return false
	}
	r.i += n
	return true
}

// sameText reports whether the text a, b of the reader's copy is that of s
// in f's batch.
func (r *BinaryReader) sameText(f *from, a, b int, s span) bool {
	return b-a == s.b-s.a && r.s[a:b] == unsafe.String(unsafe.SliceData(f.buf[s.a:]), s.b-s.a)
}

func (r *BinaryReader) record(rec *Record, f *from) error {
	op, err := r.readByte()
	if err != nil {
		return err
	}
	if int(op) >= len(opNames) {
		return fmt.Errorf("op %d is not an operation", op)
	}
	flags, err := r.readByte()
	if err != nil {
		return err
	}
	if flags&^(binaryTxFirst|binaryTxLast) != 0 {
		return fmt.Errorf("a record's flags %#x", flags)
	}
	rec.Op = Op(op)
	rec.Tx.First, rec.Tx.Last = flags&binaryTxFirst != 0, flags&binaryTxLast != 0
	if rec.Source, err = r.str(); err != nil {
		return err
	}
	if rec.Namespace, err = r.str(); err != nil {
		return err
	}
	for k, v := range [3]*Value{&rec.Key, &rec.Before, &rec.After} {
		if f != nil && r.same(f, f.sp.vals[k]) {
			*v = *[3]*Value{&f.rec.Key, &f.rec.Before, &f.rec.After}[k]
			continue
		}
		if *v, err = r.value(0); err != nil {
			return err
		}
	}
	a, b, err := r.span()
	switch {
	case err != nil:
		return err
	case f != nil && r.sameText(f, a, b, f.sp.ct):
		rec.CommitTime = f.rec.CommitTime
	case b > a:
		if rec.CommitTime, err = time.Parse(time.RFC3339Nano, r.s[a:b]); err != nil {
			return fmt.Errorf("commit_time: %w", err)
		}
	}
	if rec.Tx.ID, err = r.str(); err != nil {
		return err
	}
	if a, b, err = r.span(); err != nil {
		return err
	}
	switch {
	case b == a:
	case f != nil && r.sameText(f, a, b, f.sp.pos):
		rec.Position = f.rec.Position
	case !json.Valid(r.buf[a:b]):
		return errors.New("position: not JSON")
	default:
		rec.Position = Position(r.buf[a:b:b])
	}
	return nil
}

func (r *BinaryReader) value(depth int) (Value, error) {
	tag, err := r.readByte()
	if err != nil {
		return Value{}, err
	}
	if tag == binaryCanonical {
		doc, err := r.str()
		if err != nil {
			return Value{}, err
		}
		var v Value
		if err := v.UnmarshalJSON([]byte(doc)); err != nil {
			return Value{}, fmt.Errorf("a value in canonical JSON: %w", err)
		}
		return v, nil
	}
	if tag < binaryTag || tag > binaryTag+byte(KindMap) {
		return Value{}, fmt.Errorf("a value's tag %#x", tag)
	}
	kind := Kind(tag - binaryTag)
	switch kind {
	case KindNull:
		return Value{}, nil
	case KindBool:
		c, err := r.readByte()
		if err == nil && c > 1 {
			err = fmt.Errorf("a bool of %d", c)
		}
		return Bool(c == 1), err
	case KindInt:
		i, err := r.varint()
		return Int(i), err
	case KindUint:
		u, err := r.Uvarint()
		return Uint(u), err
	case KindFloat:
		if len(r.s)-r.i < 8 {
			return Value{}, errShort
		}
		bits := binary.LittleEndian.Uint64(r.buf[r.i:])
		r.i += 8
		return Float(math.Float64frombits(bits)), nil
	case KindDecimal, KindString, KindBytes:
		s, err := r.str()
		return Value{kind: kind, str: s}, err
	case KindJSON:
		a, b, err := r.span()
		if err == nil && !json.Valid(r.buf[a:b]) {
			err = errors.New("a json value that is not JSON")
		}
		return Value{kind: kind, str: r.s[a:b]}, err
	case KindTime:
		return r.time()
	case KindDate:
		y, err := r.varint()
		if err != nil {
			return Value{}, err
		}
		if len(r.s)-r.i < 2 {
			return Value{}, errShort
		}
		m, d := r.s[r.i], r.s[r.i+1]
		r.i += 2
		if y < -1e6 || y > 1e6 || m < 1 || m > 12 || d < 1 || d > 31 {
			return Value{}, fmt.Errorf("the date %d-%d-%d", y, m, d)
		}
		return Date(int(y), time.Month(m), int(d)), nil
	}
	if depth >= maxBinaryDepth {
		return Value{}, errors.New("values nested too deep")
	}
	if len(r.s)-r.i < 4 {
		return Value{}, errShort
	}
	size := binary.LittleEndian.Uint32(r.buf[r.i:])
	r.i += 4
	if uint64(size) > uint64(len(r.s)-r.i) {
		return Value{}, errShort
	}
	end := r.i + int(size)
	v, err := r.container(kind, depth)
	if err == nil && r.i != end {
		err = fmt.Errorf("a %s's length is not what it holds", kind)
	}
	return v, err
}

// container reads an array's or a map's count and what it holds.
func (r *BinaryReader) container(kind Kind, depth int) (Value, error) {
	if kind == KindArray {
		n, err := r.count(1)
		if err != nil {
			return Value{}, err
		}
		elems := r.takeValues(n)
		for i := range elems {
			if elems[i], err = r.value(depth + 1); err != nil {
				return Value{}, err
			}
		}
		return Value{kind: KindArray, elems: elems}, nil
	}
	n, err := r.count(2)
	if err != nil {
		return Value{}, err
	}
	fields := r.takeFields(n)
	// seen has a bit for each name read, of a hash of it, so that a name is
	// looked for among those before it only when its bit is set already.
	var seen uint64
	for i := range fields {
		name, err := r.str()
		if err != nil {
			return Value{}, err
		}
		bit := uint64(1) << (nameHash(name) & 63)
		if seen&bit != 0 {
			for _, f := range fields[:i] {
				if f.Name == name {
					return Value{}, fmt.Errorf("%q is there twice", name)
				}
			}
		}
		seen |= bit
		fields[i].Name = name
		if fields[i].Value, err = r.value(depth + 1); err != nil {
			return Value{}, fmt.Errorf("%s: %w", name, err)
		}
	}
	return Value{kind: KindMap, fields: fields}, nil
}

// nameHash is a cheap hash of a field's name.
func nameHash(s string) uint {
	h := uint(len(s))
	if len(s) > 0 {
		h = h*31 + uint(s[0])
		h = h*31 + uint(s[len(s)-1])
		h = h*31 + uint(s[len(s)/2])
	}
	return h ^ h>>6
}

func (r *BinaryReader) time() (Value, error) {
	sec, err := r.varint()
	if err != nil {
		return Value{}, err
	}
	nsec, err := r.Uvarint()
	if err != nil {
		return Value{}, err
	}
	off, err := r.varint()
	if err != nil {
		return Value{}, err
	}
	if nsec >= 1e9 || off <= -86400 || off >= 86400 {
		return Value{}, errors.New("a time out of range")
	}
	return Value{kind: KindTime, tm: time.Unix(sec, int64(nsec)).In(zone(int(off)))}, nil
}

// zones are the fixed zones times have been read in, by offset.
var zones sync.Map

func zone(off int) *time.Location {
	if off == 0 {
		return time.UTC
	}
	if z, ok := zones.Load(off); ok {
		return z.(*time.Location)
	}
	z, _ := zones.LoadOrStore(off, time.FixedZone("", off))
	return z.(*time.Location)
}

// takeValues returns n values carved out of r's block, a new one when it
// has too few.
func (r *BinaryReader) takeValues(n int) []Value {
	if n == 0 {
		return []Value{}
	}
	if n > len(r.vals) {
		if n > readerValues/2 {
			return make([]Value, n)
		}
		r.vals = make([]Value, readerValues)
	}
	s := r.vals[:n:n]
	r.vals = r.vals[n:]
	return s
}

// takeFields is takeValues for a map's fields.
func (r *BinaryReader) takeFields(n int) []Field {
	if n == 0 {
		return []Field{}
	}
	if n > len(r.fields) {
		if n > readerFields/4 {
			return make([]Field, n)
		}
		r.fields = make([]Field, readerFields)
	}
	s := r.fields[:n:n]
	r.fields = r.fields[n:]
	return s
}

// ParseBatchBinary reads a batch in the binary form, as AppendBatchBinary
// writes it.
func ParseBatchBinary(data []byte) ([]*Record, error) {
	var r BinaryReader
	r.Reset(data)
	n, err := r.count(5)
	if err != nil {
		return nil, err
	}
	recs := make([]*Record, n)
	for i := range recs {
		if recs[i], err = r.Record(); err != nil {
			return nil, fmt.Errorf("record %d: %w", i, err)
		}
	}
	if r.More() {
		return nil, errors.New("more after the batch")
	}
	return recs, nil
}

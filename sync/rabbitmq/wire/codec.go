package amqpwire

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"time"
)

// The frame types, and the octet that ends every frame.
const (
	frameMethod    = 1
	frameHeader    = 2
	frameBody      = 3
	frameHeartbeat = 8
	frameEnd       = 0xCE
	// frameOverhead is a frame's header and end octet, which frame_max
	// counts.
	frameOverhead = 8
)

// The classes and methods spoken, as class<<16 | method.
const (
	connectionStart     = 10<<16 | 10
	connectionStartOK   = 10<<16 | 11
	connectionSecure    = 10<<16 | 20
	connectionTune      = 10<<16 | 30
	connectionTuneOK    = 10<<16 | 31
	connectionOpen      = 10<<16 | 40
	connectionOpenOK    = 10<<16 | 41
	connectionClose     = 10<<16 | 50
	connectionCloseOK   = 10<<16 | 51
	connectionBlocked   = 10<<16 | 60
	connectionUnblocked = 10<<16 | 61
	channelOpen         = 20<<16 | 10
	channelOpenOK       = 20<<16 | 11
	channelFlow         = 20<<16 | 20
	channelFlowOK       = 20<<16 | 21
	channelClose        = 20<<16 | 40
	channelCloseOK      = 20<<16 | 41
	queueDeclare        = 50<<16 | 10
	queueDeclareOK      = 50<<16 | 11
	basicQos            = 60<<16 | 10
	basicQosOK          = 60<<16 | 11
	basicConsume        = 60<<16 | 20
	basicConsumeOK      = 60<<16 | 21
	basicCancel         = 60<<16 | 30
	basicPublish        = 60<<16 | 40
	basicReturn         = 60<<16 | 50
	basicDeliver        = 60<<16 | 60
	basicAck            = 60<<16 | 80
	basicNack           = 60<<16 | 120
	confirmSelect       = 85<<16 | 10
	confirmSelectOK     = 85<<16 | 11
)

const classBasic = 60

// errBadFrame is a frame the protocol does not allow.
var errBadFrame = errors.New("amqp: a malformed frame")

// frame is one frame read.
type frame struct {
	typ     byte
	channel uint16
	payload []byte
}

// readFrame reads a frame of at most max bytes, its header and end octet
// counted, as frame_max is.
func readFrame(r *bufio.Reader, max uint32) (frame, error) {
	var h [7]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return frame{}, err
	}
	size := binary.BigEndian.Uint32(h[3:])
	if uint64(size)+frameOverhead > uint64(max) {
		return frame{}, fmt.Errorf("amqp: a frame of %d bytes, more than the %d agreed", uint64(size)+frameOverhead, max)
	}
	p := make([]byte, size+1)
	if _, err := io.ReadFull(r, p); err != nil {
		return frame{}, err
	}
	if p[size] != frameEnd {
		return frame{}, errBadFrame
	}
	return frame{typ: h[0], channel: binary.BigEndian.Uint16(h[1:]), payload: p[:size]}, nil
}

// appendFrame appends a frame of payload.
func appendFrame(dst []byte, typ byte, channel uint16, payload []byte) []byte {
	dst = append(dst, typ, byte(channel>>8), byte(channel))
	dst = binary.BigEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, payload...)
	return append(dst, frameEnd)
}

// enc builds a method's or a header's payload.
type enc []byte

func method(id uint32) enc { return binary.BigEndian.AppendUint32(nil, id) }

func (e enc) u8(v uint8) enc   { return append(e, v) }
func (e enc) u16(v uint16) enc { return binary.BigEndian.AppendUint16(e, v) }
func (e enc) u32(v uint32) enc { return binary.BigEndian.AppendUint32(e, v) }
func (e enc) u64(v uint64) enc { return binary.BigEndian.AppendUint64(e, v) }

// short appends a short string, cut at 255 bytes: callers check lengths
// that matter.
func (e enc) short(s string) enc {
	if len(s) > 255 {
		s = s[:255]
	}
	return append(append(e, byte(len(s))), s...)
}

func (e enc) long(s string) enc { return append(e.u32(uint32(len(s))), s...) }

// bits packs flags into an octet, the first the lowest bit.
func (e enc) bits(flags ...bool) enc {
	var b byte
	for i, f := range flags {
		if f {
			b |= 1 << i
		}
	}
	return append(e, b)
}

func (e enc) table(t Table) enc {
	at := len(e)
	e = e.u32(0)
	for k, v := range t {
		e = e.short(k)
		e = e.value(v)
	}
	binary.BigEndian.PutUint32(e[at:], uint32(len(e)-at-4))
	return e
}

// value appends a field value: its type octet and its encoding, as
// RabbitMQ reads them.
func (e enc) value(v any) enc {
	switch v := v.(type) {
	case nil:
		return append(e, 'V')
	case bool:
		return e.u8('t').bits(v)
	case int8:
		return e.u8('b').u8(uint8(v))
	case uint8:
		return e.u8('B').u8(v)
	case int16:
		return e.u8('s').u16(uint16(v))
	case uint16:
		return e.u8('u').u16(v)
	case int32:
		return e.u8('I').u32(uint32(v))
	case uint32:
		return e.u8('i').u32(v)
	case int:
		return e.u8('l').u64(uint64(v))
	case int64:
		return e.u8('l').u64(uint64(v))
	case float32:
		return e.u8('f').u32(math.Float32bits(v))
	case float64:
		return e.u8('d').u64(math.Float64bits(v))
	case Decimal:
		return e.u8('D').u8(v.Scale).u32(uint32(v.Value))
	case string:
		return e.u8('S').long(v)
	case []byte:
		return e.u8('x').long(string(v))
	case time.Time:
		return e.u8('T').u64(uint64(v.Unix()))
	case Table:
		return e.u8('F').table(v)
	case []any:
		e = e.u8('A')
		at := len(e)
		e = e.u32(0)
		for _, x := range v {
			e = e.value(x)
		}
		binary.BigEndian.PutUint32(e[at:], uint32(len(e)-at-4))
		return e
	}
	// Anything else, as its text.
	return e.u8('S').long(fmt.Sprint(v))
}

// Table is a field table: headers, a server's properties, arguments.
// Values are nil, bool, int8, uint8, int16, uint16, int32, uint32, int64,
// float32, float64, Decimal, string, []byte, time.Time, Table and []any;
// an int is sent as an int64, anything else as its text.
type Table map[string]any

// Decimal is a field table's decimal: Value / 10^Scale.
type Decimal struct {
	Scale uint8
	Value int32
}

// maxDepth bounds how deep tables and arrays nest in what is read.
const maxDepth = 16

// dec reads a payload; the first error sticks, and reads after it are
// zero.
type dec struct {
	b   []byte
	err error
}

func (d *dec) take(n int) []byte {
	if d.err != nil {
		return nil
	}
	if n < 0 || n > len(d.b) {
		d.err = errBadFrame
		d.b = nil
		return nil
	}
	v := d.b[:n:n]
	d.b = d.b[n:]
	return v
}

func (d *dec) u8() uint8 {
	if b := d.take(1); b != nil {
		return b[0]
	}
	return 0
}

func (d *dec) u16() uint16 {
	if b := d.take(2); b != nil {
		return binary.BigEndian.Uint16(b)
	}
	return 0
}

func (d *dec) u32() uint32 {
	if b := d.take(4); b != nil {
		return binary.BigEndian.Uint32(b)
	}
	return 0
}

func (d *dec) u64() uint64 {
	if b := d.take(8); b != nil {
		return binary.BigEndian.Uint64(b)
	}
	return 0
}

func (d *dec) short() string { return string(d.take(int(d.u8()))) }

func (d *dec) long() string { return string(d.take(int(d.u32()))) }

func (d *dec) table(depth int) Table {
	n := d.u32()
	body := &dec{b: d.take(int(n))}
	if d.err != nil {
		return nil
	}
	if depth > maxDepth {
		d.err = errBadFrame
		return nil
	}
	t := Table{}
	for len(body.b) > 0 && body.err == nil {
		k := body.short()
		t[k] = body.value(depth)
	}
	d.err = body.err
	return t
}

func (d *dec) value(depth int) any {
	switch typ := d.u8(); typ {
	case 't':
		return d.u8() != 0
	case 'b':
		return int8(d.u8())
	case 'B':
		return d.u8()
	case 's':
		return int16(d.u16())
	case 'u':
		return d.u16()
	case 'I':
		return int32(d.u32())
	case 'i':
		return d.u32()
	case 'l':
		return int64(d.u64())
	case 'f':
		return math.Float32frombits(d.u32())
	case 'd':
		return math.Float64frombits(d.u64())
	case 'D':
		return Decimal{Scale: d.u8(), Value: int32(d.u32())}
	case 'S':
		return d.long()
	case 'x':
		return []byte(d.long())
	case 'T':
		return time.Unix(int64(d.u64()), 0).UTC()
	case 'F':
		return d.table(depth + 1)
	case 'A':
		n := d.u32()
		body := &dec{b: d.take(int(n))}
		if d.err != nil {
			return nil
		}
		if depth+1 > maxDepth {
			d.err = errBadFrame
			return nil
		}
		a := []any{}
		for len(body.b) > 0 && body.err == nil {
			a = append(a, body.value(depth+1))
		}
		d.err = body.err
		return a
	case 'V':
		return nil
	default:
		if d.err == nil {
			d.err = fmt.Errorf("amqp: a field of unknown type %q", typ)
		}
		return nil
	}
}

// Properties are a message's basic properties.
type Properties struct {
	ContentType     string
	ContentEncoding string
	Headers         Table
	// DeliveryMode is 2 for a persistent message, 1 or 0 otherwise.
	DeliveryMode  uint8
	Priority      uint8
	CorrelationID string
	ReplyTo       string
	Expiration    string
	MessageID     string
	Timestamp     time.Time
	Type          string
	UserID        string
	AppID         string
}

// The property flags, from the highest bit.
const (
	flagContentType     = 1 << 15
	flagContentEncoding = 1 << 14
	flagHeaders         = 1 << 13
	flagDeliveryMode    = 1 << 12
	flagPriority        = 1 << 11
	flagCorrelationID   = 1 << 10
	flagReplyTo         = 1 << 9
	flagExpiration      = 1 << 8
	flagMessageID       = 1 << 7
	flagTimestamp       = 1 << 6
	flagType            = 1 << 5
	flagUserID          = 1 << 4
	flagAppID           = 1 << 3
	flagClusterID       = 1 << 2
)

// header appends a content header of a message of size bytes.
func (p *Properties) header(size uint64) enc {
	var flags uint16
	e := enc(nil).u16(classBasic).u16(0).u64(size).u16(0)
	str := func(flag uint16, s string) {
		if s != "" {
			flags |= flag
			e = e.short(s)
		}
	}
	str(flagContentType, p.ContentType)
	str(flagContentEncoding, p.ContentEncoding)
	if p.Headers != nil {
		flags |= flagHeaders
		e = e.table(p.Headers)
	}
	if p.DeliveryMode != 0 {
		flags |= flagDeliveryMode
		e = e.u8(p.DeliveryMode)
	}
	if p.Priority != 0 {
		flags |= flagPriority
		e = e.u8(p.Priority)
	}
	str(flagCorrelationID, p.CorrelationID)
	str(flagReplyTo, p.ReplyTo)
	str(flagExpiration, p.Expiration)
	str(flagMessageID, p.MessageID)
	if !p.Timestamp.IsZero() {
		flags |= flagTimestamp
		e = e.u64(uint64(p.Timestamp.Unix()))
	}
	str(flagType, p.Type)
	str(flagUserID, p.UserID)
	str(flagAppID, p.AppID)
	binary.BigEndian.PutUint16(e[12:], flags)
	return e
}

// readHeader reads a content header: the body's size and the properties.
func readHeader(b []byte) (uint64, Properties, error) {
	d := &dec{b: b}
	class := d.u16()
	d.u16() // weight
	size := d.u64()
	flags := d.u16()
	var p Properties
	if flags&1 != 0 {
		// A continuation of the flags: not in 0-9-1's basic class.
		return 0, p, errBadFrame
	}
	str := func(flag uint16, s *string) {
		if flags&flag != 0 {
			*s = d.short()
		}
	}
	str(flagContentType, &p.ContentType)
	str(flagContentEncoding, &p.ContentEncoding)
	if flags&flagHeaders != 0 {
		p.Headers = d.table(0)
	}
	if flags&flagDeliveryMode != 0 {
		p.DeliveryMode = d.u8()
	}
	if flags&flagPriority != 0 {
		p.Priority = d.u8()
	}
	str(flagCorrelationID, &p.CorrelationID)
	str(flagReplyTo, &p.ReplyTo)
	str(flagExpiration, &p.Expiration)
	str(flagMessageID, &p.MessageID)
	if flags&flagTimestamp != 0 {
		p.Timestamp = time.Unix(int64(d.u64()), 0).UTC()
	}
	str(flagType, &p.Type)
	str(flagUserID, &p.UserID)
	str(flagAppID, &p.AppID)
	var cluster string
	str(flagClusterID, &cluster)
	if d.err == nil && class != classBasic {
		d.err = errBadFrame
	}
	return size, p, d.err
}

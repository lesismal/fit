package pulsarwire

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/snappy"
	"github.com/klauspost/compress/zstd"

	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
)

// A frame is its total size, the size of its command, the command, and, for
// SEND and MESSAGE, the payload: a magic number, a CRC32C checksum of what
// follows it, the metadata's size, the metadata and the message's bytes.
// MESSAGE may start its payload with the broker's entry metadata, under a
// magic number of its own.
const (
	magicCRC32C       = 0x0e01
	magicBrokerEntry  = 0x0e02
	frameOverhead     = 10 << 10
	defaultMaxMessage = 5 << 20
	maxBatchMetaSize  = 1 << 20
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// frame makes a frame of cmd, with no payload.
func frame(cmd []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(4+len(cmd)))
	b = binary.BigEndian.AppendUint32(b, uint32(len(cmd)))
	return append(b, cmd...)
}

// payloadFrame makes a frame of cmd with metadata and payload, checksummed.
func payloadFrame(cmd, meta, payload []byte) []byte {
	total := 4 + len(cmd) + 2 + 4 + 4 + len(meta) + len(payload)
	b := make([]byte, 0, 4+total)
	b = binary.BigEndian.AppendUint32(b, uint32(total))
	b = binary.BigEndian.AppendUint32(b, uint32(len(cmd)))
	b = append(b, cmd...)
	b = binary.BigEndian.AppendUint16(b, magicCRC32C)
	at := len(b)
	b = append(b, 0, 0, 0, 0)
	b = binary.BigEndian.AppendUint32(b, uint32(len(meta)))
	b = append(append(b, meta...), payload...)
	binary.BigEndian.PutUint32(b[at:], crc32.Checksum(b[at+4:], castagnoli))
	return b
}

// readFrame reads a frame of at most max bytes, returning its command and
// what follows the command.
func readFrame(r *bufio.Reader, max int) (cmd, rest []byte, err error) {
	var h [8]byte
	if _, err := io.ReadFull(r, h[:4]); err != nil {
		return nil, nil, err
	}
	total := int(binary.BigEndian.Uint32(h[:4]))
	if total < 4 || total > max {
		return nil, nil, fmt.Errorf("%w: %d bytes, past the bound of %d", ErrFrame, total, max)
	}
	b := make([]byte, total)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, nil, err
	}
	n := int(binary.BigEndian.Uint32(b))
	if n > total-4 {
		return nil, nil, fmt.Errorf("%w: a command of %d bytes in a frame of %d", ErrFrame, n, total)
	}
	return b[4 : 4+n], b[4+n:], nil
}

// splitPayload checks a payload's checksum and splits it into the message's
// metadata and its bytes.
func splitPayload(p []byte) (meta, data []byte, err error) {
	if len(p) >= 6 && binary.BigEndian.Uint16(p) == magicBrokerEntry {
		n := int(binary.BigEndian.Uint32(p[2:]))
		if n > len(p)-6 {
			return nil, nil, fmt.Errorf("%w: broker entry metadata", ErrFrame)
		}
		p = p[6+n:]
	}
	if len(p) >= 2 && binary.BigEndian.Uint16(p) == magicCRC32C {
		if len(p) < 6 {
			return nil, nil, fmt.Errorf("%w: a cut checksum", ErrFrame)
		}
		if crc32.Checksum(p[6:], castagnoli) != binary.BigEndian.Uint32(p[2:]) {
			return nil, nil, fmt.Errorf("%w: the checksum does not match", ErrFrame)
		}
		p = p[6:]
	}
	if len(p) < 4 {
		return nil, nil, fmt.Errorf("%w: no metadata", ErrFrame)
	}
	n := int(binary.BigEndian.Uint32(p))
	if n > len(p)-4 {
		return nil, nil, fmt.Errorf("%w: metadata of %d bytes", ErrFrame, n)
	}
	return p[4 : 4+n], p[4+n:], nil
}

// metadata is a message's MessageMetadata, or a batched message's
// SingleMessageMetadata, the fields the client reads and writes of each.
type metadata struct {
	producerName      string
	sequenceID        uint64
	publishTime       uint64
	properties        []KeyValue
	partitionKey      string
	hasKey            bool
	keyB64            bool
	compression       Compression
	uncompressedSize  uint32
	numMessages       int32
	batched           bool
	eventTime         uint64
	orderingKey       []byte
	nullValue         bool
	highestSequenceID uint64
	encrypted         bool
	chunks            int32
	marker            bool
	// payloadSize and compactedOut are a batched message's.
	payloadSize  int32
	compactedOut bool
}

// encode writes m as a MessageMetadata.
func (m *metadata) encode() []byte {
	b := pb(nil).str(1, m.producerName).uint(2, m.sequenceID).uint(3, m.publishTime).properties(4, m.properties)
	if m.compression != None {
		b = b.uint(8, uint64(m.compression)).uint(9, uint64(m.uncompressedSize))
	}
	b = b.int(11, int64(m.numMessages))
	return b.uint(24, m.highestSequenceID)
}

// encodeSingle writes m as a SingleMessageMetadata.
func (m *metadata) encodeSingle() []byte {
	b := pb(nil).properties(1, m.properties)
	if m.hasKey {
		b = b.str(2, m.partitionKey)
		if m.keyB64 {
			b = b.bool(6, true)
		}
	}
	b = b.int(3, int64(m.payloadSize))
	if m.eventTime > 0 {
		b = b.uint(5, m.eventTime)
	}
	b = b.uint(8, m.sequenceID)
	if m.nullValue {
		b = b.bool(9, true)
	}
	return b
}

func decodeMetadata(b []byte, single bool) (*metadata, error) {
	m := &metadata{numMessages: 1}
	r := pbReader{b: b}
	for f, ok := r.next(); ok; f, ok = r.next() {
		if single {
			f += 1000
		}
		switch f {
		case 1:
			m.producerName = r.str()
		case 2:
			m.sequenceID = r.uint()
		case 3:
			m.publishTime = r.uint()
		case 4, 1001:
			kv, err := decodeKeyValue(r.bytes())
			if err != nil {
				return nil, err
			}
			m.properties = append(m.properties, kv)
		case 6, 1002:
			m.partitionKey, m.hasKey = r.str(), true
		case 8:
			m.compression = Compression(r.uint())
		case 9:
			m.uncompressedSize = uint32(r.uint())
		case 11:
			m.numMessages, m.batched = int32(r.int()), true
		case 12, 1005:
			m.eventTime = r.uint()
		case 13:
			m.encrypted = true
			r.skip()
		case 17, 1006:
			m.keyB64 = r.uint() != 0
		case 18, 1007:
			m.orderingKey = r.bytes()
		case 20:
			m.marker = true
			r.skip()
		case 25, 1009:
			m.nullValue = r.uint() != 0
		case 27:
			m.chunks = int32(r.int())
		case 1003:
			m.payloadSize = int32(r.int())
		case 1004:
			m.compactedOut = r.uint() != 0
		case 1008:
			m.sequenceID = r.uint()
		default:
			r.skip()
		}
	}
	return m, r.err
}

// Compression is how a message's bytes are compressed, as Pulsar numbers
// it.
type Compression int32

// The compressions.
const (
	None Compression = iota
	LZ4
	Zlib
	Zstd
	Snappy
)

var compressionNames = [...]string{"none", "lz4", "zlib", "zstd", "snappy"}

func (c Compression) String() string {
	if c >= 0 && int(c) < len(compressionNames) {
		return compressionNames[c]
	}
	return fmt.Sprintf("compression(%d)", int32(c))
}

// ParseCompression reads a compression's name: none (or empty), lz4,
// zlib, zstd or snappy.
func ParseCompression(s string) (Compression, error) {
	for i, n := range compressionNames {
		if strings.EqualFold(s, n) || s == "" && i == 0 {
			return Compression(i), nil
		}
	}
	return None, fmt.Errorf("pulsar: unknown compression %q (none, lz4, zlib, zstd, snappy)", s)
}

var (
	zstdEnc  *zstd.Encoder
	zstdDec  *zstd.Decoder
	zstdOnce sync.Once
)

func zstdCodec() (*zstd.Encoder, *zstd.Decoder) {
	zstdOnce.Do(func() {
		zstdEnc, _ = zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
		zstdDec, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(1<<28))
	})
	return zstdEnc, zstdDec
}

// compress returns src compressed with c.
func compress(c Compression, src []byte) []byte {
	switch c {
	case LZ4:
		return kafkawire.LZ4Block(nil, src)
	case Zlib:
		var buf bytes.Buffer
		w := zlib.NewWriter(&buf)
		w.Write(src)
		w.Close() // into a buffer: no error
		return buf.Bytes()
	case Zstd:
		enc, _ := zstdCodec()
		return enc.EncodeAll(src, nil)
	case Snappy:
		return snappy.Encode(nil, src)
	}
	return src
}

var errSize = errors.New("the size it decompresses to is not the one its metadata says")

// decompress returns src decompressed with c, which must come to size
// bytes, no more than limit.
func decompress(c Compression, src []byte, size, limit int) ([]byte, error) {
	if c == None {
		return src, nil
	}
	if size > limit {
		return nil, fmt.Errorf("pulsar: a message of %d bytes decompressed, past the bound of %d", size, limit)
	}
	var out []byte
	var err error
	switch c {
	case LZ4:
		out, err = kafkawire.LZ4Unblock(make([]byte, 0, size), src, size)
	case Zlib:
		var r io.ReadCloser
		if r, err = zlib.NewReader(bytes.NewReader(src)); err == nil {
			out, err = io.ReadAll(io.LimitReader(r, int64(size)+1))
		}
	case Zstd:
		_, dec := zstdCodec()
		out, err = dec.DecodeAll(src, make([]byte, 0, size))
	case Snappy:
		var n int
		if n, err = snappy.DecodedLen(src); err == nil && n != size {
			err = errSize
		}
		if err == nil {
			out, err = snappy.Decode(make([]byte, size), src)
		}
	default:
		return nil, fmt.Errorf("%w: %v", ErrUnsupported, c)
	}
	if err == nil && len(out) != size {
		err = errSize
	}
	if err != nil {
		return nil, fmt.Errorf("pulsar: %v: %w", c, err)
	}
	return out, nil
}

// Message is a message read.
type Message struct {
	ID MessageID
	// Key is the message's partition key, nil when it has none; a key
	// written in base64 (bytes) is decoded.
	Key         []byte
	OrderingKey []byte
	// Payload is nil for a message with a null value, a compacted topic's
	// delete.
	Payload      []byte
	Properties   []KeyValue
	PublishTime  time.Time
	EventTime    time.Time
	ProducerName string
	SequenceID   uint64
	Redelivery   uint32
}

// ProducerMessage is a message to send.
type ProducerMessage struct {
	// Key is the partition key, none when nil. Bytes not valid as UTF-8
	// are sent in base64, as the Java client sends a key of bytes.
	Key        []byte
	Payload    []byte
	Null       bool
	Properties []KeyValue
	EventTime  time.Time
}

// buildBatch writes msgs as a batch's payload, numbering them from seq.
func buildBatch(msgs []ProducerMessage, seq uint64) []byte {
	var b []byte
	for i, m := range msgs {
		sm := metadata{properties: m.Properties, payloadSize: int32(len(m.Payload)), sequenceID: seq + uint64(i), nullValue: m.Null}
		if m.Key != nil {
			sm.hasKey = true
			sm.partitionKey, sm.keyB64 = KeyString(m.Key)
		}
		if !m.EventTime.IsZero() {
			sm.eventTime = uint64(m.EventTime.UnixMilli())
		}
		meta := sm.encodeSingle()
		b = binary.BigEndian.AppendUint32(b, uint32(len(meta)))
		b = append(append(b, meta...), m.Payload...)
	}
	return b
}

// KeyString writes a key as a partition key: as it is when it is UTF-8,
// in base64 otherwise.
func KeyString(k []byte) (string, bool) {
	if utf8.Valid(k) {
		return string(k), false
	}
	return base64.StdEncoding.EncodeToString(k), true
}

// keyBytes reads a partition key, decoding it when it is in base64.
func keyBytes(s string, b64 bool) []byte {
	if b64 {
		if b, err := base64.StdEncoding.DecodeString(s); err == nil {
			return b
		}
	}
	return []byte(s)
}

// millis is a time of milliseconds since the epoch, zero for 0.
func millis(ms uint64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(ms)).UTC()
}

// unbatch makes the messages of an entry's payload: one, or each of its
// batch's, the compacted-out ones left out.
func unbatch(id MessageID, m *metadata, data []byte) ([]Message, error) {
	msg := func(sm *metadata, payload []byte) Message {
		out := Message{ID: id, ProducerName: m.producerName, SequenceID: sm.sequenceID, Properties: sm.properties,
			PublishTime: millis(m.publishTime), EventTime: millis(sm.eventTime), OrderingKey: sm.orderingKey}
		if sm.hasKey {
			out.Key = keyBytes(sm.partitionKey, sm.keyB64)
		}
		if !sm.nullValue {
			out.Payload = payload
			if out.Payload == nil {
				out.Payload = []byte{}
			}
		}
		return out
	}
	if !m.batched {
		id.BatchIndex = -1
		return []Message{msg(m, data)}, nil
	}
	if m.numMessages <= 0 || int(m.numMessages) > len(data) {
		return nil, fmt.Errorf("%w: a batch of %d messages in %d bytes", ErrFrame, m.numMessages, len(data))
	}
	out := make([]Message, 0, m.numMessages)
	id.BatchSize = m.numMessages
	for i := int32(0); i < m.numMessages; i++ {
		if len(data) < 4 {
			return nil, fmt.Errorf("%w: a cut batch", ErrFrame)
		}
		n := int(binary.BigEndian.Uint32(data))
		if n > len(data)-4 || n > maxBatchMetaSize {
			return nil, fmt.Errorf("%w: a batched message's metadata of %d bytes", ErrFrame, n)
		}
		sm, err := decodeMetadata(data[4:4+n], true)
		if err != nil {
			return nil, err
		}
		data = data[4+n:]
		if sm.payloadSize < 0 || int(sm.payloadSize) > len(data) {
			return nil, fmt.Errorf("%w: a batched message of %d bytes", ErrFrame, sm.payloadSize)
		}
		payload := data[:sm.payloadSize:sm.payloadSize]
		data = data[sm.payloadSize:]
		if sm.compactedOut {
			continue
		}
		id.BatchIndex = i
		one := msg(sm, payload)
		one.ID = id
		out = append(out, one)
	}
	return out, nil
}

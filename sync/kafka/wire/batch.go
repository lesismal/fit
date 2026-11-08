package kafkawire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"slices"
	"time"
)

// Header is one of a record's headers.
type Header struct {
	Key   string
	Value []byte
}

// Record is a Kafka record. Key and Value may be nil (a null key, a
// tombstone). A record read by Fetch has its Offset; Timestamp is the
// producer's time, or the broker's for a topic with LogAppendTime. The
// slices of a record read by Fetch are its own, not shared with others.
type Record struct {
	Key, Value []byte
	Headers    []Header
	Timestamp  time.Time
	Offset     int64
}

// The attributes of a record batch.
const (
	attrCompression   = 0x07
	attrLogAppendTime = 0x08
	attrTransactional = 0x10
	attrControl       = 0x20
)

// batchHeaderLen is the bytes of a record batch before its records: base
// offset, length, partition leader epoch, magic, CRC, attributes, last
// offset delta, first and max timestamps, producer ID and epoch, base
// sequence and the record count.
const batchHeaderLen = 61

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// producer identifies the producer of a batch: none (-1) unless the
// client is idempotent, which it is not (see the package doc).
type producer struct {
	id       int64
	epoch    int16
	sequence int32
}

var noProducer = producer{id: -1, epoch: -1, sequence: -1}

// appendBatch appends recs as one record batch of magic 2, its base offset
// 0 (the broker assigns offsets), compressed with codec. Records without a
// timestamp get now.
func appendBatch(dst []byte, recs []Record, codec Compression, level int, p producer, now time.Time) ([]byte, error) {
	if len(recs) == 0 {
		return dst, errors.New("kafka: a batch of no records")
	}
	ts := func(r *Record) int64 {
		if r.Timestamp.IsZero() {
			return now.UnixMilli()
		}
		return r.Timestamp.UnixMilli()
	}
	first, maxTS := ts(&recs[0]), ts(&recs[0])
	var body encoder
	for i := range recs {
		r := &recs[i]
		t := ts(r)
		maxTS = max(maxTS, t)
		var rec encoder
		rec.int8(0) // attributes
		rec.varint(t - first)
		rec.varint(int64(i))
		rec.varbytes(r.Key)
		rec.varbytes(r.Value)
		rec.varint(int64(len(r.Headers)))
		for _, h := range r.Headers {
			rec.varint(int64(len(h.Key)))
			rec.b = append(rec.b, h.Key...)
			rec.varbytes(h.Value)
		}
		body.varint(int64(len(rec.b)))
		body.b = append(body.b, rec.b...)
	}
	records := body.b
	if codec != None {
		var err error
		if records, err = compress(codec, level, body.b); err != nil {
			return dst, err
		}
	}

	start := len(dst)
	e := encoder{b: dst}
	e.int64(0)                                         // base offset
	e.int32(int32(batchHeaderLen - 12 + len(records))) // length, after itself
	e.int32(-1)                                        // partition leader epoch
	e.int8(2)                                          // magic
	e.int32(0)                                         // CRC, below
	e.int16(int16(codec))
	e.int32(int32(len(recs) - 1))
	e.int64(first)
	e.int64(maxTS)
	e.int64(p.id)
	e.int16(p.epoch)
	e.int32(p.sequence)
	e.int32(int32(len(recs)))
	e.b = append(e.b, records...)
	putCRC(e.b[start:])
	return e.b, nil
}

// putCRC sets a batch's CRC, of its bytes from its attributes on.
func putCRC(batch []byte) {
	binary.BigEndian.PutUint32(batch[17:], crc32.Checksum(batch[21:], castagnoli))
}

// batchReader walks the record batches of a partition's record set as a
// fetch returns it: whole batches, perhaps one cut short at the end, which
// is not an error. It skips control batches and, read committed, the
// batches of aborted transactions.
type batchReader struct {
	maxBatchBytes int
	aborted       []abortedTxn // ascending by first offset; consumed from the front
	abortedIDs    map[int64]struct{}
	dec           *decompressor
}

type abortedTxn struct {
	producerID, firstOffset int64
}

// read appends to out the records of set at or after offset and returns
// them with the offset past the last whole batch (or offset when there is
// none). The records are copies, not slices of set.
func (br *batchReader) read(set []byte, offset int64, out []Record) ([]Record, int64, error) {
	next := offset
	for len(set) >= 17 { // base offset, length, epoch, magic
		baseOffset := int64(binary.BigEndian.Uint64(set))
		length := int(int32(binary.BigEndian.Uint32(set[8:])))
		if length < batchHeaderLen-12 {
			return out, next, fmt.Errorf("kafka: record batch of %d bytes", length)
		}
		if len(set) < 12+length {
			break // cut short by the fetch's size
		}
		batch := set[:12+length]
		set = set[12+length:]
		if magic := batch[16]; magic != 2 {
			return out, next, fmt.Errorf("kafka: record batch of magic %d (only 2 is read)", magic)
		}
		if crc32.Checksum(batch[21:], castagnoli) != binary.BigEndian.Uint32(batch[17:]) {
			return out, next, fmt.Errorf("kafka: record batch at offset %d: CRC mismatch", baseOffset)
		}
		attrs := int16(binary.BigEndian.Uint16(batch[21:]))
		lastOffset := baseOffset + int64(int32(binary.BigEndian.Uint32(batch[23:])))
		firstTS := int64(binary.BigEndian.Uint64(batch[27:]))
		maxTS := int64(binary.BigEndian.Uint64(batch[35:]))
		producerID := int64(binary.BigEndian.Uint64(batch[43:]))
		count := int(int32(binary.BigEndian.Uint32(batch[57:])))
		if lastOffset < offset {
			continue
		}
		next = max(next, lastOffset+1)

		// Read committed: a transaction begun at or before this batch's
		// end is aborted until its marker.
		for len(br.aborted) > 0 && br.aborted[0].firstOffset <= lastOffset {
			if br.abortedIDs == nil {
				br.abortedIDs = make(map[int64]struct{})
			}
			br.abortedIDs[br.aborted[0].producerID] = struct{}{}
			br.aborted = br.aborted[1:]
		}
		if attrs&attrControl != 0 {
			if br.abortedIDs != nil && isAbortMarker(batch[batchHeaderLen:], attrs) {
				delete(br.abortedIDs, producerID)
			}
			continue
		}
		if attrs&attrTransactional != 0 && br.abortedIDs != nil {
			if _, ok := br.abortedIDs[producerID]; ok {
				continue
			}
		}

		records := batch[batchHeaderLen:]
		if c := Compression(attrs & attrCompression); c != None {
			var err error
			if records, err = br.dec.decompress(c, records, br.maxBatchBytes); err != nil {
				return out, next, fmt.Errorf("kafka: record batch at offset %d: %w", baseOffset, err)
			}
		} else {
			records = slices.Clone(records) // the records' slices are their own
		}
		if count < 0 || count > len(records) {
			return out, next, fmt.Errorf("kafka: record batch at offset %d: %d records in %d bytes", baseOffset, count, len(records))
		}
		d := decoder{b: records}
		for range count {
			n := d.varint()
			if n < 0 || n > int64(d.remaining()) {
				d.fail(errShort)
				break
			}
			rd := decoder{b: d.take(int(n))}
			rd.int8() // attributes, unused
			tsDelta := rd.varint()
			offDelta := rd.varint()
			r := Record{Offset: baseOffset + offDelta}
			r.Key = rd.varbytes()
			r.Value = rd.varbytes()
			if nh := rd.varint(); nh > 0 {
				if nh > int64(rd.remaining()) {
					rd.fail(errShort)
				} else {
					r.Headers = make([]Header, 0, nh)
					for range nh {
						k := rd.varbytes()
						r.Headers = append(r.Headers, Header{Key: string(k), Value: rd.varbytes()})
					}
				}
			}
			if rd.err != nil {
				d.fail(rd.err)
				break
			}
			if attrs&attrLogAppendTime != 0 {
				r.Timestamp = time.UnixMilli(maxTS)
			} else {
				r.Timestamp = time.UnixMilli(firstTS + tsDelta)
			}
			if r.Offset >= offset {
				out = append(out, r)
			}
		}
		if d.err != nil {
			return out, next, fmt.Errorf("kafka: record batch at offset %d: %w", baseOffset, d.err)
		}
	}
	return out, next, nil
}

// isAbortMarker reads a control batch's one record: its key is a version
// and a type, 0 for an abort.
func isAbortMarker(records []byte, attrs int16) bool {
	if attrs&attrCompression != 0 {
		return false // control batches are not compressed
	}
	d := decoder{b: records}
	if n := d.varint(); n <= 0 {
		return false
	}
	d.int8()
	d.varint()
	d.varint()
	key := d.varbytes()
	if d.err != nil || len(key) < 4 {
		return false
	}
	return binary.BigEndian.Uint16(key[2:]) == 0
}

package fitsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
)

// RecordIDs returns an ID for each record of batch, the same each time the
// records are written again, for sinks whose servers drop a message sent
// twice by its ID (a NATS JetStream message ID, a message property): 32
// hex digits of a SHA-256.
//
// A record with an Origin is named by it: its source, its origin and its
// place among the records the transforms made of its source record, which
// a replay after a restart repeats however the source's partitions
// interleave and however batches are cut.
//
// A record without one is named by a position and a distance: by the
// anchor at or after it in the batch, the last of a run of records with
// one Position (a transform's records share their source record's), and
// how many records before the anchor it is; a record with none at or after
// it, by the nearest record before it with a Position and how far past it
// it is. Since a position ends a source transaction, and a batch written
// again starts after a checkpoint, such a record keeps its ID however the
// batch is cut, unless a batch ends inside a transaction and the rest
// comes in another, or the source's position holds several partitions'
// progress in the order a run read them. A batch with no position at all
// gets IDs of its records' canonical JSON and their place, which only a
// batch written again whole repeats.
func RecordIDs(batch []*Record) []string {
	ids := make([]string, len(batch))
	anchor := -1
	for i := len(batch) - 1; i >= 0; i-- {
		r := batch[i]
		if p := r.Position; p != nil && (i+1 == len(batch) || !bytes.Equal(batch[i+1].Position, p)) {
			anchor = i
		}
		switch {
		case r.Origin.Stream != "":
			ids[i] = originID(r)
		case anchor >= 0:
			ids[i] = recordID(batch[anchor].Position, "-", anchor-i)
		}
	}
	last := -1
	for i, r := range batch {
		if r.Position != nil {
			last = i
		}
		switch {
		case ids[i] != "":
		case last >= 0:
			ids[i] = recordID(batch[last].Position, "+", i-last)
		default:
			b, _ := r.AppendJSON(nil)
			ids[i] = recordID(b, "#", i)
		}
	}
	return ids
}

// AppendOrigin appends to dst the bytes that name r by its origin, which
// RecordIDs hashes: its source, its Origin and its place among the records
// the transforms made of its source record. It returns dst as it was when
// r has no origin. Sinks that name a request, a load or an object by its
// records use it in place of their positions.
func (r *Record) AppendOrigin(dst []byte) []byte {
	if r.Origin.Stream == "" {
		return dst
	}
	dst = binary.AppendUvarint(dst, uint64(len(r.Source)))
	dst = append(dst, r.Source...)
	dst = binary.AppendUvarint(dst, uint64(len(r.Origin.Stream)))
	dst = append(dst, r.Origin.Stream...)
	for _, n := range r.Origin.N {
		dst = binary.BigEndian.AppendUint64(dst, n)
	}
	return binary.BigEndian.AppendUint32(dst, r.sub)
}

// originID is the ID of a record with an origin; the leading 'o' keeps it
// apart from a position's.
func originID(r *Record) string {
	var buf [128]byte
	sum := sha256.Sum256(r.AppendOrigin(append(buf[:0], 'o')))
	var h [32]byte
	hex.Encode(h[:], sum[:16])
	return string(h[:])
}

func recordID(pos []byte, sign string, n int) string {
	h := sha256.New()
	h.Write(pos)
	h.Write([]byte(sign + strconv.Itoa(n)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

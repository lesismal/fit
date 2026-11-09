package fitsync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// RecordIDs returns an ID for each record of batch, the same each time the
// records are written again, for sinks whose servers drop a message sent
// twice by its ID (a NATS JetStream message ID, a message property): 32
// hex digits of a SHA-256 of a position and a distance. A record is named
// by the anchor at or after it in the batch, the last of a run of records
// with one Position (a transform's records share their source record's),
// and how many records before the anchor it is; a record with none at or
// after it, by the nearest record before it with a Position and how far
// past it it is. Since a position ends a source transaction, and a batch
// written again starts after a checkpoint, a record keeps its ID however
// the batch is cut, unless a batch ends inside a transaction and the rest
// comes in another. A batch with no position at all gets IDs of its
// records' canonical JSON and their place, which only a batch written
// again whole repeats.
func RecordIDs(batch []*Record) []string {
	ids := make([]string, len(batch))
	anchor := -1
	for i := len(batch) - 1; i >= 0; i-- {
		if p := batch[i].Position; p != nil && (i+1 == len(batch) || !bytes.Equal(batch[i+1].Position, p)) {
			anchor = i
		}
		if anchor >= 0 {
			ids[i] = recordID(batch[anchor].Position, "-", anchor-i)
		}
	}
	last := -1
	for i, r := range batch {
		switch {
		case r.Position != nil:
			last = i
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

func recordID(pos []byte, sign string, n int) string {
	h := sha256.New()
	h.Write(pos)
	h.Write([]byte(sign + strconv.Itoa(n)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

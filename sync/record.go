package fitsync

import (
	"fmt"
	"time"
)

// Op is what a record says happened.
type Op uint8

// The operations of a record.
const (
	// OpInsert is a row, document or line added: After holds it.
	OpInsert Op = iota + 1
	// OpUpdate is one changed: Before holds it as it was, when the source
	// has that, and After as it is.
	OpUpdate
	// OpDelete is one removed: Key names it, and Before holds it, when the
	// source has that.
	OpDelete
	// OpDDL is a change to a schema, reported: After holds the statement or
	// what the source says of it.
	OpDDL
	// OpMessage is a record with no row behind it: a log line, a queue's
	// message, a dead letter. After holds it.
	OpMessage
)

var opNames = [...]string{"", "insert", "update", "delete", "ddl", "message"}

func (o Op) String() string {
	if o > 0 && int(o) < len(opNames) {
		return opNames[o]
	}
	return fmt.Sprintf("op(%d)", uint8(o))
}

func parseOp(s string) (Op, bool) {
	for i, n := range opNames {
		if i > 0 && n == s {
			return Op(i), true
		}
	}
	return 0, false
}

// Tx is the source transaction a record belongs to. Records of one
// transaction come one after the other; the checkpoint never falls inside
// one, so that a restart reads a transaction again whole or not at all.
type Tx struct {
	// ID is the transaction's, as the source names it: a GTID, an XID, a
	// commit LSN. Empty for a record that is a transaction of its own.
	ID string
	// First and Last say the record is the transaction's first and last.
	First, Last bool
}

// Position is where the source is once it has read a record, as the source
// writes it: a binlog file and offset, an LSN, a resume token, a file's
// offset. It is opaque to the runtime, which keeps the last one acknowledged
// as the pipeline's checkpoint, and hands it back to the source to resume
// from. It must be JSON, so that the checkpoint file stays readable.
type Position []byte

// Record is what a source reads, transforms change and sinks write: one
// row, document, line or message, as one model for every connector.
type Record struct {
	// Source is the name of the source module that read it.
	Source string
	// Namespace is what it belongs to: db.table, an index, a collection, a
	// file, a topic.
	Namespace string
	Op        Op
	// Key identifies the row or document across records, as a map of the
	// key's columns or a single value; null when there is none, as for a
	// log line.
	Key Value
	// Before and After are the row as it was and as it is, maps; null when
	// the record has none.
	Before, After Value
	// CommitTime is when the source committed it; zero when unknown.
	CommitTime time.Time
	Tx         Tx
	// Position is where the source is once this record is read.
	Position Position

	// seq is the number of the source's record this one comes from, which
	// acknowledgements count by; a transform's records keep it.
	seq uint64
	// made says a transform made r, with Clone, so that the transforms
	// after it may change it in place.
	made bool
	// lane, when not 0, is the reader of a job's chunks that read r, plus
	// one: its records, and the records transforms make of them, go to
	// the writer of that number of each ParallelSink, in place of their
	// key's hash, so that a writer's batches hold runs of the keys of the
	// chunk read rather than keys from across the table.
	lane int
}

// boundary reports whether the source can resume after r: r ends a
// transaction, or has none.
func (r *Record) boundary() bool { return r.Tx.ID == "" || r.Tx.Last }

// Clone returns a copy of r a transform may change, which counts as the
// same source record as r. Values are not copied: they are not changed once
// made.
func (r *Record) Clone() *Record {
	c := *r
	c.made = true
	return &c
}

// Own returns r for a transform to change: r itself when a transform before
// it in the chain made it with Clone, and so holds the only reference to it,
// or a clone otherwise, so that the source's record stays as it was read. A
// chain of transforms thus copies a record once, however many change it. A
// transform emits each record it owns once.
func (r *Record) Own() *Record {
	if r.made {
		return r
	}
	return r.Clone()
}

// recordOverhead is what a record is reckoned to take besides its strings
// and values.
const recordOverhead = 160

// Size reckons the memory r takes, as the queue and batches bound it.
func (r *Record) Size() int {
	return recordOverhead + len(r.Source) + len(r.Namespace) + len(r.Tx.ID) + len(r.Position) +
		r.Key.size() + r.Before.size() + r.After.size()
}

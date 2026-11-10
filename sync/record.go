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

// Origin is a record's own coordinate in its source, which does not change
// however the source's reads of its partitions or streams interleave: a
// Kafka topic, partition, offset and the record's place among the
// message's records; a Pulsar partition and message ID; a binlog
// transaction and the row's place in it. Stream and N mean what the source
// says; only their equality counts. A record has an origin when Stream is
// not empty. It is a fixed struct, set without an allocation where the
// source already holds Stream's string.
type Origin struct {
	// Stream is what N counts in: a topic, a stream, a transaction.
	Stream string
	// N are the numbers that place the record in Stream.
	N [4]uint64
}

// Record is what a source reads, transforms change and sinks write: one
// row, document, line or message, as one model for every connector.
type Record struct {
	// Source is the name of the source module that read it.
	Source string
	// Namespace is what it belongs to: db.table, an index, a collection, a
	// file, a topic.
	Namespace string
	Op        Op
	// posOnly says r is a position-only record (Run.EmitPosition): it
	// carries only its Position, which the checkpoint moves to once the
	// records before it are acknowledged, and no transform or sink sees it.
	posOnly bool
	// made says a transform made r, with Clone, so that the transforms
	// after it may change it in place.
	made bool
	// sub is r's place among the records the transforms made of its
	// source record, which the pipeline sets before r reaches a sink, so
	// that records sharing an Origin have IDs of their own.
	//
	// posOnly, made and sub fill the padding after Op, so that Record
	// stays at most 512 bytes (TestRecordSize): the largest object the GC
	// allocates without a malloc header and marks by its span. One larger
	// costs every record more to allocate and to mark.
	sub uint32
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
	// Origin names the record in its source, the same each time the source
	// reads it again, for sinks that drop a record written twice by an ID
	// (RecordIDs); zero when the source has no such name for it.
	Origin Origin

	// seq is the number of the source's record this one comes from, which
	// acknowledgements count by; a transform's records keep it.
	seq uint64
	// run is what the pipeline run reading r sets before r reaches a
	// sink: one pointer to the run's constants, shared by its records, in
	// place of a copy of each in every record. nil before then.
	run *recordRun
	// lane, when not 0, is the reader of a job's chunks that read r, plus
	// one: its records, and the records transforms make of them, go to
	// the writer of that number of each ParallelSink, in place of their
	// key's hash, so that a writer's batches hold runs of the keys of the
	// chunk read rather than keys from across the table.
	lane int
}

// Lineage returns the lineage of the pipeline's checkpoint r was read in,
// which a reset of the checkpoint, a position set by hand and a new job
// change, and which restarts keep: sinks whose servers drop a load, a
// request or an object they had by a name made from positions or origins
// (a Doris label, an Idempotency-Key) put it in the name, so that the
// records a reset reads again are written again. It is empty for a record
// that has not reached a sink of a pipeline, and for a pipeline whose
// checkpoint predates lineages, until it is reset.
func (r *Record) Lineage() string {
	if r.run == nil {
		return ""
	}
	return r.run.lineage
}

// pipeline returns the name of the pipeline r goes through; empty before r
// reaches a sink.
func (r *Record) pipeline() string {
	if r.run == nil {
		return ""
	}
	return r.run.pipeline
}

// recordRun is what every record a pipeline run hands its sinks shares.
type recordRun struct {
	// pipeline is the pipeline's name, so that two pipelines' records of
	// equal origins (one topic name on two Kafka clusters feeding one
	// stream) have IDs of their own.
	pipeline string
	// lineage is the line of positions of the pipeline's checkpoint the
	// run is of: a reset, a position set by hand and a new job start
	// another.
	lineage string
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

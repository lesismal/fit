// Package fitsync is fit's sync role: pipelines that read records from a
// producer, a source, put them through transforms and write them to
// consumers, sinks, with a checkpoint saved only once every sink has
// acknowledged what it covers. Its directory is sync, as config and the
// plan name it; its name is fitsync, so that it does not shadow the standard
// library's sync where both are imported. See docs/decisions/036-sync.md.
//
// Connectors and transforms are modules, in the namespaces sync.sources,
// sync.sinks and sync.transforms, their params decoded as strictly as any
// module's. None of them runs on a fib worker: each pipeline runs on
// goroutines of its own, a bounded number of them.
package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// The namespaces of sync's modules.
const (
	Sources    = "sync.sources"
	Sinks      = "sync.sinks"
	Transforms = "sync.transforms"
)

// Source reads records. It is a module of the namespace sync.sources.
type Source interface {
	// Run reads records from where r.From says, in order, handing each to
	// r.Emit, until ctx ends or the source reaches its end. It returns nil
	// at its end, which stops the pipeline with its checkpoint written, and
	// ctx's error, or its own, otherwise. An error restarts the pipeline
	// from its checkpoint, after a backoff; Run is then called again, on
	// the same Source.
	Run(ctx context.Context, r *Run) error
}

// Run is what a Source's Run is given.
type Run struct {
	// From is the checkpoint, the Position of the last record every sink
	// acknowledged that ends a transaction; nil when there is none, and the
	// source starts where its params say.
	From Position
	// Once says the pipeline's mode is once: the source stops at its end,
	// as the end of a snapshot or of the files as they are now, rather
	// than wait for more.
	Once bool
	// Emit hands a record on, and returns once the queue has room for it:
	// a full queue holds the source here. It returns an error once the
	// pipeline stops, which Run returns. Records of one transaction are
	// emitted one after the other, the first with Tx.First and the last
	// with Tx.Last, and Position is set on the records the source can
	// resume after.
	Emit func(*Record) error
	// Pipeline is the name of the pipeline the source runs in, by which a
	// source that takes records in from elsewhere, as the http source from
	// a route's sync_ingest handler, is found.
	Pipeline string
	// Lag, when not nil, keeps how far behind its server's head the source
	// is, for the pipeline's status and metrics; a source calls ReportLag,
	// which spares it the nil check.
	Lag func(SourceLag)
	// Start, when not nil, is what SaveStart calls: the pipeline's, in a
	// run that has no checkpoint yet.
	Start func(Position) error
}

// SaveStart gives the pipeline, when it has no checkpoint (From is nil),
// the position a run started again would resume from, before the source
// emits its first record: a source that reads a snapshot then a stream
// gives its stream's start with none of the snapshot read. The pipeline
// saves it as its checkpoint at once, so that a run killed before any
// record is acknowledged starts again from it, replaying the stream from
// that start over a new snapshot, rather than from nothing: a new
// snapshot and a stream from its own, later start, which would lose the
// changes between the two (the deletes of rows the first run wrote among
// them). It does nothing when the run has a checkpoint, in a job (mode
// once) and in a dry run. Its error, the pipeline stopping or the file
// not written, is the source's to return.
func (r *Run) SaveStart(p Position) error {
	if r.Start == nil || r.From != nil || p == nil {
		return nil
	}
	return r.Start(p)
}

// SourceLag is how far behind its server's head a source is: a PostgreSQL
// slot's or a binlog's bytes, a Kafka partition's offsets. It is the
// source's own count, in its own Unit, as of At.
type SourceLag struct {
	Behind int64     `json:"behind"`
	Unit   string    `json:"unit"`
	At     time.Time `json:"at"`
}

// The units of SourceLag.
const (
	LagBytes   = "bytes"
	LagOffsets = "offsets"
)

// ReportLag tells the pipeline the source is behind its server's head by
// behind, in unit (LagBytes, LagOffsets), as it learns it: from a
// keepalive, a fetch's high watermark. It must not block, and does nothing
// in a dry run.
func (r *Run) ReportLag(behind int64, unit string) {
	if r.Lag != nil {
		r.Lag(SourceLag{Behind: max(behind, 0), Unit: unit, At: time.Now()})
	}
}

// Confirmer is a Source told of each checkpoint once it is saved, as a
// PostgreSQL slot is told the LSN it may release, never before every sink
// has acknowledged it.
type Confirmer interface {
	Confirm(p Position)
}

// Acknowledger is a Source told, as the pipeline goes, of each position up
// to which every sink has acknowledged every record (one on_error took
// counting as acknowledged), on a record that ends a transaction: the
// watermark, before it is saved. The http source answers its clients then.
// Positions come in order, though a call may come after a later one's;
// Acknowledged must not block, and must not call Emit.
type Acknowledger interface {
	Acknowledged(p Position)
}

// Sink writes records. It is a module of the namespace sync.sinks.
type Sink interface {
	// Write writes a batch, in order. nil acknowledges every record in it.
	// A *BatchError says how many of them were written before the one that
	// failed; any other error says none was. A batch is written again, or
	// from the record that failed, so writes must be idempotent. Write is
	// called on one goroutine at a time, unless the Sink is a
	// ParallelSink, and must return once ctx ends.
	Write(ctx context.Context, batch []*Record) error
}

// ParallelSink is a Sink whose Write may be called on several goroutines at
// once: a job's sink implementing it gets job.max_parallel writers, each
// record going to the writer its key hashes to, so that a key is never
// written out of order.
type ParallelSink interface {
	Sink
	// ParallelWrites marks the sink.
	ParallelWrites()
}

// Chunker is a Source whose snapshot can be split into chunks, which a job
// (a pipeline of mode once) reads several at a time, job.max_parallel, and
// resumes by chunk: a chunk is not read again once every sink has
// acknowledged all its records. A database's table splits by ranges of its
// primary key, a collection by ranges of _id.
type Chunker interface {
	Source
	// Chunks plans the snapshot as chunks, in the order they are read;
	// none says the source is not split, and Run reads it whole. It is
	// called when a job starts, and the plan is kept in the job's
	// checkpoint, so that a job resumed reads the chunks it planned even if
	// the data has changed since. At most MaxChunks.
	Chunks(ctx context.Context) ([]Chunk, error)
	// ReadChunk reads chunk c, handing each record to emit in order: from
	// its start when from is nil, or after the record whose Position from
	// is. Positions are the chunk's own; the runtime puts them in the
	// job's. It returns nil at the chunk's end, and emit's error, ctx's or
	// its own otherwise; an error restarts the job from its checkpoint.
	// Several chunks are read at once, each on a goroutine of its own, and
	// their records' transactions are dropped: a snapshot has none.
	ReadChunk(ctx context.Context, c Chunk, from Position, emit func(*Record) error) error
}

// MaxChunks is the most chunks a snapshot may be split into.
const MaxChunks = 100000

// Chunk is a part of a source's snapshot: a range of keys of a namespace.
type Chunk struct {
	// Namespace is the table, index or collection the chunk is of, when the
	// source reads several.
	Namespace string `json:"namespace,omitempty"`
	// Lo and Hi bound the chunk's keys, from Lo to before Hi, in the
	// source's own JSON form; nil is unbounded.
	Lo json.RawMessage `json:"lo,omitempty"`
	Hi json.RawMessage `json:"hi,omitempty"`
	// Rows is how many records the source reckons the chunk holds, 0 when
	// it cannot tell: the job's ETA comes from them.
	Rows int64 `json:"rows,omitempty"`
}

// BatchError is a Sink's error about one record of a batch: those before it
// were written.
type BatchError struct {
	// Index is the record's, in the batch.
	Index int
	Err   error
}

func (e *BatchError) Error() string { return fmt.Sprintf("record %d of the batch: %v", e.Index, e.Err) }
func (e *BatchError) Unwrap() error { return e.Err }

// Permanent marks err as one that trying again cannot cure, as a document
// a search index's mapping refuses: a sink's write failing with it, alone
// or as a BatchError's Err, goes to on_error at once, without the retries.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

// IsPermanent reports whether err, or an error it wraps, was marked with
// Permanent.
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Transform changes records on their way from the source to the sinks. It is
// a module of the namespace sync.transforms.
type Transform interface {
	// Apply hands emit what r becomes: nothing to drop it, r or a change of
	// it, or several records to split it, each made with r.Clone so that it
	// counts as r. It is called on one goroutine, record after record, and
	// must not block. An error fails the record, which on_error then
	// takes.
	Apply(r *Record, emit func(*Record)) error
}

// BatchTransform is a Transform that takes the records several at a time,
// as a WASM plugin does, to spare a call for each. The dispatcher hands it
// all the records it holds at once, rather than one by one to Apply.
type BatchTransform interface {
	Transform
	// ApplyBatch hands emit(i, o) what in[i] becomes, as Apply would, in
	// order: i never goes back. fail(i, err) fails in[i], dropping what it
	// became. An error fails every record of in. It is called on one
	// goroutine and must return within a bound of its own.
	ApplyBatch(in []*Record, emit func(i int, o *Record), fail func(i int, err error)) error
}

// Codec turns bytes into records and records into bytes, for the sources
// that read bytes (file lines, Kafka messages, HTTP bodies, S3 objects) and
// the sinks that write them. It is a module of the namespace sync.codecs.
type Codec interface {
	// Decode hands emit the records data holds: a line, a message or a
	// body, as the source reads them; zero or more. The records are the
	// caller's, without positions: the source sets them.
	Decode(data []byte, emit func(*Record)) error
	// Encode appends recs, encoded, to b.
	Encode(b []byte, recs []*Record) ([]byte, error)
}

// Codecs is the namespace of codecs.
const Codecs = "sync.codecs"

// errStopped is Emit's error once the pipeline stops.
var errStopped = errors.New("sync: the pipeline is stopping")

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
}

// Confirmer is a Source told of each checkpoint once it is saved, as a
// PostgreSQL slot is told the LSN it may release, never before every sink
// has acknowledged it.
type Confirmer interface {
	Confirm(p Position)
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

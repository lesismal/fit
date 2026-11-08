package fitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// The connectors here are for tests and trials: generate makes records,
// memory keeps them, stdout prints them.

func init() {
	module.Register(module.Info{ID: Sources + ".generate", New: func() any { return new(Generate) },
		Doc: "Makes numbered records of every value type, the same ones each time, in transactions of tx_size: for trying out sinks and transforms, and for tests. Its position is the count of records made; it ends after records."})
	module.Register(module.Info{ID: Sinks + ".memory", New: func() any { return new(Memory) },
		Doc: "Keeps the records written in memory, in a store named by store, up to max_records: for tests and trials; the records are gone when fit stops."})
	module.Register(module.Info{ID: Sinks + ".stdout", New: func() any { return new(Stdout) },
		Doc: "Writes each record to fit's standard output as a line of its canonical JSON: for trials and for watching a pipeline."})
}

// MaxGenerateTx is the most records a generate transaction may hold.
const MaxGenerateTx = 100000

// Generate is the source generate.
type Generate struct {
	Records      int64           `json:"records,omitempty" min:"0" doc:"How many records to make, after which the source ends and the pipeline stops; 0, or left out, makes them without end."`
	TxSize       int             `json:"tx_size,omitempty" min:"0" max:"100000" default:"1" doc:"How many records a transaction holds; 1, or left out, makes each record a transaction of its own, without a transaction ID."`
	Namespace    string          `json:"namespace,omitempty" default:"\"generate.records\"" doc:"The records' namespace. generate.records when left out."`
	Interval     config.Duration `json:"interval,omitempty" doc:"How long to wait after each transaction; none when left out."`
	ChunkRecords int64           `json:"chunk_records,omitempty" min:"0" doc:"In a job (mode once), split the records into chunks of this many, by ranges of their ids, read job.max_parallel at a time, each record a transaction of its own: for trying out jobs. Needs records; at most 100000 chunks. Not split when left out."`
}

// Chunks plans chunk_records' chunks, none without it.
func (g *Generate) Chunks(context.Context) ([]Chunk, error) {
	if g.ChunkRecords <= 0 || g.Records <= 0 {
		return nil, nil
	}
	if (g.Records+g.ChunkRecords-1)/g.ChunkRecords > MaxChunks {
		return nil, fmt.Errorf("generate: %d records in chunks of %d are more than %d chunks", g.Records, g.ChunkRecords, MaxChunks)
	}
	var plan []Chunk
	for lo := int64(0); lo < g.Records; lo += g.ChunkRecords {
		hi := min(lo+g.ChunkRecords, g.Records)
		plan = append(plan, Chunk{Namespace: g.Namespace, Lo: fmt.Appendf(nil, "%d", lo), Hi: fmt.Appendf(nil, "%d", hi), Rows: hi - lo})
	}
	return plan, nil
}

// ReadChunk makes the records of c, from its Lo, or from's count, to before
// its Hi.
func (g *Generate) ReadChunk(ctx context.Context, c Chunk, from Position, emit func(*Record) error) error {
	var lo, hi int64
	if err := errors.Join(json.Unmarshal(c.Lo, &lo), json.Unmarshal(c.Hi, &hi)); err != nil {
		return fmt.Errorf("generate: the chunk %s-%s: %w", c.Lo, c.Hi, err)
	}
	if from != nil {
		var p struct{ N int64 }
		if err := json.Unmarshal(from, &p); err != nil {
			return fmt.Errorf("generate: the position %s: %w", from, err)
		}
		lo = p.N
	}
	for n := lo; n < hi; n++ {
		if err := emit(GenerateRecord(g.Namespace, n, g.Records, 1)); err != nil {
			return err
		}
		if d := g.Interval.Std(); d > 0 {
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
		}
	}
	return nil
}

func (g *Generate) txSize() int64 { return max(int64(g.TxSize), 1) }

// GenerateRecord returns the record generate makes at n, counting from 0,
// with transactions of txSize.
func GenerateRecord(namespace string, n, total, txSize int64) *Record {
	if namespace == "" {
		namespace = "generate.records"
	}
	txSize = max(txSize, 1)
	key := Map(Field{"id", Int(n)})
	row := Map(
		Field{"id", Int(n)},
		Field{"name", String(fmt.Sprintf("record-%d", n))},
		Field{"count", Uint(uint64(n) * 3)},
		Field{"score", Float(float64(n) / 4)},
		Field{"price", Decimal(fmt.Sprintf("%d.%02d", n, n%100))},
		Field{"even", Bool(n%2 == 0)},
		Field{"blob", Bytes([]byte{byte(n), byte(n >> 8), 0xff})},
		Field{"at", Time(time.Unix(1_700_000_000+n, n%1000*1_000_000).UTC())},
		Field{"day", Date(2026, 1, 1+int(n%365))},
		Field{"doc", JSON(fmt.Appendf(nil, `{"n":%d}`, n))},
		Field{"tags", Array(String("a"), Int(n%10))},
		Field{"none", Null()},
	)
	r := &Record{Source: "generate", Namespace: namespace, Key: key, CommitTime: time.Unix(1_700_000_000+n, 0).UTC(),
		Position: Position(fmt.Appendf(nil, `{"n":%d}`, n+1))}
	switch n % 7 {
	case 5:
		r.Op, r.Before, r.After = OpUpdate, key, row
	case 6:
		r.Op, r.Before = OpDelete, row
	default:
		r.Op, r.After = OpInsert, row
	}
	if txSize > 1 {
		r.Tx = Tx{ID: fmt.Sprintf("tx-%d", n/txSize), First: n%txSize == 0, Last: n%txSize == txSize-1 || total > 0 && n == total-1}
	}
	return r
}

// Run makes the records from the checkpoint's count on.
func (g *Generate) Run(ctx context.Context, r *Run) error {
	var n int64
	if r.From != nil {
		var p struct{ N int64 }
		if err := json.Unmarshal(r.From, &p); err != nil {
			return fmt.Errorf("generate: the checkpoint %s: %w", r.From, err)
		}
		n = p.N
	}
	for ; g.Records == 0 || n < g.Records; n++ {
		rec := GenerateRecord(g.Namespace, n, g.Records, g.txSize())
		if err := r.Emit(rec); err != nil {
			return err
		}
		if d := g.Interval.Std(); d > 0 && rec.boundary() {
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
		}
	}
	return nil
}

// DefaultMemoryRecords is the most records a memory store keeps, unless its
// max_records says otherwise.
const DefaultMemoryRecords = 100000

// Memory is the sink memory.
type Memory struct {
	Store      string `json:"store,omitempty" default:"\"default\"" doc:"The store's name: sinks naming the same store write into it. default when left out."`
	MaxRecords int    `json:"max_records,omitempty" min:"0" default:"100000" doc:"The most records the store keeps; a write past it fails, as a full disk would. 100000 when left out or 0."`
}

// ParallelWrites marks Memory a ParallelSink: its store has a lock.
func (*Memory) ParallelWrites() {}

// Write keeps batch in the store.
func (m *Memory) Write(ctx context.Context, batch []*Record) error {
	return MemoryStore(m.Store).write(ctx, batch, m.MaxRecords)
}

// MemStore is a memory sink's store.
type MemStore struct {
	mu   sync.Mutex
	recs []*Record
	hook func(ctx context.Context, batch []*Record) error
}

var (
	memMu     sync.Mutex
	memStores = map[string]*MemStore{}
)

// MemoryStore returns the store named name, made when there is none.
func MemoryStore(name string) *MemStore {
	if name == "" {
		name = "default"
	}
	memMu.Lock()
	defer memMu.Unlock()
	s := memStores[name]
	if s == nil {
		s = &MemStore{}
		memStores[name] = s
	}
	return s
}

// SetHook has the store call hook with each batch before keeping it, for
// tests: an error is the write's, and a *BatchError keeps the records
// before its Index. nil removes the hook.
func (s *MemStore) SetHook(hook func(ctx context.Context, batch []*Record) error) {
	s.mu.Lock()
	s.hook = hook
	s.mu.Unlock()
}

// Records returns the records kept, in the order written.
func (s *MemStore) Records() []*Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Record(nil), s.recs...)
}

// Reset empties the store and removes its hook.
func (s *MemStore) Reset() {
	s.mu.Lock()
	s.recs, s.hook = nil, nil
	s.mu.Unlock()
}

func (s *MemStore) write(ctx context.Context, batch []*Record, most int) error {
	if most <= 0 {
		most = DefaultMemoryRecords
	}
	s.mu.Lock()
	hook := s.hook
	s.mu.Unlock()
	keep := batch
	var err error
	if hook != nil {
		if err = hook(ctx, batch); err != nil {
			// A BatchError's index names a record of the batch; one past
			// it says, as the runtime takes it, that none was written.
			keep = nil
			if be, ok := err.(*BatchError); ok && be.Index >= 0 && be.Index < len(batch) {
				keep = batch[:be.Index]
			}
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if room := most - len(s.recs); len(keep) > room {
		s.recs = append(s.recs, keep[:room]...)
		return &BatchError{Index: room, Err: fmt.Errorf("the memory store is full: %d records", most)}
	}
	s.recs = append(s.recs, keep...)
	return err
}

// Stdout is the sink stdout.
type Stdout struct{}

// stdout is where Stdout writes; tests replace it.
var stdout io.Writer = os.Stdout

var stdoutMu sync.Mutex

// Write prints batch, a line for each record.
func (*Stdout) Write(_ context.Context, batch []*Record) error {
	var buf []byte
	for _, r := range batch {
		var err error
		if buf, err = r.AppendJSON(buf); err != nil {
			return err
		}
		buf = append(buf, '\n')
	}
	stdoutMu.Lock()
	defer stdoutMu.Unlock()
	_, err := stdout.Write(buf)
	return err
}

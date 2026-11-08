package wasm

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

// The sync modules a WASM plugin makes: a transform, a codec, a source and
// a sink, each named wasm in its namespace.
func init() {
	module.Register(module.Info{ID: fitsync.Transforms + ".wasm", New: func() any { return new(SyncTransform) },
		Doc: "Runs a WebAssembly plugin that exports fit_sync_transform over the records, a batch at a time: it emits zero or more records for each, so it drops, changes, splits and routes them. The records cross as canonical JSON; a call that fails, traps, or runs past its time or memory fails its batch, which on_error then takes."})
	module.Register(module.Info{ID: fitsync.Codecs + ".wasm", New: func() any { return new(SyncCodec) },
		Doc: "Runs a WebAssembly plugin that exports fit_sync_decode, turning bytes (a line, a message, a body) into records, and fit_sync_encode, turning records into bytes, for the sources and sinks that read and write bytes."})
	module.Register(module.Info{ID: fitsync.Sources + ".wasm", New: func() any { return new(SyncSource) },
		Doc: "Runs a WebAssembly plugin that exports fit_sync_poll: given its checkpoint, it returns records and the next checkpoint, reading through fit's HTTP client from the hosts allow_hosts names, with no sockets of its own. A poll's records are saved as a whole: the checkpoint moves past the last of them only."})
	module.Register(module.Info{ID: fitsync.Sinks + ".wasm", New: func() any { return new(SyncSink) },
		Doc: "Runs a WebAssembly plugin that exports fit_sync_write over each batch, writing it through fit's HTTP client to the hosts allow_hosts names, with no sockets of its own; it may name the record that failed, those before it written."})
}

// Defaults of the sync modules' params.
const (
	DefaultSyncTimeout          = time.Second
	DefaultSyncConnectorTimeout = 30 * time.Second
	DefaultSyncBatchRecords     = 256
	DefaultSyncMaxOutputBytes   = 16 << 20
	DefaultSyncMaxResponseBytes = 4 << 20
	DefaultSyncPollInterval     = time.Second
	maxSyncTransformInstances   = 4
)

// SyncTransform is the transform wasm.
type SyncTransform struct {
	Path           string          `json:"path" fit:"required" doc:"The plugin: a WebAssembly module, a WASI reactor, that exports fit_sync_transform; package plugin/wasm/guest builds one from Go with GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared."`
	Config         string          `json:"config,omitempty" doc:"A string the plugin reads, for settings of its own."`
	Instances      int             `json:"instances,omitempty" min:"0" max:"16" doc:"How many instances of the plugin may run a batch's parts at once, each with its own memory, made as they are needed: the CPUs fit has, at most 4, when left out or 0."`
	BatchRecords   int             `json:"batch_records,omitempty" min:"0" max:"100000" default:"256" doc:"The most records one call takes; the records the pipeline holds are split into calls of this many, run on the instances at once. 256 when left out or 0."`
	Timeout        config.Duration `json:"timeout,omitempty" default:"\"1s\"" doc:"How long a call may run; past it the call is stopped, its instance made again, and its records fail, as on_error says. 1s when left out."`
	MaxMemoryBytes int64           `json:"max_memory_bytes,omitempty" min:"0" default:"134217728" doc:"The most memory an instance may have; a call that asks for more fails its records. 128 MiB when left out or 0."`
	MaxOutputBytes int64           `json:"max_output_bytes,omitempty" min:"0" default:"16777216" doc:"The most bytes of records a call may hand back; past it the call fails. 16 MiB when left out or 0."`

	pool *syncPool
}

// SyncCodec is the codec wasm.
type SyncCodec struct {
	Path           string          `json:"path" fit:"required" doc:"The plugin: a WebAssembly module, a WASI reactor, that exports fit_sync_decode, fit_sync_encode, or both; package plugin/wasm/guest builds one from Go with GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared."`
	Config         string          `json:"config,omitempty" doc:"A string the plugin reads, for settings of its own."`
	Timeout        config.Duration `json:"timeout,omitempty" default:"\"1s\"" doc:"How long a call may run; past it the call is stopped and fails. 1s when left out."`
	MaxMemoryBytes int64           `json:"max_memory_bytes,omitempty" min:"0" default:"134217728" doc:"The most memory the plugin's instance may have. 128 MiB when left out or 0."`
	MaxOutputBytes int64           `json:"max_output_bytes,omitempty" min:"0" default:"16777216" doc:"The most bytes, of records or of encoded data, a call may hand back. 16 MiB when left out or 0."`

	decode, encode *syncPool
}

// SyncSource is the source wasm.
type SyncSource struct {
	Path             string          `json:"path" fit:"required" doc:"The plugin: a WebAssembly module, a WASI reactor, that exports fit_sync_poll; package plugin/wasm/guest builds one from Go with GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared."`
	Config           string          `json:"config,omitempty" doc:"A string the plugin reads, for settings of its own."`
	AllowHosts       []string        `json:"allow_hosts,omitempty" doc:"The hosts the plugin's HTTP requests may go to: a host name or address, any port; host:port; or *.example.com for its subdomains. A request to any other fails the poll. None when left out."`
	PollInterval     config.Duration `json:"poll_interval,omitempty" default:"\"1s\"" doc:"How long to wait before polling again once the plugin has caught up, or returned nothing. 1s when left out."`
	Timeout          config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long a poll may run, its HTTP requests and sleeps included; past it the poll fails, and the pipeline starts again from its checkpoint. 30s when left out."`
	MaxMemoryBytes   int64           `json:"max_memory_bytes,omitempty" min:"0" default:"134217728" doc:"The most memory the plugin's instance may have. 128 MiB when left out or 0."`
	MaxOutputBytes   int64           `json:"max_output_bytes,omitempty" min:"0" default:"16777216" doc:"The most bytes of records a poll may hand back. 16 MiB when left out or 0."`
	MaxResponseBytes int64           `json:"max_response_bytes,omitempty" min:"0" default:"4194304" doc:"The largest HTTP response body the plugin may be given; a larger one fails its request. 4 MiB when left out or 0."`

	pool *syncPool
}

// SyncSink is the sink wasm.
type SyncSink struct {
	Path             string          `json:"path" fit:"required" doc:"The plugin: a WebAssembly module, a WASI reactor, that exports fit_sync_write; package plugin/wasm/guest builds one from Go with GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared."`
	Config           string          `json:"config,omitempty" doc:"A string the plugin reads, for settings of its own."`
	AllowHosts       []string        `json:"allow_hosts,omitempty" doc:"The hosts the plugin's HTTP requests may go to: a host name or address, any port; host:port; or *.example.com for its subdomains. A request to any other fails the write. None when left out."`
	Timeout          config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long a write may run, its HTTP requests and sleeps included; past it the write fails, and is tried again as retry says. 30s when left out."`
	MaxMemoryBytes   int64           `json:"max_memory_bytes,omitempty" min:"0" default:"134217728" doc:"The most memory the plugin's instance may have. 128 MiB when left out or 0."`
	MaxResponseBytes int64           `json:"max_response_bytes,omitempty" min:"0" default:"4194304" doc:"The largest HTTP response body the plugin may be given; a larger one fails its request. 4 MiB when left out or 0."`

	pool *syncPool
}

func orDuration(d config.Duration, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d.Std()
}

func orInt64(n, def int64) int64 {
	if n <= 0 {
		return def
	}
	return n
}

// allowHosts checks allow_hosts, and returns it, never nil, for a connector.
func allowHosts(ctx *module.Context, hosts []string) ([]string, error) {
	for i, h := range hosts {
		if h == "" || h == "*" {
			return nil, ctx.Params.Field("allow_hosts").Index(i).Errorf("%q names no host", h)
		}
	}
	return append([]string{}, hosts...), nil
}

// Provision compiles the plugin and makes its first instance.
func (t *SyncTransform) Provision(ctx *module.Context) error {
	if t.Instances == 0 {
		t.Instances = min(goruntime.GOMAXPROCS(0), maxSyncTransformInstances)
	}
	if t.BatchRecords == 0 {
		t.BatchRecords = DefaultSyncBatchRecords
	}
	var err error
	t.pool, err = newSyncPool(syncOpts{path: t.Path, config: t.Config, export: exportTransform, instances: t.Instances,
		timeout: orDuration(t.Timeout, DefaultSyncTimeout), maxMemory: orInt64(t.MaxMemoryBytes, DefaultMaxMemoryBytes),
		maxOutput: orInt64(t.MaxOutputBytes, DefaultSyncMaxOutputBytes)})
	if err != nil {
		return ctx.Errorf("path", "%v", err)
	}
	return nil
}

// Cleanup stops the plugin's instances.
func (t *SyncTransform) Cleanup() {
	if t.pool != nil {
		t.pool.close()
	}
}

// Apply is ApplyBatch for one record.
func (t *SyncTransform) Apply(r *fitsync.Record, emit func(*fitsync.Record)) error {
	var failed error
	err := t.ApplyBatch([]*fitsync.Record{r}, func(_ int, o *fitsync.Record) { emit(o) }, func(_ int, err error) { failed = err })
	if err != nil {
		return err
	}
	return failed
}

// ApplyBatch splits in into calls of batch_records, runs them on the
// instances at once, and hands on what they made in order. A call that
// fails fails each of its records.
func (t *SyncTransform) ApplyBatch(in []*fitsync.Record, emit func(int, *fitsync.Record), fail func(int, error)) error {
	size := t.BatchRecords
	calls := make([]syncCall, (len(in)+size-1)/size)
	errs := make([]error, len(calls))
	do := func(j int) {
		part := in[j*size : min((j+1)*size, len(in))]
		sc := &calls[j]
		sc.n = len(part)
		var err error
		if sc.input, err = fitsync.AppendBatchJSON(nil, part); err != nil {
			errs[j] = err
			return
		}
		status, err := t.pool.call(context.Background(), sc)
		switch {
		case err != nil:
			errs[j] = err
		case status != syncOK:
			errs[j] = sc.failure(status)
		}
	}
	if workers := min(len(calls), t.Instances); workers <= 1 {
		for j := range calls {
			do(j)
		}
	} else {
		var next sync.Mutex
		n := 0
		var wg sync.WaitGroup
		for range workers {
			wg.Go(func() {
				for {
					next.Lock()
					j := n
					n++
					next.Unlock()
					if j >= len(calls) {
						return
					}
					do(j)
				}
			})
		}
		wg.Wait()
	}
	for j := range calls {
		base := j * size
		if errs[j] != nil {
			for i := range calls[j].n {
				fail(base+i, errs[j])
			}
			continue
		}
		for _, f := range calls[j].fails {
			fail(base+f.i, errors.New(f.msg))
		}
		for _, o := range calls[j].outs {
			emit(base+o.i, o.rec)
		}
	}
	return nil
}

// Provision compiles the plugin and makes an instance for each of its
// exports.
func (c *SyncCodec) Provision(ctx *module.Context) error {
	o := syncOpts{path: c.Path, config: c.Config, instances: 1, timeout: orDuration(c.Timeout, DefaultSyncTimeout),
		maxMemory: orInt64(c.MaxMemoryBytes, DefaultMaxMemoryBytes), maxOutput: orInt64(c.MaxOutputBytes, DefaultSyncMaxOutputBytes)}
	var errs []error
	for _, e := range []struct {
		export string
		pool   **syncPool
	}{{exportDecode, &c.decode}, {exportEncode, &c.encode}} {
		o.export = e.export
		p, err := newSyncPool(o)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		*e.pool = p
	}
	if len(errs) == 2 {
		return ctx.Errorf("path", "%v", errors.Join(errs...))
	}
	return nil
}

// Cleanup stops the plugin's instances.
func (c *SyncCodec) Cleanup() {
	for _, p := range []*syncPool{c.decode, c.encode} {
		if p != nil {
			p.close()
		}
	}
}

// Decode hands emit the records the plugin makes of data.
func (c *SyncCodec) Decode(data []byte, emit func(*fitsync.Record)) error {
	if c.decode == nil {
		return errors.New("wasm codec: the plugin does not export " + exportDecode)
	}
	sc := &syncCall{input: data, n: 1}
	if err := c.run(c.decode, sc); err != nil {
		return err
	}
	for _, o := range sc.outs {
		emit(o.rec)
	}
	return nil
}

// Encode appends what the plugin makes of recs to b.
func (c *SyncCodec) Encode(b []byte, recs []*fitsync.Record) ([]byte, error) {
	if c.encode == nil {
		return b, errors.New("wasm codec: the plugin does not export " + exportEncode)
	}
	input, err := fitsync.AppendBatchJSON(nil, recs)
	if err != nil {
		return b, err
	}
	sc := &syncCall{input: input, n: len(recs)}
	if err := c.run(c.encode, sc); err != nil {
		return b, err
	}
	return append(b, sc.output...), nil
}

func (c *SyncCodec) run(p *syncPool, sc *syncCall) error {
	status, err := p.call(context.Background(), sc)
	switch {
	case err != nil:
		return fmt.Errorf("wasm codec: %w", err)
	case status != syncOK:
		return fmt.Errorf("wasm codec: %w", sc.failure(status))
	}
	return nil
}

// Provision compiles the plugin and makes its instance and HTTP client.
func (s *SyncSource) Provision(ctx *module.Context) error {
	allow, err := allowHosts(ctx, s.AllowHosts)
	if err != nil {
		return err
	}
	s.pool, err = newSyncPool(syncOpts{path: s.Path, config: s.Config, export: exportPoll, instances: 1,
		timeout: orDuration(s.Timeout, DefaultSyncConnectorTimeout), maxMemory: orInt64(s.MaxMemoryBytes, DefaultMaxMemoryBytes),
		maxOutput: orInt64(s.MaxOutputBytes, DefaultSyncMaxOutputBytes), allow: allow,
		maxResponse: orInt64(s.MaxResponseBytes, DefaultSyncMaxResponseBytes), engine: ctx.Engine()})
	if err != nil {
		return ctx.Errorf("path", "%v", err)
	}
	return nil
}

// Cleanup stops the plugin's instance and its HTTP client.
func (s *SyncSource) Cleanup() {
	if s.pool != nil {
		s.pool.close()
	}
}

// Run polls the plugin from the checkpoint on. Each poll's records go out
// with their transactions and positions cleared, the last carrying the
// checkpoint the poll moved to, so a restart polls again from the last
// checkpoint whose records every sink has.
func (s *SyncSource) Run(ctx context.Context, r *fitsync.Run) error {
	cp := []byte(r.From)
	interval := orDuration(s.PollInterval, DefaultSyncPollInterval)
	for {
		sc := &syncCall{input: cp, n: 1}
		status, err := s.pool.call(ctx, sc)
		switch {
		case err != nil:
			return fmt.Errorf("wasm source: %w", err)
		case status != syncOK && status != syncCaughtUp:
			return fmt.Errorf("wasm source: %w", sc.failure(status))
		}
		next := cp
		if sc.checkpoint != nil {
			next = sc.checkpoint
		}
		for i, o := range sc.outs {
			rec := o.rec
			rec.Tx, rec.Position = fitsync.Tx{}, nil
			if rec.Source == "" {
				rec.Source = "wasm"
			}
			if i == len(sc.outs)-1 && next != nil {
				rec.Position = fitsync.Position(next)
			}
			if err := r.Emit(rec); err != nil {
				return err
			}
		}
		cp = next
		if status == syncCaughtUp && r.Once {
			return nil
		}
		if status == syncCaughtUp || len(sc.outs) == 0 {
			t := time.NewTimer(interval)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			}
		}
	}
}

// Provision compiles the plugin and makes its instance and HTTP client.
func (s *SyncSink) Provision(ctx *module.Context) error {
	allow, err := allowHosts(ctx, s.AllowHosts)
	if err != nil {
		return err
	}
	s.pool, err = newSyncPool(syncOpts{path: s.Path, config: s.Config, export: exportWrite, instances: 1,
		timeout: orDuration(s.Timeout, DefaultSyncConnectorTimeout), maxMemory: orInt64(s.MaxMemoryBytes, DefaultMaxMemoryBytes),
		maxOutput: maxField, allow: allow, maxResponse: orInt64(s.MaxResponseBytes, DefaultSyncMaxResponseBytes), engine: ctx.Engine()})
	if err != nil {
		return ctx.Errorf("path", "%v", err)
	}
	return nil
}

// Cleanup stops the plugin's instance and its HTTP client.
func (s *SyncSink) Cleanup() {
	if s.pool != nil {
		s.pool.close()
	}
}

// Write hands the plugin the batch. A record it names as failed fails the
// write from there, those before it written.
func (s *SyncSink) Write(ctx context.Context, batch []*fitsync.Record) error {
	input, err := fitsync.AppendBatchJSON(nil, batch)
	if err != nil {
		return err
	}
	sc := &syncCall{input: input, n: len(batch)}
	status, err := s.pool.call(ctx, sc)
	switch {
	case err != nil:
		return fmt.Errorf("wasm sink: %w", err)
	case len(sc.fails) > 0:
		f := sc.fails[0]
		for _, g := range sc.fails[1:] {
			if g.i < f.i {
				f = g
			}
		}
		return &fitsync.BatchError{Index: f.i, Err: fmt.Errorf("wasm sink: %s", f.msg)}
	case status != syncOK:
		return fmt.Errorf("wasm sink: %w", sc.failure(status))
	}
	return nil
}

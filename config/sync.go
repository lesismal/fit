package config

import (
	"slices"
	"strings"
	"time"

	"github.com/lesismal/fit/internal/cron"
)

// The defaults of Sync's pipelines.
const (
	DefaultSyncQueueRecords       = 10000
	DefaultSyncQueueBytes   int64 = 64 << 20
	DefaultSyncBatchRecords       = 500
	DefaultSyncBatchBytes   int64 = 4 << 20
	DefaultSyncMaxAttempts        = 5
	DefaultSyncCheckpoint         = 5 * time.Second
	DefaultSyncBatchWait          = time.Second
	DefaultSyncBackoff            = 200 * time.Millisecond
	DefaultSyncMaxBackoff         = 30 * time.Second
	DefaultSyncMaxParallel        = 4
	MaxSyncParallel               = 256
)

// The modes and error policies of a SyncPipeline.
const (
	SyncStream     = "stream"
	SyncOnce       = "once"
	SyncStop       = "stop"
	SyncSkip       = "skip"
	SyncDeadLetter = "dead_letter"
)

// Sync is fit's sync role: pipelines that read records from a source and
// write them to sinks, with a checkpoint saved only once every sink has
// acknowledged what it covers. See docs/decisions/036-sync.md.
type Sync struct {
	StateDir  string         `json:"state_dir" fit:"required" doc:"The directory each pipeline's checkpoint is kept in, as <state_dir>/<pipeline>.json, written to a temporary file, synced and renamed into place. Made when missing. A pipeline starts from its checkpoint there; moving a pipeline to another fit means moving its file."`
	Pipelines []SyncPipeline `json:"pipelines,omitempty" doc:"The pipelines, each a source, its transforms in order and its sinks, with one checkpoint. A reload starts the pipelines added, stops those removed and restarts only those whose settings changed."`

	At Value `json:"-" fit:"self"`
}

// SyncPipeline is one pipeline: a source, its transforms and its sinks.
type SyncPipeline struct {
	Name               string      `json:"name" fit:"required" doc:"The pipeline's name, unique, which names its checkpoint file. Letters, digits, '_', '.' and '-'."`
	Source             ModuleRef   `json:"source" fit:"required" module:"sync.sources" doc:"Where the records come from, and its params."`
	Transforms         []ModuleRef `json:"transforms,omitempty" module:"sync.transforms" doc:"What each record goes through, in order, before the sinks: a transform may change, drop or split a record. None when left out."`
	Sinks              []SyncSink  `json:"sinks" fit:"required" doc:"Where every record is written, at least one: each sink takes every record, in order, in batches of its own, and the checkpoint is the position all of them have acknowledged."`
	Mode               string      `json:"mode,omitempty" enum:"stream,once" default:"\"stream\"" doc:"stream follows the source for good; once stops at the source's end, as a batch job, with the checkpoint written. A pipeline also stops when its source has an end and reaches it. stream when left out."`
	OnError            string      `json:"on_error,omitempty" enum:"stop,skip,dead_letter" default:"\"stop\"" doc:"What happens to a record a sink still fails to write, or a transform fails on, once its attempts are spent: stop stops the pipeline, its checkpoint before the record, until the config changes or fit restarts; skip leaves the record out of that sink, and logs it; dead_letter writes it, with its error, to the dead_letter sink before going on. stop when left out."`
	DeadLetter         *SyncSink   `json:"dead_letter,omitempty" doc:"The sink records go to when on_error is dead_letter, each a message record holding the record's canonical JSON, the error, where it failed and the attempts made. Needed with dead_letter, and only with it. A dead letter it cannot write either, after its attempts, stops the pipeline."`
	CheckpointInterval Duration    `json:"checkpoint_interval,omitempty" default:"\"5s\"" doc:"How often, at most, the checkpoint is written when it has moved; it is also written when the pipeline stops. Records after the last checkpoint written are written again after a crash, so sinks write idempotently. 5s when left out."`
	Queue              *SyncQueue  `json:"queue,omitempty" doc:"The bound on records read and not yet acknowledged by every sink: past it the source waits, so a slow sink slows the source rather than growing memory. The defaults when left out."`
	Retry              *SyncRetry  `json:"retry,omitempty" doc:"How a failed write is tried again, with a backoff doubling up to max_backoff, before on_error applies. A source that fails restarts the pipeline from its checkpoint after the same backoff, for as long as it fails. The defaults when left out."`
	Job                *SyncJob    `json:"job,omitempty" doc:"How a pipeline of mode once runs as a batch job: its source's snapshot read in chunks, max_parallel at a time, where the source can split it, its progress kept in the checkpoint so that a job interrupted goes on from its finished chunks, and a schedule starting it again. Only with mode once; the defaults when left out."`

	At Value `json:"-" fit:"self"`
}

// SyncSink is one of a pipeline's sinks.
type SyncSink struct {
	Name  string     `json:"name" fit:"required" doc:"The sink's name, unique in its pipeline, which logs and errors call it by. Letters, digits, '_', '.' and '-'."`
	Sink  ModuleRef  `json:"sink" fit:"required" module:"sync.sinks" doc:"What the records are written to, and its params."`
	Batch *SyncBatch `json:"batch,omitempty" doc:"How records are gathered into a write: a batch is written once it holds max_records records or max_bytes bytes, or max_wait after its first record arrived, whichever comes first, and at once when the source has ended. The defaults when left out."`

	At Value `json:"-" fit:"self"`
}

// SyncJob is how a pipeline of mode once runs as a batch job.
type SyncJob struct {
	MaxParallel int    `json:"max_parallel,omitempty" min:"0" max:"256" default:"4" doc:"How many chunks of the source's snapshot are read at once, and how many writers each sink that can take writes at once (the memory sink, the database sinks) has, a record's key always going to the same writer, so that one key is never written out of order. A source that cannot split its snapshot is read whole, and a sink that cannot take writes at once has one writer. 4 when left out or 0."`
	Schedule    string `json:"schedule,omitempty" doc:"When the job starts again, in cron's five fields (minute, hour, day of the month, month, day of the week, as 0 3 * * * for 03:00 every day) or a descriptor (@hourly, @daily, @weekly, @monthly, @yearly), in time_zone. A run never overlaps one still going: times passing while a job runs are skipped. A job interrupted, by a crash or a restart, goes on at once; one finished waits for the next time. Without a schedule a job runs once, when its pipeline starts, and not again once finished."`
	TimeZone    string `json:"time_zone,omitempty" default:"\"UTC\"" doc:"The IANA time zone the schedule is in, as Asia/Shanghai, or Local for the host's. UTC when left out. Times a daylight saving change skips do not run; times it repeats run once."`

	At Value `json:"-" fit:"self"`
}

// Parallel returns job.max_parallel, or its default.
func (p *SyncPipeline) Parallel() int {
	if p.Job == nil || p.Job.MaxParallel <= 0 {
		return DefaultSyncMaxParallel
	}
	return min(p.Job.MaxParallel, MaxSyncParallel)
}

// Schedule returns the job's schedule and its time zone, nil when it has
// none.
func (p *SyncPipeline) Schedule() (*cron.Schedule, *time.Location, error) {
	if p.Job == nil || p.Job.Schedule == "" {
		return nil, nil, nil
	}
	s, err := cron.Parse(p.Job.Schedule)
	if err != nil {
		return nil, nil, err
	}
	tz := p.Job.TimeZone
	if tz == "" {
		tz = "UTC"
	}
	loc, err := time.LoadLocation(tz)
	return s, loc, err
}

// SyncQueue bounds a pipeline's records in flight.
type SyncQueue struct {
	MaxRecords int   `json:"max_records,omitempty" min:"0" default:"10000" doc:"The most records read and not yet acknowledged by every sink. 10000 when left out or 0."`
	MaxBytes   int64 `json:"max_bytes,omitempty" min:"0" default:"67108864" doc:"The most bytes, as the records' sizes are reckoned, read and not yet acknowledged by every sink. A record larger than all of it waits until the queue is empty, and then goes through alone. 64 MiB when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// SyncBatch bounds a sink's batches.
type SyncBatch struct {
	MaxRecords int      `json:"max_records,omitempty" min:"0" default:"500" doc:"The most records in a batch. 500 when left out or 0."`
	MaxBytes   int64    `json:"max_bytes,omitempty" min:"0" default:"4194304" doc:"The most bytes in a batch, as the records' sizes are reckoned; a record larger than it is a batch of its own. 4 MiB when left out or 0."`
	MaxWait    Duration `json:"max_wait,omitempty" default:"\"1s\"" doc:"How long a batch waits for more records after its first arrived. 1s when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// SyncRetry is how failed writes are tried again.
type SyncRetry struct {
	MaxAttempts int      `json:"max_attempts,omitempty" min:"0" max:"1000" default:"5" doc:"How many times a write is tried, the first included, before on_error applies. 5 when left out or 0."`
	Backoff     Duration `json:"backoff,omitempty" default:"\"200ms\"" doc:"The wait before the first try again, doubled at each after. 200ms when left out or 0."`
	MaxBackoff  Duration `json:"max_backoff,omitempty" default:"\"30s\"" doc:"The longest wait between tries. 30s when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// Records returns queue.max_records, or its default.
func (p *SyncPipeline) Records() int {
	if p.Queue == nil || p.Queue.MaxRecords <= 0 {
		return DefaultSyncQueueRecords
	}
	return p.Queue.MaxRecords
}

// Bytes returns queue.max_bytes, or its default.
func (p *SyncPipeline) Bytes() int64 {
	if p.Queue == nil || p.Queue.MaxBytes <= 0 {
		return DefaultSyncQueueBytes
	}
	return p.Queue.MaxBytes
}

// Once reports whether the pipeline's mode is once.
func (p *SyncPipeline) Once() bool { return p.Mode == SyncOnce }

// ErrorPolicy returns on_error, or its default.
func (p *SyncPipeline) ErrorPolicy() string {
	if p.OnError == "" {
		return SyncStop
	}
	return p.OnError
}

// Interval returns checkpoint_interval, or its default.
func (p *SyncPipeline) Interval() time.Duration {
	return durationOr(p.CheckpointInterval, DefaultSyncCheckpoint)
}

// Attempts returns retry.max_attempts, or its default.
func (p *SyncPipeline) Attempts() int {
	if p.Retry == nil || p.Retry.MaxAttempts <= 0 {
		return DefaultSyncMaxAttempts
	}
	return p.Retry.MaxAttempts
}

// Backoff returns the wait before try n, the first try again being 1:
// retry.backoff doubled n-1 times, at most retry.max_backoff.
func (p *SyncPipeline) Backoff(n int) time.Duration {
	b, most := DefaultSyncBackoff, DefaultSyncMaxBackoff
	if r := p.Retry; r != nil {
		b, most = durationOr(r.Backoff, b), durationOr(r.MaxBackoff, most)
	}
	for i := 1; i < n && b < most; i++ {
		b *= 2
	}
	return min(b, most)
}

// Records returns batch.max_records, or its default.
func (s *SyncSink) Records() int {
	if s.Batch == nil || s.Batch.MaxRecords <= 0 {
		return DefaultSyncBatchRecords
	}
	return s.Batch.MaxRecords
}

// Bytes returns batch.max_bytes, or its default.
func (s *SyncSink) Bytes() int64 {
	if s.Batch == nil || s.Batch.MaxBytes <= 0 {
		return DefaultSyncBatchBytes
	}
	return s.Batch.MaxBytes
}

// Wait returns batch.max_wait, or its default.
func (s *SyncSink) Wait() time.Duration {
	if s.Batch == nil {
		return DefaultSyncBatchWait
	}
	return durationOr(s.Batch.MaxWait, DefaultSyncBatchWait)
}

// checkSync checks the sync role's names and references; its modules are
// checked when the config is compiled.
func (c *Config) checkSync(add func(Value, string, ...any)) {
	s := c.Sync
	if s == nil {
		return
	}
	if strings.TrimSpace(s.StateDir) == "" {
		add(s.At.Field("state_dir"), "state_dir may not be empty")
	}
	for i := range s.Pipelines {
		p := &s.Pipelines[i]
		switch {
		case !namePattern.MatchString(p.Name):
			add(p.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", p.Name)
		case slices.IndexFunc(s.Pipelines, func(o SyncPipeline) bool { return o.Name == p.Name }) < i:
			add(p.At.Field("name"), "another pipeline is already named %q", p.Name)
		}
		if p.Source.Module == "" {
			add(p.Source.At.Field("module"), "the module name is empty")
		}
		for j, t := range p.Transforms {
			if t.Module == "" {
				add(p.At.Field("transforms").Index(j).Field("module"), "the module name is empty")
			}
		}
		if len(p.Sinks) == 0 {
			add(p.At.Field("sinks"), "a pipeline needs at least one sink")
		}
		sinks := slices.Clone(p.Sinks)
		if p.DeadLetter != nil {
			sinks = append(sinks, *p.DeadLetter)
		}
		for j := range sinks {
			k := &sinks[j]
			switch {
			case !namePattern.MatchString(k.Name):
				add(k.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", k.Name)
			case slices.IndexFunc(sinks, func(o SyncSink) bool { return o.Name == k.Name }) < j:
				add(k.At.Field("name"), "another sink of pipeline %q is already named %q", p.Name, k.Name)
			}
			if k.Sink.Module == "" {
				add(k.Sink.At.Field("module"), "the module name is empty")
			}
		}
		switch {
		case p.ErrorPolicy() == SyncDeadLetter && p.DeadLetter == nil:
			add(p.At.Field("on_error"), "on_error dead_letter needs a dead_letter sink")
		case p.ErrorPolicy() != SyncDeadLetter && p.DeadLetter != nil:
			add(p.At.Field("dead_letter"), "a dead_letter sink is used only with on_error dead_letter")
		}
		if j := p.Job; j != nil {
			if !p.Once() {
				add(j.At, "job is for pipelines of mode once")
			}
			if j.Schedule != "" {
				if _, err := cron.Parse(j.Schedule); err != nil {
					add(j.At.Field("schedule"), "%v", err)
				}
			}
			if j.TimeZone != "" {
				if _, err := time.LoadLocation(j.TimeZone); err != nil {
					add(j.At.Field("time_zone"), "%q is not a time zone: an IANA name, as Asia/Shanghai, or Local", j.TimeZone)
				}
			}
		}
		if r := p.Retry; r != nil && r.Backoff > 0 && r.MaxBackoff > 0 && r.MaxBackoff < r.Backoff {
			add(r.At.Field("max_backoff"), "max_backoff %s is shorter than backoff %s", r.MaxBackoff, r.Backoff)
		}
	}
}

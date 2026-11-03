// Package fibconf makes the fib.Config of fit's engines from the config's
// engine settings.
package fibconf

import (
	"runtime"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
)

// ReadBufferSize is how much a connection reads its socket into at a time
// when the config does not say: 64 KiB, four times fib's default. A proxied
// body goes on as it is read, a write for each read, so the read's size is
// the size of every write too; at 16 KiB, a 1 MiB transfer over HTTP/1.1
// costs 64 reads, 64 writes and as many ACKs, which was four fifths of fit's
// CPU in the large-transfer workload. A small request is read whole either
// way, and the buffer is the round's, taken from fib's pool, not each
// connection's.
const ReadBufferSize = 64 << 10

// WorkersPerCPU is the ceiling of an engine's pool of workers for each CPU
// fit may use, when the config does not say: a tenth of fib's default. fib
// sizes its pool for handlers that wait on a database or another server;
// fit's never wait, but a worker can still wait on a lock, and the pool
// starts another worker for each task that finds the rest busy. Proxying W1
// at 5,000 requests in flight to one backend, thousands of workers waited on
// the HTTP client's lock, each with its read round's buffers taken, and the
// pool grew to 6,000-9,000 goroutines. At 100 a CPU the tasks wait in the
// pool's queue instead, holding nothing: the peak memory fell from 321-383
// to 186-200 MiB over HTTP/1.1 and from 305-398 to 238-285 MiB over h2, at
// the same throughput.
const WorkersPerCPU = 100

// LoopWaitBlock is the loop_wait that blocks the event loops in epoll_wait
// rather than parking them in Go's network poller, fib's default and fit's.
// Parked, a loop waiting for events gives its P up at once, but Go looks for
// a parked goroutine's events only when a P runs out of work, or every 10ms,
// so on a busy server the loop hears of them late. Blocking is a trade
// rather than a default: W6 (500 connections proxied to nginx at 200,000
// requests a second, 6 CPUs) over HTTP/1.1 had its p99 at 2.88-3.88ms
// blocked against 4.28-4.36ms parked, but its median at 1.57ms against
// 0.95ms; h2 and the maximum did not change, and W1 at 500 in flight served
// h2 0.9% less.
const LoopWaitBlock = "block"

// Config returns fib's default config for an engine named name, with fit's
// own ReadBufferSize, and with what e sets in place of those; e may be nil.
func Config(name string, e *config.Engine) fib.Config {
	cfg := fib.DefaultConfig()
	cfg.Name = name
	cfg.ReadBufferSize = ReadBufferSize
	// Zero lets fib size the watermark from the read buffer, the budget and
	// max_conns when the engine is made; a write_watermark set below wins.
	cfg.WriteBufferHighWatermark = 0
	// fit prints its own startup configuration, so fib's per-parameter list
	// would only repeat it, twice over.
	cfg.LogSettings = false
	workers := runtime.GOMAXPROCS(0) * WorkersPerCPU
	// See LoopWait.
	cfg.BlockingWait = e != nil && e.LoopWait == LoopWaitBlock
	if e == nil {
		cfg.SetPoolSizing(workers, 0)
		return cfg
	}
	if e.Workers > 0 {
		workers = e.Workers
	} else {
		workers = max(workers, e.MinWorkers)
	}
	cfg.SetPoolSizing(workers, e.TaskQueue)
	if e.MinWorkers > 0 {
		cfg.MinWorkerCount = e.MinWorkers
	}
	if e.EventLoops > 0 {
		cfg.IOPollerCount = e.EventLoops
	}
	if e.PollBatch > 0 {
		cfg.PollBatchSize = e.PollBatch
	}
	if e.ReadBufferSize > 0 {
		cfg.ReadBufferSize = e.ReadBufferSize
	}
	if e.WriteWatermark > 0 {
		cfg.WriteBufferHighWatermark = e.WriteWatermark
	}
	if e.Backlog > 0 {
		cfg.Backlog = e.Backlog
	}
	return cfg
}

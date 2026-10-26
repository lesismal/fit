// Package fibconf makes the fib.Config of fit's engines from the config's
// engine settings.
package fibconf

import (
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
	if e == nil {
		return cfg
	}
	if e.Workers > 0 || e.TaskQueue > 0 {
		cfg.SetPoolSizing(e.Workers, e.TaskQueue)
	}
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

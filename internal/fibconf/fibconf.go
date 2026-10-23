// Package fibconf makes the fib.Config of fit's engines from the config's
// engine settings.
package fibconf

import (
	"github.com/lesismal/fib"

	"github.com/lesismal/fit/config"
)

// Config returns fib's default config for an engine named name, with what e
// sets in place of fib's defaults; e may be nil.
func Config(name string, e *config.Engine) fib.Config {
	cfg := fib.DefaultConfig()
	cfg.Name = name
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

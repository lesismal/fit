package logs

import (
	"log/slog"
	"os"
	"slices"
	"sync/atomic"

	"github.com/lesismal/fit/config"
)

// std is the process's log, which Named's loggers write to.
var std atomic.Pointer[Root]

// SetDefault makes r the process's log: Named's loggers write to it, and so
// do Go's log and log/slog, as the engine logger, fib's messages among
// them. A nil r has them write to stderr again, at info and up.
func SetDefault(r *Root) {
	std.Store(r)
	if r == nil {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		return
	}
	slog.SetDefault(slog.New(r.Handler(config.LoggerEngine)))
}

// Named returns logger name, one of config.Loggers, of the process's log,
// for code that is handed no log of its own, as modules are: each message
// goes to the log SetDefault set last, or, before there is one, to stderr
// at info and up.
func Named(name string) *Logger {
	if !slices.Contains(config.Loggers, name) {
		panic("logs: no logger is named " + name)
	}
	return &Logger{name: name, dflt: true}
}

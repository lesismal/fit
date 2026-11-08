package logs

import (
	"log/slog"
	"os"
	"slices"
	"sync/atomic"

	fiblog "github.com/lesismal/fib/log"

	"github.com/lesismal/fit/config"
)

// std is the process's log, which Named's loggers write to.
var std atomic.Pointer[Root]

// SetDefault makes r the process's log: Named's loggers write to it, and so
// do fib's package log and Go's log and log/slog, as the engine logger. A
// nil r has them write to stderr again, at info and up.
func SetDefault(r *Root) {
	std.Store(r)
	if r == nil {
		fiblog.SetLogger(nil)
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
		return
	}
	engine := slog.New(r.Handler(config.LoggerEngine))
	fiblog.SetLogger(engine)
	slog.SetDefault(engine)
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

// shared is the set of files DefaultFiles returns before there is a log.
var shared = NewFiles()

// DefaultFiles returns the process's log files, those of the log SetDefault
// set last, so that a module writing a file of its own, as sync's file sink
// does, shares their rules: one sink per path, one rotate per file, and the
// workbench's list. Before there is a log, as in tests, it is a set of its
// own.
func DefaultFiles() *Files {
	if r := std.Load(); r != nil && r.files != nil {
		return r.files
	}
	return shared
}

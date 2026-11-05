// Package logs is fit's own log: what fit says about itself, as opposed to
// the access log's line for each request (package observe).
//
// Messages come from loggers, named as config.Loggers lists: server, tls,
// provider and the rest, and engine, which takes what fib and any other
// library write to Go's log and log/slog. Each logger has a least level,
// debug, info, warn or error, which a reload may change, and a format and an
// output of its own or the log's. The formats are fib's (package
// github.com/lesismal/fib/log): line, text and json, written by fib's
// handlers, so that fit's lines and fib's look alike, each carrying the
// logger's name as logger. A message below its logger's level is not
// formatted at all.
//
// A message is written at once, by the goroutine that logs it: fit logs
// about starting, reloading, certificates and providers, not about each
// request, so there are few, and none on a worker's hot path.
package logs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	fiblog "github.com/lesismal/fib/log"

	"github.com/lesismal/fit/config"
)

// The levels of messages, those of log/slog.
const (
	LevelDebug = slog.LevelDebug
	LevelInfo  = slog.LevelInfo
	LevelWarn  = slog.LevelWarn
	LevelError = slog.LevelError
)

// ParseLevel returns the level a config names: debug, info, warn or error,
// and info for anything else.
func ParseLevel(s string) slog.Level {
	switch s {
	case config.LevelDebug:
		return LevelDebug
	case config.LevelWarn:
		return LevelWarn
	case config.LevelError:
		return LevelError
	}
	return LevelInfo
}

// ParseFormat returns the format of fib's package log a config names: line,
// text or json, and line for anything else.
func ParseFormat(s string) fiblog.Format {
	switch s {
	case config.FormatText:
		return fiblog.FormatText
	case config.FormatJSON:
		return fiblog.FormatJSON
	}
	return fiblog.FormatLine
}

// Root is a log, where its loggers' messages go. It is safe for concurrent
// use.
type Root struct {
	stdout, stderr *output
	// files is the set of file sinks the log writes through, shared with
	// the access log; sinks are its own, by the output path they write.
	files    *Files
	sinks    map[string]*File
	levels   []slog.LevelVar // each logger's least level, by its index in config.Loggers
	handlers []slog.Handler  // each logger's, which writes its messages
	loggers  []Logger
	zone     atomic.Pointer[zone]
	closed   atomic.Bool
}

// zone is the time zone the log writes its lines and turns its files in.
type zone struct {
	loc  *time.Location
	name string
}

func zoneOf(c *config.Log) (*zone, error) {
	loc, err := c.TimeZoneLocation()
	if err != nil {
		return nil, err
	}
	name := config.TimeZoneLocal
	if c != nil {
		name = c.TimeZoneName()
	}
	return &zone{loc: loc, name: name}, nil
}

// output is a writer the loggers writing to it share, one message a Write
// under its lock, so that their lines never interleave.
type output struct {
	mu sync.Mutex
	w  io.Writer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.w.Write(p)
}

// sinkWriter is where a file logger's lines go: the file sink until the log
// is closed, and stderr after, as the log did before the sink.
type sinkWriter struct {
	root *Root
	file *File
}

func (w *sinkWriter) Write(p []byte) (int, error) {
	if w.root.closed.Load() {
		return w.root.stderr.Write(p)
	}
	return w.file.Write(p)
}

// zoneHandler writes a record's time in the log's time zone, so that every
// format of every logger writes the zone the log's time_zone names.
type zoneHandler struct {
	inner slog.Handler
	loc   func() *time.Location
}

func (h zoneHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h zoneHandler) Handle(ctx context.Context, r slog.Record) error {
	if loc := h.loc(); loc != nil {
		r.Time = r.Time.In(loc)
	}
	return h.inner.Handle(ctx, r)
}

func (h zoneHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return zoneHandler{inner: h.inner.WithAttrs(attrs), loc: h.loc}
}

func (h zoneHandler) WithGroup(name string) slog.Handler {
	return zoneHandler{inner: h.inner.WithGroup(name), loc: h.loc}
}

// New opens the log c describes, nil for the default: info and up, debug
// and info messages to stdout and the rest to stderr, as lines.
func New(c *config.Log, stdout, stderr io.Writer) (*Root, error) {
	z, err := zoneOf(c)
	if err != nil {
		return nil, err
	}
	r := &Root{stdout: &output{w: stdout}, stderr: &output{w: stderr}, files: NewFiles(), sinks: map[string]*File{},
		levels: make([]slog.LevelVar, len(config.Loggers)), handlers: make([]slog.Handler, len(config.Loggers)),
		loggers: make([]Logger, len(config.Loggers))}
	r.zone.Store(z)
	r.SetLevels(c)
	for i, name := range config.Loggers {
		r.loggers[i] = Logger{r: r, name: name, i: i}
		h, err := r.handler(c.OutputOf(name), ParseFormat(c.FormatOf(name)), &r.levels[i], c.RotateOf(name))
		if err != nil {
			_ = r.Close()
			return nil, err
		}
		r.handlers[i] = h.WithAttrs([]slog.Attr{slog.String("logger", name)})
	}
	return r, nil
}

// handler returns a handler writing to out, a config's output, in format at
// level, with the times in the log's zone.
func (r *Root) handler(out string, format fiblog.Format, level slog.Leveler, rot *config.Rotate) (slog.Handler, error) {
	newHandler := func(w io.Writer) slog.Handler {
		return fiblog.NewHandler(fiblog.Config{Format: format, Level: level, Output: w})
	}
	var h slog.Handler
	switch out {
	case "":
		h = splitHandler{low: newHandler(r.stdout), high: newHandler(r.stderr)}
	case "stdout":
		h = newHandler(r.stdout)
	case "stderr":
		h = newHandler(r.stderr)
	default:
		f, err := r.sink(out, rot)
		if err != nil {
			return nil, err
		}
		h = newHandler(&sinkWriter{root: r, file: f})
	}
	return zoneHandler{inner: h, loc: r.currentZone}, nil
}

// sink returns the sink of a file output, opening it the first time a
// logger writes there.
func (r *Root) sink(path string, rot *config.Rotate) (*File, error) {
	if f := r.sinks[path]; f != nil {
		return f, nil
	}
	f, err := r.files.Open(path, rot, r.currentZone)
	if err != nil {
		return nil, err
	}
	r.sinks[path] = f
	return f, nil
}

// currentZone returns the zone the log writes its times in.
func (r *Root) currentZone() *time.Location {
	if z := r.zone.Load(); z != nil {
		return z.loc
	}
	return time.Local
}

// splitHandler writes the records below warn with low and the rest with
// high, as the default output splits them between stdout and stderr.
type splitHandler struct{ low, high slog.Handler }

func (h splitHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.low.Enabled(ctx, level)
}

func (h splitHandler) Handle(ctx context.Context, r slog.Record) error {
	if r.Level >= LevelWarn {
		return h.high.Handle(ctx, r)
	}
	return h.low.Handle(ctx, r)
}

func (h splitHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return splitHandler{low: h.low.WithAttrs(attrs), high: h.high.WithAttrs(attrs)}
}

func (h splitHandler) WithGroup(name string) slog.Handler {
	return splitHandler{low: h.low.WithGroup(name), high: h.high.WithGroup(name)}
}

// SetLevels sets each logger's least level as c says, for a reload.
func (r *Root) SetLevels(c *config.Log) {
	for i, name := range config.Loggers {
		r.levels[i].Set(ParseLevel(c.LevelOf(name)))
	}
}

// SetConfig has the log write its lines and turn its files as c says from
// now on: the levels, the time zone, and the rotation and retention
// settings, all of which a reload changes in place. A file's path or a
// logger's format still takes a restart, so they are not taken up here.
func (r *Root) SetConfig(c *config.Log) error {
	z, err := zoneOf(c)
	if err != nil {
		return err
	}
	r.SetLevels(c)
	r.zone.Store(z)
	rots := map[string]*config.Rotate{}
	for _, out := range c.FileOutputs() {
		rots[out.Path] = out.Rotate
	}
	for path, f := range r.sinks {
		f.SetRotate(rots[path])
	}
	return nil
}

// Files returns the set of file sinks the log writes through, for the
// access log, which shares them, and for the workbench.
func (r *Root) Files() *Files { return r.files }

// TimeZoneName returns the name of the zone the log writes its times in.
func (r *Root) TimeZoneName() string {
	if z := r.zone.Load(); z != nil {
		return z.name
	}
	return config.TimeZoneLocal
}

// Logger returns the logger named name, one of config.Loggers. It panics
// for any other name, a mistake in fit's code.
func (r *Root) Logger(name string) *Logger {
	i := slices.Index(config.Loggers, name)
	if i < 0 {
		panic("logs: no logger is named " + name)
	}
	return &r.loggers[i]
}

// Close closes the files the log opened. Messages logged after Close go to
// stderr.
func (r *Root) Close() error {
	r.closed.Store(true)
	var first error
	for path, f := range r.sinks {
		if err := r.files.Release(f); err != nil && first == nil {
			first = err
		}
		delete(r.sinks, path)
	}
	return first
}

// write writes msg, a message of logger i's at level.
func (r *Root) write(i int, level slog.Level, msg string) {
	_ = r.handlers[i].Handle(context.Background(), slog.NewRecord(time.Now(), level, msg, 0))
}

// Logger is one of a log's loggers. A nil Logger writes nothing.
type Logger struct {
	r    *Root
	name string
	i    int
	// f, when set, takes every message instead of a root: see Func.
	f func(format string, args ...any)
	// dflt says the logger is Named's, of whichever log SetDefault set.
	dflt bool
	// prefix goes before each message's format: see Prefixed.
	prefix string
}

// Func returns a logger that hands every message, whatever its level, to f,
// as a test or an embedder may want.
func Func(f func(format string, args ...any)) *Logger { return &Logger{f: f} }

// Prefixed returns a logger that writes what l does, each message after
// prefix.
func (l *Logger) Prefixed(prefix string) *Logger {
	if l == nil {
		return nil
	}
	l2 := *l
	l2.prefix += strings.ReplaceAll(prefix, "%", "%%")
	return &l2
}

// Name returns the logger's name.
func (l *Logger) Name() string { return l.name }

// Enabled reports whether a message at level would be written.
func (l *Logger) Enabled(level slog.Level) bool {
	switch {
	case l == nil:
		return false
	case l.f != nil:
		return true
	case l.dflt:
		if r := std.Load(); r != nil {
			return r.Logger(l.name).Enabled(level)
		}
		return level >= LevelInfo
	}
	return level >= l.r.levels[l.i].Level()
}

// Logf writes a message at level, formatted as fmt.Sprintf does, if the
// logger's level lets it.
func (l *Logger) Logf(level slog.Level, format string, args ...any) {
	if !l.Enabled(level) {
		return
	}
	format = l.prefix + format
	switch {
	case l.f != nil:
		l.f(format, args...)
	case l.dflt:
		if r := std.Load(); r != nil {
			r.loggers[slices.Index(config.Loggers, l.name)].Logf(level, format, args...)
		} else {
			fmt.Fprintf(os.Stderr, "fit: "+format+"\n", args...)
		}
	default:
		l.r.write(l.i, level, fmt.Sprintf(format, args...))
	}
}

// Msg writes msg at level, as it is, for lines printed whole, as fit's
// commands print theirs.
func (l *Logger) Msg(level slog.Level, msg string) {
	if l != nil && l.prefix != "" {
		msg = fmt.Sprintf(l.prefix+"%s", msg)
	}
	switch {
	case !l.Enabled(level):
	case l.f != nil:
		l.f("%s", msg)
	case l.dflt:
		if r := std.Load(); r != nil {
			r.loggers[slices.Index(config.Loggers, l.name)].Msg(level, msg)
		} else {
			fmt.Fprintln(os.Stderr, "fit: "+msg)
		}
	default:
		l.r.write(l.i, level, msg)
	}
}

// Debugf, Infof, Warnf and Errorf write a message at their level.
func (l *Logger) Debugf(format string, args ...any) { l.Logf(LevelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.Logf(LevelInfo, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.Logf(LevelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.Logf(LevelError, format, args...) }

// Handler returns the log/slog handler logger name's messages are written
// with, as the engine's are.
func (r *Root) Handler(name string) slog.Handler {
	return r.handlers[slices.Index(config.Loggers, name)]
}

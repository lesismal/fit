// Package logs is fit's own log: what fit says about itself, as opposed to
// the access log's line for each request (package observe).
//
// Messages come from loggers, named as config.Loggers lists: server, tls,
// provider and the rest, and engine, which takes what fib and any other
// library write to Go's log and log/slog. Each logger has a least level,
// debug, info, warn or error, which a reload may change; a message below
// its logger's level is not formatted at all.
//
// A message is written at once, under a lock, by the goroutine that logs
// it: fit logs about starting, reloading, certificates and providers, not
// about each request, so there are few, and none on a worker's hot path.
package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

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

// levelName is how a level is written: debug, info, warn or error.
func levelName(l slog.Level) string {
	switch {
	case l < LevelInfo:
		return config.LevelDebug
	case l < LevelWarn:
		return config.LevelInfo
	case l < LevelError:
		return config.LevelWarn
	}
	return config.LevelError
}

// Root is a log, where its loggers' messages go. It is safe for concurrent
// use.
type Root struct {
	mu sync.Mutex
	// out takes every message, or, when it is nil, stdout those below warn
	// and stderr the rest.
	out            io.Writer
	stdout, stderr io.Writer
	file           *os.File // the file out is, when the root owns it
	json           bool
	levels         []atomic.Int64 // each logger's least level, by its index in config.Loggers
	loggers        []Logger
}

// New opens the log c describes, nil for the default: info and up, debug
// and info messages to stdout and the rest to stderr, as text.
func New(c *config.Log, stdout, stderr io.Writer) (*Root, error) {
	r := &Root{stdout: stdout, stderr: stderr, levels: make([]atomic.Int64, len(config.Loggers)),
		loggers: make([]Logger, len(config.Loggers))}
	for i, name := range config.Loggers {
		r.loggers[i] = Logger{r: r, name: name, i: i}
	}
	if c != nil {
		r.json = c.Format == "json"
		switch c.Output {
		case "":
		case "stdout":
			r.out = stdout
		case "stderr":
			r.out = stderr
		default:
			f, err := os.OpenFile(c.Output, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
			if err != nil {
				return nil, err
			}
			r.out, r.file = f, f
		}
	}
	r.SetLevels(c)
	return r, nil
}

// SetLevels sets each logger's least level as c says, for a reload.
func (r *Root) SetLevels(c *config.Log) {
	for i, name := range config.Loggers {
		r.levels[i].Store(int64(ParseLevel(c.LevelOf(name))))
	}
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

// Close closes the file the log opened, if it did. Messages logged after
// Close go to stderr.
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.out, r.file = r.stderr, nil
	return err
}

// write writes a message of logger i's at level, with attrs, the engine's.
// text, when not empty, is the message's line as text, as it is.
func (r *Root) write(i int, level slog.Level, t time.Time, msg string, attrs []slog.Attr, text string) {
	var stack [256]byte
	var b []byte
	if r.json {
		b = append(stack[:0], `{"time":"`...)
		b = t.UTC().AppendFormat(b, "2006-01-02T15:04:05.000Z07:00")
		b = append(b, `","level":"`...)
		b = append(b, levelName(level)...)
		b = append(b, `","logger":"`...)
		b = append(b, config.Loggers[i]...)
		b = append(b, `","msg":`...)
		b = appendString(b, msg)
		for _, a := range attrs {
			b = append(b, ',')
			b = appendString(b, a.Key)
			b = append(b, ':')
			b = appendJSON(b, a.Value)
		}
		b = append(b, "}\n"...)
	} else if text != "" {
		b = append(append(stack[:0], text...), '\n')
	} else {
		b = append(stack[:0], "fit: "...)
		b = append(b, msg...)
		for _, a := range attrs {
			b = append(b, ' ')
			b = append(b, a.Key...)
			b = append(b, '=')
			b = appendText(b, a.Value)
		}
		b = append(b, '\n')
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.out
	if w == nil {
		w = r.stdout
		if level >= LevelWarn {
			w = r.stderr
		}
	}
	_, _ = w.Write(b)
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
	return int64(level) >= l.r.levels[l.i].Load()
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
		l.r.write(l.i, level, time.Now(), fmt.Sprintf(format, args...), nil, "")
	}
}

// Line writes a message at level that is line as text, as it is, and msg
// as JSON, for lines printed whole, as fit's commands print theirs.
func (l *Logger) Line(level slog.Level, line, msg string) {
	switch {
	case !l.Enabled(level):
	case l.f != nil:
		l.f(l.prefix+"%s", msg)
	case l.dflt:
		if r := std.Load(); r != nil {
			r.loggers[slices.Index(config.Loggers, l.name)].Line(level, line, msg)
		} else {
			fmt.Fprintln(os.Stderr, line)
		}
	default:
		l.r.write(l.i, level, time.Now(), msg, nil, line)
	}
}

// Debugf, Infof, Warnf and Errorf write a message at their level.
func (l *Logger) Debugf(format string, args ...any) { l.Logf(LevelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.Logf(LevelInfo, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.Logf(LevelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.Logf(LevelError, format, args...) }

// Handler returns a log/slog handler whose records are logger name's
// messages, as the engine's are.
func (r *Root) Handler(name string) slog.Handler { return &handler{l: r.Logger(name)} }

// handler is a log/slog handler writing to a logger: Go's log and log/slog,
// through slog.SetDefault, and so fib's messages.
type handler struct {
	l      *Logger
	attrs  []slog.Attr
	prefix string // the groups opened, each followed by a dot
}

func (h *handler) Enabled(_ context.Context, level slog.Level) bool { return h.l.Enabled(level) }

func (h *handler) Handle(_ context.Context, rec slog.Record) error {
	attrs := slices.Clip(h.attrs)
	rec.Attrs(func(a slog.Attr) bool {
		attrs = flatten(attrs, h.prefix, a)
		return true
	})
	t := rec.Time
	if t.IsZero() {
		t = time.Now()
	}
	h.l.r.write(h.l.i, rec.Level, t, rec.Message, attrs, "")
	return nil
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	h2 := *h
	h2.attrs = slices.Clip(h.attrs)
	for _, a := range as {
		h2.attrs = flatten(h2.attrs, h.prefix, a)
	}
	return &h2
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h2 := *h
	h2.prefix += name + "."
	return &h2
}

// flatten appends a to attrs, its key after prefix, and a group's members
// each in turn, their keys after the group's.
func flatten(attrs []slog.Attr, prefix string, a slog.Attr) []slog.Attr {
	a.Value = a.Value.Resolve()
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, m := range a.Value.Group() {
			attrs = flatten(attrs, p, m)
		}
		return attrs
	}
	if a.Equal(slog.Attr{}) {
		return attrs
	}
	a.Key = prefix + a.Key
	return append(attrs, a)
}

// appendText appends v as text: as it is, or quoted when it has a space, a
// quote or a control character, or is empty.
func appendText(b []byte, v slog.Value) []byte {
	var s string
	switch v.Kind() {
	case slog.KindTime:
		s = v.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok {
			s = err.Error()
		} else {
			s = fmt.Sprint(v.Any())
		}
	default:
		s = v.String()
	}
	for _, c := range []byte(s) {
		if c <= ' ' || c == '"' || c == '=' || c >= utf8.RuneSelf {
			return strconv.AppendQuote(b, s)
		}
	}
	if s == "" {
		return append(b, `""`...)
	}
	return append(b, s...)
}

// appendJSON appends v as a JSON value.
func appendJSON(b []byte, v slog.Value) []byte {
	switch v.Kind() {
	case slog.KindString:
		return appendString(b, v.String())
	case slog.KindInt64:
		return strconv.AppendInt(b, v.Int64(), 10)
	case slog.KindUint64:
		return strconv.AppendUint(b, v.Uint64(), 10)
	case slog.KindBool:
		return strconv.AppendBool(b, v.Bool())
	case slog.KindDuration:
		return appendString(b, v.Duration().String())
	case slog.KindTime:
		return appendString(b, v.Time().UTC().Format(time.RFC3339Nano))
	case slog.KindFloat64:
		if j, err := json.Marshal(v.Float64()); err == nil {
			return append(b, j...)
		}
		return appendString(b, v.String())
	}
	x := v.Any()
	if err, ok := x.(error); ok {
		return appendString(b, err.Error())
	}
	if j, err := json.Marshal(x); err == nil {
		return append(b, j...)
	}
	return appendString(b, fmt.Sprint(x))
}

// appendString appends s as a JSON string; bytes that are not valid UTF-8
// become U+FFFD.
func appendString(b []byte, s string) []byte {
	j, _ := json.Marshal(s)
	return append(b, j...)
}

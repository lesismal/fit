package config

import (
	"slices"
	"strings"
)

// Log is fit's own log: what fit says about itself, as opposed to the
// access log's line for each request. Each message comes from a logger,
// one of Loggers, and is written when its level is at least the logger's,
// in the format and to the output of that logger, or of the log.
type Log struct {
	Level   string             `json:"level,omitempty" enum:"debug,info,warn,error" default:"\"info\"" doc:"The least level written, of every logger levels and loggers do not set one for: debug, info, warn or error. info when left out. A change takes effect on reload."`
	Levels  map[string]string  `json:"levels,omitempty" doc:"The least level of some loggers, by name, over level: server (listeners, loading and reloading configs, shutting down), admin (the Admin API), tls (certificates: ACME, OCSP, certificate files), provider (providers), telemetry (exporting over OTLP), access_log (the access log's dropped lines), plugin (wasm plugins), fastcgi (what FastCGI applications write to their stderr) and engine (fib, the network engine, and whatever else writes to Go's log and log/slog). The same as loggers' level, which a logger may not have both of. A change takes effect on reload."`
	Output  string             `json:"output,omitempty" doc:"\"stdout\", \"stderr\", or the path of a file messages are appended to, of every logger loggers does not set one for. When left out, debug and info messages go to stdout and warn and error ones to stderr. A change takes a restart."`
	Format  string             `json:"format,omitempty" enum:"line,text,json" default:"\"line\"" doc:"How messages are written, those of every logger loggers does not set one for, as fib writes its own: line, the time, level and message, then the attributes as key=value; text, every part as key=value (logfmt); or json, a JSON object a line. Each carries the logger's name as logger. line when left out. A change takes a restart."`
	Loggers map[string]*Logger `json:"loggers,omitempty" doc:"Settings of some loggers of their own, by name (those levels lists), over level, output and format: engine's to a file of its own, say, or access_log's as json."`

	At Value `json:"-" fit:"self"`
}

// Logger is the settings of one of a log's loggers; what it leaves out is
// the log's.
type Logger struct {
	Level  string `json:"level,omitempty" enum:"debug,info,warn,error" doc:"The least level the logger writes. A change takes effect on reload."`
	Output string `json:"output,omitempty" doc:"\"stdout\", \"stderr\", or the path of a file the logger's messages are appended to. A change takes a restart."`
	Format string `json:"format,omitempty" enum:"line,text,json" doc:"How the logger's messages are written: line, text or json, as the log's format. A change takes a restart."`

	At Value `json:"-" fit:"self"`
}

// The formats of fit's log, those of fib's package log, and of the access
// log.
const (
	FormatLine = "line"
	FormatText = "text"
	FormatJSON = "json"
)

// The levels of fit's log, and of the access log's requests.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Levels are the levels of fit's log, least first.
var Levels = []string{LevelDebug, LevelInfo, LevelWarn, LevelError}

// The loggers of fit's log.
const (
	LoggerServer    = "server"
	LoggerAdmin     = "admin"
	LoggerTLS       = "tls"
	LoggerProvider  = "provider"
	LoggerTelemetry = "telemetry"
	LoggerAccessLog = "access_log"
	LoggerPlugin    = "plugin"
	LoggerFastCGI   = "fastcgi"
	LoggerEngine    = "engine"
)

// Loggers are the names of fit's loggers, which Log.Levels and Log.Loggers
// may name.
var Loggers = []string{LoggerServer, LoggerAdmin, LoggerTLS, LoggerProvider, LoggerTelemetry, LoggerAccessLog,
	LoggerPlugin, LoggerFastCGI, LoggerEngine}

// LevelOf returns the least level written of logger: the one Loggers or
// Levels gives it, or Level, or info.
func (l *Log) LevelOf(logger string) string {
	if l == nil {
		return LevelInfo
	}
	if lg := l.Loggers[logger]; lg != nil && lg.Level != "" {
		return lg.Level
	}
	if lv, ok := l.Levels[logger]; ok {
		return lv
	}
	if l.Level != "" {
		return l.Level
	}
	return LevelInfo
}

// OutputOf returns where logger writes: the output Loggers gives it, or
// Output; empty for stdout below warn and stderr from it.
func (l *Log) OutputOf(logger string) string {
	if l == nil {
		return ""
	}
	if lg := l.Loggers[logger]; lg != nil && lg.Output != "" {
		return lg.Output
	}
	return l.Output
}

// FormatOf returns how logger writes: the format Loggers gives it, or
// Format, or line.
func (l *Log) FormatOf(logger string) string {
	if l == nil {
		return FormatLine
	}
	if lg := l.Loggers[logger]; lg != nil && lg.Format != "" {
		return lg.Format
	}
	if l.Format != "" {
		return l.Format
	}
	return FormatLine
}

func checkLog(l *Log, add func(Value, string, ...any)) {
	for _, name := range sortedKeys(l.Levels) {
		at := l.At.Field("levels").Field(name)
		if !slices.Contains(Loggers, name) {
			add(at, "no logger is named %q: the loggers are %s", name, strings.Join(Loggers, ", "))
			continue
		}
		if lv := l.Levels[name]; !slices.Contains(Levels, lv) {
			add(at, "%q is not a level: debug, info, warn or error", lv)
		}
	}
	for _, name := range sortedKeys(l.Loggers) {
		lg := l.Loggers[name]
		at := l.At.Field("loggers").Field(name)
		if !slices.Contains(Loggers, name) {
			add(at, "no logger is named %q: the loggers are %s", name, strings.Join(Loggers, ", "))
			continue
		}
		if _, ok := l.Levels[name]; ok && lg != nil && lg.Level != "" {
			add(at.Field("level"), "logger %s has a level in levels too: set it in one place", name)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

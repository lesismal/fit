package config

import (
	"slices"
	"strings"
)

// Log is fit's own log: what fit says about itself, as opposed to the
// access log's line for each request. Each message comes from a logger,
// one of Loggers, and is written when its level is at least the logger's.
type Log struct {
	Level  string            `json:"level,omitempty" enum:"debug,info,warn,error" default:"\"info\"" doc:"The least level written, of every logger levels does not name: debug, info, warn or error. info when left out. A change takes effect on reload."`
	Levels map[string]string `json:"levels,omitempty" doc:"The least level of some loggers, by name, over level: server (listeners, loading and reloading configs, shutting down), admin (the Admin API), tls (certificates: ACME, OCSP, certificate files), provider (providers), telemetry (exporting over OTLP), access_log (the access log's dropped lines), plugin (wasm plugins), fastcgi (what FastCGI applications write to their stderr) and engine (fib, the network engine, and whatever else writes to Go's log and log/slog). A change takes effect on reload."`
	Output string            `json:"output,omitempty" doc:"\"stdout\", \"stderr\", or the path of a file messages are appended to. When left out, debug and info messages go to stdout and warn and error ones to stderr."`
	Format string            `json:"format,omitempty" enum:"text,json" default:"\"text\"" doc:"text, a line \"fit: \" and the message, or json, a JSON object a line, with time, level, logger and msg, and the attributes of what the engine logs. text when left out."`

	At Value `json:"-" fit:"self"`
}

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

// Loggers are the names of fit's loggers, which Log.Levels may name.
var Loggers = []string{LoggerServer, LoggerAdmin, LoggerTLS, LoggerProvider, LoggerTelemetry, LoggerAccessLog,
	LoggerPlugin, LoggerFastCGI, LoggerEngine}

// LevelOf returns the least level written of logger: the one Levels gives
// it, or Level, or info.
func (l *Log) LevelOf(logger string) string {
	if l == nil {
		return LevelInfo
	}
	if lv, ok := l.Levels[logger]; ok {
		return lv
	}
	if l.Level != "" {
		return l.Level
	}
	return LevelInfo
}

func checkLog(l *Log, add func(Value, string, ...any)) {
	names := make([]string, 0, len(l.Levels))
	for name := range l.Levels {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		at := l.At.Field("levels").Field(name)
		if !slices.Contains(Loggers, name) {
			add(at, "no logger is named %q: the loggers are %s", name, strings.Join(Loggers, ", "))
			continue
		}
		if lv := l.Levels[name]; !slices.Contains(Levels, lv) {
			add(at, "%q is not a level: debug, info, warn or error", lv)
		}
	}
}

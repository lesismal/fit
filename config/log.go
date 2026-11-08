package config

import (
	"slices"
	"strings"
	"time"
)

// Log is fit's own log: what fit says about itself, as opposed to the
// access log's line for each request. Each message comes from a logger,
// one of Loggers, and is written when its level is at least the logger's,
// in the format and to the output of that logger, or of the log.
type Log struct {
	Level    string             `json:"level,omitempty" enum:"debug,info,warn,error" default:"\"info\"" doc:"The least level written, of every logger levels and loggers do not set one for: debug, info, warn or error. info when left out. A change takes effect on reload."`
	Levels   map[string]string  `json:"levels,omitempty" doc:"The least level of some loggers, by name, over level: server (listeners, loading and reloading configs, shutting down), admin (the Admin API), tls (certificates: ACME, OCSP, certificate files), provider (providers), telemetry (exporting over OTLP), access_log (the access log's dropped lines), plugin (wasm plugins), fastcgi (what FastCGI applications write to their stderr), llm and tunnel (their roles), webrtc (the TURN server), sync (its pipelines) and engine (fib, the network engine, and whatever else writes to Go's log and log/slog). The same as loggers' level, which a logger may not have both of. A change takes effect on reload."`
	Output   string             `json:"output,omitempty" doc:"\"stdout\", \"stderr\", or the path of a file messages are appended to, of every logger loggers does not set one for. When left out, debug and info messages go to stdout and warn and error ones to stderr. A change takes a restart."`
	Format   string             `json:"format,omitempty" enum:"line,text,json" default:"\"line\"" doc:"How messages are written, those of every logger loggers does not set one for, as fib writes its own: line, the time, level and message, then the attributes as key=value; text, every part as key=value (logfmt); or json, a JSON object a line. Each carries the logger's name as logger. line when left out. A change takes a restart."`
	TimeZone string             `json:"time_zone,omitempty" doc:"The zone the time of every line is written in, and the zone a file's interval is rotated at: local (the default), utc, or an IANA zone name such as Asia/Shanghai. So 24h turns at local midnight by default, and at UTC midnight with utc. The zone database is embedded, so an IANA name works in a container without zoneinfo. A change takes effect on reload."`
	Rotate   *Rotate            `json:"rotate,omitempty" doc:"Rotate the file log.output names, and remove old rotated files: the size and interval a file is rotated at, and the age, count and total size past which rotated files are removed. None when left out: the file grows without bound, as before."`
	Loggers  map[string]*Logger `json:"loggers,omitempty" doc:"Settings of some loggers of their own, by name (those levels lists), over level, output, format and rotate: engine's to a file of its own, say, or access_log's as json."`

	At Value `json:"-" fit:"self"`
}

// Logger is the settings of one of a log's loggers; what it leaves out is
// the log's.
type Logger struct {
	Level  string  `json:"level,omitempty" enum:"debug,info,warn,error" doc:"The least level the logger writes. A change takes effect on reload."`
	Output string  `json:"output,omitempty" doc:"\"stdout\", \"stderr\", or the path of a file the logger's messages are appended to. A change takes a restart."`
	Format string  `json:"format,omitempty" enum:"line,text,json" doc:"How the logger's messages are written: line, text or json, as the log's format. A change takes a restart."`
	Rotate *Rotate `json:"rotate,omitempty" doc:"Rotate this logger's file, when output names one of its own; the log's rotate is used when the logger writes to the log's output. The settings are the log's rotate's."`

	At Value `json:"-" fit:"self"`
}

// RotateOf returns how the file logger writes rotates and keeps files: the
// logger's own rotate when its output is a file of its own, or the log's.
func (l *Log) RotateOf(logger string) *Rotate {
	if l == nil {
		return nil
	}
	if lg := l.Loggers[logger]; lg != nil && fileOutput(lg.Output) && lg.Output != l.Output {
		return lg.Rotate
	}
	return l.Rotate
}

// TimeZoneLocation returns the zone every line's time and every file's
// interval uses: time_zone, or the local zone when left out.
func (l *Log) TimeZoneLocation() (*time.Location, error) {
	if l == nil {
		return time.Local, nil
	}
	return LoadTimeZone(l.TimeZone)
}

// TimeZoneName returns the name of the zone l writes its times in, for the
// workbench: local when left out.
func (l *Log) TimeZoneName() string {
	if l == nil {
		return TimeZoneLocal
	}
	return TimeZoneName(l.TimeZone)
}

// FileOutputs returns the file outputs of the log: the path of each, the
// loggers writing there, and its rotation, for the workbench and for the
// check that two logs share one file with one policy.
func (l *Log) FileOutputs() []FileOutput {
	if l == nil {
		return nil
	}
	var outs []FileOutput
	index := map[string]int{}
	add := func(name, out string, rot *Rotate) {
		if !fileOutput(out) {
			return
		}
		path := outputPath(out)
		if i, ok := index[path]; ok {
			outs[i].Loggers = append(outs[i].Loggers, name)
			return
		}
		index[path] = len(outs)
		outs = append(outs, FileOutput{Path: out, Loggers: []string{name}, Rotate: rot})
	}
	for _, name := range Loggers {
		add(name, l.OutputOf(name), l.RotateOf(name))
	}
	return outs
}

// FileOutput is one file a log writes to with the loggers writing there and
// its rotation.
type FileOutput struct {
	Path    string
	Loggers []string
	Rotate  *Rotate
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
	LoggerLLM       = "llm"
	LoggerTunnel    = "tunnel"
	LoggerWebRTC    = "webrtc"
	LoggerSync      = "sync"
)

// Loggers are the names of fit's loggers, which Log.Levels and Log.Loggers
// may name.
var Loggers = []string{LoggerServer, LoggerAdmin, LoggerTLS, LoggerProvider, LoggerTelemetry, LoggerAccessLog,
	LoggerPlugin, LoggerFastCGI, LoggerEngine, LoggerLLM, LoggerTunnel, LoggerWebRTC, LoggerSync}

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
	if msg := checkTimeZone(l.TimeZone); msg != "" {
		add(l.At.Field("time_zone"), "%s", msg)
	}
	checkRotate(l.Rotate, l.At.Field("rotate"), add)
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
	// The outputs sharing a file need one rotation policy: two loggers
	// writing to one file cannot each rotate it on their own.
	shared := map[string]*Rotate{}
	note := func(name, out string, rot *Rotate, at Value) {
		if !fileOutput(out) {
			return
		}
		path := outputPath(out)
		if prev, ok := shared[path]; ok {
			if !prev.Equal(rot) {
				add(at, "logger %s writes the log's other loggers' file %q with other rotation settings: a file has one rotate", name, out)
			}
			return
		}
		shared[path] = rot
	}
	note("", l.Output, l.Rotate, l.At.Field("output"))
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
		if lg == nil {
			continue
		}
		checkRotate(lg.Rotate, at.Field("rotate"), add)
		note(name, lg.Output, lg.Rotate, at.Field("output"))
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

// checkAccessLog reports what is wrong with the access log's time zone and
// rotation.
func checkAccessLog(a *AccessLog, add func(Value, string, ...any)) {
	if msg := checkTimeZone(a.TimeZone); msg != "" {
		add(a.At.Field("time_zone"), "%s", msg)
	}
	checkRotate(a.Rotate, a.At.Field("rotate"), add)
}

// checkLogFileSharing reports a file fit's own log and the access log both
// write to with different rotation or time zones: one file turns at one
// boundary, and has one rotate.
func checkLogFileSharing(c *Config, add func(Value, string, ...any)) {
	if c.AccessLog == nil || !fileOutput(c.AccessLog.Output) {
		return
	}
	accessZone, err := c.AccessLog.TimeZoneLocation()
	if err != nil {
		return // reported by checkAccessLog
	}
	for _, out := range c.Log.FileOutputs() {
		if outputPath(out.Path) != outputPath(c.AccessLog.Output) {
			continue
		}
		if !out.Rotate.Equal(c.AccessLog.Rotate) {
			add(c.AccessLog.At.Field("rotate"), "the access log writes the log's file %q with other rotation settings: a file has one rotate", c.AccessLog.Output)
		}
		logZone, err := c.Log.TimeZoneLocation()
		if err == nil && logZone.String() != accessZone.String() {
			add(c.AccessLog.At.Field("time_zone"), "the access log writes the log's file %q in %s, not the log's %s: a file turns at one boundary", c.AccessLog.Output, accessZone, logZone)
		}
	}
}

package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	fiblog "github.com/lesismal/fib/log"

	"github.com/lesismal/fit/config"
)

// syncBuffer is a bytes.Buffer safe for concurrent use.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func newRoot(t *testing.T, c *config.Log) (r *Root, stdout, stderr *syncBuffer) {
	t.Helper()
	stdout, stderr = &syncBuffer{}, &syncBuffer{}
	r, err := New(c, stdout, stderr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, stdout, stderr
}

// stamp is the time and level a line starts with.
const stamp = `\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.\d{3} `

// matchLines checks that got is the lines of want, each a regular
// expression after stamp.
func matchLines(t *testing.T, what, got string, want ...string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if got == "" {
		lines = nil
	}
	if len(lines) != len(want) {
		t.Errorf("%s: got %q, want %d lines", what, got, len(want))
		return
	}
	for i, w := range want {
		if !regexp.MustCompile("^" + stamp + w + "$").MatchString(lines[i]) {
			t.Errorf("%s: line %q, want %s", what, lines[i], w)
		}
	}
}

func TestDefaultSplitsByLevel(t *testing.T) {
	r, stdout, stderr := newRoot(t, nil)
	l := r.Logger(config.LoggerServer)
	l.Debugf("hidden %d", 1)
	l.Infof("listener %q on %s", "web", ":80")
	l.Warnf("a warning")
	l.Errorf("an error: %v", errors.New("boom"))
	matchLines(t, "stdout", stdout.String(), `INFO  listener "web" on :80 logger=server`)
	matchLines(t, "stderr", stderr.String(), `WARN  a warning logger=server`, `ERROR an error: boom logger=server`)
}

func TestLevelsByLogger(t *testing.T) {
	c := &config.Log{Level: "warn", Levels: map[string]string{config.LoggerTLS: "debug"}, Output: "stdout"}
	r, stdout, stderr := newRoot(t, c)
	r.Logger(config.LoggerServer).Infof("server info")
	r.Logger(config.LoggerServer).Warnf("server warn")
	r.Logger(config.LoggerTLS).Debugf("tls debug")
	matchLines(t, "stdout", stdout.String(), `WARN  server warn logger=server`, `DEBUG tls debug logger=tls`)
	if stderr.String() != "" {
		t.Errorf("stderr %q with output stdout", stderr.String())
	}

	// A reload changes the levels.
	r.SetLevels(&config.Log{Level: "error"})
	r.Logger(config.LoggerTLS).Warnf("tls warn")
	r.Logger(config.LoggerServer).Errorf("server error")
	matchLines(t, "after SetLevels", stdout.String(), `WARN  server warn logger=server`, `DEBUG tls debug logger=tls`,
		`ERROR server error logger=server`)
	if r.Logger(config.LoggerTLS).Enabled(LevelWarn) {
		t.Error("tls enabled at warn under error")
	}
}

// TestLoggersOwnSettings checks loggers' own level, format and output over
// the log's.
func TestLoggersOwnSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.log")
	c := &config.Log{Output: "stdout", Loggers: map[string]*config.Logger{
		config.LoggerEngine: {Output: path, Format: "json", Level: "debug"},
		config.LoggerTLS:    {Format: "text"},
		config.LoggerAdmin:  {Output: "stderr"},
	}}
	r, stdout, stderr := newRoot(t, c)
	r.Logger(config.LoggerServer).Infof("server")
	r.Logger(config.LoggerTLS).Infof("renewed %s", "example.com")
	r.Logger(config.LoggerAdmin).Infof("admin")
	slog.New(r.Handler(config.LoggerEngine)).Debug("fib: engine config", "engine", "fit")
	matchLines(t, "stderr", stderr.String(), `INFO  admin logger=admin`)
	lines := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(lines) != 2 || !regexp.MustCompile("^"+stamp+`INFO  server logger=server$`).MatchString(lines[0]) ||
		!regexp.MustCompile(`^time=\S+ level=INFO msg="renewed example.com" logger=tls$`).MatchString(lines[1]) {
		t.Errorf("stdout %q", stdout.String())
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("%q: %v", data, err)
	}
	if m["level"] != "DEBUG" || m["logger"] != "engine" || m["msg"] != "fib: engine config" || m["engine"] != "fit" {
		t.Errorf("engine's file %v", m)
	}
}

func TestJSON(t *testing.T) {
	r, stdout, _ := newRoot(t, &config.Log{Format: "json", Output: "stdout", Level: "debug"})
	r.Logger(config.LoggerProvider).Warnf("provider %s: \"%s\"", "k8s", "gone")
	h := slog.New(r.Handler(config.LoggerEngine)).With("engine", "listeners").WithGroup("g")
	h.Info("fib: engine started", "workers", 4, "err", errors.New("x"), slog.Group("in", "d", time.Second))
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines %q", lines)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatal(err)
	}
	if m["level"] != "WARN" || m["logger"] != "provider" || m["msg"] != `provider k8s: "gone"` {
		t.Errorf("first line %v", m)
	}
	if _, err := time.Parse(time.RFC3339, m["time"].(string)); err != nil {
		t.Errorf("time: %v", err)
	}
	m = nil
	if err := json.Unmarshal([]byte(lines[1]), &m); err != nil {
		t.Fatal(err)
	}
	g, _ := m["g"].(map[string]any)
	if m["level"] != "INFO" || m["logger"] != "engine" || m["msg"] != "fib: engine started" || m["engine"] != "listeners" ||
		g["workers"] != float64(4) || g["err"] != "x" {
		t.Errorf("second line %s", lines[1])
	}
}

func TestLineAttrs(t *testing.T) {
	r, stdout, _ := newRoot(t, &config.Log{Output: "stdout"})
	slog.New(r.Handler(config.LoggerEngine)).Info("fib: config", "parameter", "Workers", "value", "two words", "empty", "")
	matchLines(t, "stdout", stdout.String(), `INFO  fib: config logger=engine parameter=Workers value="two words" empty=""`)
	slog.New(r.Handler(config.LoggerEngine)).Debug("below info")
	if strings.Contains(stdout.String(), "below info") {
		t.Error("a debug record was written at info")
	}
}

// TestFile checks a file two loggers share, and that what is logged after
// Close goes to stderr.
func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.log")
	stderr := &syncBuffer{}
	r, err := New(&config.Log{Output: path, Loggers: map[string]*config.Logger{config.LoggerTLS: {Output: path}}}, nil, stderr)
	if err != nil {
		t.Fatal(err)
	}
	r.Logger(config.LoggerAdmin).Infof("one")
	r.Logger(config.LoggerTLS).Errorf("two")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r.Logger(config.LoggerAdmin).Infof("three")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	matchLines(t, "file", string(data), `INFO  one logger=admin`, `ERROR two logger=tls`)
	matchLines(t, "stderr", stderr.String(), `INFO  three logger=admin`)
	if _, err := New(&config.Log{Output: filepath.Join(path, "nested")}, nil, nil); err == nil {
		t.Error("a file that cannot be opened")
	}
}

// TestDefault checks that SetDefault has fib's package log, log/slog and Go's
// log write as the engine logger.
func TestDefault(t *testing.T) {
	r, stdout, stderr := newRoot(t, &config.Log{Levels: map[string]string{config.LoggerEngine: "warn"}})
	SetDefault(r)
	t.Cleanup(func() { SetDefault(nil) })
	Named(config.LoggerPlugin).Infof("plugin %s", "p.wasm")
	fiblog.Info("fib: engine started")
	fiblog.Warn("fib: from fib")
	slog.Warn("fib: a warning")
	log.Printf("a library's line")
	matchLines(t, "stdout", stdout.String(), `INFO  plugin p.wasm logger=plugin`)
	matchLines(t, "stderr", stderr.String(), `WARN  fib: from fib logger=engine`, `WARN  fib: a warning logger=engine`)
}

func TestMsg(t *testing.T) {
	r, stdout, _ := newRoot(t, &config.Log{Output: "stdout"})
	l := r.Logger(config.LoggerServer)
	l.Msg(LevelInfo, "listening on :80, 100%")
	l.Prefixed("web: ").Msg(LevelInfo, "up")
	l.Msg(LevelDebug, "hidden")
	matchLines(t, "stdout", stdout.String(), `INFO  listening on :80, 100% logger=server`, `INFO  web: up logger=server`)
	var got []string
	Func(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) }).Prefixed("p 100%: ").Msg(LevelInfo, "m")
	if len(got) != 1 || got[0] != "p 100%: m" {
		t.Errorf("Func got %q", got)
	}
}

func TestFuncAndNil(t *testing.T) {
	var got []string
	l := Func(func(format string, args ...any) { got = append(got, format) }).Prefixed("p 100%: ")
	l.Debugf("a")
	l.Errorf("b")
	if len(got) != 2 || got[0] != "p 100%%: a" {
		t.Errorf("got %q", got)
	}
	var nl *Logger
	nl.Errorf("nothing")
	nl.Msg(LevelError, "nothing")
	if nl.Enabled(LevelError) || nl.Prefixed("x") != nil {
		t.Error("a nil logger writes")
	}
}

func TestParse(t *testing.T) {
	for s, want := range map[string]slog.Level{"debug": LevelDebug, "info": LevelInfo, "warn": LevelWarn, "error": LevelError, "": LevelInfo} {
		if got := ParseLevel(s); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", s, got, want)
		}
	}
	for s, want := range map[string]fiblog.Format{"line": fiblog.FormatLine, "text": fiblog.FormatText, "json": fiblog.FormatJSON, "": fiblog.FormatLine} {
		if got := ParseFormat(s); got != want {
			t.Errorf("ParseFormat(%q) = %v, want %v", s, got, want)
		}
	}
}

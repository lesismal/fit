package logs

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestDefaultSplitsByLevel(t *testing.T) {
	r, stdout, stderr := newRoot(t, nil)
	l := r.Logger(config.LoggerServer)
	l.Debugf("hidden %d", 1)
	l.Infof("listener %q on %s", "web", ":80")
	l.Warnf("a warning")
	l.Errorf("an error: %v", errors.New("boom"))
	if got, want := stdout.String(), "fit: listener \"web\" on :80\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if got, want := stderr.String(), "fit: a warning\nfit: an error: boom\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
	}
}

func TestLevelsByLogger(t *testing.T) {
	c := &config.Log{Level: "warn", Levels: map[string]string{config.LoggerTLS: "debug"}, Output: "stdout"}
	r, stdout, stderr := newRoot(t, c)
	r.Logger(config.LoggerServer).Infof("server info")
	r.Logger(config.LoggerServer).Warnf("server warn")
	r.Logger(config.LoggerTLS).Debugf("tls debug")
	if got, want := stdout.String(), "fit: server warn\nfit: tls debug\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if stderr.String() != "" {
		t.Errorf("stderr %q with output stdout", stderr.String())
	}

	// A reload changes the levels.
	r.SetLevels(&config.Log{Level: "error"})
	r.Logger(config.LoggerTLS).Warnf("tls warn")
	r.Logger(config.LoggerServer).Errorf("server error")
	if got, want := stdout.String(), "fit: server warn\nfit: tls debug\nfit: server error\n"; got != want {
		t.Errorf("after SetLevels got %q, want %q", got, want)
	}
	if r.Logger(config.LoggerTLS).Enabled(LevelWarn) {
		t.Error("tls enabled at warn under error")
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
	if m["level"] != "warn" || m["logger"] != "provider" || m["msg"] != `provider k8s: "gone"` {
		t.Errorf("first line %v", m)
	}
	if _, err := time.Parse(time.RFC3339, m["time"].(string)); err != nil {
		t.Errorf("time: %v", err)
	}
	m = nil
	if err := json.Unmarshal([]byte(lines[1]), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"level": "info", "logger": "engine", "msg": "fib: engine started", "engine": "listeners",
		"g.workers": float64(4), "g.err": "x", "g.in.d": "1s"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s is %v, want %v; line %s", k, m[k], v, lines[1])
		}
	}
}

func TestTextAttrs(t *testing.T) {
	r, stdout, _ := newRoot(t, &config.Log{Output: "stdout"})
	slog.New(r.Handler(config.LoggerEngine)).Info("fib: config", "parameter", "Workers", "value", "two words", "empty", "")
	if got, want := stdout.String(), "fit: fib: config parameter=Workers value=\"two words\" empty=\"\"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	slog.New(r.Handler(config.LoggerEngine)).Debug("below info")
	if strings.Contains(stdout.String(), "below info") {
		t.Error("a debug record was written at info")
	}
}

func TestFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.log")
	r, err := New(&config.Log{Output: path}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Logger(config.LoggerAdmin).Infof("one")
	r.Logger(config.LoggerAdmin).Errorf("two")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "fit: one\nfit: two\n"; got != want {
		t.Errorf("file %q, want %q", got, want)
	}
	if _, err := New(&config.Log{Output: filepath.Join(path, "nested")}, nil, nil); err == nil {
		t.Error("a file that cannot be opened")
	}
}

func TestDefault(t *testing.T) {
	r, stdout, stderr := newRoot(t, &config.Log{Levels: map[string]string{config.LoggerEngine: "warn"}})
	SetDefault(r)
	t.Cleanup(func() { SetDefault(nil) })
	Named(config.LoggerPlugin).Infof("plugin %s", "p.wasm")
	slog.Info("fib: engine started")
	slog.Warn("fib: a warning")
	log.Printf("a library's line")
	if got, want := stdout.String(), "fit: plugin p.wasm\n"; got != want {
		t.Errorf("stdout %q, want %q", got, want)
	}
	if got, want := stderr.String(), "fit: fib: a warning\n"; got != want {
		t.Errorf("stderr %q, want %q", got, want)
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
	if nl.Enabled(LevelError) || nl.Prefixed("x") != nil {
		t.Error("a nil logger writes")
	}
}

func TestParseLevel(t *testing.T) {
	for s, want := range map[string]slog.Level{"debug": LevelDebug, "info": LevelInfo, "warn": LevelWarn, "error": LevelError, "": LevelInfo} {
		if got := ParseLevel(s); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", s, got, want)
		}
		if s != "" && levelName(ParseLevel(s)) != s {
			t.Errorf("levelName of %q", s)
		}
	}
}

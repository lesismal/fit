package logs

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestNoSuchLogger checks a logger's name that is not one of
// config.Loggers is a mistake that panics.
func TestNoSuchLogger(t *testing.T) {
	r, _, _ := newRoot(t, nil)
	for what, fn := range map[string]func(){
		"Named":  func() { Named("nope") },
		"Logger": func() { r.Logger("nope") },
	} {
		func() {
			defer func() {
				if p := recover(); p == nil || !strings.Contains(p.(string), "nope") {
					t.Errorf("%s: panicked with %v", what, p)
				}
			}()
			fn()
		}()
	}
	if got := r.Logger(config.LoggerTLS).Name(); got != config.LoggerTLS {
		t.Errorf("Name() = %q", got)
	}
}

// TestHandlerGroup checks the default output's handler keeps a group on
// both sides of its split.
func TestHandlerGroup(t *testing.T) {
	r, stdout, stderr := newRoot(t, nil)
	l := slog.New(r.Handler(config.LoggerEngine).WithGroup("g"))
	l.Info("low", "k", 1)
	l.Warn("high", "k", 2)
	if !strings.Contains(stdout.String(), "g.k=1") || !strings.Contains(stderr.String(), "g.k=2") {
		t.Errorf("stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

// TestNamedMsg checks Named's Msg writes to the process's log, or, before
// there is one, to stderr.
func TestNamedMsg(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = saved }()
	SetDefault(nil)
	Named(config.LoggerPlugin).Msg(LevelInfo, "before")
	r, stdout, _ := newRoot(t, nil)
	SetDefault(r)
	t.Cleanup(func() { SetDefault(nil) })
	Named(config.LoggerPlugin).Msg(LevelInfo, "after")
	if data, _ := os.ReadFile(path); string(data) != "fit: before\n" {
		t.Errorf("stderr %q", data)
	}
	matchLines(t, "stdout", stdout.String(), `INFO  after logger=plugin`)
}

// TestCloseError checks Close reports a file that does not close.
func TestCloseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.log")
	r, err := New(&config.Log{Output: path}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := r.Files().File(path)
	if f == nil {
		t.Fatalf("no sink for %s", path)
	}
	_ = f.file.Close()
	if err := r.Close(); err == nil {
		t.Error("closing a closed file reported nothing")
	}
}

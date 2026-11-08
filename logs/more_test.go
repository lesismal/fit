package logs

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestWriteSync checks a write that waits for the disk: it is in the file
// on return, after what was queued, and refused once the file is closed or
// its context ends.
func TestWriteSync(t *testing.T) {
	fs := NewFiles()
	path := filepath.Join(t.TempDir(), "out.log")
	f, err := fs.Open(path, nil, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("queued\n"))
	if err := f.WriteSync(context.Background(), [][]byte{[]byte("a\n"), []byte("b\n")}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "queued\na\nb\n" {
		t.Fatalf("%q", data)
	}
	if err := f.WriteSync(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.WriteSync(ctx, [][]byte{[]byte("c\n")}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	fs.Release(f)
	if err := f.WriteSync(context.Background(), [][]byte{[]byte("d\n")}); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if DefaultFiles() == nil {
		t.Fatal("no default files")
	}
}

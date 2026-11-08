package syncfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/logs"
	fitsync "github.com/lesismal/fit/sync"
)

// canDenyAccess reports whether a file's mode can deny this process access:
// not on Windows, and not to root.
func canDenyAccess() bool { return runtime.GOOS != "windows" && os.Geteuid() != 0 }

func source(t *testing.T, paths ...string) *Source {
	t.Helper()
	b := `{"paths": [`
	for i, p := range paths {
		if i > 0 {
			b += ", "
		}
		b += jsonString(p)
	}
	return load(t, fitsync.Sources, "file", b+`]}`).(*Source)
}

func TestSourceTest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	for _, n := range []string{"a.log", "b.log"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "d.log"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := source(t, filepath.Join(dir, "*.log"), filepath.Join(dir, "[x]*", "*", "none.log")).Test(ctx); err != nil {
		t.Fatalf("readable: %v", err)
	}
	if err := source(t, filepath.Join(dir, "missing", "*.log")).Test(ctx); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("no directory: %v", err)
	}
	if err := source(t, filepath.Join(dir, "a.log", "*.log")).Test(ctx); err == nil {
		t.Error("a file as the directory passed")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := source(t, filepath.Join(dir, "*.log")).Test(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
	// Only the first maxTestFiles are opened: the one past them is not.
	many := t.TempDir()
	for i := range maxTestFiles + 1 {
		if err := os.WriteFile(filepath.Join(many, fmt.Sprintf("%04d.log", i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !canDenyAccess() {
		return
	}
	if err := os.Chmod(filepath.Join(many, fmt.Sprintf("%04d.log", maxTestFiles)), 0); err != nil {
		t.Fatal(err)
	}
	if err := source(t, filepath.Join(many, "*.log")).Test(ctx); err != nil {
		t.Errorf("past the files opened: %v", err)
	}
	if err := os.Chmod(filepath.Join(dir, "b.log"), 0); err != nil {
		t.Fatal(err)
	}
	if err := source(t, filepath.Join(dir, "*.log")).Test(ctx); err == nil || !strings.Contains(err.Error(), "b.log") {
		t.Errorf("unreadable: %v", err)
	}
}

func TestSinkTest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "out.log")
	s := sink(t, `{"path": `+jsonString(path)+`, "rotate": {"max_bytes": 100000}}`)
	if err := s.Test(ctx); err != nil {
		t.Fatalf("no file yet: %v", err)
	}
	if names, _ := os.ReadDir(dir); len(names) != 0 {
		t.Fatalf("left behind: %v", names)
	}
	if err := s.Write(ctx, records(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Test(ctx); err != nil {
		t.Fatalf("open, rotated: %v", err)
	}
	if err := sink(t, `{"path": `+jsonString(path)+`}`).Test(ctx); err == nil || !strings.Contains(err.Error(), "another sync file sink") {
		t.Errorf("another sink's: %v", err)
	}
	logPath := filepath.Join(dir, "fit.log")
	lf, err := logs.DefaultFiles().Open(logPath, nil, func() *time.Location { return time.UTC })
	if err != nil {
		t.Fatal(err)
	}
	defer logs.DefaultFiles().Release(lf)
	if err := sink(t, `{"path": `+jsonString(logPath)+`}`).Test(ctx); err == nil || !strings.Contains(err.Error(), "fit's log") {
		t.Errorf("a log's: %v", err)
	}
	there := filepath.Join(dir, "there.log")
	if err := os.WriteFile(there, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := sink(t, `{"path": `+jsonString(there)+`}`).Test(ctx); err != nil {
		t.Errorf("a file there: %v", err)
	}
	if err := sink(t, `{"path": `+jsonString(dir)+`}`).Test(ctx); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("a directory: %v", err)
	}
	if err := sink(t, `{"path": `+jsonString(filepath.Join(dir, "no", "out.log"))+`}`).Test(ctx); err == nil || !strings.Contains(err.Error(), "takes no new file") {
		t.Errorf("no directory: %v", err)
	}
	if err := sink(t, `{"path": `+jsonString(filepath.Join(there, "out.log"))+`}`).Test(ctx); err == nil {
		t.Error("a file as the directory passed")
	}
	if !canDenyAccess() {
		return
	}
	if err := os.Chmod(there, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := sink(t, `{"path": `+jsonString(there)+`}`).Test(ctx); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("read-only: %v", err)
	}
}

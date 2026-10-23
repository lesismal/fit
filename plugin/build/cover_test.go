package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBuildNoWorkingDirectory checks that Build reports a relative path it
// cannot make absolute, the output's or fit's directory, when the working
// directory is gone.
func TestBuildNoWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the working directory cannot be removed")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "fit")
	gone := filepath.Join(dir, "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("the working directory is still known")
	}
	ctx := context.Background()
	if err := Build(ctx, Options{Output: "fit", Fit: Module{Version: "v1.0.0"}}); err == nil {
		t.Error("output: no error")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	if err := Build(ctx, Options{Output: out, Fit: Module{Dir: "fit"}, Keep: filepath.Join(dir, "main")}); err == nil {
		t.Error("fit's directory: no error")
	}
}

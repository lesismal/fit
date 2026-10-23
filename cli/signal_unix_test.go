//go:build unix

package cli

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestRunSignals runs fit run as the command line does: SIGHUP reloads the
// config, SIGTERM stops it.
func TestRunSignals(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "fit.json")
	writeFile(t, cfg, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`)
	out, errOut := newSyncBuf(), newSyncBuf()
	done := make(chan int, 1)
	go func() { done <- Main([]string{"run", "-c", cfg, "-watch-certs=false"}, out, errOut) }()
	// The listener is reported once fit hears the signals.
	out.waitFor(t, "fit: listener ", 1)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	out.waitFor(t, "fit: reloaded "+cfg+" (config version ", 1)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("%d %q %q", code, out.String(), errOut.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatalf("fit did not stop: %q %q", out.String(), errOut.String())
	}
}

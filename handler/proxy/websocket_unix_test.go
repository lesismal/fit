//go:build !windows

package proxy

import (
	"io"
	"net"
	stdhttp "net/http"
	"syscall"
	"testing"
	"time"
)

// TestWebSocketBackpressure has the upstream send far more than a client
// that does not read can take: the upstream is held back, not buffered
// for, and the client gets every byte once it reads. The client's receive
// buffer is made small before it connects, so that the kernel's buffers
// hold little of the flood.
func TestWebSocketBackpressure(t *testing.T) {
	b := newWSBackend(t)
	base := startFit(t, retryConfig(``, ``), b.addr)
	d := &net.Dialer{Timeout: 5 * time.Second, Control: func(_, _ string, rc syscall.RawConn) error {
		var serr error
		err := rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 64<<10) })
		if err != nil {
			return err
		}
		return serr
	}}
	c, br, resp := wsDialWith(t, d, base, "/flood", "")
	if resp.StatusCode != stdhttp.StatusSwitchingProtocols {
		t.Fatalf("%d", resp.StatusCode)
	}
	if _, err := io.WriteString(c, "flood"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if n := b.written.Load(); n >= floodBytes/2 {
		t.Errorf("the upstream sent %d bytes to a client that read none: nothing held it back", n)
	}
	start := time.Now()
	if n, err := io.CopyN(io.Discard, br, floodBytes); err != nil || n != floodBytes {
		t.Fatalf("read %d %v", n, err)
	}
	t.Logf("held at %d bytes; the rest read in %v", b.written.Load(), time.Since(start))
}

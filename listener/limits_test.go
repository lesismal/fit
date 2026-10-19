package listener

import (
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/lesismal/fit/handler/static"
)

func TestHeaderLimit(t *testing.T) {
	_, s := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "limits": {"max_header_bytes": 4096},
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`)
	addr := s.Addrs()[0].Addr.String()
	for _, tt := range []struct {
		size   int
		status int
	}{{1000, 200}, {8000, 431}} {
		req, _ := stdhttp.NewRequest("GET", "http://"+addr+"/", nil)
		req.Header.Set("X-Big", strings.Repeat("a", tt.size))
		resp, err := client(false).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != tt.status {
			t.Errorf("a %d-byte field: %d, want %d", tt.size, resp.StatusCode, tt.status)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	// max_body_bytes bounds a body read whole and, past the stream threshold,
	// one that streams: both are refused with 413 when they declare a length
	// past it.
	_, s := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "limits": {"max_body_bytes": 100000},
  "routes": [{"handler": {"module": "respond", "params": {"body": "ok"}}}]}`)
	addr := s.Addrs()[0].Addr.String()
	for _, h2c := range []bool{false, true} {
		for _, tt := range []struct {
			size   int
			status int
		}{{1000, 200}, {90000, 200}, {150000, 413}} {
			resp, err := client(h2c).Post("http://"+addr+"/", "text/plain", strings.NewReader(strings.Repeat("b", tt.size)))
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.status {
				t.Errorf("h2c=%v, a %d-byte body: %d, want %d", h2c, tt.size, resp.StatusCode, tt.status)
			}
		}
	}
}

// TestPendingBytesBudget reaches max_pending_bytes: a client that asks for
// more than the budget and reads none of it leaves that much queued, and fib
// pauses reads for the budget; once the client reads, the budget frees and
// another request is served.
func TestPendingBytesBudget(t *testing.T) {
	const budget = 1 << 20
	root := t.TempDir()
	// Cached, so it goes out whole, queued on the connection rather than sent
	// as the client reads; and far more than the kernel's socket buffers
	// take, so that most of it waits in fib.
	if err := os.WriteFile(filepath.Join(root, "big.bin"), make([]byte, 16<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "small.txt"), []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, s := start(t, fmt.Sprintf(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "limits": {"max_pending_bytes": %d},
  "routes": [{"handler": {"module": "static", "params": {"root": %q, "max_file_bytes": 33554432}}}]}`, budget, root))
	addr := s.Addrs()[0].Addr.String()

	hog, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer hog.Close()
	// Two requests: the second is read in a round after the first's reply
	// has spent the budget, which pauses that round's connection.
	_, _ = io.WriteString(hog, "GET /big.bin HTTP/1.1\r\nHost: x\r\n\r\n")
	time.Sleep(200 * time.Millisecond)
	_, _ = io.WriteString(hog, "GET /small.txt HTTP/1.1\r\nHost: x\r\n\r\n")

	deadline := time.Now().Add(5 * time.Second)
	for {
		st := s.Stats()
		if st.PendingBytes >= budget && st.ReadsPausedByBudget > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the budget was not reached: %d bytes pending, %d pauses by budget", st.PendingBytes, st.ReadsPausedByBudget)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The hog reads what it was sent: the budget frees, and both its
	// replies and another client's request go through.
	done := make(chan int64, 1)
	go func() {
		n, _ := io.Copy(io.Discard, hog)
		done <- n
	}()
	c := client(false)
	c.Timeout = 10 * time.Second
	resp, err := c.Get("http://" + addr + "/small.txt")
	if err != nil {
		t.Fatalf("another request once the budget freed: %v", err)
	}
	resp.Body.Close()
	hog.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n := <-done; n < 16<<20 {
		t.Fatalf("the hog got %d bytes", n)
	}
}

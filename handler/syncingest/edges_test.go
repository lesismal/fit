package syncingest

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	fitsync "github.com/lesismal/fit/sync"
)

// TestBodyEdges checks a chunked body past max_body_bytes is answered 413,
// a body cut off by its client writes nothing, and a pipeline that stops
// while a body arrives answers it 503.
func TestBodyEdges(t *testing.T) {
	const name = "ingest-body"
	store := fitsync.MemoryStore(name)
	store.Reset()
	defer store.Reset()
	f := start(t, conf(t.TempDir(), name, `{}`, `{"pipeline": "ingest-body", "max_body_bytes": 4194304}`))
	defer f.stop()
	waitRunning(t, name)

	// Chunked, so no Content-Length tells the size first.
	req, _ := http.NewRequest(http.MethodPost, f.base+"/ingest", io.MultiReader(strings.NewReader(`{"v": "`),
		strings.NewReader(strings.Repeat("x", 5<<20)), strings.NewReader(`"}`)))
	req.SetBasicAuth("fit", "pw")
	if resp, err := client.Do(req); err != nil {
		t.Logf("chunked: %v", err) // the server may close before the client is done sending
	} else {
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Errorf("chunked: %d", resp.StatusCode)
		}
	}

	addr := strings.TrimPrefix(f.base, "http://")
	head := "POST /ingest HTTP/1.1\r\nHost: x\r\nAuthorization: Basic Zml0OnB3\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n"
	body := `{"id": 1}` + strings.Repeat(" ", 3<<20)

	// Cut off: half a body, then the connection closes.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, head, len(body))
	_, _ = conn.Write([]byte(body[:1<<20]))
	_ = conn.Close()

	// A body still arriving when the pipeline stops.
	conn, err = net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, head, len(body))
	_, _ = conn.Write([]byte(body[:1<<20]))
	time.Sleep(50 * time.Millisecond) // lets the handler start; either way the answer is 503
	f.sync.Close(context.Background())
	_, _ = conn.Write([]byte(body[1<<20:]))
	_ = conn.SetReadDeadline(time.Now().Add(hang))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(b), "not running") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
	if n := len(store.Records()); n != 0 {
		t.Errorf("%d records written", n)
	}
}

// TestProvisionRefuses checks a handler naming no pipeline is refused.
func TestProvisionRefuses(t *testing.T) {
	c, err := config.Load([]byte(conf(t.TempDir(), "ingest-none", `{}`, `{"pipeline": ""}`)))
	if err == nil {
		_, err = runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics()})
	}
	if err == nil || !strings.Contains(err.Error(), "pipeline") {
		t.Errorf("%v", err)
	}
}

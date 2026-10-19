package proxy

import (
	"bufio"
	"fmt"
	stdhttp "net/http"
	"testing"
	"time"
)

// TestSSELatency: a response of no declared length that trickles, as
// server-sent events do, reaches the client as it arrives, not once 64KB of
// it have.
func TestSSELatency(t *testing.T) {
	eachProto(t, func(t *testing.T, proto string) {
		backend := serveBackend(t, proto, stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for i := range 3 {
				fmt.Fprintf(w, "data: %d\n\n", i)
				w.(stdhttp.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(200 * time.Millisecond):
				}
			}
		}))
		base := startFit(t, config1(`{"upstream": "app"}`), backend)
		began := time.Now()
		resp, err := client(false).Get(base + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		took := time.Since(began)
		if err != nil || line != "data: 0\n" {
			t.Fatalf("first line %q, %v", line, err)
		}
		if took > 100*time.Millisecond {
			t.Fatalf("the first event took %v to arrive", took)
		}
	})
}

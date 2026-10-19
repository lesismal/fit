//go:build !windows

package e2e

import (
	"io"
	stdhttp "net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// waitLine waits for a line of fit's output that starts with prefix.
func (f *fit) waitLine(t *testing.T, prefix string) string {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-f.lines:
			if !ok {
				t.Fatalf("fit exited waiting for %q", prefix)
			}
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-timeout:
			t.Fatalf("no line %q from fit", prefix)
		}
	}
}

// reload writes doc over fit's config and sends it SIGHUP.
func (f *fit) reload(t *testing.T, doc string) {
	t.Helper()
	if err := os.WriteFile(f.config, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
}

func reloadConfig(body string, extra string) string {
	return `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}` + extra + `],
  "routes": [{"handler": {"module": "respond", "params": {"body": "` + body + `"}}}]
}`
}

func TestReloadUnderLoad(t *testing.T) {
	f, addrs := startFit(t, reloadConfig("v1", ""))
	base := "http://" + addrs["web"] + "/"

	// Load: keep-alive and fresh connections, over HTTP/1.1 and h2c, until
	// told to stop. Every request must succeed with v1 or v2.
	var (
		stop             atomic.Bool
		ok, failed, seen atomic.Int64
		v2Only           atomic.Bool // set once v2 is current everywhere
		mu               sync.Mutex
		failures         []string
		wg               sync.WaitGroup
	)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := client(i%2 == 1)
			fresh := i%4 >= 2
			if fresh {
				c.Transport.(*stdhttp.Transport).DisableKeepAlives = true
			}
			for !stop.Load() {
				if fresh {
					// A new connection each time, paced so that the closed
					// ones, waiting out TIME_WAIT, do not use up the ports.
					time.Sleep(5 * time.Millisecond)
				}
				checkV2 := v2Only.Load()
				resp, err := c.Get(base)
				var body []byte
				if err == nil {
					body, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				switch {
				case err != nil, resp.StatusCode != 200, string(body) != "v1" && string(body) != "v2",
					checkV2 && string(body) != "v2":
					failed.Add(1)
					mu.Lock()
					failures = append(failures, fmt0(err, resp, body))
					mu.Unlock()
				default:
					ok.Add(1)
					if string(body) == "v2" {
						seen.Add(1)
					}
				}
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)

	f.reload(t, reloadConfig("v2", `, {"name": "extra", "address": "127.0.0.1:0"}`))
	f.waitLine(t, "fit: reloading")
	extra := f.waitLine(t, `fit: listener "extra" on `)
	f.waitLine(t, "fit: reloaded")
	v2Only.Store(true)
	time.Sleep(200 * time.Millisecond)

	extraAddr := strings.Fields(strings.TrimPrefix(extra, `fit: listener "extra" on `))[0]
	if resp, err := client(false).Get("http://" + extraAddr + "/"); err != nil || resp.StatusCode != 200 {
		t.Errorf("the added listener: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	// A broken config: the running one stays.
	f.reload(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "nope"}}]}`)
	f.waitLine(t, "fit: reloading")
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(f.stderr.String(), "reload failed") {
		if time.Now().After(deadline) {
			t.Fatalf("no reload error on stderr: %q", f.stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(f.stderr.String(), `there is no module "nope"`) {
		t.Errorf("stderr: %q", f.stderr.String())
	}
	time.Sleep(200 * time.Millisecond)

	stop.Store(true)
	wg.Wait()
	t.Logf("%d requests, %d of them answered v2, %d failed", ok.Load()+failed.Load(), seen.Load(), failed.Load())
	if failed.Load() > 0 {
		t.Fatalf("%d requests failed, first: %s", failed.Load(), failures[0])
	}
	if seen.Load() == 0 || ok.Load() < 100 {
		t.Fatalf("too little load: %d requests, %d v2", ok.Load(), seen.Load())
	}
	if code, _ := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d", code)
	}
}

func fmt0(err error, resp *stdhttp.Response, body []byte) string {
	if err != nil {
		return err.Error()
	}
	return resp.Status + " " + string(body)
}

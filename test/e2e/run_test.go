//go:build !windows

// Package e2e runs the fit binary as a user would, and talks to it over the
// network.
package e2e

import (
	"bufio"
	"fmt"
	"io"
	stdhttp "net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// fitBin is the fit binary TestMain builds.
var fitBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "fit-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fitBin = filepath.Join(dir, "fit")
	build := exec.Command("go", "build", "-o", fitBin, "github.com/lesismal/fit/cmd/fit")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "building fit:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// fit is a running fit process.
type fit struct {
	cmd    *exec.Cmd
	lines  chan string // its stdout, line by line; closed when it ends
	done   chan error  // its exit, once
	stderr *lockedBuffer
	config string // the config file's path
}

// lockedBuffer keeps what a process writes, for a test to read meanwhile.
type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

var listening = regexp.MustCompile(`^fit: listener "([^"]+)" on (\S+) `)

// adminListening is the line fit prints once its Admin API listens.
var adminListening = regexp.MustCompile(`^fit: admin API on (\S+)$`)

// startFit runs fit on the config doc and waits until it listens. It
// returns the address of each listener by name, and of the Admin API as
// "admin".
func startFit(t *testing.T, doc string) (*fit, map[string]string) {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "fit.json")
	if err := os.WriteFile(cfg, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(fitBin, "run", "-c", cfg)
	stderr := new(lockedBuffer)
	cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	f := &fit{cmd: cmd, lines: make(chan string, 16), done: make(chan error, 1), stderr: stderr, config: cfg}
	go func() {
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			f.lines <- s.Text()
		}
		close(f.lines)
		f.done <- cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		for range f.lines {
		}
	})

	addrs := map[string]string{}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-f.lines:
			if !ok {
				t.Fatalf("fit exited before listening: %v", <-f.done)
			}
			if m := listening.FindStringSubmatch(line); m != nil {
				addrs[m[1]] = m[2]
			} else if m := adminListening.FindStringSubmatch(line); m != nil {
				addrs["admin"] = m[1]
			} else {
				t.Fatalf("unexpected output: %q", line)
			}
			if strings.Count(doc, `"address"`) == len(addrs) {
				return f, addrs
			}
		case <-deadline:
			t.Fatal("fit did not start listening")
		}
	}
}

// stop sends fit SIGTERM and returns the rest of its output once it exits.
func (f *fit) stop(t *testing.T) (code int, output []string) {
	t.Helper()
	if err := f.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case line, ok := <-f.lines:
			if !ok {
				if err := <-f.done; err != nil {
					return f.cmd.ProcessState.ExitCode(), output
				}
				return 0, output
			}
			output = append(output, line)
		case <-timeout:
			t.Fatal("fit did not exit after SIGTERM")
		}
	}
}

func client(h2c bool) *stdhttp.Client {
	tr := &stdhttp.Transport{Protocols: new(stdhttp.Protocols)}
	if h2c {
		tr.Protocols.SetUnencryptedHTTP2(true)
	} else {
		tr.Protocols.SetHTTP1(true)
	}
	return &stdhttp.Client{Transport: tr, Timeout: 5 * time.Second}
}

const helloConfig = `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0", "protocols": ["http/1.1", "h2c"]}],
  "routes": [
    {"match": {"path": ["/hello"]}, "handler": {"module": "respond", "params": {"body": "hello\n", "headers": {"X-Fit": "e2e"}}}}
  ]
}`

func TestRunServesHTTP1AndH2C(t *testing.T) {
	f, addrs := startFit(t, helloConfig)
	base := "http://" + addrs["web"]
	for _, tt := range []struct {
		h2c   bool
		proto string
	}{{false, "HTTP/1.1"}, {true, "HTTP/2.0"}} {
		c := client(tt.h2c)
		resp, err := c.Get(base + "/hello")
		if err != nil {
			t.Fatalf("%s: %v", tt.proto, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.Proto != tt.proto || resp.StatusCode != 200 || string(body) != "hello\n" || resp.Header.Get("X-Fit") != "e2e" {
			t.Errorf("%s: %s %d %q %v", tt.proto, resp.Proto, resp.StatusCode, body, resp.Header)
		}
		resp, err = c.Get(base + "/missing")
		if err != nil {
			t.Fatalf("%s: %v", tt.proto, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s /missing: %d", tt.proto, resp.StatusCode)
		}
		// The connection stays open, idle, through the shutdown below.
	}

	code, out := f.stop(t)
	if code != 0 || strings.Join(out, "\n") != "fit: shutting down\nfit: stopped" {
		t.Fatalf("stop: exit %d, output %q", code, out)
	}
	if _, err := client(false).Get(base + "/hello"); err == nil {
		t.Fatal("fit still answers after stopping")
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "fit.json")
	if err := os.WriteFile(cfg, []byte(`{"listeners": [{"name": "web", "address": "127.0.0.1:0", "bogus": 1}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(fitBin, "run", "-c", cfg).CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(string(out), `listeners[0].bogus: unknown field`) {
		t.Fatalf("bad config: %v %q", err, out)
	}
}

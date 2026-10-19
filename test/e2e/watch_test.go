//go:build !windows

package e2e

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// TestWatch edits the config file of a fit run -watch and sees each edit
// served; a broken edit is reported and the running config stays.
func TestWatch(t *testing.T) {
	f, addrs := startFit(t, reloadConfig("v1", ""), "-watch", "-watch-interval", "50ms")
	url := "http://" + addrs["web"] + "/"
	body := func() string {
		resp, err := client(false).Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return string(data)
	}
	edit := func(doc string) {
		t.Helper()
		if err := os.WriteFile(f.config, []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitBody := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for body() != want {
			if time.Now().After(deadline) {
				t.Fatalf("still serving %q, want %q", body(), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	edit(reloadConfig("v2", ""))
	f.waitLine(t, "fit: "+f.config+" changed; reloading")
	f.waitLine(t, "fit: reloaded")
	waitBody("v2")

	// Broken: reported, and v2 stays.
	edit(`{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "nope"}}]}`)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(f.stderr.String(), `there is no module "nope"`) {
		if time.Now().After(deadline) {
			t.Fatalf("the broken edit was not reported: %q", f.stderr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := body(); got != "v2" {
		t.Fatalf("after the broken edit: %q", got)
	}

	// Fixed: applied.
	edit(reloadConfig("v3", ""))
	waitBody("v3")
	if code, _ := f.stop(t); code != 0 {
		t.Fatalf("stop: exit %d", code)
	}
}

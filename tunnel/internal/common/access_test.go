package common

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

func TestConsoleAccess(t *testing.T) {
	c := NewConsole(&app.Host{Engine: tunneltest.Engine(t), Log: discardLog(t)}, "tunnel.test", nil)
	defer c.Stop(context.Background())
	cfg := &config.RoleConsole{Address: "127.0.0.1:0", Users: []config.ConsoleUser{{Username: "admin", PasswordHash: hash(t, "pw")}}}
	if err := c.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	addr := c.Addr()
	reaches := func() bool {
		hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := hc.Get("http://" + addr + "/")
		if err != nil {
			return false
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return true
	}
	if !reaches() {
		t.Fatal("the pages are not reached without access")
	}
	// The access changes on the running pages: they do not start anew.
	denied := *cfg
	denied.Access = &config.Access{Deny: []string{"127.0.0.1"}}
	if err := c.Apply(&denied); err != nil {
		t.Fatal(err)
	}
	if c.Addr() != addr {
		t.Fatalf("a change of access moved the pages from %s to %s", addr, c.Addr())
	}
	for range 3 {
		if reaches() {
			t.Fatal("a denied client reached the pages")
		}
	}
	if err := c.Apply(cfg); err != nil {
		t.Fatal(err)
	}
	if c.Addr() != addr || !reaches() {
		t.Fatal("the pages are not reached once access is gone")
	}
	// One that does not compile leaves the policy before in force.
	bad := *cfg
	bad.Access = &config.Access{AllowFiles: []string{"/nonexistent/allow.txt"}}
	if err := c.Apply(&bad); err == nil {
		t.Fatal("an access whose file is missing was applied")
	}
	if !reaches() {
		t.Fatal("a bad access changed the policy")
	}
}

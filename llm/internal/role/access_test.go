package role

import (
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
)

func TestConsoleAccess(t *testing.T) {
	b := newBase(t, model.RoleHub, &editor{})
	cfg := consoleCfg(t)
	if err := b.Apply(Common{ID: "hub-c", Console: cfg}); err != nil {
		t.Fatal(err)
	}
	c := b.Console()
	reaches := func() bool {
		hc := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := hc.Get(c.URL())
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
	// A change of access is set on the running pages, which do not start
	// anew.
	denied := *cfg
	denied.Access = &config.Access{Deny: []string{"127.0.0.1"}}
	if err := b.Apply(Common{ID: "hub-c", Console: &denied}); err != nil || b.Console() != c {
		t.Fatalf("a change of access restarted the pages: %v", err)
	}
	if reaches() {
		t.Fatal("a denied client reached the pages")
	}
	if err := b.Apply(Common{ID: "hub-c", Console: cfg}); err != nil || b.Console() != c {
		t.Fatalf("access removed: %v", err)
	}
	if !reaches() {
		t.Fatal("the pages are not reached once access is gone")
	}
}

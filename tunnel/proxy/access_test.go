package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/tunneltest"
)

// servedOpen checks a connection to addr stays open.
func servedOpen(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("%s: an allowed address was closed: %v", addr, err)
	}
}

// setGlobal sets the config's global access, as JSON, and applies the
// config again, the proxy's member unchanged.
func (h *harness) setGlobal(access string) {
	h.t.Helper()
	h.mu.Lock()
	if access == "" {
		delete(h.doc, "access")
	} else {
		var v any
		if err := json.Unmarshal([]byte(access), &v); err != nil {
			h.t.Fatal(err)
		}
		h.doc["access"] = v
	}
	h.mu.Unlock()
	if _, err := h.edit("", []string{"tunnel", "proxy"}, func(cur []byte) ([]byte, error) { return cur, nil }); err != nil {
		h.t.Fatal(err)
	}
}

// The proxy's access, then the global one, decides which addresses its
// forwards and web proxies take: set on every Apply, the bindings kept, and
// kept in the member through the pages' edits.
func TestAccess(t *testing.T) {
	echo := tunneltest.Echo(t, "")
	var a hubAddrs
	// The hub serves the loopback whatever the global access says, so
	// that the node and the proxy stay linked.
	h := start(t, func() string {
		a = hubAddrs{freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)}
		return fmt.Sprintf(`{"tunnel": {
 "hub": {"name": "hub", "password": "pw", "access": {"allow": ["127.0.0.0/8"]}, "listeners": [{"protocol": "tcp", "address": %q}], "console_socket": {"address": %q, "password": "sock"}, "console": %s},
 "node": {"name": "lab", "hubs": [%s], "services": [{"name": "echo", "address": %q}]},
 "proxy": {"name": "me", "hubs": [%s], "forwards": [{"listen": %q, "node": "lab", "service": "echo"}],
  "web_proxies": [{"name": "hub", "listen": %q, "url": "ws://%s/", "password": "sock"}], "console": %s,
  "access": {"deny": ["10.0.0.0/8"]}}
}}`, a.listen, a.sock, consoleJSON(freeAddr(t)), uplinkJSON("hub", a.listen), echo, uplinkJSON("hub", a.listen), a.fwd, a.proxy, a.sock, consoleJSON(a.con))
	})
	h.waitNode("hub", "lab", "echo")
	indexes := func() [2]int {
		h.c.mu.Lock()
		defer h.c.mu.Unlock()
		return [2]int{h.c.forwards[0].index, h.c.proxies[0].index}
	}
	was := indexes()
	kept := func() {
		t.Helper()
		if got := indexes(); got != was {
			t.Fatalf("a change of access listened anew: %v, was %v", got, was)
		}
	}
	// The member's deny has no say on the loopback: served.
	echoOnce(t, a.fwd, "served")
	servedOpen(t, a.proxy)

	// The global access refuses the loopback, the member unchanged: the
	// same member applied again takes it.
	h.setGlobal(`{"deny": ["127.0.0.0/8"]}`)
	kept()
	refused(t, a.fwd)
	refused(t, a.proxy)

	// An edit from the pages keeps the member's access.
	p := login(t, a.con)
	p.expect("/api/meta", map[string]string{"site": "lab"}, 200, "ok")
	if cfg := h.c.Config(); cfg.Meta["site"] != "lab" || cfg.Access == nil || len(cfg.Access.Deny) != 1 {
		t.Fatalf("the member after the edit: %+v", cfg)
	}
	refused(t, a.fwd)

	// The member's allow decides before the global deny.
	if err := h.editProxy(func(m common.Member) error {
		m["access"] = json.RawMessage(`{"allow": ["127.0.0.1"]}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	kept()
	echoOnce(t, a.fwd, "allowed")
	servedOpen(t, a.proxy)

	// Without an access of its own, the global one decides.
	cfg := *h.c.Config()
	cfg.Access = nil
	if err := h.c.Apply(&cfg); err != nil {
		t.Fatal(err)
	}
	refused(t, a.fwd)
}

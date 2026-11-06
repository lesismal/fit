package tunnel_test

import (
	"strings"
	"testing"
	"time"
)

// A token revoked on the hub's pages closes its links at once, and its node
// links no more; one whose expiry passes is refused too.
func TestTokenRevoke(t *testing.T) {
	h := startHub(t, hubOpts{protocol: "tls", tokens: true})
	echoOnce(t, h.echoListen, "before")

	var st struct {
		Peers []struct {
			Info struct {
				Name string `json:"name"`
			} `json:"info"`
			Tokens []string `json:"tokens"`
		} `json:"peers"`
		PasswordPeers []string `json:"password_peers"`
	}
	h.hub.status(&st)
	if len(st.PasswordPeers) != 0 || len(st.Peers) != 2 || len(st.Peers[0].Tokens) != 1 {
		t.Fatalf("status %+v", st)
	}
	waitFor(t, "the bindings in the config", func() bool {
		m := string(h.proc.member("tunnel", "hub", "tokens"))
		return strings.Contains(m, `"node/lab"`) && strings.Contains(m, `"proxy/me"`)
	})

	// The proxy's token expires in a second: its links close, and it is
	// refused from then on.
	h.hub.post("/api/tokens/expiry", map[string]any{"id": h.tokens[1].ID, "expires_at": time.Now().Add(1500 * time.Millisecond).Unix()})
	waitFor(t, "the proxy to lose the hub", func() bool {
		var ps proxyStatus
		h.proxy.status(&ps)
		return len(ps.Hubs) == 1 && strings.Contains(ps.Hubs[0].Error, "expired")
	})

	// Revoking the node's token closes its links at once.
	h.hub.post("/api/tokens/revoke", map[string]any{"id": h.tokens[0].ID})
	waitFor(t, "the node to lose the hub", func() bool {
		var ns nodeStatus
		h.node.status(&ns)
		return len(ns.Hubs) == 1 && strings.Contains(ns.Hubs[0].Error, "no such token")
	})
	var after struct {
		Peers []any `json:"peers"`
	}
	waitFor(t, "the hub to forget the peers", func() bool {
		h.hub.status(&after)
		return len(after.Peers) == 0
	})
}

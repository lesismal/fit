package e2e

import (
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/mesh"
)

// servedOpen checks a connection to addr stays open, and refusedTCP that
// one is closed as soon as it is accepted.
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

func refusedTCP(t *testing.T, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("%s: a refused address was served (%v)", addr, err)
	}
}

// meshOf is the mesh of the role named role running in f.
func meshOf(t *testing.T, f *llmtest.Fit, role string) *mesh.Mesh {
	t.Helper()
	switch role {
	case "hub":
		return hubOf(t, f).Mesh
	case "node":
		return nodeOf(t, f).Mesh
	}
	return proxyOf(t, f).Mesh
}

// Each role's access, then the global one, decides which addresses its
// listen takes links from, and the proxy's api: set on every Apply, the
// listener kept, so that a link open stays, and a list that does not load
// fails the config.
func TestRoleAccess(t *testing.T) {
	for _, tc := range []struct{ role, dialer string }{{"hub", "node"}, {"node", "hub"}, {"proxy", "hub"}} {
		t.Run(tc.role, func(t *testing.T) {
			addr, api := freeAddr(t), ""
			extra := ""
			if tc.role == "proxy" {
				api = freeAddr(t)
				extra = `, "api": {"address": "` + api + `"}`
			}
			conf := func(global, own string) []byte {
				doc := `{"llm": {"` + tc.role + `": {"id": "` + tc.role + `-fixed", "listen": {"address": "` + addr + `", "password": "` + meshPass + `"}` + extra
				if own != "" {
					doc += `, "access": ` + own
				}
				doc += `}}`
				if global != "" {
					doc += `, "access": ` + global
				}
				return []byte(doc + `}`)
			}
			apply := func(f *llmtest.Fit, global, own string) error {
				_, err := f.Configs.Apply(conf(global, own), admin.SourceAdmin)
				return err
			}
			check := func(served bool) {
				t.Helper()
				f := refusedTCP
				if served {
					f = servedOpen
				}
				f(t, addr)
				if api != "" {
					f(t, api)
				}
			}
			f := llmtest.Start(t, string(conf("", `{"deny": ["10.0.0.0/8"]}`)), "")
			m := meshOf(t, f, tc.role)
			// The member's deny has no say on the loopback: served, and a
			// peer links.
			check(true)
			llmtest.Start(t, member(tc.dialer, `{"peers": [{"address": "`+addr+`", "password": "`+meshPass+`", "role": "`+tc.role+`"}]}`), "")
			llmtest.Eventually(t, "the link", func() bool { return len(m.Peers()) == 1 })
			conn := m.Peers()[0].ConnID
			linked := func() {
				t.Helper()
				if p := m.Peers(); len(p) != 1 || p[0].ConnID != conn {
					t.Fatalf("the link was dropped: %+v", p)
				}
			}

			// The global access refuses the loopback, the member unchanged:
			// new connections are refused, and the link open stays, the
			// listener kept.
			if err := apply(f, `{"deny": ["127.0.0.0/8"]}`, `{"deny": ["10.0.0.0/8"]}`); err != nil {
				t.Fatal(err)
			}
			check(false)
			linked()

			// The member's allow decides before the global deny.
			if err := apply(f, `{"deny": ["127.0.0.0/8"]}`, `{"allow": ["127.0.0.1"]}`); err != nil {
				t.Fatal(err)
			}
			check(true)
			linked()

			// A list file that does not load fails the config, and the role
			// runs by the access it had.
			missing := filepath.Join(t.TempDir(), "none")
			if err := apply(f, "", `{"deny_files": ["`+missing+`"]}`); err == nil || !strings.Contains(err.Error(), "none") {
				t.Fatalf("applied a deny file that does not exist: %v", err)
			}
			check(true)
			linked()
		})
	}
}

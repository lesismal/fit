//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// step runs one ordered part of the test; a failed step stops the whole run, since later steps build on it.
func step(t *testing.T, name string, fn func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.FailNow()
	}
}

// TestE2E configures the topology through the pages and exercises every role.
func TestE2E(t *testing.T) {
	step(t, "consoles", testConsoles)
	step(t, "mesh/setup", testMeshSetup)
	step(t, "mesh/rejects", testMeshRejects)
	step(t, "node/settings", testNodeSettings)
	step(t, "node/models", testNodeModels)
	step(t, "hub/aggregate", testHubAggregate)
	step(t, "node/gpu", testNodeGPU)
	step(t, "node/actions-jobs", testNodeActionsJobs)
	step(t, "node/console-proxy", testNodeConsoleProxy)
	step(t, "hub/manage", testHubManage)
	step(t, "client/keys", testClientKeys)
	step(t, "client/agent-api", testClientAgentAPI)
	step(t, "client/errors", testClientErrors)
	step(t, "routing/direct-node", testDirectNode)
	step(t, "routing/balance", testBalance)
	step(t, "routing/failover", testFailover)
	step(t, "routing/max-inflight", testMaxInflight)
	step(t, "routing/cancel", testCancel)
	step(t, "client/stats", testClientStats)
	step(t, "mesh/undial-kick", testUndialKick)
	step(t, "profiles", testProfiles)
	// agenthub's console/password step is gone: fit's role pages have no
	// password change; their users are the console member's, in the config.
	step(t, "save-shared", func(t *testing.T) {
		s := shared{Key: keyMain, Key2: key2, IDs: map[string]string{}}
		for n, c := range consoles {
			s.IDs[n] = c.id
		}
		saveShared(t, s)
	})
	// last: it locks this test's address out of node3's pages until node3
	// restarts
	step(t, "console/login-lockout", testLoginLockout)
}

// keyMain: a client1 API key made by testClientKeys; key2 is client2's.
var keyMain, key2 string

// tagOf maps a node console name to the mock upstream tag its models use.
var tagOf = map[string]string{"node1": "n1", "node2": "n2", "node3": "n3"}

func nodeName(id string) string {
	for n, c := range consoles {
		if c.id == id {
			return n
		}
	}
	return id
}

// ---- pages ----

func testConsoles(t *testing.T) {
	plain := httpClient(nil)
	for _, name := range []string{"hub", "node1", "node2", "node3", "client1", "client2"} {
		c := con(name)
		t.Run(name, func(t *testing.T) {
			// fit has no /healthz: wait for the pages themselves.
			eventually(t, 60*time.Second, name+" pages", func() error {
				resp, err := plain.Get(c.base + "/")
				if err != nil {
					return err
				}
				resp.Body.Close()
				if resp.StatusCode != 200 {
					return fmt.Errorf("HTTP %d", resp.StatusCode)
				}
				return nil
			})
			for _, p := range []string{"/", "/app.js", "/static/ui.js", "/static/ui.css", "/static/peers.js", "/static/jobs.js"} {
				resp, err := plain.Get(c.base + p)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != 200 {
					t.Fatalf("GET %s: HTTP %d", p, resp.StatusCode)
				}
				if resp.Header.Get("Content-Security-Policy") == "" || resp.Header.Get("Strict-Transport-Security") == "" {
					t.Fatalf("GET %s: no Content-Security-Policy or Strict-Transport-Security: %v", p, resp.Header)
				}
			}
			// A session is required; logins are JSON only.
			if st, _, _ := c.do("GET", "/api/me", nil, nil); st != 401 {
				t.Fatalf("/api/me before login: HTTP %d, want 401", st)
			}
			if st, _ := c.rpcErr("/mesh/peers", nil); st != 401 {
				t.Fatalf("rpc before login: HTTP %d, want 401", st)
			}
			if st, _, _ := c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
				map[string]string{"Content-Type": "text/plain"}); st != 415 {
				t.Fatalf("login as text/plain: HTTP %d, want 415", st)
			}
			if st, _ := c.login("wrong-password"); st != 401 {
				t.Fatalf("login with a wrong password: HTTP %d, want 401", st)
			}
			if st, _, _ := c.do("POST", "/api/login", map[string]string{"username": "root", "password": password}, nil); st != 401 {
				t.Fatalf("login as another user: HTTP %d, want 401", st)
			}
			// Requests from another web page are refused, the login too.
			if st, _, _ := c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password},
				map[string]string{"Origin": "https://evil.example"}); st != 403 {
				t.Fatalf("login from another origin: HTTP %d, want 403", st)
			}
			if _, err := c.login(password); err != nil {
				t.Fatal(err)
			}
			st, raw, err := c.do("GET", "/api/me", nil, nil)
			if err != nil || st != 200 {
				t.Fatalf("/api/me: HTTP %d %v", st, err)
			}
			var me struct {
				User     string   `json:"user"`
				Identity identity `json:"identity"`
				APIURL   string   `json:"api_url"`
			}
			_ = json.Unmarshal(raw, &me)
			if me.User != "admin" || me.Identity.Role != c.role || !strings.HasPrefix(me.Identity.ID, c.role+"-") {
				t.Fatalf("/api/me: %s", raw)
			}
			if c.role == "client" && !strings.HasPrefix(me.APIURL, "https://") {
				t.Fatalf("/api/me of a client: no https api_url: %s", raw)
			}
			c.id = me.Identity.ID
			st, raw, _ = c.do("GET", "/api/session", nil, nil)
			if st != 200 || !bytes.Contains(raw, []byte(c.csrf)) {
				t.Fatalf("/api/session: HTTP %d %s", st, raw)
			}
			// Every request but a GET needs the session's CSRF token.
			if st, _, _ := c.do("POST", "/api/rpc/mesh/peers", map[string]any{}, map[string]string{"X-Fit-Csrf": ""}); st != 403 {
				t.Fatalf("rpc without the CSRF token: HTTP %d, want 403", st)
			}
			if st, _, _ := c.do("POST", "/api/rpc/mesh/peers", map[string]any{}, map[string]string{"X-Fit-Csrf": "wrong"}); st != 403 {
				t.Fatalf("rpc with a wrong CSRF token: HTTP %d, want 403", st)
			}
			// A method takes JSON only.
			req, _ := http.NewRequest("POST", c.base+"/api/rpc/mesh/peers", strings.NewReader("{not json"))
			req.Header.Set("X-Fit-Csrf", c.csrf)
			req.Header.Set("Content-Type", "application/json")
			if resp, err := c.hc.Do(req); err != nil || resp.StatusCode != 400 {
				t.Fatalf("rpc with a body that is not JSON: %v %v, want 400", resp, err)
			} else {
				resp.Body.Close()
			}
			if st, _ := c.rpcErr("/no/such/method", nil); st != 404 {
				t.Fatalf("unknown method: HTTP %d, want 404", st)
			}
		})
	}
	t.Run("logout", func(t *testing.T) {
		c := newConsole("hub-logout", "hub", "hub:9200")
		if _, err := c.login(password); err != nil {
			t.Fatal(err)
		}
		if st, _, _ := c.do("POST", "/api/logout", nil, nil); st != 204 {
			t.Fatalf("logout: HTTP %d, want 204", st)
		}
		if st, _, _ := c.do("GET", "/api/me", nil, nil); st != 401 {
			t.Fatalf("/api/me after logout: HTTP %d, want 401", st)
		}
	})
}

// ---- mesh ----

func dial(t testing.TB, from, addr, role string) {
	t.Helper()
	con(from).rpc(t, "/mesh/dial", map[string]string{"addr": addr, "role": role, "password": meshPass}, nil)
}

// waitTargets waits until every dial target of a console is connected.
func waitTargets(t testing.TB, name string, n int) {
	t.Helper()
	eventually(t, 30*time.Second, name+" dial targets connected", func() error {
		p := con(name).peers(t)
		if len(p.Targets) != n {
			return fmt.Errorf("%d targets, want %d", len(p.Targets), n)
		}
		for _, tg := range p.Targets {
			if !tg.Connected {
				return fmt.Errorf("%s (%s) not connected: %s", tg.Addr, tg.Role, tg.Error)
			}
		}
		return nil
	})
}

func testMeshSetup(t *testing.T) {
	hub := con("hub")
	hub.rpcFail(t, "/mesh/listen", map[string]string{"addr": hubListen}, 400) // no password
	hub.rpcFail(t, "/mesh/listen", map[string]string{"addr": "no-port", "password": meshPass}, 400)
	hub.rpc(t, "/mesh/listen", map[string]string{"addr": hubListen, "password": meshPass}, nil)
	con("node2").rpc(t, "/mesh/listen", map[string]string{"addr": node2Listen, "password": meshPass}, nil)
	con("client2").rpc(t, "/mesh/listen", map[string]string{"addr": client2Listen, "password": meshPass}, nil)
	for _, name := range []string{"hub", "node2", "client2"} {
		p := con(name).peers(t)
		if p.Listen.Addr == "" || !p.Listen.HasPassword || p.Listen.Error != "" {
			t.Fatalf("%s listen: %+v", name, p.Listen)
		}
	}

	// Dial validation.
	n1 := con("node1")
	n1.rpcFail(t, "/mesh/dial", map[string]string{"addr": "node2:9101", "role": "node", "password": meshPass}, 400) // own role
	n1.rpcFail(t, "/mesh/dial", map[string]string{"addr": "hub", "role": "hub", "password": meshPass}, 400)         // no port
	n1.rpcFail(t, "/mesh/dial", map[string]string{"addr": "hub:9201", "role": "hub"}, 400)                          // no password
	n1.rpcFail(t, "/mesh/dial", map[string]string{"addr": "hub:9201", "role": "admin", "password": meshPass}, 400)  // unknown role

	// Every directed pair of roles.
	dial(t, "node1", "hub:9201", "hub")
	dial(t, "hub", "node2:9101", "node")
	dial(t, "client1", "hub:9201", "hub")
	dial(t, "client1", "node2:9101", "node")
	dial(t, "hub", "client2:9301", "client")
	dial(t, "node3", "client2:9301", "client")
	waitTargets(t, "node1", 1)
	waitTargets(t, "hub", 2)
	waitTargets(t, "client1", 2)
	waitTargets(t, "node3", 1)

	eventually(t, 10*time.Second, "hub connections", func() error {
		want := map[string]string{con("node1").id: "in", con("node2").id: "out", con("client1").id: "in", con("client2").id: "out"}
		got := map[string]string{}
		for _, c := range hub.peers(t).Conns {
			got[c.ID] = c.Direction
		}
		for id, dir := range want {
			if got[id] != dir {
				return fmt.Errorf("conn to %s: %q, want %q (all: %v)", nodeName(id), got[id], dir, got)
			}
		}
		return nil
	})
	// No console reply ever holds a password.
	for _, name := range []string{"hub", "node1", "node2", "client1", "client2", "node3"} {
		_, raw := con(name).rpcErr("/mesh/peers", nil)
		if bytes.Contains(raw, []byte(meshPass)) {
			t.Fatalf("%s /mesh/peers leaks the mesh password", name)
		}
	}
}

func testMeshRejects(t *testing.T) {
	n3 := con("node3")
	cases := []struct{ addr, role, pass, why string }{
		{"hub:9201", "hub", "wrong-password", "wrong password"},
		{"hub:9201", "client", meshPass, "declared role differs from the peer's"},
		{"node2:9101", "hub", meshPass, "same-role peer"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			n3.rpc(t, "/mesh/dial", map[string]string{"addr": tc.addr, "role": tc.role, "password": tc.pass}, nil)
			eventually(t, 15*time.Second, "dial error", func() error {
				for _, tg := range n3.peers(t).Targets {
					if tg.Addr == tc.addr {
						if tg.Connected || tg.Error == "" {
							return fmt.Errorf("target %+v", tg)
						}
						return nil
					}
				}
				return fmt.Errorf("target %s missing", tc.addr)
			})
			n3.rpc(t, "/mesh/undial", map[string]string{"addr": tc.addr}, nil)
		})
	}
	n3.rpcFail(t, "/mesh/undial", map[string]string{"addr": "nowhere:1"}, 404)
	waitTargets(t, "node3", 1)
	for _, c := range con("hub").peers(t).Conns {
		if c.ID == n3.id {
			t.Fatalf("hub has a connection to node3: %+v", c)
		}
	}
}

// ---- node ----

func testNodeSettings(t *testing.T) {
	n1 := con("node1")
	var s nodeSettings
	n1.rpc(t, "/node/settings/get", nil, &s)
	if s.AllowHubManage || !s.GPUSudo || s.RefreshInterval != "30s" || s.JobTimeout != "1h0m0s" {
		t.Fatalf("default settings: %+v", s)
	}
	// Refresh 5s to 1h, job timeout 10s to 168h; empty means the default.
	n1.rpcFail(t, "/node/settings/set", nodeSettings{RefreshInterval: "1s"}, 400)
	n1.rpcFail(t, "/node/settings/set", nodeSettings{RefreshInterval: "2h"}, 400)
	n1.rpcFail(t, "/node/settings/set", nodeSettings{JobTimeout: "5s"}, 400)
	n1.rpcFail(t, "/node/settings/set", nodeSettings{JobTimeout: "soon"}, 400)
	n1.rpc(t, "/node/settings/set", nodeSettings{AllowHubManage: true, GPUSudo: true, RefreshInterval: "10s"}, &s)
	if !s.AllowHubManage || !s.GPUSudo || s.RefreshInterval != "10s" || s.JobTimeout != "1h0m0s" {
		t.Fatalf("settings after set: %+v", s)
	}
	n1.rpc(t, "/node/settings/get", nil, &s)
	if !s.AllowHubManage || s.RefreshInterval != "10s" {
		t.Fatalf("settings read back: %+v", s)
	}
	n1.rpcFail(t, "/node/profile/set", map[string]string{"name": ""}, 400)
	n1.rpc(t, "/node/profile/set", map[string]string{"name": "node-one", "description": "e2e node 1"}, nil)
	var info nodeInfo
	n1.rpc(t, "/node/info", nil, &info)
	if info.Name != "node-one" || info.Description != "e2e node 1" || !info.HubManage {
		t.Fatalf("node info: %+v", info)
	}
}

func upstreamBase(tag string) string { return upstreamURL + "/u/" + tag + "/v1" }

func saveModel(t testing.TB, node string, m mdl) {
	t.Helper()
	con(node).rpc(t, "/node/model/save", m, nil)
}

func nodeModels(t testing.TB, node string) map[string]modelView {
	t.Helper()
	var info nodeInfo
	con(node).rpc(t, "/node/info", nil, &info)
	m := map[string]modelView{}
	for _, v := range info.Models {
		m[v.Name+"/"+v.API] = v
	}
	return m
}

func testNodeModels(t *testing.T) {
	n1 := con("node1")
	// Validation.
	for _, bad := range []mdl{
		{Name: "has space", API: "openai", BaseURL: upstreamBase("n1")},
		{Name: "x", API: "grpc", BaseURL: upstreamBase("n1")},
		{Name: "x", API: "openai", BaseURL: "ftp://upstream/v1"},
		{Name: "x", API: "openai", BaseURL: upstreamBase("n1") + "?q=1"},
		{Name: "x", API: "openai", BaseURL: upstreamBase("n1"), MaxInflight: -1},
	} {
		n1.rpcFail(t, "/node/model/save", bad, 400)
	}

	for node, tag := range map[string]string{"node1": "n1", "node2": "n2"} {
		saveModel(t, node, mdl{Name: "m-shared", API: "openai", BaseURL: upstreamBase(tag) + "/", Upstream: "up-" + tag, APIKey: "sk-" + tag})
		saveModel(t, node, mdl{Name: "m-claude", API: "anthropic", BaseURL: upstreamBase(tag), Upstream: "claude-up-" + tag, APIKey: "sk-" + tag})
	}
	saveModel(t, "node1", mdl{Name: "m-only1", API: "openai", BaseURL: upstreamBase("n1"), APIKey: "sk-n1", Description: "node1 only"})
	saveModel(t, "node1", mdl{Name: "m-limit", API: "openai", BaseURL: upstreamBase("n1"), APIKey: "sk-n1", MaxInflight: 1})
	saveModel(t, "node1", mdl{Name: "m-fail", API: "openai", BaseURL: "http://upstream:1/v1", APIKey: "sk-n1"}) // unreachable
	saveModel(t, "node2", mdl{Name: "m-fail", API: "openai", BaseURL: upstreamBase("n2"), APIKey: "sk-n2"})
	saveModel(t, "node1", mdl{Name: "m-badkey", API: "openai", BaseURL: upstreamBase("n1"), APIKey: "sk-wrong"})
	saveModel(t, "node1", mdl{Name: "m-off", API: "openai", BaseURL: upstreamBase("n1"), APIKey: "sk-n1", Disabled: true})
	saveModel(t, "node1", mdl{Name: "m-temp", API: "openai", BaseURL: upstreamBase("n1"), APIKey: "sk-n1"})
	saveModel(t, "node3", mdl{Name: "m-direct3", API: "openai", BaseURL: upstreamBase("n3"), APIKey: "sk-n3"})

	// Keys are masked on read; the masked value saved back keeps the key.
	var list []mdl
	n1.rpc(t, "/node/model/list", nil, &list)
	var shared *mdl
	for i := range list {
		if strings.HasPrefix(list[i].APIKey, "sk-") {
			t.Fatalf("model %s: key not masked: %q", list[i].Name, list[i].APIKey)
		}
		if list[i].Name == "m-shared" {
			shared = &list[i]
		}
	}
	if shared == nil || !strings.Contains(shared.APIKey, "****") || shared.BaseURL != upstreamBase("n1") {
		t.Fatalf("m-shared as listed: %+v", shared)
	}
	shared.Description = "resaved with the masked key"
	n1.rpc(t, "/node/model/save", shared, nil)
	var v modelView
	n1.rpc(t, "/node/model/check", map[string]string{"name": "m-shared", "api": "openai"}, &v)
	if v.State != "ready" {
		t.Fatalf("m-shared after resave + check: %+v", v)
	}
	// Same name in two formats needs api.
	saveModel(t, "node1", mdl{Name: "m-temp", API: "anthropic", BaseURL: upstreamBase("n1"), APIKey: "sk-n1"})
	n1.rpcFail(t, "/node/model/check", map[string]string{"name": "m-temp"}, 400)
	n1.rpc(t, "/node/model/delete", map[string]string{"name": "m-temp", "api": "anthropic"}, nil)
	n1.rpc(t, "/node/model/delete", map[string]string{"name": "m-temp"}, nil)
	n1.rpcFail(t, "/node/model/delete", map[string]string{"name": "m-temp"}, 404)

	// A wrong key / an unreachable upstream puts the model in error.
	n1.rpc(t, "/node/model/check", map[string]string{"name": "m-badkey"}, &v)
	if v.State != "error" || !strings.Contains(v.Error, "401") {
		t.Fatalf("m-badkey after check: %+v", v)
	}
	ms := nodeModels(t, "node1")
	if ms["m-off/openai"].State != "disabled" || ms["m-badkey/openai"].State != "error" || ms["m-limit/openai"].MaxInflight != 1 {
		t.Fatalf("node1 models: %+v", ms)
	}
	if st := upstreamStatsOf(t, "n1"); st.Checks == 0 {
		t.Fatalf("upstream n1 was never checked: %+v", st)
	}
}

func testHubAggregate(t *testing.T) {
	hub := con("hub")
	n1, n2 := con("node1").id, con("node2").id
	eventually(t, 10*time.Second, "hub model summary", func() error {
		m := hub.hubModel(t, "m-shared", "openai")
		if m == nil || m.Ready != 2 || !slices.Equal(m.Nodes, sortedIDs(n1, n2)) {
			return fmt.Errorf("m-shared: %+v", m)
		}
		if m := hub.hubModel(t, "m-claude", "anthropic"); m == nil || m.Ready != 2 {
			return fmt.Errorf("m-claude: %+v", m)
		}
		if m := hub.hubModel(t, "m-badkey", "openai"); m == nil || m.Ready != 0 {
			return fmt.Errorf("m-badkey: %+v", m)
		}
		if m := hub.hubModel(t, "m-off", "openai"); m != nil {
			return fmt.Errorf("a disabled model is listed: %+v", m)
		}
		if m := hub.hubModel(t, "m-direct3", "openai"); m != nil {
			return fmt.Errorf("node3 is not connected to the hub, yet its model is listed: %+v", m)
		}
		return nil
	})
	ns := hub.hubNodes(t)
	if !ns[n1].Online || !ns[n2].Online || ns[n1].Info.Name != "node-one" || !ns[n1].Info.HubManage || ns[n2].Info.HubManage {
		t.Fatalf("hub nodes: %+v", ns)
	}
	// node3 reached the hub once in mesh/rejects (hello done, then node3 refused the role), so the hub may remember
	// it, but only as offline.
	if n, ok := ns[con("node3").id]; ok && n.Online {
		t.Fatalf("hub lists node3 as online: %+v", n)
	}
	eventually(t, 10*time.Second, "client catalogs", func() error {
		for _, c := range []string{"client1", "client2"} {
			cm := con(c).catalog(t, "m-only1", "openai")
			if cm == nil || !cm.Ready {
				return fmt.Errorf("%s m-only1: %+v", c, cm)
			}
		}
		return nil
	})
}

func sortedIDs(ids ...string) []string {
	slices.Sort(ids)
	return ids
}

func testNodeGPU(t *testing.T) {
	n1 := con("node1")
	var catalog []struct {
		ID   string `json:"id"`
		Tool string `json:"tool"`
	}
	n1.rpc(t, "/node/gpu/catalog", nil, &catalog)
	if !slices.ContainsFunc(catalog, func(v struct {
		ID   string `json:"id"`
		Tool string `json:"tool"`
	}) bool {
		return v.ID == "nvidia"
	}) {
		t.Fatalf("catalog has no nvidia: %+v", catalog)
	}
	var st struct {
		OS      string `json:"os"`
		Root    bool   `json:"root"`
		Devices []struct {
			Vendor string `json:"vendor"`
			Name   string `json:"name"`
		} `json:"devices"`
		Errors map[string]string `json:"errors"`
	}
	n1.rpc(t, "/node/gpu/status", map[string]bool{"fresh": true}, &st)
	if st.OS != "debian" || !st.Root || len(st.Devices) != 1 || st.Devices[0].Name != "Fake GPU" || st.Errors["nvidia"] != "" {
		t.Fatalf("gpu status (fake nvidia-smi): %+v", st)
	}
	call := func(method string, req, out any) { n1.rpc(t, method, req, out) }
	var j job
	n1.rpc(t, "/node/gpu/set", map[string]any{"vendor": "nvidia", "index": 0, "key": "power_limit", "value": "250"}, &j)
	j = waitJob(t, call, j.ID, 20*time.Second)
	if j.Status != "done" || !strings.Contains(j.Output, "-pl 250") {
		t.Fatalf("power_limit job: %+v", j)
	}
	n1.rpc(t, "/node/gpu/set", map[string]any{"vendor": "nvidia", "index": -1, "key": "compute_mode", "value": "DEFAULT"}, &j)
	if j = waitJob(t, call, j.ID, 20*time.Second); j.Status != "done" || !strings.Contains(j.Title, "all GPUs") {
		t.Fatalf("compute_mode on all GPUs: %+v", j)
	}
	for _, bad := range []map[string]any{
		{"vendor": "nvidia", "index": 0, "key": "power_limit", "value": "250; reboot"},
		{"vendor": "nvidia", "index": 0, "key": "power_limit", "value": "abc"},
		{"vendor": "nvidia", "index": 0, "key": "compute_mode", "value": "TURBO"},
		{"vendor": "nvidia", "index": 0, "key": "no_such_setting", "value": "1"},
		{"vendor": "nvidia", "index": 5000, "key": "gpu_clock_reset"},
		{"vendor": "acme", "index": 0, "key": "power_limit", "value": "1"},
		{"vendor": "amd", "index": -1, "key": "power_limit", "value": "100"}, // no AMD GPU
	} {
		n1.rpcFail(t, "/node/gpu/set", bad, 400)
	}
	n1.rpcFail(t, "/node/gpu/install", map[string]string{"vendor": "acme"}, 400)
	n1.rpcFail(t, "/node/gpu/install", map[string]string{"vendor": "apple"}, 400) // no method for debian
}

func testNodeActionsJobs(t *testing.T) {
	n1 := con("node1")
	call := func(method string, req, out any) { n1.rpc(t, method, req, out) }
	var actions []struct {
		ID string `json:"id"`
	}
	n1.rpc(t, "/node/action/list", nil, &actions)
	ids := map[string]bool{}
	for _, a := range actions {
		ids[a.ID] = true
	}
	for _, want := range []string{"ollama_install", "ollama_list", "ollama_pull", "ollama_status", "nvidia_smi"} {
		if !ids[want] {
			t.Fatalf("action %s missing on linux: %v", want, ids)
		}
	}

	// Parameter validation: nothing reaches a shell.
	for _, bad := range []map[string]any{
		{"id": "no_such_action"},
		{"id": "ollama_pull"},
		{"id": "ollama_pull", "params": map[string]string{"model": "-rf"}},
		{"id": "ollama_pull", "params": map[string]string{"model": "qwen;reboot"}},
		{"id": "ollama_pull", "params": map[string]string{"model": "qwen $(id)"}},
		{"id": "ollama_pull", "params": map[string]string{"model": "qwen", "extra": "1"}},
		{"id": "ollama_logs", "params": map[string]string{"lines": "0"}},
		{"id": "ollama_list", "params": map[string]string{"model": "x"}},
	} {
		n1.rpcFail(t, "/node/action/run", bad, 400)
	}

	run := func(id string, params map[string]string) job {
		var j job
		n1.rpc(t, "/node/action/run", map[string]any{"id": id, "params": params}, &j)
		if j.Status != "running" || j.ID == 0 {
			t.Fatalf("%s started as %+v", id, j)
		}
		return j
	}
	j := waitJob(t, call, run("ollama_list", nil).ID, 20*time.Second)
	if j.Status != "done" || *j.ExitCode != 0 || !strings.HasPrefix(j.Output, "$ ollama list") || !strings.Contains(j.Output, "fake:latest") {
		t.Fatalf("ollama_list: %+v", j)
	}
	j = waitJob(t, call, run("ollama_pull", map[string]string{"model": "qwen3:8b"}).ID, 20*time.Second)
	if j.Status != "done" || !strings.Contains(j.Output, "pulling qwen3:8b") {
		t.Fatalf("ollama_pull: %+v", j)
	}
	j = waitJob(t, call, run("nvidia_smi", nil).ID, 20*time.Second)
	if j.Status != "done" || !strings.Contains(j.Output, "Fake NVIDIA-SMI") {
		t.Fatalf("nvidia_smi: %+v", j)
	}
	j = waitJob(t, call, run("ollama_status", nil).ID, 20*time.Second) // no systemctl in the container
	if j.Status != "failed" {
		t.Fatalf("ollama_status without systemctl: %+v", j)
	}

	// Follow a long job over SSE, read it from an offset, then cancel it.
	slow := run("ollama_pull", map[string]string{"model": "slow-model"})
	var seen strings.Builder
	frames := 0
	n1.stream(t, "/node/job/follow", map[string]any{"id": slow.ID, "offset": 0}, func(ev sseEvent) bool {
		var f job
		if ev.Event == "" && json.Unmarshal([]byte(ev.Data), &f) == nil {
			frames++
			seen.WriteString(f.Output)
		}
		return !strings.Contains(seen.String(), "pulling slow-model 5")
	})
	if frames == 0 || !strings.HasPrefix(seen.String(), "$ ollama pull slow-model") {
		t.Fatalf("follow: %d frames, output %q", frames, seen.String())
	}
	var part job
	n1.rpc(t, "/node/job/get", map[string]any{"id": slow.ID, "offset": 10}, &part)
	if part.Status != "running" || part.Offset != 10 || len(part.Output) != part.OutputLen-10 {
		t.Fatalf("job get from offset 10: offset %d, %d bytes of %d", part.Offset, len(part.Output), part.OutputLen)
	}
	var running nodeInfo
	n1.rpc(t, "/node/info", nil, &running)
	if running.Jobs < 1 {
		t.Fatalf("node info counts %d running jobs", running.Jobs)
	}
	n1.rpc(t, "/node/job/cancel", map[string]int64{"id": slow.ID}, nil)
	if j = waitJob(t, call, slow.ID, 10*time.Second); j.Status != "canceled" {
		t.Fatalf("canceled job: %+v", j)
	}
	n1.rpcFail(t, "/node/job/cancel", map[string]int64{"id": slow.ID}, 404)
	n1.rpcFail(t, "/node/job/get", map[string]int64{"id": 999999}, 404)
	// Following a finished job replays it and ends with its final state.
	evs := n1.stream(t, "/node/job/follow", map[string]any{"id": slow.ID, "offset": 0}, nil)
	if last := evs[len(evs)-1]; last.Event != "end" || !strings.Contains(last.Data, `"canceled"`) {
		t.Fatalf("follow of a canceled job ends with %+v", last)
	}

	var jobs []job
	n1.rpc(t, "/node/job/list", nil, &jobs)
	if len(jobs) < 6 || jobs[0].ID < jobs[len(jobs)-1].ID || jobs[0].Output != "" {
		t.Fatalf("job list (newest first, no output): %+v", jobs)
	}

	// Job timeout from the node settings.
	n1.rpc(t, "/node/settings/set", nodeSettings{AllowHubManage: true, GPUSudo: true, RefreshInterval: "10s", JobTimeout: "10s"}, nil)
	j = waitJob(t, call, run("ollama_pull", map[string]string{"model": "slow-timeout"}).ID, 30*time.Second)
	if j.Status != "failed" || j.Error != "timeout" {
		t.Fatalf("job past its timeout: %+v", j)
	}
	n1.rpc(t, "/node/settings/set", nodeSettings{AllowHubManage: true, GPUSudo: true, RefreshInterval: "10s"}, nil)
}

// proxyReq is model.ProxyReq ([]byte travels as base64 in JSON).
type proxyReq struct {
	API    string            `json:"api"`
	Path   string            `json:"path"`
	Model  string            `json:"model"`
	Body   []byte            `json:"body"`
	Header map[string]string `json:"header,omitempty"`
}

func testNodeConsoleProxy(t *testing.T) {
	n1 := con("node1")
	body := []byte(`{"model":"m-shared","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	evs := n1.stream(t, "/node/proxy", proxyReq{API: "openai", Path: "chat/completions", Model: "m-shared", Body: body}, nil)
	var head struct {
		Status int    `json:"status"`
		Node   string `json:"node"`
	}
	if len(evs) < 3 || json.Unmarshal([]byte(evs[0].Data), &head) != nil || head.Status != 200 || head.Node != n1.id {
		t.Fatalf("console proxy events: %+v", evs)
	}
	all := ""
	for _, ev := range evs[1:] {
		all += ev.Data + "\n"
	}
	if !strings.Contains(all, "echo from n1") || !strings.Contains(all, "[DONE]") {
		t.Fatalf("console proxy body: %s", all)
	}
	if end := evs[len(evs)-1]; end.Event != "end" || !strings.Contains(end.Data, `"status":200`) {
		t.Fatalf("console proxy end: %+v", end)
	}
	// Only the paths of the model's format; only known models.
	st, _, _ := n1.do("POST", "/api/stream/node/proxy", proxyReq{API: "openai", Path: "../admin", Model: "m-shared", Body: body}, nil)
	if st != 400 {
		t.Fatalf("proxy to a foreign path: HTTP %d, want 400", st)
	}
	st, _, _ = n1.do("POST", "/api/stream/node/proxy", proxyReq{API: "openai", Path: "chat/completions", Model: "nope", Body: body}, nil)
	if st != 503 {
		t.Fatalf("proxy of an unknown model: HTTP %d, want 503", st)
	}
}

// ---- hub management ----

func testHubManage(t *testing.T) {
	hub := con("hub")
	n1, n2 := con("node1").id, con("node2").id
	hcall := func(node, method string, req, out any) {
		t.Helper()
		raw, _ := json.Marshal(req)
		hub.rpc(t, "/hub/node/call", map[string]any{"id": node, "method": method, "req": json.RawMessage(raw)}, out)
	}
	hfail := func(node, method string, req any, want int) apiErr {
		t.Helper()
		raw, _ := json.Marshal(req)
		return hub.rpcFail(t, "/hub/node/call", map[string]any{"id": node, "method": method, "req": json.RawMessage(raw)}, want)
	}

	if r := hfail(n2, "/node/model/list", struct{}{}, 403); !strings.Contains(r.Error, "allow_hub_manage") {
		t.Fatalf("forbidden message: %s", r.Error)
	}
	// Only management methods, never the local-only settings.
	hfail(n1, "/node/settings/set", nodeSettings{AllowHubManage: true}, 400)
	hfail(n1, "/node/settings/get", struct{}{}, 400)
	hfail(n1, "/mesh/dial", map[string]string{"addr": "x:1"}, 400)
	hfail(n1, "/node/job/follow", map[string]int{"id": 1}, 400) // a stream: use /hub/node/stream
	hfail("node-0000000000000000", "/node/model/list", struct{}{}, 503)

	var list []mdl
	hcall(n1, "/node/model/list", struct{}{}, &list)
	if len(list) == 0 {
		t.Fatal("managed model list is empty")
	}
	for _, m := range list {
		if strings.HasPrefix(m.APIKey, "sk-") {
			t.Fatalf("hub sees a plain API key: %+v", m)
		}
	}
	// Save, check and delete a model on node1 through the hub.
	hcall(n1, "/node/model/save", mdl{Name: "m-hubmade", API: "openai", BaseURL: upstreamBase("n1"), APIKey: "sk-n1"}, nil)
	var v modelView
	hcall(n1, "/node/model/check", map[string]string{"name": "m-hubmade"}, &v)
	if v.State != "ready" {
		t.Fatalf("m-hubmade: %+v", v)
	}
	eventually(t, 10*time.Second, "m-hubmade in the hub summary", func() error {
		if m := hub.hubModel(t, "m-hubmade", "openai"); m == nil || m.Ready != 1 {
			return fmt.Errorf("%+v", m)
		}
		return nil
	})
	hcall(n1, "/node/model/delete", map[string]string{"name": "m-hubmade"}, nil)
	eventually(t, 10*time.Second, "m-hubmade gone from the hub summary", func() error {
		if m := hub.hubModel(t, "m-hubmade", "openai"); m != nil {
			return fmt.Errorf("%+v", m)
		}
		return nil
	})

	// GPU, actions and jobs through the hub, following the job over /hub/node/stream.
	var gpuSt struct {
		Devices []any `json:"devices"`
	}
	hcall(n1, "/node/gpu/status", struct{}{}, &gpuSt)
	if len(gpuSt.Devices) != 1 {
		t.Fatalf("managed gpu status: %+v", gpuSt)
	}
	hfail(n1, "/node/action/run", map[string]any{"id": "ollama_pull", "params": map[string]string{"model": "x;y"}}, 400)
	var j job
	hcall(n1, "/node/action/run", map[string]any{"id": "ollama_show", "params": map[string]string{"model": "qwen3:8b"}}, &j)
	evs := hub.stream(t, "/hub/node/stream", map[string]any{"id": n1, "method": "/node/job/follow",
		"req": map[string]any{"id": j.ID, "offset": 0}}, nil)
	if last := evs[len(evs)-1]; last.Event != "end" || !strings.Contains(last.Data, `"done"`) {
		t.Fatalf("followed through the hub: %+v", evs)
	}
	st, _, _ := hub.do("POST", "/api/stream/hub/node/stream", map[string]any{"id": n1, "method": "/node/model/list"}, nil)
	if st != 400 {
		t.Fatalf("/hub/node/stream of a non-stream method: HTTP %d, want 400", st)
	}

	// Revoking takes effect at once, without reconnecting.
	con("node1").rpc(t, "/node/settings/set", nodeSettings{GPUSudo: true, RefreshInterval: "10s"}, nil)
	hfail(n1, "/node/model/list", struct{}{}, 403)
	eventually(t, 10*time.Second, "hub sees hub_manage off", func() error {
		if hub.hubNodes(t)[n1].Info.HubManage {
			return fmt.Errorf("still on")
		}
		return nil
	})
	con("node1").rpc(t, "/node/settings/set", nodeSettings{AllowHubManage: true, GPUSudo: true, RefreshInterval: "10s"}, nil)
	hcall(n1, "/node/model/list", struct{}{}, &list)
}

// ---- client ----

func testClientKeys(t *testing.T) {
	c1 := con("client1")
	var k keyView
	c1.rpc(t, "/client/keys/create", map[string]string{"name": "e2e-main"}, &k)
	if !strings.HasPrefix(k.Key, "ah-") || len(k.Key) != 46 || k.Prefix != k.Key[:10] {
		t.Fatalf("new key: %+v", k)
	}
	keyMain = k.Key
	var spare keyView
	c1.rpc(t, "/client/keys/create", map[string]string{"name": "e2e-spare"}, &spare)

	_, raw := c1.rpcErr("/client/keys/list", nil)
	if bytes.Contains(raw, []byte(keyMain)) || bytes.Contains(raw, []byte("hash")) {
		t.Fatalf("key list leaks a key or a hash: %s", raw)
	}
	var keys []keyView
	_ = json.Unmarshal(raw, &keys)
	if len(keys) < 2 {
		t.Fatalf("key list: %+v", keys)
	}
	// A deleted key stops working at once.
	if r := agent(t, "client1", "GET", "/v1/models", nil, bearer(spare.Key)); r.Status != 200 {
		t.Fatalf("spare key: HTTP %d", r.Status)
	}
	c1.rpc(t, "/client/keys/delete", map[string]string{"id": spare.ID}, nil)
	if r := agent(t, "client1", "GET", "/v1/models", nil, bearer(spare.Key)); r.Status != 401 {
		t.Fatalf("deleted key: HTTP %d, want 401", r.Status)
	}
	c1.rpcFail(t, "/client/keys/delete", map[string]string{"id": spare.ID}, 404)

	var k2 keyView
	con("client2").rpc(t, "/client/keys/create", map[string]string{"name": "e2e"}, &k2)
	key2 = k2.Key
	if r := agent(t, "client2", "GET", "/v1/models", nil, bearer(keyMain)); r.Status != 401 {
		t.Fatalf("client1's key on client2: HTTP %d, want 401", r.Status)
	}
}

func testClientAgentAPI(t *testing.T) {
	r := agent(t, "client1", "GET", "/v1/models", nil, bearer(keyMain))
	m := r.json(t)
	data, _ := m["data"].([]any)
	names := map[string]bool{}
	for _, d := range data {
		e := d.(map[string]any)
		names[e["id"].(string)] = true
		if e["object"] != "model" || e["type"] != "model" || e["display_name"] == nil {
			t.Fatalf("/v1/models entry: %v", e)
		}
	}
	if m["object"] != "list" || m["has_more"] != false || !names["m-shared"] || !names["m-claude"] || !names["m-only1"] || names["m-off"] {
		t.Fatalf("/v1/models: %s", r.Body)
	}
	if r := agent(t, "client1", "GET", "/v1/models/m-claude", nil, map[string]string{"x-api-key": keyMain}); r.Status != 200 || r.json(t)["id"] != "m-claude" {
		t.Fatalf("/v1/models/m-claude: HTTP %d %s", r.Status, r.Body)
	}

	// OpenAI chat, non-streaming: model rewritten, the node's key used, caller headers dropped, body kept.
	hdr := bearer(keyMain)
	hdr["openai-beta"] = "assistants=v2"
	hdr["Cookie"] = "secret=1"
	r = agent(t, "client1", "POST", "/v1/chat/completions", []byte(`{"temperature":0.5,"model":"m-only1","messages":[{"role":"user","content":"<b>&amp;</b>"}]}`), hdr)
	if r.Status != 200 || r.Header.Get("X-Agenthub-Node") != con("node1").id || r.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("chat m-only1: HTTP %d %v %s", r.Status, r.Header, r.Body)
	}
	if msg := r.json(t)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]; msg != "echo from n1" {
		t.Fatalf("chat reply: %v", msg)
	}
	up := upstreamLast(t, "n1")
	if up.Path != "chat/completions" || up.Model != "m-only1" || up.Header["authorization"] != "Bearer sk-n1" ||
		up.Header["openai-beta"] != "assistants=v2" || up.Header["cookie"] != "" || !bytes.Contains(up.Body, []byte(`<b>&amp;</b>`)) ||
		!bytes.Contains(up.Body, []byte(`"temperature":0.5`)) {
		t.Fatalf("upstream saw: %+v %s", up, up.Body)
	}

	// Streaming chat: SSE relayed event by event.
	r = agent(t, "client1", "POST", "/v1/chat/completions", map[string]any{"model": "m-shared", "stream": true, "chunks": 4,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}}, bearer(keyMain))
	if r.Status != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream chat: HTTP %d %s", r.Status, r.Header.Get("Content-Type"))
	}
	var chunks []string
	_ = readSSE(bytes.NewReader(r.Body), func(ev sseEvent) bool { chunks = append(chunks, ev.Data); return true })
	if len(chunks) != 5 || chunks[4] != "[DONE]" {
		t.Fatalf("stream chunks: %q", chunks)
	}
	tag := tagOf[nodeName(r.Header.Get("X-Agenthub-Node"))]
	if up := upstreamLast(t, tag); up.Model != "up-"+tag {
		t.Fatalf("m-shared not rewritten to its upstream id: %+v", up)
	}

	// The other OpenAI paths.
	for _, p := range []string{"responses", "completions", "embeddings"} {
		r = agent(t, "client1", "POST", "/v1/"+p, map[string]any{"model": "m-only1", "input": "hi"}, bearer(keyMain))
		if r.Status != 200 || upstreamLast(t, "n1").Path != p {
			t.Fatalf("/v1/%s: HTTP %d %s", p, r.Status, r.Body)
		}
	}

	r = agent(t, "client1", "POST", "/v1/messages", map[string]any{"model": "m-claude", "stream": true, "max_tokens": 16,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		map[string]string{"x-api-key": keyMain, "anthropic-version": "2023-06-01", "anthropic-beta": "tools-2024-04-04"})
	if r.Status != 200 {
		t.Fatalf("messages: HTTP %d %s", r.Status, r.Body)
	}
	var events []string
	_ = readSSE(bytes.NewReader(r.Body), func(ev sseEvent) bool { events = append(events, ev.Event); return true })
	if len(events) != 4 || events[3] != "message_stop" {
		t.Fatalf("messages events: %v", events)
	}
	tag = tagOf[nodeName(r.Header.Get("X-Agenthub-Node"))]
	up = upstreamLast(t, tag)
	if up.Path != "messages" || up.Model != "claude-up-"+tag || up.Header["x-api-key"] != "sk-"+tag ||
		up.Header["anthropic-version"] != "2023-06-01" || up.Header["anthropic-beta"] != "tools-2024-04-04" || up.Header["authorization"] != "" {
		t.Fatalf("anthropic upstream saw: %+v", up)
	}
	// Without anthropic-version the node sends the default; count_tokens.
	r = agent(t, "client1", "POST", "/v1/messages/count_tokens", map[string]any{"model": "m-claude",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}}, bearer(keyMain))
	if r.Status != 200 || r.json(t)["input_tokens"] != float64(7) {
		t.Fatalf("count_tokens: HTTP %d %s", r.Status, r.Body)
	}
	if up := upstreamLast(t, tagOf[nodeName(r.Header.Get("X-Agenthub-Node"))]); up.Header["anthropic-version"] != "2023-06-01" {
		t.Fatalf("default anthropic-version: %+v", up)
	}
}

func testClientErrors(t *testing.T) {
	chatBody := func(model string) map[string]any {
		return map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "hi"}}}
	}
	openaiErr := func(r agentResp, status int, what string) {
		t.Helper()
		if r.Status != status {
			t.Fatalf("%s: HTTP %d, want %d: %s", what, r.Status, status, r.Body)
		}
		if e, ok := r.json(t)["error"].(map[string]any); !ok || e["message"] == "" {
			t.Fatalf("%s: not an OpenAI error: %s", what, r.Body)
		}
	}
	anthropicErr := func(r agentResp, status int, typ, what string) {
		t.Helper()
		m := r.json(t)
		e, _ := m["error"].(map[string]any)
		if r.Status != status || m["type"] != "error" || e["type"] != typ {
			t.Fatalf("%s: HTTP %d, want %d %s: %s", what, r.Status, status, typ, r.Body)
		}
	}
	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", chatBody("m-shared"), nil), 401, "no key")
	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", chatBody("m-shared"), bearer("ah-wrong")), 401, "wrong key")
	openaiErr(agent(t, "client1", "GET", "/v1/models", nil, nil), 401, "models without a key")
	anthropicErr(agent(t, "client1", "POST", "/v1/messages", chatBody("m-claude"), nil), 401, "authentication_error", "messages without a key")
	anthropicErr(agent(t, "client1", "GET", "/v1/models", nil, map[string]string{"anthropic-version": "2023-06-01"}), 401, "authentication_error", "anthropic models without a key")

	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", []byte(`{not json`), bearer(keyMain)), 400, "bad JSON")
	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", map[string]any{"messages": []any{}}, bearer(keyMain)), 400, "no model")
	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", chatBody("no-such-model"), bearer(keyMain)), 404, "unknown model")
	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", chatBody("m-claude"), bearer(keyMain)), 404, "anthropic model on the OpenAI endpoint")
	anthropicErr(agent(t, "client1", "POST", "/v1/messages", chatBody("m-shared"), bearer(keyMain)), 404, "not_found_error", "OpenAI model on /v1/messages")
	openaiErr(agent(t, "client1", "POST", "/v1/chat/completions", chatBody("m-badkey"), bearer(keyMain)), 503, "no ready route")
	if r := agent(t, "client1", "POST", "/v1/no/such/path", chatBody("m-shared"), bearer(keyMain)); r.Status != 404 {
		t.Fatalf("unknown /v1 path: HTTP %d", r.Status)
	}
	if r := agent(t, "client1", "GET", "/v1/models/no-such-model", nil, bearer(keyMain)); r.Status != 404 {
		t.Fatalf("unknown model id: HTTP %d", r.Status)
	}
	big := append([]byte(`{"model":"m-shared","pad":"`), bytes.Repeat([]byte("x"), 33<<20)...)
	big = append(big, `"}`...)
	if st := oversized(t, apiHost["client1"], keyMain, big); st != 413 {
		t.Fatalf("33 MB body: HTTP %d, want 413", st)
	}
	// An upstream's own error is relayed as is.
	upstreamAdmin(t, "POST", "/admin/n1/down", nil)
	r := agent(t, "client1", "POST", "/v1/chat/completions", chatBody("m-only1"), bearer(keyMain))
	upstreamAdmin(t, "POST", "/admin/n1/up", nil)
	if r.Status != 503 || !strings.Contains(string(r.Body), "upstream n1 is down") {
		t.Fatalf("upstream error relayed: HTTP %d %s", r.Status, r.Body)
	}
}

// oversized posts body over a raw TLS connection and reads the answer while still writing: the client answers 413
// after 32 MiB (api.max_body_bytes' default) and closes, so a regular HTTP client may only see its write fail.
func oversized(t *testing.T, host, key string, body []byte) int {
	t.Helper()
	c, err := tls.Dial("tcp", host, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		fmt.Fprintf(c, "POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n"+
			"Content-Type: application/json\r\nContent-Length: %d\r\n\r\n", host, key, len(body))
		_, _ = c.Write(body)
	}()
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("oversized request: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func testDirectNode(t *testing.T) {
	// client1 -> node2 directly: preferred over the hub.
	cm := con("client1").catalog(t, "m-shared", "openai")
	kinds := map[string]bool{}
	for _, r := range cm.Routes {
		kinds[r.Kind] = true
	}
	if !kinds["node"] || !kinds["hub"] {
		t.Fatalf("client1 routes of m-shared: %+v", cm)
	}
	for i := 0; i < 4; i++ {
		if n := chat(t, "client1", keyMain, "m-shared"); n != con("node2").id {
			t.Fatalf("call %d served by %s, want node2 (direct)", i, nodeName(n))
		}
	}
	// node3 -> client2: a model only reachable through the direct link.
	eventually(t, 10*time.Second, "client2 sees m-direct3", func() error {
		if cm := con("client2").catalog(t, "m-direct3", "openai"); cm == nil || !cm.Ready || cm.Routes[0].Kind != "node" {
			return fmt.Errorf("%+v", cm)
		}
		return nil
	})
	if n := chat(t, "client2", key2, "m-direct3"); n != con("node3").id {
		t.Fatalf("m-direct3 served by %s", nodeName(n))
	}
	if r := agent(t, "client1", "POST", "/v1/chat/completions", map[string]any{"model": "m-direct3"}, bearer(keyMain)); r.Status != 404 {
		t.Fatalf("client1 -> m-direct3: HTTP %d, want 404", r.Status)
	}
}

func testBalance(t *testing.T) {
	// client2 reaches m-shared only through the hub, which spreads the calls.
	served := map[string]int{}
	for i := 0; i < 8; i++ {
		served[nodeName(chat(t, "client2", key2, "m-shared"))]++
	}
	if served["node1"] == 0 || served["node2"] == 0 {
		t.Fatalf("hub balance: %v", served)
	}
}

func testFailover(t *testing.T) {
	hub, n2 := con("hub"), con("node2")
	// An unreachable upstream: the node reports unavailable, the hub tries the other node.
	for i := 0; i < 6; i++ {
		if n := chat(t, "client2", key2, "m-fail"); n != n2.id {
			t.Fatalf("m-fail served by %s, want node2", nodeName(n))
		}
	}
	if v := nodeModels(t, "node1")["m-fail/openai"]; v.State != "error" {
		t.Fatalf("node1 m-fail after failing: %+v", v)
	}
	eventually(t, 10*time.Second, "m-fail ready on one node", func() error {
		if m := hub.hubModel(t, "m-fail", "openai"); m == nil || m.Ready != 1 {
			return fmt.Errorf("%+v", m)
		}
		return nil
	})

	// Upstream n2 down: node2's m-shared goes to error, everything goes to node1, also client1's direct route.
	upstreamAdmin(t, "POST", "/admin/n2/down", nil)
	var v modelView
	n2.rpc(t, "/node/model/check", map[string]string{"name": "m-shared", "api": "openai"}, &v)
	if v.State != "error" {
		t.Fatalf("node2 m-shared with its upstream down: %+v", v)
	}
	eventually(t, 10*time.Second, "m-shared ready on node1 only", func() error {
		if m := hub.hubModel(t, "m-shared", "openai"); m == nil || m.Ready != 1 {
			return fmt.Errorf("hub: %+v", m)
		}
		if cm := con("client1").catalog(t, "m-shared", "openai"); cm == nil || !cm.Ready {
			return fmt.Errorf("client1: %+v", cm)
		}
		return nil
	})
	for _, c := range []struct{ client, key string }{{"client1", keyMain}, {"client2", key2}, {"client1", keyMain}, {"client2", key2}} {
		if n := chat(t, c.client, c.key, "m-shared"); n != con("node1").id {
			t.Fatalf("%s m-shared with node2 down: served by %s", c.client, nodeName(n))
		}
	}
	upstreamAdmin(t, "POST", "/admin/n2/up", nil)
	n2.rpc(t, "/node/model/check", map[string]string{"name": "m-shared", "api": "openai"}, &v)
	if v.State != "ready" {
		t.Fatalf("node2 m-shared after recovery: %+v", v)
	}
	eventually(t, 10*time.Second, "m-shared ready on both nodes", func() error {
		if m := hub.hubModel(t, "m-shared", "openai"); m == nil || m.Ready != 2 {
			return fmt.Errorf("%+v", m)
		}
		return nil
	})
}

func testMaxInflight(t *testing.T) {
	hub := con("hub")
	var wg sync.WaitGroup
	wg.Add(1)
	var first agentResp
	go func() {
		defer wg.Done()
		first = agent(t, "client1", "POST", "/v1/chat/completions", map[string]any{"model": "m-limit", "sleep_ms": 4000}, bearer(keyMain))
	}()
	eventually(t, 5*time.Second, "m-limit busy", func() error {
		if m := hub.hubModel(t, "m-limit", "openai"); m == nil || m.Ready != 0 {
			return fmt.Errorf("%+v", m)
		}
		return nil
	})
	if r := agent(t, "client1", "POST", "/v1/chat/completions", map[string]any{"model": "m-limit"}, bearer(keyMain)); r.Status != 503 {
		t.Fatalf("second call over max_inflight: HTTP %d, want 503: %s", r.Status, r.Body)
	}
	wg.Wait()
	if first.Status != 200 {
		t.Fatalf("first call: HTTP %d %s", first.Status, first.Body)
	}
	eventually(t, 5*time.Second, "m-limit free", func() error {
		if m := hub.hubModel(t, "m-limit", "openai"); m == nil || m.Ready != 1 {
			return fmt.Errorf("%+v", m)
		}
		return nil
	})
	chat(t, "client1", keyMain, "m-limit")
}

func testCancel(t *testing.T) {
	// The agent goes away mid-stream: the cancel reaches the upstream request.
	before := upstreamStatsOf(t, "n1").Canceled + upstreamStatsOf(t, "n2").Canceled
	body, _ := json.Marshal(map[string]any{"model": "m-shared", "stream": true, "chunks": 100, "chunk_ms": 100})
	req, _ := http.NewRequest("POST", "https://"+apiHost["client2"]+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key2)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if _, err := io.ReadAtLeast(resp.Body, buf, 1); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	eventually(t, 20*time.Second, "upstream request canceled", func() error {
		if now := upstreamStatsOf(t, "n1").Canceled + upstreamStatsOf(t, "n2").Canceled; now <= before {
			return fmt.Errorf("canceled %d", now)
		}
		return nil
	})
	eventually(t, 10*time.Second, "no calls in flight", func() error {
		for _, n := range []string{"node1", "node2"} {
			var info nodeInfo
			con(n).rpc(t, "/node/info", nil, &info)
			for _, m := range info.Models {
				if m.Inflight != 0 {
					return fmt.Errorf("%s %s inflight %d", n, m.Name, m.Inflight)
				}
			}
		}
		return nil
	})
}

func testClientStats(t *testing.T) {
	var s statsResp
	con("client1").rpc(t, "/client/stats", nil, &s)
	byModel := map[string]modelStats{}
	for _, m := range s.Models {
		byModel[m.Model] = m
	}
	if s.Total.Calls < 10 || byModel["m-shared"].Calls == 0 || byModel["m-shared"].Bytes == 0 || s.Total.Errors == 0 {
		t.Fatalf("client1 stats: %+v", s)
	}
	var keys []keyView
	con("client1").rpc(t, "/client/keys/list", nil, &keys)
	for _, k := range keys {
		if k.Name == "e2e-main" && (k.Calls == 0 || k.LastUsedAt == 0) {
			t.Fatalf("key usage not counted: %+v", k)
		}
	}
	var cms []catalogModel
	con("client1").rpc(t, "/client/models", nil, &cms)
	if len(cms) == 0 {
		t.Fatal("client1 catalog is empty")
	}
}

func testUndialKick(t *testing.T) {
	hub := con("hub")
	n1, n2 := con("node1").id, con("node2").id
	// Undial node2: offline in the hub (name and last seen kept), its models leave the summary.
	hub.rpc(t, "/mesh/undial", map[string]string{"addr": "node2:9101"}, nil)
	eventually(t, 10*time.Second, "node2 offline in the hub", func() error {
		n := hub.hubNodes(t)[n2]
		if n.Online || n.LastSeen == 0 {
			return fmt.Errorf("%+v", n)
		}
		if m := hub.hubModel(t, "m-shared", "openai"); m == nil || len(m.Nodes) != 1 {
			return fmt.Errorf("m-shared: %+v", m)
		}
		return nil
	})
	dial(t, "hub", "node2:9101", "node")
	waitTargets(t, "hub", 2)
	// Kick node1: its dial loop reconnects.
	hub.rpc(t, "/mesh/kick", map[string]string{"id": n1}, nil)
	eventually(t, 30*time.Second, "both nodes back", func() error {
		ns := hub.hubNodes(t)
		if !ns[n1].Online || !ns[n2].Online {
			return fmt.Errorf("node1 %v node2 %v", ns[n1].Online, ns[n2].Online)
		}
		if m := hub.hubModel(t, "m-shared", "openai"); m == nil || m.Ready != 2 {
			return fmt.Errorf("m-shared: %+v", m)
		}
		return nil
	})
	chat(t, "client2", key2, "m-shared")
}

func testProfiles(t *testing.T) {
	var id identity
	con("hub").rpc(t, "/hub/profile/set", map[string]string{"name": "hub-e2e"}, &id)
	if id.Name != "hub-e2e" {
		t.Fatalf("hub profile: %+v", id)
	}
	con("client1").rpc(t, "/client/profile/set", map[string]string{"name": "client-one"}, nil)
	con("client1").rpcFail(t, "/client/profile/set", map[string]string{"name": ""}, 400)
	if n := con("hub").hubNodes(t)[con("node1").id]; n.Info.Name != "node-one" {
		t.Fatalf("hub sees node1 as %q", n.Info.Name)
	}
}

// testLoginLockout: after 5 failed logins from one address (counting the 2
// of testConsoles), node3's pages refuse even the right password with 429
// and Retry-After, for 15 minutes or until node3 restarts.
func testLoginLockout(t *testing.T) {
	c := newConsole("node3-lockout", "node", "node3:9100")
	failed := 0
	for i := 0; i < 10; i++ {
		st, _ := c.login("wrong-password")
		if st == 429 {
			break
		}
		if st != 401 {
			t.Fatalf("failed login %d: HTTP %d, want 401", i+1, st)
		}
		failed++
	}
	if failed == 0 || failed > 5 {
		t.Fatalf("locked out after %d more failed logins, want 1 to 5", failed)
	}
	st, raw, err := c.do("POST", "/api/login", map[string]string{"username": "admin", "password": password}, nil)
	if err != nil || st != 429 {
		t.Fatalf("login with the right password while locked out: HTTP %d %s %v, want 429", st, raw, err)
	}
	// A session made before the lockout still works.
	con("node3").rpc(t, "/node/info", nil, nil)
}

// TestPersist runs after e2e.sh restarted every role: each reads its config
// back from its encrypted store, with everything its pages changed.
func TestPersist(t *testing.T) {
	s := loadShared(t)
	step(t, "login", func(t *testing.T) {
		for name, c := range consoles {
			eventually(t, 60*time.Second, name+" up", func() error {
				_, err := c.login(password)
				return err
			})
			var me struct {
				Identity identity `json:"identity"`
			}
			_, raw, _ := c.do("GET", "/api/me", nil, nil)
			_ = json.Unmarshal(raw, &me)
			if me.Identity.ID != s.IDs[name] {
				t.Fatalf("%s id %q, was %q", name, me.Identity.ID, s.IDs[name])
			}
			c.id = me.Identity.ID
			if name == "hub" && me.Identity.Name != "hub-e2e" || name == "client1" && me.Identity.Name != "client-one" ||
				name == "node1" && me.Identity.Name != "node-one" {
				t.Fatalf("%s name not kept: %q", name, me.Identity.Name)
			}
		}
	})
	step(t, "mesh", func(t *testing.T) {
		for name, addr := range map[string]string{"hub": hubListen, "node2": node2Listen, "client2": client2Listen} {
			if p := con(name).peers(t); p.Listen.Addr != addr || !p.Listen.HasPassword || p.Listen.Error != "" {
				t.Fatalf("%s listen: %+v", name, p.Listen)
			}
		}
		waitTargets(t, "node1", 1)
		waitTargets(t, "hub", 2)
		waitTargets(t, "client1", 2)
		waitTargets(t, "node3", 1)
		for _, name := range []string{"hub", "node1", "node2", "node3", "client1", "client2"} {
			_, raw := con(name).rpcErr("/mesh/peers", nil)
			if bytes.Contains(raw, []byte(meshPass)) {
				t.Fatalf("%s /mesh/peers leaks the mesh password", name)
			}
		}
	})
	step(t, "node", func(t *testing.T) {
		n1 := con("node1")
		var st nodeSettings
		n1.rpc(t, "/node/settings/get", nil, &st)
		if !st.AllowHubManage || !st.GPUSudo || st.RefreshInterval != "10s" || st.JobTimeout != "1h0m0s" {
			t.Fatalf("node1 settings: %+v", st)
		}
		var info nodeInfo
		n1.rpc(t, "/node/info", nil, &info)
		if info.Name != "node-one" || info.Description != "e2e node 1" || len(info.Models) != 7 {
			t.Fatalf("node1 info: %+v", info)
		}
		var list []mdl
		n1.rpc(t, "/node/model/list", nil, &list)
		byName := map[string]mdl{}
		for _, m := range list {
			if strings.HasPrefix(m.APIKey, "sk-") || !strings.Contains(m.APIKey, "****") {
				t.Fatalf("key of %s after restart: %q", m.Name, m.APIKey)
			}
			byName[m.Name+"/"+m.API] = m
		}
		if m := byName["m-shared/openai"]; m.Upstream != "up-n1" || m.BaseURL != upstreamBase("n1") || m.Description != "resaved with the masked key" {
			t.Fatalf("m-shared after restart: %+v", m)
		}
		if !byName["m-off/openai"].Disabled || byName["m-limit/openai"].MaxInflight != 1 {
			t.Fatalf("node1 models after restart: %+v", list)
		}
		// The keys were kept, not only their masks: a check reaches the upstream with them.
		var v modelView
		n1.rpc(t, "/node/model/check", map[string]string{"name": "m-shared", "api": "openai"}, &v)
		if v.State != "ready" {
			t.Fatalf("m-shared check after restart: %+v", v)
		}
	})
	step(t, "hub", func(t *testing.T) {
		eventually(t, 30*time.Second, "hub summary", func() error {
			ns := con("hub").hubNodes(t)
			if !ns[con("node1").id].Online || !ns[con("node2").id].Online {
				return fmt.Errorf("nodes %+v", ns)
			}
			if m := con("hub").hubModel(t, "m-shared", "openai"); m == nil || m.Ready != 2 {
				return fmt.Errorf("m-shared %+v", m)
			}
			return nil
		})
		// fit's hub keeps no node table across restarts: node3, which once
		// reached it in mesh/rejects, is not remembered.
		if ns := con("hub").hubNodes(t); len(ns) != 2 {
			t.Fatalf("hub nodes after restart: %+v", ns)
		}
	})
	step(t, "client", func(t *testing.T) {
		eventually(t, 30*time.Second, "client1 catalog", func() error {
			if cm := con("client1").catalog(t, "m-only1", "openai"); cm == nil || !cm.Ready {
				return fmt.Errorf("%+v", cm)
			}
			return nil
		})
		chat(t, "client1", s.Key, "m-only1")
		var keys []keyView
		con("client1").rpc(t, "/client/keys/list", nil, &keys)
		// fit keeps a key's usage (calls, last_used_at) in memory only, so
		// the counts start again; agenthub saved them on stop.
		if len(keys) != 1 || keys[0].Name != "e2e-main" || keys[0].Calls < 1 || keys[0].Calls >= 10 {
			t.Fatalf("keys after restart: %+v", keys)
		}
		eventually(t, 30*time.Second, "client2 catalog", func() error {
			if cm := con("client2").catalog(t, "m-direct3", "openai"); cm == nil || !cm.Ready {
				return fmt.Errorf("%+v", cm)
			}
			return nil
		})
		chat(t, "client2", s.Key2, "m-direct3")
		chat(t, "client2", s.Key2, "m-shared")
	})
}

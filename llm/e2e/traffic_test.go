package e2e

import (
	"io"
	"strings"
	"testing"

	"github.com/lesismal/fit/internal/certtest"
	"github.com/lesismal/fit/llm/internal/llmtest"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/proxy"
)

// fastLimits pings every 200ms, so that latency shows soon.
const fastLimits = `"limits": {"ping_interval": "200ms"}`

// The hub syncs its node list to its proxies: a node that joins, changes or
// leaves shows on the proxy's pages, which only read it; the hub's max_nodes,
// set on its pages, bounds it.
func TestNodeListSync(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	hubAddr := freeAddr(t)
	hf := llmtest.Start(t, member("hub", `{"name": "hub-a", "console": `+llmtest.Console()+`, `+fastLimits+`,
		"listen": {"address": "`+hubAddr+`", "password": "`+meshPass+`"}}`), "")
	nodeConf := func(name, desc string) string {
		return member("node", `{"name": "`+name+`", "description": "`+desc+`", "console": `+llmtest.Console()+`, `+fastLimits+`,
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}],
		"models": [{"name": "m-`+name+`", "api": "openai", "base_url": "`+mock.BaseURL(name)+`", "api_key": "sk-`+name+`"}]}`)
	}
	n1f := llmtest.Start(t, nodeConf("n1", "first"), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`, `+fastLimits+`,
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}]}`), "")
	h, n1, c := hubOf(t, hf), nodeOf(t, n1f), proxyOf(t, cf)
	hp, n1p, cp := pages(t, h.Console().URL()), pages(t, n1.Console().URL()), pages(t, c.Console().URL())

	nodes := func() proxy.NodesResp {
		var r proxy.NodesResp
		if err := cp.RPC(proxy.MethodProxyNodes, nil, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	find := func(r proxy.NodesResp, name string) *model.NodeSummary {
		for _, hb := range r.Hubs {
			for i := range hb.Nodes {
				if hb.Nodes[i].Name == name {
					return &hb.Nodes[i]
				}
			}
		}
		return nil
	}
	llmtest.Eventually(t, "node 1 on the proxy", func() bool {
		r := nodes()
		n := find(r, "n1")
		return len(r.Hubs) == 1 && r.Hubs[0].Online && r.Hubs[0].Hub.Name == "hub-a" && n != nil && n.Online &&
			n.Description == "first" && len(n.Models) == 1 && n.Models[0].Name == "m-n1" && n.HW.Arch != "" && len(n.Links) == 1
	})
	if r := nodes(); find(r, "n1").HW.Disks != nil || find(r, "n1").HW.IPs != nil {
		t.Fatalf("the list carries disks or addresses: %+v", find(r, "n1").HW)
	}

	// A node joins.
	n2f := llmtest.Start(t, nodeConf("n2", "second"), "")
	llmtest.Eventually(t, "node 2 on the proxy", func() bool {
		r := nodes()
		return find(r, "n2") != nil && find(r, "n2").Online && r.Hubs[0].Total == 2
	})
	// A node changes, from its own pages.
	if err := n1p.RPC(model.MethodNodeProfileSet, model.ProfileReq{Name: "n1", Description: "renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	llmtest.Eventually(t, "node 1's change on the proxy", func() bool {
		n := find(nodes(), "n1")
		return n != nil && n.Description == "renamed"
	})
	// A node leaves: it stays in the hub's table, offline.
	n2f.Stop()
	llmtest.Eventually(t, "node 2 offline on the proxy", func() bool {
		n := find(nodes(), "n2")
		return n != nil && !n.Online
	})
	// The proxy cannot manage nodes: the hub's management methods are not
	// the proxy's, and its own pages have none.
	if err := cp.RPC(model.MethodHubNodeCall, model.HubNodeCallReq{}, nil); llmtest.CodeOf(err) == 0 && err == nil {
		t.Fatal("a hub method answered on the proxy's pages")
	}

	// max_nodes, set on the hub's pages, bounds the table, and so the list.
	var hs model.HubSettings
	if err := hp.RPC(model.MethodHubSettingsGet, nil, &hs); err != nil || hs.MaxNodes != 1024 {
		t.Fatalf("hub settings %+v %v", hs, err)
	}
	if err := hp.RPC(model.MethodHubSettingsSet, model.HubSettings{MaxNodes: -1}, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
		t.Fatalf("a negative max_nodes: %v", err)
	}
	if err := hp.RPC(model.MethodHubSettingsSet, model.HubSettings{MaxNodes: 1}, &hs); err != nil || hs.MaxNodes != 1 {
		t.Fatalf("set max_nodes: %+v %v", hs, err)
	}
	if !strings.Contains(compact(hf.Current()), `"max_nodes":1`) {
		t.Fatalf("max_nodes not written back: %s", hf.Current())
	}
	llmtest.Eventually(t, "the offline node forgotten", func() bool {
		r := nodes()
		return find(r, "n2") == nil && find(r, "n1") != nil && r.Hubs[0].Total == 1
	})
	if err := hp.RPC(model.MethodHubSettingsSet, model.HubSettings{MaxNodes: 1024}, &hs); err != nil || hs.MaxNodes != 1024 ||
		strings.Contains(compact(hf.Current()), `max_nodes`) {
		t.Fatalf("the default max_nodes is left out: %+v %v %s", hs, err, hf.Current())
	}

	// The hub's link to the proxy goes; the proxy forgets the hub's list.
	hf.Stop()
	llmtest.Eventually(t, "the hub gone from the proxy", func() bool { return len(nodes().Hubs) == 0 })
}

// compact is a config without its white space.
func compact(b []byte) string { return strings.Join(strings.Fields(string(b)), "") }

// statusOf reads a role's /mesh/status through its pages.
func statusOf(t *testing.T, b *llmtest.Browser) model.MeshStatus {
	t.Helper()
	var s model.MeshStatus
	if err := b.RPC(model.MethodStatus, nil, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func routeOf(s model.MeshStatus, from, to string) model.RouteStats {
	var out model.RouteStats
	for _, r := range s.Routes {
		if r.From == from && r.To == to {
			out.Calls += r.Calls
			out.Errors += r.Errors
			out.Inflight += r.Inflight
			out.ReqBytes += r.ReqBytes
			out.RespBytes += r.RespBytes
			out.Model = r.Model
		}
	}
	return out
}

// Each role counts the calls it relays on each route, and the same bytes
// cross all three: a body inline in the call, one streamed past 1 MiB, and
// the answer, its head frame included. Every link has its bytes and
// latency; a link cut in the middle of an answer ends its call as failed.
func TestTrafficCounters(t *testing.T) {
	mock := llmtest.StartMockLLM(t)
	hubAddr := freeAddr(t)
	hf := llmtest.Start(t, member("hub", `{"console": `+llmtest.Console()+`, `+fastLimits+`,
		"listen": {"address": "`+hubAddr+`", "password": "`+meshPass+`"}}`), "")
	nf := llmtest.Start(t, member("node", `{"console": `+llmtest.Console()+`, `+fastLimits+`,
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}],
		"models": [{"name": "m", "api": "openai", "base_url": "`+mock.BaseURL("a")+`", "api_key": "sk-a"}]}`), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`, `+fastLimits+`, "api": {"address": "127.0.0.1:0"},
		"peers": [{"address": "`+hubAddr+`", "password": "`+meshPass+`", "role": "hub"}]}`), "")
	h, n, c := hubOf(t, hf), nodeOf(t, nf), proxyOf(t, cf)
	hp, np, cp := pages(t, h.Console().URL()), pages(t, n.Console().URL()), pages(t, c.Console().URL())
	hubID, nodeID, proxyID := h.Self().ID, n.Self().ID, c.Self().ID
	llmtest.Eventually(t, "the model on the proxy", func() bool {
		var cat []proxy.CatalogModel
		return cp.RPC(proxy.MethodProxyModels, nil, &cat) == nil && len(cat) == 1 && cat[0].Ready
	})
	var key proxy.KeyCreateResp
	if err := cp.RPC(proxy.MethodProxyKeysCreate, proxy.KeyCreateReq{Name: "k"}, &key); err != nil {
		t.Fatal(err)
	}
	a := newAgent(c.API(), key.Key)

	small := `{"model":"m","stream":false}`
	status, answer := a.call(t, "/v1/chat/completions", small, nil)
	if status != 200 {
		t.Fatalf("%d %s", status, answer)
	}
	// A route's call ends, and its in-flight count drops, once its answer
	// has gone out, which may be after the agent has read it.
	var pr, hr, nr model.RouteStats
	llmtest.Eventually(t, "the call counted on every route", func() bool {
		cs, hs, ns := statusOf(t, cp), statusOf(t, hp), statusOf(t, np)
		pr, hr, nr = routeOf(cs, "", hubID), routeOf(hs, proxyID, nodeID), routeOf(ns, hubID, "")
		return pr.Inflight == 0 && hr.Inflight == 0 && nr.Inflight == 0
	})
	for _, r := range []model.RouteStats{pr, hr, nr} {
		if r.Calls != 1 || r.Errors != 0 || r.Inflight != 0 || r.Model != "m" || r.ReqBytes != int64(len(small)) ||
			r.RespBytes <= int64(len(answer)) || r.RespBytes != nr.RespBytes {
			t.Fatalf("one call: proxy %+v hub %+v node %+v (answer %d bytes)", pr, hr, nr, len(answer))
		}
	}

	// A body past the inline bound streams, and counts the same.
	big := `{"model":"m","pad":"` + strings.Repeat("p", 2<<20) + `"}`
	if status, body := a.call(t, "/v1/chat/completions", big, nil); status != 200 {
		t.Fatalf("%d %.200s", status, body)
	}
	llmtest.Eventually(t, "the streamed call counted on every route", func() bool {
		cs, hs, ns := statusOf(t, cp), statusOf(t, hp), statusOf(t, np)
		pr, hr, nr = routeOf(cs, "", hubID), routeOf(hs, proxyID, nodeID), routeOf(ns, hubID, "")
		return pr.Inflight == 0 && hr.Inflight == 0 && nr.Inflight == 0
	})
	want := int64(len(small) + len(big))
	if pr.ReqBytes != want || hr.ReqBytes != want || nr.ReqBytes != want || pr.Calls != 2 {
		t.Fatalf("a streamed body: proxy %+v hub %+v node %+v, want %d", pr, hr, nr, want)
	}

	// Every link has its bytes, compressed as they crossed, and a latency.
	llmtest.Eventually(t, "the links' latency", func() bool {
		for _, s := range []model.MeshStatus{statusOf(t, cp), statusOf(t, hp), statusOf(t, np)} {
			for _, l := range s.Links {
				if l.Link.RTT.Samples == 0 || l.Link.BytesIn == 0 || l.Link.BytesOut == 0 {
					return false
				}
			}
		}
		return true
	})

	// The hub to node link, as the proxy sees it in the hub's list, with
	// the hub's traffic to the node.
	llmtest.Eventually(t, "the hub's traffic on the proxy", func() bool {
		var r proxy.NodesResp
		if cp.RPC(proxy.MethodProxyNodes, nil, &r) != nil || len(r.Hubs) != 1 || len(r.Hubs[0].Nodes) != 1 {
			return false
		}
		nd := r.Hubs[0].Nodes[0]
		return nd.Traffic.Calls == 2 && nd.Traffic.ReqBytes == want && len(nd.Links) == 1 && nd.Links[0].Link.RTT.Samples > 0
	})

	// A link cut in the middle of an answer: the call fails on every role
	// that saw it break, and runs no more.
	resp, err := a.post("/v1/chat/completions", map[string]any{"model": "m", "stream": true, "chunks": 50, "chunk_ms": 100}, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	llmtest.Eventually(t, "the call running", func() bool { return routeOf(statusOf(t, hp), proxyID, nodeID).Inflight == 1 })
	if err := hp.RPC(model.MethodKick, model.KickReq{ID: nodeID}, nil); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	llmtest.Eventually(t, "the cut call counted", func() bool {
		pr, hr, nr := routeOf(statusOf(t, cp), "", hubID), routeOf(statusOf(t, hp), proxyID, nodeID), routeOf(statusOf(t, np), hubID, "")
		return pr.Calls == 3 && pr.Errors == 1 && pr.Inflight == 0 && hr.Errors == 1 && hr.Inflight == 0 && nr.Errors == 1 && nr.Inflight == 0
	})
}

// Each role's own pages read and set its bounds, written back into its
// member and checked as the config is: the links' limits, the node's
// upstream bounds, the proxy's API.
func TestSettingsPages(t *testing.T) {
	nf := llmtest.Start(t, member("node", `{"console": `+llmtest.Console()+`}`), "")
	cf := llmtest.Start(t, member("proxy", `{"console": `+llmtest.Console()+`}`), "")
	n, c := nodeOf(t, nf), proxyOf(t, cf)
	np, cp := pages(t, n.Console().URL()), pages(t, c.Console().URL())

	// Limits, every role's.
	var l model.Limits
	if err := np.RPC(model.MethodLimits, nil, &l); err != nil || l.MaxConns != 1024 || l.PingInterval != "15s" || l.StreamIdle != "2m0s" {
		t.Fatalf("limits %+v %v", l, err)
	}
	if err := np.RPC(model.MethodLimitsSet, model.Limits{MaxConns: 10, PingInterval: "2s", StreamIdle: "2m"}, &l); err != nil ||
		l.MaxConns != 10 || l.PingInterval != "2s" || l.MaxInflight != 1024 {
		t.Fatalf("set limits %+v %v", l, err)
	}
	cur := compact(nf.Current())
	if !strings.Contains(cur, `"limits":{"max_conns":10,"ping_interval":"2s"}`) {
		t.Fatalf("limits not written back, defaults left out: %s", cur)
	}
	for _, bad := range []model.Limits{{MaxConns: -1}, {MaxInflight: -2}, {PingInterval: "50ms"}, {SlowTimeout: "x"},
		{RetryInterval: "1ms"}, {StreamIdle: "10ms"}} {
		if err := np.RPC(model.MethodLimitsSet, bad, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if err := np.RPC(model.MethodLimitsSet, model.Limits{}, &l); err != nil || l.MaxConns != 1024 || strings.Contains(compact(nf.Current()), `"limits"`) {
		t.Fatalf("back to the defaults: %+v %v %s", l, err, nf.Current())
	}

	// The node's upstream bounds.
	var u model.NodeUpstream
	if err := np.RPC(model.MethodNodeUpstreamGet, nil, &u); err != nil || u.ConnectTimeout != "10s" || u.MaxInflight != 256 || u.MaxBodyBytes != 32<<20 {
		t.Fatalf("upstream %+v %v", u, err)
	}
	if err := np.RPC(model.MethodNodeUpstreamSet, model.NodeUpstream{ConnectTimeout: "3s", Timeout: "1h", MaxInflight: 8, MaxBodyBytes: 1 << 20}, &u); err != nil ||
		u.ConnectTimeout != "3s" || u.Timeout != "1h0m0s" || u.MaxInflight != 8 || u.MaxBodyBytes != 1<<20 || u.IdleTimeout != "5m0s" {
		t.Fatalf("set upstream %+v %v", u, err)
	}
	if !strings.Contains(compact(nf.Current()), `"proxy":{"connect_timeout":"3s","max_body_bytes":1048576,"max_inflight":8,"timeout":"1h0m0s"}`) {
		t.Fatalf("upstream not written back: %s", nf.Current())
	}
	for _, bad := range []model.NodeUpstream{{ConnectTimeout: "-1s"}, {FirstByteTimeout: "soon"}, {IdleTimeout: "1"}, {Timeout: "x"},
		{MaxInflight: -1}, {MaxBodyBytes: -1}} {
		if err := np.RPC(model.MethodNodeUpstreamSet, bad, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
			t.Fatalf("%+v: %v", bad, err)
		}
	}

	// The proxy's API: none at first; a loopback address serves; a LAN one
	// without a certificate is refused, as the config refuses it; an empty
	// address takes it out.
	var as proxy.APISettings
	if err := cp.RPC(proxy.MethodProxyAPIGet, nil, &as); err != nil || as.Address != "" || as.URL != "" {
		t.Fatalf("api %+v %v", as, err)
	}
	if err := cp.RPC(proxy.MethodProxyAPISet, proxy.APISettings{Address: "127.0.0.1:0", MaxInflight: 9, Timeout: "10m"}, &as); err != nil ||
		as.MaxInflight != 9 || as.Timeout != "10m0s" || as.MaxConns != 256 || !strings.HasPrefix(as.URL, "http://127.0.0.1:") || c.API() != as.URL {
		t.Fatalf("set api %+v %v", as, err)
	}
	if status, _ := newAgent(as.URL, "nope").call(t, "/v1/chat/completions", `{"model":"m"}`, nil); status != 401 {
		t.Fatalf("the API serves: %d", status)
	}
	for _, bad := range []proxy.APISettings{{Address: "10.1.2.3:9301"}, {Address: ":9301"}, {Address: "nope"},
		{Address: "127.0.0.1:0", CertFile: "a.pem"}, {Address: "127.0.0.1:0", CertFile: "/no/such.pem", KeyFile: "/no/such.key"},
		{Address: "127.0.0.1:0", MaxConns: -1}, {Address: "127.0.0.1:0", FirstByteTimeout: "later"}} {
		if err := cp.RPC(proxy.MethodProxyAPISet, bad, nil); llmtest.CodeOf(err) != model.CodeBadRequest {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	// With a certificate, over TLS; localhost counts as loopback.
	cert, err := certtest.Write(t.TempDir(), "localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.RPC(proxy.MethodProxyAPISet, proxy.APISettings{Address: "localhost:0", CertFile: cert.CertFile, KeyFile: cert.KeyFile}, &as); err != nil ||
		!strings.HasPrefix(as.URL, "https://") || as.CertFile != cert.CertFile {
		t.Fatalf("api over TLS: %+v %v", as, err)
	}
	if err := cp.RPC(proxy.MethodProxyAPISet, proxy.APISettings{}, &as); err != nil || as.Address != "" || c.API() != "" ||
		strings.Contains(compact(cf.Current()), `"api"`) {
		t.Fatalf("api off: %+v %v %s", as, err, cf.Current())
	}
}

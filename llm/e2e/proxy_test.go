package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/client"
	"github.com/lesismal/fit/agent/internal/agenttest"
)

// direct runs a node serving models and a client linked to it, and returns
// an agent with a key.
type direct struct {
	mock   *agenttest.MockLLM
	nf, cf *agenttest.Fit
	a      *agent
	cl     *client.Client
}

func startDirect(t *testing.T, models, nodeExtra, apiExtra string) *direct {
	t.Helper()
	d := &direct{mock: agenttest.StartMockLLM(t)}
	models = strings.ReplaceAll(models, "MOCK", d.mock.URL)
	nodeAddr := freeAddr(t)
	d.nf = agenttest.Start(t, member("node", `{"listen": {"address": "`+nodeAddr+`", "password": "`+meshPass+`"}, "models": `+models+nodeExtra+`}`), "")
	d.cf = agenttest.Start(t, member("client", `{"console": `+agenttest.Console()+`, "api": {"address": "127.0.0.1:0"`+apiExtra+`},
		"peers": [{"address": "`+nodeAddr+`", "password": "`+meshPass+`", "role": "node"}]}`), "")
	n, c := nodeOf(t, d.nf), clientOf(t, d.cf)
	d.cl = c
	agenttest.Eventually(t, "the link", func() bool { return c.Mesh.Online(n.Self().ID) })
	p := pages(t, c.Console().URL())
	var key client.KeyCreateResp
	if err := p.RPC(client.MethodClientKeysCreate, client.KeyCreateReq{Name: "test"}, &key); err != nil {
		t.Fatal(err)
	}
	agenttest.Eventually(t, "the catalog", func() bool {
		var cat []client.CatalogModel
		return p.RPC(client.MethodClientModels, nil, &cat) == nil && len(cat) > 0
	})
	d.a = newAgent(c.API(), key.Key)
	return d
}

// Large request bodies: past 1 MiB, a body streams to the node in frames,
// and on to the upstream as it arrives, or whole when the node must set the
// model it names; past the node's or the client's bound, the call fails.
func TestLargeBodies(t *testing.T) {
	d := startDirect(t, `[{"name": "same", "api": "openai", "base_url": "MOCK/u/s/v1", "api_key": "sk-s"},
		{"name": "renamed", "api": "openai", "base_url": "MOCK/u/r/v1", "api_key": "sk-r", "upstream": "up-r"}]`,
		`, "proxy": {"max_body_bytes": 6291456}`, `, "max_body_bytes": 8388608`)
	pad := strings.Repeat("p", 3<<20)
	for _, name := range []string{"same", "renamed"} {
		body := `{"model":"` + name + `","pad":"` + pad + `"}`
		status, resp := d.a.call(t, "/v1/chat/completions", body, nil)
		if status != 200 {
			t.Fatalf("%s: %d %.200s", name, status, resp)
		}
	}
	if st := d.mock.Stats("s"); st.BodyBytes != len(pad)+len(`{"model":"same","pad":""}`) {
		t.Fatalf("the upstream got %d bytes", st.BodyBytes)
	}
	if last := d.mock.Last("r"); last == nil || last.Model != "up-r" {
		t.Fatal("the model was not set upstream")
	}
	// Past the node's 6 MiB.
	big := `{"model":"same","pad":"` + strings.Repeat("p", 7<<20) + `"}`
	if status, resp := d.a.call(t, "/v1/chat/completions", big, nil); status != 400 {
		t.Fatalf("past the node's bound: %d %.200s", status, resp)
	}
	// Past the client's 8 MiB.
	huge := `{"model":"same","pad":"` + strings.Repeat("p", 9<<20) + `"}`
	resp, err := d.a.post("/v1/chat/completions", huge, nil)
	if err == nil {
		if resp.StatusCode != 413 {
			t.Fatalf("past the client's bound: %d", resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// A slow agent holds back a large response without failing it, and gets all
// of it; a fast one too.
func TestSlowAgentLargeResponse(t *testing.T) {
	d := startDirect(t, `[{"name": "m", "api": "openai", "base_url": "MOCK/u/a/v1", "api_key": "sk-a"}]`, "", "")
	const size = 8 << 20
	resp, err := d.a.post("/v1/chat/completions", map[string]any{"model": "m", "big_bytes": size}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	n := 0
	for i := 0; ; i++ {
		k, err := resp.Body.Read(buf)
		n += k
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d bytes: %v", n, err)
		}
		if i < 40 {
			time.Sleep(10 * time.Millisecond) // slow at first, while the windows fill
		}
	}
	if n != size {
		t.Fatalf("got %d of %d bytes", n, size)
	}
}

// A slow upstream's events reach the agent one by one, as they come; an
// upstream slower than the first byte timeout fails the call.
func TestSlowUpstream(t *testing.T) {
	d := startDirect(t, `[{"name": "m", "api": "openai", "base_url": "MOCK/u/a/v1", "api_key": "sk-a"}]`,
		`, "proxy": {"first_byte_timeout": "1s"}`, "")
	resp, err := d.a.post("/v1/chat/completions", map[string]any{"model": "m", "stream": true, "chunks": 3, "chunk_ms": 300}, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	buf := make([]byte, 4096)
	k, err := resp.Body.Read(buf)
	if err != nil || !bytes.Contains(buf[:k], []byte("#0")) {
		t.Fatalf("first read %q %v", buf[:k], err)
	}
	if time.Since(start) > 250*time.Millisecond {
		t.Fatalf("the first event waited %v", time.Since(start))
	}
	rest, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Contains(rest, []byte("[DONE]")) {
		t.Fatalf("rest %q", rest)
	}
	// Past the first byte timeout.
	status, body := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "m", "sleep_ms": 3000}, nil)
	if status != 503 {
		t.Fatalf("a silent upstream: %d %s", status, body)
	}
}

// An agent that goes away mid-response cancels the upstream's request; an
// upstream that breaks mid-body cuts the agent's response short.
func TestCutConnections(t *testing.T) {
	d := startDirect(t, `[{"name": "m", "api": "openai", "base_url": "MOCK/u/a/v1", "api_key": "sk-a"}]`, "", "")
	resp, err := d.a.post("/v1/chat/completions", map[string]any{"model": "m", "stream": true, "chunks": 100, "chunk_ms": 50}, nil)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	agenttest.Eventually(t, "the upstream's request cancelled", func() bool { return d.mock.Stats("a").Canceled == 1 })

	resp, err = d.a.post("/v1/chat/completions", map[string]any{"model": "m", "big_bytes": 4 << 20, "cut_after": 1 << 20}, nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if err == nil || n >= 4<<20 {
		t.Fatalf("a broken upstream body came whole: %d bytes, %v", n, err)
	}
	// The node and the client go on serving.
	if status, _ := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "m"}, nil); status != 200 {
		t.Fatalf("after the cuts: %d", status)
	}
}

// Calls past a model's max_inflight go elsewhere, here nowhere: 503; a
// disabled model, a model of another API, an unreachable upstream.
func TestUnavailable(t *testing.T) {
	d := startDirect(t, `[{"name": "busy", "api": "openai", "base_url": "MOCK/u/a/v1", "api_key": "sk-a", "max_inflight": 1},
		{"name": "off", "api": "openai", "base_url": "MOCK/u/a/v1", "disabled": true},
		{"name": "claude", "api": "anthropic", "base_url": "MOCK/u/a/v1", "api_key": "sk-a"},
		{"name": "down", "api": "openai", "base_url": "http://`+freeAddr(t)+`/v1"}]`, "", "")
	first := make(chan int, 1)
	go func() {
		status, _ := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "busy", "sleep_ms": 1500}, nil)
		first <- status
	}()
	agenttest.Eventually(t, "the first call upstream", func() bool { return d.mock.Stats("a").Calls == 1 })
	if status, body := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "busy"}, nil); status != 503 {
		t.Fatalf("past max_inflight: %d %s", status, body)
	}
	if status := <-first; status != 200 {
		t.Fatalf("the first call: %d", status)
	}
	if status, _ := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "off"}, nil); status != 503 {
		t.Fatalf("disabled: %d", status)
	}
	if status, body := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "claude"}, nil); status != 404 || !strings.Contains(body, "anthropic") {
		t.Fatalf("another API: %d %s", status, body)
	}
	status, body := d.a.call(t, "/v1/messages", map[string]any{"model": "claude"}, map[string]string{"anthropic-beta": "b1"})
	if status != 200 || !strings.Contains(body, `"type":"message"`) {
		t.Fatalf("anthropic: %d %s", status, body)
	}
	if last := d.mock.Last("a"); last.Header["x-api-key"] != "sk-a" || last.Header["anthropic-version"] != "2023-06-01" ||
		last.Header["anthropic-beta"] != "b1" || last.Header["authorization"] != "" {
		t.Fatalf("anthropic headers %+v", last.Header)
	}
	if status, _ := d.a.call(t, "/v1/chat/completions", map[string]any{"model": "down"}, nil); status != 503 {
		t.Fatalf("down: %d", status)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(body), &out)
}

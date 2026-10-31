package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/agent/client"
	"github.com/lesismal/fit/agent/hub"
	"github.com/lesismal/fit/agent/internal/agenttest"
	"github.com/lesismal/fit/agent/node"
)

const meshPass = "e2e-mesh-secret"

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// member wraps a role's member in a config.
func member(role, body string) string { return `{"agent": {"` + role + `": ` + body + `}}` }

func nodeOf(t *testing.T, f *agenttest.Fit) *node.Node {
	t.Helper()
	n, _ := f.Role("node").(*node.Node)
	if n == nil {
		t.Fatal("no node running")
	}
	return n
}

func hubOf(t *testing.T, f *agenttest.Fit) *hub.Role {
	t.Helper()
	h, _ := f.Role("hub").(*hub.Role)
	if h == nil {
		t.Fatal("no hub running")
	}
	return h
}

func clientOf(t *testing.T, f *agenttest.Fit) *client.Client {
	t.Helper()
	c, _ := f.Role("client").(*client.Client)
	if c == nil {
		t.Fatal("no client running")
	}
	return c
}

// pages logs in to the pages of a role.
func pages(t *testing.T, url string) *agenttest.Browser { return agenttest.Login(t, url) }

// agent calls the client's API.
type agent struct {
	base, key string
	hc        *http.Client
}

func newAgent(base, key string) *agent {
	return &agent{base: strings.TrimSuffix(base, "/"), key: key, hc: &http.Client{Timeout: 60 * time.Second}}
}

func (a *agent) post(path string, body any, hdr map[string]string) (*http.Response, error) {
	var rd io.Reader
	switch b := body.(type) {
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest("POST", a.base+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if a.key != "" {
		req.Header.Set("Authorization", "Bearer "+a.key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return a.hc.Do(req)
}

// call posts and returns the status and the whole body.
func (a *agent) call(t *testing.T, path string, body any, hdr map[string]string) (int, string) {
	t.Helper()
	resp, err := a.post(path, body, hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// events reads an SSE body's data lines as they arrive.
func events(t *testing.T, r io.Reader) []string {
	t.Helper()
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			out = append(out, d)
		}
	}
	return out
}

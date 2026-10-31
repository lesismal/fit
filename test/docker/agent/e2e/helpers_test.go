//go:build e2e

// Package e2e is the docker end-to-end test of fit's agent. It runs in the
// tester container of test/docker/agent/docker-compose.yml against real fit
// processes, one per role (hub, node1-3, client1-2), and the mock upstream
// mockllm, and is driven by test/docker/agent/e2e.sh: TestE2E configures and
// exercises everything through the roles' pages and the clients' APIs, the
// script restarts every role, then TestPersist checks what must survive a
// restart (the config each role's pages changed, kept in its store).
package e2e

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Addresses inside the compose network.
const (
	upstreamURL = "http://upstream:8000"
	meshPass    = "e2e-mesh-secret"

	hubListen     = ":9201"
	node2Listen   = ":9101"
	client2Listen = ":9301"
)

// password logs in to every role's pages as admin: configs/*.json hold its
// bcrypt hash.
const password = "fit-e2e-pass"

// The pages and the client APIs are served over TLS with certgen's
// self-signed certificate, which the test does not verify.
var transport = &http.Transport{
	TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
	MaxIdleConnsPerHost: 8,
	ForceAttemptHTTP2:   false,
}

func httpClient(jar http.CookieJar) *http.Client {
	return &http.Client{Transport: transport, Jar: jar, Timeout: 120 * time.Second}
}

// ---- pages ----

type console struct {
	name, role, base string
	hc               *http.Client
	csrf             string // the session's token, sent as X-Fit-Csrf
	id               string // the role's stable id, filled by login
}

var consoles = map[string]*console{}

func newConsole(name, role, host string) *console {
	jar, _ := cookiejar.New(nil)
	return &console{name: name, role: role, base: "https://" + host, hc: httpClient(jar)}
}

func init() {
	for _, c := range []*console{
		newConsole("hub", "hub", "hub:9200"),
		newConsole("node1", "node", "node1:9100"), newConsole("node2", "node", "node2:9100"), newConsole("node3", "node", "node3:9100"),
		newConsole("client1", "client", "client1:9300"), newConsole("client2", "client", "client2:9300"),
	} {
		consoles[c.name] = c
	}
}

func con(name string) *console { return consoles[name] }

// apiErr is the pages' answer to a request that failed.
type apiErr struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

// do sends a request to the pages with the session's CSRF token and returns
// the status and the raw body. hdr sets headers; an empty value deletes one.
func (c *console) do(method, path string, body any, hdr map[string]string) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.csrf != "" {
		req.Header.Set("X-Fit-Csrf", c.csrf)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

// login logs in as user admin and keeps the session's cookie and token.
func (c *console) login(pass string) (int, error) {
	st, raw, err := c.do("POST", "/api/login", map[string]string{"username": "admin", "password": pass}, nil)
	if err != nil {
		return st, err
	}
	if st != 200 {
		return st, fmt.Errorf("login %s: HTTP %d %s", c.name, st, raw)
	}
	var s struct {
		User string `json:"user"`
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.User != "admin" || s.CSRF == "" {
		return st, fmt.Errorf("login %s: %s", c.name, raw)
	}
	c.csrf = s.CSRF
	return st, nil
}

// rpcErr calls a method and returns the status and the raw answer (no check).
func (c *console) rpcErr(method string, req any) (int, []byte) {
	if req == nil {
		req = struct{}{}
	}
	st, raw, err := c.do("POST", "/api/rpc"+method, req, nil)
	if err != nil {
		return 0, []byte(err.Error())
	}
	return st, raw
}

// rpc calls a method that must succeed and decodes its result into out (when
// not nil).
func (c *console) rpc(t testing.TB, method string, req, out any) {
	t.Helper()
	st, raw := c.rpcErr(method, req)
	if st != 200 {
		t.Fatalf("%s %s: HTTP %d: %s", c.name, method, st, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode %s: %v", c.name, method, raw, err)
		}
	}
}

// rpcFail calls a method that must fail with HTTP status want and returns
// the error the pages answered.
func (c *console) rpcFail(t testing.TB, method string, req any, want int) apiErr {
	t.Helper()
	st, raw := c.rpcErr(method, req)
	var e apiErr
	_ = json.Unmarshal(raw, &e)
	if st != want || e.Error == "" || e.Code == 0 {
		t.Fatalf("%s %s %v: want HTTP %d with an error, got HTTP %d: %s", c.name, method, req, want, st, raw)
	}
	return e
}

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	Event string
	Data  string
}

// readSSE reads events until EOF or until fn returns false.
func readSSE(r io.Reader, fn func(ev sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var ev sseEvent
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 || ev.Event != "" {
				ev.Data = strings.Join(data, "\n")
				if !fn(ev) {
					return nil
				}
			}
			ev, data = sseEvent{}, nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			ev.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return sc.Err()
}

// stream opens a stream method of the pages and collects its events (until
// fn returns false; fn may be nil).
func (c *console) stream(t testing.TB, method string, req any, fn func(ev sseEvent) bool) []sseEvent {
	t.Helper()
	b, _ := json.Marshal(req)
	hr, _ := http.NewRequest("POST", c.base+"/api/stream"+method, bytes.NewReader(b))
	hr.Header.Set("X-Fit-Csrf", c.csrf)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(hr)
	if err != nil {
		t.Fatalf("%s stream %s: %v", c.name, method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s stream %s: HTTP %d %s: %.300s", c.name, method, resp.StatusCode, resp.Header.Get("Content-Type"), raw)
	}
	var evs []sseEvent
	err = readSSE(resp.Body, func(ev sseEvent) bool {
		evs = append(evs, ev)
		return fn == nil || fn(ev)
	})
	if err != nil {
		t.Fatalf("%s stream %s: read: %v", c.name, method, err)
	}
	return evs
}

// ---- the client's agent API ----

// apiHost is the host and port of a client's agent API.
var apiHost = map[string]string{"client1": "client1:9400", "client2": "client2:9400"}

type agentResp struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r agentResp) json(t testing.TB) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("response is not JSON (HTTP %d): %.300s", r.Status, r.Body)
	}
	return m
}

// agent calls a client's agent API (/v1/...) like an SDK would.
func agent(t testing.TB, client, method, path string, body any, hdr map[string]string) agentResp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, "https://"+apiHost[client]+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := httpClient(nil).Do(req)
	if err != nil {
		t.Fatalf("%s %s %s: %v", client, method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return agentResp{Status: resp.StatusCode, Header: resp.Header, Body: raw}
}

func bearer(key string) map[string]string { return map[string]string{"Authorization": "Bearer " + key} }

// chat sends a non-streaming chat completion and requires HTTP 200; it
// returns the serving node.
func chat(t testing.TB, client, key, mdl string) string {
	t.Helper()
	r := agent(t, client, "POST", "/v1/chat/completions", map[string]any{"model": mdl,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}}}, bearer(key))
	if r.Status != 200 {
		t.Fatalf("%s chat %s: HTTP %d: %s", client, mdl, r.Status, r.Body)
	}
	return r.Header.Get("X-Agenthub-Node")
}

// ---- mock upstream ----

type upstreamReq struct {
	Path   string            `json:"path"`
	Model  string            `json:"model"`
	Header map[string]string `json:"header"`
	Body   json.RawMessage   `json:"body"`
}

type upstreamStats struct {
	Calls     int  `json:"calls"`
	BodyBytes int  `json:"body_bytes"`
	Checks    int  `json:"checks"`
	Canceled  int  `json:"canceled"`
	Down      bool `json:"down"`
}

func upstreamAdmin(t testing.TB, method, path string, out any) {
	t.Helper()
	req, _ := http.NewRequest(method, upstreamURL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upstream %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("upstream %s: HTTP %d %s", path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("upstream %s: %v", path, err)
		}
	}
}

func upstreamLast(t testing.TB, tag string) upstreamReq {
	var r upstreamReq
	upstreamAdmin(t, "GET", "/admin/"+tag+"/last", &r)
	return r
}

func upstreamStatsOf(t testing.TB, tag string) upstreamStats {
	var s upstreamStats
	upstreamAdmin(t, "GET", "/admin/"+tag+"/stats", &s)
	return s
}

// ---- waiting ----

// eventually retries fn until it returns nil or timeout passes.
func eventually(t testing.TB, timeout time.Duration, what string, fn func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := fn()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout after %v waiting for %s: %v", timeout, what, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ---- types used by the test (a subset of fit/agent/internal/model and fit/agent/client) ----

type identity struct {
	Role      string `json:"role"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	StartedAt int64  `json:"started_at"`
}

type peerConn struct {
	identity
	Direction string `json:"direction"`
	Draining  bool   `json:"draining"`
}

type dialTarget struct {
	Addr      string    `json:"addr"`
	Role      string    `json:"role"`
	Connected bool      `json:"connected"`
	Peer      *identity `json:"peer"`
	Error     string    `json:"error"`
}

type peersResp struct {
	Self   identity `json:"self"`
	Listen struct {
		Addr        string `json:"addr"`
		HasPassword bool   `json:"has_password"`
		Error       string `json:"error"`
	} `json:"listen"`
	Conns   []peerConn   `json:"conns"`
	Targets []dialTarget `json:"targets"`
}

type modelView struct {
	Name        string `json:"name"`
	API         string `json:"api"`
	State       string `json:"state"`
	Error       string `json:"error"`
	Inflight    int    `json:"inflight"`
	MaxInflight int    `json:"max_inflight"`
}

type nodeInfo struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Models      []modelView `json:"models"`
	Jobs        int         `json:"jobs"`
	HubManage   bool        `json:"hub_manage"`
	Seq         int64       `json:"seq"`
}

type hubNode struct {
	Info     nodeInfo `json:"info"`
	Online   bool     `json:"online"`
	LastSeen int64    `json:"last_seen"`
}

type hubModel struct {
	Name  string   `json:"name"`
	API   string   `json:"api"`
	Nodes []string `json:"nodes"`
	Ready int      `json:"ready"`
}

type hubModels struct {
	Hub    string     `json:"hub"`
	Models []hubModel `json:"models"`
	Seq    int64      `json:"seq"`
}

type mdl struct {
	Name        string `json:"name"`
	API         string `json:"api"`
	BaseURL     string `json:"base_url"`
	Upstream    string `json:"upstream,omitempty"`
	APIKey      string `json:"api_key,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Description string `json:"description,omitempty"`
	MaxInflight int    `json:"max_inflight,omitempty"`
}

type job struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	ExitCode  *int   `json:"exit_code"`
	Error     string `json:"error"`
	Output    string `json:"output"`
	Offset    int    `json:"offset"`
	OutputLen int    `json:"output_len"`
}

// nodeSettings are a node's own settings: durations as Go strings ("30s",
// "1h0m0s"); empty sets the default.
type nodeSettings struct {
	AllowHubManage  bool   `json:"allow_hub_manage"`
	GPUSudo         bool   `json:"gpu_sudo"`
	RefreshInterval string `json:"refresh_interval"`
	JobTimeout      string `json:"job_timeout"`
	GPUDataDir      string `json:"gpu_data_dir,omitempty"`
}

type route struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Ready      bool   `json:"ready"`
	Nodes      int    `json:"nodes"`
	ReadyNodes int    `json:"ready_nodes"`
}

type catalogModel struct {
	Name   string  `json:"name"`
	API    string  `json:"api"`
	Ready  bool    `json:"ready"`
	Routes []route `json:"routes"`
}

type keyView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	LastUsedAt int64  `json:"last_used_at"`
	Calls      int64  `json:"calls"`
	Key        string `json:"key"`
}

type modelStats struct {
	Model  string `json:"model"`
	Calls  int64  `json:"calls"`
	Errors int64  `json:"errors"`
	Bytes  int64  `json:"bytes"`
}

type statsResp struct {
	Total  modelStats   `json:"total"`
	Models []modelStats `json:"models"`
}

// ---- helpers over the types ----

func (c *console) peers(t testing.TB) peersResp {
	t.Helper()
	var p peersResp
	c.rpc(t, "/mesh/peers", nil, &p)
	return p
}

func (c *console) hubNodes(t testing.TB) map[string]hubNode {
	t.Helper()
	var ns []hubNode
	c.rpc(t, "/hub/nodes", nil, &ns)
	m := map[string]hubNode{}
	for _, n := range ns {
		m[n.Info.ID] = n
	}
	return m
}

func (c *console) hubModel(t testing.TB, name, api string) *hubModel {
	t.Helper()
	var hm hubModels
	c.rpc(t, "/hub/models", nil, &hm)
	for i := range hm.Models {
		if hm.Models[i].Name == name && hm.Models[i].API == api {
			return &hm.Models[i]
		}
	}
	return nil
}

func (c *console) catalog(t testing.TB, name, api string) *catalogModel {
	t.Helper()
	var cms []catalogModel
	c.rpc(t, "/client/models", nil, &cms)
	for i := range cms {
		if cms[i].Name == name && cms[i].API == api {
			return &cms[i]
		}
	}
	return nil
}

// waitJob waits until a job leaves running and returns it with its whole
// output.
func waitJob(t testing.TB, call func(method string, req, out any), id int64, timeout time.Duration) job {
	t.Helper()
	var j job
	eventually(t, timeout, fmt.Sprintf("job %d to finish", id), func() error {
		call("/node/job/get", map[string]any{"id": id, "offset": 0}, &j)
		if j.Status == "running" {
			return fmt.Errorf("still running, %d bytes of output", j.OutputLen)
		}
		return nil
	})
	return j
}

// ---- state shared between TestE2E and TestPersist ----

type shared struct {
	Key  string            `json:"key"`  // a client1 API key
	Key2 string            `json:"key2"` // a client2 API key
	IDs  map[string]string `json:"ids"`  // console name -> role id
}

func sharedPath() string {
	dir := os.Getenv("FIT_E2E_SHARED")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "e2e-state.json")
}

func saveShared(t testing.TB, s shared) {
	b, _ := json.Marshal(s)
	if err := os.WriteFile(sharedPath(), b, 0o600); err != nil {
		t.Fatalf("save shared state: %v", err)
	}
}

func loadShared(t testing.TB) shared {
	var s shared
	b, err := os.ReadFile(sharedPath())
	if err != nil {
		t.Fatalf("no state from TestE2E (run it first): %v", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("shared state: %v", err)
	}
	return s
}

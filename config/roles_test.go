package config

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/internal/sysmem"
)

// TestLLMChecks checks what the LLM feature's roles refuse beyond decoding:
// every member's own error, each in a config of its own.
func TestLLMChecks(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	model := func(m string) string {
		return `{"llm": {"node": {"models": [` + m + `]}}}`
	}
	peers := make([]string, MaxLLMPeers+1)
	for i := range peers {
		peers[i] = `{"address": "h:` + strconv.Itoa(1000+i) + `", "password": "p", "role": "hub"}`
	}
	keys := make([]string, MaxLLMKeys+1)
	for i := range keys {
		keys[i] = `{"id": "x", "name": "k", "hash": "y"}`
	}
	models := make([]string, MaxLLMModels+1)
	for i := range models {
		models[i] = `{"name": "m` + strconv.Itoa(i) + `", "api": "openai", "base_url": "http://h"}`
	}
	for _, bad := range []struct{ doc, want string }{
		{`{"llm": {"hub": {"id": "a b"}}}`, "the ID is up to 128 characters"},
		{`{"llm": {"hub": {"listen": {"address": ":9200", "password": ""}}}}`, "a listener needs a password"},
		{`{"llm": {"node": {"peers": [` + strings.Join(peers, ",") + `]}}}`, "at most 64 peers"},
		{`{"llm": {"hub": {"peers": [{"address": "h:1", "password": "", "role": "node"}]}}}`, "a peer needs its password"},
		{`{"llm": {"hub": {"limits": {"ping_interval": "10ms"}}}}`, "ping_interval"},
		{`{"llm": {"hub": {"limits": {"slow_timeout": "-1s"}}}}`, "slow_timeout"},
		{`{"llm": {"hub": {"limits": {"stream_idle": "10ms"}}}}`, "stream_idle"},
		{model(`{"name": "a", "api": "openai", "base_url": "http://h", "upstream": "a b"}`), "upstream model ID"},
		{model(`{"name": "a", "api": "openai", "base_url": "http://h", "api_key": "a b"}`), "API key"},
		{model(`{"name": "a", "api": "openai", "base_url": "http://h", "description": "` + long(1025) + `"}`), "description is longer"},
		{`{"llm": {"node": {"description": "` + long(1025) + `"}}}`, "description is longer"},
		{`{"llm": {"node": {"models": [` + strings.Join(models, ",") + `]}}}`, "at most 256 models"},
		{`{"llm": {"node": {"settings": {"job_timeout": "1s"}}}}`, "job_timeout is 10s to 168h"},
		{`{"llm": {"proxy": {"api": {"address": "nope"}}}}`, "nope"},
		{`{"llm": {"proxy": {"api": {"address": "127.0.0.1:9300", "cert_file": "c"}}}}`, "cert_file and key_file go together"},
		{`{"llm": {"proxy": {"api_keys": [` + strings.Join(keys, ",") + `]}}}`, "at most 256 keys"},
		{`{"llm": {"proxy": {"api_keys": [{"id": "0123456789abcdef", "name": "k", "hash": "` + long(64) + `"},
			{"id": "0123456789abcdef", "name": "l", "hash": "` + long(64) + `"}]}}}`, "listed twice"},
		{`{"llm": {"proxy": {"api_keys": [{"id": "0123456789abcdef", "name": " ", "hash": "` + long(64) + `"}]}}}`, "the name needs 1 to 64"},
		{`{"llm": {"proxy": {"api_keys": [{"id": "0123456789abcdef", "name": "k", "prefix": "` + long(17) + `", "hash": "` + long(64) + `"}]}}}`, "the prefix is up to 16"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%.120s: %v, want %q", bad.doc, err, bad.want)
		}
	}
}

// TestLLMChecksUndecoded checks what decoding refuses before the checks
// see it, as the pages may hand it to them: a model's max_inflight out of
// range, an api of neither kind, and a negative proxy timeout.
func TestLLMChecksUndecoded(t *testing.T) {
	if f, msg := CheckLLMModel(&LLMModel{Name: "a", API: "openai", BaseURL: "http://h", MaxInflight: -1}); f != "max_inflight" || msg == "" {
		t.Errorf("max_inflight -1: %q %q", f, msg)
	}
	if f, _ := CheckLLMModel(&LLMModel{Name: "a", API: "palm", BaseURL: "http://h"}); f != "api" {
		t.Errorf("api palm: %q", f)
	}
	for _, api := range LLMAPIs {
		m := LLMModel{Name: "a", API: api, BaseURL: "http://h"}
		if api == "azure_openai" {
			m.APIVersion = "2024-10-21"
		}
		if f, msg := CheckLLMModel(&m); msg != "" {
			t.Errorf("api %s: %s %s", api, f, msg)
		}
	}
	var got []string
	add := func(_ Value, format string, args ...any) { got = append(got, format) }
	checkLLMNode(&LLMNode{Proxy: &LLMNodeProxy{ConnectTimeout: -1}}, add)
	if len(got) != 1 || !strings.Contains(got[0], "is negative") {
		t.Errorf("negative connect_timeout: %q", got)
	}
}

// TestLLMDefaults checks the LLM feature's settings fall back to their
// defaults, on a nil struct as on one left empty, and the peer roles of a
// role.
func TestLLMDefaults(t *testing.T) {
	for _, l := range []*LLMLimits{nil, {}} {
		if l.Conns() != DefaultLLMMaxConns || l.Inflight() != DefaultLLMMaxInflight ||
			l.Ping() != DefaultLLMPingInterval.Std() || l.Slow() != DefaultLLMSlowTimeout.Std() ||
			l.Retry() != DefaultLLMRetryInterval.Std() || l.Idle() != DefaultLLMStreamIdle.Std() {
			t.Errorf("%+v: not the defaults", l)
		}
	}
	l := &LLMLimits{MaxConns: 3, MaxInflight: 4, PingInterval: Duration(time.Second)}
	if l.Conns() != 3 || l.Inflight() != 4 || l.Ping() != time.Second {
		t.Errorf("%+v: not its own", l)
	}
	a := &LLMProxyAPI{}
	if a.Conns() != DefaultLLMProxyMaxConns || a.Inflight() != DefaultLLMProxyMaxInflight {
		t.Errorf("%+v: not the defaults", a)
	}
	a = &LLMProxyAPI{MaxConns: 5, MaxInflight: 6}
	if a.Conns() != 5 || a.Inflight() != 6 {
		t.Errorf("%+v: not its own", a)
	}
	for role, want := range map[string]string{"hub": "node proxy", "node": "hub proxy", "proxy": "hub node", "x": ""} {
		if got := strings.Join(LLMPeerRoles(role), " "); got != want {
			t.Errorf("LLMPeerRoles(%q) = %q, want %q", role, got, want)
		}
	}
}

// TestTunnelChecks checks what the tunnel's roles refuse beyond decoding.
func TestTunnelChecks(t *testing.T) {
	up := func(u string) string { return `{"tunnel": {"node": {"hubs": [` + u + `]}}}` }
	for _, bad := range []struct{ doc, want string }{
		{`{"tunnel": {"hub": {"name": " s"}}}`, "is not a name"},
		{`{"tunnel": {"hub": {"listeners": [{"protocol": "tcp", "address": "nope"}]}}}`, "nope"},
		{`{"tunnel": {"hub": {"listeners": [{"protocol": "ws", "address": ":7000", "path": "x"}]}}}`, "does not start with '/'"},
		{`{"tunnel": {"hub": {"password": "p", "console_socket": {"address": "nope"}}}}`, "nope"},
		{`{"tunnel": {"hub": {"console_socket": {"address": ":7001"}}}}`, "a password is needed"},
		{`{"tunnel": {"node": {"name": "a/b"}}}`, "is not a name"},
		{`{"tunnel": {"node": {"services": [{"name": "", "address": "h:22"}]}}}`, "the name is empty"},
		{`{"tunnel": {"node": {"services": [{"name": "a/b", "address": "h:22"}]}}}`, "is not a name"},
		{`{"tunnel": {"proxy": {"name": " c"}}}`, "is not a name"},
		{up(`{"name": " s", "endpoints": [{"address": "h:1"}], "password": "p"}`), "is not a name"},
		{up(`{"name": "s", "endpoints": [], "password": "p"}`), "list at least one endpoint"},
		{up(`{"endpoints": [{"address": "h:1"}, {"address": "h:1"}], "password": "p"}`), "listed twice"},
		{up(`{"endpoints": [{"address": "h:1"}], "password": ""}`), "the password is empty"},
		{up(`{"endpoints": [{"address": "h:0"}], "password": "p"}`), "not a host and port to dial"},
		{up(`{"endpoints": [{"address": "ws://:1/x"}], "password": "p"}`), "not a ws:// URL"},
		{up(`{"endpoints": [{"address": "quic://h"}], "password": "p"}`), "not a quic:// URL"},
		{up(`{"endpoints": [{"address": "quic://h:1/x"}], "password": "p"}`), "has a path"},
		{`{"tunnel": {"proxy": {"forwards": [{"listen": "nope", "node": "n", "service": "s"}]}}}`, "nope"},
		{`{"tunnel": {"proxy": {"forwards": [{"listen": "127.0.0.1:0", "node": "", "service": "s"}]}}}`, "the node is empty"},
		{`{"tunnel": {"proxy": {"forwards": [{"listen": "127.0.0.1:0", "node": "n", "service": ""}]}}}`, "the service is empty"},
		{`{"tunnel": {"proxy": {"web_proxies": [{"listen": "127.0.0.1:0", "url": "ws://h:1/", "password": ""}]}}}`, "the password is empty"},
		{`{"tunnel": {"hub": {"console": {"address": "nope", "users": []}}}}`, "nope"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	// Two forwards on port 0 each pick a free port: not the same listener.
	good := `{"tunnel": {"proxy": {"forwards": [{"listen": "127.0.0.1:0", "node": "n", "service": "s"},
		{"listen": "127.0.0.1:0", "node": "n", "service": "s"}]}}}`
	if _, err := Load([]byte(good)); err != nil {
		t.Fatal(err)
	}
}

// TestTunnelNames checks a role left unnamed takes the host's name, and an
// uplink with neither a name nor an endpoint has none.
func TestTunnelNames(t *testing.T) {
	want := "fit"
	if h, err := os.Hostname(); err == nil && h != "" {
		want = h
	}
	if got := tunnelHostName(); got != want {
		t.Errorf("tunnelHostName() = %q, want %q", got, want)
	}
	if (&TunnelHub{}).HubName() != want || (&TunnelNode{}).NodeName() != want || (&TunnelProxy{}).ProxyName() != want {
		t.Error("an unnamed role does not take the host's name")
	}
	if (&TunnelUplink{}).HubName() != "" {
		t.Error("an uplink without endpoints has a name")
	}
	if (TunnelListener{Protocol: TunnelWS, Path: "/x"}).WSPath() != "/x" {
		t.Error("a ws listener's own path is not kept")
	}
}

// TestMemoryUnread checks a share of memory is its fallback where the
// memory cannot be read.
func TestMemoryUnread(t *testing.T) {
	old := sysmem.Total
	sysmem.Total = func() uint64 { return 0 }
	defer func() { sysmem.Total = old }()
	if got := MemoryPercent(10).Bytes(42); got != 42 {
		t.Errorf("Bytes = %d, want the fallback 42", got)
	}
}

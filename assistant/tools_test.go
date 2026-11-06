package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/assistant/model"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// toolsAssistant is an assistant whose tools read the fake Admin API, and
// a config of cfg.
func toolsAssistant(t *testing.T, cfg *config.Config, opts ...func(*Options)) *Assistant {
	t.Helper()
	schema, _ := os.ReadFile("../schema/fit.schema.json")
	o := Options{Settings: &config.Assistant{Data: &config.AssistantData{SendLogs: true, SendAccessLog: true}}, API: fakeAdmin(),
		Config: func() *config.Config { return cfg }, Schema: schema,
		SchemaZh: func() map[string]string {
			return map[string]string{"How requests are matched, and what handles them.": "请求如何匹配，由谁处理。"}
		}}
	for _, fn := range opts {
		fn(&o)
	}
	a, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// run calls the tool name with args, as the loop does.
func run(t *testing.T, a *Assistant, name, args string) (string, error) {
	t.Helper()
	tool := a.Registry().Tool(name)
	if tool == nil {
		t.Fatalf("no tool %s", name)
	}
	return tool.Run(&Call{Context: context.Background(), Args: json.RawMessage(args), User: "ada", Level: "read", Settings: a.Settings(), a: a})
}

func mustRun(t *testing.T, a *Assistant, name, args string) string {
	t.Helper()
	out, err := run(t, a, name, args)
	if err != nil {
		t.Fatalf("%s %s: %v", name, args, err)
	}
	return out
}

func wantErr(t *testing.T, a *Assistant, name, args, want string) {
	t.Helper()
	if _, err := run(t, a, name, args); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s %s: %v, want %q", name, args, err, want)
	}
}

func TestToolConfigGet(t *testing.T) {
	a := toolsAssistant(t, &config.Config{})
	out := mustRun(t, a, "config_get", `{}`)
	for _, s := range []string{secretHash, secretToken, secretJWT, "MHcCAQEEIBzKey", `"assistant"`, `"users"`, "2019"} {
		if strings.Contains(out, s) {
			t.Errorf("the config carries %q: %s", s, out)
		}
	}
	if !strings.Contains(out, `"secret":"[redacted]"`) || !strings.Contains(out, `"api_keys":["[redacted]"]`) || !strings.Contains(out, `"console":{"address":":9000"}`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "config_get", `{"source":"effective","members":["upstreams"]}`); out != `{"upstreams":[{"name":"api","targets":[{"address":"10.0.0.7:9000"}]}]}` {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "config_get", `{"members":["listeners"],"format":"yaml"}`); !strings.HasPrefix(out, "yaml of {\"listeners\"") {
		t.Errorf("%s", out)
	}
	wantErr(t, a, "config_get", `{"source":"x"}`, "base or effective")
	wantErr(t, a, "config_get", `{"format":"x"}`, "json, conf or yaml")
	wantErr(t, a, "config_get", `{"members":["nope"]}`, `no member "nope"`)
	wantErr(t, a, "config_get", `{"members":"x"}`, "not what the tool takes")

	// The Admin API's answers that are not what they should be.
	bad := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/config":
				_, _ = w.Write([]byte(`[1`))
			case "/config/effective":
				_, _ = w.Write([]byte(`{}`))
			case "/config/render":
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"no render"}`))
			default:
				w.WriteHeader(500)
				_, _ = w.Write([]byte(`plain failure`))
			}
		})
	})
	wantErr(t, bad, "config_get", `{}`, "not JSON")
	wantErr(t, bad, "config_get", `{"source":"effective"}`, "not JSON")
	wantErr(t, bad, "status_get", `{}`, "500: plain failure")
	wantErr(t, bad, "cache_zones", `{}`, "500")
	wantErr(t, bad, "consistency_check", `{}`, "500")
	wantErr(t, bad, "versions_diff", `{}`, "500")
	wantErr(t, bad, "metrics_query", `{}`, "500")
	wantErr(t, bad, "upstream_health", `{}`, "500")
	wantErr(t, bad, "certs_list", `{}`, "500")
	none := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = nil })
	wantErr(t, none, "config_get", `{}`, "not reachable")
	big := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(make([]byte, maxAPIBytes))
			_, _ = w.Write([]byte("x"))
		})
	})
	wantErr(t, big, "config_get", `{}`, "larger than")
	// An answer with no body is a 200.
	empty := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}) })
	wantErr(t, empty, "config_get", `{}`, "not JSON")
	if _, err := a.api(context.Background(), "GET", "/\x7f", "", "", nil); err == nil {
		t.Error("a bad path")
	}
}

func TestToolSchemaDescribe(t *testing.T) {
	a := toolsAssistant(t, &config.Config{})
	out := mustRun(t, a, "schema_describe", `{"path":"routes"}`)
	if !strings.Contains(out, `"description":"How requests are matched, and what handles them."`) || !strings.Contains(out, `"name":"match"`) || !strings.Contains(out, `"type":"array"`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "schema_describe", `{"path":"routes","lang":"zh"}`); !strings.Contains(out, "请求如何匹配") {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "schema_describe", `{"path":"admin.assistant.limits.max_turns"}`); !strings.Contains(out, `"default":40`) || !strings.Contains(out, `"type":"integer"`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "schema_describe", `{}`); !strings.Contains(out, `"name":"listeners"`) {
		t.Errorf("%.300s", out)
	}
	wantErr(t, a, "schema_describe", `{"path":"routes.nope"}`, `no member "nope"`)
	wantErr(t, a, "schema_describe", `[`, "not what the tool takes")
	no := toolsAssistant(t, &config.Config{}, func(o *Options) { o.Schema = nil })
	wantErr(t, no, "schema_describe", `{}`, "not loaded")
	bad := toolsAssistant(t, &config.Config{}, func(o *Options) { o.Schema = []byte("x"); o.SchemaZh = nil })
	wantErr(t, bad, "schema_describe", `{}`, "not JSON")
}

func TestToolConfigValidate(t *testing.T) {
	a := toolsAssistant(t, &config.Config{})
	if out := mustRun(t, a, "config_validate", `{"text":"{\"listeners\":[{\"name\":\"w\",\"address\":\":8080\"}]}"}`); out != `{"valid":true}` {
		t.Errorf("%s", out)
	}
	out := mustRun(t, a, "config_validate", `{"text":"listeners: 3\n","format":"yaml"}`)
	if !strings.Contains(out, `"valid":false`) || !strings.Contains(out, `"path":"listeners"`) || !strings.Contains(out, `"line":1`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "config_validate", `{"text":"listeners {","format":"conf"}`); !strings.Contains(out, `"valid":false`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "config_validate", `{"patch":{"routes":[],"tls":null}}`); !strings.Contains(out, `"valid":`) {
		t.Errorf("%s", out)
	}
	wantErr(t, a, "config_validate", `{}`, "either text or patch")
	wantErr(t, a, "config_validate", `{"text":"x","format":"toml"}`, "conf, yaml or json")
	wantErr(t, a, "config_validate", `{"patch":[1]}`, "not a JSON object")
	wantErr(t, a, "config_validate", `{"text":"`+strings.Repeat("x", maxToolTextBytes+1)+`"}`, "larger than")
	wantErr(t, a, "config_validate", `{"text":3}`, "not what the tool takes")

	// The validation given, with many errors.
	many := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.Validate = func([]byte) error {
			var errs config.Errors
			for range 60 {
				errs = append(errs, &config.Error{Path: "x", Msg: "bad"})
			}
			return errs
		}
	})
	if out := mustRun(t, many, "config_validate", `{"text":"{}"}`); !strings.Contains(out, "and 10 more") {
		t.Errorf("%s", out)
	}
	plain := toolsAssistant(t, &config.Config{}, func(o *Options) { o.Validate = func([]byte) error { return errors.New("compiling: no") } })
	if out := mustRun(t, plain, "config_validate", `{"text":"{}"}`); !strings.Contains(out, `"msg":"compiling: no"`) {
		t.Errorf("%s", out)
	}
	badBase := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	})
	wantErr(t, badBase, "config_validate", `{"patch":{}}`, "not a JSON object")
	none := toolsAssistant(t, &config.Config{}, func(o *Options) { o.API = nil })
	wantErr(t, none, "config_validate", `{"patch":{}}`, "not reachable")
}

func TestToolVersionsDiff(t *testing.T) {
	a := toolsAssistant(t, &config.Config{})
	if out := mustRun(t, a, "versions_diff", `{}`); !strings.Contains(out, `"current":3`) {
		t.Errorf("%s", out)
	}
	out := mustRun(t, a, "versions_diff", `{"from":2}`)
	var d struct {
		From, To uint64
		Changes  []change
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.From != 2 || d.To != 3 {
		t.Fatalf("%s", out)
	}
	ops := map[string]string{}
	for _, c := range d.Changes {
		ops[c.Path] = c.Op
	}
	if ops["listeners[0].address"] != "changed" || ops["old"] != "removed" || ops["upstreams"] != "added" || ops["routes"] != "changed" || strings.Contains(out, secretHash) || strings.Contains(out, "users") {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "versions_diff", `{"from":3,"to":3}`); !strings.Contains(out, `"changes":[]`) {
		t.Errorf("%s", out)
	}
	wantErr(t, a, "versions_diff", `{"from":9}`, "not kept")
	wantErr(t, a, "versions_diff", `{"from":2,"to":9}`, "not kept")
	wantErr(t, a, "versions_diff", `{"from":"x"}`, "not what the tool takes")

	// Past the bound of changes.
	var out2 []change
	from, to := map[string]any{}, map[string]any{}
	for i := range maxDiffEntries + 10 {
		to[strings.Repeat("k", i%5+1)+string(rune('a'+i%26))+string(rune('a'+i/26%26))] = i
	}
	diffJSON("", from, to, &out2)
	if len(out2) != maxDiffEntries+1 {
		t.Errorf("%d changes", len(out2))
	}
	textA := toolsAssistant(t, &config.Config{}, func(o *Options) {
		o.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/versions" {
				_, _ = w.Write([]byte(`{"current":2}`))
				return
			}
			_, _ = w.Write([]byte(`not json`))
		})
	})
	wantErr(t, textA, "versions_diff", `{"from":1}`, "not JSON")
}

func TestToolsRuntime(t *testing.T) {
	cfg, err := config.Load([]byte(`{"upstreams":[{"name":"api","targets":[{"address":"10.0.0.7:9000"}]}],"llm":{"hub":{"address":":7100"}},"streaming":{"rtmp":{"address":":1935"}}}`))
	if err != nil {
		t.Logf("a config that does not load (%v): a smaller one", err)
		cfg = &config.Config{}
	}
	a := toolsAssistant(t, cfg)
	out := mustRun(t, a, "status_get", `{}`)
	if !strings.Contains(out, `"uptime_seconds"`) || !strings.Contains(out, `"engine"`) || !strings.Contains(out, `{"backends":1,"in_flight":0,"name":"api","up":0}`) || strings.Contains(out, "certificates") {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "upstream_health", `{}`); !strings.Contains(out, `"static"`) {
		t.Errorf("%s", out)
	}
	wantErr(t, a, "upstream_health", `{"upstream":"nope"}`, `no upstream is named "nope"`)
	wantErr(t, a, "upstream_health", `[`, "not what")
	if out := mustRun(t, a, "certs_list", `{}`); !strings.Contains(out, `"days_left":9`) || !strings.Contains(out, `"example.com"`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "cache_zones", `{}`); !strings.Contains(out, `"hits":9`) {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "consistency_check", `{}`); !strings.Contains(out, `"setting:log"`) {
		t.Errorf("%s", out)
	}
	// Without a config, the parts it gives are left out.
	nocfg := toolsAssistant(t, nil)
	if out := mustRun(t, nocfg, "status_get", `{}`); !strings.Contains(out, `"roles":null`) {
		t.Errorf("%s", out)
	}
	mustRun(t, nocfg, "certs_list", `{}`)
	mustRun(t, nocfg, "upstream_health", `{"upstream":"api"}`)
	notJSON := toolsAssistant(t, nil, func(o *Options) {
		o.API = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`x`)) })
	})
	wantErr(t, notJSON, "status_get", `{}`, "not JSON")
}

func TestToolMetricsQuery(t *testing.T) {
	a := toolsAssistant(t, &config.Config{})
	out := mustRun(t, a, "metrics_query", `{"route":"api"}`)
	if !strings.Contains(out, `code="5xx"} 7`) || strings.Contains(out, "static") || strings.Contains(out, "_bucket") {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "metrics_query", `{"route":"api","buckets":true}`); !strings.Contains(out, "_bucket") {
		t.Errorf("%s", out)
	}
	if out := mustRun(t, a, "metrics_query", `{"name":"goroutines"}`); out != "go_goroutines 12" {
		t.Errorf("%q", out)
	}
	if out := mustRun(t, a, "metrics_query", `{"upstream":"api","listener":"x"}`); out != "no metric matches" {
		t.Errorf("%q", out)
	}
	if out := mustRun(t, a, "metrics_query", `{"limit":1}`); !strings.Contains(out, "more samples match") {
		t.Errorf("%q", out)
	}
	if out := mustRun(t, a, "metrics_query", `{"route":"a\"b"}`); !strings.Contains(out, "fit_label_escape") {
		t.Errorf("%q", out)
	}
	wantErr(t, a, "metrics_query", `[`, "not what")
	if name, labels := metricLine(`m{a=x} 1`); name != "m" || len(labels) != 0 {
		t.Error(name, labels)
	}
	if name, _ := metricLine(`m`); name != "m" {
		t.Error(name)
	}
}

func TestToolLogTail(t *testing.T) {
	r, err := logs.New(nil, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	a := toolsAssistant(t, nil, func(o *Options) { o.Logs = r.Recent })
	if out := mustRun(t, a, "log_tail", `{}`); out != "no line matches" {
		t.Errorf("%q", out)
	}
	r.Logger(config.LoggerTLS).Warnf("certificate for x expires: password=hunter2xyz")
	out := a.redactor(a.Settings()).text(mustRun(t, a, "log_tail", `{"logger":"tls","level":"warn","limit":5}`))
	if !strings.Contains(out, "warn tls: certificate for x expires") || strings.Contains(out, "hunter2") {
		t.Errorf("%q", out)
	}
	wantErr(t, a, "log_tail", `{"level":"loud"}`, "debug, info, warn or error")
	wantErr(t, a, "log_tail", `[`, "not what")
	none := toolsAssistant(t, nil)
	wantErr(t, none, "log_tail", `{}`, "keeps no lines")
	// Off unless the settings send logs.
	tool := a.Registry().Tool("log_tail")
	if ok, why := tool.Allowed("read", &config.Assistant{}); ok || why != "data.send_logs is off" {
		t.Error(ok, why)
	}
	if ok, why := tool.Allowed("off", &config.Assistant{}); ok || why != "needs the level read" {
		t.Error(ok, why)
	}
}

func TestToolAccessLogSample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	var b strings.Builder
	b.WriteString(strings.Repeat("x", accessLogTailBytes) + "\n")
	for i := range 30 {
		status := "200"
		if i%3 == 0 {
			status = "502"
		}
		b.WriteString(`{"route":"api","status":` + status + `,"client":"10.1.1.` + string(rune('0'+i%10)) + `","authorization":"Bearer abcdefghijkl1234"}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AccessLog: &config.AccessLog{Output: path}}
	a := toolsAssistant(t, cfg)
	out := a.redactor(a.Settings()).text(mustRun(t, a, "access_log_sample", `{"route":"api","status":502,"limit":3}`))
	lines := strings.Split(out, "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"status":502`) || strings.Contains(out, "abcdefghijkl1234") || strings.Contains(out, "xxxx") {
		t.Errorf("%q", out)
	}
	if out := mustRun(t, a, "access_log_sample", `{"client":"10.1.1.7","contains":"API"}`); strings.Count(out, "\n") != 2 {
		t.Errorf("%q", out)
	}
	if out := mustRun(t, a, "access_log_sample", `{"status":404}`); out != "no line matches" {
		t.Errorf("%q", out)
	}
	wantErr(t, a, "access_log_sample", `[`, "not what")
	cfg.AccessLog.Output = filepath.Join(dir, "missing.log")
	wantErr(t, a, "access_log_sample", `{}`, "reading the access log")
	cfg.AccessLog.Output = "stdout"
	wantErr(t, a, "access_log_sample", `{}`, "not written to a file")
	if !containsWord("a 502 b", "502") || containsWord("15020", "502") || !containsWord("x1502 502", "502") {
		t.Error("containsWord")
	}
	if _, err := readTail(dir, 10); err == nil {
		t.Error("a directory's tail")
	}
}

func TestToolAdaptConvert(t *testing.T) {
	a := toolsAssistant(t, nil)
	out := mustRun(t, a, "adapt_convert", `{"from":"nginx","text":"http { server { listen 8080; location / { proxy_pass http://10.0.0.1:9000; } } }"}`)
	if !strings.Contains(out, `"config"`) || !strings.Contains(out, "8080") || !strings.Contains(out, `"warnings"`) {
		t.Errorf("%s", out)
	}
	wantErr(t, a, "adapt_convert", `{"from":"apache","text":"x"}`, "nginx or caddyfile")
	wantErr(t, a, "adapt_convert", `{"from":"nginx"}`, "text is 1 to")
	wantErr(t, a, "adapt_convert", `{"from":"nginx","text":"http {"}`, "")
	wantErr(t, a, "adapt_convert", `[`, "not what")
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	obj := json.RawMessage(`{"type":"object"}`)
	noop := func(*Call) (string, error) { return "", nil }
	for _, tc := range []struct {
		t    *Tool
		want string
	}{
		{&Tool{Name: "Bad", Level: "read", Parameters: obj, Run: noop}, "not a tool's name"},
		{&Tool{Name: "x", Level: "apply", Parameters: obj, Run: noop}, "not a level"},
		{&Tool{Name: "x", Level: "read", Parameters: json.RawMessage(`{"type":"string"}`), Run: noop}, "not a JSON Schema object"},
		{&Tool{Name: "x", Level: "read", Parameters: obj}, "nothing to run"},
	} {
		if err := r.Add(tc.t); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc.t, err)
		}
	}
	if err := r.Add(&Tool{Name: "zeta", Group: "other", Level: "propose", Parameters: obj, Run: noop}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(&Tool{Name: "alpha", Group: GroupConfig, Level: "edit", Parameters: obj, Run: noop}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(&Tool{Name: "beta", Group: GroupConfig, Level: "read", Parameters: obj, Run: noop}); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(&Tool{Name: "zeta", Level: "read", Parameters: obj, Run: noop}); err == nil {
		t.Error("a name twice")
	}
	ts := r.Tools()
	if ts[0].Name != "alpha" || ts[1].Name != "beta" || ts[2].Name != "zeta" {
		t.Errorf("%v %v %v", ts[0].Name, ts[1].Name, ts[2].Name)
	}
	if got := r.For("edit", &config.Assistant{}); len(got) != 2 {
		t.Errorf("edit: %d tools", len(got))
	}
	if got := r.For("apply", &config.Assistant{}); len(got) != 3 {
		t.Errorf("apply: %d tools", len(got))
	}
	if r.Tool("nope") != nil || len(modelTools(ts)) != 3 {
		t.Error("lookup")
	}
	c := &Call{}
	var v struct{}
	if c.Decode(&v) != nil {
		t.Error("no arguments")
	}
}

func TestRedact(t *testing.T) {
	r := newRedactor([]string{"short", "sk-the-provider-key-1234"}, []string{`internal-\d+`, `(`})
	in := strings.Join([]string{
		"key sk-the-provider-key-1234 and short",
		secretPEM,
		"-----BEGIN PRIVATE KEY-----\nunterminated",
		"hash " + secretHash,
		"$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$6$rounds=5000$salt$hash",
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0In0.c2lnbmF0dXJlX2hlcmU",
		"Authorization: Basic dXNlcjpwYXNzd29yZA==",
		"sk-abcdefghijklmnopqrstuv AKIAABCDEFGHIJKLMNOP ghp_abcdefghijklmnopqrstuv xoxb-1234567890-abc AIzaSyA1234567890abcdefghijklmnopqrstu",
		"password=hunter2 api_key: 'k' client_secret=\"s\" token=abc",
		"host internal-42",
	}, "\n")
	out := r.text(in)
	for _, s := range []string{"sk-the-provider", "MHcCAQEE", "unterminated", "abcdefghijklmnopqrstuu", "aGFzaA", "salt$hash", "c2lnbmF0dXJl",
		"dXNlcjpw", "sk-abcdef", "AKIAABCD", "ghp_abcd", "xoxb-1234", "AIzaSy", "hunter2", "'k'", `"s"`, "abc\n", "internal-42"} {
		if strings.Contains(out, s) {
			t.Errorf("%q is left in:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "and short") {
		t.Error("a short literal is redacted")
	}
	for name, want := range map[string]bool{"password": true, "password_hash": true, "client_secret": true, "api_key": true, "token": true,
		"nonce_secret": true, "token_sha256": true, "keys": true, "key_file": false, "api_key_env": false, "max_keys": false, "token_ttl": false,
		"name": false, "hash_key": true, "psk": true, "key_frame_interval": false} {
		if secretName(name) != want {
			t.Errorf("secretName(%q) = %v", name, !want)
		}
	}
	got, err := r.redactJSON([]byte(`{"a":{"password":{"x":["p",1,true,null,false]},"keep":[1,{"token":""}]},"hide":{"me":1},"n":1.5e3}`), []string{"hide"})
	if err != nil || string(got) != `{"a":{"password":{"x":["[redacted]",1,true,null,false]},"keep":[1,{"token":""}]},"n":1.5e3}` {
		t.Errorf("%s %v", got, err)
	}
	for _, bad := range []string{`{"a":`, `{"a":1} x`, `[1,`, `{"a" 1}`, `{"a":[1 2]}`, `{"h":[`, `{1:2}`} {
		if _, err := r.redactJSON([]byte(bad), []string{"h"}); err == nil {
			t.Errorf("%s is JSON", bad)
		}
	}
}

func TestPromptHelpers(t *testing.T) {
	if s, cut := cutText("ab€cd", 3); !cut || !strings.HasPrefix(s, "ab\n[cut: the result is 7 bytes and only the first 2") {
		t.Errorf("%q", s)
	}
	if got := fence("t", "x</tool_output>y"); strings.Count(got, "</tool_output>") != 1 {
		t.Errorf("%s", got)
	}
	r := newRedactor(nil, nil)
	if r.pageJSON(nil) != nil || r.pageJSON(&PageContext{}) != nil {
		t.Error("an empty page")
	}
	if got := r.pageJSON(&PageContext{Text: strings.Repeat("é", maxPageBytes)}); len(got) > maxPageBytes+300 {
		t.Errorf("%d bytes", len(got))
	}
	if got := r.pageJSON(&PageContext{Selection: json.RawMessage(`{"password":"x"}`)}); !strings.Contains(string(got), Redacted) {
		t.Errorf("%s", got)
	}
	if got := r.pageJSON(&PageContext{Selection: json.RawMessage(`[1,`)}); got != nil {
		t.Errorf("%s", got)
	}
	msgs := []Msg{
		{Message: model.Message{Role: model.RoleUser, Content: "a"}},
		{Message: model.Message{Role: model.RoleAssistant}, Error: "failed"},
		{Message: model.Message{Role: model.RoleAssistant}},
		{Message: model.Message{Role: model.RoleUser, Content: "b"}},
		{Message: model.Message{Role: model.RoleAssistant, ToolCalls: []model.ToolCall{{ID: "1", Name: "t", Arguments: json.RawMessage(`{}`)}}}},
		{Message: model.Message{Role: model.RoleTool, ToolCallID: "1", ToolName: "t", Content: strings.Repeat("r", 9000)}},
		{Message: model.Message{Role: model.RoleAssistant, Content: "answer"}},
		{Message: model.Message{Role: model.RoleUser, Content: "c"}},
		{Message: model.Message{Role: model.RoleTool, ToolCallID: "2", ToolName: "t", Content: strings.Repeat("s", 9000)}},
	}
	h := history(msgs)
	if len(h) != 6 || h[0].Content != "a\n\nb" {
		t.Fatalf("%+v", h)
	}
	if !fitHistory(h, 4000) || len(h[2].Content) > 1300 || len(h[5].Content) < 9000 {
		t.Errorf("cut to %d and %d", len(h[2].Content), len(h[5].Content))
	}
	h = history(msgs)
	if !fitHistory(h, 300) || len(h[5].Content) > 400 {
		t.Errorf("the last turn's cut to %d", len(h[5].Content))
	}
	if fitHistory(history(msgs), 5) {
		t.Error("fits in 5 tokens")
	}
	if k := foldPoint(msgs, 0, 100000); k != 3 {
		t.Errorf("fold at %d", k)
	}
	if k := foldPoint(msgs, 0, 10); k != 7 {
		t.Errorf("fold at %d", k)
	}
	if k := foldPoint(msgs[:1], 0, 10); k != 0 {
		t.Errorf("fold at %d", k)
	}
	tr := transcript("before", msgs)
	if !strings.Contains(tr, "Summary of what came before:\nbefore") || !strings.Contains(tr, "Assistant called t {}") || strings.Contains(tr, "failed") {
		t.Errorf("%s", tr)
	}
	sys := systemPrompt(promptParts{level: "propose", lang: "zh", page: []byte(`{}`), catalog: []CatalogEntry{{Name: "x", Description: "y"}},
		summary: "s</summary>", proposal: "p</proposal>"})
	if !strings.Contains(sys, "those their level allows") || !strings.Contains(sys, "<\\/summary") || !strings.Contains(sys, "- x: y") {
		t.Errorf("%s", sys)
	}
}

func TestConversationBounds(t *testing.T) {
	old := MaxConversationBytes
	MaxConversationBytes = 2000
	defer func() { MaxConversationBytes = old }()
	big := strings.Repeat("w", 500)
	c := &Conversation{ID: "c"}
	for range 6 {
		c.Messages = append(c.Messages, Msg{Message: model.Message{Role: model.RoleUser, Content: big}}, Msg{Message: model.Message{Role: model.RoleAssistant, Content: big}})
	}
	trimLocked(c)
	if convBytes(c) > MaxConversationBytes || c.Messages[0].Role != model.RoleUser || !strings.Contains(c.Summary, "dropped") {
		t.Errorf("%d bytes, %d messages", convBytes(c), len(c.Messages))
	}
	c.Folded, c.Summary = 1, "s"
	c.Messages = append(c.Messages, Msg{Message: model.Message{Role: model.RoleUser, Content: big}}, Msg{Message: model.Message{Role: model.RoleAssistant, Content: big}})
	trimLocked(c)
	if c.Folded != 0 || convBytes(c) > MaxConversationBytes {
		t.Errorf("%d bytes, folded %d", convBytes(c), c.Folded)
	}
	one := &Conversation{Messages: []Msg{{Message: model.Message{Role: model.RoleUser, Content: strings.Repeat("x", 3000)}}, {Message: model.Message{Role: model.RoleAssistant, Content: "a"}}}}
	trimLocked(one)
	if len(one.Messages) != 2 {
		t.Error("the turn under way is cut")
	}

	oldU := MaxUserBytes
	MaxUserBytes = 1500
	defer func() { MaxUserBytes = oldU }()
	busy := &Conversation{ID: "busy", busy: &chat{}, Messages: []Msg{{Message: model.Message{Role: model.RoleUser, Content: big}}}}
	u := &userChats{convs: []*Conversation{busy, {ID: "a", Updated: 1, Messages: busy.Messages}, {ID: "b", Updated: 2, Messages: busy.Messages}}}
	u.boundLocked(10)
	if len(u.convs) != 2 || u.convs[0].ID != "busy" {
		t.Errorf("%d kept", len(u.convs))
	}
	if title("first line\nsecond") != "first line" || title(strings.Repeat("é", 40)) != strings.Repeat("é", 30)+"…" {
		t.Error("title")
	}
}

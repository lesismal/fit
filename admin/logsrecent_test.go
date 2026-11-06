package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

const recentDoc = `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond"}}]}`

// TestRecentLogs checks GET /logs/recent: the ring's latest messages,
// filtered, after a sequence number, with the last one to ask after next;
// bad queries are refused; without a log, none.
func TestRecentLogs(t *testing.T) {
	root, err := logs.New(&config.Log{Level: config.LevelInfo}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	f := startEnv(t, recentDoc, &config.Admin{}, runtime.Env{Log: root, Metrics: observe.NewMetrics()})
	before := root.Last()
	srv, tls := root.Logger(config.LoggerServer), root.Logger(config.LoggerTLS)
	srv.Infof("one")
	tls.Warnf("two expires")
	srv.Errorf("three")
	read := func(query string) (int, recentLogsBody, string) {
		t.Helper()
		resp, got := f.get(t, "/logs/recent"+query)
		var b recentLogsBody
		_ = json.Unmarshal([]byte(got), &b)
		return resp.StatusCode, b, got
	}
	after := "?after=" + itoa(before)
	code, b, raw := read(after)
	if code != 200 || len(b.Records) != 3 || b.Records[0].Message != "one" || b.Last != before+3 || b.RingSize != logs.RingSize {
		t.Fatalf("%d %s", code, raw)
	}
	if _, b, raw = read(after + "&level=warn"); len(b.Records) != 2 || b.Records[0].Logger != "tls" {
		t.Errorf("warn and up: %s", raw)
	}
	if _, b, raw = read(after + "&logger=server&contains=THREE"); len(b.Records) != 1 || b.Records[0].Message != "three" {
		t.Errorf("server, three: %s", raw)
	}
	if _, b, raw = read("?after=" + itoa(before+2)); len(b.Records) != 1 || b.Records[0].Seq != before+3 {
		t.Errorf("after the second: %s", raw)
	}
	if _, b, raw = read("?limit=1"); len(b.Records) != 1 || b.Records[0].Message != "three" {
		t.Errorf("the latest: %s", raw)
	}
	for _, q := range []string{"?level=loud", "?limit=x", "?limit=1001", "?after=-1", "?contains=" + strings.Repeat("x", 257)} {
		if code, _, raw := read(q); code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, code, raw)
		}
	}

	// Without a log, nothing.
	g := start(t, recentDoc, &config.Admin{})
	resp, got := g.get(t, "/logs/recent")
	if resp.StatusCode != 200 || !strings.Contains(got, `"records":[]`) {
		t.Errorf("without a log: %d %s", resp.StatusCode, got)
	}
}

// TestClusterHandler: /cluster is 404 until a manager sets its handler,
// which then serves it behind the token, and is taken away again.
func TestClusterHandler(t *testing.T) {
	f := start(t, recentDoc, &config.Admin{})
	if resp, got := f.get(t, "/cluster/nodes"); resp.StatusCode != 404 || !strings.Contains(got, "admin.cluster") {
		t.Fatalf("%d %s", resp.StatusCode, got)
	}
	f.admin.SetCluster(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"path": r.URL.Path}) }))
	for _, p := range []string{"/cluster", "/cluster/nodes"} {
		if resp, got := f.get(t, p); resp.StatusCode != 200 || !strings.Contains(got, `"path":"`+p+`"`) {
			t.Errorf("%s: %d %s", p, resp.StatusCode, got)
		}
	}
	f.admin.SetCluster(nil)
	if resp, _ := f.get(t, "/cluster"); resp.StatusCode != 404 {
		t.Errorf("taken away: %d", resp.StatusCode)
	}
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

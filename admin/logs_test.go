package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// TestLogsAPI checks GET /logs, POST /logs/rotate and POST /logs/cleanup:
// the outputs and their files, a rotation now, and a cleanup that previews
// before it removes, and only the sink's own files.
func TestLogsAPI(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "fit.log")
	accessPath := filepath.Join(dir, "access.log")
	logJSON, _ := json.Marshal(logPath)
	accessJSON, _ := json.Marshal(accessPath)
	rot := &config.Rotate{
		MaxBytes: int64p(200), MaxAge: durp(time.Hour), MaxTotalBytes: int64p(0), Compress: boolp(false),
	}
	doc := `{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "log": {"output": ` + string(logJSON) + `, "rotate": {"max_bytes": 200, "max_age": "1h", "max_total_bytes": 0, "compress": false}},
  "access_log": {"output": ` + string(accessJSON) + `},
  "routes": [{"handler": {"module": "respond", "params": {"body": "hi"}}}]
}`
	root, err := logs.New(&config.Log{Output: logPath, Rotate: rot}, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	access, err := observe.NewAccessLogFiles(&config.AccessLog{
		Output: accessPath,
		Rotate: &config.Rotate{MaxBytes: int64p(0), MaxAge: durp(0), MaxTotalBytes: int64p(0), Compress: boolp(false)},
	}, root.Files())
	if err != nil {
		t.Fatal(err)
	}
	defer access.Close()
	f := startEnv(t, doc, &config.Admin{}, runtime.Env{Log: root, AccessLog: access, Metrics: observe.NewMetrics()})

	get := func() logsBody {
		t.Helper()
		resp, got := f.get(t, "/logs")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /logs: %d %s", resp.StatusCode, got)
		}
		var body logsBody
		if err := json.Unmarshal([]byte(got), &body); err != nil {
			t.Fatalf("%s: %v", got, err)
		}
		return body
	}
	body := get()
	if len(body.Outputs) != 2 {
		t.Fatalf("outputs %+v", body.Outputs)
	}
	var logOut *logOutputBody
	for i := range body.Outputs {
		if body.Outputs[i].Path == logPath {
			logOut = &body.Outputs[i]
		}
	}
	if logOut == nil || logOut.Rotate == nil || logOut.Rotate.MaxBytes != 200 || logOut.TimeZone != "local" {
		t.Fatalf("the log's output %+v", body.Outputs)
	}
	if len(logOut.Loggers) == 0 || logOut.Format != "line" {
		t.Errorf("loggers %v, format %q", logOut.Loggers, logOut.Format)
	}

	// A rotation now turns the file fit's own log has written to.
	root.Logger(config.LoggerServer).Infof("a line")
	waitSink(t, root, logPath, 5)
	post := func(path, body string) (int, string) {
		req, _ := http.NewRequest("POST", f.api+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, got := f.do(t, req)
		return resp.StatusCode, got
	}
	if status, got := post("/logs/rotate", `{"path":`+string(logJSON)+`}`); status != http.StatusOK {
		t.Fatalf("rotate: %d %s", status, got)
	}
	body = get()
	for i := range body.Outputs {
		if body.Outputs[i].Path != logPath {
			continue
		}
		rotated := 0
		for _, file := range body.Outputs[i].Files {
			if !file.Current {
				rotated++
			}
		}
		if rotated == 0 {
			t.Errorf("no rotated file after rotate now: %+v", body.Outputs[i].Files)
		}
	}

	// A cleanup previews what it would remove and removes only that, and
	// only a file the sink made.
	old := "fit.log." + time.Now().Add(-2*time.Hour).UTC().Format("2006-01-02T15-04-05.000")
	if err := os.WriteFile(filepath.Join(dir, old), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, old), mtime, mtime); err != nil {
		t.Fatal(err)
	}
	status, got := post("/logs/cleanup", `{"path":`+string(logJSON)+`,"dry_run":true}`)
	if status != http.StatusOK || !strings.Contains(got, old) {
		t.Fatalf("dry run: %d %s", status, got)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); err != nil {
		t.Fatalf("the dry run removed %s: %v", old, err)
	}
	status, got = post("/logs/cleanup", `{"path":`+string(logJSON)+`}`)
	if status != http.StatusOK || !strings.Contains(got, old) {
		t.Fatalf("cleanup: %d %s", status, got)
	}
	if _, err := os.Stat(filepath.Join(dir, old)); !os.IsNotExist(err) {
		t.Errorf("%s was not removed: %v", old, err)
	}
	if status, _ := post("/logs/cleanup", `{"path":"/etc/passwd"}`); status != http.StatusBadRequest {
		t.Errorf("a path that is not a log file: %d", status)
	}
}

// waitSink waits until the sink of path has a byte written, so that a
// rotation now has something to turn.
func waitSink(t *testing.T, root *logs.Root, path string, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f := root.Files().File(path); f != nil && f.Info().CurrentSize >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not reach %d bytes", path, want)
}

func int64p(v int64) *int64 { return &v }

func durp(v time.Duration) *config.Duration {
	d := config.Duration(v)
	return &d
}

func boolp(v bool) *bool { return &v }

package admin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// TestMediaLibrary reads a library through the Admin API: the libraries,
// a page of one's items, filtered, an item whole, and a rescan.
func TestMediaLibrary(t *testing.T) {
	f := start(t, `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}], "routes": [{"handler": {"module": "respond", "params": {"body": "x"}}}]}`, &config.Admin{})
	root, err := filepath.Abs(filepath.Join("..", "medialib", "testdata", "lib"))
	if err != nil {
		t.Fatal(err)
	}
	engine := f.configs.env.Engine
	if err := medialib.Apply(engine, []config.MediaLibrary{{Name: "films", Roots: []string{root}}}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { medialib.Remove(engine) })
	l := medialib.Lookup(engine, "films")
	for deadline := time.Now().Add(20 * time.Second); l.Status().Scans == 0; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no scan")
		}
	}
	decode := func(path string, want int) map[string]any {
		t.Helper()
		resp, body := f.get(t, path)
		if resp.StatusCode != want {
			t.Fatalf("GET %s: %d %s", path, resp.StatusCode, body)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}

	libs := decode("/media/library", 200)["libraries"].([]any)
	if len(libs) != 1 {
		t.Fatalf("libraries: %v", libs)
	}
	lib := libs[0].(map[string]any)
	if lib["name"] != "films" || lib["items"] != 5.0 || lib["errors"] != 1.0 || lib["status"].(map[string]any)["scans"] != 1.0 ||
		lib["pool"].(map[string]any)["workers"] == 0.0 || lib["tables"].(map[string]any)["max"] != float64(config.DefaultMediaMaxTableBytes) {
		t.Errorf("the library: %v", lib)
	}

	page := decode("/media/library/films", 200)
	if page["matched"] != 5.0 || page["limit"] != 100.0 || len(page["page"].([]any)) != 5 || len(page["problem_list"].([]any)) != 0 {
		t.Errorf("the page: %v", page)
	}
	ids := func(path string) string {
		var out []string
		for _, it := range decode(path, 200)["page"].([]any) {
			out = append(out, it.(map[string]any)["id"].(string))
		}
		return strings.Join(out, " ")
	}
	for path, want := range map[string]string{
		"/media/library/films?offset=1&limit=2":      "clips/Av1-Opus-" + strings.Split(ids("/media/library/films?q=opus&codec=av1"), "-")[2] + " clips/late",
		"/media/library/films?q=SINTEL":              "sintel",
		"/media/library/films?codec=h265":            "sintel",
		"/media/library/films?error=true":            "broken",
		"/media/library/films?error=0&codec=aac&q=b": "films/bbb",
		"/media/library/films?offset=9":              "",
	} {
		if got := ids(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
	bbb := decode("/media/library/films?q=bbb", 200)["page"].([]any)[0].(map[string]any)
	if bbb["width"] != 128.0 || bbb["renditions"] != 2.0 || bbb["audio"] != 1.0 || bbb["subtitles"] != 2.0 || bbb["poster"] != true ||
		bbb["problems"] != false || strings.Join(toStrings(bbb["codecs"]), ",") != "h264,aac" {
		t.Errorf("bbb: %v", bbb)
	}
	for _, bad := range []string{"offset=-1", "limit=0", "limit=1001", "offset=x", "error=maybe"} {
		decode("/media/library/films?"+bad, 400)
	}

	item := decode("/media/library/films/items/films/bbb", 200)
	if item["id"] != "films/bbb" || len(item["renditions"].([]any)) != 2 || item["poster"].(map[string]any)["path"] != "films/bbb.jpg" {
		t.Errorf("the item: %v", item)
	}
	decode("/media/library/films/items/films/none", 404)
	decode("/media/library/none", 404)
	decode("/media/library/none/items/x", 404)

	req, _ := http.NewRequest("POST", f.api+"/media/library/films/rescan", nil)
	resp, body := f.do(t, req)
	if resp.StatusCode != http.StatusAccepted || !strings.Contains(body, `"queued":`) {
		t.Errorf("rescan: %d %s", resp.StatusCode, body)
	}
	req, _ = http.NewRequest("POST", f.api+"/media/library/none/rescan", nil)
	if resp, _ := f.do(t, req); resp.StatusCode != 404 {
		t.Errorf("rescan of no library: %d", resp.StatusCode)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

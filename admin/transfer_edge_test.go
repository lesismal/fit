package admin

import (
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/config"
)

// TestExportRequestErrors: an export whose body is not the one it takes,
// or that asks a fit without a store to seal with its key, is refused; the
// members it names are taken once each, empty names left out.
func TestExportRequestErrors(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, c := range []struct {
		name, contentType, body string
		status                  int
		want                    string
	}{
		{"text", "text/plain", `{"format": "json"}`, 415, "application/json"},
		{"not JSON", "application/json", `{"format": `, 400, "the body must be {"},
		{"a store's key without a store", "application/json", `{"format": "store"}`, 400, "fit runs without a store"},
	} {
		resp, body := f.send(t, "POST", "/config/export", c.contentType, c.body, "")
		if resp.StatusCode != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.name, resp.StatusCode, body)
		}
	}
	resp, body := f.send(t, "POST", "/config/export", "application/json", `{"format": "json", "members": ["", " routes ", "routes", "upstreams"]}`, "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"routes"`) || strings.Contains(body, "listeners") ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), `filename="fit-routes-upstreams-v1.json"`) {
		t.Errorf("members with empty names and twice: %d %v %s", resp.StatusCode, resp.Header, body)
	}
}

// TestImportRequestErrors: an import is refused, and nothing applied, when
// it names admin, its body is not of its format's type, its file does not
// parse or is not a config object, or fit has no store to open it with.
func TestImportRequestErrors(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	before := f.configs.Current()
	list, err := store.Seal("pw", []byte(`[1]`), nil)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := store.Seal("pw", []byte(`{"listeners": []}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, query, contentType, password, body string
		status                                   int
		want                                     string
	}{
		{"admin", "format=json&members=admin", "text/plain", "", `{}`, 400, "admin cannot be edited this way"},
		{"a store as text", "format=store", "text/plain", "", "FITSTORE", 415, "application/octet-stream"},
		{"a store without a password", "format=store", "application/octet-stream", "", "FITSTORE", 400, "fit runs without a store"},
		{"a store that is not one", "format=store", "application/octet-stream", "pw", "FITSTORE", 400, "not a fitstore file"},
		{"a store of a list", "format=store", "application/octet-stream", "pw", string(list), 400, "the file is not a config object"},
		{"a store whose config does not load", "format=store", "application/octet-stream", "pw", string(empty), 400, "the config was not applied"},
		{"no format", "", "text/plain", "", `{}`, 400, "conf, yaml or json"},
		{"XML", "format=json", "application/xml", "", `{}`, 415, "text/plain"},
		{"broken conf", "format=conf", "text/plain", "", "listener web {\n", 400, "the text does not parse"},
	} {
		req, _ := http.NewRequest("POST", f.api+"/config/import?"+c.query, strings.NewReader(c.body))
		req.Header.Set("Content-Type", c.contentType)
		if c.password != "" {
			req.Header.Set(StorePasswordHeader, c.password)
		}
		resp, body := f.do(t, req)
		if resp.StatusCode != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d %s", c.name, resp.StatusCode, body)
		}
	}
	if f.configs.Current() != before || f.served(t) != "v1" {
		t.Fatal("a refused import changed the config")
	}
}

// TestImportMembersMissing: a member an import takes that its file lacks is
// removed from the config, and the others stay.
func TestImportMembersMissing(t *testing.T) {
	withUpstream := `{"listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "respond", "params": {"body": "v1"}}}],
  "upstreams": [{"name": "app", "addresses": ["127.0.0.1:9000"]}]}`
	f := start(t, withUpstream, &config.Admin{})
	resp, body := f.send(t, "POST", "/config/import?format=json&members=routes,upstreams", "text/plain",
		`{"routes": [{"handler": {"module": "respond", "params": {"body": "v2"}}}]}`, "")
	if resp.StatusCode != 200 || f.served(t) != "v2" {
		t.Fatalf("import routes and upstreams: %d %s", resp.StatusCode, body)
	}
	cfg := currentConfig(t, f)
	if _, ok := cfg["upstreams"]; ok {
		t.Errorf("upstreams after a file that lacks them: %s", cfg["upstreams"])
	}
	if _, ok := cfg["listeners"]; !ok {
		t.Error("the listeners were removed")
	}
}

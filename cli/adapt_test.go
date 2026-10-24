package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestYAMLConfig checks that fit validate takes YAML, and points its errors
// at the YAML.
func TestYAMLConfig(t *testing.T) {
	if code, stdout, stderr := runFit("validate", "-c", "../examples/hello.yaml"); code != 0 || !strings.Contains(stdout, "valid") {
		t.Errorf("hello.yaml: %d %q %q", code, stdout, stderr)
	}
	bad := filepath.Join(t.TempDir(), "bad.yml")
	_ = os.WriteFile(bad, []byte("listeners:\n  - name: web\n    address: \":8080\"\n    protocols: [http/1.1, h3]\nroutes:\n  - handler: {module: respond}\n"), 0o644)
	code, _, stderr := runFit("validate", "-c", bad)
	if code != 1 || !strings.Contains(stderr, bad+":4:27: listeners[0].protocols[1]: h3 needs tls") {
		t.Errorf("an error: %d %q", code, stderr)
	}
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	_ = os.WriteFile(broken, []byte("listeners: [\n"), 0o644)
	if code, _, stderr := runFit("validate", "-c", broken); code != 1 || !strings.Contains(stderr, broken) {
		t.Errorf("YAML that does not parse: %d %q", code, stderr)
	}
}

func TestAdapt(t *testing.T) {
	code, yaml, stderr := runFit("adapt", "-c", "../examples/proxy.json", "-to", "yaml")
	if code != 0 || !strings.Contains(yaml, "upstreams:\n  - name: app") {
		t.Fatalf("to yaml: %d %q %q", code, yaml, stderr)
	}
	f := filepath.Join(t.TempDir(), "proxy.yaml")
	_ = os.WriteFile(f, []byte(yaml), 0o644)
	out := filepath.Join(t.TempDir(), "proxy.json")
	if code, _, stderr := runFit("adapt", "-c", f, "-o", out); code != 0 {
		t.Fatalf("back: %d %q", code, stderr)
	}
	if code, _, stderr := runFit("validate", "-c", out); code != 0 {
		t.Errorf("the JSON it wrote: %d %q", code, stderr)
	}
	for _, args := range [][]string{{"-to", "toml"}, {"-from", "xml", "-c", "../examples/proxy.json"}} {
		if code, _, _ := runFit(append([]string{"adapt"}, args...)...); code == 0 {
			t.Errorf("%v: no error", args)
		}
	}
}

// TestAdaptNginx converts an nginx config, the adapter named with -from,
// and lists what it skipped.
func TestAdaptNginx(t *testing.T) {
	code, out, stderr := runFit("adapt", "-from", "nginx", "-c", "../adapter/nginx/testdata/full.conf")
	if code != 0 || !strings.Contains(out, `"listeners"`) || !strings.Contains(stderr, "full.conf: warning: line 55: stream: skipped") {
		t.Errorf("%d %q %q", code, out, stderr)
	}
}

// TestAdaptCaddyfile converts a Caddyfile, the adapter named with -from,
// and lists what it skipped.
func TestAdaptCaddyfile(t *testing.T) {
	data, err := os.ReadFile("../adapter/caddyfile/testdata/full.caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "Caddyfile")
	_ = os.WriteFile(f, data, 0o644)
	code, out, stderr := runFit("adapt", "-from", "caddyfile", "-c", f)
	if code != 0 || !strings.Contains(out, `"listeners"`) || !strings.Contains(stderr, "Caddyfile: warning: line 44: templates: skipped") {
		t.Errorf("%d %q %q", code, out, stderr)
	}
}

// TestJSONWithComments checks that fit validate takes JSON with comments
// and trailing commas, and points its errors at their lines.
func TestJSONWithComments(t *testing.T) {
	f := filepath.Join(t.TempDir(), "fit.json")
	_ = os.WriteFile(f, []byte("// a comment\n{\n  \"listeners\": [{\"name\": \"web\", \"address\": \":8080\",}],\n  \"routes\": [\n    /* hello */ {\"handler\": {\"module\": \"respond\",},},\n  ],\n}\n"), 0o644)
	if code, stdout, stderr := runFit("validate", "-c", f); code != 0 || !strings.Contains(stdout, "valid") {
		t.Errorf("%d %q %q", code, stdout, stderr)
	}
	_ = os.WriteFile(f, []byte("{\n  // a comment\n  \"listeners\": [{\"name\": \"web\", \"address\": \"nowhere\",},],\n}\n"), 0o644)
	if code, _, stderr := runFit("validate", "-c", f); code != 1 || !strings.Contains(stderr, f+":3:") {
		t.Errorf("%d %q", code, stderr)
	}
}

// TestExampleFitFiles checks that the examples' .konf files are valid and
// say what their JSON twins do.
func TestExampleFitFiles(t *testing.T) {
	for _, name := range []string{"hello", "proxy"} {
		f := "../examples/" + name + ".konf"
		if code, stdout, stderr := runFit("validate", "-c", f); code != 0 || !strings.Contains(stdout, "valid") || stderr != "" {
			t.Errorf("%s: %d %q %q", f, code, stdout, stderr)
		}
		_, fromFit, _ := runFit("adapt", "-c", f)
		_, fromJSON, _ := runFit("adapt", "-c", "../examples/"+name+".json")
		if !sameJSON(fromFit, fromJSON) {
			t.Errorf("%s is not its JSON twin:\n%s\n%s", f, fromFit, fromJSON)
		}
	}
}

// TestAdaptFit writes a config as a .konf file and reads it back.
func TestAdaptFit(t *testing.T) {
	code, out, stderr := runFit("adapt", "-c", "../examples/proxy.json", "-to", "fit")
	if code != 0 || !strings.Contains(out, "upstream app 127.0.0.1:9000 127.0.0.1:9001 {\n\tmax_conns 256\n") {
		t.Fatalf("to fit: %d %q %q", code, out, stderr)
	}
	f := filepath.Join(t.TempDir(), "proxy.konf")
	_ = os.WriteFile(f, []byte(out), 0o644)
	_, back, _ := runFit("adapt", "-c", f)
	_, want, _ := runFit("adapt", "-c", "../examples/proxy.json")
	if !sameJSON(back, want) {
		t.Errorf("the round trip changed it:\n%s\n%s", back, want)
	}
}

// TestSeveralFiles checks that a config read from several files, named
// or in a directory, is merged, and its errors point at their files.
func TestSeveralFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.konf")
	_ = os.WriteFile(base, []byte("listener web {\n\taddress :8080\n}\n"), 0o644)
	sites := filepath.Join(dir, "sites")
	_ = os.Mkdir(sites, 0o755)
	_ = os.WriteFile(filepath.Join(sites, "a.konf"), []byte("site a.example {\n\troute { handler respond { body a } }\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(sites, "b.yaml"), []byte("routes:\n  - handler: {module: respond, params: {status: 99}}\n"), 0o644)

	code, _, stderr := runFit("validate", "-c", base, "-c", sites)
	if want := filepath.Join(sites, "b.yaml") + ":2:49: routes[0].handler.params.status"; code != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("an error in the second file: %d %q, want %q", code, stderr, want)
	}
	_ = os.WriteFile(filepath.Join(sites, "b.yaml"), []byte("routes:\n  - handler: {module: respond, params: {status: 204}}\n"), 0o644)
	if code, stdout, stderr := runFit("validate", "-c", base, "-c", sites); code != 0 || stdout != base+", "+sites+": valid\n" {
		t.Fatalf("valid: %d %q %q", code, stdout, stderr)
	}
	code, out, stderr := runFit("adapt", "-c", dir)
	if code != 0 || strings.Count(out, `"module": "respond"`) != 2 || !strings.Contains(out, `"a.example"`) {
		t.Fatalf("adapt a directory: %d %q %q", code, out, stderr)
	}
	if code, _, _ := runFit("adapt", "-c", base, "-c", sites, "-from", "fit"); code != 2 {
		t.Errorf("-from with several files: %d", code)
	}
	_ = os.WriteFile(filepath.Join(sites, "c.konf"), []byte("listener web {\n\taddress :8081\n}\n"), 0o644)
	// The second listener is the first of c.fit's.
	if code, _, stderr := runFit("validate", "-c", dir); code != 1 || !strings.Contains(stderr, filepath.Join(sites, "c.konf")+":1:10: listeners[0].name") {
		t.Errorf("two listeners of one name: %d %q", code, stderr)
	}
}

// sameJSON says two JSON documents hold the same value.
func sameJSON(a, b string) bool {
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// TestValidateFastCGI checks that fit validate takes a config with the
// fastcgi handler, which it compiles with nothing to dial with.
func TestValidateFastCGI(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "fit.json")
	_ = os.WriteFile(f, []byte(`{"listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "fastcgi", "params": {"address": "127.0.0.1:9000", "root": `+strconv.Quote(dir)+`}}}]}`), 0o644)
	if code, stdout, stderr := runFit("validate", "-c", f); code != 0 || !strings.Contains(stdout, "valid") {
		t.Errorf("%d %q %q", code, stdout, stderr)
	}
}

package cli

import (
	"os"
	"path/filepath"
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

// TestAdaptNginx converts an nginx config, the adapter picked by its
// extension, and lists what it skipped.
func TestAdaptNginx(t *testing.T) {
	code, out, stderr := runFit("adapt", "-c", "../adapter/nginx/testdata/full.conf")
	if code != 0 || !strings.Contains(out, `"listeners"`) || !strings.Contains(stderr, "full.conf: warning: line 55: stream: skipped") {
		t.Errorf("%d %q %q", code, out, stderr)
	}
}

// TestAdaptCaddyfile converts a Caddyfile, the adapter picked by the file's
// name, and lists what it skipped.
func TestAdaptCaddyfile(t *testing.T) {
	data, err := os.ReadFile("../adapter/caddyfile/testdata/full.caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "Caddyfile")
	_ = os.WriteFile(f, data, 0o644)
	code, out, stderr := runFit("adapt", "-c", f)
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

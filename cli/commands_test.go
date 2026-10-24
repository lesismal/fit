package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/plugin/build"
	fitruntime "github.com/lesismal/fit/runtime"
)

func TestMainFunc(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := Main([]string{"help"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("%d %q %q", code, out.String(), errOut.String())
	}
	for _, flag := range []string{"-h", "-help", "--help"} {
		if code, stdout, _ := runFit(flag); code != 0 || stdout != usage {
			t.Errorf("%s: %d %q", flag, code, stdout)
		}
	}
	if code, _, stderr := runFit("version", "-x"); code != 2 || !strings.Contains(stderr, "-x") {
		t.Errorf("version -x: %d %q", code, stderr)
	}
}

func TestBuildFlags(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	writeFile(t, file, "")
	// -keep under a file fails before the go command runs.
	keep := filepath.Join(file, "keep")
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-bogus"}, 2, "Usage: fit build"},
		{[]string{"extra"}, 2, `fit build: unexpected "extra"`},
		{[]string{"-with", "@v1"}, 2, "no module path"},
		{[]string{"-with", "example.com/m@v1=dir"}, 2, "a version and a directory both"},
		{[]string{"-fit", "v1.0.0", "-without", "example.com/none"}, 2, "-without example.com/none"},
		{[]string{"-fit", "v1.0.0", "-keep", keep, "-o", filepath.Join(dir, "fit")}, 1, "fit build: "},
		{[]string{"-fit", dir, "-with", "example.com/m", "-with", "example.com/m", "-keep", keep, "-o", filepath.Join(dir, "fit")}, 1, "fit build: "},
	} {
		code, stdout, stderr := runFit(append([]string{"build"}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.want) {
			t.Errorf("%v: %d %q %q, want %d and %q", tt.args, code, stdout, stderr, tt.code, tt.want)
		}
		if code == 1 && !strings.Contains(stdout, "fit build: building ") {
			t.Errorf("%v: %q", tt.args, stdout)
		}
	}
	// With no -fit, this fit's own is built on: a test binary does not know
	// its version, and says so; one that does fails at -keep.
	code, _, stderr := runFit("build", "-keep", keep, "-o", filepath.Join(dir, "fit"))
	if code == 0 || !strings.Contains(stderr, "fit build: ") {
		t.Errorf("own fit: %d %q", code, stderr)
	}
	if got := describe(ownFit()); got != "" && strings.HasSuffix(got, "+dirty") {
		t.Errorf("own fit %q", got)
	}
	if got := describe(build.Module{Dir: "/src"}); got != "from /src" {
		t.Errorf("describe: %q", got)
	}
}

func TestAdaptFlags(t *testing.T) {
	dir := t.TempDir()
	hello := "../examples/hello.json"
	for _, tt := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"-bogus"}, 2, "Usage: fit adapt"},
		{[]string{"-c", hello, "extra"}, 2, `unexpected argument "extra"`},
		{[]string{"-c", "-", "-c", hello}, 2, "reads standard input alone"},
		{[]string{"-from", "yaml", "-c", hello, "-c", hello}, 2, "-from is for a single file"},
		{[]string{"-from", "yaml", "-c", filepath.Join(dir, "missing.yaml")}, 1, "missing.yaml"},
		{[]string{"-c", hello, "-o", filepath.Join(dir, "missing", "out.json")}, 1, "fit adapt: "},
	} {
		code, _, stderr := runFit(append([]string{"adapt"}, tt.args...)...)
		if code != tt.code || !strings.Contains(stderr, tt.want) {
			t.Errorf("%v: %d %q, want %d and %q", tt.args, code, stderr, tt.code, tt.want)
		}
	}
	if s := adapterList(); !strings.Contains(s, "nginx") || !strings.Contains(s, ", ") {
		t.Errorf("adapters: %q", s)
	}

	// -from names the format of a file whose extension does not.
	yml := filepath.Join(dir, "config.txt")
	writeFile(t, yml, "listeners:\n  - name: web\n    address: \":8080\"\nroutes:\n  - handler: {module: respond}\n")
	code, stdout, stderr := runFit("adapt", "-from", "yaml", "-c", yml, "-to", "fit")
	if code != 0 || !strings.Contains(stdout, "web") {
		t.Fatalf("-from yaml -to fit: %d %q %q", code, stdout, stderr)
	}

	// -c - reads standard input, JSON when -from does not say.
	for _, tt := range []struct {
		from, input string
		code        int
	}{
		{"", `{"listeners": [{"name": "web", "address": ":8080"}], "routes": [{"handler": {"module": "respond"}}]}`, 0},
		{"yaml", "listeners: [\n", 1},
	} {
		in := filepath.Join(dir, "stdin")
		writeFile(t, in, tt.input)
		f, err := os.Open(in)
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdin
		os.Stdin = f
		args := []string{"adapt", "-c", "-"}
		if tt.from != "" {
			args = append(args, "-from", tt.from)
		}
		code, stdout, stderr := runFit(args...)
		os.Stdin = saved
		f.Close()
		if code != tt.code || (code == 0 && !strings.Contains(stdout, `"listeners"`)) {
			t.Errorf("stdin %q: %d %q %q", tt.from, code, stdout, stderr)
		}
	}
}

func TestSchemaFlags(t *testing.T) {
	if code, stdout, _ := runFit("schema", "-template", "yaml"); code != 0 || stdout == "" {
		t.Errorf("template: %d %.100q", code, stdout)
	}
	if code, _, stderr := runFit("schema", "-template", "toml"); code != 1 || !strings.HasPrefix(stderr, "fit schema: ") {
		t.Errorf("bad template: %d %q", code, stderr)
	}
	dir := t.TempDir()
	if code, _, stderr := runFit("schema", "-template", "fit", "-dir", dir); code != 0 {
		t.Errorf("template dir: %d %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "sites", "example.com.konf")); err != nil {
		t.Errorf("template dir: %v", err)
	}
	if code, _, stderr := runFit("schema", "-template", "fit", "-dir", dir); code != 1 || !strings.Contains(stderr, "there already") {
		t.Errorf("template dir over itself: %d %q", code, stderr)
	}
	if code, _, _ := runFit("schema", "-dir", dir); code != 2 {
		t.Errorf("-dir without -template: %d", code)
	}
	if code, _, _ := runFit("schema", "-bogus"); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
	if code, _, _ := runFit("validate", "-bogus"); code != 2 {
		t.Errorf("validate bad flag: %d", code)
	}
	printWarnings(io.Discard, nil)
}

// TestDefaultConfig takes fit.json from the working directory when -c is
// not given.
func TestDefaultConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	writeFile(t, "fit.json", `{"listeners": [{"name": "web", "address": ":8080"}], "routes": [{"handler": {"module": "respond"}}]}`)
	if code, stdout, stderr := runFit("validate"); code != 0 || stdout != "fit.json: valid\n" {
		t.Errorf("%d %q %q", code, stdout, stderr)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("broken pipe") }

func TestHashPasswordErrors(t *testing.T) {
	defer func(r io.Reader) { stdin = r }(stdin)
	stdin = failingReader{}
	if code, _, stderr := runFit("hash-password"); code != 1 || stderr != "fit hash-password: broken pipe\n" {
		t.Errorf("read error: %d %q", code, stderr)
	}
	if code, _, _ := runFit("hash-password", "-bogus"); code != 2 {
		t.Errorf("bad flag: %d", code)
	}
}

// TestStartProvidersErrors starts providers fit's config check would have
// refused.
func TestStartProvidersErrors(t *testing.T) {
	c := &config.Config{Providers: []config.Provider{{Name: "none"}}}
	if _, err := startProviders(c, nil, io.Discard, io.Discard); err == nil || err.Error() != "provider none: no kind is set" {
		t.Errorf("no kind: %v", err)
	}
	if s := kindList(); !strings.Contains(s, "consul_kv") {
		t.Errorf("kinds: %q", s)
	}
	configs := admin.NewConfigs(new(fitruntime.Runtime), nil, fitruntime.Env{}, c, []byte(`{}`), admin.SourceFile, 1)
	c = &config.Config{Providers: []config.Provider{{Name: "Bad", ConsulKV: &config.ConsulKVProvider{}}}}
	var out bytes.Buffer
	if _, err := startProviders(c, configs, &out, io.Discard); err == nil || !strings.Contains(err.Error(), `provider name "Bad"`) {
		t.Errorf("bad name: %v", err)
	}
}

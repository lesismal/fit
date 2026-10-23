package admin

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
	"github.com/lesismal/fit/provider/file"
)

// route is a route for path answering body, named name when name is not
// empty, and with more members when more is not empty.
func route(name, path, body, more string) string {
	n := ""
	if name != "" {
		n = `"name": "` + name + `", `
	}
	if more != "" {
		more = ", " + more
	}
	return `{` + n + `"match": {"path": ["` + path + `"]}, "handler": {"module": "respond", "params": {"body": "` + body + `"}}` + more + `}`
}

// fakeRoute is a part with one route, as route makes it.
func fakeRoute(name, path, body string) string {
	return `{"routes": [` + route(name, path, body, "") + `]}`
}

// servedAt returns what the server's listener answers at path.
func (f *fixture) servedAt(t *testing.T, path string) string {
	t.Helper()
	resp, err := f.client.Get("http://" + f.server.Addrs()[0].Addr.String() + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body)
}

func (f *fixture) effective(t *testing.T) Effective {
	t.Helper()
	resp, body := f.get(t, "/config/effective")
	if resp.StatusCode != 200 {
		t.Fatalf("GET /config/effective: %d %s", resp.StatusCode, body)
	}
	var e Effective
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("GET /config/effective: %v: %s", err, body)
	}
	if resp.Header.Get(VersionHeader) == "" {
		t.Fatalf("GET /config/effective has no %s", VersionHeader)
	}
	return e
}

func hasSource(e Effective, s provider.Source) bool { return slices.Contains(e.Sources, s) }

func TestProviders(t *testing.T) {
	f := start(t, doc("v1"), &config.Admin{})
	for _, name := range []string{"fake", "other"} {
		if err := f.configs.AddProvider(name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"fake", "file", "admin", "Bad", "", "a/b"} {
		if err := f.configs.AddProvider(name); err == nil {
			t.Errorf("AddProvider(%q) was taken", name)
		}
	}
	if _, err := f.configs.Provide("nope", []byte(`{}`)); err == nil {
		t.Error("a provider never added provided")
	}
	etag := f.configs.Current().ETag

	// A provider's route is served beside the base config's.
	fake := fakeRoute("fr", "/p", "from fake")
	r, err := f.configs.Provide("fake", []byte(fake))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != 2 || r.ETag != etag || r.Source != "fake" {
		t.Fatalf("Provide: %+v", r)
	}
	if got := f.servedAt(t, "/p"); got != "from fake" {
		t.Fatalf("/p served %q", got)
	}
	if got := f.served(t); got != "v1" {
		t.Fatalf("/ served %q", got)
	}
	if resp, body := f.get(t, "/config"); body != doc("v1") || resp.Header.Get("ETag") != etag {
		t.Fatalf("GET /config is not the base config: %v %s", resp.Header, body)
	}

	// The other provider's route named as fake's is left out; its other
	// route is served.
	other := `{"routes": [` + route("fr", "/p", "from other", "") + `, ` + route("", "/q", "from other", `"listeners": ["web"]`) + `]}`
	if _, err := f.configs.Provide("other", []byte(other)); err != nil {
		t.Fatal(err)
	}
	if got := f.servedAt(t, "/p"); got != "from fake" {
		t.Fatalf("/p served %q after the other provider", got)
	}
	if got := f.servedAt(t, "/q"); got != "from other" {
		t.Fatalf("/q served %q", got)
	}
	e := f.effective(t)
	if e.Version != 3 || e.ETag != etag || !slices.Equal(e.Precedence, []string{"file", "fake", "other"}) {
		t.Fatalf("effective: %+v", e)
	}
	for _, s := range []provider.Source{
		{Path: "/listeners/0", Source: "file"},
		{Path: "/routes/0", Source: "file"},
		{Path: "/routes/1", Source: "fake", From: "/routes/0"},
		{Path: "/routes/2", Source: "other", From: "/routes/1"},
	} {
		if !hasSource(e, s) {
			t.Errorf("effective has no source %+v: %+v", s, e.Sources)
		}
	}
	wantShadowed := []provider.Shadowed{{Source: "other", From: "/routes/0", Name: "fr", By: "fake"}}
	if !slices.Equal(e.Shadowed, wantShadowed) {
		t.Errorf("shadowed: %+v", e.Shadowed)
	}
	var merged config.Config
	if err := json.Unmarshal(e.Config, &merged); err != nil || len(merged.Routes) != 3 || len(merged.Listeners) != 1 {
		t.Errorf("effective config: %v %s", err, e.Config)
	}

	// The same part again is not applied again.
	if r, err := f.configs.Provide("fake", []byte(fake)); err != nil || r.Version != 3 {
		t.Fatalf("the same part again: %+v %v", r, err)
	}

	// A broken part is refused with its errors where it wrote them, and
	// what the provider provided before stays.
	_, err = f.configs.Provide("fake", []byte(`{"routes": [{"hanlder": {}}]}`))
	es := config.AsErrors(err)
	if len(es) == 0 || !strings.Contains(es.Error(), "routes[0].hanlder") || !strings.Contains(es.Error(), "(provided by fake)") {
		t.Fatalf("a broken part: %v", err)
	}
	if got := f.servedAt(t, "/p"); got != "from fake" || f.configs.Current().Number != 3 {
		t.Fatalf("after a broken part: /p served %q, version %d", got, f.configs.Current().Number)
	}

	// A config loaded through the API keeps what providers provide.
	if resp, body := f.load(t, doc("v2")); resp.StatusCode != 200 {
		t.Fatalf("POST /load: %d %s", resp.StatusCode, body)
	}
	if f.served(t) != "v2" || f.servedAt(t, "/p") != "from fake" || f.servedAt(t, "/q") != "from other" {
		t.Fatal("a load dropped what providers provide")
	}
	if e := f.effective(t); e.Precedence[0] != SourceAdmin || !hasSource(e, provider.Source{Path: "/routes/0", Source: SourceAdmin}) {
		t.Fatalf("effective after a load: %+v", e)
	}

	// A base config that breaks what a provider provides is refused, with
	// the error in the provider's part.
	resp, body := f.load(t, strings.Replace(doc("v3"), `"web"`, `"main"`, 1))
	if resp.StatusCode != 400 || !strings.Contains(body, `"path":"routes[1].listeners[0]"`) || !strings.Contains(body, "(provided by other)") {
		t.Fatalf("a base config that breaks a provider's route: %d %s", resp.StatusCode, body)
	}

	// A rollback brings back the base config, and keeps what providers
	// provide.
	if _, err := f.configs.Rollback(1, ""); err != nil {
		t.Fatal(err)
	}
	if f.served(t) != "v1" || f.servedAt(t, "/p") != "from fake" {
		t.Fatal("a rollback dropped what providers provide")
	}
	if e := f.effective(t); e.Precedence[0] != SourceFile || f.configs.Current().Source != SourceRollback {
		t.Fatalf("effective after a rollback: %+v", e)
	}

	// A provider that provides nothing any more takes its routes away.
	if _, err := f.configs.Provide("fake", nil); err != nil {
		t.Fatal(err)
	}
	if got := f.servedAt(t, "/p"); got != "from other" {
		t.Fatalf("/p served %q once fake provides nothing, want the other provider's, no longer shadowed", got)
	}
	if e := f.effective(t); len(e.Shadowed) != 0 {
		t.Fatalf("shadowed once fake provides nothing: %+v", e.Shadowed)
	}
	if tried, failed := f.configs.Reloads(); tried != 7 || failed != 2 {
		t.Errorf("reloads: %d tried, %d failed", tried, failed)
	}
}

// TestFileAndProviders runs the file provider and fake providers together:
// an edit to the file and what providers send are merged, and the
// effective config says where each part came from.
func TestFileAndProviders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.json")
	data := []byte(doc("v1"))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	f := start(t, string(data), &config.Admin{})
	fake := &fakeProvider{"fake", make(chan []byte, 1)}
	if err := f.configs.AddProvider(fake.name); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := file.New([]string{path}, []file.File{{Path: path, Data: data}}, file.Options{Interval: 10 * time.Millisecond, Debounce: 30 * time.Millisecond}, func(files []file.File) error {
		_, err := f.configs.Apply(files[0].Data, SourceFile)
		return err
	}, func(err error) { t.Errorf("the file provider: %v", err) })
	done := make(chan struct{}, 2)
	go func() { w.Run(ctx); done <- struct{}{} }()
	go func() {
		provider.Run(ctx, []provider.Provider{fake}, func(name string, data []byte) error {
			_, err := f.configs.Provide(name, data)
			return err
		}, func(name string, err error) { t.Errorf("provider %s: %v", name, err) })
		done <- struct{}{}
	}()

	fake.parts <- []byte(fakeRoute("", "/p", "from fake"))
	waitServed(t, f, "/p", "from fake")
	if err := os.WriteFile(path, []byte(doc("v2")), 0o644); err != nil {
		t.Fatal(err)
	}
	waitServed(t, f, "/", "v2")
	if got := f.servedAt(t, "/p"); got != "from fake" {
		t.Fatalf("/p served %q after the file changed", got)
	}
	e := f.effective(t)
	if !hasSource(e, provider.Source{Path: "/routes/0", Source: SourceFile}) ||
		!hasSource(e, provider.Source{Path: "/routes/1", Source: "fake", From: "/routes/0"}) ||
		!hasSource(e, provider.Source{Path: "/listeners/0", Source: SourceFile}) {
		t.Fatalf("effective: %+v", e)
	}
	fake.parts <- []byte(fakeRoute("", "/p", "changed"))
	waitServed(t, f, "/p", "changed")
	cancel()
	<-done
	<-done
}

func waitServed(t *testing.T, f *fixture, path, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := f.servedAt(t, path)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s served %q, want %q", path, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// fakeProvider is a provider whose parts a test sends.
type fakeProvider struct {
	name  string
	parts chan []byte
}

func (p *fakeProvider) Name() string { return p.name }

func (p *fakeProvider) Run(ctx context.Context, update func([]byte)) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-p.parts:
			update(data)
		}
	}
}

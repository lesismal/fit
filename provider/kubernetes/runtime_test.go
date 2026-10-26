package kubernetes

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/provider"
)

func TestKind(t *testing.T) {
	k, ok := provider.LookupKind("kubernetes")
	if !ok {
		t.Fatal("kind kubernetes is not registered")
	}
	p, watching := k("k", &config.Provider{Kubernetes: &config.KubernetesProvider{}}, nil)
	if p.Name() != "k" || watching != "watching Kubernetes for GatewayClasses of lesismal.github.io/fit" {
		t.Errorf("%q, %q", p.Name(), watching)
	}
}

func TestRedirectRefused(t *testing.T) {
	for _, rd := range []*HTTPRequestRedirect{
		{Path: &HTTPPathModifier{Type: "ReplacePrefixMatch"}},
		{Path: &HTTPPathModifier{Type: "Bad"}},
	} {
		if _, err := redirect(rd, "Exact", ""); err == nil {
			t.Errorf("redirect(%+v): no error", rd.Path)
		}
	}
}

// logLines collects what is logged.
type logLines struct {
	mu    sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logLines) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func (l *logLines) waitFor(t *testing.T, want string) {
	t.Helper()
	waitFor(t, "a log of "+want, func() bool { return strings.Contains(l.all(), want) })
}

// plainClient is a client of a plain HTTP server that h answers.
func plainClient(t *testing.T, h http.HandlerFunc) *client {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &client{server: srv.URL, hc: srv.Client(), timeout: 10 * time.Second}
}

func TestClientFails(t *testing.T) {
	var gone atomic.Int32
	c := plainClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gone":
			http.Error(w, "", http.StatusGone)
		case "/notjson":
			w.Write([]byte("nope"))
		case "/cut":
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("{"))
		case "/expires":
			// A continue token that expires once: the list starts over.
			switch {
			case r.URL.Query().Get("continue") == "":
				fmt.Fprint(w, `{"metadata": {"resourceVersion": "5", "continue": "1"}, "items": [{"a": 1}]}`)
			case gone.Add(1) == 1:
				http.Error(w, "", http.StatusGone)
			default:
				fmt.Fprint(w, `{"metadata": {"resourceVersion": "6"}, "items": [{"b": 2}]}`)
			}
		case "/many":
			fmt.Fprint(w, `{"metadata": {"resourceVersion": "7"}, "items": [{}, {}, {}]}`)
		case "/x/status":
			http.Error(w, `{"kind": "Status", "message": "no way"}`, http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	if _, err := c.get(ctx, "/gone", nil); err != errGone {
		t.Errorf("a 410: %v", err)
	}
	if _, err := c.watch(ctx, "/missing", nil, "1"); err != errNotFound {
		t.Errorf("a watch of a 404: %v", err)
	}
	var v any
	if err := c.page(ctx, "/notjson", nil, &v); err == nil || !strings.Contains(err.Error(), "GET /notjson: invalid character") {
		t.Errorf("a page not JSON: %v", err)
	}
	if err := c.page(ctx, "/cut", nil, &v); err == nil || !strings.Contains(err.Error(), "GET /cut: unexpected EOF") {
		t.Errorf("a page cut short: %v", err)
	}
	items, rv, truncated, err := c.list(ctx, "/expires", nil, 10)
	if err != nil || rv != "6" || truncated || len(items) != 2 || string(items[0]) != `{"a": 1}` || string(items[1]) != `{"b": 2}` {
		t.Errorf("a continue token expired: %s %q %v %v", items, rv, truncated, err)
	}
	if items, _, truncated, err := c.list(ctx, "/many", nil, 2); err != nil || !truncated || len(items) != 2 {
		t.Errorf("past max: %d %v %v", len(items), truncated, err)
	}
	if err := c.patchStatus(ctx, "/missing", []byte("{}")); err != errNotFound {
		t.Errorf("a patch of a 404: %v", err)
	}
	if err := c.patchStatus(ctx, "/x", []byte("{}")); err == nil || err.Error() != "PATCH /x/status: 403 Forbidden: no way" {
		t.Errorf("a patch refused: %v", err)
	}

	// A token that cannot be read.
	c.token = &token{file: filepath.Join(t.TempDir(), "missing")}
	if _, err := c.get(ctx, "/gone", nil); err == nil || !strings.HasPrefix(err.Error(), "token: ") {
		t.Errorf("get without a token: %v", err)
	}
	if err := c.patchStatus(ctx, "/x", nil); err == nil || !strings.HasPrefix(err.Error(), "token: ") {
		t.Errorf("patch without a token: %v", err)
	}
	c.token = nil
	// A server that is not a URL, and one that is not there.
	server := c.server
	c.server = "::bad"
	if _, err := c.get(ctx, "/x", nil); err == nil {
		t.Error("get of a bad URL")
	}
	if err := c.patchStatus(ctx, "/x", nil); err == nil {
		t.Error("patch of a bad URL")
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	c.server = srv.URL
	srv.Close()
	if _, err := c.get(ctx, "/x", nil); err == nil {
		t.Error("get of a server gone")
	}
	c.server = server
}

func TestLoadConfigEdges(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	crt, key := testCert(t, "client")
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	path := write("config", `apiVersion: v1
clusters:
- {name: c, cluster: {server: "https://127.0.0.1:1", certificate-authority-data: "`+b64(crt)+`"}}
- {name: noserver, cluster: {}}
- {name: nocafile, cluster: {server: "https://127.0.0.1:1", certificate-authority: missing.crt}}
- {name: notca, cluster: {server: "https://127.0.0.1:1", certificate-authority-data: "`+b64([]byte("not PEM"))+`"}}
users:
- {name: cert, user: {client-certificate-data: "`+b64(crt)+`", client-key-data: "`+b64(key)+`"}}
- {name: badcert, user: {client-certificate-data: "%%%"}}
- {name: nokey, user: {client-certificate-data: "`+b64(crt)+`", client-key: missing.key}}
contexts:
- {name: cert, context: {cluster: c, user: cert}}
- {name: noserver, context: {cluster: noserver, user: cert}}
- {name: nocafile, context: {cluster: nocafile, user: cert}}
- {name: notca, context: {cluster: notca, user: cert}}
- {name: badcert, context: {cluster: c, user: badcert}}
- {name: nokey, context: {cluster: c, user: nokey}}
`)
	rc, err := fromKubeconfig(path, "cert")
	if err != nil || len(rc.tls.Certificates) != 1 || rc.token != nil {
		t.Errorf("a client certificate: %+v %v", rc, err)
	}
	for ctx, want := range map[string]string{
		"noserver": `cluster "noserver" has no server`,
		"nocafile": `cluster "nocafile": certificate-authority: `,
		"notca":    `cluster "notca": certificate-authority holds no certificate`,
		"badcert":  `user "badcert": client-certificate: `,
		"nokey":    `user "nokey": client-key: `,
	} {
		if _, err := fromKubeconfig(path, ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("context %s: %v, want %q", ctx, err, want)
		}
	}
	if _, err := fromKubeconfig(filepath.Join(dir, "missing"), ""); err == nil || !strings.HasPrefix(err.Error(), "kubeconfig: ") {
		t.Errorf("a kubeconfig missing: %v", err)
	}

	// Outside a cluster, with no $KUBECONFIG: ~/.kube/config, or nothing
	// without a home.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBECONFIG", "")
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	if _, err := loadConfig(&config.KubernetesProvider{}); err == nil || !strings.Contains(err.Error(), filepath.Join(dir, ".kube", "config")) {
		t.Errorf("~/.kube/config: %v", err)
	}
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")
	if _, err := loadConfig(&config.KubernetesProvider{}); err == nil || !strings.Contains(err.Error(), "no home directory") {
		t.Errorf("no home: %v", err)
	}

	// In a cluster, with the service account's files missing, and with no
	// port given.
	tok, ca := serviceAccountToken, serviceAccountCA
	t.Cleanup(func() { serviceAccountToken, serviceAccountCA = tok, ca })
	serviceAccountCA, serviceAccountToken = filepath.Join(dir, "missing"), filepath.Join(dir, "missing")
	if _, err := inCluster("10.96.0.1", ""); err == nil || !strings.HasPrefix(err.Error(), "in the cluster: ") {
		t.Errorf("no CA: %v", err)
	}
	serviceAccountCA = write("ca.crt", string(crt))
	if _, err := inCluster("10.96.0.1", ""); err == nil || !strings.HasPrefix(err.Error(), "in the cluster: ") {
		t.Errorf("no token: %v", err)
	}
	serviceAccountToken = write("token", "t")
	if rc, err := inCluster("10.96.0.1", ""); err != nil || rc.server != "https://10.96.0.1:443" {
		t.Errorf("no port: %+v %v", rc, err)
	}
}

func TestDecodeKinds(t *testing.T) {
	for _, k := range watched(&config.KubernetesProvider{Ingress: &config.KubernetesIngress{}}) {
		if _, m, err := k.decode([]byte(`{"metadata": {"namespace": "n", "name": "x"}}`)); err != nil || m.key() != "n/x" {
			t.Errorf("%s: %v %v", k.name, m, err)
		}
		if _, _, err := k.decode([]byte(`1`)); err == nil {
			t.Errorf("%s: a number decoded", k.name)
		}
	}
}

// TestInformerWatch reads a watch's events: a bookmark, objects past max,
// and an error; and an object that does not decode, in a watch and in a
// list.
func TestInformerWatch(t *testing.T) {
	c := plainClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("watch") != "1":
			fmt.Fprint(w, `{"metadata": {"resourceVersion": "1"}, "items": [1]}`)
		case r.URL.Path == "/a":
			for _, e := range []string{
				`{"type": "BOOKMARK", "object": {"metadata": {"resourceVersion": "7"}}}`,
				`{"type": "ADDED", "object": {"metadata": {"namespace": "n", "name": "a", "resourceVersion": "8"}}}`,
				`{"type": "ADDED", "object": {"metadata": {"namespace": "n", "name": "b", "resourceVersion": "9"}}}`,
				`{"type": "MODIFIED", "object": {"metadata": {"namespace": "n", "name": "a"}}}`,
				`{"type": "ERROR", "object": {"kind": "Status", "code": 500, "message": "boom"}}`,
			} {
				fmt.Fprintln(w, e)
			}
		default:
			fmt.Fprintln(w, `{"type": "ADDED", "object": 1}`)
		}
	})
	var l logLines
	in := newInformer(kind{name: "things", path: "/a", decode: kindNamespaces.decode}, c, 1, make(chan struct{}, 1), logs.Func(l.logf))
	rv, err := in.watch(context.Background(), "1")
	if rv != "9" || err == nil || err.Error() != "the watch failed: 500 boom" {
		t.Errorf("watch: %q %v", rv, err)
	}
	if len(in.objs) != 1 || !strings.Contains(l.all(), "more than max_objects, 1, things: the rest are left out") {
		t.Errorf("objects %v; logs:\n%s", in.objs, l.all())
	}
	in.at = "/b"
	if _, err := in.watch(context.Background(), "1"); err == nil || !strings.HasPrefix(err.Error(), "an event of things: ") {
		t.Errorf("an event that does not decode: %v", err)
	}
	if _, err := in.list(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "things: ") {
		t.Errorf("a list that does not decode: %v", err)
	}
}

// TestInformerRun follows a kind that is missing, then served, whose watch
// fails; and checks that run returns when its context ends while it lists
// or waits.
func TestInformerRun(t *testing.T) {
	lo, hi := minBackoff, maxBackoff
	minBackoff, maxBackoff = time.Hour, 10*time.Millisecond
	t.Cleanup(func() { minBackoff, maxBackoff = lo, hi })
	var lists atomic.Int32
	c := plainClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("watch") == "1":
			http.Error(w, `{"message": "boom"}`, http.StatusInternalServerError)
		case lists.Add(1) == 1:
			http.NotFound(w, r)
		default:
			fmt.Fprint(w, `{"metadata": {"resourceVersion": "1"}, "items": []}`)
		}
	})
	var l logLines
	in := newInformer(kind{name: "things", path: "/a", decode: kindNamespaces.decode}, c, 10, make(chan struct{}, 1), logs.Func(l.logf))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		in.run(ctx)
	}()
	l.waitFor(t, "watching things: GET /a: 500 Internal Server Error: boom; listing them again in 1h0m0s")
	cancel() // while it waits
	<-done
	for _, want := range []string{"things are not served by the API server", "things are served now"} {
		if !strings.Contains(l.all(), want) {
			t.Errorf("logs lack %q:\n%s", want, l.all())
		}
	}

	// A watch that keeps failing is tried again, later each time.
	minBackoff = 10 * time.Millisecond
	l = logLines{}
	in = newInformer(kind{name: "things", path: "/a", decode: kindNamespaces.decode}, c, 10, make(chan struct{}, 1), logs.Func(l.logf))
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		defer close(done)
		in.run(ctx)
	}()
	waitFor(t, "a watch failing twice", func() bool { return strings.Count(l.all(), "watching things: ") >= 2 })
	cancel()
	<-done

	// The context ends while it lists.
	entered := make(chan struct{})
	c = plainClient(t, func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
	})
	in = newInformer(kind{name: "things", path: "/a", decode: kindNamespaces.decode}, c, 10, make(chan struct{}, 1), logs.Func(l.logf))
	ctx, cancel = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		defer close(done)
		in.run(ctx)
	}()
	<-entered
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return")
	}
}

// TestStatusWriter checks that a failing write is reported once, and its
// recovery, and that the writer stops between writes when its context
// ends.
func TestStatusWriter(t *testing.T) {
	var (
		fail   atomic.Bool
		cancel context.CancelFunc
		mu     sync.Mutex
	)
	c := plainClient(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		cf := cancel
		mu.Unlock()
		if cf != nil {
			cf()
		}
		if fail.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("{}"))
	})
	var l logLines
	in := newInformer(kindRoutes, c, 10, make(chan struct{}, 1), logs.Func(l.logf))
	sw := &statusWriter{c: c, informers: map[string]*informer{"httproutes": in}, log: logs.Func(l.logf), wake: make(chan struct{}, 1), failing: map[string]bool{}}
	u := statusUpdate{kind: "httproutes", ns: "default", name: "r", patch: []byte("{}")}
	fail.Store(true)
	sw.write(context.Background(), u)
	sw.write(context.Background(), u)
	fail.Store(false)
	sw.write(context.Background(), u)
	all := l.all()
	if strings.Count(all, "writing the status of httproutes: PATCH ") != 1 || !strings.Contains(all, "the status of httproutes is written again") {
		t.Errorf("logs:\n%s", all)
	}

	// Two sets before the writer runs: one wake-up, the last set. The
	// first write ends the context; the second is not made.
	sw.set([]statusUpdate{u})
	sw.set([]statusUpdate{u, u})
	ctx, cf := context.WithCancel(context.Background())
	mu.Lock()
	cancel = cf
	mu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		sw.run(ctx)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return")
	}
}

func TestCertFiles(t *testing.T) {
	dir := t.TempDir()
	removeCerts("", nil)
	removeCerts(filepath.Join(dir, "missing"), nil)
	if err := writeCerts(map[string][]byte{filepath.Join(dir, "missing", "a.pem"): []byte("x")}); err == nil {
		t.Error("a certificate written into a directory that is not there")
	}
}

// TestProviderSetupFails runs the provider with no cluster to reach, and
// with nowhere to write certificates.
func TestProviderSetupFails(t *testing.T) {
	shorten(t)
	run := func(opts *config.KubernetesProvider, want string) {
		t.Helper()
		var l logLines
		p := New("k8s", opts, logs.Func(l.logf))
		if p.Name() != "k8s" {
			t.Errorf("Name: %q", p.Name())
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			p.Run(ctx, func([]byte) {})
		}()
		l.waitFor(t, want)
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Run did not return")
		}
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	// Tried again: logged twice.
	var l logLines
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: missing}, logs.Func(l.logf))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func([]byte) { t.Error("a part without a cluster") })
	}()
	waitFor(t, "two tries", func() bool { return strings.Count(l.all(), "kubeconfig: ") >= 2 })
	cancel()
	<-done

	a := newAPIServer(t)
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	kc := a.kubeconfig(t)
	run(&config.KubernetesProvider{Kubeconfig: kc, CertificatesDir: filepath.Join(file, "certs")}, "certificates_dir: ")
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)
	run(&config.KubernetesProvider{Kubeconfig: kc}, "no directory for the certificates of HTTPS listeners: ")
}

// TestSendCertsFail checks that a part whose certificates cannot be
// written is not sent.
func TestSendCertsFail(t *testing.T) {
	crt, key := testCert(t, "a.example.com")
	var l logLines
	p := New("k8s", &config.KubernetesProvider{}, logs.Func(l.logf))
	p.certDir = filepath.Join(t.TempDir(), "missing")
	informers := map[string]*informer{}
	for _, k := range watched(p.opts) {
		in := newInformer(k, nil, 100, make(chan struct{}, 1), logs.Func(l.logf))
		in.replace(nil, false)
		informers[k.name] = in
	}
	put := func(k kind, doc string) {
		o, m, err := k.decode([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		informers[k.name].objs[m.key()] = o
	}
	put(kindClasses, `{"metadata": {"name": "fit"}, "spec": {"controllerName": "lesismal.github.io/fit"}}`)
	put(kindGateways, `{"metadata": {"namespace": "default", "name": "gw"}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "https", "port": 443, "protocol": "HTTPS", "tls": {"certificateRefs": [{"name": "a"}]}}]}}`)
	put(kindSecrets, secretDoc("default", "a", crt, key))
	sw := &statusWriter{wake: make(chan struct{}, 1)}
	p.send(informers, func([]byte) { t.Error("a part sent without its certificates") }, sw, true)
	if !strings.Contains(l.all(), "writing certificates: ") {
		t.Errorf("logs:\n%s", l.all())
	}
}

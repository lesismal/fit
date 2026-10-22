package kubernetes

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// apiServer is a fake Kubernetes API server: it lists the objects a test
// puts, in pages, and streams their changes to watches.
type apiServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	rv       int
	objs     map[string]map[string]json.RawMessage // by path, then namespace/name
	missing  map[string]bool                       // paths answered 404
	watchers map[string][]chan string
	lists    map[string]int
	watches  map[string]int
}

const testToken = "test-token"

func newAPIServer(t *testing.T) *apiServer {
	a := &apiServer{t: t, objs: map[string]map[string]json.RawMessage{}, missing: map[string]bool{},
		watchers: map[string][]chan string{}, lists: map[string]int{}, watches: map[string]int{}}
	a.srv = httptest.NewTLSServer(http.HandlerFunc(a.serve))
	t.Cleanup(a.srv.Close)
	return a
}

func (a *apiServer) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, `{"kind":"Status","code":401,"message":"Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	a.mu.Lock()
	if a.missing[path] {
		a.mu.Unlock()
		http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("watch") == "1" {
		a.watches[path]++
		ch := make(chan string, 64)
		a.watchers[path] = append(a.watchers[path], ch)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case e, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintln(w, e)
				w.(http.Flusher).Flush()
			}
		}
	}
	a.lists[path]++
	keys := sortedKeys(a.objs[path])
	from, _ := strconv.Atoi(r.URL.Query().Get("continue"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = len(keys)
	}
	to := min(from+limit, len(keys))
	items := make([]json.RawMessage, 0, to-from)
	for _, k := range keys[from:to] {
		items = append(items, a.objs[path][k])
	}
	cont := ""
	if to < len(keys) {
		cont = strconv.Itoa(to)
	}
	rv := strconv.Itoa(a.rv)
	a.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]string{"resourceVersion": rv, "continue": cont}, "items": items})
}

// put adds or changes an object at path, given as JSON with its metadata,
// and tells the watches.
func (a *apiServer) put(path, doc string) {
	a.event(path, doc, false)
}

func (a *apiServer) del(path, doc string) {
	a.event(path, doc, true)
}

func (a *apiServer) event(path, doc string, deleted bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.rv++
	var o map[string]any
	if err := json.Unmarshal([]byte(doc), &o); err != nil {
		a.t.Fatalf("%v: %s", err, doc)
	}
	m := o["metadata"].(map[string]any)
	m["resourceVersion"] = strconv.Itoa(a.rv)
	data, _ := json.Marshal(o)
	key := fmt.Sprint(m["namespace"]) + "/" + fmt.Sprint(m["name"])
	if a.objs[path] == nil {
		a.objs[path] = map[string]json.RawMessage{}
	}
	typ := "MODIFIED"
	switch _, had := a.objs[path][key]; {
	case deleted:
		typ = "DELETED"
		delete(a.objs[path], key)
	case !had:
		typ = "ADDED"
	}
	if !deleted {
		a.objs[path][key] = data
	}
	e, _ := json.Marshal(map[string]any{"type": typ, "object": json.RawMessage(data)})
	for _, ch := range a.watchers[path] {
		ch <- string(e)
	}
}

// send sends raw events to the watches of path.
func (a *apiServer) send(path, e string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ch := range a.watchers[path] {
		ch <- e
	}
}

// endWatches ends the watches of path.
func (a *apiServer) endWatches(path string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, ch := range a.watchers[path] {
		close(ch)
	}
	a.watchers[path] = nil
}

func (a *apiServer) counts(path string) (lists, watches int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lists[path], a.watches[path]
}

// kubeconfig writes a kubeconfig for the server, with its CA as data and
// the token, and returns its path.
func (a *apiServer) kubeconfig(t *testing.T) string {
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.srv.Certificate().Raw})
	doc := `apiVersion: v1
kind: Config
current-context: test
clusters:
- name: test-cluster
  cluster:
    server: ` + a.srv.URL + `
    certificate-authority-data: ` + base64.StdEncoding.EncodeToString(ca) + `
users:
- name: test-user
  user: {token: "` + testToken + `"}
contexts:
- name: other
  context: {cluster: nowhere, user: nobody}
- name: test
  context:
    cluster: test-cluster
    user: test-user
`
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func shorten(t *testing.T) {
	d, lo, hi := debounce, minBackoff, maxBackoff
	debounce, minBackoff, maxBackoff = 30*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { debounce, minBackoff, maxBackoff = d, lo, hi })
}

// parts collects what a provider sends.
type parts chan []byte

func (p parts) update(data []byte) {
	select {
	case p <- data:
	default:
		panic("parts not read")
	}
}

// next returns the next part, decoded.
func (p parts) next(t *testing.T) part {
	t.Helper()
	select {
	case data := <-p:
		var out part
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatalf("%v: %s", err, data)
		}
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("no part came")
		return part{}
	}
}

// TestProvider runs the provider against the fake API server: nothing is
// sent until every kind is listed, a missing kind counts as empty, lists
// come in pages, watches carry the changes, a watch too old makes a new
// list, and an ended watch is made again.
func TestProvider(t *testing.T) {
	shorten(t)
	a := newAPIServer(t)
	a.missing[kindGrants.path] = true
	a.put(kindClasses.path, `{"metadata": {"name": "fit"}, "spec": {"controllerName": "lesismal.github.io/fit"}}`)
	a.put(kindGateways.path, `{"metadata": {"namespace": "default", "name": "gw"}, "spec": {"gatewayClassName": "fit", "listeners": [{"name": "http", "port": 8000, "protocol": "HTTP"}]}}`)
	a.put(kindServices.path, `{"metadata": {"namespace": "default", "name": "web"}, "spec": {"ports": [{"port": 80}]}}`)
	// More slices than a page holds.
	for i := range pageSize + 20 {
		a.put(kindSlices.path, fmt.Sprintf(`{"metadata": {"namespace": "default", "name": "web-%d", "labels": {"kubernetes.io/service-name": "web"}},
  "endpoints": [{"addresses": ["10.1.%d.%d"]}], "ports": [{"port": 8080}]}`, i, i/250, i%250))
	}

	var logMu sync.Mutex
	var logs []string
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: a.kubeconfig(t)}, func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	})
	got := make(parts, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, got.update)
	}()

	first := got.next(t)
	if len(first.Listeners) != 1 || first.Listeners[0].Name != "k8s-8000" || len(first.Routes) != 0 || len(first.Upstreams) != 0 {
		t.Fatalf("at the start: %+v", first)
	}
	if lists, _ := a.counts(kindSlices.path); lists != 2 {
		t.Errorf("%d pages of slices", lists)
	}

	// A route comes: one upstream of every slice's endpoint.
	a.put(kindRoutes.path, `{"metadata": {"namespace": "default", "name": "r"}, "spec": {"parentRefs": [{"name": "gw"}], "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}}`)
	p1 := got.next(t)
	if len(p1.Routes) != 1 || len(p1.Upstreams) != 1 || len(p1.Upstreams[0].Addresses) != pageSize+20 {
		t.Fatalf("after a route: %d routes, %+v", len(p1.Routes), len(p1.Upstreams))
	}

	// The watch of slices is too old: they are listed again, as they are
	// now, which the watch never said.
	lists, _ := a.counts(kindSlices.path)
	a.mu.Lock()
	a.objs[kindSlices.path] = map[string]json.RawMessage{"default/web-new": json.RawMessage(
		`{"metadata": {"namespace": "default", "name": "web-new", "labels": {"kubernetes.io/service-name": "web"}}, "endpoints": [{"addresses": ["10.9.9.9"]}], "ports": [{"port": 8080}]}`)}
	a.mu.Unlock()
	a.send(kindSlices.path, `{"type": "ERROR", "object": {"kind": "Status", "code": 410, "message": "too old"}}`)
	p2 := got.next(t)
	if len(p2.Upstreams) != 1 || !reflect.DeepEqual(p2.Upstreams[0].Addresses, []string{"10.9.9.9:8080"}) {
		t.Fatalf("after the list again: %+v", p2.Upstreams)
	}
	if l, _ := a.counts(kindSlices.path); l != lists+1 {
		t.Errorf("%d lists after a watch too old", l-lists)
	}

	// An ended watch is made again, from where it was: a change after it
	// still comes.
	_, watches := a.counts(kindRoutes.path)
	a.endWatches(kindRoutes.path)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, w := a.counts(kindRoutes.path); w > watches {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("an ended watch was not made again")
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.del(kindRoutes.path, `{"metadata": {"namespace": "default", "name": "r"}}`)
	if p3 := got.next(t); len(p3.Routes) != 0 {
		t.Fatalf("after the route went: %+v", p3.Routes)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	logMu.Lock()
	defer logMu.Unlock()
	all := strings.Join(logs, "\n")
	if !strings.Contains(all, "provider k8s: watching the cluster at "+a.srv.URL) ||
		!strings.Contains(all, "provider k8s: referencegrants are not served by the API server") {
		t.Errorf("logs:\n%s", all)
	}
}

// TestProviderUnauthorized checks that a list refused is reported and
// tried again, and that nothing is sent meanwhile.
func TestProviderUnauthorized(t *testing.T) {
	shorten(t)
	a := newAPIServer(t)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	data, _ := os.ReadFile(a.kubeconfig(t))
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), testToken, "wrong", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	n := 0
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: path}, func(format string, args ...any) {
		if strings.Contains(fmt.Sprintf(format, args...), "401 Unauthorized") {
			mu.Lock()
			n++
			mu.Unlock()
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func([]byte) { t.Error("a part while unauthorized") })
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		tries := n
		mu.Unlock()
		if tries >= 14 { // twice for each of the seven kinds
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d refusals reported", tries)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestKubeconfig(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a := newAPIServer(t)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.srv.Certificate().Raw})
	write("ca.crt", string(ca))
	write("token", testToken+"\n")
	path := write("config", `apiVersion: v1
current-context: files
clusters:
- {name: c, cluster: {server: "`+a.srv.URL+`/", certificate-authority: ca.crt, tls-server-name: example.com}}
- {name: insecure, cluster: {server: "https://127.0.0.1:1", insecure-skip-tls-verify: true}}
users:
- {name: files, user: {tokenFile: token}}
- {name: plugin, user: {exec: {command: aws}}}
- {name: badcert, user: {client-certificate-data: "bm90IGEgY2VydA==", client-key-data: "bm90IGEga2V5"}}
contexts:
- {name: files, context: {cluster: c, user: files}}
- {name: plugin, context: {cluster: c, user: plugin}}
- {name: insecure, context: {cluster: insecure, user: files}}
- {name: badcert, context: {cluster: c, user: badcert}}
- {name: nocluster, context: {cluster: none, user: files}}
`)
	rc, err := fromKubeconfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if rc.server != a.srv.URL || rc.tls.ServerName != "example.com" || rc.tls.RootCAs == nil {
		t.Errorf("files: %+v", rc)
	}
	if tok, err := rc.token.get(); err != nil || tok != testToken {
		t.Errorf("token file: %q %v", tok, err)
	}
	if rc, err := fromKubeconfig(path, "insecure"); err != nil || !rc.tls.InsecureSkipVerify {
		t.Errorf("insecure: %+v %v", rc, err)
	}
	for ctx, want := range map[string]string{
		"plugin":    "authenticates with a plugin",
		"badcert":   "failed to find any PEM data",
		"nocluster": `no cluster "none"`,
		"absent":    `no context "absent"`,
	} {
		if _, err := fromKubeconfig(path, ctx); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("context %s: %v, want %q", ctx, err, want)
		}
	}
	if _, err := fromKubeconfig(write("nocurrent", "clusters: []\n"), ""); err == nil || !strings.Contains(err.Error(), "no current-context") {
		t.Errorf("no current context: %v", err)
	}
	if _, err := fromKubeconfig(write("bad", "clusters: [\n"), ""); err == nil {
		t.Error("a kubeconfig that is not YAML")
	}

	// loadConfig takes $KUBECONFIG outside a cluster, and the service
	// account inside one.
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBECONFIG", path+string(os.PathListSeparator)+"/nonexistent")
	if rc, err := loadConfig(&config.KubernetesProvider{}); err != nil || rc.server != a.srv.URL {
		t.Errorf("$KUBECONFIG: %+v %v", rc, err)
	}
	tok, crt := serviceAccountToken, serviceAccountCA
	t.Cleanup(func() { serviceAccountToken, serviceAccountCA = tok, crt })
	serviceAccountToken, serviceAccountCA = filepath.Join(dir, "token"), filepath.Join(dir, "ca.crt")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	rc, err = loadConfig(&config.KubernetesProvider{})
	if err != nil || rc.server != "https://10.96.0.1:443" || rc.where != "the pod's service account" {
		t.Errorf("in the cluster: %+v %v", rc, err)
	}
	// A kubeconfig named wins over the service account.
	if rc, err := loadConfig(&config.KubernetesProvider{Kubeconfig: path}); err != nil || rc.server != a.srv.URL {
		t.Errorf("a kubeconfig in the cluster: %+v %v", rc, err)
	}
	serviceAccountCA = filepath.Join(dir, "token") // no certificate in it
	if _, err := loadConfig(&config.KubernetesProvider{}); err == nil {
		t.Error("a service account CA with no certificate")
	}
}

// TestTokenRotates checks that a token file is read again once a minute
// has passed, and that the last token stays while the file is gone.
func TestTokenRotates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	tk := &token{file: path}
	if got, _ := tk.get(); got != "one" {
		t.Fatalf("first: %q", got)
	}
	if err := os.WriteFile(path, []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := tk.get(); got != "one" {
		t.Errorf("within a minute: %q", got)
	}
	tk.read = tk.read.Add(-2 * time.Minute)
	if got, _ := tk.get(); got != "two" {
		t.Errorf("after a minute: %q", got)
	}
	os.Remove(path)
	tk.read = tk.read.Add(-2 * time.Minute)
	if got, err := tk.get(); got != "two" || err != nil {
		t.Errorf("with the file gone: %q %v", got, err)
	}
}

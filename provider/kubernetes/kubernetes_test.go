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
	"github.com/lesismal/fit/logs"
)

// apiServer is a fake Kubernetes API server: it lists the objects a test
// puts, in pages, and streams their changes to watches, from the resource
// version each asks for.
type apiServer struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	rv       int
	objs     map[string]map[string]json.RawMessage // by path, then namespace/name
	missing  map[string]bool                       // paths answered 404
	watchers map[string][]chan string
	// events are the changes stored at each path, by resource version, which
	// a watch from an older version is sent first, as the API server does.
	events  map[string][]storedEvent
	lists   map[string]int
	watches map[string]int
	patches map[string]int // status patches by collection
	// conflicts is how many status patches are yet to be refused with 409.
	conflicts int
}

// storedEvent is a watch event and the resource version it made.
type storedEvent struct {
	rv    int
	event string
}

const testToken = "test-token"

func newAPIServer(t *testing.T) *apiServer {
	a := &apiServer{t: t, objs: map[string]map[string]json.RawMessage{}, missing: map[string]bool{},
		watchers: map[string][]chan string{}, events: map[string][]storedEvent{}, lists: map[string]int{}, watches: map[string]int{}, patches: map[string]int{}}
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
	if r.Method == http.MethodPatch {
		a.patch(w, r)
		return
	}
	a.mu.Lock()
	if a.missing[path] {
		a.mu.Unlock()
		http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("watch") == "1" {
		a.watches[path]++
		// The changes since the version listed come first: one made between
		// the list and this watch is not lost.
		from, _ := strconv.Atoi(r.URL.Query().Get("resourceVersion"))
		var missed []string
		for _, e := range a.events[path] {
			if e.rv > from {
				missed = append(missed, e.event)
			}
		}
		ch := make(chan string, 64)
		a.watchers[path] = append(a.watchers[path], ch)
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		for _, e := range missed {
			fmt.Fprintln(w, e)
		}
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
	var o map[string]any
	if err := json.Unmarshal([]byte(doc), &o); err != nil {
		a.t.Fatalf("%v: %s", err, doc)
	}
	a.store(path, o, deleted)
}

// store stores o at path, or deletes it, with a new resource version, and
// tells the watches; a.mu is held.
func (a *apiServer) store(path string, o map[string]any, deleted bool) {
	a.rv++
	m := o["metadata"].(map[string]any)
	m["resourceVersion"] = strconv.Itoa(a.rv)
	data, _ := json.Marshal(o)
	ns, _ := m["namespace"].(string)
	key := ns + "/" + fmt.Sprint(m["name"])
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
	a.events[path] = append(a.events[path], storedEvent{a.rv, string(e)})
	for _, ch := range a.watchers[path] {
		ch <- string(e)
	}
}

// patch applies a JSON merge patch to an object's status, as the API
// server does: refused with 409 when the patch names another resource
// version than the object's.
func (a *apiServer) patch(w http.ResponseWriter, r *http.Request) {
	path, ok := strings.CutSuffix(r.URL.Path, "/status")
	if !ok || r.Header.Get("Content-Type") != "application/merge-patch+json" {
		http.Error(w, `{"kind":"Status","code":400}`, http.StatusBadRequest)
		return
	}
	coll, ns, name := path[:strings.LastIndex(path, "/")], "", path[strings.LastIndex(path, "/")+1:]
	if before, rest, found := strings.Cut(coll, "/namespaces/"); found {
		ns, rest, _ = strings.Cut(rest, "/")
		coll = before + "/" + rest
	}
	var p struct {
		Metadata struct {
			ResourceVersion string `json:"resourceVersion"`
		} `json:"metadata"`
		Status map[string]any `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, `{"kind":"Status","code":400}`, http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.patches[coll]++
	data, ok := a.objs[coll][ns+"/"+name]
	if !ok {
		http.Error(w, `{"kind":"Status","code":404}`, http.StatusNotFound)
		return
	}
	var o map[string]any
	_ = json.Unmarshal(data, &o)
	if rv := o["metadata"].(map[string]any)["resourceVersion"]; a.conflicts > 0 || p.Metadata.ResourceVersion != rv {
		a.conflicts = max(a.conflicts-1, 0)
		http.Error(w, `{"kind":"Status","code":409,"message":"the object has been modified"}`, http.StatusConflict)
		return
	}
	status, _ := o["status"].(map[string]any)
	if status == nil {
		status = map[string]any{}
	}
	for k, v := range p.Status {
		if v == nil {
			delete(status, k)
		} else {
			status[k] = v
		}
	}
	o["status"] = status
	a.store(coll, o, false)
	_ = json.NewEncoder(w).Encode(o)
}

// object returns the object at path of namespace ns and name, decoded.
func (a *apiServer) object(path, ns, name string) map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	var o map[string]any
	_ = json.Unmarshal(a.objs[path][ns+"/"+name], &o)
	return o
}

func (a *apiServer) patchCount(path string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.patches[path]
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
	a.missing[kindGrants.older[0]] = true
	a.put(kindClasses.path, `{"metadata": {"name": "fit"}, "spec": {"controllerName": "lesismal.github.io/fit"}}`)
	a.put(kindGateways.path, `{"metadata": {"namespace": "default", "name": "gw"}, "spec": {"gatewayClassName": "fit", "listeners": [{"name": "http", "port": 8000, "protocol": "HTTP"}]}}`)
	a.put(kindServices.path, `{"metadata": {"namespace": "default", "name": "web"}, "spec": {"ports": [{"port": 80}]}}`)
	// More slices than a page holds.
	for i := range pageSize + 20 {
		a.put(kindSlices.path, fmt.Sprintf(`{"metadata": {"namespace": "default", "name": "web-%d", "labels": {"kubernetes.io/service-name": "web"}},
  "endpoints": [{"addresses": ["10.1.%d.%d"]}], "ports": [{"port": 8080}]}`, i, i/250, i%250))
	}

	var logMu sync.Mutex
	var lines []string
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: a.kubeconfig(t)}, logs.Func(func(format string, args ...any) {
		logMu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}))
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
	all := strings.Join(lines, "\n")
	if !strings.Contains(all, "provider k8s: watching the cluster at "+a.srv.URL) ||
		!strings.Contains(all, "provider k8s: referencegrants are not served by the API server") {
		t.Errorf("lines:\n%s", all)
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
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: path}, logs.Func(func(format string, args ...any) {
		if strings.Contains(fmt.Sprintf(format, args...), "401 Unauthorized") {
			mu.Lock()
			n++
			mu.Unlock()
		}
	}))
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
		if tries >= 2*len(watched(&config.KubernetesProvider{})) { // twice for each kind
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

// waitFor fails t unless ok holds within 10s.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within 10s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// condition returns the status and reason of the condition of type typ in
// conds, a status's conditions decoded.
func condition(conds any, typ string) string {
	list, _ := conds.([]any)
	for _, c := range list {
		if m := c.(map[string]any); m["type"] == typ {
			return fmt.Sprint(m["status"], " ", m["reason"])
		}
	}
	return ""
}

// TestProviderStatus runs the provider against the fake API server and
// follows what it writes back: the statuses of the class, the Gateway and
// its listeners, and routes, written once and then left; a write refused
// made again; a TLSRoute at the version the server has; and an HTTPS
// listener's certificate written to a file, and removed with its Secret.
func TestProviderStatus(t *testing.T) {
	shorten(t)
	r := resync
	resync = 200 * time.Millisecond
	t.Cleanup(func() { resync = r })
	a := newAPIServer(t)
	a.missing[kindTLSRoutes.path] = true
	a.missing[kindTLSRoutes.older[0]] = true
	tlsPath := kindTLSRoutes.older[1]
	crt, key := testCert(t, "a.example.com")
	a.put(kindSecrets.path, secretDoc("default", "a", crt, key))
	a.put(kindClasses.path, `{"metadata": {"name": "fit"}, "spec": {"controllerName": "lesismal.github.io/fit"}}`)
	a.put(kindGateways.path, `{"metadata": {"namespace": "default", "name": "gw", "generation": 1}, "spec": {"gatewayClassName": "fit", "listeners": [
  {"name": "http", "port": 8000, "protocol": "HTTP"},
  {"name": "https", "port": 8443, "protocol": "HTTPS", "tls": {"certificateRefs": [{"name": "a"}]}},
  {"name": "pass", "port": 9443, "protocol": "TLS", "tls": {"mode": "Passthrough"}}]}}`)
	a.put(kindServices.path, `{"metadata": {"namespace": "default", "name": "web"}, "spec": {"ports": [{"port": 80}]}}`)
	a.put(kindSlices.path, `{"metadata": {"namespace": "default", "name": "web-1", "labels": {"kubernetes.io/service-name": "web"}},
  "endpoints": [{"addresses": ["10.1.0.1"]}], "ports": [{"port": 8080}]}`)
	a.put(kindRoutes.path, `{"metadata": {"namespace": "default", "name": "r", "generation": 1}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}],
  "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}}`)
	a.put(tlsPath, `{"metadata": {"namespace": "default", "name": "db", "generation": 1}, "spec": {"parentRefs": [{"name": "gw"}],
  "rules": [{"backendRefs": [{"name": "web", "port": 80}]}]}}`)

	dir := filepath.Join(t.TempDir(), "certs")
	var mu sync.Mutex
	var last []byte
	p := New("k8s", &config.KubernetesProvider{Kubeconfig: a.kubeconfig(t), CertificatesDir: dir}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func(data []byte) {
			mu.Lock()
			last = data
			mu.Unlock()
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	current := func() part {
		mu.Lock()
		defer mu.Unlock()
		var out part
		_ = json.Unmarshal(last, &out)
		return out
	}

	routeStatus := func(path, name string) string {
		st, _ := a.object(path, "default", name)["status"].(map[string]any)
		parents, _ := st["parents"].([]any)
		if len(parents) != 1 {
			return ""
		}
		c := parents[0].(map[string]any)["conditions"]
		return condition(c, "Accepted") + ", " + condition(c, "ResolvedRefs")
	}
	waitFor(t, "the statuses", func() bool {
		gs, _ := a.object(kindGateways.path, "default", "gw")["status"].(map[string]any)
		cs, _ := a.object(kindClasses.path, "", "fit")["status"].(map[string]any)
		return condition(gs["conditions"], "Programmed") == "True Programmed" && condition(cs["conditions"], "Accepted") == "True Accepted" &&
			routeStatus(kindRoutes.path, "r") == "True Accepted, True ResolvedRefs" && routeStatus(tlsPath, "db") == "True Accepted, True ResolvedRefs"
	})
	gs := a.object(kindGateways.path, "default", "gw")["status"].(map[string]any)
	for _, l := range gs["listeners"].([]any) {
		l := l.(map[string]any)
		want := map[string]string{
			"http":  `1 [{"group":"gateway.networking.k8s.io","kind":"HTTPRoute"},{"group":"gateway.networking.k8s.io","kind":"GRPCRoute"}]`,
			"https": `0 [{"group":"gateway.networking.k8s.io","kind":"HTTPRoute"},{"group":"gateway.networking.k8s.io","kind":"GRPCRoute"}]`,
			"pass":  `1 [{"group":"gateway.networking.k8s.io","kind":"TLSRoute"}]`,
		}[l["name"].(string)]
		if got := fmt.Sprint(l["attachedRoutes"], " ", toJSON(l["supportedKinds"])); got != want {
			t.Errorf("listener %s: %s, want %s", l["name"], got, want)
		}
	}

	// The certificate is in a file of its own, which the part names.
	files, _ := filepath.Glob(filepath.Join(dir, "default.a.*.pem"))
	if len(files) != 1 {
		t.Fatalf("certificate files: %v", files)
	}
	if data, err := os.ReadFile(files[0]); err != nil || string(data) != string(crt)+"\n"+string(key) {
		t.Errorf("%s: %v", files[0], err)
	}
	if fi, err := os.Stat(files[0]); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s: %v", files[0], fi.Mode())
	}
	waitFor(t, "the HTTPS listener", func() bool {
		ls := current().Listeners
		return len(ls) == 3 && ls[1].TLS != nil && ls[1].TLS.Certificates[0].CertFile == files[0]
	})

	// Written once: resyncs and the watches of what was written write
	// nothing more.
	count := func() int {
		return a.patchCount(kindClasses.path) + a.patchCount(kindGateways.path) + a.patchCount(kindRoutes.path) + a.patchCount(tlsPath)
	}
	before := count()
	time.Sleep(3 * resync)
	if n := count(); n != before {
		t.Fatalf("%d more status writes with nothing changed", n-before)
	}

	// A write refused is made again: the next resync finds the status
	// still to write.
	a.mu.Lock()
	a.conflicts = 1
	a.mu.Unlock()
	a.put(kindRoutes.path, `{"metadata": {"namespace": "default", "name": "r", "generation": 2}, "spec": {"parentRefs": [{"name": "gw", "sectionName": "http"}],
  "rules": [{"backendRefs": [{"name": "gone", "port": 80}]}]}}`)
	waitFor(t, "the route's status after a refused write", func() bool {
		return routeStatus(kindRoutes.path, "r") == "True Accepted, False BackendNotFound"
	})

	// The Secret gone: the listener is not served, and its file goes.
	a.del(kindSecrets.path, secretDoc("default", "a", crt, key))
	waitFor(t, "the certificate file removed", func() bool {
		_, err := os.Stat(files[0])
		return os.IsNotExist(err)
	})
	waitFor(t, "the HTTPS listener's status", func() bool {
		gs, _ := a.object(kindGateways.path, "default", "gw")["status"].(map[string]any)
		return strings.Contains(toJSON(gs["listeners"]), `"reason":"InvalidCertificateRef"`)
	})
}

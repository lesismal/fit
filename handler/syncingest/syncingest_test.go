package syncingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/httpsync"
	"github.com/lesismal/fit/upstream"

	_ "github.com/lesismal/fit/handler/respond"
	_ "github.com/lesismal/fit/middleware/auth"
)

// hang bounds every wait: a guard against a hang, not a measure of speed.
const hang = 2 * time.Minute

// fit is fit serving routes and running sync, as a process does.
type fit struct {
	rt     *runtime.Runtime
	sync   *fitsync.Runtime
	server *listener.Server
	stopUp func()
	base   string
}

var (
	hashOnce sync.Once
	hash     string
)

// conf is a config with sync_ingest on /ingest behind basic_auth (user
// fit, password pw), respond on /ping, and a pipeline name of an http source
// with params into the memory store name.
func conf(dir, name, source, handler string) string {
	hashOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
		if err != nil {
			panic(err)
		}
		hash = string(h)
	})
	return fmt.Sprintf(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [
    {"match": {"path": ["/ingest"]},
     "middleware": [{"module": "basic_auth", "params": {"users": [{"username": "fit", "password_hash": %q}]}}],
     "handler": {"module": "sync_ingest", "params": %s}},
    {"match": {"path": ["/ping"]}, "handler": {"module": "respond", "params": {"body": "pong"}}}
  ],
  "sync": {"state_dir": %q, "pipelines": [{"name": %q,
    "source": {"module": "http", "params": %s},
    "sinks": [{"name": "m", "sink": {"module": "memory", "params": {"store": %q}}, "batch": {"max_records": 50, "max_wait": "1ms"}}]}]}
}`, hash, handler, dir, name, source, name)
}

// start serves doc and runs its sync.
func start(t *testing.T, doc string) *fit {
	t.Helper()
	c, err := config.Load([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	up, stopUp, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	f := &fit{rt: new(runtime.Runtime), stopUp: stopUp, sync: fitsync.NewRuntime(fitsync.Options{Engine: up})}
	g, err := runtime.Compile(c, runtime.Env{Metrics: observe.NewMetrics(), Engine: up})
	if err != nil {
		t.Fatal(err)
	}
	f.rt.Swap(g)
	if f.server, err = listener.Start(f.rt); err != nil {
		t.Fatal(err)
	}
	if err := f.sync.Apply(c.Sync); err != nil {
		t.Fatal(err)
	}
	f.base = "http://" + f.server.Addrs()[0].Addr.String()
	return f
}

// stop stops serving and sync, as fit's stop does.
func (f *fit) stop() {
	_ = f.server.Close()
	f.sync.Close(context.Background())
	f.rt.Close()
	f.stopUp()
}

// waitRunning waits for the pipeline's source to run.
func waitRunning(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(hang)
	for httpsync.Lookup(name) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("%s never ran", name)
		}
		time.Sleep(time.Millisecond)
	}
}

type reply struct {
	status  int
	header  http.Header
	records int
	err     string
}

var client = &http.Client{Timeout: hang}

func post(base, body string, auth bool) (reply, error) {
	req, err := http.NewRequest(http.MethodPost, base+"/ingest", strings.NewReader(body))
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if auth {
		req.SetBasicAuth("fit", "pw")
	}
	resp, err := client.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var a struct {
		Records int    `json:"records"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(b, &a)
	return reply{resp.StatusCode, resp.Header, a.Records, a.Error}, nil
}

// TestRetryingClientLosesNothing checks that clients sending each record
// again until it is answered 200 lose none across a restart of fit, which
// stops serving and sync and starts them again on another address.
func TestRetryingClientLosesNothing(t *testing.T) {
	const name, ids, clients = "ingest-restart", 400, 8
	store := fitsync.MemoryStore(name)
	store.Reset()
	defer store.Reset()
	// Slow writes, so that requests are under way when fit stops.
	store.SetHook(func(context.Context, []*fitsync.Record) error { time.Sleep(time.Millisecond); return nil })
	doc := conf(t.TempDir(), name, `{}`, `{"pipeline": "ingest-restart", "timeout": "10s"}`)
	f := start(t, doc)
	var base atomic.Value
	base.Store(f.base)
	waitRunning(t, name)

	var answered atomic.Int64
	var wg sync.WaitGroup
	deadline := time.Now().Add(hang)
	for c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := c; id < ids; id += clients {
				for {
					r, err := post(base.Load().(string), fmt.Sprintf(`{"id": %d}`, id), true)
					if err == nil && r.status == http.StatusOK && r.records == 1 {
						answered.Add(1)
						break
					}
					if err == nil && r.status != http.StatusServiceUnavailable && r.status != http.StatusNotFound {
						t.Errorf("record %d: %+v", id, r)
						return
					}
					if time.Now().After(deadline) {
						t.Errorf("record %d never answered", id)
						return
					}
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}
	for answered.Load() < ids/3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	f.stop()
	f = start(t, doc)
	defer f.stop()
	base.Store(f.base)
	wg.Wait()

	seen := map[string]bool{}
	for _, r := range store.Records() {
		v, _ := r.After.Get("id")
		seen[fitsync.TextOf(v)] = true
	}
	for id := range ids {
		if !seen[fmt.Sprint(id)] {
			t.Errorf("record %d is lost", id)
		}
	}
}

// TestFullQueueAnswers503 checks a request past the source's pending
// requests is answered 503 at once, with Retry-After, while one is held,
// that fib's workers go on serving meanwhile, and that the held one is
// answered once the sink has its records.
func TestFullQueueAnswers503(t *testing.T) {
	const name = "ingest-full"
	store := fitsync.MemoryStore(name)
	store.Reset()
	defer store.Reset()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	store.SetHook(func(ctx context.Context, _ []*fitsync.Record) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	f := start(t, conf(t.TempDir(), name, `{"max_pending_requests": 1}`, `{"pipeline": "ingest-full", "retry_after": "3s"}`))
	defer f.stop()
	waitRunning(t, name)

	held := make(chan reply, 1)
	go func() {
		r, err := post(f.base, `{"id": 1}`, true)
		if err != nil {
			t.Error(err)
		}
		held <- r
	}()
	select {
	case <-entered:
	case <-time.After(hang):
		t.Fatal("the sink never got the record")
	}
	r, err := post(f.base, `{"id": 2}`, true)
	if err != nil || r.status != http.StatusServiceUnavailable || r.header.Get("Retry-After") != "3" {
		t.Fatalf("%v %+v", err, r)
	}
	for range 20 {
		resp, err := client.Get(f.base + "/ping")
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("ping: %d", resp.StatusCode)
		}
	}
	close(release)
	if r := <-held; r.status != http.StatusOK || r.records != 1 {
		t.Fatalf("%+v", r)
	}
}

// TestAnswers checks the route's other answers: the middleware's 401, 405,
// 413, 415, 400, 503 past the timeout and while no pipeline runs.
func TestAnswers(t *testing.T) {
	const name = "ingest-answers"
	store := fitsync.MemoryStore(name)
	store.Reset()
	defer store.Reset()
	f := start(t, conf(t.TempDir(), name, `{}`, `{"pipeline": "ingest-answers", "timeout": "100ms", "max_body_bytes": 64}`))
	defer f.stop()
	waitRunning(t, name)

	if r, _ := post(f.base, `{"id": 1}`, false); r.status != http.StatusUnauthorized {
		t.Errorf("no credentials: %+v", r)
	}
	if n := len(store.Records()); n != 0 {
		t.Errorf("%d records written without credentials", n)
	}
	req, _ := http.NewRequest(http.MethodGet, f.base+"/ingest", nil)
	req.SetBasicAuth("fit", "pw")
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "POST" {
		t.Errorf("GET: %v %+v", err, resp)
	} else {
		_ = resp.Body.Close()
	}
	if r, _ := post(f.base, `{"id": "`+strings.Repeat("x", 100)+`"}`, true); r.status != http.StatusRequestEntityTooLarge {
		t.Errorf("too large: %+v", r)
	}
	if r, _ := post(f.base, `{"id": `, true); r.status != http.StatusBadRequest || r.err == "" {
		t.Errorf("bad JSON: %+v", r)
	}
	req, _ = http.NewRequest(http.MethodPost, f.base+"/ingest", strings.NewReader("a,b"))
	req.SetBasicAuth("fit", "pw")
	req.Header.Set("Content-Type", "text/csv")
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("csv: %v %+v", err, resp)
	} else {
		_ = resp.Body.Close()
	}

	release := make(chan struct{})
	store.SetHook(func(ctx context.Context, _ []*fitsync.Record) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	})
	if r, _ := post(f.base, `{"id": 2}`, true); r.status != http.StatusServiceUnavailable || r.header.Get("Retry-After") != "1" {
		t.Errorf("past the timeout: %+v", r)
	}
	close(release)

	f.sync.Close(context.Background())
	if r, _ := post(f.base, `{"id": 3}`, true); r.status != http.StatusServiceUnavailable || !strings.Contains(r.err, "not running") {
		t.Errorf("no pipeline: %+v", r)
	}
}

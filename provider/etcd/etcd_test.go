package etcd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider/internal/watch"
)

// fakeEtcd answers the parts of etcd's JSON gateway the provider asks for.
type fakeEtcd struct {
	srv      *httptest.Server
	mu       sync.Mutex
	kv       map[string]string
	rev      int
	watchers map[chan string]bool // each watch's channel of messages to send
	user     string               // the user asked for, with password; none when empty
	password string
	tokens   int
	seen     []string
	noCreate bool // watches are never created
}

func newFakeEtcd(t *testing.T) *fakeEtcd {
	f := &fakeEtcd{kv: map[string]string{}, rev: 1, watchers: map[chan string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.srv.CloseClientConnections()
		f.srv.Close()
	})
	return f
}

// put sets key, or deletes it when value is empty, and tells the watches.
func (f *fakeEtcd) put(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rev++
	if value == "" {
		delete(f.kv, key)
	} else {
		f.kv[key] = value
	}
	for ch := range f.watchers {
		ch <- fmt.Sprintf(`{"result":{"header":{"revision":"%d"},"events":[{"kv":{"key":"%s"}}]}}`, f.rev, key)
	}
}

// broadcast sends msg to every watch.
func (f *fakeEtcd) broadcast(msg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch := range f.watchers {
		ch <- msg
	}
}

func (f *fakeEtcd) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || r.Method != http.MethodPost {
		http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.seen = append(f.seen, r.URL.Path+" auth="+r.Header.Get("Authorization"))
	if f.user != "" && r.URL.Path != "/v3/auth/authenticate" && !strings.HasPrefix(r.Header.Get("Authorization"), "token-") {
		f.mu.Unlock()
		http.Error(w, `{"error":"etcdserver: user name is empty","code":3}`, http.StatusBadRequest)
		return
	}
	switch r.URL.Path {
	case "/v3/auth/authenticate":
		var in struct{ Name, Password string }
		raw, _ := json.Marshal(body)
		json.Unmarshal(raw, &in)
		if in.Name != f.user || in.Password != f.password {
			f.mu.Unlock()
			http.Error(w, `{"error":"etcdserver: authentication failed, invalid user ID or password","code":3}`, http.StatusBadRequest)
			return
		}
		f.tokens++
		tok := fmt.Sprintf("token-%d", f.tokens)
		f.mu.Unlock()
		fmt.Fprintf(w, `{"header":{},"token":%q}`, tok)
	case "/v3/kv/range":
		var in struct {
			Key   []byte `json:"key"`
			Limit int    `json:"limit"`
		}
		raw, _ := json.Marshal(body)
		json.Unmarshal(raw, &in)
		var end []byte
		json.Unmarshal(body["range_end"], &end)
		type kv struct {
			Key   []byte `json:"key"`
			Value []byte `json:"value"`
		}
		var kvs []kv
		for k, v := range f.kv {
			if k >= string(in.Key) && (string(end) == "\x00" || k < string(end)) {
				kvs = append(kvs, kv{[]byte(k), []byte(v)})
			}
		}
		slices.SortFunc(kvs, func(a, b kv) int { return bytes.Compare(a.Key, b.Key) })
		more := false
		if in.Limit > 0 && len(kvs) > in.Limit {
			kvs, more = kvs[:in.Limit], true
		}
		out := map[string]any{"header": map[string]string{"revision": fmt.Sprint(f.rev)}, "kvs": kvs, "more": more, "count": fmt.Sprint(len(kvs))}
		f.mu.Unlock()
		json.NewEncoder(w).Encode(out)
	case "/v3/watch":
		ch := make(chan string, 16)
		if !f.noCreate {
			ch <- `{"result":{"header":{"revision":"1"},"created":true}}`
		}
		f.watchers[ch] = true
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			delete(f.watchers, ch)
			f.mu.Unlock()
		}()
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case msg := <-ch:
				if msg == "" { // end the stream
					return
				}
				fmt.Fprintln(w, msg)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	default:
		f.mu.Unlock()
		http.NotFound(w, r)
	}
}

func (f *fakeEtcd) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

// background runs run until the test ends, and waits for it to return.
func background(t *testing.T, run func(context.Context, func([]byte)), update func([]byte)) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, update)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func shorten(t *testing.T) {
	d, lo, hi := watch.Debounce, watch.MinBackoff, watch.MaxBackoff
	watch.Debounce, watch.MinBackoff, watch.MaxBackoff = 20*time.Millisecond, 10*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { watch.Debounce, watch.MinBackoff, watch.MaxBackoff = d, lo, hi })
}

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *logs) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// waitPart waits for the part want, passing over others: the follower
// sends one again every refresh_interval.
func waitPart(t *testing.T, ch <-chan []byte, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var last []byte
	for {
		select {
		case p := <-ch:
			if string(p) == want {
				return
			}
			last = p
		case <-deadline:
			t.Fatalf("no part %s; the last: %s", want, last)
		}
	}
}

func route(name string) string {
	return `{"routes": [{"name": "` + name + `", "handler": {"module": "respond"}}]}`
}

func routesPart(names ...string) string {
	var rs []string
	for _, n := range names {
		rs = append(rs, `{"name":"`+n+`","handler":{"module":"respond"}}`)
	}
	if rs == nil {
		return "{}"
	}
	return `{"routes":[` + strings.Join(rs, ",") + `]}`
}

func TestProvider(t *testing.T) {
	shorten(t)
	f := newFakeEtcd(t)
	f.user, f.password = "fit", "pa ss"
	f.put("fit/a", route("a"))
	f.put("fit/bad", "nope")
	f.put("fiu", route("outside")) // past the prefix's range
	f.put("fis", route("before"))
	pw := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pw, []byte("pa ss\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var l logs
	// The first endpoint is not there: the provider goes on to the next.
	p := New("etcd", &config.EtcdProvider{
		Endpoints: []string{"http://127.0.0.1:1", f.srv.URL + "/"}, Username: "fit", PasswordFile: pw,
		RefreshInterval: config.Duration(time.Hour),
	}, l.logf)
	if p.Name() != "etcd" {
		t.Fatalf("Name: %q", p.Name())
	}
	parts := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.Run(ctx, func(b []byte) { parts <- b })
	}()
	waitPart(t, parts, routesPart("a"))
	f.put("fit/b", route("b"))
	waitPart(t, parts, routesPart("a", "b"))
	f.put("fit/a", "")
	waitPart(t, parts, routesPart("b"))
	// Progress notifications, with no events, read nothing.
	before := len(f.requests())
	f.broadcast(`{"result":{"header":{"revision":"9"}}}`)
	time.Sleep(100 * time.Millisecond)
	if after := len(f.requests()); after != before {
		t.Errorf("a progress notification made %d requests", after-before)
	}
	// The watch is canceled, as after a compaction: the provider watches
	// again, on the endpoints in turn, and reads at once.
	f.broadcast(`{"result":{"header":{},"canceled":true,"compact_revision":"5","cancel_reason":"mvcc: required revision has been compacted"}}`)
	f.put("fit/c", route("c"))
	waitPart(t, parts, routesPart("b", "c"))
	// The stream ends.
	f.broadcast("")
	f.put("fit/b", "")
	waitPart(t, parts, routesPart("c"))
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	all := l.all()
	for _, want := range []string{
		"provider etcd: key fit/bad: ",
		"127.0.0.1:1",
		"etcd canceled the watch: mvcc: required revision has been compacted (compacted at revision 5)",
		"etcd ended the watch",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("logs lack %q:\n%s", want, all)
		}
	}
	for _, r := range f.requests() {
		if !strings.HasPrefix(r, "/v3/auth/authenticate auth=") && !strings.Contains(r, "auth=token-") {
			t.Errorf("a request without a token: %s", r)
		}
	}
}

func TestProviderMaxKeys(t *testing.T) {
	shorten(t)
	f := newFakeEtcd(t)
	f.put("fit/1", route("one"))
	f.put("fit/2", route("two"))
	f.put("fit/3", route("three"))
	var l logs
	p := New("etcd", &config.EtcdProvider{Endpoints: []string{f.srv.URL}, MaxKeys: 2, Prefix: "fit/", RefreshInterval: config.Duration(time.Hour)}, l.logf)
	parts := make(chan []byte, 4)
	background(t, p.Run, func(b []byte) { parts <- b })
	waitPart(t, parts, routesPart("one", "two"))
	if !strings.Contains(l.all(), "provider etcd: the keys past the first 2 under fit/ are left out: max_keys") {
		t.Errorf("logs:\n%s", l.all())
	}
}

func TestProviderWatchNotCreated(t *testing.T) {
	shorten(t)
	f := newFakeEtcd(t)
	f.noCreate = true
	var l logs
	p := New("etcd", &config.EtcdProvider{Endpoints: []string{f.srv.URL}, RequestTimeout: config.Duration(50 * time.Millisecond)}, l.logf)
	background(t, p.Run, func([]byte) { t.Error("a part without a watch") })
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(l.all(), "the watch was not created within 50ms") {
		if time.Now().After(deadline) {
			t.Fatalf("logs:\n%s", l.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProviderBadPassword(t *testing.T) {
	shorten(t)
	f := newFakeEtcd(t)
	f.user, f.password = "fit", "right"
	pw := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(pw, []byte("wrong"), 0o600); err != nil {
		t.Fatal(err)
	}
	var l logs
	p := New("etcd", &config.EtcdProvider{Endpoints: []string{f.srv.URL}, Username: "fit", PasswordFile: pw}, l.logf)
	background(t, p.Run, func([]byte) { t.Error("a part without authenticating") })
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(l.all(), "authentication failed") {
		if time.Now().After(deadline) {
			t.Fatalf("logs:\n%s", l.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRangeEnd(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"fit/", "fit0"},
		{"a\xff", "b"},
		{"\xff\xff", "\x00"},
		{"", "\x00"},
	} {
		if got := string(rangeEnd([]byte(c.in))); got != c.want {
			t.Errorf("rangeEnd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReadWatch(t *testing.T) {
	var ready, changed int
	err := readWatch(strings.NewReader(`{"result":{"created":true}}`+"\n\n"+`{"result":{"events":[{}]}}`+"\n"+`{"error":{"message":"boom"}}`+"\n"),
		func() { ready++ }, func() { changed++ })
	if err == nil || err.Error() != "the watch failed: boom" || ready != 1 || changed != 1 {
		t.Errorf("readWatch: %v, ready %d, changed %d", err, ready, changed)
	}
	if err := readWatch(strings.NewReader(`{"result":{"events":[{}]}}`), func() {}, func() {}); err == nil || !strings.Contains(err.Error(), "does not say it is created") {
		t.Errorf("a first message not created: %v", err)
	}
	if err := readWatch(strings.NewReader(`nope`), func() {}, func() {}); err == nil || !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("not JSON: %v", err)
	}
}

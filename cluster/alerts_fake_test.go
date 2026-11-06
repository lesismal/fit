package cluster

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
)

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *testClock {
	return &testClock{t: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// offsetClock is the real time plus an offset tests move forward, so that
// a manager's poller keeps polling while the tests skip ahead.
type offsetClock struct {
	mu  sync.Mutex
	off time.Duration
}

func (c *offsetClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.off)
}

func (c *offsetClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.off += d
}

// bareManager is a manager whose alerts are evaluated by the test alone:
// no poller and no evaluator run, its store is in memory, and its clock is
// the test's.
func bareManager(t *testing.T, clock func() time.Time) (*Manager, *memStore, *logBuf) {
	t.Helper()
	st, log := newMemStore(), &logBuf{}
	m := &Manager{o: Options{Engine: testEngine(t), Settings: &config.Cluster{}, Store: st, Logf: log.logf, Now: clock, NodeID: "mgr-1"},
		stop: make(chan struct{}), maxNodes: 256, nodes: map[string]*node{}}
	m.loadAlerts()
	close(m.al.done)
	t.Cleanup(func() { m.stopAlerts(time.Second) })
	return m, st, log
}

// setRules replaces the bare manager's rules and webhooks.
func setRules(m *Manager, rules []AlertRule, hooks ...Webhook) {
	for i := range rules {
		if err := rules[i].check(); err != nil {
			panic(err)
		}
	}
	m.al.mu.Lock()
	m.al.cfg.rules, m.al.cfg.webhooks = rules, hooks
	m.al.mu.Unlock()
	m.syncSenders()
}

// hookReq is a request a webhook got.
type hookReq struct {
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// payloadOf is a request's body as fit's JSON.
func (r hookReq) payload(t *testing.T) payload {
	t.Helper()
	var p payload
	if err := json.Unmarshal(r.Body, &p); err != nil {
		t.Fatalf("%s: %v", r.Body, err)
	}
	return p
}

// hookSink is a webhook: it keeps what it gets, and answers status and
// answer, after hold is closed when it is set.
type hookSink struct {
	srv *httptest.Server

	mu     sync.Mutex
	got    []hookReq
	status int
	answer string
	hold   chan struct{}
}

func newHookSink(t *testing.T) *hookSink {
	t.Helper()
	h := &hookSink{status: 200, answer: `{}`}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.got = append(h.got, hookReq{Path: r.URL.Path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: body})
		hold, status, answer := h.hold, h.status, h.answer
		h.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(func() {
		h.release()
		h.srv.Close()
	})
	return h
}

func (h *hookSink) set(fn func(h *hookSink)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

// release lets the requests held go.
func (h *hookSink) release() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hold != nil {
		close(h.hold)
		h.hold = nil
	}
}

func (h *hookSink) requests() []hookReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hookReq{}, h.got...)
}

// wait waits for the webhook to have got n requests, and returns them.
func (h *hookSink) wait(t *testing.T, n int) []hookReq {
	t.Helper()
	var got []hookReq
	waitFor(t, "the webhook's requests", func() bool { got = h.requests(); return len(got) >= n })
	return got
}

// hook is a webhook named name to h, in format.
func (h *hookSink) hook(name, format string) Webhook {
	w := Webhook{Name: name, URL: h.srv.URL + "/hook/" + name + "?token=sekret-token", Format: format, TimeoutMs: 5000, SendResolved: true}
	return w
}

// senderOf returns the bare manager's sender of the webhook name.
func senderOf(m *Manager, name string) *sender {
	m.al.mu.Lock()
	defer m.al.mu.Unlock()
	return m.al.senders[name]
}

// waitSent waits for the webhook name to have sent or failed n
// notifications, and returns its status.
func waitSent(t *testing.T, m *Manager, name string, n uint64) WebhookStatus {
	t.Helper()
	var st WebhookStatus
	waitFor(t, "the webhook "+name+" to send", func() bool {
		st = senderOf(m, name).status()
		return st.Sent+st.Failed >= n && st.Queued == 0
	})
	return st
}

// up and down are members' states as the poller leaves them.
func up(name string, polled time.Time, groups ...string) State {
	return State{Name: name, Status: StatusUp, Since: polled, Polled: polled, Groups: append([]string{}, groups...)}
}

func down(name string, since time.Time, groups ...string) State {
	return State{Name: name, Status: StatusDown, Since: since, Polled: since, Groups: append([]string{}, groups...),
		Error: &Problem{Kind: KindUnreachable, Message: "connection refused"}}
}

// eval evaluates the bare manager's rules at now on states, and delivers
// what is due.
func eval(m *Manager, now time.Time, in *evalInput) {
	if in.syncs == nil {
		in.syncs = map[string]MemberSync{}
	}
	m.deliver(m.evaluate(now, in))
}

// alertOf returns the alert of rule on node, firing or pending, or nil.
func alertOf(m *Manager, rule, node, subject string) *Alert {
	m.al.mu.Lock()
	defer m.al.mu.Unlock()
	key := alertKey(rule, node, subject)
	if a := m.al.active[key]; a != nil {
		c := *a
		return &c
	}
	if a := m.al.pending[key]; a != nil {
		c := *a
		return &c
	}
	return nil
}

// history returns the bare manager's resolved alerts, the oldest first.
func history(m *Manager) []Alert {
	m.al.mu.Lock()
	defer m.al.mu.Unlock()
	return append([]Alert{}, m.al.history...)
}

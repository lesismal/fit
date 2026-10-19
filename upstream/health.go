package upstream

import (
	"crypto/tls"
	"math/rand/v2"
	"net"
	stdhttp "net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	fib "github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

// prober runs an upstream's active health check: a probe of each backend
// every interval, plus jitter, each one started by a timer and finished in
// fib's callback, so that no goroutine waits for a probe and none runs on a
// listener's worker. A backend's probes follow one another: the next is
// scheduled once the last is over. Probe results go to the backend's state,
// which Pick reads.
type prober struct {
	check  *config.HealthCheck
	engine *fib.Engine
	client *fibhttp.Client // for http probes; nil for tcp
	scheme string

	interval, timeout, jitter time.Duration
	fails, passes             int

	stopped atomic.Bool
	mu      sync.Mutex // guards timers against stop
	timers  []*time.Timer
	targets []*probeTarget
}

// probeTarget is one backend's probes and the run of results they have had.
type probeTarget struct {
	backend *Backend
	address string // where probes go: the backend's address, or its host and the check's port
	url     string // for http probes
	// streak counts the probes in a row that went against the backend's
	// state: failures while it is up, passes while it is down. Only the
	// backend's own probes touch it, one after another.
	streak int
}

// newProber returns the prober of a pool's backends, sending http probes
// with the pool's TLS settings and protocol. Start starts it.
func newProber(engine *fib.Engine, c *config.Upstream, scheme string, tlsConfig *tls.Config, backends []*Backend) *prober {
	h := c.HealthCheck
	pr := &prober{check: h, engine: engine, scheme: scheme, interval: h.Every(), timeout: h.Wait(), jitter: h.Spread(),
		fails: h.FailsNeeded(), passes: h.PassesNeeded()}
	if h.Type != config.HealthTCP {
		cc := fibhttp.DefaultClientConfig()
		cc.Timeout = pr.timeout
		cc.DialTimeout = pr.timeout
		// One probe at a time to each address, each on a connection of its
		// own, so that a probe sees whether the backend takes new
		// connections, and never waits behind the pool's requests. Two
		// connections, so that the next probe need not wait for the last
		// one's to finish closing, which takes a while when it was cut off
		// in the middle of a large body.
		cc.MaxConnsPerHost = 2
		cc.MaxIdleConnsPerHost = 0
		// The status is all a probe needs: the callback runs once the header
		// has arrived, and the body is thrown away.
		cc.StreamResponseBody = true
		cc.StreamResponseBodyThreshold = 0
		cc.MaxResponseHeaderBytes = 64 << 10
		cc.TLSConfig = tlsConfig
		cc.UnencryptedHTTP2 = c.Protocol == config.ProtocolH2C
		pr.client = fibhttp.NewClient(engine, cc)
	}
	path := h.Path
	if path == "" {
		path = "/"
	}
	for _, b := range backends {
		t := &probeTarget{backend: b, address: b.Address}
		if h.Port != 0 {
			host, _, _ := net.SplitHostPort(b.Address)
			t.address = net.JoinHostPort(host, strconv.Itoa(h.Port))
		}
		t.url = scheme + "://" + t.address + path
		pr.targets = append(pr.targets, t)
	}
	return pr
}

// start schedules each backend's first probe, within a jitter of now.
func (pr *prober) start() {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	for i, t := range pr.targets {
		pr.timers = append(pr.timers, time.AfterFunc(pr.spread(), func() { pr.probe(i, t) }))
	}
}

// stop stops the probes: none starts after it, and the result of one under
// way is dropped.
func (pr *prober) stop() {
	if pr.stopped.Swap(true) {
		return
	}
	pr.mu.Lock()
	for _, t := range pr.timers {
		t.Stop()
	}
	pr.mu.Unlock()
	if pr.client != nil {
		pr.client.Close()
	}
}

func (pr *prober) spread() time.Duration {
	if pr.jitter <= 0 {
		return 0
	}
	return rand.N(pr.jitter)
}

// probe sends one probe to t, and schedules the next once it is over.
func (pr *prober) probe(i int, t *probeTarget) {
	if pr.stopped.Load() {
		return
	}
	began := time.Now()
	done := func(passed bool) {
		if pr.stopped.Load() {
			return
		}
		pr.record(t, passed)
		next := pr.interval - time.Since(began) + pr.spread()
		pr.mu.Lock()
		if !pr.stopped.Load() {
			pr.timers[i].Reset(max(next, 0))
		}
		pr.mu.Unlock()
	}
	if pr.client == nil {
		pr.probeTCP(t, done)
		return
	}
	pr.probeHTTP(t, done)
}

// probeTCP passes once a connection to t opens, and closes it at once.
func (pr *prober) probeTCP(t *probeTarget, done func(bool)) {
	err := pr.engine.DialWithHandler("tcp", t.address, pr.timeout, discard{}, func(c *fib.Connection, err error) {
		if c != nil {
			_ = c.Close()
		}
		done(err == nil)
	})
	if err != nil {
		done(false)
	}
}

// probeHTTP sends GET to t and passes on an expected status.
func (pr *prober) probeHTTP(t *probeTarget, done func(bool)) {
	req, err := stdhttp.NewRequest(stdhttp.MethodGet, t.url, nil)
	if err != nil {
		done(false)
		return
	}
	if pr.check.Host != "" {
		req.Host = pr.check.Host
	}
	req.Header.Set("User-Agent", "fit-health-check")
	pr.client.Do(req, func(resp *fibhttp.ClientResponse, err error) {
		if err != nil {
			done(false)
			return
		}
		status := resp.StatusCode
		resp.Close()
		done(pr.expected(status))
	})
}

func (pr *prober) expected(status int) bool {
	if len(pr.check.ExpectStatus) == 0 {
		return status >= 200 && status < 400
	}
	for _, s := range pr.check.ExpectStatus {
		if s == status {
			return true
		}
	}
	return false
}

// record counts a probe's result: fails failures in a row take the
// backend out of rotation, and passes passes in a row put it back.
func (pr *prober) record(t *probeTarget, passed bool) {
	down := t.backend.ProbedDown()
	if passed != down {
		t.streak = 0 // a result that agrees with the state ends any run against it
		return
	}
	t.streak++
	if down && t.streak >= pr.passes {
		t.streak = 0
		t.backend.setProbedDown(false)
	} else if !down && t.streak >= pr.fails {
		t.streak = 0
		t.backend.setProbedDown(true)
	}
}

// discard is the handler of a tcp probe's connection, which is closed as
// soon as it opens.
type discard struct{}

func (discard) OnOpen(*fib.Connection)                 {}
func (discard) OnData(*fib.Connection, []byte)         {}
func (discard) OnPriorityData(*fib.Connection, []byte) {}
func (discard) OnClose(*fib.Connection, error)         {}

package upstream

import (
	stdhttp "net/http"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
)

// TestWeightedRoundRobinWithoutSchedule picks by weight without a
// precomputed sequence: for weights of none, and for weights whose sequence
// would be too long, with one backend down and then all.
func TestWeightedRoundRobinWithoutSchedule(t *testing.T) {
	if w := newWeightedRoundRobin(backends(0, 0)); w.schedule != nil {
		t.Fatalf("a schedule of %d for weights 0", len(w.schedule))
	}
	bs := backends(maxSchedule, 1)
	w := newWeightedRoundRobin(bs)
	if w.schedule != nil {
		t.Fatalf("a schedule of %d entries", len(w.schedule))
	}
	bs[0].setProbedDown(true)
	if got := counts(w, 10); got["b"] != 10 {
		t.Fatalf("a down: %v", got)
	}
	bs[1].setProbedDown(true)
	if got := counts(w, maxSchedule+1); got["a"] != maxSchedule || got["b"] != 1 {
		t.Fatalf("all down: %v", got)
	}
}

// TestWeightedRandomAllDown draws by weight over every backend once none is
// up.
func TestWeightedRandomAllDown(t *testing.T) {
	bs := backends(3, 1)
	bal := NewBalancer(Random, bs, "")
	bs[0].failed(1, time.Hour)
	bs[1].failed(1, time.Hour)
	if got := counts(bal, 400); got["a"] == 0 || got["b"] == 0 {
		t.Fatalf("all down: %v", got)
	}
}

// TestHashRingEdges hashes onto a ring whose backends share an address,
// with every backend down, and a URI hash of a request with no URL.
func TestHashRingEdges(t *testing.T) {
	bs := []*Backend{{Address: "a", Weight: 1}, {Address: "a", Weight: 1}}
	ring := NewBalancer(IPHash, bs, "")
	bs[0].failed(1, time.Hour)
	bs[1].failed(1, time.Hour)
	r := &stdhttp.Request{RemoteAddr: "192.0.2.1:1"}
	if b := ring.Pick(r); b == nil || b.Address != "a" {
		t.Fatalf("all down: %v", b)
	}
	uri := NewBalancer(URIHash, backends(1, 1), "")
	if b := uri.Pick(&stdhttp.Request{}); b == nil {
		t.Fatal("no backend for a request without a URL")
	}
}

func TestStickyNilRequest(t *testing.T) {
	s := newSticky(&config.StickyCookie{Name: "s"}, backends(1, 1))
	if b := s.pick(nil); b != nil {
		t.Fatalf("picked %v", b.Address)
	}
}

// TestProberEdges runs a prober's parts on their own: no jitter, a probe
// once stopped, a tcp probe of an address that cannot be dialed, an http
// probe of a URL no request can be made for, which fails before the
// stopped prober's client is used, and expected statuses.
func TestProberEdges(t *testing.T) {
	engine := startEngine(t)
	c, err := decode(t, `{"addresses": ["127.0.0.1:1"], "health_check": {"expect_status": [204]}}`)
	if err != nil {
		t.Fatal(err)
	}
	pr := newProber(engine, c, "http", nil, []*Backend{{Address: "127.0.0.1:1", Weight: 1}})
	pr.jitter = 0
	if d := pr.spread(); d != 0 {
		t.Fatalf("spread %v without jitter", d)
	}
	pr.stop()
	pr.probe(0, pr.targets[0]) // nothing happens once stopped

	results := make(chan bool, 2)
	pr.probeTCP(&probeTarget{address: "not an address"}, func(ok bool) { results <- ok })
	if <-results {
		t.Error("a tcp probe of a bad address passed")
	}
	pr.probeHTTP(&probeTarget{url: "http://bad\x7fhost/"}, func(ok bool) { results <- ok })
	if <-results {
		t.Error("an http probe of a bad URL passed")
	}
	if !pr.expected(204) || pr.expected(200) {
		t.Error("expect_status not kept")
	}
}

// TestSendUpgradeFails sends upgrades through a pool with no engine and
// through one that is closed.
func TestSendUpgradeFails(t *testing.T) {
	c, err := decode(t, `{"addresses": ["127.0.0.1:1"]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []bool{false, true} {
		p := mustNew(t, nil, c)
		want := ErrNoEngine
		if engine {
			p = mustNew(t, startEngine(t), c)
			want = ErrClosed
		}
		p.Close()
		req, _ := stdhttp.NewRequest("GET", "http://x/", nil)
		var got error
		p.SendUpgrade(p.Backends()[0], req, nil, func(_ *fibhttp.ClientResponse, err error) { got = err })
		if got != want {
			t.Errorf("engine %v: %v, want %v", engine, got, want)
		}
	}
}

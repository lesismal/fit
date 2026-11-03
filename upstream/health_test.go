package upstream

import (
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthMaxFails(t *testing.T) {
	bs := backends(1, 1, 1)
	b := bs[1]
	b.failed(2, time.Hour)
	if b.Down() {
		t.Fatal("down after 1 of 2 failures")
	}
	b.failed(2, time.Hour)
	if !b.Down() {
		t.Fatal("up after 2 of 2 failures")
	}
	b.succeeded()
	if b.Down() {
		t.Fatal("down after a success")
	}
	// Failures outside the window do not add up.
	b.failed(2, 30*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	b.failed(2, 30*time.Millisecond)
	if b.Down() {
		t.Fatal("down after failures in two windows")
	}
	// No max_fails: never down.
	b.failed(0, time.Hour)
	b.failed(0, time.Hour)
	b.failed(0, time.Hour)
	if b.Down() {
		t.Fatal("down with max_fails 0")
	}
}

func TestPickSkipsDown(t *testing.T) {
	for _, policy := range []string{RoundRobin, WeightedRoundRobin, LeastConn} {
		bs := backends(1, 1, 1)
		bal := NewBalancer(policy, bs, "")
		bs[1].failed(1, 100*time.Millisecond)
		got := counts(bal, 300)
		if got["b"] != 0 || got["a"] != 150 || got["c"] != 150 {
			t.Errorf("%s with b down: %v", policy, got)
		}
		if n := testing.AllocsPerRun(100, func() { bal.Pick(nil) }); n != 0 {
			t.Errorf("%s: %v allocations a pick with a backend down", policy, n)
		}
		// All down: health is ignored rather than failing everything.
		bs[0].failed(1, 100*time.Millisecond)
		bs[2].failed(1, 100*time.Millisecond)
		if got := counts(bal, 300); got["a"]+got["b"]+got["c"] != 300 || got["a"] == 0 || got["c"] == 0 {
			t.Errorf("%s with all down: %v", policy, got)
		}
		// After the window, they are back.
		time.Sleep(120 * time.Millisecond)
		if got := counts(bal, 300); got["a"] != 100 || got["b"] != 100 || got["c"] != 100 {
			t.Errorf("%s after the window: %v", policy, got)
		}
	}
}

func TestLeaveAndRejoin(t *testing.T) {
	good := newBackend(t, hello("g"))
	// flaky listens on a port the test closes and opens again.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	flakyAddr := ln.Addr().String()
	flaky := httptest.NewUnstartedServer(stdhttp.HandlerFunc(hello("f")))
	flaky.Listener.Close()
	flaky.Listener = ln
	flaky.Start()
	c, err := decode(t, `{"addresses": ["`+good.addr+`", "`+flakyAddr+`"], "fail_timeout": "300ms"}`)
	if err != nil {
		t.Fatal(err)
	}
	p := mustNew(t, startEngine(t), c)
	defer p.Close()
	fb, failTimeout := p.Backends()[1], 300*time.Millisecond
	// fail_timeout alone keeps flaky out, and a run slowed down enough, as
	// one under -race beside other loads is, spends it within the requests
	// below: flaky is then rightly tried again, and fails again. So rather
	// than count on the requests being quick, seen checks what the window
	// promises of each request: one to flaky that failed took it out, and
	// none went to it while it was out, the window in force from the
	// request's start to its answer. errs holds the errors seen, which a
	// failure reports: the count alone does not tell a refused dial from a
	// retry or a request turned away.
	var errs []string
	seen := func(n int) map[string]int {
		t.Helper()
		got := map[string]int{}
		for range n {
			until, start := fb.downUntil.Load(), time.Now().UnixNano()
			r := do(t, p, "/")
			// The callback runs before Done: wait for Done to count the
			// request's failure or success.
			settle(t, fb, 0)
			if (r.err != nil || r.body[:1] == "f") && until != 0 && time.Now().UnixNano() < until {
				t.Fatalf("flaky took a request while down: body %q, error %v; errors before %q", r.body, r.err, errs)
			}
			if r.err != nil {
				got["error"]++
				errs = append(errs, r.err.Error())
				if fb.downUntil.Load() < start+int64(failTimeout) {
					t.Fatalf("a failure did not take flaky out for fail_timeout: %q", errs)
				}
				continue
			}
			got[r.body[:1]]++
		}
		return got
	}
	if got := seen(10); got["g"] != 5 || got["f"] != 5 {
		t.Fatalf("both up: %v", got)
	}

	flaky.Close() // its port now refuses connections
	// The first request to flaky fails, and seen holds the rest to the
	// window: one error in a quick run, one more each fail_timeout in a
	// slow one.
	if got := seen(20); got["error"] < 1 || got["error"]+got["g"] != 20 {
		t.Fatalf("one failure should take flaky out: %v, errors %q", got, errs)
	}

	// Back on the same port; after fail_timeout it is tried again.
	ln, err = net.Listen("tcp", flakyAddr)
	if err != nil {
		t.Skipf("the port did not come back: %v", err)
	}
	again := httptest.NewUnstartedServer(stdhttp.HandlerFunc(hello("f")))
	again.Listener.Close()
	again.Listener = ln
	again.Start()
	defer again.Close()
	// While its window lasts, seen fails a request that reaches flaky.
	if got := seen(10); got["error"] != 0 {
		t.Fatalf("back on its port: %v, errors %q", got, errs)
	}
	time.Sleep(time.Until(time.Unix(0, fb.downUntil.Load())) + 50*time.Millisecond)
	if got := seen(10); got["g"] != 5 || got["f"] != 5 {
		t.Fatalf("after rejoining: %v", got)
	}
	if fb.Down() {
		t.Fatal("flaky is still down")
	}
}

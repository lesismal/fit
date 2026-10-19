package main

import (
	"math"
	"testing"
	"time"
)

const wrkOutput = `Running 10s test @ http://127.0.0.1:8080/fixed
  4 threads and 256 connections
  Thread Stats   Avg      Stdev     Max   +/- Stdev
    Latency     1.20ms  300.00us  20.00ms   90.00%
  1000000 requests in 10.00s, 120.00MB read
Requests/sec: 100000.00
Transfer/sec:     12.00MB
FITBENCH {"requests":1000000,"duration_us":10000000,"bytes":125829120,"errors":{"connect":0,"read":2,"write":0,"status":3,"timeout":1},"latency_us":{"p50":1100,"p90":1500,"p99":2500,"p999":9000,"max":20000,"mean":1200.50,"stdev":300.25}}
`

const h2loadOutput = `starting benchmark...
spawning thread #0: 16 total client(s). Timing-based test with 0s of warm-up time and 10s of main duration for measurements.
Main benchmark duration is over for thread #0. Stopping all clients.

finished in 10.00s, 160367.30 req/s, 7.03MB/s
requests: 1603673 total, 1603673 started, 1603673 done, 1603662 succeeded, 11 failed, 2 errored, 1 timeout
status codes: 1603662 2xx, 0 3xx, 4 4xx, 5 5xx
traffic: 70.34MB (73753350) total, 9.18MB (9622038) headers (space savings 90.24%), 18.35MB (19244076) data
                     min         max         mean         sd        +/- sd
time for request:       77us     14.55ms      3.17ms      1.15ms    70.81%
time for connect:      205us       402us       330us        82us    56.25%
time to 1st byte:     1.21ms      1.43ms      1.31ms        74us    62.50%
req/s           :    9887.34    10161.44    10022.95       88.86    62.50%
`

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestParseWrk(t *testing.T) {
	var r Result
	if err := parseWrk(wrkOutput, &r); err != nil {
		t.Fatal(err)
	}
	l := r.Latency
	if r.Requests != 1000000 || !near(r.RequestsPerSec, 100000) || r.Errors != 6 ||
		!near(l.P50, 1.1) || !near(l.P90, 1.5) || !near(l.P99, 2.5) || !near(l.P999, 9) || !near(l.Max, 20) ||
		!near(l.Mean, 1.2005) || !near(l.Stdev, 0.30025) ||
		len(r.ErrorKinds) != 3 || r.ErrorKinds["read"] != 2 || r.ErrorKinds["status"] != 3 || r.ErrorKinds["timeout"] != 1 {
		t.Fatalf("%+v %+v", r, *l)
	}
	if err := parseWrk("Running 10s test\nunable to connect\n", &r); err == nil {
		t.Fatal("output without a report parsed")
	}
}

func TestParseH2load(t *testing.T) {
	var r Result
	if err := parseH2load(h2loadOutput, &r); err != nil {
		t.Fatal(err)
	}
	l := r.Latency
	// failed takes in the 2 errored (1 of which timed out), 4 4xx and 5 5xx.
	if r.Requests != 1603673 || !near(r.RequestsPerSec, 160367.30) || r.Errors != 11 ||
		!near(l.Max, 14.55) || !near(l.Mean, 3.17) || !near(l.Stdev, 1.15) || l.P99 != 0 ||
		len(r.ErrorKinds) != 5 || r.ErrorKinds["failed"] != 11 || r.ErrorKinds["5xx"] != 5 {
		t.Fatalf("%+v %+v", r, *l)
	}
	if err := parseH2load("starting benchmark...\n", &r); err == nil {
		t.Fatal("output without a summary parsed")
	}
}

func TestCgroup(t *testing.T) {
	cpu := "usage_usec 2500000\nuser_usec 2000000\nsystem_usec 500000\n"
	if d, ok := cgroupCPU(cpu); !ok || d != 2500*time.Millisecond {
		t.Fatalf("cpu: %v %v", d, ok)
	}
	mem := "anon 1048576\nfile 4096\nkernel 8192\n"
	if n, ok := cgroupAnon(mem); !ok || n != 1<<20 {
		t.Fatalf("anon: %v %v", n, ok)
	}
	if _, ok := cgroupAnon("file 1\n"); ok {
		t.Fatal("found anon where there is none")
	}
}

func TestSplitCPUs(t *testing.T) {
	for _, tt := range []struct {
		n       int
		backend bool
		want    [3]string
	}{
		{2, true, [3]string{}},
		{4, true, [3]string{"0", "1-2", "3"}},
		{8, false, [3]string{"0-3", "4-7", ""}},
		{14, true, [3]string{"0-5", "6-12", "13"}},
		{16, true, [3]string{"0-6", "7-13", "14-15"}},
	} {
		if got := splitCPUs(tt.n, tt.backend); got != tt.want {
			t.Errorf("splitCPUs(%d, %v) = %q, want %q", tt.n, tt.backend, got, tt.want)
		}
	}
	for set, want := range map[string]int{"": 0, "3": 1, "0-3": 4, "0-3,8,10-11": 7, "3-1": 0, "x": 0} {
		if got := cpusetCount(set); got != want {
			t.Errorf("cpusetCount(%q) = %d, want %d", set, got, want)
		}
	}
}

func TestPick(t *testing.T) {
	got, err := pickNames("proxy,fixed", scenarioNames())
	if err != nil || len(got) != 2 || got[0] != "fixed" || got[1] != "proxy" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := pickNames("fixed,bogus", scenarioNames()); err == nil {
		t.Fatal("an unknown scenario was picked")
	}
}

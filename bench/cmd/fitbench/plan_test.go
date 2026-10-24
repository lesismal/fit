package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// A fakeMachine describes a machine for fakeSys to lay out as Linux's /sys
// and /proc would.
type fakeMachine struct {
	cores, threads, nodes int
	mhz                   int
	memGiB                int
	somaxconn             int
	nrOpen                int64
	ports                 [2]int
	rmemMax               int64
	tcpMem                int64 // the pressure value, in 4 KiB pages
}

// benchHost is the benchmark host: a Ryzen 7 7800X3D, CPUs n and n+8 the
// threads of core n.
var benchHost = fakeMachine{cores: 8, threads: 2, nodes: 1, mhz: 5053, memGiB: 62, somaxconn: 2048,
	nrOpen: 2000500, ports: [2]int{1024, 65535}, rmemMax: 7500000, tcpMem: 262144}

// fakeSys lays f out as a filesystem: CPU n's siblings are n + k*cores, as
// Linux numbers them on x86, and the nodes take the cores in equal runs.
func fakeSys(f fakeMachine) fstest.MapFS {
	fsys := fstest.MapFS{}
	put := func(name, data string) { fsys[name] = &fstest.MapFile{Data: []byte(data + "\n")} }
	n := f.cores * f.threads
	put("sys/devices/system/cpu/online", fmt.Sprintf("0-%d", n-1))
	nodeCPUs := make([][]int, f.nodes)
	for c := 0; c < f.cores; c++ {
		var sib []int
		for t := 0; t < f.threads; t++ {
			sib = append(sib, c+t*f.cores)
		}
		node := c * f.nodes / f.cores
		nodeCPUs[node] = append(nodeCPUs[node], sib...)
		for _, cpu := range sib {
			put(fmt.Sprintf("sys/devices/system/cpu/cpu%d/topology/thread_siblings_list", cpu), formatCPUList(sib))
			put(fmt.Sprintf("sys/devices/system/cpu/cpu%d/cpufreq/cpuinfo_max_freq", cpu), fmt.Sprint(f.mhz*1000))
		}
	}
	for i, cpus := range nodeCPUs {
		put(fmt.Sprintf("sys/devices/system/node/node%d/cpulist", i), formatCPUList(cpus))
	}
	put("proc/cpuinfo", "processor\t: 0\nmodel name\t: Fake CPU\ncpu MHz\t\t: 1234.5")
	put("proc/meminfo", fmt.Sprintf("MemTotal:       %d kB\nMemFree:        1 kB", f.memGiB<<20))
	put("proc/sys/net/core/somaxconn", fmt.Sprint(f.somaxconn))
	put("proc/sys/fs/nr_open", fmt.Sprint(f.nrOpen))
	put("proc/sys/fs/file-max", "9223372036854775807")
	put("proc/sys/net/ipv4/ip_local_port_range", fmt.Sprintf("%d\t%d", f.ports[0], f.ports[1]))
	put("proc/sys/net/core/rmem_max", fmt.Sprint(f.rmemMax))
	put("proc/sys/net/ipv4/tcp_mem", fmt.Sprintf("%d\t%d\t%d", f.tcpMem/2, f.tcpMem, f.tcpMem*2))
	return fsys
}

func machineOf(t *testing.T, f fakeMachine) Machine {
	t.Helper()
	m, err := detectMachine(fakeSys(f), 4096)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDetectMachine(t *testing.T) {
	m := machineOf(t, benchHost)
	if m.CPUs != 16 || m.CoreCount != 8 || m.ThreadsPerCore != 2 || m.Nodes != 1 || m.MaxFreqMHz != 5053 ||
		m.Model != "Fake CPU" || m.MemBytes != 62<<30 || m.Somaxconn != 2048 || m.NrOpen != 2000500 ||
		m.ports() != 64512 || m.RmemMax != 7500000 || m.TCPMemPressure != 1<<30 {
		t.Errorf("%+v", m)
	}
	if !slices.Equal(m.Cores[0], []int{0, 8}) || !slices.Equal(m.Cores[7], []int{7, 15}) {
		t.Errorf("cores %v", m.Cores)
	}

	// Without cpufreq, nor the files under /proc/sys.
	fsys := fakeSys(fakeMachine{cores: 4, threads: 1, nodes: 1})
	for name := range fsys {
		if strings.Contains(name, "cpufreq") || strings.HasPrefix(name, "proc/sys") {
			delete(fsys, name)
		}
	}
	m, err := detectMachine(fsys, 4096)
	if err != nil || m.MaxFreqMHz != 1234 || m.Somaxconn != 0 || m.ports() != 0 || m.TCPMemPressure != 0 {
		t.Errorf("%+v %v", m, err)
	}
	// An offline sibling is left out, and a core without topology is a
	// core of its own.
	fsys = fakeSys(fakeMachine{cores: 2, threads: 2, nodes: 1})
	fsys["sys/devices/system/cpu/online"] = &fstest.MapFile{Data: []byte("0-2\n")}
	delete(fsys, "sys/devices/system/cpu/cpu2/topology/thread_siblings_list")
	if m, err = detectMachine(fsys, 4096); err != nil || fmt.Sprint(m.Cores) != "[[0 2] [1]]" {
		t.Errorf("%v %v", m.Cores, err)
	}
	if _, err := detectMachine(fstest.MapFS{}, 4096); err == nil {
		t.Error("a machine without /sys")
	}
	fsys["sys/devices/system/cpu/online"] = &fstest.MapFile{Data: []byte("x\n")}
	if _, err := detectMachine(fsys, 4096); err == nil {
		t.Error("a bad CPU list")
	}
}

func TestDerivePlan(t *testing.T) {
	levels := func(p Plan) string {
		var s []string
		for _, l := range p.Levels {
			s = append(s, l.arg())
		}
		return strings.Join(s, " ")
	}
	for _, tt := range []struct {
		name                    string
		m                       fakeMachine
		server, backend, client string
		top                     int
		levels                  string
		w5Idle, w4              int
		conns, idle, backlog    int
		warning                 string
	}{
		{"the benchmark host", benchHost, "0-1,8-9", "2-4,10-12", "5-7,13-15", 10000,
			"100:10:10 500:20:25 2000:80:25 5000:200:25 10000:400:25", 40000, 100, 16384, 4096, 2048, ""},
		{"64 threads on 2 nodes", fakeMachine{cores: 32, threads: 2, nodes: 2, mhz: 3000, memGiB: 256, somaxconn: 4096,
			nrOpen: 1 << 20, ports: [2]int{32768, 60999}, rmemMax: 7500000, tcpMem: 1 << 20},
			"0-7,32-39", "8-19,40-51", "20-31,52-63", 20000,
			"100:24:4 500:24:20 4000:160:25 10000:400:25 20000:800:25", 80000, 100, 32768, 2048, 4096, "span 2 NUMA nodes"},
		{"8 threads, 16 GiB", fakeMachine{cores: 4, threads: 2, nodes: 1, mhz: 4000, memGiB: 16, somaxconn: 4096,
			nrOpen: 1 << 20, ports: [2]int{32768, 60999}, rmemMax: 212992, tcpMem: 196608},
			"0,4", "1-2,5-6", "3,7", 5000, "100:10:10 500:20:25 1000:40:25 2500:100:25 5000:200:25", 20000, 50, 8192, 4096, 4096, "rmem_max"},
		{"4 threads without SMT", fakeMachine{cores: 4, threads: 1, nodes: 1, memGiB: 8, somaxconn: 4096, nrOpen: 1 << 20},
			"0", "1-2", "3", 2000, "100:10:10 500:20:25 1000:40:25 2000:80:25", 8000, 100, 4096, 4096, 4096, ""},
		{"4 threads on 2 cores", fakeMachine{cores: 2, threads: 2, nodes: 1, memGiB: 4, somaxconn: 128, nrOpen: 1 << 20},
			"0", "1-2", "3", 500, "100:10:10 500:20:25", 2000, 100, 1024, 1024, 128, "split by thread"},
		{"6 cores without SMT, little memory", fakeMachine{cores: 6, threads: 1, nodes: 1, memGiB: 1, somaxconn: 4096, nrOpen: 1 << 20},
			"0-1", "2-3", "4-5", 500, "100:10:10 500:20:25", 96, 100, 1024, 512, 4096, "W5 can hold only"},
		{"3 cores", fakeMachine{cores: 3, threads: 1, nodes: 1, memGiB: 2, nrOpen: 1 << 20},
			"0", "1", "2", 1000, "100:10:10 500:20:25 1000:40:25", 4000, 100, 2048, 2048, 4096, ""},
		{"5 cores, the odd one to the backends", fakeMachine{cores: 5, threads: 1, nodes: 1, memGiB: 32, nrOpen: 1 << 20},
			"0", "1-2", "3-4", 2000, "100:10:10 500:20:25 1000:40:25 2000:80:25", 8000, 100, 4096, 4096, 4096, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p, err := derivePlan(machineOf(t, tt.m), Overrides{})
			if err != nil {
				t.Fatal(err)
			}
			if p.ServerCPUs != tt.server || p.BackendCPUs != tt.backend || p.ClientCPUs != tt.client {
				t.Errorf("split %s / %s / %s", p.ServerCPUs, p.BackendCPUs, p.ClientCPUs)
			}
			if p.BackendWorkers < p.ClientThreads {
				t.Errorf("backends %d smaller than the load generators %d", p.BackendWorkers, p.ClientThreads)
			}
			if p.Top != tt.top || levels(p) != tt.levels {
				t.Errorf("top %d %v, levels %s", p.Top, p.TopBounds, levels(p))
			}
			if p.W5Idle != tt.w5Idle || p.W4.InFlight != tt.w4 {
				t.Errorf("W5 %d %v, W4 %d", p.W5Idle, p.W5Bounds, p.W4.InFlight)
			}
			if a := p.Aligned; a.BackendConns != tt.conns || a.IdlePerWorker != tt.idle || a.Backlog != tt.backlog || a.BackendConns <= p.Top {
				t.Errorf("aligned %+v", a)
			}
			if got := strings.Join(p.Warnings, "; "); (tt.warning == "") != (got == "") || !strings.Contains(got, tt.warning) {
				t.Errorf("warnings %q, want %q", got, tt.warning)
			}
			for _, l := range append(p.Levels, p.W4) {
				if l.H2Conns < p.ClientThreads || l.Conns < p.ClientThreads || l.H2Streams > 25 {
					t.Errorf("level %+v for %d threads", l, p.ClientThreads)
				}
			}
		})
	}
}

// On the benchmark host the derived plan is the one hand-tuned before it:
// the same split, levels, workloads and aligned settings, and its 232 runs.
func TestDerivePlanBenchHost(t *testing.T) {
	p, err := derivePlan(machineOf(t, benchHost), Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Mid.arg() != "500:20:25" || len(p.W2) != 2 || p.W2[1].InFlight != 2000 || fmt.Sprint(p.W3) != "[100 500]" ||
		p.W4.arg() != "100:10:10" || fmt.Sprint(p.W6Rates) != "[20000 100000]" || p.Runs != 232 || p.Workers != 4 ||
		p.Aligned.WorkerConnections != 65536 || p.TopBounds["cpu"] != 10000 || p.TopBounds["somaxconn"] != 10240 {
		t.Errorf("%+v", p)
	}
	if p.Estimated != "1h33m0s" {
		t.Errorf("estimated %s", p.Estimated)
	}
}

func TestDerivePlanOverrides(t *testing.T) {
	m := machineOf(t, benchHost)
	p, err := derivePlan(m, Overrides{ServerCPUs: "0-3", Levels: []int{500, 100, 500}, W5Idle: 1000, W6Rates: []int{1, 2, 3},
		BackendConns: 400, Backlog: 4096, Duration: 3 * time.Second, Warmup: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if p.ServerCPUs != "0-3" || p.BackendCPUs != "2-4,10-12" || p.Top != 500 || len(p.Levels) != 2 || p.W5Idle != 1000 ||
		fmt.Sprint(p.W6Rates) != "[1 2 3]" || p.W6From != "set" || p.Aligned.BackendConns != 400 || p.Aligned.IdlePerWorker != 100 ||
		p.Aligned.Backlog != 4096 || p.Duration != "3s" || p.Warmup != "1s" || p.Runs != 16*2+10*2+15+8*2+26+10+30 {
		t.Errorf("%+v", p)
	}
	warnings := strings.Join(p.Warnings, "; ")
	for _, want := range []string{"CPU 2 is given to both", "does not exceed the top level", "past somaxconn"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("no %q in %q", want, warnings)
		}
	}
	if len(p.Set) != 6 {
		t.Errorf("set %q", p.Set)
	}
	if p, _ := derivePlan(m, Overrides{Top: 50000}); p.Top != 50000 || !strings.Contains(strings.Join(p.Warnings, ";"), "past what the machine") {
		t.Errorf("top %d %q", p.Top, p.Warnings)
	}
	// A machine too small to split, unless every role's CPUs are set.
	small := machineOf(t, fakeMachine{cores: 1, threads: 2, nodes: 1, memGiB: 4, nrOpen: 1 << 20})
	if _, err := derivePlan(small, Overrides{}); err == nil || !strings.Contains(err.Error(), "at least 3") {
		t.Errorf("%v", err)
	}
	if p, err := derivePlan(small, Overrides{ServerCPUs: "0", BackendCPUs: "1", ClientCPUs: "1"}); err != nil || !strings.Contains(strings.Join(p.Warnings, ";"), "given to both") {
		t.Errorf("%v %q", err, p.Warnings)
	}
	if _, err := derivePlan(m, Overrides{ServerCPUs: "x"}); err == nil {
		t.Error("a bad CPU list")
	}
	low := m
	low.NrOpen = 65536
	if _, err := derivePlan(low, Overrides{}); err == nil || !strings.Contains(err.Error(), "nr_open") {
		t.Errorf("%v", err)
	}
	tiny := m
	tiny.MemBytes = 16 << 20
	if _, err := derivePlan(tiny, Overrides{}); err == nil || !strings.Contains(err.Error(), "starts at 100") {
		t.Errorf("%v", err)
	}
	// W5 with little room for idle connections.
	little := m
	little.MemBytes = 400 << 20
	if p, err := derivePlan(little, Overrides{}); err != nil || p.Top != 200 || !strings.Contains(strings.Join(p.Warnings, ";"), "W5 can hold only") {
		t.Errorf("%v %d %q", err, p.Top, p.Warnings)
	}
}

func TestShape(t *testing.T) {
	for _, tt := range []struct {
		n, threads int
		want       string
	}{
		{100, 6, "100:10:10"}, {500, 6, "500:20:25"}, {10000, 6, "10000:400:25"},
		{100, 12, "100:12:8"}, {200, 1, "200:14:14"}, {250, 1, "250:10:25"}, {30, 6, "30:6:5"}, {1, 2, "1:2:1"},
	} {
		if got := shape(tt.n, tt.threads).arg(); got != tt.want {
			t.Errorf("shape(%d, %d) = %s, want %s", tt.n, tt.threads, got, tt.want)
		}
	}
}

func TestRounding(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 1, 3: 2, 9: 5, 10: 10, 10240: 10000, 31712: 20000, 51609: 50000} {
		if got := floor125(n); got != want {
			t.Errorf("floor125(%d) = %d, want %d", n, got, want)
		}
	}
	for n, want := range map[int]int{0: 0, 7: 7, 40000: 40000, 252160: 250000, 1999: 1900} {
		if got := floorSig(n, 2); got != want {
			t.Errorf("floorSig(%d) = %d, want %d", n, got, want)
		}
	}
	if roundSig(19773, 1) != 20000 || roundSig(97690, 1) != 100000 || roundSig(97690, 2) != 98000 || roundSig(0, 1) != 0 {
		t.Error("roundSig")
	}
	if nextPow2(15000) != 16384 || nextPow2(1) != 1 {
		t.Error("nextPow2")
	}
	if got := formatCPUList([]int{9, 0, 1, 8, 4}); got != "0-1,4,8-9" {
		t.Error(got)
	}
	if _, err := parseCPUList("3-1"); err == nil {
		t.Error("a backward range")
	}
	if _, err := parseCPUList(""); err == nil {
		t.Error("an empty list")
	}
}

// api is a calibration's result for server over protocol.
func api(server, protocol string, rps float64) Result {
	return Result{Server: server, Scenario: "api", Protocol: protocol, RequestsPerSec: rps}
}

// The rates come out as they were chosen by hand from W1 at 500 on the
// benchmark host, 2026-10-24.
func TestCalibrate(t *testing.T) {
	results := []Result{
		api("nginx", "http/1.1", 198652), api("nginx", "h2", 163194),
		api("caddy", "http/1.1", 48340), api("caddy", "h2", 39547),
		api("traefik", "http/1.1", 64728), api("traefik", "h2", 46294),
		api("envoy", "http/1.1", 63048), api("envoy", "h2", 80039),
		api("fit", "http/1.1", 136438), api("fit", "h2", 115342),
		{Server: "fit", Scenario: "api", Protocol: "h3", Error: "hung"},
		{Server: "fit", Scenario: "fixed", Protocol: "h2", RequestsPerSec: 1e6},
	}
	rates, from, err := calibrate(results)
	if err != nil || fmt.Sprint(rates) != "[20000 100000]" {
		t.Errorf("%v %v", rates, err)
	}
	if !strings.Contains(from, "caddy, traefik, envoy at most 80039, fit, nginx at least 115342") {
		t.Error(from)
	}
	// No round number between the two sides: 0.9 of the faster side's least.
	rates, _, _ = calibrate([]Result{api("a", "h2", 100000), api("b", "h2", 300000), api("b", "h1", 290000), api("a", "h1", 295000)})
	if fmt.Sprint(rates) != "[50000 260000]" {
		t.Errorf("%v", rates)
	}
	rates, _, _ = calibrate([]Result{api("a", "h2", 100000)})
	if fmt.Sprint(rates) != "[50000 90000]" {
		t.Errorf("%v", rates)
	}
	if _, _, err := calibrate(nil); err == nil {
		t.Error("nothing to calibrate from")
	}
}

func TestRunPlan(t *testing.T) {
	root := t.TempDir()
	for name, f := range fakeSys(benchHost) {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	plan := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runPlan(append([]string{"-sys", root}, args...), &out)
		return out.String(), err
	}
	text, err := plan()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"split       server 0-1,8-9 (4 workers), backends 2-4,10-12 (6), load generators 5-7,13-15 (6 threads)",
		"top level   10000 in flight", "10000 (wrk 10000, h2load 400×25)", "40000 idle TLS connections",
		"backend connections and waiting requests 16384, nginx idle 4096 a worker, backlog 2048", "runs        232"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	sh, err := plan("-format", "sh", "-levels", "100,500", "-duration", "3s", "-warmup", "1s")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PLAN_DURATION=3s\n", "PLAN_LEVELS='100:10:10 500:20:25'\n", "PLAN_W3_CONNS='100 500'\n",
		"PLAN_CPUS='-server-cpus 0-1,8-9 -backend-cpus 2-4,10-12 -client-cpus 5-7,13-15'\n", "PLAN_W6_SET=false\n",
		"PLAN_ALIGNED='-backend-conns 1024 -backlog 2048'\n", "PLAN_MID_LEVEL='500:20:25'\n", "PLAN_W5_IDLE=2000\n"} {
		if !strings.Contains(sh, want) {
			t.Errorf("no %q in\n%s", want, sh)
		}
	}

	// W6's rates from a calibration's results, unless they are set.
	calib := filepath.Join(t.TempDir(), "w1.json")
	data, _ := json.Marshal(Report{Results: []Result{api("a", "h2", 100000), api("b", "h2", 300000)}})
	if err := os.WriteFile(calib, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := plan("-format", "json", "-calibration", calib); err != nil || !strings.Contains(out, `"w6_rates": [
    50000,
    200000
  ]`) {
		t.Errorf("%v\n%s", err, out)
	}
	if out, _ := plan("-format", "sh", "-calibration", calib, "-w6-rates", "7,8"); !strings.Contains(out, "PLAN_W6_RATES='7 8'\nPLAN_W6_SET=true") {
		t.Error(out)
	}
	if err := os.WriteFile(calib, []byte(`{"results": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, _ := plan("-calibration", calib); !strings.Contains(out, "W6 keeps the estimated rates") {
		t.Error(out)
	}
	for _, args := range [][]string{{"-format", "xml"}, {"-levels", "x"}, {"-w6-rates", "-1"}, {"extra"},
		{"-calibration", "/nonexistent"}, {"-bogus"}, {"-server-cpus", "x"}} {
		if _, err := plan(args...); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	if err := os.WriteFile(calib, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := plan("-calibration", calib); err == nil {
		t.Error("a calibration that is not JSON")
	}
	if err := runPlan([]string{"-sys", t.TempDir()}, &bytes.Buffer{}); err == nil {
		t.Error("a machine without /sys")
	}
}

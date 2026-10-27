package main

// fitbench plan reads the machine it runs on and derives the load test
// plan's parameters from it: the CPU split, the levels of load, the aligned
// settings and W6's rates, as bench/README.md ("Deriving the plan") says.
// bench/plan.sh runs it first, and runs the plan it prints.

import (
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
)

// A Machine is what fitbench plan reads of the host, from /sys and /proc. A
// value it could not read is 0, and bounds nothing.
type Machine struct {
	Model string `json:"model,omitempty"`
	CPUs  int    `json:"cpus"`
	// Cores are the online CPUs by physical core, each core's threads in
	// order, the cores by NUMA node and then by their first CPU.
	Cores          [][]int `json:"-"`
	CoreCount      int     `json:"cores"`
	ThreadsPerCore int     `json:"threads_per_core"`
	Nodes          int     `json:"numa_nodes"`
	// nodeOf maps each CPU to its NUMA node.
	nodeOf     map[int]int
	MaxFreqMHz int   `json:"max_mhz,omitempty"`
	MemBytes   int64 `json:"memory_bytes"`
	Somaxconn  int   `json:"somaxconn,omitempty"`
	NrOpen     int64 `json:"nr_open,omitempty"`
	FileMax    int64 `json:"file_max,omitempty"`
	PortLo     int   `json:"port_lo,omitempty"`
	PortHi     int   `json:"port_hi,omitempty"`
	RmemMax    int64 `json:"rmem_max,omitempty"`
	// TCPMemPressure is tcp_mem's middle value in bytes: past it, across
	// every socket, the kernel starts to hold TCP buffers back.
	TCPMemPressure int64 `json:"tcp_mem_pressure_bytes,omitempty"`
}

// ports is how many local ports a connection can be opened from, toward
// one address; 0 when unknown.
func (m Machine) ports() int {
	if m.PortHi < m.PortLo || m.PortLo == 0 {
		return 0
	}
	return m.PortHi - m.PortLo + 1
}

// detectMachine reads the machine from fsys, the root of a Linux
// filesystem; pageSize is the size tcp_mem counts in.
func detectMachine(fsys fs.FS, pageSize int) (Machine, error) {
	var m Machine
	online, err := fs.ReadFile(fsys, "sys/devices/system/cpu/online")
	if err != nil {
		return m, fmt.Errorf("no CPU topology to read (fitbench plan reads Linux's /sys): %v", err)
	}
	cpus, err := parseCPUList(strings.TrimSpace(string(online)))
	if err != nil {
		return m, fmt.Errorf("sys/devices/system/cpu/online: %v", err)
	}
	m.CPUs = len(cpus)
	m.nodeOf = map[int]int{}
	nodes, _ := fs.Glob(fsys, "sys/devices/system/node/node*/cpulist")
	for _, name := range nodes {
		n, err := strconv.Atoi(strings.TrimPrefix(path.Base(path.Dir(name)), "node"))
		if err != nil {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		list, err := parseCPUList(strings.TrimSpace(string(data)))
		if err != nil || len(list) == 0 {
			continue
		}
		m.Nodes++
		for _, c := range list {
			m.nodeOf[c] = n
		}
	}
	m.Nodes = max(m.Nodes, 1)
	seen := map[int]bool{}
	for _, c := range cpus {
		if seen[c] {
			continue
		}
		core := []int{c}
		dir := fmt.Sprintf("sys/devices/system/cpu/cpu%d/topology/", c)
		data, err := fs.ReadFile(fsys, dir+"thread_siblings_list")
		if err != nil {
			data, err = fs.ReadFile(fsys, dir+"core_cpus_list")
		}
		if err == nil {
			if sib, err := parseCPUList(strings.TrimSpace(string(data))); err == nil && slices.Contains(sib, c) {
				// Only the online ones.
				core = slices.DeleteFunc(sib, func(x int) bool { return !slices.Contains(cpus, x) })
			}
		}
		for _, x := range core {
			seen[x] = true
		}
		m.Cores = append(m.Cores, core)
	}
	slices.SortStableFunc(m.Cores, func(a, b []int) int {
		if na, nb := m.nodeOf[a[0]], m.nodeOf[b[0]]; na != nb {
			return na - nb
		}
		return a[0] - b[0]
	})
	m.CoreCount = len(m.Cores)
	for _, core := range m.Cores {
		m.ThreadsPerCore = max(m.ThreadsPerCore, len(core))
	}

	if khz := readInt(fsys, fmt.Sprintf("sys/devices/system/cpu/cpu%d/cpufreq/cpuinfo_max_freq", cpus[0])); khz > 0 {
		m.MaxFreqMHz = int(khz / 1000)
	}
	if info, err := fs.ReadFile(fsys, "proc/cpuinfo"); err == nil {
		for line := range strings.SplitSeq(string(info), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "model name":
				if m.Model == "" {
					m.Model = strings.TrimSpace(v)
				}
			case "cpu MHz":
				// Without cpufreq, the most any CPU runs at now.
				if mhz, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil && m.MaxFreqMHz == 0 {
					m.MaxFreqMHz = max(m.MaxFreqMHz, int(mhz))
				}
			}
		}
	}
	if info, err := fs.ReadFile(fsys, "proc/meminfo"); err == nil {
		for line := range strings.SplitSeq(string(info), "\n") {
			if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
				kb, _ := strconv.ParseInt(f[1], 10, 64)
				m.MemBytes = kb << 10
			}
		}
	}
	m.Somaxconn = int(readInt(fsys, "proc/sys/net/core/somaxconn"))
	m.NrOpen = readInt(fsys, "proc/sys/fs/nr_open")
	m.FileMax = readInt(fsys, "proc/sys/fs/file-max")
	m.RmemMax = readInt(fsys, "proc/sys/net/core/rmem_max")
	if f := readFields(fsys, "proc/sys/net/ipv4/ip_local_port_range"); len(f) == 2 {
		m.PortLo, m.PortHi = int(f[0]), int(f[1])
	}
	if f := readFields(fsys, "proc/sys/net/ipv4/tcp_mem"); len(f) == 3 {
		m.TCPMemPressure = f[1] * int64(pageSize)
	}
	return m, nil
}

// readInt reads a file holding one integer; 0 when it cannot.
func readInt(fsys fs.FS, name string) int64 {
	f := readFields(fsys, name)
	if len(f) != 1 {
		return 0
	}
	return f[0]
}

func readFields(fsys fs.FS, name string) []int64 {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil
	}
	var out []int64
	for _, s := range strings.Fields(string(data)) {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// parseCPUList parses a list such as "0-3,8,10-11".
func parseCPUList(set string) ([]int, error) {
	if set == "" {
		return nil, errors.New("empty CPU list")
	}
	var cpus []int
	for part := range strings.SplitSeq(set, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("CPU list %q: %v", set, err)
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return nil, fmt.Errorf("CPU list %q: bad range %q", set, part)
			}
		}
		for c := a; c <= b; c++ {
			cpus = append(cpus, c)
		}
	}
	return cpus, nil
}

// formatCPUList writes CPUs as a list with ranges, as "0-1,8-9".
func formatCPUList(cpus []int) string {
	cpus = slices.Clone(cpus)
	slices.Sort(cpus)
	var parts []string
	for i := 0; i < len(cpus); {
		j := i
		for j+1 < len(cpus) && cpus[j+1] == cpus[j]+1 {
			j++
		}
		if j == i {
			parts = append(parts, strconv.Itoa(cpus[i]))
		} else {
			parts = append(parts, fmt.Sprintf("%d-%d", cpus[i], cpus[j]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// What the plan's servers were measured to hold on the benchmark host, which
// the levels are bounded by on other machines (bench/README.md, "Deriving
// the plan").
const (
	// inFlightPerServerCPU is how many requests in flight the top level
	// keeps for each of the server's CPUs: 2,500, which on the host's 4
	// was past every server's ceiling and on its 6 allows 15,000, past the
	// 10,000 somaxconn allows.
	inFlightPerServerCPU = 2500
	// inFlightPerClientThread is how many requests in flight a load
	// generator thread drives: the host's 10,000 over the 6 threads that
	// drove it before.
	inFlightPerClientThread = 1667
	// inFlightPerBackendCPU is how many requests in flight the proxy
	// backends answer for each CPU: they used 2.18 of 6 at 10,000 (4,587
	// a CPU); 4,000 a CPU leaves them room.
	inFlightPerBackendCPU = 4000
	// memPerInFlight is the most a server held for each request in flight
	// (Envoy over HTTP/3 at 10,000, 4.9 GiB), rounded up, and memPerIdle for
	// each idle TLS connection (Traefik, 2.4 GiB for 40,000).
	memPerInFlight = 512 << 10
	memPerIdle     = 64 << 10
	// tcpPerInFlight is what a small request and its answer keep in TCP
	// buffers across the hops, and tcpPerTransfer a 1 MiB transfer's: up to
	// 1 MiB in each of the four sockets it passes.
	tcpPerInFlight = 16 << 10
	tcpPerTransfer = 4 << 20
	// burstPerBacklog is how many connections wrk may open at once for each
	// one the accept queue holds: on the host, 10,000 against somaxconn's
	// 2,048 started cleanly.
	burstPerBacklog = 5
	// containerNofile is the nofile every container is started with.
	containerNofile = 1048576
	// idlePerTop is W5's idle TLS connections for each request in flight at
	// the top level: 40,000 for 10,000.
	idlePerTop = 4
	// The benchmark host's server: 4 CPUs at 5,053 MHz, against which W6's
	// rates are estimated before a calibration sets them.
	refServerCPUs = 4
	refMaxMHz     = 5053
	refLowRate    = 20000
	refHighRate   = 100000
	// runOverhead is what each run costs past its warm-up and measured
	// load: starting the server, the checks, the reads (about 24s a run of
	// 5s and 15s on the host).
	runOverhead = 4 * time.Second
	// minBackendConns is the backend connection bound a run outside the
	// plan gets at least, as before the plan derived it.
	minBackendConns = 16384
)

// The runs each workload makes at each of its levels, or in all (W4).
const (
	runsW0 = 16 // nginx, Caddy, Envoy, fit × HTTP/1.1, h2c, h2, HTTP/3
	runsW1 = 10 // all five × HTTP/1.1, h2
	runsW2 = 15 // site: nginx, Caddy, fit × 3 protocols; edge: nginx, fit × 3
	runsW3 = 8  // nginx, Caddy, Envoy, fit × churn-1, churn-10
	runsW4 = 26 // download, proxy-download, proxy-upload over HTTP/1.1 and h2, by who serves them
	runsW5 = 10
	runsW6 = 10
)

// A Level is a level of load: InFlight requests, as wrk's Conns over
// HTTP/1.1 and h2load's H2Conns × H2Streams over h2c, h2 and HTTP/3.
type Level struct {
	InFlight  int `json:"in_flight"`
	Conns     int `json:"connections"`
	H2Conns   int `json:"h2_connections"`
	H2Streams int `json:"h2_streams"`
}

// shape makes a level of n requests in flight for threads load generator
// threads: 25 streams a connection when that makes at least 10 connections
// and as many as the threads, or else near a square, every connection count
// at least the threads.
func shape(n, threads int) Level {
	l := Level{InFlight: n, Conns: max(n, threads)}
	if n/25 >= max(10, threads) {
		l.H2Conns, l.H2Streams = n/25, 25
		return l
	}
	s := max(1, int(math.Sqrt(float64(n))))
	c := n / s
	if c < threads {
		c, s = threads, max(1, n/threads)
	}
	l.H2Conns, l.H2Streams = c, s
	return l
}

func (l Level) arg() string { return fmt.Sprintf("%d:%d:%d", l.InFlight, l.H2Conns, l.H2Streams) }

// Overrides are the plan's settings set explicitly, each in place of the
// derived one; zero values are derived.
type Overrides struct {
	ServerCPUs, BackendCPUs, ClientCPUs string
	Levels                              []int
	Top                                 int
	W5Idle                              int
	W6Rates                             []int
	BackendConns, Backlog               int
	Duration, Warmup                    time.Duration
}

// A Plan is the load test plan derived for a machine.
type Plan struct {
	Machine        Machine `json:"machine"`
	ServerCPUs     string  `json:"server_cpus"`
	BackendCPUs    string  `json:"backend_cpus"`
	ClientCPUs     string  `json:"client_cpus"`
	Workers        int     `json:"workers"`
	BackendWorkers int     `json:"backend_workers"`
	ClientThreads  int     `json:"client_threads"`
	// Top is the top level, the least of TopBounds, each what one input
	// allows, rounded down to 1, 2 or 5 times a power of ten.
	Top       int            `json:"top"`
	TopBounds map[string]int `json:"top_bounds"`
	Levels    []Level        `json:"levels"`
	// Mid is the level near 500 that W2 to W6 and the calibration share.
	Mid       Level          `json:"mid"`
	W2        []Level        `json:"w2_levels"`
	W3        []int          `json:"w3_connections"`
	W4        Level          `json:"w4_level"`
	W5Idle    int            `json:"w5_idle"`
	W5Bounds  map[string]int `json:"w5_idle_bounds"`
	W6Rates   []int          `json:"w6_rates"`
	W6From    string         `json:"w6_rates_from"`
	Aligned   Aligned        `json:"aligned"`
	Duration  string         `json:"duration"`
	Warmup    string         `json:"warmup"`
	Runs      int            `json:"runs"`
	Estimated string         `json:"estimated"`
	// Set names the settings given explicitly rather than derived.
	Set      []string `json:"set,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Aligned are the settings every server under test shares
// (bench/README.md, "Aligned settings") that the plan derives.
type Aligned struct {
	// BackendConns bounds the connections to a backend, and the requests
	// waiting for one; IdlePerWorker is nginx's share of them kept idle in
	// each worker.
	BackendConns  int `json:"backend_conns"`
	IdlePerWorker int `json:"backend_idle_per_worker"`
	Backlog       int `json:"backlog"`
	// WorkerConnections is nginx's worker_connections.
	WorkerConnections int `json:"nginx_worker_connections"`
}

// alignedFor derives the aligned settings for a run of at most inFlight
// requests in flight with idle idle connections held, on workers workers;
// conns and backlog are set when above 0.
func alignedFor(inFlight, idle, workers, conns, backlog, somaxconn int) Aligned {
	a := Aligned{BackendConns: conns, Backlog: backlog}
	if a.BackendConns <= 0 {
		a.BackendConns = max(1024, nextPow2(inFlight*3/2))
	}
	if a.Backlog <= 0 {
		a.Backlog = somaxconn
	}
	if a.Backlog <= 0 {
		a.Backlog = 4096 // Linux's default since 5.4
	}
	workers = max(1, workers)
	a.IdlePerWorker = (a.BackendConns + workers - 1) / workers
	// A worker holds its share of the client connections, as many backend
	// connections, and the idle ones, twice over for an uneven spread.
	a.WorkerConnections = max(65536, nextPow2(2*(2*a.BackendConns+idle)/workers))
	return a
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// ceilDiv divides a by b, rounding up.
func ceilDiv(a, b int) int {
	if a <= 0 {
		return 0
	}
	return (a + b - 1) / b
}

// floor125 rounds n down to 1, 2 or 5 times a power of ten.
func floor125(n int) int {
	if n < 1 {
		return 0
	}
	p := 1
	for p*10 <= n {
		p *= 10
	}
	for _, f := range []int{5, 2, 1} {
		if f*p <= n {
			return f * p
		}
	}
	return p
}

// roundSig rounds x to sig significant digits.
func roundSig(x float64, sig int) int {
	if x <= 0 {
		return 0
	}
	p := math.Pow(10, math.Floor(math.Log10(x))-float64(sig-1))
	return int(math.Round(x/p) * p)
}

// floorSig rounds n down to sig significant digits.
func floorSig(n, sig int) int {
	if n <= 0 {
		return 0
	}
	p := max(1, int(math.Pow(10, math.Floor(math.Log10(float64(n)))-float64(sig-1))))
	return n / p * p
}

// splitCores divides the machine's CPUs between the server under test, the
// backends and the load generators by physical core, so that no role shares
// a core with another. The load generators get the cores that hold the
// threads they need to drive inFlight requests, the backends the cores that
// hold the CPUs they need to answer them, and the server every core left:
// on the benchmark host 3 cores (6 CPUs) to the server, 2 (4 CPUs) to the
// backends and 3 (6 threads) to the load generators, the server up from 2
// cores while the load generators keep what drove 10,000 in flight before.
// A role's reservation is capped at half the cores for the load generators
// and a quarter for the backends, so the server keeps at least one core.
// With fewer than 3 cores it splits threads instead, with a warning; with
// fewer than 3 CPUs it cannot.
func splitCores(m Machine, inFlight int) (server, backend, client []int, warnings []string, err error) {
	units := m.Cores
	if len(units) < 3 {
		if m.CPUs < 3 {
			return nil, nil, nil, nil, fmt.Errorf("%d CPUs cannot be split between the server, the backends and the load generators: the plan needs at least 3", m.CPUs)
		}
		units = nil
		for _, core := range m.Cores {
			for _, c := range core {
				units = append(units, []int{c})
			}
		}
		warnings = append(warnings, fmt.Sprintf("only %d physical cores: the CPUs are split by thread, and the roles share cores, which costs each of them", len(m.Cores)))
	}
	n := len(units)
	// unitsFor is how many units hold threads threads, a whole core (both
	// its threads) coming with it.
	unitsFor := func(threads int) int {
		count := 0
		for i, u := range units {
			count += len(u)
			if count >= threads {
				return i + 1
			}
		}
		return n
	}
	clientN := min(unitsFor(max(2, ceilDiv(inFlight, inFlightPerClientThread))), max(1, n/2))
	backendN := min(unitsFor(max(2, ceilDiv(inFlight, inFlightPerBackendCPU))), max(1, n/4))
	// The server keeps at least one unit, taken from the larger of the two
	// reservations.
	for n-clientN-backendN < 1 {
		if clientN >= backendN && clientN > 1 {
			clientN--
		} else if backendN > 1 {
			backendN--
		} else {
			break
		}
	}
	s := n - clientN - backendN
	take := func(from, count int) []int {
		var cpus []int
		for _, u := range units[from : from+count] {
			cpus = append(cpus, u...)
		}
		return cpus
	}
	server, backend, client = take(0, s), take(s, backendN), take(s+backendN, clientN)
	for name, cpus := range map[string][]int{"the server": server, "the backends": backend, "the load generators": client} {
		nodes := map[int]bool{}
		for _, c := range cpus {
			nodes[m.nodeOf[c]] = true
		}
		if len(nodes) > 1 {
			warnings = append(warnings, fmt.Sprintf("%s span %d NUMA nodes", name, len(nodes)))
		}
	}
	return server, backend, client, warnings, nil
}

// derivePlan derives the load test plan for m, with what o sets in place of
// what would be derived.
func derivePlan(m Machine, o Overrides) (Plan, error) {
	p := Plan{Machine: m, TopBounds: map[string]int{}, W5Bounds: map[string]int{}}
	nofile := int64(containerNofile)
	if m.NrOpen > 0 && m.NrOpen < containerNofile {
		return p, fmt.Errorf("fs.nr_open is %d: the containers' nofile of %d cannot be set; raise it (sysctl fs.nr_open=%d)", m.NrOpen, containerNofile, containerNofile)
	}
	// The top level's bounds that do not depend on the split.
	base := map[string]int{}
	if m.MemBytes > 0 {
		base["memory"] = int(m.MemBytes / 4 / memPerInFlight)
	}
	if ports := m.ports(); ports > 0 {
		base["ports"] = ports * 8 / 10
	}
	if m.Somaxconn > 0 {
		base["somaxconn"] = burstPerBacklog * m.Somaxconn
	}
	if m.TCPMemPressure > 0 {
		base["tcp_mem"] = int(m.TCPMemPressure / 2 / tcpPerInFlight)
	}
	base["nofile"] = int(nofile / 4)
	if m.FileMax > 0 && m.FileMax < math.MaxInt32 {
		base["file_max"] = int(m.FileMax / 8)
	}
	least := math.MaxInt
	for _, v := range base {
		least = min(least, v)
	}

	// The split and the top level size each other: the split reserves what
	// the top level needs of the load generators and the backends, and the
	// top level is bounded by what the split allows. Start from the
	// machine's own bounds and settle the two in a few rounds, unless the
	// CPUs or the levels are given.
	inFlight := floor125(least)
	if len(o.Levels) > 0 {
		inFlight = slices.Max(o.Levels)
	} else if o.Top > 0 {
		inFlight = o.Top
	}
	server, backend, client, warnings, err := splitCores(m, inFlight)
	if err != nil && (o.ServerCPUs == "" || o.BackendCPUs == "" || o.ClientCPUs == "") {
		return p, err
	}
	p.Warnings = warnings
	p.ServerCPUs, p.BackendCPUs, p.ClientCPUs = formatCPUList(server), formatCPUList(backend), formatCPUList(client)
	for _, set := range []struct {
		name string
		from string
		to   *string
	}{{"server CPUs", o.ServerCPUs, &p.ServerCPUs}, {"backend CPUs", o.BackendCPUs, &p.BackendCPUs}, {"load generator CPUs", o.ClientCPUs, &p.ClientCPUs}} {
		if set.from != "" {
			if _, err := parseCPUList(set.from); err != nil {
				return p, err
			}
			*set.to, p.Set = set.from, append(p.Set, set.name)
		}
	}
	cpusSet := len(p.Set) > 0
	if cpusSet {
		p.Warnings = nil // the split warned of is not the one used
		seen := map[int]string{}
		for _, role := range []struct{ name, set string }{{"server", p.ServerCPUs}, {"backends", p.BackendCPUs}, {"load generators", p.ClientCPUs}} {
			cpus, _ := parseCPUList(role.set)
			for _, c := range cpus {
				if other, ok := seen[c]; ok && other != role.name {
					p.Warnings = append(p.Warnings, fmt.Sprintf("CPU %d is given to both the %s and the %s", c, other, role.name))
				}
				seen[c] = role.name
			}
		}
	}
	p.Workers, p.BackendWorkers, p.ClientThreads = cpusetCount(p.ServerCPUs), cpusetCount(p.BackendCPUs), cpusetCount(p.ClientCPUs)
	// bounds are the top level's bounds for the split as it stands.
	bounds := func() map[string]int {
		b := make(map[string]int, len(base)+3)
		for k, v := range base {
			b[k] = v
		}
		// Each request in flight is a client connection, a backend
		// connection at the server, and one at each end: at most 4
		// descriptors.
		b["cpu"] = inFlightPerServerCPU * p.Workers
		b["client"] = inFlightPerClientThread * p.ClientThreads
		b["backend"] = inFlightPerBackendCPU * p.BackendWorkers
		return b
	}
	if !cpusSet && len(o.Levels) == 0 && o.Top == 0 {
		for range 3 {
			least = math.MaxInt
			for _, v := range bounds() {
				least = min(least, v)
			}
			next := floor125(least)
			if next == inFlight {
				break
			}
			inFlight = next
			server, backend, client, warnings, err = splitCores(m, inFlight)
			if err != nil {
				return p, err
			}
			p.ServerCPUs, p.BackendCPUs, p.ClientCPUs = formatCPUList(server), formatCPUList(backend), formatCPUList(client)
			p.Workers, p.BackendWorkers, p.ClientThreads = cpusetCount(p.ServerCPUs), cpusetCount(p.BackendCPUs), cpusetCount(p.ClientCPUs)
			p.Warnings = warnings
		}
	}
	p.TopBounds = bounds()
	least = math.MaxInt
	for _, v := range p.TopBounds {
		least = min(least, v)
	}
	p.Top = floor125(least)

	var levels []int
	switch {
	case len(o.Levels) > 0:
		levels = slices.Clone(o.Levels)
		slices.Sort(levels)
		levels = slices.Compact(levels)
		p.Top, p.Set = levels[len(levels)-1], append(p.Set, "levels")
	case o.Top > 0:
		p.Top, p.Set = o.Top, append(p.Set, "top level")
		fallthrough
	default:
		if p.Top < 100 {
			return p, fmt.Errorf("the machine holds at most %d requests in flight (%v); the plan starts at 100", least, p.TopBounds)
		}
		for _, n := range []int{100, 500, p.Top / 5, p.Top / 2, p.Top} {
			// 100, 500, and a fifth and a half of the top above 500.
			if n <= p.Top && (n == 100 || n >= 500 || n == p.Top) {
				levels = append(levels, n)
			}
		}
		slices.Sort(levels)
		levels = slices.Compact(levels)
	}
	if p.Top > least {
		p.Warnings = append(p.Warnings, fmt.Sprintf("the top level set, %d, is past what the machine was found to hold, %d", p.Top, least))
	}
	// The load generators and the backends must hold the top level, or they
	// set the pace instead of the server.
	for _, role := range []struct{ name, bound string }{
		{"the load generators", "client"},
		{"the backends", "backend"},
	} {
		if p.TopBounds[role.bound] < p.Top {
			p.Warnings = append(p.Warnings, fmt.Sprintf("%s hold at most %d requests in flight, below the top level %d, and may set the pace", role.name, p.TopBounds[role.bound], p.Top))
		}
	}
	for _, n := range levels {
		p.Levels = append(p.Levels, shape(n, p.ClientThreads))
	}
	// The level near 500, which W2 to W6 share.
	p.Mid = p.Levels[0]
	for _, l := range p.Levels {
		if abs(l.InFlight-500) < abs(p.Mid.InFlight-500) {
			p.Mid = l
		}
	}
	p.W2 = []Level{p.Mid}
	if i := slices.Index(p.Levels, p.Mid); i+1 < len(p.Levels) {
		p.W2 = append(p.W2, p.Levels[i+1])
	}
	p.W3 = []int{p.Levels[0].InFlight}
	if p.Mid.InFlight != p.W3[0] {
		p.W3 = append(p.W3, p.Mid.InFlight)
	}
	w4 := 100
	if m.TCPMemPressure > 0 {
		w4 = min(w4, max(1, floor125(int(m.TCPMemPressure/2/tcpPerTransfer))))
	}
	p.W4 = shape(min(w4, p.Levels[0].InFlight), p.ClientThreads)

	p.W5Bounds["top"] = idlePerTop * p.Top
	if m.MemBytes > 0 {
		p.W5Bounds["memory"] = max(0, int((m.MemBytes/4-int64(p.Mid.InFlight)*memPerInFlight)/memPerIdle))
	}
	// The server holds each idle connection and two descriptors for each
	// request in flight, in half its nofile; the host, both ends of each.
	p.W5Bounds["nofile"] = int(nofile/2) - 2*p.Mid.InFlight
	if m.FileMax > 0 && m.FileMax < math.MaxInt32 {
		p.W5Bounds["file_max"] = int(m.FileMax / 4)
	}
	p.W5Idle = math.MaxInt
	for _, v := range p.W5Bounds {
		p.W5Idle = min(p.W5Idle, v)
	}
	p.W5Idle = floorSig(p.W5Idle, 2)
	if p.W5Idle < 1000 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("W5 can hold only %d idle connections", p.W5Idle))
	}
	if o.W5Idle > 0 {
		p.W5Idle, p.Set = o.W5Idle, append(p.Set, "W5 idle connections")
	}

	if len(o.W6Rates) > 0 {
		p.W6Rates, p.W6From, p.Set = slices.Clone(o.W6Rates), "set", append(p.Set, "W6 rates")
	} else {
		scale := float64(p.Workers) / refServerCPUs
		if m.MaxFreqMHz > 0 {
			scale *= float64(m.MaxFreqMHz) / refMaxMHz
		}
		p.W6Rates = []int{max(1000, roundSig(refLowRate*scale, 1)), max(2000, roundSig(refHighRate*scale, 1))}
		p.W6From = fmt.Sprintf("estimate: the benchmark host's %d and %d for %d server CPUs at %d MHz, scaled; the plan calibrates them from W1 at %d", refLowRate, refHighRate, refServerCPUs, refMaxMHz, p.Mid.InFlight)
	}

	idle := max(p.W5Idle, 0)
	p.Aligned = alignedFor(p.Top, idle, p.Workers, o.BackendConns, o.Backlog, m.Somaxconn)
	if o.BackendConns > 0 {
		p.Set = append(p.Set, "backend connections")
		if o.BackendConns <= p.Top {
			p.Warnings = append(p.Warnings, fmt.Sprintf("the backend connection bound %d does not exceed the top level %d: requests will wait for a connection", o.BackendConns, p.Top))
		}
	}
	if o.Backlog > 0 {
		p.Set = append(p.Set, "backlog")
	}
	if m.Somaxconn > 0 && p.Aligned.Backlog > m.Somaxconn {
		p.Warnings = append(p.Warnings, fmt.Sprintf("the backlog %d is past somaxconn %d, which caps it", p.Aligned.Backlog, m.Somaxconn))
	}
	if m.RmemMax > 0 && m.RmemMax < 7500000 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("net.core.rmem_max is %d: the HTTP/3 servers that ask for about 7 MB of UDP buffer get less (plan.sh applies /etc/sysctl.d/90-fitbench-udp-buffers.conf when it is there)", m.RmemMax))
	}

	p.Duration, p.Warmup = (15 * time.Second).String(), (5 * time.Second).String()
	d, w := 15*time.Second, 5*time.Second
	if o.Duration > 0 {
		d, p.Duration = o.Duration, o.Duration.String()
	}
	if o.Warmup > 0 {
		w, p.Warmup = o.Warmup, o.Warmup.String()
	}
	p.Runs = runsW0*len(p.Levels) + runsW1*len(p.Levels) + runsW2*len(p.W2) + runsW3*len(p.W3) + runsW4 + runsW5 + runsW6*len(p.W6Rates)
	p.Estimated = (time.Duration(p.Runs) * (d + w + runOverhead)).Round(time.Minute).String()
	return p, nil
}

func abs(n int) int { return max(n, -n) }

// calibrate sets W6's rates from W1's results at the mid level: the low
// rate about half the slowest server's ceiling, so that every server runs
// below it; the high one between the servers on either side of the widest
// gap between their ceilings, past the slower ones' and within the faster
// ones'. A server's ceiling is the least it answered over any protocol.
func calibrate(results []Result) (rates []int, from string, err error) {
	ceil, most := map[string]float64{}, map[string]float64{}
	for _, r := range results {
		if r.Scenario != "api" || r.Error != "" || r.Skipped != "" || r.RequestsPerSec <= 0 || r.Rate > 0 {
			continue
		}
		if c, ok := ceil[r.Server]; !ok || r.RequestsPerSec < c {
			ceil[r.Server] = r.RequestsPerSec
		}
		most[r.Server] = max(most[r.Server], r.RequestsPerSec)
	}
	if len(ceil) == 0 {
		return nil, "", errors.New("no API results to calibrate from")
	}
	var names []string
	for n := range ceil {
		names = append(names, n)
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Compare(ceil[a], ceil[b]) })
	low := roundSig(ceil[names[0]]/2, 1)
	if len(names) == 1 {
		high := roundSig(ceil[names[0]]*0.9, 1)
		return []int{low, high}, fmt.Sprintf("calibration: %s alone, %.0f a second at most", names[0], ceil[names[0]]), nil
	}
	gap := 0
	for i := 1; i+1 < len(names); i++ {
		if ceil[names[i+1]]/ceil[names[i]] > ceil[names[gap+1]]/ceil[names[gap]] {
			gap = i
		}
	}
	slowMost := 0.0
	for _, n := range names[:gap+1] {
		slowMost = max(slowMost, most[n])
	}
	fastLeast := ceil[names[gap+1]]
	high := 0
	if slowMost < fastLeast {
		mid := (slowMost + fastLeast) / 2
		for sig := 1; sig <= 4; sig++ {
			if v := float64(roundSig(mid, sig)); v > slowMost && v < fastLeast {
				high = int(v)
				break
			}
		}
	}
	if high == 0 {
		high = roundSig(fastLeast*0.9, 2)
	}
	from = fmt.Sprintf("calibration: the slowest, %s, answered %.0f a second; %s at most %.0f, %s at least %.0f",
		names[0], ceil[names[0]], strings.Join(names[:gap+1], ", "), slowMost, strings.Join(names[gap+1:], ", "), fastLeast)
	return []int{low, max(high, low+1)}, from, nil
}

// runPlan is fitbench plan: it derives the plan and prints it.
func runPlan(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("fitbench plan", flag.ContinueOnError)
	var o Overrides
	sys := flags.String("sys", "/", "the root of the filesystem /sys and /proc are read from")
	format := flags.String("format", "text", `"text" to read, "sh" for bench/plan.sh, or "json"`)
	calibration := flags.String("calibration", "", "a results file of W1 at the mid level, to set W6's rates from")
	flags.StringVar(&o.ServerCPUs, "server-cpus", "", "the server's CPUs, in place of the derived ones")
	flags.StringVar(&o.BackendCPUs, "backend-cpus", "", "the backends' CPUs")
	flags.StringVar(&o.ClientCPUs, "client-cpus", "", "the load generators' CPUs")
	levels := flags.String("levels", "", "the levels of load, requests in flight, as 100,500,2000")
	flags.IntVar(&o.Top, "top", 0, "the top level, from which the others are derived")
	flags.IntVar(&o.W5Idle, "w5-idle", 0, "W5's idle TLS connections")
	rates := flags.String("w6-rates", "", "W6's rates, requests a second, as 20000,100000")
	flags.IntVar(&o.BackendConns, "backend-conns", 0, "the backend connection bound")
	flags.IntVar(&o.Backlog, "backlog", 0, "the listen backlog")
	flags.DurationVar(&o.Duration, "duration", 0, "each measured run; 15s when 0")
	flags.DurationVar(&o.Warmup, "warmup", 0, "each warm-up; 5s when 0")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("fitbench plan: unexpected %q", flags.Args())
	}
	var err error
	if o.Levels, err = parseInts(*levels); err != nil {
		return fmt.Errorf("-levels: %v", err)
	}
	if o.W6Rates, err = parseInts(*rates); err != nil {
		return fmt.Errorf("-w6-rates: %v", err)
	}
	m, err := detectMachine(os.DirFS(*sys), os.Getpagesize())
	if err != nil {
		return err
	}
	p, err := derivePlan(m, o)
	if err != nil {
		return err
	}
	if *calibration != "" && len(o.W6Rates) == 0 {
		data, err := os.ReadFile(*calibration)
		if err != nil {
			return err
		}
		var rep Report
		if err := json.Unmarshal(data, &rep); err != nil {
			return fmt.Errorf("%s: %v", *calibration, err)
		}
		if rates, from, err := calibrate(rep.Results); err == nil {
			p.W6Rates, p.W6From = rates, from
		} else {
			p.Warnings = append(p.Warnings, fmt.Sprintf("W6 keeps the estimated rates: %s: %v", *calibration, err))
		}
	}
	switch *format {
	case "text":
		printPlan(stdout, p)
	case "sh":
		printPlanShell(stdout, p)
	case "json":
		data, _ := json.MarshalIndent(p, "", "  ")
		fmt.Fprintf(stdout, "%s\n", data)
	default:
		return fmt.Errorf("-format: %q is not text, sh or json", *format)
	}
	return nil
}

func parseInts(list string) ([]int, error) {
	var out []int
	for s := range strings.SplitSeq(list, ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("%q is not a positive number", s)
		}
		out = append(out, n)
	}
	return out, nil
}

// printPlanShell prints the plan as variables for bench/plan.sh to eval.
func printPlanShell(w io.Writer, p Plan) {
	args := func(ls []Level) string {
		var s []string
		for _, l := range ls {
			s = append(s, l.arg())
		}
		return strings.Join(s, " ")
	}
	ints := func(ns []int) string {
		var s []string
		for _, n := range ns {
			s = append(s, strconv.Itoa(n))
		}
		return strings.Join(s, " ")
	}
	fmt.Fprintf(w, "PLAN_DURATION=%s\nPLAN_WARMUP=%s\n", p.Duration, p.Warmup)
	fmt.Fprintf(w, "PLAN_CPUS='-server-cpus %s -backend-cpus %s -client-cpus %s'\n", p.ServerCPUs, p.BackendCPUs, p.ClientCPUs)
	fmt.Fprintf(w, "PLAN_ALIGNED='-backend-conns %d -backlog %d'\n", p.Aligned.BackendConns, p.Aligned.Backlog)
	fmt.Fprintf(w, "PLAN_LEVELS='%s'\nPLAN_MID=%d\nPLAN_W2_LEVELS='%s'\nPLAN_W3_CONNS='%s'\n", args(p.Levels), p.Mid.InFlight, args(p.W2), ints(p.W3))
	fmt.Fprintf(w, "PLAN_W4_LEVEL='%s'\nPLAN_MID_LEVEL='%s'\nPLAN_W5_IDLE=%d\n", p.W4.arg(), p.Mid.arg(), p.W5Idle)
	fmt.Fprintf(w, "PLAN_W6_RATES='%s'\nPLAN_W6_SET=%t\nPLAN_RUNS=%d\nPLAN_ESTIMATED=%s\n", ints(p.W6Rates), p.W6From == "set", p.Runs, p.Estimated)
}

// printPlan prints the plan to be read, as a dry run.
func printPlan(w io.Writer, p Plan) {
	m := p.Machine
	fmt.Fprintf(w, "machine     %s\n", m.Model)
	fmt.Fprintf(w, "            %d CPUs, %d cores of %d threads, %d NUMA node(s), %d MHz at most, %.1f GiB\n",
		m.CPUs, m.CoreCount, m.ThreadsPerCore, m.Nodes, m.MaxFreqMHz, float64(m.MemBytes)/(1<<30))
	fmt.Fprintf(w, "            somaxconn %d, nr_open %d, file-max %d, ports %d-%d, rmem_max %d, tcp_mem pressure %d MiB\n",
		m.Somaxconn, m.NrOpen, m.FileMax, m.PortLo, m.PortHi, m.RmemMax, m.TCPMemPressure>>20)
	fmt.Fprintf(w, "split       server %s (%d workers), backends %s (%d), load generators %s (%d threads)\n",
		p.ServerCPUs, p.Workers, p.BackendCPUs, p.BackendWorkers, p.ClientCPUs, p.ClientThreads)
	fmt.Fprintf(w, "top level   %d in flight; each input allows %s\n", p.Top, bounds(p.TopBounds))
	var ls []string
	for _, l := range p.Levels {
		ls = append(ls, levelText(l))
	}
	fmt.Fprintf(w, "levels      %s\n", strings.Join(ls, ", "))
	fmt.Fprintf(w, "W0, W1      every level: %d and %d runs\n", runsW0*len(p.Levels), runsW1*len(p.Levels))
	var w2 []string
	for _, l := range p.W2 {
		w2 = append(w2, levelText(l))
	}
	fmt.Fprintf(w, "W2          %s: %d runs\n", strings.Join(w2, ", "), runsW2*len(p.W2))
	fmt.Fprintf(w, "W3          %v connections: %d runs\n", p.W3, runsW3*len(p.W3))
	fmt.Fprintf(w, "W4          %s: %d runs\n", levelText(p.W4), runsW4)
	fmt.Fprintf(w, "W5          %s with %d idle TLS connections (each input allows %s): %d runs\n", levelText(p.Mid), p.W5Idle, bounds(p.W5Bounds), runsW5)
	fmt.Fprintf(w, "W6          %s at %v a second: %d runs\n            (%s)\n", levelText(p.Mid), p.W6Rates, runsW6*len(p.W6Rates), p.W6From)
	a := p.Aligned
	fmt.Fprintf(w, "aligned     backend connections and waiting requests %d, nginx idle %d a worker, backlog %d, nginx worker_connections %d\n",
		a.BackendConns, a.IdlePerWorker, a.Backlog, a.WorkerConnections)
	fmt.Fprintf(w, "runs        %d of %s warm-up and %s measured, about %s\n", p.Runs, p.Warmup, p.Duration, p.Estimated)
	if len(p.Set) > 0 {
		fmt.Fprintf(w, "set         %s\n", strings.Join(p.Set, ", "))
	}
	for _, warning := range p.Warnings {
		fmt.Fprintf(w, "warning     %s\n", warning)
	}
}

func levelText(l Level) string {
	return fmt.Sprintf("%d (wrk %d, h2load %d×%d)", l.InFlight, l.Conns, l.H2Conns, l.H2Streams)
}

func bounds(b map[string]int) string {
	var keys []string
	for k := range b {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var s []string
	for _, k := range keys {
		s = append(s, fmt.Sprintf("%s %d", k, b[k]))
	}
	return strings.Join(s, ", ")
}

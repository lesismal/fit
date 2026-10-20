// Command fitbench runs nginx, Caddy, Traefik and fit through the same
// scenarios under the same load generators, and writes the results as JSON
// with the hardware and the versions they were measured on.
//
// Every server, the proxy backend and the load generators run in Docker
// containers, each pinned to CPUs of its own. Run it from the repository:
//
//	go run ./bench/cmd/fitbench
//
// See bench/README.md for what it measures and how to read the results.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "fitbench:", err)
		os.Exit(1)
	}
}

// Report is what a run writes.
type Report struct {
	Started  time.Time         `json:"started"`
	Finished time.Time         `json:"finished"`
	Host     map[string]string `json:"host"`
	Params   Params            `json:"params"`
	// Versions are the servers' and the load generators' own, and Images
	// the images they ran from, with their digests.
	Versions map[string]string `json:"versions"`
	Images   map[string]string `json:"images"`
	Results  []Result          `json:"results"`
	Idle     []IdleResult      `json:"idle,omitempty"`
}

// Params are the settings a run was made with.
type Params struct {
	Duration      string `json:"duration"`
	Warmup        string `json:"warmup"`
	Connections   int    `json:"connections"`
	H2Connections int    `json:"h2_connections"`
	H2Streams     int    `json:"h2_streams"`
	IdleConns     int    `json:"idle_connections,omitempty"`
	IdleSettle    string `json:"idle_settle,omitempty"`
	Network       string `json:"network"`
	ServerCPUs    string `json:"server_cpus"`
	ClientCPUs    string `json:"client_cpus"`
	BackendCPUs   string `json:"backend_cpus"`
}

// Container names; one of each runs at a time.
const (
	serverName    = "fitbench-server"
	backendName   = "fitbench-backend"
	wsBackendName = "fitbench-wsbackend"
	loadgenName   = "fitbench-loadgen"
	networkName   = "fitbench"
	loadgenTag    = "fitbench-loadgen"
	// wwwVolume holds the static files: a volume rather than a bind mount,
	// which on Docker Desktop is slow enough to be what is measured.
	wwwVolume = "fitbench-www"
)

type harness struct {
	servers    []server
	scenarios  []string
	protocols  []string
	duration   time.Duration
	warmup     time.Duration
	conns      int
	h2conns    int
	h2streams  int
	idle       []string
	idleConns  int
	idleSettle time.Duration
	baseImage  string
	images     map[string]string

	bench       string // the bench directory
	work        string // bench/.work: the fit binary, the rendered configs
	netArgs     []string
	serverHost  string
	serverCPUs  []string // docker run arguments pinning each container
	clientCPUs  []string
	backendCPUs []string
	threads     int // load generator threads: one per client CPU
	report      Report
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("fitbench", flag.ContinueOnError)
	h := &harness{images: map[string]string{}}
	serverList := flags.String("servers", "nginx,caddy,traefik,fit", "the servers to run")
	scenarioList := flags.String("scenarios", "fixed,static,proxy", "the scenarios to run")
	protocolList := flags.String("protocols", "http/1.1,h2c", "the protocols to run")
	flags.DurationVar(&h.duration, "duration", 30*time.Second, "how long each measured run lasts")
	flags.DurationVar(&h.warmup, "warmup", 5*time.Second, "how long the load runs, unmeasured, before each measured run")
	flags.IntVar(&h.conns, "connections", 256, "HTTP/1.1 connections (wrk)")
	flags.IntVar(&h.h2conns, "h2-connections", 64, "h2c connections (h2load)")
	flags.IntVar(&h.h2streams, "h2-streams", 32, "concurrent streams per h2c connection (h2load)")
	idleList := flags.String("idle", "http1,tls,ws", "the kinds of idle connection to measure memory for; empty for none")
	flags.IntVar(&h.idleConns, "idle-connections", 10000, "how many idle connections to open")
	flags.DurationVar(&h.idleSettle, "idle-settle", 5*time.Second, "how long the idle connections sit before the server's memory is read")
	network := flags.String("network", defaultNetwork(), `"host" (Linux; what published numbers use) or "bridge"`)
	cpuFlags := [3]*string{
		flags.String("server-cpus", "", "the CPUs the server is pinned to, as in 0-7; split from what Docker has when empty"),
		flags.String("client-cpus", "", "the CPUs the load generators are pinned to"),
		flags.String("backend-cpus", "", "the CPUs the proxy backend is pinned to"),
	}
	flags.StringVar(&h.baseImage, "base-image", "ubuntu:24.04", "the image fit, the backend and the load generators run on")
	for _, s := range servers {
		if !s.fit {
			flags.StringVar(new(string), s.name+"-image", s.image, "the "+s.name+" image")
		}
	}
	out := flags.String("out", "", "where to write the results; bench/results/run-<time>.json when empty")
	rootDir := flags.String("root", "", "the fit repository; the module go finds when empty")
	binDir := flags.String("bin", "", "a directory with fit and idleconn already built for the host, for a host without Go; they are built when empty")
	if err := flags.Parse(args); err != nil {
		return err
	}
	for _, s := range servers {
		if f := flags.Lookup(s.name + "-image"); f != nil {
			h.images[s.name] = f.Value.String()
		}
	}

	var err error
	if h.servers, err = pick(*serverList, servers, func(s server) string { return s.name }); err != nil {
		return err
	}
	if *scenarioList != "" {
		if h.scenarios, err = pickNames(*scenarioList, scenarioNames()); err != nil {
			return err
		}
	}
	if h.protocols, err = pickNames(*protocolList, protocolNames()); err != nil {
		return err
	}
	if *idleList != "" {
		if h.idle, err = pickNames(*idleList, idleKindNames()); err != nil {
			return err
		}
	}

	root := *rootDir
	if root == "" {
		if root, err = moduleRoot(); err != nil {
			return err
		}
	}
	h.bench = filepath.Join(root, "bench")
	h.work = filepath.Join(h.bench, ".work")
	if *out == "" {
		*out = filepath.Join(h.bench, "results", "run-"+time.Now().UTC().Format("20060102-150405")+".json")
	}

	info, err := docker(ctx, "info", "--format", "{{.NCPU}} {{.Architecture}}")
	if err != nil {
		return err
	}
	var ncpu int
	var arch string
	if _, err := fmt.Sscan(info, &ncpu, &arch); err != nil {
		return fmt.Errorf("docker info: %q: %v", info, err)
	}

	backendAddr, wsBackendAddr := "127.0.0.1:9000", "127.0.0.1:9001"
	switch *network {
	case "host":
		h.netArgs, h.serverHost = []string{"--network", "host"}, "127.0.0.1"
	case "bridge":
		h.netArgs, h.serverHost = []string{"--network", networkName}, serverName
		backendAddr, wsBackendAddr = backendName+":9000", wsBackendName+":9001"
	default:
		return fmt.Errorf("-network: %q is neither host nor bridge", *network)
	}

	cpus := splitCPUs(ncpu, true)
	for i, f := range cpuFlags {
		if *f != "" {
			cpus[i] = *f
		}
	}
	pin := func(set string) []string {
		if set == "" {
			return nil
		}
		return []string{"--cpuset-cpus", set}
	}
	h.serverCPUs, h.clientCPUs, h.backendCPUs = pin(cpus[0]), pin(cpus[1]), pin(cpus[2])
	serverThreads := cpusetCount(cpus[0])
	if serverThreads == 0 {
		serverThreads = ncpu
	}
	if h.threads = cpusetCount(cpus[1]); h.threads == 0 {
		h.threads = ncpu
	}

	h.report = Report{
		Started:  time.Now().UTC(),
		Host:     map[string]string{"docker_cpus": strconv.Itoa(ncpu), "docker_arch": arch},
		Versions: map[string]string{},
		Images:   map[string]string{},
		Params: Params{
			Duration: h.duration.String(), Warmup: h.warmup.String(),
			Connections: max(h.conns, h.threads), H2Connections: max(h.h2conns, h.threads), H2Streams: h.h2streams,
			IdleConns: h.idleConns, IdleSettle: h.idleSettle.String(),
			Network: *network, ServerCPUs: cpus[0], ClientCPUs: cpus[1], BackendCPUs: cpus[2],
		},
	}

	if *binDir != "" {
		if err := h.copyTools(*binDir); err != nil {
			return err
		}
	} else if err := h.buildTools(ctx, root, arch); err != nil {
		return err
	}
	if err := h.renderConfigs(map[string]string{"{{BACKEND}}": backendAddr, "{{WSBACKEND}}": wsBackendAddr, "{{WORKERS}}": strconv.Itoa(serverThreads)}); err != nil {
		return err
	}
	if err := writeCert(filepath.Join(h.work, "certs")); err != nil {
		return err
	}
	step("building the load generator image")
	if _, err := docker(ctx, "build", "-q", "-t", loadgenTag, "--build-arg", "BASE="+h.baseImage, filepath.Join(h.bench, "loadgen")); err != nil {
		return err
	}

	defer cleanup(*network == "bridge")
	cleanup(*network == "bridge")
	if *network == "bridge" {
		if _, err := docker(ctx, "network", "create", networkName); err != nil {
			return err
		}
	}
	if _, err := docker(ctx, "run", "--rm", "-v", wwwVolume+":/srv/www", "-v", filepath.Join(h.bench, "www")+":/src:ro",
		h.baseImage, "cp", "-r", "/src/.", "/srv/www/"); err != nil {
		return err
	}
	step("starting the load generators")
	if _, err := docker(ctx, append(append(append([]string{"run", "-d", "--name", loadgenName, "--ulimit", "nofile=1048576:1048576",
		"-v", filepath.Join(h.work, "idleconn") + ":/fitbench/idleconn:ro"},
		h.netArgs...), h.clientCPUs...), loadgenTag, "sleep", "infinity")...); err != nil {
		return err
	}
	h.describeHost(ctx)

	step("starting the proxy backend")
	if err := h.start(ctx, backend, backendName, h.backendCPUs); err != nil {
		return err
	}
	if err := h.waitReady(ctx, backendName, "http://"+backendAddr+backend.ready); err != nil {
		return err
	}

	// Both backends run whatever is chosen: nginx will not start while a
	// name in its config does not resolve.
	step("starting the WebSocket backend")
	wsArgs := append(append([]string{"run", "-d", "--name", wsBackendName, "--ulimit", "nofile=1048576:1048576",
		"-v", filepath.Join(h.work, "idleconn") + ":/usr/local/bin/idleconn:ro"}, h.netArgs...), h.backendCPUs...)
	if _, err := docker(ctx, append(wsArgs, h.baseImage, "/usr/local/bin/idleconn", "-serve-ws", ":9001")...); err != nil {
		return err
	}

	for _, s := range h.servers {
		if err := ctx.Err(); err != nil {
			return err
		}
		h.runServer(ctx, s)
		h.runIdle(ctx, s)
	}
	h.report.Finished = time.Now().UTC()

	data, err := json.MarshalIndent(h.report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		return err
	}
	printSummary(h.report.Results, h.report.Idle)
	fmt.Println("results written to", *out)
	return nil
}

func defaultNetwork() string {
	if runtime.GOOS == "linux" {
		return "host"
	}
	return "bridge"
}

func scenarioNames() []string {
	var names []string
	for _, s := range scenarios {
		names = append(names, s.name)
	}
	return names
}

func protocolNames() []string {
	var names []string
	for _, p := range protocols {
		names = append(names, p.name)
	}
	return names
}

// pick returns the items of all named in the comma-separated list, in all's
// order.
func pick[T any](list string, all []T, name func(T) string) ([]T, error) {
	want := strings.Split(list, ",")
	var picked []T
	for _, item := range all {
		if i := slices.Index(want, name(item)); i >= 0 {
			picked = append(picked, item)
			want = slices.Delete(want, i, i+1)
		}
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("unknown: %s", strings.Join(want, ", "))
	}
	return picked, nil
}

func pickNames(list string, all []string) ([]string, error) {
	return pick(list, all, func(s string) string { return s })
}

// splitCPUs divides n CPUs between the server, the load generators and the
// proxy backend, in that order: the backend an eighth, at least one, when
// it is needed, and the server and the load generators half each of the
// rest. With fewer than four CPUs nothing is pinned.
func splitCPUs(n int, needBackend bool) [3]string {
	if n < 4 {
		return [3]string{}
	}
	b := 0
	if needBackend {
		b = max(1, n/8)
	}
	s := (n - b) / 2
	c := n - b - s
	set := func(from, count int) string {
		if count == 0 {
			return ""
		}
		if count == 1 {
			return strconv.Itoa(from)
		}
		return fmt.Sprintf("%d-%d", from, from+count-1)
	}
	return [3]string{set(0, s), set(s, c), set(s+c, b)}
}

// cpusetCount counts the CPUs in a list such as "0-3,8"; 0 for an empty or
// malformed one.
func cpusetCount(set string) int {
	n := 0
	for part := range strings.SplitSeq(set, ",") {
		lo, hi, isRange := strings.Cut(strings.TrimSpace(part), "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return 0
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(hi); err != nil || b < a {
				return 0
			}
		}
		n += b - a + 1
	}
	return n
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	gomod := strings.TrimSpace(string(out))
	if err != nil || gomod == "" || gomod == os.DevNull {
		return "", errors.New("run fitbench from within the fit repository")
	}
	return filepath.Dir(gomod), nil
}

// buildTools builds fit and idleconn for Linux on Docker's architecture,
// statically, to be mounted into the base image.
func (h *harness) buildTools(ctx context.Context, root, arch string) error {
	goarch := map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64", "amd64": "amd64"}[arch]
	if goarch == "" {
		return fmt.Errorf("docker architecture %q is not one fitbench knows", arch)
	}
	step("building fit and idleconn for linux/" + goarch)
	for name, pkg := range map[string]string{"fit": "./cmd/fit", "idleconn": "./bench/cmd/idleconn"} {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", filepath.Join(h.work, name), pkg)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s: %v\n%s", name, err, out)
		}
	}
	return nil
}

// copyTools takes fit and idleconn from dir, built elsewhere for this host,
// for a host without Go.
func (h *harness) copyTools(dir string) error {
	step("taking fit and idleconn from " + dir)
	if err := os.MkdirAll(h.work, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"fit", "idleconn"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(h.work, name), data, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// renderConfigs copies bench/configs into the work directory with vars
// replaced.
func (h *harness) renderConfigs(vars map[string]string) error {
	src := filepath.Join(h.bench, "configs")
	dst := filepath.Join(h.work, "configs")
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(data)
		for k, v := range vars {
			s = strings.ReplaceAll(s, k, v)
		}
		return os.WriteFile(filepath.Join(dst, rel), []byte(s), 0o644)
	})
}

// describeHost records the hardware and the load generators' versions, as
// the load generator container sees them.
func (h *harness) describeHost(ctx context.Context) {
	if out, err := docker(ctx, "exec", loadgenName, "lscpu"); err == nil {
		for line := range strings.SplitSeq(out, "\n") {
			if k, v, ok := strings.Cut(line, ":"); ok && (k == "Model name" || k == "Architecture" || k == "CPU(s)") {
				h.report.Host[strings.ToLower(strings.ReplaceAll(k, " ", "_"))] = strings.TrimSpace(v)
			}
		}
	}
	if out, err := docker(ctx, "exec", loadgenName, "uname", "-sr"); err == nil {
		h.report.Host["kernel"] = strings.TrimSpace(out)
	}
	if out, err := docker(ctx, "exec", loadgenName, "grep", "MemTotal", "/proc/meminfo"); err == nil {
		h.report.Host["memory"] = strings.Join(strings.Fields(out)[1:], " ")
	}
	h.report.Host["os"] = runtime.GOOS
	if runtime.GOOS == "darwin" {
		// Docker runs in a VM, whose lscpu knows nothing of the CPU model.
		if out, err := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			h.report.Host["model_name"] = strings.TrimSpace(string(out))
		}
	}
	if out, err := docker(ctx, "version", "--format", "{{.Server.Version}}"); err == nil {
		h.report.Versions["docker"] = strings.TrimSpace(out)
	}
	out, _ := docker(ctx, "exec", loadgenName, "wrk", "-v") // exits 1 after printing it
	h.report.Versions["wrk"] = firstLine(out, "wrk ")
	out, _ = docker(ctx, "exec", loadgenName, "h2load", "--version")
	h.report.Versions["h2load"] = firstLine(out, "h2load")
}

func firstLine(out, prefix string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return strings.TrimSpace(out)
}

// start runs s in a container called name.
func (h *harness) start(ctx context.Context, s server, name string, cpus []string) error {
	args := append([]string{"run", "-d", "--name", name, "--ulimit", "nofile=1048576:1048576"}, h.netArgs...)
	args = append(args, cpus...)
	for _, m := range s.mounts {
		src := filepath.Join(h.work, "configs", m[0])
		switch m[0] {
		case "www":
			src = wwwVolume
		case "certs":
			src = filepath.Join(h.work, "certs")
		}
		args = append(args, "-v", src+":"+m[1]+":ro")
	}
	image := h.images[s.name]
	if s.fit {
		image = h.baseImage
		args = append(args, "-v", filepath.Join(h.work, "fit")+":/usr/local/bin/fit:ro")
	}
	args = append(args, image)
	args = append(args, s.cmd...)
	_, err := docker(ctx, args...)
	return err
}

// waitReady waits until url answers 200.
func (h *harness) waitReady(ctx context.Context, name, url string) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		out, _ := docker(ctx, "exec", loadgenName, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "1", url)
		if strings.TrimSpace(out) == "200" {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs, _ := docker(context.Background(), "logs", "--tail", "20", name)
	return fmt.Errorf("%s did not answer at %s within 30s; its last logs:\n%s", name, url, logs)
}

// runServer runs every chosen scenario and protocol against s.
func (h *harness) runServer(ctx context.Context, s server) {
	var todo []string
	for _, sc := range h.scenarios {
		if slices.Contains(s.scenarios, sc) {
			todo = append(todo, sc)
		}
	}
	add := func(r Result) {
		h.report.Results = append(h.report.Results, r)
		printResult(r)
	}
	fail := func(err error) {
		for _, sc := range todo {
			for _, p := range h.protocols {
				add(Result{Server: s.name, Scenario: sc, Protocol: p, Error: err.Error()})
			}
		}
	}
	for _, sc := range h.scenarios {
		if !slices.Contains(s.scenarios, sc) {
			for _, p := range h.protocols {
				add(Result{Server: s.name, Scenario: sc, Protocol: p, Skipped: s.name + " cannot serve it"})
			}
		}
	}
	if len(todo) == 0 {
		return
	}

	step("starting " + s.name)
	defer func() { _, _ = docker(context.Background(), "rm", "-f", serverName) }()
	if err := h.start(ctx, s, serverName, h.serverCPUs); err != nil {
		fail(err)
		return
	}
	if err := h.waitReady(ctx, serverName, "http://"+h.serverHost+":8080"+s.ready); err != nil {
		fail(err)
		return
	}
	out, _ := docker(ctx, append([]string{"exec", serverName}, s.version...)...)
	h.report.Versions[s.name] = strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	if s.fit {
		h.report.Versions[s.name] = strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(out), "\n", "; "))
		h.report.Images[s.name] = imageDigest(ctx, h.baseImage)
	} else {
		h.report.Images[s.name] = imageDigest(ctx, h.images[s.name])
	}

	for _, sc := range todo {
		for _, p := range h.protocols {
			if ctx.Err() != nil {
				return
			}
			add(h.measure(ctx, s, sc, p))
		}
	}
}

func imageDigest(ctx context.Context, image string) string {
	out, err := docker(ctx, "image", "inspect", "--format", "{{join .RepoDigests \",\"}}", image)
	if err != nil || strings.TrimSpace(out) == "" {
		return image
	}
	return strings.TrimSpace(out)
}

// measure checks that s answers scenario sc over protocol p as every other
// server does, warms it up, and measures it.
func (h *harness) measure(ctx context.Context, s server, sc, p string) Result {
	r := Result{Server: s.name, Scenario: sc, Protocol: p}
	i := slices.IndexFunc(scenarios, func(x struct{ name, path, body string }) bool { return x.name == sc })
	j := slices.IndexFunc(protocols, func(x struct{ name, port, tool string }) bool { return x.name == p })
	scenario, proto := scenarios[i], protocols[j]
	r.Tool = proto.tool
	url := "http://" + h.serverHost + ":" + proto.port + scenario.path

	if err := h.check(ctx, url, p, scenario.body); err != nil {
		r.Error = err.Error()
		return r
	}
	if h.warmup > 0 {
		if _, err := docker(ctx, h.loadArgs(proto.tool, url, h.warmup)...); err != nil {
			r.Error = "warmup: " + err.Error()
			return r
		}
	}
	cpu0, cpuOK := h.serverCPU(ctx)
	start := time.Now()
	out, err := docker(ctx, h.loadArgs(proto.tool, url, h.duration)...)
	elapsed := time.Since(start)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if cpu1, ok := h.serverCPU(ctx); ok && cpuOK {
		r.CPUCores = float64(cpu1-cpu0) / float64(elapsed)
	}
	if out, err := docker(ctx, "exec", serverName, "cat", "/sys/fs/cgroup/memory.current"); err == nil {
		r.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	}
	if out, err := docker(ctx, "exec", serverName, "cat", "/sys/fs/cgroup/memory.stat"); err == nil {
		r.AnonBytes, _ = cgroupAnon(out)
	}
	if proto.tool == "wrk" {
		err = parseWrk(out, &r)
	} else {
		err = parseH2load(out, &r)
	}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

// check fetches url once over protocol p and compares the response with
// body, or with the static file when body is empty.
func (h *harness) check(ctx context.Context, url, p, body string) error {
	if body == "" {
		data, err := os.ReadFile(filepath.Join(h.bench, "www", "static", "small.html"))
		if err != nil {
			return err
		}
		body = string(data)
	}
	flag := "--http1.1"
	if p == "h2c" {
		flag = "--http2-prior-knowledge"
	}
	out, err := docker(ctx, "exec", loadgenName, "curl", "-s", "--max-time", "5", flag, "-w", "\n%{http_code} %{http_version}", url)
	if err != nil {
		return fmt.Errorf("check %s: %v", url, err)
	}
	i := strings.LastIndexByte(out, '\n')
	if i < 0 {
		return fmt.Errorf("check %s: curl printed %q", url, out)
	}
	got, status := out[:i], strings.TrimSpace(out[i+1:])
	want := "200 1.1"
	if p == "h2c" {
		want = "200 2"
	}
	if status != want || got != body {
		return fmt.Errorf("check %s: got %q with body %.80q, want %q with body %.80q", url, status, got, want, body)
	}
	return nil
}

// loadArgs is the docker exec that runs tool against url for d.
func (h *harness) loadArgs(tool, url string, d time.Duration) []string {
	secs := strconv.Itoa(max(1, int(d.Round(time.Second).Seconds())))
	t := strconv.Itoa(h.threads)
	if tool == "wrk" {
		c := strconv.Itoa(max(h.conns, h.threads))
		return []string{"exec", loadgenName, "wrk", "-t", t, "-c", c, "-d", secs + "s", "--timeout", "10s", "--latency", "-s", "/fitbench/report.lua", url}
	}
	c := strconv.Itoa(max(h.h2conns, h.threads))
	return []string{"exec", loadgenName, "h2load", "-t", t, "-c", c, "-m", strconv.Itoa(h.h2streams), "-D", secs, url}
}

// serverCPU is the CPU time the server's container has used so far.
func (h *harness) serverCPU(ctx context.Context) (time.Duration, bool) {
	out, err := docker(ctx, "exec", serverName, "cat", "/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return 0, false
	}
	return cgroupCPU(out)
}

// cleanup removes every container and the network a run makes, as left by
// this run or an earlier one that was interrupted.
func cleanup(network bool) {
	ctx := context.Background()
	_, _ = docker(ctx, "rm", "-f", serverName, backendName, wsBackendName, loadgenName)
	_, _ = docker(ctx, "volume", "rm", wwwVolume)
	if network {
		_, _ = docker(ctx, "network", "rm", networkName)
	}
}

func docker(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker %s: %v: %s", strings.Join(args[:min(3, len(args))], " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func step(msg string) { fmt.Println("==>", msg) }

func printResult(r Result) {
	name := fmt.Sprintf("%-8s %-7s %-9s", r.Server, r.Scenario, r.Protocol)
	switch {
	case r.Skipped != "":
		fmt.Printf("    %s skipped: %s\n", name, r.Skipped)
	case r.Error != "":
		fmt.Printf("    %s error: %s\n", name, r.Error)
	default:
		fmt.Printf("    %s %12.0f req/s  mean %7.2fms  max %8.2fms  errors %d  cpu %.2f\n",
			name, r.RequestsPerSec, r.Latency.Mean, r.Latency.Max, r.Errors, r.CPUCores)
	}
}

// printSummary prints requests per second as a table: a row per scenario
// and protocol, a column per server.
func printSummary(results []Result, idle []IdleResult) {
	var names []string
	rows := map[string]map[string]string{}
	var order []string
	for _, r := range results {
		if !slices.Contains(names, r.Server) {
			names = append(names, r.Server)
		}
		key := r.Scenario + " " + r.Protocol
		if rows[key] == nil {
			rows[key] = map[string]string{}
			order = append(order, key)
		}
		cell := "-"
		switch {
		case r.Error != "":
			cell = "error"
		case r.Skipped == "":
			cell = strconv.FormatFloat(r.RequestsPerSec, 'f', 0, 64)
		}
		rows[key][r.Server] = cell
	}
	// A run of idle connections alone has no throughput results to name
	// the servers.
	for _, r := range idle {
		if !slices.Contains(names, r.Server) {
			names = append(names, r.Server)
		}
	}
	fmt.Printf("\n%-18s", "req/s")
	for _, n := range names {
		fmt.Printf("%14s", n)
	}
	fmt.Println()
	for _, key := range order {
		fmt.Printf("%-18s", key)
		for _, n := range names {
			fmt.Printf("%14s", rows[key][n])
		}
		fmt.Println()
	}
	if len(idle) > 0 {
		fmt.Printf("\n%-18s", "bytes/idle conn")
		for _, n := range names {
			fmt.Printf("%14s", n)
		}
		fmt.Println()
		for _, k := range idleKindNames() {
			cells := map[string]string{}
			for _, r := range idle {
				if r.Kind == k {
					cells[r.Server] = "-"
					switch {
					case r.Error != "":
						cells[r.Server] = "error"
					case r.Skipped == "":
						cells[r.Server] = strconv.FormatFloat(r.BytesPerConn, 'f', 0, 64)
					}
				}
			}
			if len(cells) == 0 {
				continue
			}
			fmt.Printf("%-18s", k)
			for _, n := range names {
				fmt.Printf("%14s", cells[n])
			}
			fmt.Println()
		}
	}
	fmt.Println()
}

// Command fitbench runs nginx, Caddy, Traefik, Envoy and fit through the same
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
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	// StartupGoroutines are the goroutines each Go server with an Admin API
	// had a second after it became ready, before any load.
	StartupGoroutines map[string]int `json:"startup_goroutines,omitempty"`
	// StartupAnon is each server's anonymous memory at the same moment.
	StartupAnon map[string]int64 `json:"startup_anon_bytes,omitempty"`
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
	// Label names the run's workload, as bench/plan.sh gives it. Rate is
	// the requests a second the load generators kept to, or 0 for as many
	// as they could; HoldIdle the idle TLS connections held open during
	// each run.
	Label       string `json:"label,omitempty"`
	Rate        int    `json:"rate,omitempty"`
	HoldIdle    int    `json:"hold_idle,omitempty"`
	Network     string `json:"network"`
	ServerCPUs  string `json:"server_cpus"`
	ClientCPUs  string `json:"client_cpus"`
	BackendCPUs string `json:"backend_cpus"`
}

// Container names; one of each runs at a time.
const (
	serverName    = "fitbench-server"
	backendName   = "fitbench-backend"
	cacheName     = "fitbench-cachebackend"
	wsBackendName = "fitbench-wsbackend"
	loadgenName   = "fitbench-loadgen"
	networkName   = "fitbench"
	loadgenTag    = "fitbench-loadgen"
	// The HTTP/3 load generators, from bench/loadgen/Dockerfile.h3.
	loadgenH3Name = "fitbench-loadgen-h3"
	loadgenH3Tag  = "fitbench-loadgen-h3"
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
	// rate, when above 0, is the requests a second each run keeps to, and
	// holdIdleConns the idle TLS connections held open during each run.
	rate          int
	holdIdleConns int
	baseImage     string
	images        map[string]string

	bench       string // the bench directory
	work        string // bench/.work: the fit binary, the rendered configs
	data        string // bench/.work/data: what the load generators read
	netArgs     []string
	serverHost  string
	serverCPUs  []string // docker run arguments pinning each container
	clientCPUs  []string
	backendCPUs []string
	threads     int // load generator threads: one per client CPU
	// cacheAdmin is the cache backend's Admin API, or "" when the load
	// generators cannot reach it.
	cacheAdmin string
	// serverCgroup is the running server's cgroup directory on this host,
	// or "" when it cannot be read from here.
	serverCgroup string
	report       Report
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("fitbench", flag.ContinueOnError)
	h := &harness{images: map[string]string{}}
	serverList := flags.String("servers", "nginx,caddy,traefik,envoy,fit", "the servers to run")
	scenarioList := flags.String("scenarios", "fixed,static,proxy", "the scenarios to run")
	protocolList := flags.String("protocols", "http/1.1,h2c,h2,h3", "the protocols to run")
	flags.DurationVar(&h.duration, "duration", 30*time.Second, "how long each measured run lasts")
	flags.DurationVar(&h.warmup, "warmup", 5*time.Second, "how long the load runs, unmeasured, before each measured run")
	flags.IntVar(&h.conns, "connections", 256, "HTTP/1.1 connections (wrk)")
	flags.IntVar(&h.h2conns, "h2-connections", 64, "h2c connections (h2load)")
	flags.IntVar(&h.h2streams, "h2-streams", 32, "concurrent streams per h2c connection (h2load)")
	idleList := flags.String("idle", "http1,tls,ws", "the kinds of idle connection to measure memory for; empty for none")
	flags.IntVar(&h.idleConns, "idle-connections", 10000, "how many idle connections to open")
	flags.DurationVar(&h.idleSettle, "idle-settle", 5*time.Second, "how long the idle connections sit before the server's memory is read")
	flags.IntVar(&h.rate, "rate", 0, "requests a second each run keeps to, with h2load over every protocol; 0 for as many as the load generators can")
	flags.IntVar(&h.holdIdleConns, "hold-idle", 0, "idle TLS connections to hold open during each run")
	label := flags.String("label", "", "a name for the run's workload, kept in its results")
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
	h.data = filepath.Join(h.work, "data")
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

	backendAddr, wsBackendAddr, cacheAddr := "127.0.0.1:9000", "127.0.0.1:9001", "127.0.0.1:9003"
	h.cacheAdmin = "127.0.0.1:9004"
	switch *network {
	case "host":
		h.netArgs, h.serverHost = []string{"--network", "host"}, "127.0.0.1"
	case "bridge":
		h.netArgs, h.serverHost = []string{"--network", networkName}, serverName
		backendAddr, wsBackendAddr, cacheAddr = backendName+":9000", wsBackendName+":9001", cacheName+":9003"
		// Its Admin API listens on its own loopback, which the load
		// generators cannot reach: no hit ratios.
		h.cacheAdmin = ""
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
		Started:           time.Now().UTC(),
		Host:              map[string]string{"docker_cpus": strconv.Itoa(ncpu), "docker_arch": arch},
		Versions:          map[string]string{},
		Images:            map[string]string{},
		StartupGoroutines: map[string]int{},
		StartupAnon:       map[string]int64{},
		Params: Params{
			Duration: h.duration.String(), Warmup: h.warmup.String(),
			Connections: max(h.conns, h.threads), H2Connections: max(h.h2conns, h.threads), H2Streams: h.h2streams,
			IdleConns: h.idleConns, IdleSettle: h.idleSettle.String(),
			Label: *label, Rate: h.rate, HoldIdle: h.holdIdleConns,
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
	vars := map[string]string{"{{BACKEND}}": backendAddr, "{{WSBACKEND}}": wsBackendAddr,
		"{{CACHEBACKEND}}": cacheAddr, "{{WORKERS}}": strconv.Itoa(serverThreads),
		"{{BACKEND_WORKERS}}": strconv.Itoa(max(1, cpusetCount(cpus[2]))),
		// The proxy backend's API bodies, which nginx's config quotes.
		"{{APIBODY}}": apiBody, "{{APIPOSTREPLY}}": apiPostReply}
	// Envoy takes a backend's host and port apart.
	for name, addr := range map[string]string{"BACKEND": backendAddr, "WSBACKEND": wsBackendAddr} {
		host, port, _ := net.SplitHostPort(addr)
		vars["{{"+name+"_HOST}}"], vars["{{"+name+"_PORT}}"] = host, port
	}
	if err := h.renderConfigs(vars); err != nil {
		return err
	}
	if err := writeCert(filepath.Join(h.work, "certs")); err != nil {
		return err
	}
	if err := writeData(h.data); err != nil {
		return err
	}
	// The website's files only for the scenarios that ask for them; the
	// large download's always.
	site := slices.Contains(h.scenarios, "site") || slices.Contains(h.scenarios, "edge")
	if site {
		step("writing the website's files")
	}
	siteDir, backendDir := filepath.Join(h.work, "www-site"), filepath.Join(h.work, "www-backend")
	_ = os.RemoveAll(siteDir)
	if err := writeSite(siteDir, h.data, site); err != nil {
		return err
	}
	if err := writeBackendFiles(backendDir); err != nil {
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
		"-v", siteDir+":/site:ro", "-v", backendDir+":/backend:ro", h.baseImage, "sh", "-c",
		"cp -r /src/. /srv/www/ && mkdir -p /srv/www/site /srv/www/backend && cp -r /site/. /srv/www/site/ && cp -r /backend/. /srv/www/backend/"); err != nil {
		return err
	}
	step("starting the load generators")
	if _, err := docker(ctx, append(append(append([]string{"run", "-d", "--name", loadgenName, "--ulimit", "nofile=1048576:1048576",
		"-v", filepath.Join(h.work, "idleconn") + ":/fitbench/idleconn:ro", "-v", h.data + ":/fitbench/data:ro",
		"-e", "OPENSSL_CONF=/fitbench/data/openssl.cnf"},
		h.netArgs...), h.clientCPUs...), loadgenTag, "sleep", "infinity")...); err != nil {
		return err
	}
	if slices.Contains(h.protocols, "h3") {
		// h2load is built from source the first time, a few minutes.
		step("building and starting the HTTP/3 load generators")
		if _, err := docker(ctx, "build", "-q", "-t", loadgenH3Tag, "-f", filepath.Join(h.bench, "loadgen", "Dockerfile.h3"),
			filepath.Join(h.bench, "loadgen")); err != nil {
			return err
		}
		if _, err := docker(ctx, append(append(append([]string{"run", "-d", "--name", loadgenH3Name, "--ulimit", "nofile=1048576:1048576",
			"-v", h.data + ":/fitbench/data:ro", "-e", "OPENSSL_CONF=/fitbench/data/openssl.cnf"},
			h.netArgs...), h.clientCPUs...), loadgenH3Tag, "sleep", "infinity")...); err != nil {
			return err
		}
		out, _ := docker(ctx, "exec", loadgenH3Name, "h2load", "--version")
		h.report.Versions["h2load-h3"] = firstLine(out, "h2load")
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

	// It runs whatever is chosen too, for nginx's config to resolve.
	step("starting the cache backend")
	if err := h.start(ctx, cacheBackend, cacheName, h.backendCPUs); err != nil {
		return err
	}
	if err := h.waitReady(ctx, cacheName, "http://"+cacheAddr+cacheBackend.ready); err != nil {
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
	printGoroutines(h.report)
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
	if image == "" {
		// A backend, which has no -<name>-image flag.
		image = s.image
	}
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
	runs := func(sc string) []string {
		var ps []string
		for _, p := range h.protocols {
			if only := scenarioOf(sc).protocols; only == nil || slices.Contains(only, p) {
				ps = append(ps, p)
			}
		}
		return ps
	}
	if len(todo) == 0 {
		return
	}

	step("starting " + s.name)
	defer func() {
		_, _ = docker(context.Background(), "rm", "-f", serverName)
		h.serverCgroup = ""
	}()
	startAnon, err := h.startFresh(ctx, s)
	if err != nil {
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
	h.report.StartupAnon[s.name] = startAnon
	if h.canCount(s) {
		if n, err := h.goroutines(ctx, s); err == nil {
			h.report.StartupGoroutines[s.name] = n
		}
	}

	// Each run gets a server started for it, so that the memory and the
	// goroutines one run leaves behind are not counted in the next.
	fresh := true
	for _, sc := range todo {
		for _, p := range h.protocols {
			if ctx.Err() != nil {
				return
			}
			if !slices.Contains(runs(sc), p) {
				add(Result{Server: s.name, Scenario: sc, Protocol: p, Skipped: "the scenario runs over " + strings.Join(scenarioOf(sc).protocols, ", ")})
				continue
			}
			if !fresh {
				if startAnon, err = h.startFresh(ctx, s); err != nil {
					add(Result{Server: s.name, Scenario: sc, Protocol: p, Error: err.Error()})
					continue
				}
			}
			fresh = false
			add(h.measure(ctx, s, sc, p, startAnon))
		}
	}
}

// startFresh starts s as the server, in place of any server running, waits
// until it is ready and has settled, and returns its anonymous memory then.
func (h *harness) startFresh(ctx context.Context, s server) (int64, error) {
	_, _ = docker(ctx, "rm", "-f", serverName)
	h.serverCgroup = ""
	if err := h.start(ctx, s, serverName, h.serverCPUs); err != nil {
		return 0, err
	}
	if err := h.waitReady(ctx, serverName, "http://"+h.serverHost+":8080"+s.ready); err != nil {
		return 0, err
	}
	h.serverCgroup = findCgroup(ctx)
	time.Sleep(time.Second) // let the server settle after starting
	anon, _ := h.serverAnon(ctx)
	return anon, nil
}

// scenarioOf returns the scenario named name.
func scenarioOf(name string) scenario {
	return scenarios[slices.IndexFunc(scenarios, func(x scenario) bool { return x.name == name })]
}

// backendCount returns how many requests the cache origin has answered,
// from its stub_status, which counts its own requests among them. route is
// the origin's location the scenario asks for; the origin counts them all
// together, and only one scenario runs at a time.
func (h *harness) backendCount(ctx context.Context, route string) (int64, error) {
	if h.cacheAdmin == "" {
		return 0, errors.New("the cache backend's Admin API is not reachable on a bridge network")
	}
	out, err := docker(ctx, "exec", loadgenName, "curl", "-s", "--max-time", "5", "http://"+h.cacheAdmin+"/status")
	if err != nil {
		return 0, err
	}
	return parseStubStatus(out)
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
func (h *harness) measure(ctx context.Context, s server, sc, p string, startAnon int64) (r Result) {
	var err error
	r = Result{Server: s.name, Scenario: sc, Protocol: p, Rate: h.rate}
	j := slices.IndexFunc(protocols, func(x protocol) bool { return x.name == p })
	scenario, proto := scenarioOf(sc), protocols[j]
	r.Tool = proto.tool
	if h.rate > 0 {
		r.Tool = "h2load"
	}
	scheme := "http://"
	if p == "https/1.1" || p == "h2" || p == "h3" {
		scheme = "https://"
	}
	base := scheme + h.serverHost + ":" + proto.port
	path, body, err := h.expected(scenario)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	if err := h.check(ctx, base+path, p, body); err != nil {
		r.Error = err.Error()
		return r
	}
	if scheme == "https://" {
		r.TLS = h.tlsSession(ctx, base+path, p)
	}
	if scenario.kind == kindSite {
		if err := writeURIs(filepath.Join(h.data, "uris-"+sc+"-"+proto.port+".txt"), base, scenario.arg); err != nil {
			r.Error = err.Error()
			return r
		}
	}
	url := base + scenario.path
	if h.holdIdleConns > 0 {
		release, held, err := h.holdIdle(ctx)
		if err != nil {
			r.Error = "holding idle connections: " + err.Error()
			return r
		}
		r.IdleHeld = held
		defer func() { r.IdleClosed = release() }()
	}
	samples := h.sample(ctx, s)
	defer samples.stop()
	if h.warmup > 0 {
		if _, err := runLoad(ctx, h.loadRuns(scenario, proto, url, h.warmup, false)); err != nil {
			r.Error = "warmup: " + err.Error()
			return r
		}
	}
	var backend0 int64
	if scenario.backendRoute != "" {
		if backend0, err = h.backendCount(ctx, scenario.backendRoute); err != nil {
			r.Error = "the backend's count: " + err.Error()
			return r
		}
	}
	runs := h.loadRuns(scenario, proto, url, h.duration, true)
	cpu0, cpuOK := h.serverCPU(ctx)
	start := time.Now()
	outs, err := runLoad(ctx, runs)
	elapsed := time.Since(start)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	var backend1 int64
	if scenario.backendRoute != "" {
		if backend1, err = h.backendCount(ctx, scenario.backendRoute); err != nil {
			r.Error = "the backend's count: " + err.Error()
			return r
		}
	}
	if cpu1, ok := h.serverCPU(ctx); ok && cpuOK {
		r.CPUCores = float64(cpu1-cpu0) / float64(elapsed)
	}
	r.GoroutinesMax, r.AnonPeakBytes = samples.stop()
	if out, err := h.serverFile(ctx, "memory.current"); err == nil {
		r.MemoryBytes, _ = strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	}
	r.AnonBytes, _ = h.serverAnon(ctx)
	r.AnonPeakBytes = max(r.AnonPeakBytes, r.AnonBytes)
	for _, run := range runs {
		r.InFlight += run.conns * run.streams
	}
	if err := parseRuns(runs, outs, &r); err != nil {
		r.Error = err.Error()
	} else if h.rate > 0 {
		if err := h.ratePercentiles(ctx, runs, r.Latency); err != nil {
			r.Error = err.Error()
		}
	}
	if scenario.backendRoute != "" && r.Requests > 0 {
		// Less the second count's own request.
		r.BackendRequests = max(0, backend1-backend0-1)
		ratio := 1 - float64(r.BackendRequests)/float64(r.Requests)
		r.HitRatio = &ratio
	}
	r.AnonStartBytes = startAnon
	r.efficiency()
	return r
}

// expected returns the path a scenario's response is checked at, and the
// body it must have: the scenario's own, or the file it serves.
func (h *harness) expected(sc scenario) (path, body string, err error) {
	path, body = sc.path, sc.body
	file := ""
	switch {
	case sc.kind == kindSite:
		names, _ := siteFiles()
		path, file = sc.arg+names[0], filepath.Join(h.work, "www-site", names[0])
	case strings.HasPrefix(sc.path, "/site/"):
		file = filepath.Join(h.work, "www-site", strings.TrimPrefix(sc.path, "/site/"))
	case body == "":
		file = filepath.Join(h.bench, "www", "static", "small.html")
	}
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", "", err
		}
		body = string(data)
	}
	return path, body, nil
}

// tlsSession returns what curl and the server agree on over protocol p at
// url, as curl prints it: "TLSv1.3 / TLS_AES_128_GCM_SHA256 / X25519 /
// id-ecPublicKey"; or "" when it says nothing. The HTTP/3 curl is built on
// OpenSSL 3.5, the other on Ubuntu's OpenSSL 3.0, as the load generators
// beside them are.
func (h *harness) tlsSession(ctx context.Context, url, p string) string {
	container, flag := loadgenName, "--http1.1"
	switch p {
	case "h2":
		flag = "--http2"
	case "h3":
		container, flag = loadgenH3Name, "--http3-only"
	}
	out, _ := docker(ctx, "exec", container, "curl", "-s", "-v", "-k", "--max-time", "5", flag, "-o", "/dev/null", url)
	for line := range strings.SplitSeq(out, "\n") {
		if _, after, ok := strings.Cut(line, "SSL connection using "); ok {
			return strings.TrimSpace(after)
		}
	}
	return ""
}

// check fetches url once over protocol p and compares the response with
// body.
func (h *harness) check(ctx context.Context, url, p, body string) error {
	if body == "" {
		data, err := os.ReadFile(filepath.Join(h.bench, "www", "static", "small.html"))
		if err != nil {
			return err
		}
		body = string(data)
	}
	container, flag, want := loadgenName, "--http1.1", "200 1.1"
	switch p {
	case "h2c":
		flag, want = "--http2-prior-knowledge", "200 2"
	case "h2":
		// The certificate is the harness's own, self-signed, as for h3.
		flag, want = "--http2", "200 2"
	case "h3":
		container, flag, want = loadgenH3Name, "--http3-only", "200 3"
	}
	out, err := docker(ctx, "exec", container, "curl", "-s", "-k", "--max-time", "5", flag, "-w", "\n%{http_code} %{http_version}", url)
	if err != nil {
		return fmt.Errorf("check %s: %v", url, err)
	}
	i := strings.LastIndexByte(out, '\n')
	if i < 0 {
		return fmt.Errorf("check %s: curl printed %q", url, out)
	}
	got, status := out[:i], strings.TrimSpace(out[i+1:])
	if status != want || got != body {
		return fmt.Errorf("check %s: got %q with body %.80q, want %q with body %.80q", url, status, got, want, body)
	}
	return nil
}

// canCount says whether s's goroutines can be counted: it has an Admin API,
// which listens on the server's loopback, reached from here only through
// host networking.
func (h *harness) canCount(s server) bool {
	return s.admin != "" && h.serverHost == "127.0.0.1"
}

var adminClient = &http.Client{Timeout: 2 * time.Second}

// goroutines reads how many goroutines s has from its Admin API.
func (h *harness) goroutines(ctx context.Context, s server) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+s.admin+"/metrics", nil)
	if err != nil {
		return 0, err
	}
	resp, err := adminClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	return parseGoroutines(string(data))
}

// A sampler reads, every 100ms, the server's anonymous memory, when its
// cgroup can be read from here, and its goroutines, when it can count them,
// keeping the most of each. A spike shorter than that can fall between two
// samples.
type sampler struct {
	done chan struct{}
	wg   sync.WaitGroup
	once sync.Once

	goroutines int
	anon       int64
}

func (h *harness) sample(ctx context.Context, s server) *sampler {
	sm := &sampler{done: make(chan struct{})}
	count, mem := h.canCount(s), h.serverCgroup != ""
	read := func() {
		if count {
			if n, err := h.goroutines(ctx, s); err == nil {
				sm.goroutines = max(sm.goroutines, n)
			}
		}
		if mem {
			if n, ok := h.serverAnon(ctx); ok {
				sm.anon = max(sm.anon, n)
			}
		}
	}
	sm.wg.Add(1)
	go func() {
		defer sm.wg.Done()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			read()
			select {
			case <-sm.done:
				read()
				return
			case <-tick.C:
			}
		}
	}()
	return sm
}

// stop stops the sampling, and returns the most goroutines and anonymous
// memory seen; 0 for what was not sampled. Calls after the first return the
// same.
func (sm *sampler) stop() (goroutines int, anon int64) {
	sm.once.Do(func() {
		close(sm.done)
		sm.wg.Wait()
	})
	return sm.goroutines, sm.anon
}

// findCgroup returns the server container's cgroup directory on this host,
// when fitbench runs where Docker does with systemd's or cgroupfs's layout of
// cgroup v2; "" otherwise, and its files are read through docker exec.
func findCgroup(ctx context.Context) string {
	out, err := docker(ctx, "inspect", "--format", "{{.Id}}", serverName)
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(out)
	for _, dir := range []string{"/sys/fs/cgroup/system.slice/docker-" + id + ".scope", "/sys/fs/cgroup/docker/" + id} {
		if _, err := os.Stat(filepath.Join(dir, "memory.stat")); err == nil {
			return dir
		}
	}
	return ""
}

// serverFile reads one of the server's cgroup files: from this host when
// its cgroup was found, which costs the server nothing, or through docker
// exec.
func (h *harness) serverFile(ctx context.Context, name string) (string, error) {
	if h.serverCgroup != "" {
		data, err := os.ReadFile(filepath.Join(h.serverCgroup, name))
		return string(data), err
	}
	return docker(ctx, "exec", serverName, "cat", "/sys/fs/cgroup/"+name)
}

// serverAnon is the server's anonymous memory now.
func (h *harness) serverAnon(ctx context.Context) (int64, bool) {
	out, err := h.serverFile(ctx, "memory.stat")
	if err != nil {
		return 0, false
	}
	return cgroupAnon(out)
}

// serverCPU is the CPU time the server's container has used so far.
func (h *harness) serverCPU(ctx context.Context) (time.Duration, bool) {
	out, err := h.serverFile(ctx, "cpu.stat")
	if err != nil {
		return 0, false
	}
	return cgroupCPU(out)
}

// cleanup removes every container and the network a run makes, as left by
// this run or an earlier one that was interrupted.
func cleanup(network bool) {
	ctx := context.Background()
	_, _ = docker(ctx, "rm", "-f", serverName, backendName, wsBackendName, cacheName, loadgenName, loadgenH3Name)
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
	name := fmt.Sprintf("%-8s %-10s %-9s", r.Server, r.Scenario, r.Protocol)
	switch {
	case r.Skipped != "":
		fmt.Printf("    %s skipped: %s\n", name, r.Skipped)
	case r.Error != "":
		fmt.Printf("    %s error: %s\n", name, r.Error)
	default:
		hits := ""
		if r.HitRatio != nil {
			hits = fmt.Sprintf("  hits %.1f%%", 100**r.HitRatio)
		}
		if r.GoroutinesMax > 0 {
			hits += fmt.Sprintf("  goroutines %d", r.GoroutinesMax)
		}
		fmt.Printf("    %s %12.0f req/s  mean %7.2fms  max %8.2fms  errors %d  cpu %.2f (%.2fus/req)  peak %.0fMiB%s\n",
			name, r.RequestsPerSec, r.Latency.Mean, r.Latency.Max, r.Errors, r.CPUCores, r.CPUPerRequestUS,
			float64(r.AnonPeakBytes)/(1<<20), hits)
	}
}

// printSummary prints requests per second as a table: a row per scenario
// and protocol, a column per server.
func printSummary(results []Result, idle []IdleResult) {
	var names []string
	for _, r := range results {
		if !slices.Contains(names, r.Server) {
			names = append(names, r.Server)
		}
	}
	// A run of idle connections alone has no throughput results to name
	// the servers.
	for _, r := range idle {
		if !slices.Contains(names, r.Server) {
			names = append(names, r.Server)
		}
	}
	printTable("req/s", names, results, func(r Result) float64 { return r.RequestsPerSec }, 0)
	if len(results) > 0 {
		printTable("cpu us/req", names, results, func(r Result) float64 { return r.CPUPerRequestUS }, 2)
		printTable("peak anon MiB", names, results, func(r Result) float64 { return float64(r.AnonPeakBytes) / (1 << 20) }, 0)
		printTable("req/s per MiB", names, results, func(r Result) float64 { return r.RequestsPerSecPerMiB }, 0)
		printTable("bytes/in-flight", names, results, func(r Result) float64 { return r.BytesPerInFlight }, 0)
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

// printTable prints value of the results as a table: a row per scenario
// and protocol, a column per server, with prec decimals.
func printTable(title string, names []string, results []Result, value func(Result) float64, prec int) {
	rows := map[string]map[string]string{}
	var order []string
	for _, r := range results {
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
			cell = strconv.FormatFloat(value(r), 'f', prec, 64)
		}
		rows[key][r.Server] = cell
	}
	fmt.Printf("\n%-22s", title)
	for _, n := range names {
		fmt.Printf("%14s", n)
	}
	fmt.Println()
	for _, key := range order {
		fmt.Printf("%-22s", key)
		for _, n := range names {
			fmt.Printf("%14s", rows[key][n])
		}
		fmt.Println()
	}
}

// printGoroutines prints, for each server whose goroutines were counted,
// how many it had once started and the most during each kind of load.
func printGoroutines(rep Report) {
	var names []string
	for n := range rep.StartupGoroutines {
		names = append(names, n)
	}
	slices.Sort(names)
	for _, n := range names {
		fmt.Printf("goroutines %-8s started %d", n, rep.StartupGoroutines[n])
		most := 0
		for _, r := range rep.Results {
			if r.Server == n && r.GoroutinesMax > 0 {
				most = max(most, r.GoroutinesMax)
			}
		}
		fmt.Printf("  under load at most %d", most)
		for _, r := range rep.Idle {
			if r.Server == n && r.GoroutinesAfter > 0 {
				fmt.Printf("  idle-%s %d -> %d", r.Kind, r.GoroutinesBefore, r.GoroutinesAfter)
			}
		}
		fmt.Println()
	}
}

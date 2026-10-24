package main

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How a scenario's load generators ask (scenario.kind).
const (
	// kindAPI is the API workload: four of five requests a GET of /api/item,
	// answered with apiBody, and the fifth a POST of apiPostBody to it,
	// answered with apiPostReply, all with apiHeaders. wrk mixes them in one
	// script; h2load, which sends one kind of request, runs as two groups,
	// four fifths of the connections asking with GET and the rest with POST.
	kindAPI = "api"
	// kindSite is the website workload: siteCount files under the prefix
	// scenario.arg, the n-th most popular asked for as often as 1/n, with
	// browserHeaders.
	kindSite = "site"
	// kindChurn closes the connection after every scenario.arg requests,
	// so that a client opens a new one, with its own TLS handshake.
	kindChurn = "churn"
	// kindUpload POSTs a body of scenario.arg bytes.
	kindUpload = "upload"
)

// The payloads: an API's JSON, 1 KiB answered to a GET, and a POST of 2 KiB
// answered with 256 bytes; and the 1 MiB of the large download and upload.
var (
	apiBody      = jsonOf(1024)
	apiPostBody  = jsonOf(2048)
	apiPostReply = jsonOf(256)
	largeBody    = strings.Repeat("0123456789abcdef", 1<<16)
)

// largeFile is the 1 MiB file the download scenario serves, beside the
// website's files; largeSize is the upload's body.
const (
	largeFile = "large.bin"
	largeSize = "1048576"
)

// jsonOf returns a JSON object of n bytes, with nothing in it a config or a
// Lua string would have to escape.
func jsonOf(n int) string {
	const head, tail = `{"id":12345,"data":"`, `"}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

// The request headers of an API client and of a browser, about 700 bytes
// each, as real ones send: a bearer token and cookies are most of them.
var (
	apiHeaders = [][2]string{
		{"User-Agent", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36"},
		{"Accept", "application/json"},
		{"Accept-Language", "en-US,en;q=0.9"},
		{"Accept-Encoding", "gzip, deflate, br"},
		{"Authorization", "Bearer " + token(320)},
		{"Cookie", "session=" + token(48) + "; prefs=lang-en.theme-dark; _ga=GA1.2.1418925463.1729700000"},
		{"X-Request-Id", "5f2b8c1e-9d4a-4c3b-8e7f-1a2b3c4d5e6f"},
	}
	browserHeaders = [][2]string{
		{"User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"},
		{"Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
		{"Accept-Language", "en-US,en;q=0.9,zh-CN;q=0.8"},
		{"Accept-Encoding", "gzip, deflate, br, zstd"},
		{"Referer", "https://www.example.com/products/catalog?page=2"},
		{"Cookie", "session=" + token(48) + "; cart=" + token(32) + "; _ga=GA1.2.1418925463.1729700000; consent=all"},
		{"Sec-Fetch-Site", "same-origin"},
		{"Sec-Fetch-Mode", "navigate"},
		{"Sec-Fetch-Dest", "document"},
	}
)

// token returns n characters of a token's alphabet, the same each time.
func token(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	r := rand.New(rand.NewPCG(uint64(n), 7))
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	return string(b)
}

// The website: siteCount files, a tenth of them pages of about 30 KiB, a
// fifth stylesheets and scripts of 10 to 100 KiB, and the rest images of 5
// to 300 KiB, spread evenly over the logarithm of their size, which puts the
// average near 60 KiB and the whole near 600 MB: more than the L3 cache,
// well within memory. Their popularity rank is shuffled from their size.
const siteCount = 10000

// siteFiles returns the website's file names, the most popular first, and
// each one's size, the same every time.
func siteFiles() (names []string, sizes []int) {
	r := rand.New(rand.NewPCG(1, 2))
	logUniform := func(lo, hi float64) int { return int(math.Exp(math.Log(lo) + r.Float64()*(math.Log(hi)-math.Log(lo)))) }
	for i := range siteCount {
		var name string
		var size int
		switch i % 10 {
		case 0:
			name, size = fmt.Sprintf("p%05d.html", i), 24<<10+r.IntN(12<<10)
		case 1:
			name, size = fmt.Sprintf("s%05d.css", i), logUniform(10<<10, 100<<10)
		case 2:
			name, size = fmt.Sprintf("j%05d.js", i), logUniform(10<<10, 100<<10)
		default:
			name, size = fmt.Sprintf("i%05d.jpg", i), logUniform(5<<10, 300<<10)
		}
		names, sizes = append(names, name), append(sizes, size)
	}
	r.Shuffle(len(names), func(i, j int) {
		names[i], names[j] = names[j], names[i]
		sizes[i], sizes[j] = sizes[j], sizes[i]
	})
	return names, sizes
}

// writeSite writes the large download's file into dir, and, with all, the
// website's files and their list for the load generators into data.
func writeSite(dir, data string, all bool) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, largeFile), []byte(largeBody), 0o644); err != nil {
		return err
	}
	if !all {
		return nil
	}
	names, sizes := siteFiles()
	for i, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), siteContent(name, sizes[i]), 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(data, "site-files.txt"), []byte(strings.Join(names, "\n")+"\n"), 0o644)
}

// writeBackendFiles writes the files the cache origin serves into dir,
// which goes to /srv/www/backend: the 4 KiB of the cache scenarios.
func writeBackendFiles(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "cache.txt"), []byte(cacheBody), 0o644)
}

// siteContent is a website file's bytes: its name, then a pattern of its
// own.
func siteContent(name string, size int) []byte {
	b := make([]byte, size)
	pattern := name + " 0123456789abcdefghijklmnopqrstuvwxyz\n"
	for i := 0; i < size; i += len(pattern) {
		copy(b[i:], pattern)
	}
	return b
}

// writeData writes what the load generators read, into dir, which they
// mount at /fitbench/data: the headers for wrk's scripts, and the POST
// bodies for h2load.
func writeData(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var lua strings.Builder
	lua.WriteString("-- Written by fitbench: the request headers of the workloads.\n")
	for _, set := range []struct {
		name    string
		headers [][2]string
	}{{"api_headers", apiHeaders}, {"browser_headers", browserHeaders}} {
		fmt.Fprintf(&lua, "%s = {\n", set.name)
		for _, h := range set.headers {
			fmt.Fprintf(&lua, "  [%q] = %q,\n", h[0], h[1])
		}
		lua.WriteString("}\n")
	}
	fmt.Fprintf(&lua, "api_post_body = %q\n", apiPostBody)
	files := map[string]string{
		"headers.lua":  lua.String(),
		"api-post.bin": apiPostBody,
		"upload.bin":   largeBody,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeURIs writes the list of URIs h2load asks for in the website
// workload, at base, the scheme and authority, under prefix: 100,000 drawn
// by popularity, the n-th most popular as often as 1/n, in an order of
// their own. h2load goes through the list in turn.
func writeURIs(path, base, prefix string) error {
	names, _ := siteFiles()
	cdf := make([]float64, len(names))
	sum := 0.0
	for i := range names {
		sum += 1 / float64(i+1)
		cdf[i] = sum
	}
	r := rand.New(rand.NewPCG(3, 4))
	var b strings.Builder
	for range 100000 {
		x := r.Float64() * sum
		lo, hi := 0, len(cdf)-1
		for lo < hi {
			mid := (lo + hi) / 2
			if cdf[mid] < x {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		b.WriteString(base + prefix + names[lo] + "\n")
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// loadRun is one load generator process of a run.
type loadRun struct {
	args []string
	tool string // wrk or h2load, how its output is read
	// conns and streams are the connections it keeps and the requests each
	// has in flight; log is h2load's per-request log, for a run at a fixed
	// rate.
	conns, streams int
	log            string
}

// loadRuns returns the load generator processes that run scenario sc over
// protocol p at url for d, all at once. With measured, a run at a fixed
// rate logs each request, for its latency percentiles.
func (h *harness) loadRuns(sc scenario, p protocol, url string, d time.Duration, measured bool) []loadRun {
	secs := strconv.Itoa(max(1, int(d.Round(time.Second).Seconds())))
	tool := p.tool
	if h.rate > 0 && tool == "wrk" {
		// wrk asks as fast as it can; h2load keeps to a rate, and speaks
		// HTTP/1.1 too.
		tool = "h2load-h1"
	}
	if tool == "wrk" {
		script, args := sc.script, []string(nil)
		switch sc.kind {
		case kindAPI:
			script = "api.lua"
		case kindSite:
			script, args = "site.lua", []string{sc.arg}
		case kindChurn:
			script, args = "churn.lua", []string{sc.arg}
		case kindUpload:
			script, args = "upload.lua", []string{sc.arg}
		}
		if script == "" {
			script = "report.lua"
		}
		c := max(h.conns, h.threads)
		cmd := []string{"exec", loadgenName, "wrk", "-t", strconv.Itoa(h.threads), "-c", strconv.Itoa(c), "-d", secs + "s",
			"--timeout", "10s", "--latency", "-s", "/fitbench/" + script, url}
		if len(args) > 0 {
			cmd = append(append(cmd, "--"), args...)
		}
		return []loadRun{{args: cmd, tool: "wrk", conns: c, streams: 1}}
	}

	container, flags := loadgenName, []string(nil)
	conns, streams := max(h.h2conns, h.threads), h.h2streams
	switch tool {
	case "h2load-h3":
		container, flags = loadgenH3Name, []string{"--alpn-list=h3"}
	case "h2load-h1":
		flags, conns, streams = []string{"--h1"}, max(h.conns, h.threads), 1
	}
	var headers []string
	switch sc.kind {
	case kindAPI:
		headers = headerFlags(apiHeaders)
	case kindSite:
		headers = headerFlags(browserHeaders)
	}
	type group struct {
		share float64
		extra []string
	}
	groups := []group{{share: 1}}
	switch sc.kind {
	case kindAPI:
		groups = []group{{share: 0.8}, {share: 0.2, extra: []string{"-d", "/fitbench/data/api-post.bin"}}}
	case kindUpload:
		groups[0].extra = []string{"-d", "/fitbench/data/upload.bin"}
	}
	target := []string{url}
	if sc.kind == kindSite {
		target = []string{"-i", "/fitbench/data/uris-" + sc.name + "-" + p.port + ".txt"}
	}
	// The connections and threads are split by each group's share, every
	// group having at least one of each, and no group more threads than
	// connections.
	var runs []loadRun
	connsLeft, threadsLeft := conns, h.threads
	for i, g := range groups {
		c, t := connsLeft, threadsLeft
		if i < len(groups)-1 {
			c = max(1, int(math.Round(float64(conns)*g.share)))
			t = max(1, int(math.Round(float64(h.threads)*g.share)))
		}
		connsLeft, threadsLeft = connsLeft-c, threadsLeft-t
		t = min(t, c)
		cmd := []string{"exec", container, "h2load", "-t", strconv.Itoa(t), "-c", strconv.Itoa(c), "-m", strconv.Itoa(streams), "-D", secs}
		cmd = append(cmd, flags...)
		cmd = append(cmd, headers...)
		cmd = append(cmd, g.extra...)
		run := loadRun{tool: "h2load", conns: c, streams: streams}
		if h.rate > 0 {
			// h2load's rate is each connection's.
			per := max(1, int(math.Round(float64(h.rate)*g.share/float64(c))))
			cmd = append(cmd, "--rps", strconv.Itoa(per))
			if measured {
				run.log = fmt.Sprintf("/tmp/fitbench-%d.log", i)
				cmd = append(cmd, "--log-file", run.log)
			}
		}
		run.args = append(cmd, target...)
		runs = append(runs, run)
	}
	return runs
}

// headerFlags returns headers as h2load's -H flags.
func headerFlags(headers [][2]string) []string {
	var flags []string
	for _, h := range headers {
		flags = append(flags, "-H", h[0]+": "+h[1])
	}
	return flags
}

// runLoad runs every process in runs at once, and returns their outputs in
// order, and the first error.
func runLoad(ctx context.Context, runs []loadRun) ([]string, error) {
	outs := make([]string, len(runs))
	errs := make([]error, len(runs))
	var wg sync.WaitGroup
	for i, run := range runs {
		wg.Go(func() { outs[i], errs[i] = docker(ctx, run.args...) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return outs, err
		}
	}
	return outs, nil
}

// parseRuns reads the outputs of runs into r: the requests and errors of
// them all, their requests a second added up, and their latencies, a mean
// weighted by each one's requests and the largest of their maxima.
func parseRuns(runs []loadRun, outs []string, r *Result) error {
	var mean, stdev float64
	r.Latency = &Latency{}
	kinds := map[string]int64{}
	for i, run := range runs {
		var part Result
		var err error
		if run.tool == "wrk" {
			err = parseWrk(outs[i], &part)
		} else {
			err = parseH2load(outs[i], &part)
		}
		if err != nil {
			return err
		}
		r.Requests += part.Requests
		r.RequestsPerSec += part.RequestsPerSec
		r.Errors += part.Errors
		for k, v := range part.ErrorKinds {
			kinds[k] += v
		}
		if len(runs) == 1 {
			r.Latency = part.Latency
			continue
		}
		w := float64(part.Requests)
		mean += part.Latency.Mean * w
		stdev += part.Latency.Stdev * w
		r.Latency.Max = max(r.Latency.Max, part.Latency.Max)
	}
	r.ErrorKinds = nonZero(kinds)
	if len(runs) > 1 && r.Requests > 0 {
		r.Latency.Mean, r.Latency.Stdev = mean/float64(r.Requests), stdev/float64(r.Requests)
	}
	return nil
}

// ratePercentiles reads the latency percentiles of a run at a fixed rate
// from its processes' logs, in the load generator container, and removes
// them. h2load logs a line a request, its third field the request's time in
// microseconds.
func (h *harness) ratePercentiles(ctx context.Context, runs []loadRun, l *Latency) error {
	var logs []string
	for _, run := range runs {
		if run.log != "" {
			logs = append(logs, run.log)
		}
	}
	if len(logs) == 0 {
		return nil
	}
	script := "cat " + strings.Join(logs, " ") + " | cut -f3 | sort -n | awk '{v[NR]=$1} END {if (NR == 0) exit 1; " +
		"printf \"%d %d %d %d\\n\", v[int(NR*0.5)+1], v[int(NR*0.9)+1], v[int(NR*0.99)+1], v[int(NR*0.999)+1]}'; rm -f " +
		strings.Join(logs, " ")
	out, err := docker(ctx, "exec", loadgenName, "sh", "-c", script)
	if err != nil {
		return fmt.Errorf("the latency percentiles: %v", err)
	}
	var us [4]float64
	if _, err := fmt.Sscan(out, &us[0], &us[1], &us[2], &us[3]); err != nil {
		return fmt.Errorf("the latency percentiles: %q: %v", out, err)
	}
	l.P50, l.P90, l.P99, l.P999 = us[0]/1e3, us[1]/1e3, us[2]/1e3, us[3]/1e3
	return nil
}

// holdIdle opens h.holdIdle idle TLS connections to the server and holds
// them, and returns a function that closes them and reports how many the
// server had closed meanwhile, and how many were held.
func (h *harness) holdIdle(ctx context.Context) (release func() (closed int), held int, err error) {
	sources := 1
	if h.serverHost == "127.0.0.1" {
		sources = (h.holdIdleConns + 39999) / 40000
	}
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", loadgenName, "/fitbench/idleconn",
		"-mode", "tls", "-url", "https://"+h.serverHost+":8443/idle", "-n", strconv.Itoa(h.holdIdleConns),
		"-concurrency", "256", "-hold", "10m", "-sources", strconv.Itoa(sources))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, 0, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, err
	}
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		if f := strings.Fields(lines.Text()); len(f) >= 2 && f[0] == "READY" {
			held, _ = strconv.Atoi(f[1])
			release = func() int {
				stdin.Close()
				closed := 0
				for lines.Scan() {
					if f := strings.Fields(lines.Text()); len(f) >= 2 && f[0] == "DONE" {
						closed, _ = strconv.Atoi(f[1])
					}
				}
				_ = cmd.Wait()
				return closed
			}
			return release, held, nil
		}
	}
	stdin.Close()
	waitErr := cmd.Wait()
	return nil, 0, fmt.Errorf("idleconn: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
}

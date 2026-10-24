// Command benchreport renders a fitbench run's results as the markdown tables
// the benchmark reports use: a table for each scenario and protocol, with a
// row for each server, ranked by throughput, then CPU efficiency, then memory
// efficiency, each ranked value followed by its share of the best; and a
// table of the memory each server holds per idle connection.
//
//	go run ./bench/cmd/benchreport -in bench/results/run.json [-prev earlier.json]
//
// With -prev, each row also gives its throughput's change from the same
// server, scenario and protocol in the earlier run. With -sweep, a list of
// runs made at rising connection counts, it renders instead a table for each
// scenario and protocol with a column for each run.
//
// Throughput counts successful responses only: a response the load generator
// counted as an error is not served.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

func main() {
	in := flag.String("in", "", "the run's results, as fitbench writes them")
	prev := flag.String("prev", "", "an earlier run, to compare throughput with")
	prevName := flag.String("prev-name", "prev", "what to call the earlier run in the table")
	level := flag.Int("level", 3, "the heading level of each table's title")
	idle := flag.Bool("idle", true, "also render the idle connections")
	idleOnly := flag.Bool("idle-only", false, "render the idle connections alone")
	idleHeading := flag.String("idle-heading", "Idle connections, bytes each", "the idle connections table's title")
	sweep := flag.String("sweep", "", "runs at rising connection counts, comma-separated, to render as scaling tables")
	flag.Parse()
	if *sweep != "" {
		var runs []*run
		for _, path := range strings.Split(*sweep, ",") {
			r, err := load(path)
			if err != nil {
				fmt.Fprintln(os.Stderr, "benchreport:", err)
				os.Exit(1)
			}
			runs = append(runs, r)
		}
		renderSweep(os.Stdout, runs, *level)
		return
	}
	if *in == "" {
		fmt.Fprintln(os.Stderr, "benchreport: -in is required")
		os.Exit(2)
	}
	cur, err := load(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "benchreport:", err)
		os.Exit(1)
	}
	var old *run
	if *prev != "" {
		if old, err = load(*prev); err != nil {
			fmt.Fprintln(os.Stderr, "benchreport:", err)
			os.Exit(1)
		}
	}
	if *idleOnly {
		cur.Results = nil
	}
	render(os.Stdout, cur, old, *prevName, *level, *idle || *idleOnly, *idleHeading)
}

// run is the part of a fitbench report that the tables need.
type run struct {
	Params struct {
		Connections   int `json:"connections"`
		H2Connections int `json:"h2_connections"`
		H2Streams     int `json:"h2_streams"`
	} `json:"params"`
	Results []result `json:"results"`
	Idle    []struct {
		Server       string  `json:"server"`
		Kind         string  `json:"kind"`
		Connections  int     `json:"connections"`
		Skipped      string  `json:"skipped"`
		Error        string  `json:"error"`
		BytesPerConn float64 `json:"bytes_per_conn"`
	} `json:"idle"`
}

type result struct {
	Server         string  `json:"server"`
	Scenario       string  `json:"scenario"`
	Protocol       string  `json:"protocol"`
	Skipped        string  `json:"skipped"`
	Error          string  `json:"error"`
	RequestsPerSec float64 `json:"requests_per_sec"`
	Requests       int64   `json:"requests"`
	Errors         int64   `json:"errors"`
	CPUCores       float64 `json:"cpu_cores"`
	// BackendCPUCores is the proxy backend's CPU during the run, given by
	// runs that measured it.
	BackendCPUCores float64 `json:"backend_cpu_cores"`
	AnonBytes       int64   `json:"anon_bytes"`
	AnonPeakBytes   int64   `json:"anon_peak_bytes"`
	// BytesPerInFlight and HitRatio are given only by runs, and scenarios,
	// that measure them.
	BytesPerInFlight float64  `json:"bytes_per_in_flight"`
	HitRatio         *float64 `json:"hit_ratio"`
	Latency          *struct {
		P50  float64 `json:"p50"`
		P99  float64 `json:"p99"`
		P999 float64 `json:"p99.9"`
		Mean float64 `json:"mean"`
		Max  float64 `json:"max"`
	} `json:"latency_ms"`
	// Rate is the requests a second a run kept to, 0 for as many as it
	// could; IdleHeld and IdleClosed the idle connections held open during
	// it, and those the server closed.
	Rate       int `json:"rate"`
	IdleHeld   int `json:"idle_held"`
	IdleClosed int `json:"idle_closed"`
}

func load(path string) (*run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r run
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return &r, nil
}

// tps is the successful responses a second: the requests the load generator
// counted, less those it counted as errors.
func (r result) tps() float64 {
	if r.Requests > 0 && r.Errors > 0 {
		return r.RequestsPerSec * float64(max(0, r.Requests-r.Errors)) / float64(r.Requests)
	}
	return r.RequestsPerSec
}

// cpuEER is successful responses per CPU-second: throughput for each core
// kept busy.
func (r result) cpuEER() float64 {
	if r.CPUCores <= 0 {
		return 0
	}
	return r.tps() / r.CPUCores
}

// failure says why a run gave no result: the load generator hung and was
// killed, the server did not start, or the run failed otherwise.
func (r result) failure() string {
	switch {
	case strings.Contains(r.Error, "exit status 137"):
		return "hung"
	case strings.Contains(r.Error, "did not answer"):
		return "did not start"
	}
	return "error"
}

// errorsCell is the errors and their share of the requests, or says that the
// run had no responses at all.
func (r result) errorsCell() string {
	switch {
	case r.Requests == 0 && r.RequestsPerSec == 0:
		return "no responses"
	case r.Errors > 0 && r.Requests > 0:
		return fmt.Sprintf("%s (%.0f%%)", group(float64(r.Errors), 0), float64(r.Errors)/float64(r.Requests)*100)
	}
	return strconv.FormatInt(r.Errors, 10)
}

// memory is the server's peak anonymous memory where the run sampled it, its
// anonymous memory at the end of the run otherwise.
func (r result) memory() int64 {
	if r.AnonPeakBytes > 0 {
		return r.AnonPeakBytes
	}
	return r.AnonBytes
}

// memEER is requests a second for each MiB of memory.
func (r result) memEER() float64 {
	if m := r.memory(); m > 0 {
		return r.tps() / (float64(m) / (1 << 20))
	}
	return 0
}

var protocolNames = map[string]string{"http/1.1": "HTTP/1.1", "https/1.1": "HTTP/1.1 over TLS", "h2c": "h2c", "h2": "h2", "h3": "HTTP/3"}

func render(w io.Writer, cur, old *run, prevName string, level int, idle bool, idleHeading string) {
	var servers []string
	type key struct{ scenario, protocol string }
	var keys []key
	for _, r := range cur.Results {
		if !slices.Contains(servers, r.Server) {
			servers = append(servers, r.Server)
		}
		if k := (key{r.Scenario, r.Protocol}); !slices.Contains(keys, k) {
			keys = append(keys, k)
		}
	}
	peak := slices.ContainsFunc(cur.Results, func(r result) bool { return r.AnonPeakBytes > 0 })
	memTitle := "Memory"
	if peak {
		memTitle = "Peak memory"
	}
	hashes := strings.Repeat("#", level)
	for _, k := range keys {
		var rows, failed []result
		var skipped []string
		for _, r := range cur.Results {
			if r.Scenario != k.scenario || r.Protocol != k.protocol {
				continue
			}
			switch {
			case r.Skipped != "":
				skipped = append(skipped, r.Server)
			case r.Error != "":
				failed = append(failed, r)
			default:
				rows = append(rows, r)
			}
		}
		if len(rows) == 0 && len(failed) == 0 {
			continue
		}
		// At a fixed rate every server answers about as many, and they are
		// ranked by their p99 latency instead, the lowest first.
		rated := slices.ContainsFunc(rows, func(r result) bool { return r.Rate > 0 })
		slices.SortStableFunc(rows, func(a, b result) int {
			keys := [][2]float64{{a.tps(), b.tps()}, {a.cpuEER(), b.cpuEER()}, {a.memEER(), b.memEER()}}
			if rated && a.Latency != nil && b.Latency != nil {
				keys = append([][2]float64{{-a.Latency.P99, -b.Latency.P99}}, keys[1:]...)
			}
			for _, c := range keys {
				if c[0] != c[1] {
					if c[0] > c[1] {
						return -1
					}
					return 1
				}
			}
			return slices.Index(servers, a.Server) - slices.Index(servers, b.Server)
		})
		var bestTPS, bestCPU, bestMem float64
		for _, r := range rows {
			bestTPS, bestCPU, bestMem = max(bestTPS, r.tps()), max(bestCPU, r.cpuEER()), max(bestMem, r.memEER())
		}
		inFlight := slices.ContainsFunc(rows, func(r result) bool { return r.BytesPerInFlight > 0 })
		hits := slices.ContainsFunc(rows, func(r result) bool { return r.HitRatio != nil })
		idleHeld := slices.ContainsFunc(rows, func(r result) bool { return r.IdleHeld > 0 })
		header := []string{"Server", "TPS [↓1]", "CPU EER [↓2]", "MEM EER [↓3]", latencyTitle(k.protocol, rated), "CPU", memTitle}
		if rated {
			header[1], header[4] = "TPS", header[4]+" [↓1]"
		}
		if inFlight {
			header = append(header, "KiB/in-flight")
		}
		if hits {
			header = append(header, "Hits")
		}
		if idleHeld {
			header = append(header, "Idle held / closed")
		}
		backendCPU := slices.ContainsFunc(rows, func(r result) bool { return r.BackendCPUCores > 0 })
		if backendCPU {
			header = append(header, "Backend CPU")
		}
		header = append(header, "Errors")
		if old != nil {
			header = append(header, "TPS vs "+prevName)
		}
		table := [][]string{header}
		for _, r := range rows {
			row := []string{r.Server,
				share(r.tps(), bestTPS, 0), share(r.cpuEER(), bestCPU, 0), share(r.memEER(), bestMem, 0),
				latency(r), fmt.Sprintf("%.2f", r.CPUCores), mib(r.memory())}
			if inFlight {
				row = append(row, group(r.BytesPerInFlight/1024, 1))
			}
			if hits {
				ratio := ""
				if r.HitRatio != nil {
					ratio = fmt.Sprintf("%.2f%%", *r.HitRatio*100)
				}
				row = append(row, ratio)
			}
			if idleHeld {
				row = append(row, fmt.Sprintf("%s / %d", group(float64(r.IdleHeld), 0), r.IdleClosed))
			}
			if backendCPU {
				row = append(row, fmt.Sprintf("%.2f", r.BackendCPUCores))
			}
			row = append(row, r.errorsCell())
			if old != nil {
				row = append(row, change(r, old))
			}
			table = append(table, row)
		}
		for _, r := range failed {
			row := make([]string, len(header))
			row[0], row[1] = r.Server, r.failure()
			table = append(table, row)
		}
		name := protocolNames[k.protocol]
		if name == "" {
			name = k.protocol
		}
		if rated && len(rows) > 0 {
			name += fmt.Sprintf(", %s requests a second", group(float64(rows[0].Rate), 0))
		}
		fmt.Fprintf(w, "%s %s, %s\n\n", hashes, k.scenario, name)
		writeTable(w, table, 1)
		if len(skipped) > 0 {
			fmt.Fprintf(w, "\nNot run: %s, which cannot serve it.\n", strings.Join(skipped, ", "))
		}
		fmt.Fprintln(w)
	}
	if idle && len(cur.Idle) > 0 {
		renderIdle(w, cur, old, prevName, servers, hashes, idleHeading)
	}
}

func latencyTitle(protocol string, rated bool) string {
	switch {
	case rated:
		return "p50 / p99 / p99.9"
	case protocol == "http/1.1" || protocol == "https/1.1":
		return "p99"
	}
	return "Mean / max"
}

// latency is the percentiles of a run at a fixed rate, from h2load's
// per-request log; wrk's p99; or h2load's mean and maximum, which is all it
// gives otherwise.
func latency(r result) string {
	switch {
	case r.Latency == nil:
		return ""
	case r.Rate > 0 && r.Latency.P99 > 0:
		return fmt.Sprintf("%.2f / %.2f / %.2f ms", r.Latency.P50, r.Latency.P99, r.Latency.P999)
	case (r.Protocol == "http/1.1" || r.Protocol == "https/1.1") && r.Latency.P99 > 0:
		return fmt.Sprintf("%.2f ms", r.Latency.P99)
	}
	return fmt.Sprintf("%.2f / %.0f ms", r.Latency.Mean, r.Latency.Max)
}

func mib(b int64) string {
	if b <= 0 {
		return ""
	}
	return group(float64(b)/(1<<20), 0) + " MiB"
}

// change is r's throughput against the same server, scenario and protocol in
// old.
func change(r result, old *run) string {
	for _, o := range old.Results {
		if o.Server == r.Server && o.Scenario == r.Scenario && o.Protocol == r.Protocol && o.tps() > 0 {
			pct := math.Round((r.tps()/o.tps() - 1) * 100)
			if pct == 0 {
				return "0%"
			}
			return fmt.Sprintf("%+.0f%%", pct)
		}
	}
	return "—"
}

// share is v followed by its share of best, floored so that only the best
// reads 100%.
func share(v, best float64, prec int) string {
	if best <= 0 {
		return group(v, prec)
	}
	return fmt.Sprintf("%s %3d%%", group(v, prec), int(math.Floor(v/best*100)))
}

// shareLow is share for a value where lower is better: the best's share of v.
func shareLow(v, best float64) string {
	if v <= 0 {
		return group(v, 0)
	}
	return fmt.Sprintf("%s %3d%%", group(v, 0), int(math.Floor(best/v*100)))
}

// group formats v with prec decimals and its thousands separated by commas.
func group(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	neg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if neg {
		out = "-" + out
	}
	if frac != "" {
		out += "." + frac
	}
	return out
}

// renderIdle renders the memory per idle connection: a row for each server,
// a column for each kind, ranked by the kinds in order, lowest first, each
// value followed by the best's share of it.
func renderIdle(w io.Writer, cur, old *run, prevName string, servers []string, hashes, heading string) {
	was := func(server, kind string) string {
		if old == nil {
			return ""
		}
		for _, o := range old.Idle {
			if o.Server == server && o.Kind == kind && o.BytesPerConn > 0 {
				return " (" + group(o.BytesPerConn, 0) + ")"
			}
		}
		return ""
	}
	var kinds []string
	conns := 0
	bytes := map[string]map[string]float64{}
	failed := map[string]bool{}
	for _, r := range cur.Idle {
		if r.Skipped != "" {
			continue
		}
		if !slices.Contains(kinds, r.Kind) {
			kinds = append(kinds, r.Kind)
		}
		if bytes[r.Server] == nil {
			bytes[r.Server] = map[string]float64{}
		}
		if r.Error != "" {
			failed[r.Server] = true
			continue
		}
		bytes[r.Server][r.Kind] = r.BytesPerConn
		conns = max(conns, r.Connections)
	}
	var names []string
	for _, s := range servers {
		if bytes[s] != nil {
			names = append(names, s)
		}
	}
	for s := range bytes {
		if !slices.Contains(names, s) {
			names = append(names, s)
		}
	}
	slices.SortStableFunc(names, func(a, b string) int {
		for _, k := range kinds {
			x, y := bytes[a][k], bytes[b][k]
			if x != y {
				// What was not measured goes last.
				if x == 0 {
					return 1
				}
				if y == 0 {
					return -1
				}
				if x < y {
					return -1
				}
				return 1
			}
		}
		return 0
	})
	best := map[string]float64{}
	for _, k := range kinds {
		for _, s := range names {
			if v := bytes[s][k]; v > 0 && (best[k] == 0 || v < best[k]) {
				best[k] = v
			}
		}
	}
	header := []string{"Server"}
	for i, k := range kinds {
		header = append(header, fmt.Sprintf("%s [↑%d]", k, i+1))
	}
	table := [][]string{header}
	for _, s := range names {
		row := []string{s}
		for _, k := range kinds {
			switch v := bytes[s][k]; {
			case v > 0:
				row = append(row, shareLow(v, best[k])+was(s, k))
			case failed[s]:
				row = append(row, "error")
			default:
				row = append(row, "—")
			}
		}
		table = append(table, row)
	}
	fmt.Fprintf(w, "%s %s\n\n", hashes, heading)
	writeTable(w, table, 1)
	note := ""
	if old != nil {
		note = " In brackets, " + prevName + "'s."
	}
	fmt.Fprintf(w, "\nWith %s connections of each kind; lower is better.%s\n\n", group(float64(conns), 0), note)
}

// writeTable writes table as a markdown table, its columns padded to line up,
// the first left of the others' right alignment.
func writeTable(w io.Writer, table [][]string, left int) {
	widths := make([]int, len(table[0]))
	for _, row := range table {
		for i, c := range row {
			widths[i] = max(widths[i], len([]rune(c)), 3)
		}
	}
	line := func(cells []string) {
		var b strings.Builder
		b.WriteString("|")
		for i, c := range cells {
			pad := strings.Repeat(" ", widths[i]-len([]rune(c)))
			if i < left {
				b.WriteString(" " + c + pad + " |")
			} else {
				b.WriteString(" " + pad + c + " |")
			}
		}
		fmt.Fprintln(w, b.String())
	}
	line(table[0])
	sep := make([]string, len(widths))
	for i, n := range widths {
		if i < left {
			sep[i] = strings.Repeat("-", n)
		} else {
			sep[i] = strings.Repeat("-", n-1) + ":"
		}
	}
	line(sep)
	for _, row := range table[1:] {
		line(row)
	}
}

// renderSweep renders runs made at rising connection counts: for each
// scenario and protocol, a row for each server and a column for each run,
// with the server's successful responses a second there and their share of
// that run's best, ranked by the most the server answered at any count, then
// by its CPU efficiency there.
func renderSweep(w io.Writer, runs []*run, level int) {
	type key struct{ scenario, protocol string }
	var keys []key
	var servers []string
	for _, rn := range runs {
		for _, r := range rn.Results {
			if !slices.Contains(servers, r.Server) {
				servers = append(servers, r.Server)
			}
			if k := (key{r.Scenario, r.Protocol}); !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	find := func(rn *run, server string, k key) (result, bool) {
		for _, r := range rn.Results {
			if r.Server == server && r.Scenario == k.scenario && r.Protocol == k.protocol {
				return r, true
			}
		}
		return result{}, false
	}
	hashes := strings.Repeat("#", level)
	for _, k := range keys {
		type row struct {
			server string
			peak   result
		}
		var rows []row
		for _, s := range servers {
			var peak result
			ran := false
			for _, rn := range runs {
				r, ok := find(rn, s, k)
				if !ok || r.Skipped != "" {
					continue
				}
				ran = true
				if r.Error == "" && (peak.Server == "" || r.tps() > peak.tps()) {
					peak = r
				}
			}
			if ran {
				rows = append(rows, row{s, peak})
			}
		}
		if len(rows) == 0 {
			continue
		}
		slices.SortStableFunc(rows, func(a, b row) int {
			for _, c := range [][2]float64{{a.peak.tps(), b.peak.tps()}, {a.peak.cpuEER(), b.peak.cpuEER()}} {
				if c[0] != c[1] {
					if c[0] > c[1] {
						return -1
					}
					return 1
				}
			}
			return slices.Index(servers, a.server) - slices.Index(servers, b.server)
		})
		header := []string{"Server", "Peak TPS [↓1]", "CPU EER at peak [↓2]"}
		best := make([]float64, len(runs))
		for i, rn := range runs {
			if k.protocol == "http/1.1" {
				header = append(header, group(float64(rn.Params.Connections), 0))
			} else {
				header = append(header, fmt.Sprintf("%s×%d", group(float64(rn.Params.H2Connections), 0), rn.Params.H2Streams))
			}
			for _, s := range servers {
				if r, ok := find(rn, s, k); ok && r.Error == "" && r.Skipped == "" {
					best[i] = max(best[i], r.tps())
				}
			}
		}
		var bestPeak, bestCPU float64
		for _, r := range rows {
			bestPeak, bestCPU = max(bestPeak, r.peak.tps()), max(bestCPU, r.peak.cpuEER())
		}
		table := [][]string{header}
		for _, r := range rows {
			line := []string{r.server, share(r.peak.tps(), bestPeak, 0), share(r.peak.cpuEER(), bestCPU, 0)}
			for i, rn := range runs {
				res, ok := find(rn, r.server, k)
				switch {
				case !ok || res.Skipped != "":
					line = append(line, "—")
				case res.Error != "":
					line = append(line, res.failure())
				case res.tps() == 0:
					line = append(line, "0")
				default:
					line = append(line, fmt.Sprintf("%sk %3d%%", group(res.tps()/1000, 0), int(math.Floor(res.tps()/best[i]*100))))
				}
			}
			table = append(table, line)
		}
		name := protocolNames[k.protocol]
		if name == "" {
			name = k.protocol
		}
		fmt.Fprintf(w, "%s %s, %s\n\n", hashes, k.scenario, name)
		writeTable(w, table, 1)
		fmt.Fprintln(w)
	}
}

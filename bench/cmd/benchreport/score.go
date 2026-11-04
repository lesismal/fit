package main

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"slices"
	"strings"
)

// The scores. Each table of the load test plan, one scenario over one
// protocol in one of the plan's results files, is an item. A server's score
// for an item is from 0 to 100: the weighted sum of its shares of the best in
// the table — throughput, CPU efficiency, memory efficiency and latency, the
// lowest latency being the best — scaled down by the share of its requests
// that failed. A server that failed the run, or that cannot serve the
// scenario, scores 0 for it. A server the plan does not run in a workload has
// no score there.
//
// Each workload has a weight, shared equally by its items, and a server's
// overall score is the weighted mean of its item scores over the items it
// was run in; its coverage is the share of the plan's weight those items
// hold.

// metricWeights is how much each share counts in an item's score.
type metricWeights struct{ tps, cpu, mem, latency float64 }

var (
	// defaultMetrics is for a workload driven as hard as the server goes:
	// throughput first, then what it costs in CPU and in memory, then the
	// latency at that load.
	defaultMetrics = metricWeights{tps: 0.50, cpu: 0.25, mem: 0.15, latency: 0.10}
	// idleMetrics is W5's: the idle connections held make memory count
	// more.
	idleMetrics = metricWeights{tps: 0.40, cpu: 0.20, mem: 0.30, latency: 0.10}
	// rateMetrics is W6's: at a fixed rate every server that keeps up
	// answers as many, and the p99 latency is what tells them apart.
	rateMetrics = metricWeights{tps: 0.20, cpu: 0.20, mem: 0.10, latency: 0.50}
)

// workload is one of the plan's workloads, named by its results files'
// prefix.
type workload struct {
	prefix, name string
	weight       float64
	metrics      metricWeights
}

// workloads are the plan's workloads and their weights, which add up to 1:
// the API gateway counts most, being what fit is most used as; the raw
// ceiling, the website and latency at a fixed rate next; churn, large
// transfers and idle connections least.
var workloads = []workload{
	{"w0", "W0, the raw ceiling", 0.15, defaultMetrics},
	{"w1", "W1, the API gateway", 0.25, defaultMetrics},
	{"w2", "W2, the website", 0.15, defaultMetrics},
	{"w3", "W3, connection churn", 0.10, defaultMetrics},
	{"w4", "W4, large transfers", 0.10, defaultMetrics},
	{"w5", "W5, idle and active", 0.10, idleMetrics},
	{"w6", "W6, latency at a fixed rate", 0.15, rateMetrics},
}

// workloadOf is the workload a results file belongs to, by its name.
func workloadOf(name string) (workload, bool) {
	prefix, _, _ := strings.Cut(name, "-")
	for _, w := range workloads {
		if w.prefix == prefix {
			return w, true
		}
	}
	return workload{}, false
}

// item is one table of the plan.
type item struct {
	file, scenario, protocol string
}

// plan is a plan's results files, with each item's weight.
type plan struct {
	runs    []*run
	weights map[item]float64
}

// loadPlan reads every workload's results files in dir, and gives each of
// their items its share of its workload's weight.
func loadPlan(dir string) (*plan, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "w*.json"))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(paths, func(a, b string) int { return compareNames(filepath.Base(a), filepath.Base(b)) })
	p := &plan{weights: map[item]float64{}}
	count := map[string]int{}
	for _, path := range paths {
		r, err := load(path)
		if err != nil {
			return nil, err
		}
		w, ok := workloadOf(r.name)
		if !ok {
			continue
		}
		p.runs = append(p.runs, r)
		for _, k := range r.items() {
			p.weights[k] = 0
			count[w.prefix]++
		}
	}
	if len(p.runs) == 0 {
		return nil, fmt.Errorf("%s: no results of the plan's workloads", dir)
	}
	for k := range p.weights {
		w, _ := workloadOf(k.file)
		p.weights[k] = w.weight / float64(count[w.prefix])
	}
	return p, nil
}

// compareNames orders results files by workload, then by their level, as a
// number: w0-fixed-500 before w0-fixed-2000.
func compareNames(a, b string) int {
	a, b = strings.TrimSuffix(a, ".json"), strings.TrimSuffix(b, ".json")
	ai, bi := strings.LastIndex(a, "-"), strings.LastIndex(b, "-")
	if ai < 0 || bi < 0 || a[:ai] != b[:bi] {
		return strings.Compare(a, b)
	}
	var x, y int
	if _, err := fmt.Sscan(a[ai+1:], &x); err != nil {
		return strings.Compare(a, b)
	}
	if _, err := fmt.Sscan(b[bi+1:], &y); err != nil {
		return strings.Compare(a, b)
	}
	return x - y
}

// items are r's tables, in the order they first appear.
func (r *run) items() []item {
	var out []item
	for _, res := range r.Results {
		if k := (item{r.name, res.Scenario, res.Protocol}); !slices.Contains(out, k) {
			out = append(out, k)
		}
	}
	return out
}

// latencyOf is the latency an item's score goes by: the p99 where the load
// generator gives it (wrk, and h2load at a fixed rate), the mean otherwise;
// 0 when there is none.
func latencyOf(r result) float64 {
	switch {
	case r.Latency == nil:
		return 0
	case r.Latency.P99 > 0:
		return r.Latency.P99
	}
	return r.Latency.Mean
}

// scores are the item scores of the servers in one table: rows are its
// results, failed and skipped included, which score 0.
func scores(rows []result, m metricWeights) map[string]float64 {
	var bestTPS, bestCPU, bestMem, bestLat float64
	ok := func(r result) bool { return r.Skipped == "" && r.Error == "" && r.tps() > 0 }
	for _, r := range rows {
		if !ok(r) {
			continue
		}
		bestTPS, bestCPU, bestMem = max(bestTPS, r.tps()), max(bestCPU, r.cpuEER()), max(bestMem, r.memEER())
		if l := latencyOf(r); l > 0 && (bestLat == 0 || l < bestLat) {
			bestLat = l
		}
	}
	part := func(v, best float64) float64 {
		if best <= 0 || v <= 0 {
			return 0
		}
		return min(1, v/best)
	}
	out := map[string]float64{}
	for _, r := range rows {
		if !ok(r) {
			out[r.Server] = 0
			continue
		}
		// A share not measured for any server counts as everyone's best,
		// so that it neither lifts nor sinks one against another.
		lat := 1.0
		if bestLat > 0 {
			lat = 0
			if l := latencyOf(r); l > 0 {
				lat = bestLat / l
			}
		}
		cpu, mem := 1.0, 1.0
		if bestCPU > 0 {
			cpu = part(r.cpuEER(), bestCPU)
		}
		if bestMem > 0 {
			mem = part(r.memEER(), bestMem)
		}
		s := m.tps*part(r.tps(), bestTPS) + m.cpu*cpu + m.mem*mem + m.latency*lat
		if r.Requests > 0 && r.Errors > 0 {
			s *= float64(max(0, r.Requests-r.Errors)) / float64(r.Requests)
		}
		out[r.Server] = 100 * s
	}
	return out
}

// tableScores are the item scores of one of r's tables.
func tableScores(r *run, scenario, protocol string) map[string]float64 {
	var rows []result
	for _, res := range r.Results {
		if res.Scenario == scenario && res.Protocol == protocol {
			rows = append(rows, res)
		}
	}
	m := defaultMetrics
	if w, ok := workloadOf(r.name); ok {
		m = w.metrics
	}
	return scores(rows, m)
}

// renderScores renders how the plan is scored, every item's weight and
// scores, and the servers ranked by their overall score.
func renderScores(w io.Writer, p *plan, level int) {
	hashes := strings.Repeat("#", level)
	var servers []string
	for _, r := range p.runs {
		for _, res := range r.Results {
			if !slices.Contains(servers, res.Server) {
				servers = append(servers, res.Server)
			}
		}
	}
	type total struct{ weighted, weight float64 }
	overall := map[string]*total{}
	byWorkload := map[string]map[string]*total{}
	for _, s := range servers {
		overall[s] = &total{}
		byWorkload[s] = map[string]*total{}
	}
	header := append([]string{"Item", "Weight"}, servers...)
	table := [][]string{header}
	for _, r := range p.runs {
		wl, _ := workloadOf(r.name)
		for _, k := range r.items() {
			sc := tableScores(r, k.scenario, k.protocol)
			weight := p.weights[k]
			name := protocolNames[k.protocol]
			if name == "" {
				name = k.protocol
			}
			row := []string{fmt.Sprintf("%s: %s, %s", r.name, k.scenario, name), fmt.Sprintf("%.2f%%", weight*100)}
			best := -1.0
			for _, v := range sc {
				best = max(best, v)
			}
			for _, s := range servers {
				v, ran := sc[s]
				if !ran {
					row = append(row, "—")
					continue
				}
				cell := fmt.Sprintf("%.1f", v)
				if v == best {
					cell = "**" + cell + "**"
				}
				row = append(row, cell)
				overall[s].weighted += weight * v
				overall[s].weight += weight
				t := byWorkload[s][wl.prefix]
				if t == nil {
					t = &total{}
					byWorkload[s][wl.prefix] = t
				}
				t.weighted += weight * v
				t.weight += weight
			}
			table = append(table, row)
		}
	}
	type ranked struct {
		server       string
		score, cover float64
	}
	var ranking []ranked
	for _, s := range servers {
		t := overall[s]
		if t.weight > 0 {
			ranking = append(ranking, ranked{s, t.weighted / t.weight, t.weight})
		}
	}
	slices.SortStableFunc(ranking, func(a, b ranked) int {
		switch {
		case a.score > b.score:
			return -1
		case a.score < b.score:
			return 1
		}
		return slices.Index(servers, a.server) - slices.Index(servers, b.server)
	})

	fmt.Fprintf(w, "%s Overall ranking\n\n", hashes)
	rank := [][]string{{"Rank", "Server", "Score"}}
	for _, wl := range workloads {
		rank[0] = append(rank[0], strings.ToUpper(wl.prefix[:1])+wl.prefix[1:])
	}
	rank[0] = append(rank[0], "Coverage")
	for i, r := range ranking {
		row := []string{fmt.Sprint(i + 1), r.server, fmt.Sprintf("%.1f", r.score)}
		for _, wl := range workloads {
			if t := byWorkload[r.server][wl.prefix]; t != nil && t.weight > 0 {
				row = append(row, fmt.Sprintf("%.1f", t.weighted/t.weight))
			} else {
				row = append(row, "—")
			}
		}
		row = append(row, fmt.Sprintf("%.0f%%", math.Round(r.cover*100)))
		rank = append(rank, row)
	}
	writeTable(w, rank, 2)
	fmt.Fprintf(w, "\nThe score is the weighted mean of a server's item scores over the items it ran in, out of 100; "+
		"each workload's column is the same over that workload's items. Coverage is the share of the plan's weight "+
		"those items hold: a server the plan does not run in a workload has no score there, and one that cannot "+
		"serve a scenario it is run in scores 0 for it.\n\n")

	fmt.Fprintf(w, "%s How the items are scored\n\n", hashes)
	rules := [][]string{{"Workload", "Weight", "Items", "Weight an item", "TPS", "CPU EER", "MEM EER", "Latency"}}
	for _, wl := range workloads {
		n := 0
		for k := range p.weights {
			if f, _ := workloadOf(k.file); f.prefix == wl.prefix {
				n++
			}
		}
		if n == 0 {
			continue
		}
		m := wl.metrics
		rules = append(rules, []string{wl.name, fmt.Sprintf("%.0f%%", wl.weight*100), fmt.Sprint(n),
			fmt.Sprintf("%.2f%%", wl.weight/float64(n)*100), pct(m.tps), pct(m.cpu), pct(m.mem), pct(m.latency)})
	}
	writeTable(w, rules, 1)
	fmt.Fprintf(w, "\nAn item is one table: a scenario over a protocol in one results file. A server's score for it is "+
		"100 times the weighted sum of its shares of the table's best throughput, CPU efficiency (TPS a core), "+
		"memory efficiency (TPS a MiB of peak memory) and latency (the best's latency over its own: p99 where "+
		"measured, the mean otherwise), times the share of its requests that did not fail. A run that failed or "+
		"that the server cannot serve scores 0.\n\n")

	fmt.Fprintf(w, "%s Every item\n\n", hashes)
	writeTable(w, table, 1)
	fmt.Fprintf(w, "\nEach item's best in bold; — where the plan does not run the server.\n\n")
}

func pct(v float64) string { return fmt.Sprintf("%.0f%%", v*100) }

// renderScoresFile is -scores: the scores of the plan in dir.
func renderScoresFile(w io.Writer, dir string, level int) error {
	p, err := loadPlan(dir)
	if err != nil {
		return err
	}
	renderScores(w, p, level)
	return nil
}

// scoring is the plan that -plan read, which has render give each table its
// weight and each row its score; nil without it.
var scoring *plan

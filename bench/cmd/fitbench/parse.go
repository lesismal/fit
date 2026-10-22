package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Result is one server answering one scenario over one protocol.
type Result struct {
	Server   string `json:"server"`
	Scenario string `json:"scenario"`
	Protocol string `json:"protocol"`
	Tool     string `json:"tool,omitempty"`
	// Skipped says why the run was not made: the server cannot serve the
	// scenario. Error says why it failed.
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`

	RequestsPerSec float64 `json:"requests_per_sec,omitempty"`
	Requests       int64   `json:"requests,omitempty"`
	// BackendRequests counts the requests that reached the backend during
	// the run, and HitRatio is the share of Requests the server answered
	// without it, for a cache scenario.
	BackendRequests int64    `json:"backend_requests,omitempty"`
	HitRatio        *float64 `json:"hit_ratio,omitempty"`
	// Errors counts requests that failed or were answered with a status
	// other than 2xx.
	Errors int64 `json:"errors"`
	// ErrorKinds breaks Errors down by the load generator's own categories.
	ErrorKinds map[string]int64 `json:"error_kinds,omitempty"`
	// Latency is in milliseconds. h2load reports no percentiles, only mean,
	// standard deviation and maximum.
	Latency *Latency `json:"latency_ms,omitempty"`

	// CPUCores is the CPU time the server used during the measured run
	// divided by its length: 2.5 is two and a half cores kept busy.
	CPUCores float64 `json:"cpu_cores,omitempty"`
	// MemoryBytes is the server's cgroup memory at the end of the run, and
	// AnonBytes the part of it that is not page cache: what the process
	// itself holds.
	MemoryBytes int64 `json:"memory_bytes,omitempty"`
	AnonBytes   int64 `json:"anon_bytes,omitempty"`
}

// Latency is a latency distribution, in milliseconds.
type Latency struct {
	P50   float64 `json:"p50,omitempty"`
	P90   float64 `json:"p90,omitempty"`
	P99   float64 `json:"p99,omitempty"`
	P999  float64 `json:"p99.9,omitempty"`
	Mean  float64 `json:"mean"`
	Stdev float64 `json:"stdev"`
	Max   float64 `json:"max"`
}

// wrkReport is the JSON line report.lua prints.
type wrkReport struct {
	Requests   int64 `json:"requests"`
	DurationUS int64 `json:"duration_us"`
	Errors     struct {
		Connect, Read, Write, Status, Timeout int64
	} `json:"errors"`
	LatencyUS struct {
		P50, P90, P99, P999, Max float64
		Mean, Stdev              float64
	} `json:"latency_us"`
}

// parseWrk reads the report report.lua adds to wrk's output into r.
func parseWrk(out string, r *Result) error {
	for line := range strings.SplitSeq(out, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "FITBENCH ")
		if !ok {
			continue
		}
		var w wrkReport
		if err := json.Unmarshal([]byte(data), &w); err != nil {
			return fmt.Errorf("wrk report: %v", err)
		}
		if w.DurationUS <= 0 {
			return fmt.Errorf("wrk report: duration %dus", w.DurationUS)
		}
		r.Requests = w.Requests
		r.RequestsPerSec = float64(w.Requests) / (float64(w.DurationUS) / 1e6)
		e := w.Errors
		r.Errors = e.Connect + e.Read + e.Write + e.Status + e.Timeout
		r.ErrorKinds = nonZero(map[string]int64{"connect": e.Connect, "read": e.Read, "write": e.Write, "status": e.Status, "timeout": e.Timeout})
		l := w.LatencyUS
		r.Latency = &Latency{P50: l.P50 / 1e3, P90: l.P90 / 1e3, P99: l.P99 / 1e3, P999: l.P999 / 1e3,
			Mean: l.Mean / 1e3, Stdev: l.Stdev / 1e3, Max: l.Max / 1e3}
		return nil
	}
	return fmt.Errorf("wrk printed no report: %.500q", out)
}

var (
	h2Finished = regexp.MustCompile(`(?m)^finished in \S+, ([\d.]+) req/s`)
	h2Requests = regexp.MustCompile(`(?m)^requests: (\d+) total, \d+ started, \d+ done, (\d+) succeeded, (\d+) failed, (\d+) errored, (\d+) timeout`)
	h2Status   = regexp.MustCompile(`(?m)^status codes: (\d+) 2xx, (\d+) 3xx, (\d+) 4xx, (\d+) 5xx`)
	h2Time     = regexp.MustCompile(`(?m)^time for request:\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)`)
)

// parseH2load reads h2load's output into r.
func parseH2load(out string, r *Result) error {
	m := h2Finished.FindStringSubmatch(out)
	if m == nil {
		return fmt.Errorf("h2load printed no summary: %.500q", out)
	}
	r.RequestsPerSec, _ = strconv.ParseFloat(m[1], 64)
	if m = h2Requests.FindStringSubmatch(out); m == nil {
		return fmt.Errorf("h2load printed no request counts: %.500q", out)
	}
	n := atoi(m[1:])
	r.Requests = n[0]
	// h2load's failed counts the errored requests, those that timed out,
	// and those answered 4xx or 5xx; a 3xx is a success to it, but not here.
	r.Errors = n[2]
	kinds := map[string]int64{"failed": n[2], "errored": n[3], "timeout": n[4]}
	if m = h2Status.FindStringSubmatch(out); m != nil {
		n := atoi(m[1:])
		r.Errors += n[1]
		kinds["3xx"], kinds["4xx"], kinds["5xx"] = n[1], n[2], n[3]
	}
	r.ErrorKinds = nonZero(kinds)
	if m = h2Time.FindStringSubmatch(out); m == nil {
		return fmt.Errorf("h2load printed no request times: %.500q", out)
	}
	var ms [3]float64 // min is not kept
	for i, s := range m[2:5] {
		d, err := parseH2Duration(s)
		if err != nil {
			return err
		}
		ms[i] = d
	}
	r.Latency = &Latency{Max: ms[0], Mean: ms[1], Stdev: ms[2]}
	return nil
}

// nonZero drops the zero counts from m, and returns nil when none are left.
func nonZero(m map[string]int64) map[string]int64 {
	for k, v := range m {
		if v == 0 {
			delete(m, k)
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func atoi(ss []string) []int64 {
	n := make([]int64, len(ss))
	for i, s := range ss {
		n[i], _ = strconv.ParseInt(s, 10, 64)
	}
	return n
}

// parseH2Duration reads a duration as h2load prints one, "123us", "1.23ms"
// or "2.00s", into milliseconds.
func parseH2Duration(s string) (float64, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("h2load duration %q: %v", s, err)
	}
	return float64(d) / float64(time.Millisecond), nil
}

// cgroupCPU reads usage_usec from a cgroup v2 cpu.stat.
func cgroupCPU(stat string) (time.Duration, bool) {
	return cgroupField(stat, "usage_usec", time.Microsecond)
}

// cgroupAnon reads anon from a cgroup v2 memory.stat.
func cgroupAnon(stat string) (int64, bool) {
	d, ok := cgroupField(stat, "anon", 1)
	return int64(d), ok
}

func cgroupField(stat, name string, unit time.Duration) (time.Duration, bool) {
	for line := range strings.SplitSeq(stat, "\n") {
		if v, ok := strings.CutPrefix(line, name+" "); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return time.Duration(n) * unit, err == nil
		}
	}
	return 0, false
}

// parseRequestCount sums fit's fit_http_requests_total samples for route in
// out, the text of its Admin API's /metrics.
func parseRequestCount(out, route string) (int64, error) {
	var n int64
	found := false
	label := `route="` + route + `"`
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.HasPrefix(line, "fit_http_requests_total{") || !strings.Contains(line, label) {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			return 0, fmt.Errorf("metrics: %q: %v", line, err)
		}
		n += int64(v)
		found = true
	}
	if !found {
		return 0, fmt.Errorf("metrics: no fit_http_requests_total for route %q", route)
	}
	return n, nil
}

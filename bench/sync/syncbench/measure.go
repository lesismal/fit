package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Result is one mover's run of one scenario.
type Result struct {
	Mover    string `json:"mover"`
	Scenario string `json:"scenario"`
	Variant  string `json:"variant,omitempty"`
	// Records are the measured records, and Counted those of them the sink
	// had at the end; Extra the records past all that were written,
	// counted twice.
	Records int64 `json:"records"`
	Counted int64 `json:"counted"`
	Extra   int64 `json:"extra,omitempty"`
	// RecordsPerSec is the sink's count's rise from the first measured
	// record it had to the last, over that time, DrainSeconds; CPUCores the
	// mover's CPU over the same time, in cores.
	RecordsPerSec float64 `json:"records_per_sec"`
	DrainSeconds  float64 `json:"drain_seconds"`
	CPUCores      float64 `json:"cpu_cores"`
	// StartupSeconds is from the start of the measurement to the sink's
	// first record: the warm-up rows of a stream, the first record
	// otherwise. WriteSeconds is how long the measured rows of a stream
	// took to write.
	StartupSeconds float64 `json:"startup_seconds"`
	WriteSeconds   float64 `json:"write_seconds,omitempty"`
	// RSSPeakBytes is the most anonymous memory the mover's containers held
	// together, sampled every interval through the measurement.
	RSSPeakBytes int64    `json:"rss_peak_bytes"`
	Lag          *Lag     `json:"lag_ms,omitempty"`
	Profiles     []string `json:"profiles,omitempty"`
	Error        string   `json:"error,omitempty"`
	// Rounds and Spread are merge's: the rounds a result is the median of,
	// and their throughputs' spread, (max - min) / median.
	// Failures are the errors of the rounds that failed beside them.
	Rounds   int      `json:"rounds,omitempty"`
	Spread   float64  `json:"spread,omitempty"`
	Failures []string `json:"failures,omitempty"`
}

// Lag is the steady phase's: from each write's commit to the sink's count
// reaching it, in milliseconds, at Rate records a second.
type Lag struct {
	Rate    int     `json:"rate"`
	Samples int     `json:"samples"`
	P50     float64 `json:"p50"`
	P99     float64 `json:"p99"`
	Max     float64 `json:"max"`
	Mean    float64 `json:"mean"`
}

func measureCmd(ctx context.Context, o *options, stdout io.Writer) error {
	r := measure(ctx, o)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if o.out == "" {
		_, err = stdout.Write(data)
		return err
	}
	return os.WriteFile(o.out, data, 0o644)
}

// sample is the sink's count and the mover's usage at a time.
type sample struct {
	t time.Time
	n int64
	u usage
}

// meter counts the sink and samples the mover.
type meter struct {
	o     *options
	ctr   counter
	dk    *dockerAPI
	peak  int64
	start time.Time
	// failed is a writer's error, running beside until, which stops it.
	failed atomic.Pointer[error]
}

func (m *meter) poll(ctx context.Context) (sample, error) {
	n, err := m.ctr.count(ctx)
	if err != nil {
		return sample{}, fmt.Errorf("counting the sink: %w", err)
	}
	s := sample{t: time.Now(), n: n}
	if m.dk != nil && len(m.o.containers) > 0 {
		if s.u, err = m.dk.sample(ctx, m.o.containers); err != nil {
			return s, err
		}
		m.peak = max(m.peak, s.u.anon)
	}
	return s, nil
}

// until polls every interval until the count reaches want, for at most
// limit, and returns the samples taken; the last reached want unless err.
// Each sample is passed to each, if set.
func (m *meter) until(ctx context.Context, want int64, limit time.Duration, each func(sample)) ([]sample, error) {
	deadline := time.Now().Add(limit)
	var out []sample
	tick := time.NewTicker(m.o.interval)
	defer tick.Stop()
	for {
		if err := m.failed.Load(); err != nil {
			return out, *err
		}
		s, err := m.poll(ctx)
		if err != nil {
			return out, err
		}
		out = append(out, s)
		if each != nil {
			each(s)
		}
		if s.n >= want {
			return out, nil
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("the sink had %d of %d records after %s", s.n, want, limit)
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-tick.C:
		}
	}
}

// measure runs the scenario's measurement for a mover started already; a
// failure is the result's Error.
func measure(ctx context.Context, o *options) *Result {
	r := &Result{Mover: o.mover, Scenario: o.scenario, Variant: o.variant, Records: int64(o.records)}
	if err := measureInto(ctx, o, r); err != nil {
		r.Error = err.Error()
	}
	return r
}

func measureInto(ctx context.Context, o *options, r *Result) error {
	m := &meter{o: o, start: time.Now()}
	defer func() { r.RSSPeakBytes = m.peak }()
	ctr, err := openCounter(ctx, o)
	if err != nil {
		return err
	}
	defer ctr.close()
	m.ctr = ctr
	if len(o.containers) > 0 {
		if m.dk, err = newDocker(o.docker, o.dockerTimeout); err != nil {
			return err
		}
	}
	// A mover whose records are there before it starts is started here,
	// once the sink has been counted, so that what it moves from its start
	// on is measured.
	var before *sample
	if o.start {
		n, err := m.ctr.count(ctx)
		if err != nil {
			return fmt.Errorf("counting the sink: %w", err)
		}
		if err := m.each(ctx, m.dk.start); err != nil {
			return err
		}
		m.start = time.Now()
		before = &sample{t: m.start, n: n}
	}
	var src source
	if o.sc.stream || (o.steadyRate > 0 && o.sc.source == "files") {
		if src, err = openSource(ctx, o, o.writers); err != nil {
			return err
		}
		defer src.close()
	}

	// A stream's warm-up rows, through the sink, say the mover follows the
	// source; then the measured rows, the mover paused while they are
	// written, or not. base is the sink's count before them, next the
	// rows written so far: rows written before the mover started are not
	// read, by a mover that starts from the source's position then.
	base, next := int64(0), 0
	var wrote chan error
	if o.sc.stream {
		var err error
		if base, next, err = m.warm(ctx, src); err != nil {
			return fmt.Errorf("waiting for the warm-up rows: %w", err)
		}
		r.StartupSeconds = time.Since(m.start).Seconds()
		if o.pause {
			if err := m.each(ctx, m.dk.pause); err != nil {
				return err
			}
			t := time.Now()
			err := src.write(ctx, next, o.records)
			r.WriteSeconds = time.Since(t).Seconds()
			if uerr := m.each(context.WithoutCancel(ctx), m.dk.unpause); err == nil {
				err = uerr
			}
			if err != nil {
				return fmt.Errorf("writing the rows: %w", err)
			}
		} else {
			wrote = make(chan error, 1)
			go func(next int) {
				t := time.Now()
				err := src.write(ctx, next, o.records)
				r.WriteSeconds = time.Since(t).Seconds()
				if err != nil {
					err = fmt.Errorf("writing the rows: %w", err)
					m.failed.Store(&err)
				}
				wrote <- err
			}(next)
		}
	}
	next += o.records

	profiled := m.profile(ctx, r)
	var first *sample
	samples, err := m.until(ctx, base+int64(o.records), o.timeout, func(s sample) {
		if s.n <= base {
			before = &s
		} else if first == nil {
			first = &s
		}
	})
	if wrote != nil {
		<-wrote
	}
	if len(samples) > 0 {
		r.Counted = max(0, samples[len(samples)-1].n-base)
	}
	if err != nil {
		profiled()
		return err
	}
	last := samples[len(samples)-1]
	from := first
	if from == nil || from.n >= last.n && before != nil {
		from = before
	}
	if from == nil {
		from = &last
	}
	if r.StartupSeconds == 0 && first != nil {
		r.StartupSeconds = first.t.Sub(m.start).Seconds()
	}
	if d := last.t.Sub(from.t); d > 0 {
		r.DrainSeconds = d.Seconds()
		r.RecordsPerSec = float64(last.n-from.n) / d.Seconds()
		r.CPUCores = float64(last.u.cpu-from.u.cpu) / float64(d)
	}
	want := base + int64(o.records)

	if o.steadyRate > 0 && src != nil {
		lag, err := m.steady(ctx, src, want, next)
		if err != nil {
			profiled()
			return err
		}
		r.Lag = lag
		want += int64(o.steadyRate) * int64(o.steadyFor/time.Second)
	}

	// Records counted twice show as the count passing what was written.
	end := time.Now().Add(o.settle)
	for time.Now().Before(end) {
		s, err := m.poll(ctx)
		if err != nil {
			break
		}
		r.Extra = max(r.Extra, s.n-want)
		select {
		case <-ctx.Done():
		case <-time.After(o.interval):
		}
	}
	profiled()
	m.heap(ctx, r)
	return nil
}

// each calls f on every container of the mover.
func (m *meter) each(ctx context.Context, f func(context.Context, string) error) error {
	if m.dk == nil {
		return errors.New("-pause and -start need the mover's -containers")
	}
	for _, c := range m.o.containers {
		if err := f(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// warm writes a transaction of warm-up rows at a time, from the first,
// until the sink's count rises: the mover follows the source. Once the
// count has stayed put for a second, it returns it and the rows written.
func (m *meter) warm(ctx context.Context, src source) (int64, int, error) {
	o := m.o
	deadline := time.Now().Add(o.startupFor)
	n0, err := m.ctr.count(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("counting the sink: %w", err)
	}
	next := 0
	for seen := false; !seen; {
		if time.Now().After(deadline) {
			return 0, next, fmt.Errorf("the sink had none of %d rows after %s", next, o.startupFor)
		}
		if err := src.write(ctx, next, o.txRows); err != nil {
			return 0, next, err
		}
		next += o.txRows
		for wait := time.Now().Add(warmWait); !seen && time.Now().Before(wait); {
			s, err := m.poll(ctx)
			if err != nil {
				return 0, next, err
			}
			seen = s.n > n0
			if !seen {
				time.Sleep(o.interval)
			}
		}
	}
	last, still := int64(-1), time.Now()
	for time.Since(still) < warmSettle {
		if time.Now().After(deadline) {
			return 0, next, fmt.Errorf("the sink's count did not settle in %s", o.startupFor)
		}
		s, err := m.poll(ctx)
		if err != nil {
			return 0, next, err
		}
		if s.n != last {
			last, still = s.n, time.Now()
		}
		time.Sleep(o.interval)
	}
	return last, next, nil
}

// How long warm waits for a transaction to reach the sink before it
// writes another, and how long the count must stay put after.
var warmWait, warmSettle = 2 * time.Second, time.Second

// steady writes steadyRate records a second for steadyFor, rows next+1
// on, after the count reached base, and measures each write's lag.
func (m *meter) steady(ctx context.Context, src source, base int64, next int) (*Lag, error) {
	o := m.o
	total := o.steadyRate * int(o.steadyFor/time.Second)
	if total == 0 {
		return nil, nil
	}
	const tick = 20 * time.Millisecond
	chunk := max(1, o.steadyRate*int(tick)/int(time.Second))
	type point struct {
		t time.Time
		n int64
	}
	var mu sync.Mutex
	var writes []point
	wrote := make(chan error, 1)
	go func() {
		start := time.Now()
		for done := 0; done < total; {
			n := min(chunk, total-done)
			// Due when the rate says the records before these are written.
			if d := time.Until(start.Add(time.Duration(done) * time.Second / time.Duration(o.steadyRate))); d > 0 {
				time.Sleep(d)
			}
			if err := src.write(ctx, next+done, n); err != nil {
				m.failed.Store(&err)
				wrote <- err
				return
			}
			done += n
			mu.Lock()
			writes = append(writes, point{time.Now(), base + int64(done)})
			mu.Unlock()
		}
		wrote <- nil
	}()
	samples, err := m.until(ctx, base+int64(total), o.timeout, nil)
	// The count reaches the last rows only once they are written, and a
	// failed write stops until.
	<-wrote
	if err != nil {
		return nil, fmt.Errorf("the steady phase: %w", err)
	}
	mu.Lock()
	defer mu.Unlock()
	var lags []float64
	for _, w := range writes {
		i := slices.IndexFunc(samples, func(s sample) bool { return s.n >= w.n && !s.t.Before(w.t) })
		if i >= 0 {
			lags = append(lags, float64(samples[i].t.Sub(w.t))/float64(time.Millisecond))
		}
	}
	return lagOf(o.steadyRate, lags), nil
}

// lagOf summarizes lags, in milliseconds.
func lagOf(rate int, lags []float64) *Lag {
	l := &Lag{Rate: rate, Samples: len(lags)}
	if len(lags) == 0 {
		return l
	}
	slices.Sort(lags)
	at := func(q float64) float64 { return lags[int(q*float64(len(lags)-1))] }
	var sum float64
	for _, v := range lags {
		sum += v
	}
	l.P50, l.P99, l.Max, l.Mean = at(0.5), at(0.99), lags[len(lags)-1], sum/float64(len(lags))
	return l
}

// profile starts fit's CPU profile, for profileFor, and returns what waits
// for it to be saved.
func (m *meter) profile(ctx context.Context, r *Result) func() {
	if m.o.pprof == "" {
		return func() {}
	}
	done := make(chan string, 1)
	go func() {
		path := fmt.Sprintf("profile?seconds=%d", max(1, int(m.o.profileFor/time.Second)))
		name, err := m.fetch(ctx, path, "cpu")
		// A mover just started may not listen yet.
		for end := time.Now().Add(10 * time.Second); err != nil && errors.Is(err, errDial) && time.Now().Before(end); {
			time.Sleep(100 * time.Millisecond)
			name, err = m.fetch(ctx, path, "cpu")
		}
		if err != nil {
			name = ""
			fmt.Fprintln(os.Stderr, "syncbench: the CPU profile:", err)
		}
		done <- name
	}()
	return sync.OnceFunc(func() {
		if name := <-done; name != "" {
			r.Profiles = append(r.Profiles, name)
		}
	})
}

// heap saves fit's heap profile.
func (m *meter) heap(ctx context.Context, r *Result) {
	if m.o.pprof == "" {
		return
	}
	name, err := m.fetch(ctx, "heap", "heap")
	if err != nil {
		fmt.Fprintln(os.Stderr, "syncbench: the heap profile:", err)
		return
	}
	r.Profiles = append(r.Profiles, name)
}

// errDial is fetch's error when the Admin API does not answer.
var errDial = errors.New("the Admin API did not answer")

// fetch saves the profile at /debug/pprof/<path> of fit's Admin API, at
// -pprof (its Unix socket), as <mover>[-<variant>]-<kind>.pprof in
// profileDir, and returns its name.
func (m *meter) fetch(ctx context.Context, path, kind string) (string, error) {
	client, base, err := httpClient(m.o.pprof, m.o.profileFor+time.Minute)
	if err != nil {
		return "", fmt.Errorf("-pprof: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/debug/pprof/"+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errDial, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return "", fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	name := m.o.mover
	if m.o.variant != "" {
		name += "-" + m.o.variant
	}
	name += "-" + kind + ".pprof"
	f, err := os.Create(filepath.Join(m.o.profileDir, name))
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(f, resp.Body); err == nil {
		err = f.Close()
	} else {
		f.Close()
	}
	return name, err
}

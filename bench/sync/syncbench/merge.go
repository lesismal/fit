package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// Report is a results file: one scenario's variant, every mover's rounds,
// and each mover's median round, which bench/cmd/benchreport renders.
type Report struct {
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Finished time.Time         `json:"finished"`
	Host     map[string]string `json:"host,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Images   map[string]string `json:"images,omitempty"`
	Results  []Result          `json:"results"`
	Runs     []Result          `json:"runs"`
}

// pairs is a flag of key=value pairs, given again for each.
type pairs map[string]string

func (p pairs) String() string { return "" }

func (p pairs) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("%q is not key=value", s)
	}
	p[k] = v
	return nil
}

func mergeCmd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("syncbench merge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "the results file; standard output when left out")
	name := fs.String("name", "", "the results' name, its file's name without .json")
	host, params, images := pairs{}, pairs{}, pairs{}
	fs.Var(host, "host", "the host's description, key=value, again for each")
	fs.Var(params, "param", "a setting of the run, key=value, again for each")
	fs.Var(images, "image", "a mover's or server's image, name=image, again for each")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return err
		}
		return errUsage
	}
	if fs.NArg() == 0 {
		fmt.Fprintln(stderr, "usage: syncbench merge [-out F] [-name N] [-host k=v] [-param k=v] [-image k=v] round.json...")
		return errUsage
	}
	rep := Report{Kind: "sync", Name: *name, Finished: time.Now().UTC(), Host: host, Params: params, Images: images}
	for _, path := range fs.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var r Result
		if err := json.Unmarshal(data, &r); err != nil {
			return fmt.Errorf("%s: %v", path, err)
		}
		rep.Runs = append(rep.Runs, r)
	}
	rep.Results = medians(rep.Runs)
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if *out == "" {
		_, err = stdout.Write(data)
		return err
	}
	return os.WriteFile(*out, data, 0o644)
}

// medians is each mover's median round by throughput, in the order the
// movers first come, with the rounds' count and their spread. A mover
// whose every round failed gets its first failure.
func medians(runs []Result) []Result {
	var movers []string
	for _, r := range runs {
		if !slices.Contains(movers, r.Mover) {
			movers = append(movers, r.Mover)
		}
	}
	var out []Result
	for _, m := range movers {
		var ok, failed []Result
		for _, r := range runs {
			switch {
			case r.Mover != m:
			case r.Error != "":
				failed = append(failed, r)
			default:
				ok = append(ok, r)
			}
		}
		if len(ok) == 0 {
			r := failed[0]
			r.Rounds = len(failed)
			out = append(out, r)
			continue
		}
		slices.SortStableFunc(ok, func(a, b Result) int {
			switch {
			case a.RecordsPerSec < b.RecordsPerSec:
				return -1
			case a.RecordsPerSec > b.RecordsPerSec:
				return 1
			}
			return 0
		})
		r := ok[(len(ok)-1)/2]
		r.Rounds = len(ok) + len(failed)
		if r.RecordsPerSec > 0 {
			r.Spread = (ok[len(ok)-1].RecordsPerSec - ok[0].RecordsPerSec) / r.RecordsPerSec
		}
		for _, f := range failed {
			r.Failures = append(r.Failures, f.Error)
		}
		out = append(out, r)
	}
	return out
}

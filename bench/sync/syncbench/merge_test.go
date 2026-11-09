package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMedians(t *testing.T) {
	runs := []Result{
		{Mover: "fit", RecordsPerSec: 100},
		{Mover: "canal", Error: "boom"},
		{Mover: "fit", RecordsPerSec: 300},
		{Mover: "fit", RecordsPerSec: 200},
		{Mover: "canal", Error: "again"},
		{Mover: "x", RecordsPerSec: 0},
		{Mover: "fit", Error: "late", RecordsPerSec: 1},
		{Mover: "fit", RecordsPerSec: 200},
	}
	out := medians(runs)
	if len(out) != 3 {
		t.Fatalf("%d results", len(out))
	}
	fit := out[0]
	if fit.RecordsPerSec != 200 || fit.Rounds != 5 || fit.Spread != 1 || len(fit.Failures) != 1 || fit.Error != "" {
		t.Errorf("fit: %+v", fit)
	}
	if c := out[1]; c.Error != "boom" || c.Rounds != 2 {
		t.Errorf("canal: %+v", c)
	}
	if x := out[2]; x.Spread != 0 || x.Rounds != 1 {
		t.Errorf("x: %+v", x)
	}
}

func TestMerge(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, r Result) string {
		data, _ := json.Marshal(r)
		p := filepath.Join(dir, name)
		os.WriteFile(p, data, 0o644)
		return p
	}
	a := write("a.json", Result{Mover: "fit", RecordsPerSec: 10})
	b := write("b.json", Result{Mover: "fit", RecordsPerSec: 20})
	out := filepath.Join(dir, "m.json")
	if err := mergeCmd([]string{"-out", out, "-name", "n", "-host", "cpu=x", "-param", "records=5", "-image", "fit=y", a, b}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var rep Report
	data, _ := os.ReadFile(out)
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Kind != "sync" || rep.Name != "n" || rep.Host["cpu"] != "x" || rep.Params["records"] != "5" ||
		rep.Images["fit"] != "y" || len(rep.Runs) != 2 || len(rep.Results) != 1 || rep.Results[0].Rounds != 2 {
		t.Errorf("report: %+v", rep)
	}
	var stdout bytes.Buffer
	if err := mergeCmd([]string{a}, &stdout, io.Discard); err != nil || !json.Valid(stdout.Bytes()) {
		t.Errorf("stdout: %v", err)
	}
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte("{"), 0o644)
	for _, args := range [][]string{
		{bad},
		{"-host", "nokey", a},
		{"-out", filepath.Join(dir, "none", "m.json"), a},
	} {
		if err := mergeCmd(args, io.Discard, io.Discard); err == nil {
			t.Errorf("%q: no error", args)
		}
	}
	if (pairs{}).String() != "" || (pairs{}).Set("=v") == nil {
		t.Error("pairs")
	}
}

func TestCloses(t *testing.T) {
	(&fileCounter{}).close()
	(&esCounter{}).close()
	(&fileSource{}).close()
}

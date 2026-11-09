package synctest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/influxdb"
	_ "github.com/lesismal/fit/sync/tdengine"
)

// obsEnv returns the environment variable name, which test/sync/obs.sh
// sets; without it the test skips, unless obs.sh ran (FIT_SYNC_OBS) with
// FIT_SYNC_INTEROP=required, which fails it.
func obsEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" && os.Getenv("FIT_SYNC_OBS") != "" {
			t.Fatalf("%s is not set", name)
		}
		t.Skipf("%s is not set (test/sync/obs.sh sets it)", name)
	}
	return v
}

// loadSink loads the sink module with params, its Cleanup at the test's
// end.
func loadSink(t *testing.T, name string, params map[string]any) fitsync.Sink {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": "x", "pipelines": []any{map[string]any{"name": "p",
		"source": map[string]any{"module": "generate"}, "sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": name, "params": params}}}}}}})
	cfg, err := config.Load(b)
	if err != nil {
		t.Fatal(err)
	}
	s, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { module.Cleanup(s) })
	return s.(fitsync.Sink)
}

// httpDo sends a request and returns its answer's body, failing the test on
// a status past 299.
func httpDo(t *testing.T, method, u string, header http.Header, body string) []byte {
	t.Helper()
	req, _ := http.NewRequest(method, u, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode > 299 {
		t.Fatalf("%s %s: %d %s", method, u, resp.StatusCode, b)
	}
	return b
}

// tsRec is a record of series dev, at second sec, with value v.
func tsRec(ns string, op fitsync.Op, dev string, sec int64, v any) *fitsync.Record {
	var val fitsync.Value
	switch v := v.(type) {
	case int:
		val = fitsync.Int(int64(v))
	case string:
		val = fitsync.String(v)
	}
	row := fitsync.Map(fitsync.Field{Name: "dev", Value: fitsync.String(dev)},
		fitsync.Field{Name: "at", Value: fitsync.Time(time.Unix(1_700_000_000+sec, 0).UTC())},
		fitsync.Field{Name: "v", Value: val})
	r := &fitsync.Record{Namespace: ns, Op: op, Key: fitsync.Map(fitsync.Field{Name: "dev", Value: fitsync.String(dev)}, fitsync.Field{Name: "sec", Value: fitsync.Int(sec)})}
	if op == fitsync.OpDelete {
		r.Before = row
	} else {
		r.After = row
	}
	return r
}

// tsChanges are points of 4 series, 50 seconds each, some written over by
// updates: the last value of each is 1000+sec for even seconds.
func tsChanges(ns string) []*fitsync.Record {
	var recs []*fitsync.Record
	for sec := range int64(50) {
		for d := range 4 {
			recs = append(recs, tsRec(ns, fitsync.OpInsert, fmt.Sprintf("d%d", d), sec, int(sec)))
		}
	}
	for sec := int64(0); sec < 50; sec += 2 {
		for d := range 4 {
			recs = append(recs, tsRec(ns, fitsync.OpUpdate, fmt.Sprintf("d%d", d), sec, int(1000+sec)))
		}
	}
	return recs
}

// tsSum is the sum of the values tsChanges leaves: 4 series, each second's
// last value 1000+sec when even, sec when odd.
var tsSum = func() float64 {
	sum := 0.0
	for sec := range 50 {
		if sec%2 == 0 {
			sum += float64(4 * (1000 + sec))
		} else {
			sum += float64(4 * sec)
		}
	}
	return sum
}()

// influxQL runs an InfluxQL query through the v1 API and returns the first
// value of its first series' first row.
func influxQL(t *testing.T, base string, header http.Header, db, q string) float64 {
	t.Helper()
	b := httpDo(t, http.MethodGet, base+"/query?"+url.Values{"db": {db}, "q": {q}}.Encode(), header, "")
	var r struct {
		Results []struct {
			Series []struct{ Values [][]any }
		}
	}
	if err := json.Unmarshal(b, &r); err != nil || len(r.Results) == 0 || len(r.Results[0].Series) == 0 {
		return 0
	}
	v, _ := r.Results[0].Series[0].Values[0][1].(float64)
	return v
}

// TestInfluxDBConverges writes points through each API, then the whole
// batch again from its middle and from its start: each series and time is
// one point, with its last value.
func TestInfluxDBConverges(t *testing.T) {
	u2 := obsEnv(t, "FIT_SYNC_INFLUX2_URL")
	t.Setenv("FIT_TEST_INFLUX_TOKEN", obsEnv(t, "FIT_SYNC_INFLUX2_TOKEN"))
	h2 := http.Header{"Authorization": {"Token " + os.Getenv("FIT_TEST_INFLUX_TOKEN")}}
	for _, api := range []string{"v2", "v1"} {
		t.Run(api, func(t *testing.T) {
			m := "fit_converge_" + api + fmt.Sprint(time.Now().UnixNano())
			p := map[string]any{"url": u2, "api": api, "token_env": "FIT_TEST_INFLUX_TOKEN", "tags": []string{"dev"}, "time_field": "at", "precision": "s", "measurement": m}
			if api == "v2" {
				p["org"], p["bucket"] = "fit", "fit"
			} else {
				p["database"] = "fit"
			}
			s := loadSink(t, "influxdb", p)
			if err := s.(fitsync.Tester).Test(context.Background()); err != nil {
				t.Fatal(err)
			}
			recs := tsChanges("x")
			write(t, s, recs, 37)
			write(t, s, recs[len(recs)/2:], 23)
			write(t, s, recs, 101)
			if n := influxQL(t, u2, h2, "fit", "SELECT count(v) FROM "+m); n != 200 {
				t.Errorf("%v points, want 200", n)
			}
			if n := influxQL(t, u2, h2, "fit", "SELECT sum(v) FROM "+m); n != tsSum {
				t.Errorf("the sum %v", n)
			}
		})
	}
	t.Run("v3", func(t *testing.T) {
		u3 := obsEnv(t, "FIT_SYNC_INFLUX3_URL")
		m := fmt.Sprint("fit_converge_", time.Now().UnixNano())
		s := loadSink(t, "influxdb", map[string]any{"url": u3, "api": "v3", "database": "fit", "tags": []string{"dev"}, "time_field": "at", "measurement": m})
		recs := tsChanges("x")
		write(t, s, recs, 37)
		write(t, s, recs, 101)
		if err := s.(fitsync.Tester).Test(context.Background()); err != nil {
			t.Fatal(err)
		}
		b := httpDo(t, http.MethodGet, u3+"/api/v3/query_sql?"+url.Values{"db": {"fit"}, "q": {"SELECT count(*) AS n, sum(v) AS s FROM " + m}, "format": {"json"}}.Encode(), nil, "")
		var rows []struct{ N, S int64 }
		if err := json.Unmarshal(b, &rows); err != nil || len(rows) != 1 || rows[0].N != 200 || float64(rows[0].S) != tsSum {
			t.Fatalf("%s: %v", b, err)
		}
	})
}

// TestInfluxDBBadPoint writes a batch whose fifth point has a field of
// another type than the measurement's: it fails for good, at its record,
// the others written.
func TestInfluxDBBadPoint(t *testing.T) {
	u2 := obsEnv(t, "FIT_SYNC_INFLUX2_URL")
	t.Setenv("FIT_TEST_INFLUX_TOKEN", obsEnv(t, "FIT_SYNC_INFLUX2_TOKEN"))
	h2 := http.Header{"Authorization": {"Token " + os.Getenv("FIT_TEST_INFLUX_TOKEN")}}
	m := fmt.Sprint("fit_bad_", time.Now().UnixNano())
	s := loadSink(t, "influxdb", map[string]any{"url": u2, "org": "fit", "bucket": "fit", "token_env": "FIT_TEST_INFLUX_TOKEN",
		"tags": []string{"dev"}, "time_field": "at", "measurement": m})
	var recs []*fitsync.Record
	for i := range int64(10) {
		var v any = int(i)
		if i == 4 {
			v = "not a number"
		}
		recs = append(recs, tsRec("x", fitsync.OpInsert, "d", i, v))
	}
	err := s.Write(context.Background(), recs)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 4 || !fitsync.IsPermanent(err) {
		t.Fatalf("%v", err)
	}
	if err := s.Write(context.Background(), recs[5:]); err != nil {
		t.Fatal(err)
	}
	if n := influxQL(t, u2, h2, "fit", "SELECT count(v) FROM "+m); n != 9 {
		t.Errorf("%v points, want 9", n)
	}
}

// tdengine is the server the TDengine tests write to.
type tdengine struct{ url, password string }

func tdengineServer(t *testing.T) *tdengine {
	td := &tdengine{url: obsEnv(t, "FIT_SYNC_TDENGINE_URL"), password: os.Getenv("FIT_SYNC_TDENGINE_PASSWORD")}
	if td.password == "" {
		td.password = "taosdata"
	}
	td.sql(t, "CREATE DATABASE IF NOT EXISTS fit_sync PRECISION 'ms'")
	return td
}

// sql runs q and returns its rows.
func (td *tdengine) sql(t *testing.T, q string) [][]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, td.url+"/rest/sql", strings.NewReader(q))
	req.SetBasicAuth("root", td.password)
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r struct {
		Code int
		Desc string
		Data [][]any
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.Code != 0 {
		t.Fatalf("%s: %v %d %s", q, err, r.Code, r.Desc)
	}
	return r.Data
}

func (td *tdengine) sink(t *testing.T, params map[string]any) fitsync.Sink {
	t.Setenv("FIT_TEST_TD_PASSWORD", td.password)
	p := map[string]any{"url": td.url, "username": "root", "password_env": "FIT_TEST_TD_PASSWORD", "database": "fit_sync",
		"tags": []string{"dev"}, "time_field": "at"}
	for k, v := range params {
		p[k] = v
	}
	return loadSink(t, "tdengine", p)
}

// TestTDengineConverges writes rows schemaless and by SQL, then the batch
// again: each subtable and timestamp is one row, with its last value; a
// row of the wrong type fails for good at its record.
func TestTDengineConverges(t *testing.T) {
	td := tdengineServer(t)
	for _, mode := range []string{"schemaless", "sql"} {
		t.Run(mode, func(t *testing.T) {
			st := fmt.Sprint("fit_", mode, "_", time.Now().UnixNano())
			p := map[string]any{"mode": mode, "measurement": st}
			if mode == "sql" {
				td.sql(t, "CREATE STABLE fit_sync."+st+" (ts TIMESTAMP, at TIMESTAMP, v BIGINT) TAGS (dev NCHAR(16))")
				p["subtable"] = st + "_{dev}"
			}
			s := td.sink(t, p)
			if err := s.(fitsync.Tester).Test(context.Background()); err != nil {
				t.Fatal(err)
			}
			if mode == "schemaless" {
				// The schemaless columns: at as a string, of its text.
				p["fields"] = []string{"v"}
				s = td.sink(t, p)
			}
			recs := tsChanges("x")
			write(t, s, recs, 37)
			write(t, s, recs[len(recs)/2:], 23)
			write(t, s, recs, 101)
			rows := td.sql(t, "SELECT count(*), sum(v) FROM fit_sync."+st)
			if len(rows) != 1 || rows[0][0].(float64) != 200 || rows[0][1].(float64) != tsSum {
				t.Fatalf("%v", rows)
			}
			bad := []*fitsync.Record{tsRec("x", fitsync.OpInsert, "d0", 100, 1), tsRec("x", fitsync.OpInsert, "d0", 101, "text"), tsRec("x", fitsync.OpInsert, "d0", 102, 2)}
			err := s.Write(context.Background(), bad)
			var be *fitsync.BatchError
			if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) {
				t.Fatalf("%v", err)
			}
		})
	}
}

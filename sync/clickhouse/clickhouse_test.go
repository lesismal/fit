package syncclickhouse

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func TestDedupToken(t *testing.T) {
	rec := func(pos string) *fitsync.Record { return &fitsync.Record{Position: fitsync.Position(pos)} }
	a := []*fitsync.Record{rec(`{"n":1}`), rec(""), rec(`{"n":3}`)}
	b := []*fitsync.Record{rec(`{"n":1}`), rec(""), rec(`{"n":3}`)}
	if DedupToken("t", a) != DedupToken("t", b) || DedupToken("t", a) == "" {
		t.Error("the same records make different tokens")
	}
	if DedupToken("t", a) == DedupToken("u", a) || DedupToken("t", a) == DedupToken("t", a[:2]) || DedupToken("t", a[1:]) == DedupToken("t", a) {
		t.Error("other tables or records make the same token")
	}
	if DedupToken("t", []*fitsync.Record{rec(""), rec("")}) != "" {
		t.Error("records without positions have a token")
	}
}

func TestLiteralAndCondition(t *testing.T) {
	key := fitsync.Map(
		fitsync.Field{Name: "id", Value: fitsync.Int(-3)},
		fitsync.Field{Name: "s", Value: fitsync.String(`it's \ a`)},
		fitsync.Field{Name: "at", Value: fitsync.Time(time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC))},
		fitsync.Field{Name: "d", Value: fitsync.Date(2026, 1, 2)},
		fitsync.Field{Name: "n", Value: fitsync.Null()},
		fitsync.Field{Name: "a`b", Value: fitsync.Decimal("1.50")},
	)
	got, err := keyCondition(key)
	want := "(`id` = -3 AND `s` = 'it\\'s \\\\ a' AND `at` = parseDateTime64BestEffort('2026-01-02T03:04:05.000000006Z', 9) AND `d` = toDate('2026-01-02') AND `n` IS NULL AND `a\\`b` = 1.50)"
	if err != nil || string(got) != want {
		t.Fatalf("%v\n%s\n%s", err, got, want)
	}
	if _, err := keyCondition(fitsync.Int(1)); err == nil {
		t.Error("a key that is no map")
	}
	if QuoteName("db.t") != "`db`.`t`" || QuoteName("t") != "`t`" {
		t.Error(QuoteName("db.t"))
	}
}

func TestToValue(t *testing.T) {
	for _, c := range []struct {
		typ, raw string
		want     fitsync.Value
	}{
		{"Int64", "-5", fitsync.Int(-5)},
		{"UInt64", "18446744073709551615", fitsync.Uint(18446744073709551615)},
		{"UInt64", `"7"`, fitsync.Uint(7)},
		{"Int128", "170141183460469231731687303715884105727", fitsync.Decimal("170141183460469231731687303715884105727")},
		{"Decimal(10, 2)", "1.5", fitsync.Decimal("1.5")},
		{"Float64", "0.25", fitsync.Float(0.25)},
		{"Bool", "true", fitsync.Bool(true)},
		{"Date", `"2026-03-04"`, fitsync.Date(2026, 3, 4)},
		{"DateTime64(3, 'UTC')", `"2026-03-04T05:06:07.123Z"`, fitsync.Time(time.Date(2026, 3, 4, 5, 6, 7, 123e6, time.UTC))},
		{"Nullable(String)", "null", fitsync.Null()},
		{"LowCardinality(Nullable(String))", `"x"`, fitsync.String("x")},
		{"FixedString(3)", `"a\u0000\u0000"`, fitsync.String("a")},
		{"Array(Nullable(Int32))", "[1,null]", fitsync.Array(fitsync.Int(1), fitsync.Null())},
		{"Map(String, UInt8)", `{"a": 1}`, fitsync.JSON([]byte(`{"a":1}`))},
	} {
		got, err := ToValue(c.typ, json.RawMessage(c.raw))
		if err != nil || !got.Equal(c.want) {
			t.Errorf("%s %s: %v %v", c.typ, c.raw, got, err)
		}
	}
	if _, err := ToValue("Int8", json.RawMessage(`"x"`)); err == nil {
		t.Error("no error for a bad number")
	}
}

func TestParseRows(t *testing.T) {
	body := `["id", "v", "__fit_0"]
["UInt64", "String", "String"]
[1, "a", "1"]
[2, "b", "2"]
`
	rows, err := parseRows([]byte(body), []string{"id"})
	if err != nil || len(rows) != 2 || len(rows[1].fields) != 2 || rows[1].at[0] != (bound{T: "UInt64", V: "2"}) {
		t.Fatalf("%v %+v", err, rows)
	}
	if _, err := parseRows([]byte(body), []string{"nope"}); err == nil {
		t.Error("no error for a missing column")
	}
}

func TestEscapeParam(t *testing.T) {
	if got := escapeParam("a\tb\\c\n"); got != `a\tb\\c\n` {
		t.Error(got)
	}
}

// load loads a module of ns with params, as a pipeline does.
func load(t *testing.T, ns, name string, params map[string]any) error {
	t.Helper()
	b, _ := json.Marshal(params)
	v, err := config.ParseValue(b, "params")
	if err != nil {
		t.Fatal(err)
	}
	m, err := module.Load(ns, config.ModuleRef{Module: name, Params: v}, nil)
	if err == nil {
		module.Cleanup(m)
	}
	return err
}

func TestProvision(t *testing.T) {
	ok := map[string]any{"urls": []string{"http://ch:8123"}}
	if err := load(t, fitsync.Sinks, "clickhouse", ok); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ns     string
		params map[string]any
		want   string
	}{
		{fitsync.Sinks, map[string]any{"urls": []string{"ftp://x"}}, "not an http"},
		{fitsync.Sinks, map[string]any{"urls": []string{"http://x"}, "plain": true}, "deletes must be delete or ignore"},
		{fitsync.Sinks, map[string]any{"urls": []string{"http://x"}, "settings": map[string]string{"param_x": "1"}}, "connector's to set"},
		{fitsync.Sinks, map[string]any{"urls": []string{"http://x"}, "table": "{nope}"}, "brace"},
		{fitsync.Sinks, map[string]any{"urls": []string{"http://x"}, "password_env": "FIT_TEST_CH_NONE"}, "not set"},
		{fitsync.Sources, map[string]any{"urls": []string{"http://x"}, "key": []string{"id"}}, "either table or query"},
		{fitsync.Sources, map[string]any{"urls": []string{"http://x"}, "table": "t", "key": []string{"id"}, "snapshot": "never"}, "needs a cursor"},
		{fitsync.Sources, map[string]any{"urls": []string{"http://x"}, "table": "t", "key": []string{"at"}, "cursor": "at"}, "the cursor"},
	} {
		if err := load(t, c.ns, "clickhouse", c.params); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.params, err, c.want)
		}
	}
}

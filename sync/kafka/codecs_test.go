package synckafka

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// sample is a message TiCDC v8.5.3 wrote, recorded by the interop test's
// setup (testdata).
type sample struct {
	Key   *string `json:"key"`
	Value string  `json:"value"`
}

func samples(t *testing.T, name string) []sample {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []sample
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var s sample
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func decodeAll(t *testing.T, c fitsync.Codec, ss []sample) []*fitsync.Record {
	t.Helper()
	var out []*fitsync.Record
	for _, s := range ss {
		if err := c.Decode([]byte(s.Value), func(r *fitsync.Record) { out = append(out, r) }); err != nil {
			t.Fatalf("%s: %v", s.Value, err)
		}
	}
	return out
}

func get(t *testing.T, row fitsync.Value, name string) fitsync.Value {
	t.Helper()
	v, ok := row.Get(name)
	if !ok {
		t.Fatalf("no %s in %v", name, row)
	}
	return v
}

func js(t *testing.T, v fitsync.Value) string {
	t.Helper()
	b, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkFirstRow checks the sample's first row, as every codec should read
// it, but for ENUM, SET and BIT, which the formats write differently.
func checkFirstRow(t *testing.T, row fitsync.Value, skip ...string) {
	t.Helper()
	want := map[string]string{
		"id": `{"int":1}`, "tiny": `{"int":-5}`, "f": `{"float":1.5}`, "d": `{"float":2.25}`,
		"vc": `"héllo"`, "txt": `"text"`, "bin": `{"bytes":"AP8Q"}`, "blb": `{"bytes":"3q2+7w=="}`,
		"dt": `{"time":"2024-02-03T04:05:06.123456Z"}`, "ts": `{"time":"2024-02-03T04:05:06.789Z"}`,
		"dte": `{"date":"2024-02-03"}`, "tm": `"-12:34:56"`, "yr": `{"int":2024}`, "nul": `null`,
	}
	for _, k := range skip {
		delete(want, k)
	}
	for k, w := range want {
		if got := js(t, get(t, row, k)); got != w {
			t.Errorf("%s is %s, want %s", k, got, w)
		}
	}
}

func TestCanalJSONTiCDC(t *testing.T) {
	c := mustLoad(t, fitsync.Codecs, "canal_json", map[string]any{}).(*CanalJSON)
	recs := decodeAll(t, c, samples(t, "ticdc-canal-json.jsonl"))
	var ops []string
	for _, r := range recs {
		ops = append(ops, r.Op.String())
	}
	if got := strings.Join(ops, ","); got != "insert,insert,delete,update,insert,delete,ddl,insert" {
		t.Fatalf("ops %s", got)
	}
	first := recs[0]
	if first.Namespace != "s.t" || js(t, first.Key) != `{"map":{"id":{"int":1}}}` || first.Tx.ID != "470326864360767496" {
		t.Fatalf("%+v", first)
	}
	if !first.CommitTime.Equal(time.UnixMilli(470326864360767496 >> 18)) {
		t.Fatalf("commit %v", first.CommitTime)
	}
	checkFirstRow(t, first.After)
	for k, w := range map[string]string{"u": `{"uint":4000000000}`, "dec1": `{"decimal":"-123.4500"}`, "bt": `{"uint":682}`,
		"en": `"2"`, "st": `"3"`, "js": `{"json":{"a":[1,2.5,"s"]}}`} {
		if got := js(t, get(t, first.After, k)); got != w {
			t.Errorf("%s is %s, want %s", k, got, w)
		}
	}
	up := recs[3]
	if js(t, get(t, up.Before, "vc")) != `"héllo"` || js(t, get(t, up.After, "vc")) != `"bye"` || js(t, get(t, up.After, "u")) != `{"uint":7}` {
		t.Fatalf("update %s -> %s", js(t, up.Before), js(t, up.After))
	}
	del := recs[2]
	if js(t, del.Key) != `{"map":{"id":{"int":2}}}` || !del.After.IsNull() || del.Before.IsNull() {
		t.Fatalf("delete %+v", del)
	}
	ddl := recs[6]
	if sql, _ := ddl.After.Get("sql"); !strings.HasPrefix(sql.Text(), "ALTER TABLE") {
		t.Fatalf("ddl %s", js(t, ddl.After))
	}

	// Encoded and read again: the same rows, keys and operations, ENUM and
	// SET aside.
	for _, r := range []*fitsync.Record{first, up, del, ddl, recs[7]} {
		b, err := c.Encode(nil, []*fitsync.Record{r})
		if err != nil {
			t.Fatal(err)
		}
		var back []*fitsync.Record
		if err := c.Decode(b, func(x *fitsync.Record) { back = append(back, x) }); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if len(back) != 1 || back[0].Op != r.Op || back[0].Namespace != r.Namespace {
			t.Fatalf("%s: %+v", b, back)
		}
		if r.Op != fitsync.OpDDL {
			if !back[0].Key.Equal(r.Key) || !back[0].After.Equal(r.After) {
				t.Fatalf("%s:\n%s\n%s", b, js(t, back[0].After), js(t, r.After))
			}
		}
	}
}

func TestCanalJSONEdges(t *testing.T) {
	c := &CanalJSON{}
	none := func(r *fitsync.Record) { t.Fatalf("a record %+v", r) }
	for _, bad := range []string{`{`, `{"type":"QUERYX"}`, `{"type":"INSERT"}`, `{"type":"INSERT","data":{}}`,
		`{"type":"INSERT","data":[1]}`, `{"type":"INSERT","data":[{"a":"1"}],"old":"x"}`, `{"type":"INSERT","data":[{"a":"1"`} {
		if err := c.Decode([]byte(bad), none); err == nil {
			t.Fatalf("%s decoded", bad)
		}
	}
	// Canal's own form: types with their lengths, a binary column as
	// ISO-8859-1, no TiDB extension, the update's old with its changed
	// columns alone, a table without a key.
	msg := `{"database":"d","table":"x","pkNames":null,"isDdl":false,"type":"UPDATE","es":1700000000000,
		"mysqlType":{"a":"int(11) unsigned","b":"varbinary(4)","c":"varchar(4)","t":"datetime(3)"},
		"data":[{"a":"5","b":"\u00ff\u0001","c":"x","t":"2024-01-02 03:04:05.5"}],"old":[{"c":"y"}]}`
	c = mustLoad(t, fitsync.Codecs, "canal_json", map[string]any{"time_zone": "Asia/Shanghai"}).(*CanalJSON)
	var got []*fitsync.Record
	if err := c.Decode([]byte(msg), func(r *fitsync.Record) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	r := got[0]
	if !r.Key.IsNull() || r.Tx.ID != "" || r.CommitTime.UnixMilli() != 1700000000000 {
		t.Fatalf("%+v", r)
	}
	if js(t, r.After) != `{"map":{"a":{"uint":5},"b":{"bytes":"/wE="},"c":"x","t":{"time":"2024-01-02T03:04:05.5+08:00"}}}` ||
		js(t, get(t, r.Before, "c")) != `"y"` || js(t, get(t, r.Before, "a")) != `{"uint":5}` {
		t.Fatalf("%s / %s", js(t, r.After), js(t, r.Before))
	}
	if _, err := load(t, fitsync.Codecs, "canal_json", map[string]any{"time_zone": "Nowhere/Never"}); err == nil {
		t.Fatal("a bad zone loaded")
	}
	// Every kind encodes; a record with no row does not.
	row := fitsync.Map(
		fitsync.Field{Name: "b", Value: fitsync.Bool(true)}, fitsync.Field{Name: "f", Value: fitsync.Bool(false)},
		fitsync.Field{Name: "n", Value: fitsync.Null()}, fitsync.Field{Name: "a", Value: fitsync.Array(fitsync.Int(1))},
		fitsync.Field{Name: "m", Value: fitsync.Map(fitsync.Field{Name: "x", Value: fitsync.String("y")})},
		fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(`{"z":1}`))}, fitsync.Field{Name: "d", Value: fitsync.Date(2024, 2, 3)},
		fitsync.Field{Name: "t", Value: fitsync.Time(time.Date(2024, 2, 3, 4, 5, 6, 0, time.UTC))})
	b, err := (&CanalJSON{}).Encode(nil, []*fitsync.Record{{Namespace: "nodot", Op: fitsync.OpMessage, After: row}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"data":[{"b":"1","f":"0","n":null,"a":"[1]","m":"{\"x\":\"y\"}","j":"{\"z\":1}","d":"2024-02-03","t":"2024-02-03 04:05:06"}]`) ||
		!strings.Contains(string(b), `"database":""`) || !strings.Contains(string(b), `"es":0`) {
		t.Fatalf("%s", b)
	}
	if _, err := (&CanalJSON{}).Encode(nil, []*fitsync.Record{{Op: fitsync.OpInsert}}); err == nil {
		t.Fatal("a record without a row encoded")
	}
	// A delete of a key alone.
	b, _ = (&CanalJSON{}).Encode(nil, []*fitsync.Record{{Namespace: "d.t", Op: fitsync.OpDelete, Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(3)})}})
	if !strings.Contains(string(b), `"type":"DELETE"`) || !strings.Contains(string(b), `"data":[{"id":"3"}]`) {
		t.Fatalf("%s", b)
	}
}

func TestSimpleTiCDC(t *testing.T) {
	c := mustLoad(t, fitsync.Codecs, "ticdc_simple", map[string]any{}).(*TiCDCSimple)
	ss := samples(t, "ticdc-simple.jsonl")
	recs := decodeAll(t, c, ss)
	var ops []string
	for _, r := range recs {
		ops = append(ops, r.Op.String())
	}
	if got := strings.Join(ops, ","); got != "insert,insert,delete,update,insert,delete,ddl,insert,ddl" && got != "insert,insert,delete,update,insert,delete,ddl,insert" {
		t.Fatalf("ops %s", got)
	}
	first := recs[0]
	checkFirstRow(t, first.After)
	for k, w := range map[string]string{"u": `{"uint":4000000000}`, "dec1": `{"decimal":"-123.4500"}`, "bt": `{"uint":682}`,
		"en": `"b"`, "st": `"x,y"`, "js": `{"json":{"a":[1,2.5,"s"]}}`} {
		if got := js(t, get(t, first.After, k)); got != w {
			t.Errorf("%s is %s, want %s", k, got, w)
		}
	}
	if first.Namespace != "s.t" || js(t, first.Key) != `{"map":{"id":{"int":1}}}` || first.Tx.ID != "470326864360767496" {
		t.Fatalf("%+v", first)
	}
	if first.After.Fields()[0].Name != "id" {
		t.Fatalf("columns out of the table's order: %s", js(t, first.After))
	}
	if js(t, recs[2].Key) != `{"map":{"id":{"int":2}}}` || !recs[2].After.IsNull() {
		t.Fatalf("delete %+v", recs[2])
	}
	last := recs[7]
	if js(t, get(t, last.After, "extra")) != `{"int":10}` {
		t.Fatalf("after the ALTER: %s", js(t, last.After))
	}

	// Without the bootstrap: text, no key; a table given by SetTable.
	fresh := &TiCDCSimple{}
	var rows []*fitsync.Record
	fresh.Decode([]byte(ss[2].Value), func(r *fitsync.Record) { rows = append(rows, r) })
	if !rows[0].Key.IsNull() || js(t, get(t, rows[0].After, "id")) != `"1"` || js(t, get(t, rows[0].After, "ts")) != `{"time":"2024-02-03T04:05:06.789Z"}` {
		t.Fatalf("untyped %s", js(t, rows[0].After))
	}
	fresh.SetTable("s.t", &SimpleTable{Key: []string{"id"}, Columns: []SimpleColumn{{Name: "id", Type: ParseMySQLType("bigint")},
		{Name: "en", Type: ParseMySQLType("enum"), Elements: []string{"a"}}, {Name: "st", Type: ParseMySQLType("set")}}})
	rows = nil
	fresh.Decode([]byte(ss[2].Value), func(r *fitsync.Record) { rows = append(rows, r) })
	if js(t, rows[0].Key) != `{"map":{"id":{"int":1}}}` || js(t, get(t, rows[0].After, "en")) != `"2"` || js(t, get(t, rows[0].After, "st")) != `""` {
		t.Fatalf("%s", js(t, rows[0].After))
	}
	if fs := rows[0].After.Fields(); fs[0].Name != "id" || len(fs) != 20 {
		t.Fatalf("columns %v", fs)
	}
	if _, err := c.Encode(nil, recs[:1]); err == nil {
		t.Fatal("encoded")
	}
	if _, err := load(t, fitsync.Codecs, "ticdc_simple", map[string]any{"time_zone": "Nowhere/Never"}); err == nil {
		t.Fatal("a bad zone loaded")
	}
	none := func(r *fitsync.Record) {}
	for _, bad := range []string{`{`, `{"type":"OTHER"}`, `{"type":"INSERT","database":"a","table":"b"}`, `{"type":"INSERT","data":[1]}`,
		`{"type":"INSERT","data":{"a":1`, `{"type":"UPDATE","data":{"a":"1"},"old":[1]}`} {
		if err := c.Decode([]byte(bad), none); err == nil {
			t.Fatalf("%s decoded", bad)
		}
	}
	// A DDL of a table not seen, named by its schema; an ENUM's 0; a
	// zone that does not load.
	var ddl []*fitsync.Record
	c.Decode([]byte(`{"type":"CREATE","sql":"CREATE TABLE x.y (a int)","tableSchema":{"schema":"x","table":"y","columns":[{"name":"e","dataType":{"mysqlType":"enum","elements":["p"]}}],"indexes":[{"unique":true,"columns":["e"]}]}}`),
		func(r *fitsync.Record) { ddl = append(ddl, r) })
	if ddl[0].Namespace != "x.y" || ddl[0].Op != fitsync.OpDDL {
		t.Fatalf("%+v", ddl[0])
	}
	var ins []*fitsync.Record
	c.Decode([]byte(`{"type":"INSERT","database":"x","table":"y","data":{"e":"0","z":{"location":"Not/AZone","value":"2024-01-01 00:00:00"},"w":{"location":"UTC","value":"bad"}}}`),
		func(r *fitsync.Record) { ins = append(ins, r) })
	if js(t, ins[0].After) != `{"map":{"e":"","z":{"time":"2024-01-01T00:00:00Z"},"w":"bad"}}` || js(t, ins[0].Key) != `{"map":{"e":""}}` {
		t.Fatalf("%s %s", js(t, ins[0].After), js(t, ins[0].Key))
	}
}

func TestDebeziumTiCDC(t *testing.T) {
	c := mustLoad(t, fitsync.Codecs, "debezium", map[string]any{}).(*Debezium)
	ss := samples(t, "ticdc-debezium.jsonl")
	recs := decodeAll(t, c, ss)
	if len(recs) != 7 {
		t.Fatalf("%d records", len(recs))
	}
	first := recs[0]
	checkFirstRow(t, first.After, "bin", "blb")
	for k, w := range map[string]string{"u": `{"int":4000000000}`, "dec1": `{"float":-123.45}`, "bt": `{"uint":682}`,
		"en": `"b"`, "st": `"x,y"`, "js": `{"json":{"a":[1,2.5,"s"]}}`} {
		if got := js(t, get(t, first.After, k)); got != w {
			t.Errorf("%s is %s, want %s", k, got, w)
		}
	}
	if first.Namespace != "s.t" || first.Tx.ID != "470326864360767496" || first.Op != fitsync.OpInsert {
		t.Fatalf("%+v", first)
	}
	if recs[3].Op != fitsync.OpUpdate || recs[2].Op != fitsync.OpDelete || recs[2].Before.IsNull() {
		t.Fatalf("%+v %+v", recs[3], recs[2])
	}
	k, err := c.DecodeKey([]byte(*ss[0].Key))
	if err != nil || js(t, k) != `{"map":{"id":{"int":1}}}` {
		t.Fatalf("%s %v", js(t, k), err)
	}
	if _, err := c.DecodeKey([]byte("{")); err == nil {
		t.Fatal("a bad key decoded")
	}

	// Without a schema, a transaction ID, a schema in the source; encoded
	// and read back.
	var got []*fitsync.Record
	ev := `{"before":null,"after":{"id":3,"v":"x"},"op":"r","ts_ms":5,"source":{"db":"d","schema":"public","table":"t"},"transaction":{"id":"tx1"}}`
	if err := c.Decode([]byte(ev), func(r *fitsync.Record) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	r := got[0]
	if r.Namespace != "public.t" || r.Tx.ID != "tx1" || r.CommitTime.UnixMilli() != 5 || js(t, r.After) != `{"map":{"id":{"int":3},"v":"x"}}` {
		t.Fatalf("%+v %s", r, js(t, r.After))
	}
	for _, op := range []fitsync.Op{fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpDelete, fitsync.OpMessage} {
		rec := &fitsync.Record{Namespace: "d.t", Op: op, After: r.After, Before: r.After, CommitTime: time.UnixMilli(9)}
		if op == fitsync.OpInsert {
			rec.Before, rec.CommitTime = fitsync.Null(), time.Time{}
		}
		b, err := c.Encode(nil, []*fitsync.Record{rec})
		if err != nil {
			t.Fatal(err)
		}
		var back []*fitsync.Record
		if err := c.Decode(b, func(x *fitsync.Record) { back = append(back, x) }); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if back[0].Namespace != "d.t" || !back[0].After.Equal(rec.After) || !back[0].Before.Equal(rec.Before) {
			t.Fatalf("%s: %+v", b, back[0])
		}
	}
	if _, err := c.Encode(nil, []*fitsync.Record{{Op: fitsync.OpDDL}}); err == nil {
		t.Fatal("a DDL encoded")
	}
	for _, bad := range []string{`{`, `[1]`, `{"op":"x"}`, `{"op":"c","after":{"a":`, `{"schema":{"fields":1},"payload":{}}`,
		`{"op":"c","before":[1,`, `{"payload":[1],"schema":null}`} {
		if err := c.Decode([]byte(bad), func(*fitsync.Record) {}); err == nil {
			t.Fatalf("%s decoded", bad)
		}
	}
}

func TestDebeziumTypes(t *testing.T) {
	f := func(name, typ string, params map[string]string) *dbzField {
		return &dbzField{Name: name, Type: typ, Parameters: params}
	}
	cases := []struct {
		v    fitsync.Value
		f    *dbzField
		want string
	}{
		{fitsync.Int(19756), f("io.debezium.time.Date", "int32", nil), `{"date":"2024-02-03"}`},
		{fitsync.Int(1000), f("io.debezium.time.Timestamp", "int64", nil), `{"time":"1970-01-01T00:00:01Z"}`},
		{fitsync.Int(1500), f("io.debezium.time.NanoTimestamp", "int64", nil), `{"time":"1970-01-01T00:00:00.0000015Z"}`},
		{fitsync.String("bad"), f("io.debezium.time.ZonedTimestamp", "string", nil), `"bad"`},
		{fitsync.Int(3_600_000), f("io.debezium.time.Time", "int32", nil), `"01:00:00"`},
		{fitsync.Int(1_500), f("io.debezium.time.NanoTime", "int64", nil), `"00:00:00.000001"`},
		{fitsync.String("AeI="), f("org.apache.kafka.connect.data.Decimal", "bytes", map[string]string{"scale": "2"}), `{"decimal":"4.82"}`},
		{fitsync.String("/g=="), f("org.apache.kafka.connect.data.Decimal", "bytes", map[string]string{"scale": "3"}), `{"decimal":"-0.002"}`},
		{fitsync.String("Bw=="), f("org.apache.kafka.connect.data.Decimal", "bytes", nil), `{"decimal":"7"}`},
		{fitsync.String("!"), f("org.apache.kafka.connect.data.Decimal", "bytes", nil), `"!"`},
		{fitsync.String("!"), f("io.debezium.data.Bits", "bytes", nil), `"!"`},
		{fitsync.String("!"), f("", "bytes", nil), `"!"`},
		{fitsync.Null(), f("io.debezium.time.Date", "int32", nil), `null`},
	}
	for _, c := range cases {
		if got := js(t, byField(c.v, c.f)); got != c.want {
			t.Errorf("%s %s: %s, want %s", js(t, c.v), c.f.Name, got, c.want)
		}
	}
}

func TestRawAndTypes(t *testing.T) {
	c := mustLoad(t, fitsync.Codecs, "raw", map[string]any{}).(*Raw)
	var got []*fitsync.Record
	c.Decode([]byte{0, 0xff}, func(r *fitsync.Record) { got = append(got, r) })
	b, _ := c.Encode(nil, got)
	if string(b) != "\x00\xff" {
		t.Fatalf("%q", b)
	}
	c2 := &Raw{Field: "msg"}
	b, err := c2.Encode(nil, []*fitsync.Record{
		{Op: fitsync.OpDelete, Before: fitsync.Map(fitsync.Field{Name: "msg", Value: fitsync.String("s")})},
		{After: fitsync.Map(fitsync.Field{Name: "msg", Value: fitsync.Int(5)})},
		{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "x", Value: fitsync.Int(5)})}})
	if err != nil || string(b) != `s5{"op":"insert","after":{"map":{"x":{"int":5}}}}` {
		t.Fatalf("%s %v", b, err)
	}
	if _, err := c2.Encode(nil, []*fitsync.Record{{Op: fitsync.OpInsert, After: fitsync.Map(fitsync.Field{Name: "x", Value: fitsync.JSON([]byte("{bad"))})}}); err == nil {
		t.Fatal("bad JSON encoded")
	}
	for _, c := range []struct{ typ, s, want string }{
		{"int", "x", `"x"`}, {"bigint unsigned", "x", `"x"`}, {"year", "x", `"x"`}, {"bit(3)", "x", `"x"`},
		{"double", "x", `"x"`}, {"decimal(4,2)", "x", `"x"`}, {"date", "x", `"x"`}, {"timestamp", "x", `"x"`},
		{"geometry", "x", `{"bytes":"eA=="}`}, {"enum('a')", "a", `"a"`},
	} {
		if got := js(t, MySQLText(ParseMySQLType(c.typ), c.s, []byte(c.s), nil)); got != c.want {
			t.Errorf("%s %q: %s, want %s", c.typ, c.s, got, c.want)
		}
	}
	if string(unbase64("!x")) != "!x" || toLatin1([]byte{0xe9}) != "é" || string(latin1("é")) != "\xe9" {
		t.Fatal("helpers")
	}
	if a, b := splitNamespace("t"); a != "" || b != "t" {
		t.Fatal(a, b)
	}
	if !keyOf(fitsync.Map(), []string{"a"}).IsNull() {
		t.Fatal("a key of a missing column")
	}
}

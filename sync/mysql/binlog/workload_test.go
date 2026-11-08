package binlog_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
)

// workload is testdata/workload.json: what TestMySQLBinlogInterop runs on
// each server, and the rows its binlog decodes to.
type workload struct {
	Tables []struct {
		Name    string
		Create  string
		Columns []struct {
			Name     string
			Unsigned bool
			Binary   bool
			Enum     []string
			Set      []string
		}
	}
	Steps []struct {
		SQL  string
		Only []string
	}
	Want []wantRow
}

type wantRow struct {
	Op     string
	Table  string
	Before map[string][]string
	After  map[string][]string
	Only   []string
}

func loadWorkload(t *testing.T) *workload {
	t.Helper()
	b, err := os.ReadFile("testdata/workload.json")
	if err != nil {
		t.Fatal(err)
	}
	var w workload
	if err := json.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return &w
}

// applies tells whether a step or a want of only applies to server.
func applies(only []string, server string) bool {
	if len(only) == 0 {
		return true
	}
	for _, p := range only {
		if strings.HasPrefix(server, p) {
			return true
		}
	}
	return false
}

// workloadDB is the database the workload runs in.
const workloadDB = "fit_binlog"

// gotRow is a row decoded, with the names of its table's columns.
type gotRow struct {
	op, table     string
	names         []string
	before, after []fitsync.Value
}

// decoded is what a binlog file of the workload decoded to.
type decoded struct {
	events []*binlog.Event
	rows   []gotRow
}

// decodeFile decodes a binlog file of the workload on server, completing
// each table map as a source would from information_schema, after checking
// the optional metadata it has agrees.
func (w *workload) decodeFile(t *testing.T, server string, file []byte) *decoded {
	t.Helper()
	r, err := binlog.NewReader(bytes.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	out := &decoded{}
	r.OnTableMap = func(tm *binlog.TableMap) {
		if tm.Schema == workloadDB {
			w.complete(t, server, tm)
		}
	}
	var walk func(ev *binlog.Event)
	walk = func(ev *binlog.Event) {
		out.events = append(out.events, ev)
		switch b := ev.Body.(type) {
		case *binlog.Payload:
			for _, e := range b.Events {
				walk(e)
			}
		case *binlog.Rows:
			// Other tests may write to the server meanwhile (go test runs
			// packages in parallel): only the workload's database counts.
			if b.Table == nil || b.Table.Schema != workloadDB {
				return
			}
			names := make([]string, len(b.Table.Columns))
			for i, c := range b.Table.Columns {
				names[i] = c.Name
			}
			for _, row := range b.Rows {
				out.rows = append(out.rows, gotRow{op: b.Kind.String(), table: b.Table.Table, names: names,
					before: row.Before, after: row.After})
			}
		}
	}
	for {
		ev, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%s: %v", server, err)
		}
		walk(ev)
	}
	return out
}

func (w *workload) complete(t *testing.T, server string, tm *binlog.TableMap) {
	t.Helper()
	for _, spec := range w.Tables {
		if spec.Name != tm.Table {
			continue
		}
		for i := range min(len(spec.Columns), len(tm.Columns)) {
			s, c := spec.Columns[i], &tm.Columns[i]
			where := fmt.Sprintf("%s: %s.%s column %d", server, tm.Table, s.Name, i)
			if tm.Metadata&(1<<binlog.MetaColumnName) != 0 && c.Name != s.Name {
				t.Errorf("%s: name %q", where, c.Name)
			}
			if tm.Metadata&(1<<binlog.MetaSignedness) != 0 && c.Unsigned != s.Unsigned {
				t.Errorf("%s: unsigned %v", where, c.Unsigned)
			}
			if c.Charset != 0 && (c.Charset == binlog.CharsetBinary) != s.Binary {
				t.Errorf("%s: charset %d", where, c.Charset)
			}
			if c.EnumValues != nil && !reflect.DeepEqual(c.EnumValues, s.Enum) {
				t.Errorf("%s: enum %q", where, c.EnumValues)
			}
			if c.SetValues != nil && !reflect.DeepEqual(c.SetValues, s.Set) {
				t.Errorf("%s: set %q", where, c.SetValues)
			}
			c.Name, c.Unsigned, c.EnumValues, c.SetValues = s.Name, s.Unsigned, s.Enum, s.Set
			if c.Charset == 0 {
				c.Charset = 45 // utf8mb4_general_ci
				if s.Binary {
					c.Charset = binlog.CharsetBinary
				}
			}
		}
	}
}

// check compares the rows decoded with the workload's.
func (w *workload) check(t *testing.T, server string, got []gotRow) {
	t.Helper()
	var want []wantRow
	for _, r := range w.Want {
		if applies(r.Only, server) {
			want = append(want, r)
		}
	}
	for i := range max(len(want), len(got)) {
		if i >= len(want) {
			t.Errorf("%s: row %d not expected: %s %s %v %v", server, i, got[i].op, got[i].table, got[i].before, got[i].after)
			continue
		}
		if i >= len(got) {
			t.Errorf("%s: row %d missing: %+v", server, i, want[i])
			continue
		}
		g, wr := got[i], want[i]
		if g.op != wr.Op || g.table != wr.Table {
			t.Errorf("%s: row %d is %s %s, want %s %s", server, i, g.op, g.table, wr.Op, wr.Table)
			continue
		}
		checkImage(t, fmt.Sprintf("%s: row %d (%s %s) before", server, i, g.op, g.table), g.names, g.before, wr.Before)
		checkImage(t, fmt.Sprintf("%s: row %d (%s %s) after", server, i, g.op, g.table), g.names, g.after, wr.After)
	}
}

func checkImage(t *testing.T, where string, names []string, got []fitsync.Value, want map[string][]string) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s: an image not expected", where)
		}
		return
	}
	for i, v := range got {
		wv, ok := want[names[i]]
		if !ok {
			wv = []string{"null"}
		}
		if err := sameValue(v, wv); err != nil {
			t.Errorf("%s: %s: %v", where, names[i], err)
		}
	}
	for n := range want {
		found := false
		for _, m := range names {
			found = found || m == n
		}
		if !found {
			t.Errorf("%s: no column %s", where, n)
		}
	}
}

func sameValue(v fitsync.Value, w []string) error {
	kind, text := w[0], ""
	if len(w) > 1 {
		text = w[1]
	}
	var ok bool
	switch kind {
	case "null":
		ok = v.IsNull()
	case "int":
		ok = v.Kind() == fitsync.KindInt && strconv.FormatInt(v.AsInt(), 10) == text
	case "uint":
		ok = v.Kind() == fitsync.KindUint && strconv.FormatUint(v.AsUint(), 10) == text
	case "float":
		f, _ := strconv.ParseFloat(text, 64)
		ok = v.Kind() == fitsync.KindFloat && v.AsFloat() == f
	case "decimal":
		ok = v.Kind() == fitsync.KindDecimal && v.Text() == text
	case "string":
		ok = v.Kind() == fitsync.KindString && v.Text() == text
	case "repeat":
		s, n, _ := strings.Cut(text, "|")
		k, _ := strconv.Atoi(n)
		ok = v.Kind() == fitsync.KindString && v.Text() == strings.Repeat(s, k)
	case "stringhex":
		ok = v.Kind() == fitsync.KindString && hex.EncodeToString([]byte(v.Text())) == text
	case "hex":
		ok = v.Kind() == fitsync.KindBytes && hex.EncodeToString([]byte(v.Text())) == text
	case "date":
		ok = v.Kind() == fitsync.KindDate && v.AsTime().Format(time.DateOnly) == text
	case "time":
		tm, err := time.Parse(time.RFC3339Nano, text)
		ok = err == nil && v.Kind() == fitsync.KindTime && v.AsTime().Equal(tm)
	case "json":
		ok = v.Kind() == fitsync.KindJSON && sameJSON(v.Text(), text)
	case "json-string":
		ok = v.Kind() == fitsync.KindString && sameJSON(v.Text(), text)
	default:
		return fmt.Errorf("unknown kind %q", kind)
	}
	if !ok {
		return fmt.Errorf("%s %q, want %s %q", v.Kind(), valueText(v), kind, text)
	}
	return nil
}

func valueText(v fitsync.Value) string {
	switch v.Kind() {
	case fitsync.KindInt:
		return strconv.FormatInt(v.AsInt(), 10)
	case fitsync.KindUint:
		return strconv.FormatUint(v.AsUint(), 10)
	case fitsync.KindFloat:
		return strconv.FormatFloat(v.AsFloat(), 'g', -1, 64)
	case fitsync.KindTime, fitsync.KindDate:
		return v.AsTime().Format(time.RFC3339Nano)
	case fitsync.KindBytes:
		return hex.EncodeToString([]byte(v.Text()))
	}
	return v.Text()
}

func sameJSON(a, b string) bool {
	parse := func(s string) (any, error) {
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		var v any
		err := d.Decode(&v)
		return v, err
	}
	x, err1 := parse(a)
	y, err2 := parse(b)
	return err1 == nil && err2 == nil && reflect.DeepEqual(x, y)
}

// checkEvents checks the events of a workload's binlog besides its rows:
// those each server must have written.
func checkEvents(t *testing.T, server string, events []*binlog.Event) {
	t.Helper()
	seen := map[binlog.EventType]int{}
	var queries, rowsQueries []string
	tagged := false
	for _, ev := range events {
		seen[ev.Type]++
		switch b := ev.Body.(type) {
		case *binlog.Query:
			queries = append(queries, b.SQL)
		case *binlog.RowsQuery:
			rowsQueries = append(rowsQueries, b.SQL)
		case *binlog.GTID:
			tagged = tagged || b.Tag == "fit"
		}
	}
	if seen[binlog.FormatDescriptionEvent] != 1 || seen[binlog.RotateEvent] != 1 || seen[binlog.TableMapEvent] == 0 {
		t.Errorf("%s: events %v", server, seen)
	}
	if !strings.Contains(strings.Join(queries, "\n"), "ALTER TABLE num ADD COLUMN extra INT") {
		t.Errorf("%s: no ALTER TABLE among the queries %q", server, queries)
	}
	if !strings.Contains(strings.Join(rowsQueries, "\n"), "DELETE FROM num WHERE id = 3") {
		t.Errorf("%s: no DELETE among the rows queries %q", server, rowsQueries)
	}
	mysql := strings.HasPrefix(server, "mysql")
	if mysql && (seen[binlog.GTIDEvent] == 0 || seen[binlog.PreviousGTIDsEvent] != 1 || seen[binlog.XIDEvent] == 0) {
		t.Errorf("%s: GTID events %v", server, seen)
	}
	if strings.HasPrefix(server, "mysql8") && seen[binlog.PartialUpdateRowsEvent] == 0 {
		t.Errorf("%s: no partial update events %v", server, seen)
	}
	if server == "mysql80" && seen[binlog.TransactionPayload] == 0 {
		t.Errorf("%s: no transaction payload %v", server, seen)
	}
	if server == "mysql84" && !tagged {
		t.Errorf("%s: no tagged GTID", server)
	}
	if !mysql && (seen[binlog.MariaGTIDEvent] == 0 || seen[binlog.MariaGTIDListEvent] != 1 ||
		seen[binlog.MariaWriteRowsCompressedV1] == 0 || seen[binlog.MariaUpdateRowsCompressedV1] == 0) {
		t.Errorf("%s: MariaDB's events %v", server, seen)
	}
}

// The servers whose binlogs testdata has, captured by
// TestMySQLBinlogInterop with FIT_SYNC_MYSQL_BINLOG_TESTDATA=testdata.
var capturedServers = []string{"mysql57", "mysql80", "mysql84", "mariadb1011", "mariadb11"}

func TestCapturedBinlogs(t *testing.T) {
	w := loadWorkload(t)
	for _, s := range capturedServers {
		t.Run(s, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + s + ".binlog")
			if err != nil {
				t.Fatal(err)
			}
			d := w.decodeFile(t, s, b)
			checkEvents(t, s, d.events)
			w.check(t, s, d.rows)
		})
	}
}

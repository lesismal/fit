// Package syncdoris is sync's Apache Doris and StarRocks: the sink doris,
// which loads records with Stream Load over HTTP, on fib's asynchronous
// client, each run of a batch's records for one table a load whose label
// comes from the run's positions, so that a run loaded again is refused as
// already loaded; and the source doris, which reads a table or a query by
// its key and then polls it by a cursor column, over MySQL's protocol
// (sync/mysql/wire), as the clickhouse source does. One connector serves
// both systems, its flavor naming the delete column and the partial
// update's header, the only parts that differ. See docs/sync.md.
package syncdoris

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	nametmpl "github.com/lesismal/fit/sync/internal/tmpl"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".doris", New: func() any { return new(Sink) },
		Doc: "Loads records into Apache Doris (2.1 and later) or StarRocks (3 and later), as flavor says, with Stream Load over HTTP: each run of a batch's records for one table is a load of JSON (or CSV) rows whose label is made from the run's first and last records' origins (Kafka offsets, Pulsar message IDs, a CDC transaction and place) or positions, and the lineage of the pipeline's checkpoint, so a run loaded again after a crash is refused as already loaded, and one a reset of the checkpoint reads again is loaded again. Inserts and updates are rows, upserts on a table of the unique or primary key model; a delete is the row (or its key) with Doris's __DORIS_DELETE_SIGN__ or StarRocks's __op set to 1. The frontend's redirect to a backend is followed. A load refused for its data is split until the record it cannot take is found, which fails, on_error taking it, those before it loaded."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 16 << 20
	DefaultLabelPrefix     = "fit"
)

// The delete columns of the flavors.
const (
	dorisDeleteSign = "__DORIS_DELETE_SIGN__"
	starRocksOp     = "__op"
)

// Sink is the sink doris.
type Sink struct {
	URLs            []string          `json:"urls" fit:"required" doc:"The frontends' HTTP addresses (their http_port), as http://fe1:8030, or backends' (http://be1:8040) to load into directly: the one that answered last is used, and the next in turn when one fails to (a connection's error, a timeout, a 502, 503 or 504). A frontend's redirect to a backend is followed, with the credentials, but not from https to http; the backend's address must be one fit reaches."`
	Username        string            `json:"username" fit:"required" doc:"The user to load as, which needs LOAD on the tables (INSERT on StarRocks), sent with basic authentication."`
	PasswordEnv     string            `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads; none when left out."`
	CAFile          string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against over https, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	CertFile        string            `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile         string            `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string            `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; each URL's host when left out."`
	Timeout         config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one load may take, from connecting to the end of its answer. 60s when left out or 0."`
	Flavor          string            `json:"flavor,omitempty" enum:"doris,starrocks" default:"\"doris\"" doc:"The system loaded into: doris, whose deletes set __DORIS_DELETE_SIGN__ and whose partial updates are partial_columns; starrocks, whose deletes set __op and whose partial updates are partial_update. doris when left out."`
	Database        string            `json:"database,omitempty" doc:"The database of a table named without one. Needed unless every table is named as db.table."`
	Table           string            `json:"table,omitempty" default:"\"{namespace}\"" doc:"The table a record goes to: text with {namespace} (or {$namespace}, as record templates write it), the record's namespace; db.table names the database too. The table is not made: it is the user's, of the unique key model (merge on write) on Doris or the primary key model on StarRocks for updates and deletes. {namespace} when left out."`
	Format          string            `json:"format,omitempty" enum:"json,csv" default:"\"json\"" doc:"How rows are sent: json, a JSON object of each row's columns; csv, comma-separated values, quoted with escapes, null as \\N (Doris 2.0 and StarRocks 3.0 or later). Either way the load names its columns, the union of the run's rows', so a row without a column of the union loads it null. json when left out."`
	Deletes         string            `json:"deletes,omitempty" enum:"sign,ignore" default:"\"sign\"" doc:"How deletes are written: sign, as the row before (or the key, when the source has no row before) with the flavor's delete column set to 1, which a table of the unique or primary key model takes as deleting the key; ignore, not at all, for a table of another model. A run without deletes sends no delete column. sign when left out."`
	PartialColumns  bool              `json:"partial_columns,omitempty" doc:"Update only the columns a run's rows have, keeping the others as they are, rather than replacing the row: Doris's partial_columns, StarRocks's partial_update. Needs a unique key table with merge on write, or a primary key table. false when left out."`
	LabelPrefix     string            `json:"label_prefix,omitempty" default:"\"fit\"" doc:"The start of each load's label, letters, digits, _ and -, before a hash of the table, the run's positions and the lineage of the pipeline's checkpoint, which a reset changes, so that records a reset reads again are loaded again; pipelines loading the same database need prefixes of their own only when their sources' positions could match. fit when left out."`
	TimeZone        string            `json:"time_zone,omitempty" doc:"The zone times are written in, as DATETIME text: UTC when left out, local, or an IANA name such as Asia/Shanghai."`
	Properties      map[string]string `json:"properties,omitempty" doc:"Stream Load's other headers, sent with every load, as {\"max_filter_ratio\": \"0.1\", \"strict_mode\": \"true\", \"timeout\": \"600\"}; they cannot be the ones the sink sets: label, format, columns, the JSON and CSV ones, partial_columns, partial_update, hidden_columns, merge_type, delete, jsonpaths, Expect."`
	MaxRequestBytes int64             `json:"max_request_bytes,omitempty" min:"0" default:"16777216" doc:"The most bytes of one load's rows; a batch larger is loaded in several, in order, each with a label of its own, and a row larger fails its record. 16 MiB when left out or 0."`

	client *fibrest.Client
	table  []tablePart
	zone   *time.Location
	sign   string
}

type tablePart struct {
	text      string
	namespace bool
}

var labelPrefix = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// reservedProps are the Stream Load headers the sink sets itself.
var reservedProps = map[string]bool{
	"label": true, "format": true, "columns": true, "read_json_by_line": true, "strip_outer_array": true,
	"column_separator": true, "line_delimiter": true, "enclose": true, "escape": true, "trim_double_quotes": true,
	"partial_columns": true, "partial_update": true, "hidden_columns": true, "merge_type": true, "delete": true,
	"jsonpaths": true, "json_root": true, "expect": true,
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	for k := range s.Properties {
		if reservedProps[strings.ToLower(k)] {
			errs = append(errs, ctx.Params.Field("properties").Errorf("%s is the sink's to set", k))
		}
	}
	client, cerrs := fibrest.Connect(ctx, fibrest.Connection{URLs: s.URLs, Username: s.Username, PasswordEnv: s.PasswordEnv,
		CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout,
		Headers: s.Properties, HeaderField: "properties"})
	s.client = client
	errs = append(errs, cerrs...)
	if s.Username == "" {
		errs = append(errs, ctx.Params.Field("username").Errorf("no username"))
	}
	if s.Flavor == "" {
		s.Flavor = "doris"
	}
	s.sign = dorisDeleteSign
	if s.Flavor == "starrocks" {
		s.sign = starRocksOp
	}
	if s.Format == "" {
		s.Format = "json"
	}
	if s.Deletes == "" {
		s.Deletes = "sign"
	}
	if s.LabelPrefix == "" {
		s.LabelPrefix = DefaultLabelPrefix
	}
	if !labelPrefix.MatchString(s.LabelPrefix) {
		errs = append(errs, ctx.Params.Field("label_prefix").Errorf("%q is not 1 to 64 letters, digits, _ and -", s.LabelPrefix))
	}
	tmpl := nametmpl.Canonical(s.Table)
	if tmpl == "" {
		tmpl = "{namespace}"
	}
	rest := tmpl
	for rest != "" {
		i := strings.Index(rest, "{namespace}")
		if i < 0 {
			s.table = append(s.table, tablePart{text: rest})
			break
		}
		s.table = append(s.table, tablePart{text: rest[:i]}, tablePart{namespace: true})
		rest = rest[i+len("{namespace}"):]
	}
	if strings.ContainsAny(strings.ReplaceAll(tmpl, "{namespace}", ""), "{}/") {
		errs = append(errs, ctx.Params.Field("table").Errorf("%q has a brace other than {namespace}'s (or {$namespace}'s), or a slash", s.Table))
	}
	var err error
	s.zone = time.UTC
	if s.TimeZone != "" {
		if s.zone, err = config.LoadTimeZone(s.TimeZone); err != nil {
			errs = append(errs, ctx.Params.Field("time_zone").Errorf("%v", err))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Test asks a frontend's /api/health, which answers without credentials:
// the server is there and speaks HTTP; whether the user may load into the
// tables is not known before the first load.
func (s *Sink) Test(ctx context.Context) error {
	resp, err := s.client.Do(ctx, fibrest.Request{Method: stdhttp.MethodGet, Path: "/api/health"})
	if err != nil {
		return err
	}
	if resp.Status != stdhttp.StatusOK {
		return fibrest.StatusError("the health check", resp)
	}
	return nil
}

// Cleanup closes the client.
func (s *Sink) Cleanup() {
	if s.client != nil {
		s.client.Close()
	}
}

func (s *Sink) maxRequest() int64 {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return s.MaxRequestBytes
}

// tableOf returns r's table, as db and name.
func (s *Sink) tableOf(r *fitsync.Record) (string, string, error) {
	var b strings.Builder
	for _, p := range s.table {
		if p.namespace {
			b.WriteString(r.Namespace)
		} else {
			b.WriteString(p.text)
		}
	}
	name := b.String()
	db, table, ok := strings.Cut(name, ".")
	if !ok {
		db, table = s.Database, name
	}
	switch {
	case table == "" || strings.ContainsAny(table, "/?#"):
		return "", "", fmt.Errorf("%q is no table's name", name)
	case db == "" || strings.ContainsAny(db, "/?#"):
		return "", "", fmt.Errorf("the table %q has no database, and the sink's database is not set", name)
	}
	return db, table, nil
}

// item is a record as the sink writes it.
type item struct {
	db, table string
	fields    []fitsync.Field
	deleted   bool
	size      int
	skip      bool
	err       error
}

// build makes r's item.
func (s *Sink) build(r *fitsync.Record) item {
	var it item
	switch r.Op {
	case fitsync.OpDDL:
		return item{skip: true}
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage:
	case fitsync.OpDelete:
		if s.Deletes == "ignore" {
			return item{skip: true}
		}
		it.deleted = true
	default:
		return item{err: fitsync.Permanent(fmt.Errorf("a record of operation %s", r.Op))}
	}
	var err error
	if it.db, it.table, err = s.tableOf(r); err != nil {
		return item{err: fitsync.Permanent(err)}
	}
	row := r.After
	if it.deleted {
		row = r.Before
		if row.IsNull() {
			row = r.Key
		}
	}
	if row.Kind() != fitsync.KindMap {
		return item{err: fitsync.Permanent(fmt.Errorf("a %s's row is a %s, not a map of columns", r.Op, row.Kind()))}
	}
	for _, f := range row.Fields() {
		if f.Name == "" || strings.ContainsAny(f.Name, "`,\r\n") {
			return item{err: fitsync.Permanent(fmt.Errorf("the column %q cannot be named in a load", f.Name))}
		}
		if f.Name == s.sign {
			continue
		}
		it.fields = append(it.fields, f)
		it.size += len(f.Name) + 8 + len(fitsync.TextOf(f.Value))
	}
	if int64(it.size) > s.maxRequest() {
		return item{err: fitsync.Permanent(fmt.Errorf("the row is about %d bytes, more than max_request_bytes", it.size))}
	}
	return it
}

// Write loads the batch in order, each run of records for one table a load
// with a label from its positions. It returns nil once every record is
// loaded, or a BatchError at the first one that is not: those before it
// are loaded.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	items := make([]item, len(batch))
	for i, r := range batch {
		items[i] = s.build(r)
	}
	limit := s.maxRequest()
	for i := 0; i < len(items); {
		it := items[i]
		switch {
		case it.err != nil:
			return &fitsync.BatchError{Index: i, Err: it.err}
		case it.skip:
			i++
			continue
		}
		j, size := i, int64(0)
		for j < len(items) && items[j].err == nil && !items[j].skip && items[j].db == it.db && items[j].table == it.table {
			if j > i && size+int64(items[j].size) > limit {
				break
			}
			size += int64(items[j].size)
			j++
		}
		if bad, err := s.loadRun(ctx, batch[i:j], items[i:j]); err != nil {
			return &fitsync.BatchError{Index: i + bad, Err: err}
		}
		i = j
	}
	return nil
}

// loadRun loads a run; a load refused for its data is split in two, each
// half loaded, until the one record it cannot take is found. It returns
// the index, in the run, of the record that failed, those before it
// loaded.
func (s *Sink) loadRun(ctx context.Context, recs []*fitsync.Record, items []item) (int, error) {
	err := s.load(ctx, recs, items)
	if err == nil {
		return 0, nil
	}
	var le *LoadError
	if !errors.As(err, &le) || !le.DataError() {
		return 0, err
	}
	if len(items) == 1 {
		return 0, fitsync.Permanent(err)
	}
	h := len(items) / 2
	if bad, err := s.loadRun(ctx, recs[:h], items[:h]); err != nil {
		return bad, err
	}
	bad, err := s.loadRun(ctx, recs[h:], items[h:])
	if err != nil {
		return h + bad, err
	}
	return 0, nil
}

// body returns the run's rows as the load sends them, and the columns it
// names.
func (s *Sink) body(items []item) ([]byte, []string, error) {
	var cols []string
	seen := map[string]bool{}
	deletes := false
	for _, it := range items {
		deletes = deletes || it.deleted
		for _, f := range it.fields {
			if !seen[f.Name] {
				seen[f.Name] = true
				cols = append(cols, f.Name)
			}
		}
	}
	if deletes {
		cols = append(cols, s.sign)
	}
	var b []byte
	var err error
	switch {
	case s.Format == "csv":
		null := `\N`
		if s.Flavor == "starrocks" {
			null = `\\N`
		}
		for _, it := range items {
			for k, c := range cols {
				if k > 0 {
					b = append(b, ',')
				}
				switch {
				case c == s.sign:
					b = append(b, signOf(it.deleted)...)
				default:
					v, _ := fitsync.Map(it.fields...).Get(c)
					if b, err = appendCSV(b, v, s.zone, null); err != nil {
						return nil, nil, fmt.Errorf("the column %s: %w", c, err)
					}
				}
			}
			b = append(b, '\n')
		}
	default:
		// Doris reads a row a line; StarRocks an array, its outer brackets
		// stripped.
		if s.Flavor == "starrocks" {
			b = append(b, '[')
		}
		for n, it := range items {
			if n > 0 && s.Flavor == "starrocks" {
				b = append(b, ',')
			}
			b = append(b, '{')
			for k, f := range it.fields {
				if k > 0 {
					b = append(b, ',')
				}
				b = append(fitsync.AppendJSONString(b, f.Name), ':')
				if b, err = appendJSON(b, f.Value, s.zone); err != nil {
					return nil, nil, fmt.Errorf("the column %s: %w", f.Name, err)
				}
			}
			if deletes {
				if len(it.fields) > 0 {
					b = append(b, ',')
				}
				b = append(fitsync.AppendJSONString(b, s.sign), ':')
				b = append(b, signOf(it.deleted)...)
			}
			b = append(b, '}')
			if s.Flavor != "starrocks" {
				b = append(b, '\n')
			}
		}
		if s.Flavor == "starrocks" {
			b = append(b, ']')
		}
	}
	return b, cols, nil
}

func signOf(deleted bool) string {
	if deleted {
		return "1"
	}
	return "0"
}

// LoadResult is Stream Load's answer.
type LoadResult struct {
	TxnID              int64  `json:"TxnId"`
	Label              string `json:"Label"`
	Status             string `json:"Status"`
	ExistingJobStatus  string `json:"ExistingJobStatus"`
	Message            string `json:"Message"`
	Msg                string `json:"msg"`
	NumberTotalRows    int64  `json:"NumberTotalRows"`
	NumberLoadedRows   int64  `json:"NumberLoadedRows"`
	NumberFilteredRows int64  `json:"NumberFilteredRows"`
	ErrorURL           string `json:"ErrorURL"`
}

// LoadError is a load that failed.
type LoadError struct {
	Label  string
	Result LoadResult
	// Running says a load of the label is under way: sent again later, it
	// is refused as already loaded once that one finishes.
	Running bool
}

func (e *LoadError) Error() string {
	msg := e.Result.Message
	if msg == "" {
		msg = e.Result.Msg
	}
	if e.Running {
		return fmt.Sprintf("the load %s is still running (%s): it is asked again", e.Label, e.Result.ExistingJobStatus)
	}
	s := fmt.Sprintf("the load %s failed: %s: %s", e.Label, e.Result.Status, fibrest.Clip([]byte(msg)))
	if e.Result.NumberFilteredRows > 0 {
		s += fmt.Sprintf(" (%d of %d rows filtered)", e.Result.NumberFilteredRows, e.Result.NumberTotalRows)
	}
	if e.Result.ErrorURL != "" {
		s += "; the rows' errors are at " + e.Result.ErrorURL
	}
	return s
}

// DataError reports whether the load failed for the rows it was sent, as
// a value a column cannot take, which sending them again cannot cure.
func (e *LoadError) DataError() bool {
	if e.Running {
		return false
	}
	m := strings.ToLower(e.Result.Message + " " + e.Result.Msg)
	return e.Result.NumberFilteredRows > 0 || strings.Contains(m, "too many filtered rows") || strings.Contains(m, "data_quality") ||
		strings.Contains(m, "data quality") || strings.Contains(m, "parse") && strings.Contains(m, "json")
}

// Label returns the label of a load of recs into db.table: the prefix, and
// a hash of the table, the flavor, the first and the last record's
// origins (Record.AppendOrigin) or positions, the count and the lineage of
// the pipeline's checkpoint (Record.Lineage), so that the same records
// loaded again, after a crash or a retry, have the same label and the
// server refuses them as already loaded, while those a reset of the
// checkpoint reads again, within label_keep_max_second, are loaded again.
// A record without an origin or a position is placed by the nearest one
// with a position. None when no record has either.
func Label(prefix, db, table string, recs []*fitsync.Record) string {
	first, last := anchor(recs, 0), anchor(recs, len(recs)-1)
	if first == "" || last == "" {
		return ""
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d", db, table, first, last, len(recs))
	// A checkpoint saved before lineages has none, and its labels stay
	// as they were.
	if l := recs[0].Lineage(); l != "" {
		fmt.Fprintf(h, "\x00%s", l)
	}
	return prefix + "_" + hex.EncodeToString(h.Sum(nil)[:20])
}

func anchor(recs []*fitsync.Record, i int) string {
	if o := recs[i].AppendOrigin(nil); len(o) > 0 {
		// A Kafka or Pulsar source's position differs after a restart;
		// a record's origin does not.
		return "o" + base64.RawStdEncoding.EncodeToString(o)
	}
	for k := i; k >= 0; k-- {
		if len(recs[k].Position) > 0 {
			return base64.RawStdEncoding.EncodeToString(recs[k].Position) + "+" + strconv.Itoa(i-k)
		}
	}
	for k := i + 1; k < len(recs); k++ {
		if len(recs[k].Position) > 0 {
			return base64.RawStdEncoding.EncodeToString(recs[k].Position) + "-" + strconv.Itoa(k-i)
		}
	}
	return ""
}

// load sends one Stream Load of a run.
func (s *Sink) load(ctx context.Context, recs []*fitsync.Record, items []item) error {
	body, cols, err := s.body(items)
	if err != nil {
		return fitsync.Permanent(err)
	}
	db, table := items[0].db, items[0].table
	h := stdhttp.Header{}
	h.Set("Expect", "100-continue")
	label := Label(s.LabelPrefix, db, table, recs)
	if label != "" {
		h.Set("label", label)
	}
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = quoteIdent(c)
	}
	h.Set("columns", strings.Join(q, ","))
	switch s.Format {
	case "csv":
		h.Set("format", "csv")
		h.Set("column_separator", ",")
		h.Set("enclose", `"`)
		h.Set("escape", `\`)
		if s.Flavor != "starrocks" {
			// Doris splits on the enclosing quotes, and keeps them unless
			// told.
			h.Set("trim_double_quotes", "true")
		}
		h.Set("Content-Type", "text/plain; charset=UTF-8")
	default:
		h.Set("format", "json")
		h.Set("Content-Type", "application/json")
		if s.Flavor == "starrocks" {
			h.Set("strip_outer_array", "true")
		} else {
			h.Set("read_json_by_line", "true")
		}
	}
	if s.PartialColumns {
		if s.Flavor == "starrocks" {
			h.Set("partial_update", "true")
		} else {
			h.Set("partial_columns", "true")
		}
	}
	path := "/api/" + url.PathEscape(db) + "/" + url.PathEscape(table) + "/_stream_load"
	resp, err := s.client.DoRetry(ctx, fibrest.Request{Method: stdhttp.MethodPut, Path: path, Header: h, Body: body, Redirects: fibrest.MaxRedirects}, 0)
	if err != nil {
		return err
	}
	if resp.Status != stdhttp.StatusOK {
		return fibrest.StatusError("the load "+label, resp)
	}
	var res LoadResult
	if err := json.Unmarshal(resp.Body, &res); err != nil {
		return fmt.Errorf("the answer to the load %s is not Stream Load's: %s", label, fibrest.Clip(resp.Body))
	}
	switch strings.ToLower(res.Status) {
	case "success", "publish timeout":
		// A publish timeout is a load committed, visible a little later.
		return nil
	case "label already exists":
		switch strings.ToUpper(res.ExistingJobStatus) {
		case "FINISHED", "VISIBLE", "COMMITTED":
			return nil
		}
		return &LoadError{Label: label, Result: res, Running: true}
	}
	return &LoadError{Label: label, Result: res}
}

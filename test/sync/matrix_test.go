package synctest

// The interoperability and recovery matrix (I192): each source against one
// fixed set of sinks. For each source, a table (or collection, or topic)
// is filled, a pipeline of the source into every sink at once is started,
// a mixed load of inserts, updates, deletes, key changes and transactions
// of several changes is written to the source while the pipeline is
// killed at random crash points again and again, and at the end every
// sink must hold what the source holds, row for row.
//
// The sinks are PostgreSQL, MySQL, MongoDB, Elasticsearch, ClickHouse and
// Redis (keys), one server each: the newest PostgreSQL and MongoDB of the
// environment, MySQL 8.4 (or the last of FIT_SYNC_MYSQL). The sources are
// those of mxSources: PostgreSQL, MySQL and MariaDB, MongoDB, TiDB
// through TiCDC, and Debezium's events in Kafka, and the second round's
// (matrix2_test.go): Debezium's events in RabbitMQ, NATS JetStream, Pulsar
// and MQTT, SQL Server through CDC, and Doris polled by a cursor;
// FIT_SYNC_MATRIX_SOURCES (postgres, mysql, mongo, tidb, kafka, rabbitmq,
// nats, pulsar, mqtt, mssql, doris, separated by commas or spaces) picks
// some, every one when unset. FIT_SYNC_MATRIX=full runs each source
// on every server of its kind (PostgreSQL 13 to 17, MySQL 5.7, 8.0, 8.4 and
// MariaDB 10.11, 11, MongoDB 6 to 8, Kafka and Redpanda), with a larger
// load and more kills: the nightly matrix. test/sync/run.sh starts the
// servers (SYNC_FAMILIES=matrix starts one of each).

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/clickhouse"
	_ "github.com/lesismal/fit/sync/elasticsearch"
	_ "github.com/lesismal/fit/sync/kafka"
	kafkawire "github.com/lesismal/fit/sync/kafka/wire"
	_ "github.com/lesismal/fit/sync/mongo"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
	_ "github.com/lesismal/fit/sync/mysql"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
	_ "github.com/lesismal/fit/sync/postgres"
	_ "github.com/lesismal/fit/sync/postgres/sink"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
	_ "github.com/lesismal/fit/sync/redis"
	"github.com/lesismal/fit/sync/redis/wire"
	_ "github.com/lesismal/fit/sync/tidb"
)

// mxRow is a row of the matrix's table: id BIGINT key, v text, n a
// nullable BIGINT, ver BIGINT (the load's count of changes when the row
// was written).
type mxRow struct {
	V   string
	N   *int64
	Ver int64
}

// mxLine is a row as every reader renders it: id|v|n|ver, a null n as ~.
func mxLine(id int64, v string, n *int64, ver int64) string {
	ns := "~"
	if n != nil {
		ns = strconv.FormatInt(*n, 10)
	}
	return fmt.Sprintf("%d|%s|%s|%d", id, v, ns, ver)
}

// mxState is a table's rows by id, as lines.
type mxState map[int64]string

// mxOp is one change: kind i (insert of id), u (update of id), d (delete
// of id) or k (id changed to to, ver updated).
type mxOp struct {
	Kind   byte
	ID, To int64
	Row    mxRow // for d, only Ver: the load's count when it was made
}

// mxModel makes the load and keeps the rows it leaves.
type mxModel struct {
	rnd       *rand.Rand
	rows      map[int64]mxRow
	live      []int64
	at        map[int64]int
	next, ver int64
}

func newMXModel(seed uint64) *mxModel {
	return &mxModel{rnd: rand.New(rand.NewPCG(seed, 192)), rows: map[int64]mxRow{}, at: map[int64]int{}}
}

func (m *mxModel) row() mxRow {
	m.ver++
	r := mxRow{V: fmt.Sprintf("v%d é'\"\\%d", m.ver, m.rnd.IntN(1000)), Ver: m.ver}
	if m.ver%5 != 0 {
		n := m.ver*7 - 300
		r.N = &n
	}
	return r
}

func (m *mxModel) add(id int64, r mxRow) {
	m.rows[id] = r
	m.at[id] = len(m.live)
	m.live = append(m.live, id)
}

func (m *mxModel) remove(id int64) {
	i := m.at[id]
	last := m.live[len(m.live)-1]
	m.live[i], m.at[last] = last, i
	m.live = m.live[:len(m.live)-1]
	delete(m.at, id)
	delete(m.rows, id)
}

// insert is an insert of a new id.
func (m *mxModel) insert() mxOp {
	id := m.next
	m.next++
	r := m.row()
	m.add(id, r)
	return mxOp{Kind: 'i', ID: id, Row: r}
}

// tx makes a transaction of one change, or of two to four when multi;
// key changes only when rekey.
func (m *mxModel) tx(multi, rekey bool) []mxOp {
	n := 1
	if multi && m.rnd.IntN(4) == 0 {
		n = 2 + m.rnd.IntN(3)
	}
	var ops []mxOp
	for range n {
		k := m.rnd.IntN(10)
		if len(m.live) < 20 || k < 3 {
			ops = append(ops, m.insert())
			continue
		}
		id := m.live[m.rnd.IntN(len(m.live))]
		switch {
		case k < 7 || k == 9 && !rekey:
			r := m.row()
			m.rows[id] = r
			ops = append(ops, mxOp{Kind: 'u', ID: id, Row: r})
		case k < 9:
			m.remove(id)
			m.ver++
			ops = append(ops, mxOp{Kind: 'd', ID: id, Row: mxRow{Ver: m.ver}})
		default:
			r := m.rows[id]
			m.remove(id)
			to := m.next
			m.next++
			m.ver++
			r.Ver = m.ver
			m.add(to, r)
			ops = append(ops, mxOp{Kind: 'k', ID: id, To: to, Row: r})
		}
	}
	return ops
}

func (m *mxModel) state() mxState {
	s := mxState{}
	for id, r := range m.rows {
		s[id] = mxLine(id, r.V, r.N, r.Ver)
	}
	return s
}

// mxDiff describes how got differs from want, "" when it does not.
func mxDiff(want, got mxState) string {
	var missing, extra, differ []string
	for id, w := range want {
		g, ok := got[id]
		switch {
		case !ok:
			missing = append(missing, w)
		case g != w:
			differ = append(differ, g+" (want "+w+")")
		}
	}
	for id, g := range got {
		if _, ok := want[id]; !ok {
			extra = append(extra, g)
		}
	}
	if len(missing)+len(extra)+len(differ) == 0 {
		return ""
	}
	few := func(s []string) string {
		slices.Sort(s)
		if len(s) > 5 {
			s = append(s[:5], "...")
		}
		return strings.Join(s, ", ")
	}
	return fmt.Sprintf("%d rows of %d: %d missing [%s], %d extra [%s], %d different [%s]", len(got), len(want),
		len(missing), few(missing), len(extra), few(extra), len(differ), few(differ))
}

// mxSize is the matrix's scale: the rows before the pipeline starts (its
// snapshot), the transactions of the load, the kill rounds.
type mxSize struct{ rows, txs, kills int }

func mxScale() mxSize {
	if os.Getenv("FIT_SYNC_MATRIX") == "full" {
		return mxSize{rows: 1000, txs: 2500, kills: 10}
	}
	return mxSize{rows: 200, txs: 500, kills: 4}
}

// mxRun is a source made ready for one run: its pipeline's source and
// transforms, the load's applier (called from one goroutine, in order) and
// the source's rows as it holds them (nil for a source that holds none,
// Kafka's topic, whose rows are the model's).
type mxRun struct {
	source     map[string]any
	transforms []any
	apply      func(ctx context.Context, ops []mxOp) error
	state      func(t *testing.T) mxState
	cleanup    func()
}

// mxSource is a source of the matrix: family is what
// FIT_SYNC_MATRIX_SOURCES names; txs, whether the load writes transactions
// of several changes; rekey, whether it changes keys; setup makes a table
// of name run holding the model's rows.
type mxSource struct {
	name, family string
	txs, rekey   bool
	setup        func(t *testing.T, run string, m *mxModel) mxRun
}

// mxSink is a sink of the matrix: setup makes its table of name run and
// returns the pipeline's sink (module and params) and a reader of its rows.
type mxSink struct {
	name  string
	setup func(t *testing.T, run string) (map[string]any, func(t *testing.T) mxState)
}

// TestMatrix runs every source of the environment against the sinks.
func TestMatrix(t *testing.T) {
	sinks := mxSinks(t)
	sources := mxSources(t)
	size := mxScale()
	for _, src := range sources {
		t.Run(src.name, func(t *testing.T) {
			t.Parallel()
			mxRunSource(t, src, sinks, size)
		})
	}
}

func mxRunSource(t *testing.T, src mxSource, sinks []mxSink, size mxSize) {
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 1))
	m := newMXModel(seed)
	for range size.rows {
		m.insert()
	}
	run := fmt.Sprintf("mx_%s_%s", src.name, strconv.FormatInt(time.Now().UnixNano(), 36))
	r := src.setup(t, run, m)
	if r.cleanup != nil {
		t.Cleanup(r.cleanup)
	}
	var sinkConf []any
	readers := map[string]func(t *testing.T) mxState{}
	for _, s := range sinks {
		conf, read := s.setup(t, run)
		readers[s.name] = read
		sinkConf = append(sinkConf, map[string]any{"name": s.name,
			"batch": map[string]any{"max_records": 1 + rnd.IntN(100), "max_wait": "5ms"}, "sink": conf})
	}
	pipeline := map[string]any{"name": "p", "on_error": "stop", "checkpoint_interval": "1ms",
		"queue": map[string]any{"max_records": 20 + rnd.IntN(300)}, "source": r.source, "sinks": sinkConf}
	if len(r.transforms) > 0 {
		pipeline["transforms"] = r.transforms
	}
	state := filepath.Join(t.TempDir(), "state")
	cfg := loadSync(t, state, pipeline)
	// The first run saves no checkpoint but its source's start: it is
	// killed before its first, with rows in the sinks, as a source of a
	// snapshot then a stream must survive (the start it saves, I192's
	// fix): a run again from nothing would snapshot and stream from a later
	// start, and lose the changes between the two, the deletes of rows the
	// sinks hold among them.
	first := map[string]any{}
	for k, v := range pipeline {
		first[k] = v
	}
	first["checkpoint_interval"] = "1h"
	cfgFirst := loadSync(t, state, first)

	// The load, while the pipeline runs and is killed.
	var stop atomic.Bool
	loadDone := make(chan error, 1)
	var txs [][]mxOp
	for range size.txs {
		txs = append(txs, m.tx(src.txs, src.rekey))
	}
	go func() {
		lr := rand.New(rand.NewPCG(seed, 2))
		for _, tx := range txs {
			if stop.Load() {
				break
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			err := r.apply(ctx, tx)
			cancel()
			if err != nil {
				loadDone <- err
				return
			}
			// Two milliseconds at least: ClickHouse versions a key's rows by
			// their commit times, MongoDB's and TiDB's in milliseconds.
			time.Sleep(time.Duration(2+lr.IntN(8)) * time.Millisecond)
		}
		loadDone <- nil
	}()
	t.Cleanup(func() { stop.Store(true) })

	points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
		fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
	kills := 0
	var rt *fitsync.Runtime
	start := func(c *config.Config, crash func(string, fitsync.Point) bool) {
		rt = fitsync.NewRuntime(fitsync.Options{Crash: crash})
		if err := rt.Apply(c.Sync); err != nil {
			t.Fatal(err)
		}
	}
	for round := range size.kills {
		at := points[rnd.IntN(len(points))]
		c := cfg
		if round == 0 {
			at, c = fitsync.CrashWritten, cfgFirst // killed for sure, before its first checkpoint
		}
		nth := int32(1 + rnd.IntN(200))
		if at == fitsync.CrashCheckpointWritten || at == fitsync.CrashCheckpointSaved {
			nth = int32(1 + rnd.IntN(5))
		}
		var seen atomic.Int32
		start(c, func(_ string, p fitsync.Point) bool { return p == at && seen.Add(1) == nth })
		deadline := time.Now().Add(20 * time.Second)
		for redisState(rt) != "killed" && time.Now().Before(deadline) {
			if st := redisState(rt); st == "stopped" {
				t.Fatalf("the pipeline stopped: %+v", rt.Status())
			}
			time.Sleep(time.Millisecond)
		}
		if redisState(rt) == "killed" {
			kills++
		} else {
			t.Logf("not killed at %s #%d", at, nth)
		}
		rt.Close(context.Background())
	}
	start(cfg, nil)
	defer func() { rt.Close(context.Background()) }()
	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatalf("the load: %v", err)
		}
	case <-time.After(10 * time.Minute): // a hang guard
		stop.Store(true)
		t.Fatal("the load did not end")
	}
	want := m.state()
	if r.state != nil {
		if d := mxDiff(want, r.state(t)); d != "" {
			t.Fatalf("the source does not hold the load's rows: %s", d)
		}
	}
	pending := map[string]string{}
	for name := range readers {
		pending[name] = "not read"
	}
	deadline := time.Now().Add(5 * time.Minute) // a hang guard
	for len(pending) > 0 {
		for name, read := range readers {
			if _, ok := pending[name]; !ok {
				continue
			}
			if d := mxDiff(want, read(t)); d != "" {
				pending[name] = d
			} else {
				delete(pending, name)
			}
		}
		if len(pending) == 0 {
			break
		}
		if st := redisState(rt); st == "stopped" || time.Now().After(deadline) {
			for name, d := range pending {
				t.Errorf("%s: %s", name, d)
			}
			t.Fatalf("the pipeline is %s: %+v", st, rt.Status())
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Logf("%d rows in every sink, equal to the source's, after %d transactions across %d kills", len(want), len(txs), kills)
}

// mxPick reports whether the source family is picked by
// FIT_SYNC_MATRIX_SOURCES.
func mxPick(family string) bool {
	list := os.Getenv("FIT_SYNC_MATRIX_SOURCES")
	if list == "" {
		return true
	}
	return slices.Contains(strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' }), family)
}

// mxSources lists the sources of the environment, each of a family
// FIT_SYNC_MATRIX_SOURCES picks; in the full matrix, one per server.
//
// A source is one entry here: a family, whether its load has transactions
// and key changes, and a setup that makes its table, fills it with the
// model's rows and says how to apply the load and read the rows back.
func mxSources(t *testing.T) []mxSource {
	full := os.Getenv("FIT_SYNC_MATRIX") == "full"
	var out []mxSource
	if mxPick("postgres") {
		servers := pgServers(t)
		if !full {
			servers = servers[len(servers)-1:]
		}
		for _, s := range servers {
			out = append(out, mxSource{name: "postgres" + s.version, family: "postgres", txs: true, rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxPostgresSource(t, s, run, m) }})
		}
	}
	if mxPick("mongo") {
		sets := mongoSets(t)
		names := slices.Sorted(func(yield func(string) bool) {
			for k := range sets {
				if !yield(k) {
					return
				}
			}
		})
		if !full {
			names = names[len(names)-1:]
		}
		for _, name := range names {
			uri := sets[name]
			out = append(out, mxSource{name: name, family: "mongo",
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxMongoSource(t, uri, run, m) }})
		}
	}
	if mxPick("mysql") {
		servers := mysqlServers(t)
		if !full {
			servers = servers[len(servers)-1:]
			for _, s := range mysqlServers(t) {
				if s.name == "mysql84" {
					servers = []mysqlServer{s}
				}
			}
		}
		for _, s := range servers {
			out = append(out, mxSource{name: s.name, family: "mysql", txs: true, rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxMySQLSource(t, s, run, m) }})
		}
	}
	if mxPick("tidb") {
		out = append(out, mxSource{name: "tidb", family: "tidb", txs: true, rekey: true, setup: mxTiDBSource})
	}
	if mxPick("kafka") {
		servers := kafkaServers(t)
		if !full {
			servers = servers[:1]
		}
		for _, s := range servers {
			out = append(out, mxSource{name: s.name, family: "kafka", rekey: true,
				setup: func(t *testing.T, run string, m *mxModel) mxRun { return mxKafkaSource(t, s, run, m) }})
		}
	}
	out = append(out, mxSources2(t)...)
	if len(out) == 0 {
		t.Fatalf("FIT_SYNC_MATRIX_SOURCES %q picks no source", os.Getenv("FIT_SYNC_MATRIX_SOURCES"))
	}
	return out
}

// mxSinks are the sinks every source is run against.
func mxSinks(t *testing.T) []mxSink {
	pg := pgServers(t)
	pgs := pg[len(pg)-1]
	my := mysqlServers(t)
	mys := my[len(my)-1]
	for _, s := range my {
		if s.name == "mysql84" {
			mys = s
		}
	}
	sets := mongoSets(t)
	var newest string
	for k := range sets {
		newest = max(newest, k)
	}
	mongoURI := sets[newest]
	es := mxES(t)
	ch := clickhouseServer(t)
	rs := mxRedis(t)
	return []mxSink{
		{"postgres", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			return mxPostgresSink(t, pgs, run)
		}},
		{"mysql", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			return mxMySQLSink(t, mys, run)
		}},
		{"mongo", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			return mxMongoSink(t, mongoURI, run)
		}},
		{"elasticsearch", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) { return mxESSink(t, es, run) }},
		{"clickhouse", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			return mxClickHouseSink(t, ch, run)
		}},
		{"redis", func(t *testing.T, run string) (map[string]any, func(*testing.T) mxState) {
			return mxRedisSink(t, rs, run)
		}},
	}
}

// mxValues are a row's values, as SQL arguments.
func mxValues(id int64, r mxRow) []any {
	var n any
	if r.N != nil {
		n = *r.N
	}
	return []any{id, r.V, n, r.Ver}
}

// mxSQL applies a transaction with statements of the placeholder ph
// (MySQL's ?, PostgreSQL's $n), between BEGIN and COMMIT when it has more
// than one change.
func mxSQL(ctx context.Context, exec func(ctx context.Context, q string, args ...any) error, dollar bool, ops []mxOp) error {
	ph := func(q string) string {
		if !dollar {
			return q
		}
		for i := 1; strings.Contains(q, "?"); i++ {
			q = strings.Replace(q, "?", "$"+strconv.Itoa(i), 1)
		}
		return q
	}
	if len(ops) > 1 {
		if err := exec(ctx, "BEGIN"); err != nil {
			return err
		}
	}
	for _, op := range ops {
		var err error
		switch op.Kind {
		case 'i':
			err = exec(ctx, ph("INSERT INTO t (id, v, n, ver) VALUES (?, ?, ?, ?)"), mxValues(op.ID, op.Row)...)
		case 'u':
			v := mxValues(op.ID, op.Row)
			err = exec(ctx, ph("UPDATE t SET v = ?, n = ?, ver = ? WHERE id = ?"), v[1], v[2], v[3], op.ID)
		case 'd':
			err = exec(ctx, ph("DELETE FROM t WHERE id = ?"), op.ID)
		case 'k':
			err = exec(ctx, ph("UPDATE t SET id = ?, ver = ? WHERE id = ?"), op.To, op.Row.Ver, op.ID)
		}
		if err != nil {
			return fmt.Errorf("%c %d: %w", op.Kind, op.ID, err)
		}
	}
	if len(ops) > 1 {
		return exec(ctx, "COMMIT")
	}
	return nil
}

// mxLines parses lines of id|v|n|ver.
func mxLines(t *testing.T, lines []string) mxState {
	t.Helper()
	s := mxState{}
	for _, l := range lines {
		id, err := strconv.ParseInt(l[:strings.IndexByte(l, '|')], 10, 64)
		if err != nil {
			t.Fatalf("a row %q: %v", l, err)
		}
		s[id] = l
	}
	return s
}

// mxJSON makes a row's line of a JSON object of id (or _id), v, n and ver.
func mxJSON(t *testing.T, doc []byte) (int64, string) {
	t.Helper()
	d := json.NewDecoder(bytes.NewReader(doc))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil {
		t.Fatalf("%s: %v", doc, err)
	}
	num := func(k string) (int64, bool) {
		switch v := m[k].(type) {
		case json.Number:
			n, err := v.Int64()
			return n, err == nil
		case string: // a 64-bit integer some servers quote
			n, err := strconv.ParseInt(v, 10, 64)
			return n, err == nil
		}
		return 0, false
	}
	id, ok := num("id")
	if !ok {
		id, ok = num("_id")
	}
	ver, ok2 := num("ver")
	v, ok3 := m["v"].(string)
	if !ok || !ok2 || !ok3 {
		return -1, "malformed: " + string(doc)
	}
	var n *int64
	if x, ok := num("n"); ok {
		n = &x
	} else if m["n"] != nil {
		return -1, "malformed: " + string(doc)
	}
	return id, mxLine(id, v, n, ver)
}

// The sources.

func mxPostgresSource(t *testing.T, s pgServer, run string, m *mxModel) mxRun {
	c := s.connect(t, nil)
	pgExec(t, c, "DROP SCHEMA IF EXISTS "+run+" CASCADE")
	pgExec(t, c, "CREATE SCHEMA "+run)
	pgExec(t, c, "CREATE TABLE "+run+".t (id bigint PRIMARY KEY, v text NOT NULL, n bigint, ver bigint NOT NULL)")
	for id, r := range m.rows {
		pgExec(t, c, "INSERT INTO "+run+".t VALUES ($1, $2, $3, $4)", mxValues(id, r)...)
	}
	slot := run
	lc := s.connect(t, nil)
	pgExec(t, lc, "SET search_path = "+run)
	return mxRun{
		source: map[string]any{"module": "postgres", "params": map[string]any{"address": s.addr, "user": os.Getenv("FIT_SYNC_PG_USER"),
			"password_env": "FIT_SYNC_PG_PASSWORD", "database": "fit", "ca_file": filepath.Join(os.Getenv("FIT_SYNC_PG_CA_DIR"), s.version+".pem"),
			"publication": run, "slot": slot, "create": true, "tables": []string{run + ".t"}, "status_interval": "100ms", "chunk_rows": 50}},
		apply: func(ctx context.Context, ops []mxOp) error {
			return mxSQL(ctx, func(ctx context.Context, q string, args ...any) error { _, err := lc.Exec(ctx, q, args...); return err }, true, ops)
		},
		state: func(t *testing.T) mxState {
			return mxLines(t, pgTableRows(t, c, run+".t", "id", "v", "coalesce(n::text, '~')", "ver"))
		},
		cleanup: func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			cc, err := pgwire.Connect(ctx, s.config())
			if err != nil {
				return
			}
			defer cc.Close()
			for range 100 { // the source's walsender may take a moment to go
				if _, err := cc.Exec(ctx, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE slot_name = $1", slot); err == nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			cc.Exec(ctx, "DROP PUBLICATION IF EXISTS "+run)
			cc.Exec(ctx, "DROP SCHEMA IF EXISTS "+run+" CASCADE")
		},
	}
}

// mxMongoPassword puts the sets' password, which their connection strings
// hold, in an environment variable a pipeline's password_env names; the
// same for every set.
func mxMongoPassword(uri string) (plain string, user string) {
	rest, _ := strings.CutPrefix(uri, "mongodb://")
	at := strings.LastIndexByte(rest, '@')
	user, pass, _ := strings.Cut(rest[:at], ":")
	pass, _ = url.PathUnescape(pass)
	os.Setenv("FIT_TEST_MX_MONGO_PASSWORD", pass)
	return "mongodb://" + rest[at+1:], user
}

func mxMongoDoc(id int64, r mxRow) mongowire.D {
	var n any
	if r.N != nil {
		n = *r.N
	}
	return mongowire.Doc("_id", id, "v", r.V, "n", n, "ver", r.Ver)
}

// mxMongoSource: MongoDB has no transactions here (the client speaks no
// sessions), and no key changes (_id is immutable): a key change is a
// delete and an insert.
func mxMongoSource(t *testing.T, uri, run string, m *mxModel) mxRun {
	c := mongoClient(t, uri, nil)
	ctx := hangGuard(t)
	const db = "fit_mx"
	c.Run(ctx, db, mongowire.Doc("drop", run)) // fails when there is none
	var docs []mongowire.D
	for id, r := range m.rows {
		docs = append(docs, mxMongoDoc(id, r))
	}
	mongoInsert(t, ctx, c, db, run, docs...)
	hosts, user := mxMongoPassword(uri)
	return mxRun{
		source: map[string]any{"module": "mongo", "params": map[string]any{"uri": hosts, "username": user,
			"password_env": "FIT_TEST_MX_MONGO_PASSWORD", "database": db, "collections": []string{run}, "max_await": "50ms"}},
		transforms: []any{map[string]any{"module": "fields", "params": map[string]any{"rename": map[string]any{"_id": "id"}}}},
		apply: func(ctx context.Context, ops []mxOp) error {
			for _, op := range ops {
				var cmds []mongowire.D
				switch op.Kind {
				case 'i':
					cmds = append(cmds, mongowire.Doc("insert", run, "documents", mongowire.A{mxMongoDoc(op.ID, op.Row)}))
				case 'u':
					d := mxMongoDoc(op.ID, op.Row)[1:]
					cmds = append(cmds, mongowire.Doc("update", run, "updates", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", op.ID), "u", mongowire.Doc("$set", d))}))
				case 'd':
					cmds = append(cmds, mongowire.Doc("delete", run, "deletes", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", op.ID), "limit", 1)}))
				case 'k':
					cmds = append(cmds, mongowire.Doc("delete", run, "deletes", mongowire.A{mongowire.Doc("q", mongowire.Doc("_id", op.ID), "limit", 1)}),
						mongowire.Doc("insert", run, "documents", mongowire.A{mxMongoDoc(op.To, op.Row)}))
				}
				for _, cmd := range cmds {
					res, err := c.Run(ctx, db, cmd)
					if err != nil {
						return err
					}
					if we, ok := res.Lookup("writeErrors"); ok {
						return fmt.Errorf("%v: %s", cmd[0].Key, mongowire.Raw(we.Array()[0].Document()))
					}
				}
			}
			return nil
		},
		state: func(t *testing.T) mxState { return mxMongoRows(t, c, db, run) },
		cleanup: func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c.Run(ctx, db, mongowire.Doc("drop", run))
		},
	}
}

func mxMongoRows(t *testing.T, c *mongowire.Client, db, coll string) mxState {
	t.Helper()
	s := mxState{}
	for _, d := range mongoDocsRaw(t, hangGuard(t), c, db, coll) {
		v, err := d.Value()
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(mxPlain(v))
		if err != nil {
			t.Fatal(err)
		}
		id, line := mxJSON(t, b)
		s[id] = line
	}
	return s
}

// mxPlain is a record's value as plain Go values, for JSON.
func mxPlain(v fitsync.Value) any {
	switch v.Kind() {
	case fitsync.KindNull:
		return nil
	case fitsync.KindInt:
		return v.AsInt()
	case fitsync.KindUint:
		return v.AsUint()
	case fitsync.KindMap:
		m := map[string]any{}
		for _, f := range v.Fields() {
			m[f.Name] = mxPlain(f.Value)
		}
		return m
	}
	return v.Text()
}

// mxTiDBSource: a TiDB table read through a TiCDC changefeed into Kafka,
// in canal-json, after a snapshot.
func mxTiDBSource(t *testing.T, run string, m *mxModel) mxRun {
	addr := kafkaEnv(t, "FIT_SYNC_TICDC_TIDB")
	container, cdcKafka := kafkaEnv(t, "FIT_SYNC_TICDC_CONTAINER"), kafkaEnv(t, "FIT_SYNC_TICDC_KAFKA")
	brokers := strings.Split(kafkaEnv(t, "FIT_SYNC_KAFKA_BROKERS"), ",")
	dial := func(ctx context.Context, db string) (*mysqlwire.Conn, error) {
		return mysqlwire.Dial(ctx, mysqlwire.Config{Addr: addr, User: "root", Database: db})
	}
	c, err := dial(hangGuard(t), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	run = strings.ToLower(run)
	// TiDB answers before TiKV's regions do (error 9005, Region is
	// unavailable), when the cluster has just started.
	for i := 0; ; i++ {
		_, err := c.Exec(hangGuard(t), "CREATE DATABASE IF NOT EXISTS "+run)
		if err == nil {
			break
		}
		if i == 180 {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
	}
	mxMySQLTable(t, c, run, m)
	kc := kafkaClient(t, kafkawire.Config{Brokers: brokers})
	topic := kafkaTopic(t, kc, 3, 3, nil)
	id := strings.Trim(strings.ReplaceAll(topic[max(0, len(topic)-120):], "_", "-"), "-")
	cli := func(args ...string) error {
		out, err := exec.Command("docker", append([]string{"exec", container, "/cdc", "cli", "changefeed"}, append(args, "--server=http://127.0.0.1:8300")...)...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("cdc %v: %v: %s", args, err, out)
		}
		return nil
	}
	uri := "kafka://" + strings.Split(cdcKafka, ",")[0] + "/" + topic + "?protocol=canal-json&enable-tidb-extension=true&partition-num=3&replication-factor=3&max-message-bytes=1048576"
	if err := cli("create", "--changefeed-id="+id, "--sink-uri="+uri); err != nil {
		t.Fatal(err)
	}
	lc, err := dial(hangGuard(t), run)
	if err != nil {
		t.Fatal(err)
	}
	return mxRun{
		source: map[string]any{"module": "tidb", "params": map[string]any{"address": addr, "tables": []string{run + ".t"}, "page_rows": 70,
			"kafka": map[string]any{"brokers": brokers, "topics": []string{topic}, "max_wait": "100ms"}}},
		apply: func(ctx context.Context, ops []mxOp) error { return mxSQL(ctx, mxMySQLExec(lc), false, ops) },
		state: func(t *testing.T) mxState { return mxMySQLRows(t, c, run+".t") },
		cleanup: func() {
			cli("remove", "--changefeed-id="+id)
			lc.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c.Exec(ctx, "DROP DATABASE IF EXISTS "+run)
		},
	}
}

// mxMySQLSource: a MySQL or MariaDB table read as a replica, from the
// binlog, after a snapshot.
func mxMySQLSource(t *testing.T, s mysqlServer, run string, m *mxModel) mxRun {
	c := s.root(t)
	db := strings.ToLower(run)
	mxMySQLTable(t, c, db, m)
	lc := s.root(t)
	if err := lc.UseDatabase(hangGuard(t), db); err != nil {
		t.Fatal(err)
	}
	return mxRun{
		source: map[string]any{"module": "mysql", "params": map[string]any{"address": s.addr, "username": "root",
			"password_env": "FIT_SYNC_MYSQL_PASSWORD", "tls": true, "ca_file": os.Getenv("FIT_SYNC_MYSQL_CA"), "tls_rsa": s.name == "mysql57",
			"server_id": 1000 + rand.IntN(1000000), "heartbeat": "1s", "tables": []string{db + ".t"}}},
		apply: func(ctx context.Context, ops []mxOp) error { return mxSQL(ctx, mxMySQLExec(lc), false, ops) },
		state: func(t *testing.T) mxState { return mxMySQLRows(t, c, db+".t") },
		cleanup: func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			c.Exec(ctx, "DROP DATABASE IF EXISTS "+db)
		},
	}
}

// mxMySQLTable makes database db with the table t holding the model's rows,
// on a server of MySQL's protocol.
func mxMySQLTable(t *testing.T, c *mysqlwire.Conn, db string, m *mxModel) {
	t.Helper()
	mysqlDB(t, c, db)
	mustExec(t, c, "CREATE TABLE t (id BIGINT NOT NULL PRIMARY KEY, v VARCHAR(64) NOT NULL, n BIGINT NULL, ver BIGINT NOT NULL)")
	for id, r := range m.rows {
		mustExec(t, c, "INSERT INTO t VALUES (?, ?, ?, ?)", mxValues(id, r)...)
	}
}

func mxMySQLExec(c *mysqlwire.Conn) func(ctx context.Context, q string, args ...any) error {
	return func(ctx context.Context, q string, args ...any) error {
		_, err := c.Exec(ctx, q, args...)
		return err
	}
}

func mxMySQLRows(t *testing.T, c *mysqlwire.Conn, table string) mxState {
	t.Helper()
	rows, err := c.Query(hangGuard(t), "SELECT CONCAT_WS('|', id, v, IFNULL(n, '~'), ver) FROM "+table)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for rows.Next() {
		switch v := rows.Values()[0].(type) {
		case []byte:
			lines = append(lines, string(v))
		default:
			lines = append(lines, fmt.Sprint(v))
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	return mxLines(t, lines)
}

// mxKafkaSource: Debezium's change events (without the schema envelope)
// in a topic of three partitions, each key's in one, as a Debezium
// connector writes them: a key change is a delete (and its tombstone) and
// a create. The source's rows are the model's.
func mxKafkaSource(t *testing.T, s kafkaServer, run string, m *mxModel) mxRun {
	brokers := strings.Split(s.brokers, ",")
	c := kafkaClient(t, kafkawire.Config{Brokers: brokers})
	topic := kafkaTopic(t, c, 3, s.rf, nil)
	// event is a change event of id: its row after (nil for a delete,
	// whose before holds the key), committed at ver milliseconds past an
	// epoch: each change of a key later than the one before (ClickHouse
	// versions rows by their commit times).
	event := func(op string, id int64, r *mxRow, ver int64) kafkawire.Record {
		var before, after any
		if op == "d" {
			before = map[string]any{"id": id}
		} else {
			after = map[string]any{"id": id, "v": r.V, "n": r.N, "ver": r.Ver}
		}
		key, _ := json.Marshal(map[string]any{"id": id})
		v, _ := json.Marshal(map[string]any{"op": op, "before": before, "after": after, "ts_ms": 1_700_000_000_000 + ver,
			"source": map[string]any{"db": "mx", "table": run, "ts_ms": 1_700_000_000_000 + ver}})
		return kafkawire.Record{Key: key, Value: v}
	}
	tombstone := func(id int64) kafkawire.Record {
		key, _ := json.Marshal(map[string]any{"id": id})
		return kafkawire.Record{Key: key}
	}
	produce := func(ctx context.Context, recs ...kafkawire.Record) error {
		byPart := map[int32][]kafkawire.Record{}
		var order []int32
		for _, r := range recs {
			p := kafkawire.PartitionFor(r.Key, 3)
			if _, ok := byPart[p]; !ok {
				order = append(order, p)
			}
			byPart[p] = append(byPart[p], r)
		}
		var batches []kafkawire.ProduceBatch
		for _, p := range order {
			batches = append(batches, kafkawire.ProduceBatch{Topic: topic, Partition: p, Records: byPart[p]})
		}
		_, err := c.Produce(ctx, kafkawire.ProduceOptions{}, batches...)
		return err
	}
	var initial []kafkawire.Record
	for id, r := range m.rows {
		initial = append(initial, event("r", id, &r, r.Ver))
	}
	if err := produce(hangGuard(t), initial...); err != nil {
		t.Fatal(err)
	}
	return mxRun{
		source: map[string]any{"module": "kafka", "params": map[string]any{"brokers": brokers, "topics": []string{topic},
			"codec": map[string]any{"module": "debezium"}, "tombstones": "skip", "max_wait": "100ms"}},
		apply: func(ctx context.Context, ops []mxOp) error {
			var recs []kafkawire.Record
			for _, op := range ops {
				switch op.Kind {
				case 'i':
					recs = append(recs, event("c", op.ID, &op.Row, op.Row.Ver))
				case 'u':
					recs = append(recs, event("u", op.ID, &op.Row, op.Row.Ver))
				case 'd':
					recs = append(recs, event("d", op.ID, nil, op.Row.Ver), tombstone(op.ID))
				case 'k':
					recs = append(recs, event("d", op.ID, nil, op.Row.Ver), tombstone(op.ID), event("c", op.To, &op.Row, op.Row.Ver))
				}
			}
			return produce(ctx, recs...)
		},
	}
}

// The sinks.

func mxPostgresSink(t *testing.T, s pgServer, run string) (map[string]any, func(*testing.T) mxState) {
	c := s.connect(t, nil)
	schema := run + "_sink"
	pgExec(t, c, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	pgExec(t, c, "CREATE SCHEMA "+schema)
	pgExec(t, c, "CREATE TABLE "+schema+".t (id bigint PRIMARY KEY, v text NOT NULL, n bigint, ver bigint NOT NULL)")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})
	return map[string]any{"module": "postgres", "params": pgSinkParams(s, map[string]any{"table": schema + ".t", "transactions": "source"})},
		func(t *testing.T) mxState {
			return mxLines(t, pgTableRows(t, c, schema+".t", "id", "v", "coalesce(n::text, '~')", "ver"))
		}
}

func mxMySQLSink(t *testing.T, s mysqlServer, run string) (map[string]any, func(*testing.T) mxState) {
	c := s.root(t)
	db := strings.ToLower(run) + "_sink"
	mysqlDB(t, c, db)
	mustExec(t, c, "CREATE TABLE t (id BIGINT NOT NULL PRIMARY KEY, v VARCHAR(64) NOT NULL, n BIGINT NULL, ver BIGINT NOT NULL)")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.Exec(ctx, "DROP DATABASE IF EXISTS "+db)
	})
	return map[string]any{"module": "mysql", "params": map[string]any{"address": s.addr, "username": "root", "password_env": "FIT_SYNC_MYSQL_PASSWORD",
			"tls": true, "ca_file": os.Getenv("FIT_SYNC_MYSQL_CA"), "tls_rsa": s.name == "mysql57", "database": db, "table": db + ".t", "transactions": "source"}},
		func(t *testing.T) mxState { return mxMySQLRows(t, c, db+".t") }
}

func mxMongoSink(t *testing.T, uri, run string) (map[string]any, func(*testing.T) mxState) {
	c := mongoClient(t, uri, nil)
	const db = "fit_mx_sink"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.Run(ctx, db, mongowire.Doc("drop", run))
	})
	hosts, user := mxMongoPassword(uri)
	p := map[string]any{"uri": hosts, "username": user, "password_env": "FIT_TEST_MX_MONGO_PASSWORD", "database": db, "collection": run}
	if ca := os.Getenv("FIT_SYNC_MONGO_CA"); ca != "" {
		p["tls"], p["ca_file"] = true, ca
	}
	return map[string]any{"module": "mongo", "params": p}, func(t *testing.T) mxState { return mxMongoRows(t, c, db, run) }
}

// mxES is the Elasticsearch of FIT_SYNC_ES_URL; OpenSearch, which the
// matrix's servers leave out, is not needed.
func mxES(t *testing.T) *cluster {
	u := os.Getenv("FIT_SYNC_ES_URL")
	if u == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_ES_URL is not set")
		}
		t.Skip("no Elasticsearch: FIT_SYNC_ES_URL is not set (test/sync/run.sh sets it)")
	}
	cl := &cluster{name: "elasticsearch", url: u, ca: os.Getenv("FIT_SYNC_ES_CA"), user: "elastic", password: os.Getenv("FIT_SYNC_ES_PASSWORD")}
	pool := x509.NewCertPool()
	pem, err := os.ReadFile(cl.ca)
	if err != nil || !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("the CA %s: %v", cl.ca, err)
	}
	cl.http = &http.Client{Timeout: time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	return cl
}

func mxESSink(t *testing.T, es *cluster, run string) (map[string]any, func(*testing.T) mxState) {
	index := strings.ToLower(run)
	es.call(t, http.MethodPut, "/"+index, `{"settings": {"number_of_replicas": 0}}`, nil)
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, es.url+"/"+index, nil)
		req.SetBasicAuth(es.user, es.password)
		if resp, err := es.http.Do(req); err == nil {
			resp.Body.Close()
		}
	})
	return map[string]any{"module": "elasticsearch", "params": map[string]any{"urls": []string{es.url}, "username": es.user,
			"password_env": "FIT_SYNC_ES_PASSWORD", "ca_file": es.ca, "index": index}},
		func(t *testing.T) mxState {
			s := mxState{}
			for id, doc := range es.docs(t, index) {
				n, line := mxJSON(t, []byte(doc))
				if strconv.FormatInt(n, 10) != id {
					line = "_id " + id + ": " + line
				}
				s[n] = line
			}
			return s
		}
}

// mxClickHouseSink: the sink's defaults, a ReplacingMergeTree(_version,
// _deleted) versioned by commit time (a snapshot's rows, which have none,
// 0) and deletes flagged, read with FINAL.
func mxClickHouseSink(t *testing.T, ch *clickhouse, run string) (map[string]any, func(*testing.T) mxState) {
	table := ch.table(t, run, "(id Int64, v String, n Nullable(Int64), ver Int64, _version UInt64, _deleted UInt8) ENGINE = ReplacingMergeTree(_version, _deleted) ORDER BY id SETTINGS non_replicated_deduplication_window = 1000")
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodPost, ch.url, strings.NewReader("DROP TABLE IF EXISTS "+table))
		req.SetBasicAuth(ch.user, ch.password)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	})
	p := map[string]any{"urls": []string{ch.url}, "username": ch.user, "table": table}
	if ch.password != "" {
		p["password_env"] = "FIT_SYNC_CH_PASSWORD"
	}
	return map[string]any{"module": "clickhouse", "params": p}, func(t *testing.T) mxState {
		var lines []string
		for _, r := range ch.rows(t, "SELECT concat(toString(id), '|', v, '|', ifNull(toString(n), '~'), '|', toString(ver)) FROM "+table+" FINAL WHERE _deleted = 0") {
			var row []string
			if err := json.Unmarshal([]byte(r), &row); err != nil || len(row) != 1 {
				t.Fatalf("%s: %v", r, err)
			}
			lines = append(lines, row[0])
		}
		return mxLines(t, lines)
	}
}

// mxRedis is the Redis of FIT_SYNC_REDIS_ADDR; Valkey, which the matrix's
// servers leave out, is not needed.
func mxRedis(t *testing.T) *redisServer {
	addr := os.Getenv("FIT_SYNC_REDIS_ADDR")
	if addr == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_REDIS_ADDR is not set")
		}
		t.Skip("no Redis: FIT_SYNC_REDIS_ADDR is not set (test/sync/run.sh sets it)")
	}
	return &redisServer{name: "redis", addr: addr, user: os.Getenv("FIT_SYNC_REDIS_USER"), password: os.Getenv("FIT_SYNC_REDIS_PASSWORD"), protocol: 3}
}

// mxRedisSink: a key per row, its JSON, the keys found by SCAN.
func mxRedisSink(t *testing.T, s *redisServer, run string) (map[string]any, func(*testing.T) mxState) {
	prefix := "fit-mx:" + run + ":"
	keys := func(t *testing.T, c *wire.Conn, ctx context.Context) []string {
		var out []string
		cursor := "0"
		for {
			v, err := c.Do(ctx, "SCAN", cursor, "MATCH", prefix+"*", "COUNT", "1000")
			if err == nil {
				err = v.Err()
			}
			if err != nil || len(v.Elems) != 2 {
				t.Fatalf("SCAN: %v %v", v, err)
			}
			cursor, _ = v.Elems[0].Text()
			for _, k := range v.Elems[1].Elems {
				ks, _ := k.Text()
				out = append(out, ks)
			}
			if cursor == "0" {
				return out
			}
		}
	}
	dial := func(t *testing.T, ctx context.Context) *wire.Conn {
		c, err := wire.Dial(ctx, wire.Options{Addr: s.addr, Username: s.user, Password: s.password})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c := dial(t, ctx)
		defer c.Close()
		for _, k := range keys(t, c, ctx) {
			c.Do(ctx, "DEL", k)
		}
	})
	p := map[string]any{"address": s.addr, "mode": "keys", "value": "json", "key": prefix + "{key}"}
	if s.password != "" {
		p["password_env"] = "FIT_SYNC_REDIS_PASSWORD"
		if s.user != "" {
			p["username"] = s.user
		}
	}
	return map[string]any{"module": "redis", "params": p}, func(t *testing.T) mxState {
		ctx := hangGuard(t)
		c := dial(t, ctx)
		defer c.Close()
		all := keys(t, c, ctx)
		st := mxState{}
		for len(all) > 0 {
			n := min(len(all), 500)
			v, err := c.Do(ctx, append([]string{"MGET"}, all[:n]...)...)
			if err != nil || len(v.Elems) != n {
				t.Fatalf("MGET: %v %v", v, err)
			}
			for i, x := range v.Elems {
				doc, ok := x.Text()
				if !ok {
					continue // deleted since the SCAN
				}
				id, line := mxJSON(t, []byte(doc))
				if want := prefix + strconv.FormatInt(id, 10); want != all[i] {
					line = all[i] + ": " + line
				}
				st[id] = line
			}
			all = all[n:]
		}
		return st
	}
}

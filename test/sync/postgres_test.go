package synctest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// pgServer is one PostgreSQL server of FIT_SYNC_PG ("13=127.0.0.1:29513,..."),
// with its CA from FIT_SYNC_PG_CA_DIR/<version>.pem (test/sync/postgres.sh).
type pgServer struct {
	version, addr string
	roots         *x509.CertPool
}

func pgServers(t *testing.T) []pgServer {
	t.Helper()
	list := os.Getenv("FIT_SYNC_PG")
	if list == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_PG is not set")
		}
		t.Skip("no PostgreSQL: FIT_SYNC_PG is not set (test/sync/run.sh or test/sync/postgres.sh sets it)")
	}
	var out []pgServer
	for _, kv := range strings.Split(list, ",") {
		v, addr, _ := strings.Cut(kv, "=")
		pem, err := os.ReadFile(filepath.Join(os.Getenv("FIT_SYNC_PG_CA_DIR"), v+".pem"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatalf("no certificate in the CA of %s", v)
		}
		out = append(out, pgServer{version: v, addr: addr, roots: roots})
	}
	return out
}

// eachPG runs fn against every server, in parallel.
func eachPG(t *testing.T, fn func(t *testing.T, s pgServer)) {
	for _, s := range pgServers(t) {
		t.Run("pg"+s.version, func(t *testing.T) {
			t.Parallel()
			fn(t, s)
		})
	}
}

func (s pgServer) config() pgwire.Config {
	return pgwire.Config{
		Addr: s.addr, User: os.Getenv("FIT_SYNC_PG_USER"), Password: os.Getenv("FIT_SYNC_PG_PASSWORD"),
		Database: "fit", TLS: &tls.Config{RootCAs: s.roots},
	}
}

func pgCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute) // a hang guard
	t.Cleanup(cancel)
	return ctx
}

func (s pgServer) connect(t *testing.T, edit func(*pgwire.Config)) *pgwire.Conn {
	t.Helper()
	cfg := s.config()
	if edit != nil {
		edit(&cfg)
	}
	c, err := pgwire.Connect(pgCtx(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func pgExec(t *testing.T, c *pgwire.Conn, sql string, args ...any) string {
	t.Helper()
	tag, err := c.Exec(pgCtx(t), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return tag
}

// pgOne returns the single row of sql.
func pgOne(t *testing.T, c *pgwire.Conn, sql string, args ...any) []any {
	t.Helper()
	rows, err := c.Query(pgCtx(t), sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	var out []any
	for rows.Next() {
		if out != nil {
			t.Fatalf("%s: more than one row", sql)
		}
		out = append([]any{}, rows.Values()...)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func TestPostgresAuth(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		if c.TLS() == nil || !c.ChannelBound() {
			t.Fatalf("TLS %v, channel bound %v: verify-full with SCRAM-SHA-256-PLUS expected", c.TLS() != nil, c.ChannelBound())
		}
		if got := pgOne(t, c, "SELECT current_setting('server_version_num')::int / 10000"); got[0] != int64(c.ServerVersion()/10000) || strconv.Itoa(c.ServerVersion()/10000) != s.version {
			t.Fatalf("server version %v, %d", got, c.ServerVersion())
		}
		if got := pgOne(t, c, "SELECT ssl FROM pg_stat_ssl WHERE pid = pg_backend_pid()"); got[0] != true {
			t.Fatalf("pg_stat_ssl %v", got)
		}

		c = s.connect(t, func(cfg *pgwire.Config) { cfg.ChannelBinding = pgwire.ChannelBindingRequire })
		if !c.ChannelBound() {
			t.Fatal("channel binding required and not used")
		}
		c = s.connect(t, func(cfg *pgwire.Config) { cfg.ChannelBinding = pgwire.ChannelBindingDisable })
		if c.ChannelBound() || c.TLS() == nil {
			t.Fatal("channel binding disabled and used")
		}
		pgOne(t, c, "SELECT 1")
		c = s.connect(t, func(cfg *pgwire.Config) { cfg.SSLMode = pgwire.SSLDisable })
		if c.ChannelBound() || c.TLS() != nil {
			t.Fatal("TLS disabled and used")
		}
		pgOne(t, c, "SELECT 1")
		c = s.connect(t, func(cfg *pgwire.Config) {
			cfg.SSLMode = pgwire.SSLVerifyCA
			cfg.TLS.ServerName = "not-the-server.example"
		})
		pgOne(t, c, "SELECT 1")

		ctx := pgCtx(t)
		cfg := s.config()
		cfg.TLS.ServerName = "not-the-server.example"
		if _, err := pgwire.Connect(ctx, cfg); err == nil || !strings.Contains(err.Error(), "certificate") {
			t.Fatalf("verify-full accepted a wrong name: %v", err)
		}
		cfg = s.config()
		cfg.TLS.RootCAs = x509.NewCertPool()
		cfg.SSLMode = pgwire.SSLVerifyCA
		if _, err := pgwire.Connect(ctx, cfg); err == nil {
			t.Fatal("verify-ca accepted an unknown CA")
		}
		for _, mode := range []string{pgwire.SSLDisable, pgwire.SSLVerifyFull} {
			cfg = s.config()
			cfg.SSLMode = mode
			cfg.Password = "wrong"
			var pe *pgwire.Error
			if _, err := pgwire.Connect(ctx, cfg); !errors.As(err, &pe) || pe.Code != "28P01" {
				t.Fatalf("%s: a wrong password: %v", mode, err)
			}
		}
	})
}

func TestPostgresTypes(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		pgExec(t, c, `CREATE TEMP TABLE types (
			id int PRIMARY KEY, b bool, i2 int2, i4 int4, i8 int8, f4 float4, f8 float8, n numeric(20,5),
			t text, vc varchar(10), bp char(3), by bytea, d date, ts timestamp, tz timestamptz,
			j json, jb jsonb, u uuid, ai int4[], at text[], an numeric[], af float8[], au uuid[], ad date[],
			a2 int8[], ae int4[], iv interval, tm time, ip inet, nan float8, tsi timestamp, big text, o oid, nm name)`)
		bigText := strings.Repeat("0123456789", 100_000)
		bj := []byte{0, 1, 2, 0xfe, 0xff}
		tz := time.Date(2024, 2, 29, 13, 14, 15, 123456000, time.FixedZone("", 8*3600))
		args := []any{
			1, true, int16(-32768), int32(math.MaxInt32), int64(math.MinInt64), float32(1.5), math.Pi,
			pgwire.Numeric("-12345678901234.12345"), "héllo, 世界 \"'\\\t\n", "abc", "ab", bj,
			time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), time.Date(1999, 12, 31, 23, 59, 59, 999999000, time.UTC), tz,
			pgwire.JSON(`{"a": [1, 2]}`), pgwire.JSON(`{"b":1,"a":"x"}`), "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
			[]int{1, 2, 3}, []any{"x", nil, `y,"z"`}, []string{"1.5", "-2"}, []float64{1.25, math.Inf(1)},
			[]string{"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"}, []time.Time{time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
			[][]int64{{1, 2}, {3, 4}}, []int{}, 90 * time.Second, "13:14:15.5", "10.0.0.1/8", math.NaN(), "infinity",
			bigText, uint32(4000000000), "a_name",
		}
		marks := make([]string, len(args))
		for i := range marks {
			marks[i] = "$" + strconv.Itoa(i+1)
		}
		if tag := pgExec(t, c, "INSERT INTO types VALUES ("+strings.Join(marks, ",")+")", args...); tag != "INSERT 0 1" {
			t.Fatalf("tag %q", tag)
		}
		nulls := []any{2}
		for range args[1:] {
			nulls = append(nulls, nil)
		}
		pgExec(t, c, "INSERT INTO types VALUES ("+strings.Join(marks, ",")+")", nulls...)

		got := pgOne(t, c, "SELECT * FROM types WHERE id = $1", 1)
		want := []any{
			int64(1), true, int64(-32768), int64(math.MaxInt32), int64(math.MinInt64), 1.5, math.Pi,
			pgwire.Numeric("-12345678901234.12345"), "héllo, 世界 \"'\\\t\n", "abc", "ab ", bj,
			time.Date(2024, 2, 29, 0, 0, 0, 0, time.UTC), time.Date(1999, 12, 31, 23, 59, 59, 999999000, time.UTC), tz.UTC(),
			pgwire.JSON(`{"a": [1, 2]}`), pgwire.JSON(`{"a": "x", "b": 1}`), "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
			[]any{int64(1), int64(2), int64(3)}, []any{"x", nil, `y,"z"`}, []any{pgwire.Numeric("1.5"), pgwire.Numeric("-2")},
			[]any{1.25, math.Inf(1)}, []any{"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"}, []any{time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)},
			[]any{[]any{int64(1), int64(2)}, []any{int64(3), int64(4)}}, []any{}, "00:01:30", "13:14:15.5", "10.0.0.1/8",
			"NaN", pgwire.Infinity, bigText, int64(4000000000), "a_name",
		}
		if len(got) != len(want) {
			t.Fatalf("%d columns, want %d", len(got), len(want))
		}
		for i := range want {
			if f, ok := got[i].(float64); ok && math.IsNaN(f) && want[i] == "NaN" {
				continue
			}
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("column %d: %#v, want %#v", i, got[i], want[i])
			}
		}
		for i, v := range pgOne(t, c, "SELECT * FROM types WHERE id = 2")[1:] {
			if v != nil {
				t.Errorf("column %d of the NULL row: %#v", i+1, v)
			}
		}

		// A named statement, run twice, then closed.
		st, err := c.Prepare(pgCtx(t), "by_id", "SELECT i8, t FROM types WHERE id = $1")
		if err != nil {
			t.Fatal(err)
		}
		if len(st.Params()) != 1 || st.Params()[0] != pgwire.OIDInt4 || len(st.Fields()) != 2 {
			t.Fatalf("params %v fields %v", st.Params(), st.Fields())
		}
		for _, id := range []int{1, 2} {
			rows, err := st.Query(pgCtx(t), id)
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for rows.Next() {
				n++
			}
			if err := rows.Close(); err != nil || n != 1 || rows.Tag() != "SELECT 1" {
				t.Fatalf("%d rows, %q, %v", n, rows.Tag(), err)
			}
		}
		if err := st.Close(pgCtx(t)); err != nil {
			t.Fatal(err)
		}

		// Errors leave the connection usable, and the transaction's state is
		// followed.
		var pe *pgwire.Error
		if _, err := c.Exec(pgCtx(t), "INSERT INTO types (id) VALUES ($1)", 1); !errors.As(err, &pe) || pe.Code != "23505" || pe.Constraint == "" {
			t.Fatalf("duplicate key: %v", err)
		}
		if _, err := c.Query(pgCtx(t), "SELEC 1"); !errors.As(err, &pe) || pe.Code != "42601" {
			t.Fatalf("syntax: %v", err)
		}
		if _, err := c.Query(pgCtx(t), "SELECT $1::int", "x"); !errors.As(err, &pe) || pe.Code != "22P02" {
			t.Fatalf("bad parameter: %v", err)
		}
		pgExec(t, c, "BEGIN")
		if c.TxStatus() != 'T' {
			t.Fatalf("status %c", c.TxStatus())
		}
		c.Exec(pgCtx(t), "SELECT 1/0")
		if c.TxStatus() != 'E' {
			t.Fatalf("status %c", c.TxStatus())
		}
		pgExec(t, c, "ROLLBACK")
		if c.TxStatus() != 'I' {
			t.Fatalf("status %c", c.TxStatus())
		}
		res, err := c.SimpleQuery(pgCtx(t), "SELECT 1 AS a; SELECT 'x' AS b, NULL AS c; SET application_name = 'fit-test'")
		if err != nil || len(res) != 3 || string(res[0].Rows[0][0]) != "1" || res[1].Rows[0][1] != nil || res[2].Tag != "SET" {
			t.Fatalf("%v %+v", err, res)
		}
		if c.ParameterStatus("application_name") != "fit-test" {
			t.Fatalf("application_name %q", c.ParameterStatus("application_name"))
		}
	})
}

func TestPostgresCopy(t *testing.T) {
	n := 1_000_000
	if v := os.Getenv("FIT_SYNC_PG_COPY_ROWS"); v != "" {
		n, _ = strconv.Atoi(v)
	}
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		pgExec(t, c, "CREATE TEMP TABLE c (id int8, name text, v float8, b bytea, t timestamptz)")
		w, err := c.CopyIn(pgCtx(t), "COPY c FROM STDIN")
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		for i := range n {
			var name any = "row\t" + strconv.Itoa(i)
			if i%1000 == 0 {
				name = nil
			}
			if err := w.WriteRow(i, name, float64(i)/4, []byte{byte(i), '\\'}, at.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		copied, err := w.Close()
		if err != nil || copied != int64(n) {
			t.Fatalf("copied %d, %v", copied, err)
		}
		got := pgOne(t, c, `SELECT count(*), sum(id)::text, count(name), sum(v)::text, count(*) FILTER (WHERE b = decode(lpad(to_hex(id % 256), 2, '0') || '5c', 'hex')),
			count(*) FILTER (WHERE t = $1::timestamptz + id * interval '1 second') FROM c`, at)
		sum := int64(n) * int64(n-1) / 2
		want := []any{int64(n), strconv.FormatInt(sum, 10), int64(n - (n+999)/1000), strconv.FormatFloat(float64(sum)/4, 'f', -1, 64), int64(n), int64(n)}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%v, want %v", got, want)
		}
		if got := pgOne(t, c, "SELECT name FROM c WHERE id = 7"); got[0] != "row\t7" {
			t.Fatalf("%q", got[0])
		}

		// COPY TO gives the rows back.
		var out bytes.Buffer
		if m, err := c.CopyTo(pgCtx(t), "COPY (SELECT id, name FROM c WHERE id < 3 ORDER BY id) TO STDOUT", &out); err != nil || m != 3 {
			t.Fatalf("%d %v", m, err)
		}
		if out.String() != "0\t\\N\n1\trow\\t1\n2\trow\\t2\n" {
			t.Fatalf("%q", out.String())
		}

		// An aborted copy writes nothing, and the connection goes on; so does
		// one the server refuses.
		w, err = c.CopyIn(pgCtx(t), "COPY c (id) FROM STDIN")
		if err != nil {
			t.Fatal(err)
		}
		w.WriteRow(-1)
		if err := w.Abort("changed my mind"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.CopyFrom(pgCtx(t), "COPY c (id) FROM STDIN", strings.NewReader("1\nnot a number\n")); err == nil {
			t.Fatal("bad data copied")
		}
		if got := pgOne(t, c, "SELECT count(*) FROM c WHERE id < 0 OR id >= $1", n); got[0] != int64(0) {
			t.Fatalf("%v rows from the failed copies", got)
		}
	})
}

// The replication connection's commands, as I178 and I179 use them: a slot
// exporting a snapshot that another connection reads in, then the slot's
// stream from its consistent point, and a standby status update moving its
// confirmed_flush_lsn.
func TestPostgresReplication(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		suffix := s.version + "_" + strconv.FormatInt(time.Now().UnixNano(), 36)
		table, pub, slot := "rep_"+suffix, "pub_"+suffix, "slot_"+suffix
		pgExec(t, c, "CREATE TABLE "+table+" (id int PRIMARY KEY, v text)")
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) })
		pgExec(t, c, "INSERT INTO "+table+" SELECT g, 'before' FROM generate_series(1, 10) g")
		pgExec(t, c, "CREATE PUBLICATION "+pub+" FOR TABLE "+table)
		t.Cleanup(func() { c.Exec(context.Background(), "DROP PUBLICATION IF EXISTS "+pub) })

		r := s.connect(t, func(cfg *pgwire.Config) { cfg.Replication = true })
		sys, err := r.IdentifySystem(pgCtx(t))
		if err != nil || sys.DBName != "fit" || sys.Timeline != 1 || sys.XLogPos == 0 || sys.SystemID == "" {
			t.Fatalf("%+v %v", sys, err)
		}
		sl, err := r.CreateReplicationSlot(pgCtx(t), slot, pgwire.SlotOptions{Snapshot: pgwire.SnapshotExport})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			d := s.connect(t, func(cfg *pgwire.Config) { cfg.Replication = true })
			d.DropReplicationSlot(context.Background(), slot, true)
		})
		if sl.Name != slot || sl.Plugin != "pgoutput" || sl.SnapshotName == "" || sl.ConsistentPoint < sys.XLogPos {
			t.Fatalf("%+v", sl)
		}

		// Rows written after the slot are not in its snapshot; they come in
		// the stream.
		pgExec(t, c, "INSERT INTO "+table+" VALUES (11, 'after'), (12, 'after')")
		pgExec(t, c, "UPDATE "+table+" SET v = 'changed' WHERE id = 1")
		snap := s.connect(t, nil)
		pgExec(t, snap, "BEGIN ISOLATION LEVEL REPEATABLE READ")
		pgExec(t, snap, "SET TRANSACTION SNAPSHOT "+pgwire.QuoteLiteral(sl.SnapshotName))
		if got := pgOne(t, snap, "SELECT count(*), count(*) FILTER (WHERE v = 'before') FROM "+table); !reflect.DeepEqual(got, []any{int64(10), int64(10)}) {
			t.Fatalf("the snapshot sees %v", got)
		}
		pgExec(t, snap, "COMMIT")

		proto := "1"
		if r.ServerVersion() >= 140000 {
			proto = "2"
		}
		if err := r.StartReplication(pgCtx(t), slot, sl.ConsistentPoint, "proto_version", proto, "publication_names", pub); err != nil {
			t.Fatal(err)
		}
		if _, err := r.SimpleQuery(pgCtx(t), "SELECT 1"); !errors.Is(err, pgwire.ErrBusy) {
			t.Fatalf("a query in the stream: %v", err)
		}
		var kinds []byte
		var last pgwire.LSN
		for commits := 0; commits < 2; {
			m, err := r.ReceiveReplication(pgCtx(t))
			if err != nil {
				t.Fatal(err)
			}
			switch m := m.(type) {
			case *pgwire.XLogData:
				if len(m.Data) == 0 {
					t.Fatal("empty XLogData")
				}
				kinds = append(kinds, m.Data[0])
				if m.Data[0] == 'C' {
					commits++
				}
				last = max(last, m.WALStart, m.ServerEnd)
			case *pgwire.Keepalive:
				if m.ReplyRequested {
					r.SendStandbyStatus(pgCtx(t), pgwire.StandbyStatus{Write: last, Flush: sl.ConsistentPoint, Apply: sl.ConsistentPoint})
				}
			}
		}
		if string(kinds) != "BRIICBUC" && string(kinds) != "BRIICBRUC" {
			t.Fatalf("pgoutput messages %q", kinds)
		}
		if err := r.SendStandbyStatus(pgCtx(t), pgwire.StandbyStatus{Write: last, Flush: last, Apply: last, ReplyRequested: true}); err != nil {
			t.Fatal(err)
		}
		// The server answers a reply request with a keepalive; it has the
		// status by then.
		for {
			m, err := r.ReceiveReplication(pgCtx(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := m.(*pgwire.Keepalive); ok {
				break
			}
		}
		if err := r.EndReplication(pgCtx(t)); err != nil {
			t.Fatal(err)
		}
		got := pgOne(t, c, "SELECT confirmed_flush_lsn::text FROM pg_replication_slots WHERE slot_name = $1", slot)
		if flushed, err := pgwire.ParseLSN(got[0].(string)); err != nil || flushed != last {
			t.Fatalf("confirmed_flush_lsn %v, sent %v (%v)", got[0], last, err)
		}
		if _, err := r.IdentifySystem(pgCtx(t)); err != nil {
			t.Fatalf("a command after the stream: %v", err)
		}
		if err := r.DropReplicationSlot(pgCtx(t), slot, false); err != nil {
			t.Fatal(err)
		}
		if got := pgOne(t, c, "SELECT count(*) FROM pg_replication_slots WHERE slot_name = $1", slot); got[0] != int64(0) {
			t.Fatal("the slot is still there")
		}
		if _, err := c.IdentifySystem(pgCtx(t)); err == nil {
			t.Fatal("a replication command on a normal connection")
		}
	})
}

// A context that ends cancels the query on the server and breaks the
// connection.
func TestPostgresCancel(t *testing.T) {
	eachPG(t, func(t *testing.T, s pgServer) {
		c := s.connect(t, nil)
		pid := c.BackendPID()
		ctx, cancel := context.WithTimeout(pgCtx(t), 300*time.Millisecond)
		defer cancel()
		_, err := c.Exec(ctx, "SELECT pg_sleep(600)")
		if !errors.Is(err, context.DeadlineExceeded) || c.Err() == nil {
			t.Fatalf("%v, %v", err, c.Err())
		}
		o := s.connect(t, nil)
		for {
			got := pgOne(t, o, "SELECT count(*) FROM pg_stat_activity WHERE pid = $1 AND state = 'active'", int64(pid))
			if got[0] == int64(0) {
				break
			}
			select {
			case <-pgCtx(t).Done():
				t.Fatal("the query was not cancelled")
			case <-time.After(50 * time.Millisecond):
			}
		}
	})
}

var _ = fmt.Sprint

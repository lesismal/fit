package syncpgsink

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// load loads the sink with params, as a pipeline's config does.
func load(t *testing.T, params map[string]any) (*Sink, error) {
	t.Helper()
	p := map[string]any{"name": "p", "source": map[string]any{"module": "generate"},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "postgres", "params": params}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	m, err := module.Load(fitsync.Sinks, cfg.Sync.Pipelines[0].Sinks[0].Sink, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Sink), nil
}

// sinkOf loads the sink against f, without TLS.
func sinkOf(t *testing.T, f *fake, params map[string]any) *Sink {
	t.Helper()
	params["address"] = f.addr()
	params["user"] = "fit"
	params["sslmode"] = "disable"
	s, err := load(t, params)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// certFiles writes a self-signed certificate and its key, and returns their
// paths.
func certFiles(t *testing.T) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "c"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	kder, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	cert, kf := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kf, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	return cert, kf
}

func TestProvision(t *testing.T) {
	t.Setenv("FIT_TEST_PG_PW", "pw")
	cert, key := certFiles(t)
	notPEM := filepath.Join(t.TempDir(), "x.pem")
	os.WriteFile(notPEM, []byte("no"), 0o600)
	for _, tc := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"user": "u"}, ""},
		{map[string]any{"user": "u", "password_env": "FIT_TEST_PG_PW", "ca_file": cert, "cert_file": cert, "key_file": key, "server_name": "db",
			"table": "public.{namespace}_copy", "key_columns": []string{"id"}, "transactions": "source", "copy": "never"}, ""},
		{map[string]any{}, "user"},
		{map[string]any{"user": "u", "address": "nohost"}, "not a host and port"},
		{map[string]any{"user": "u", "password_env": "FIT_TEST_PG_UNSET"}, "not set"},
		{map[string]any{"user": "u", "password_env": "1-x"}, "not an environment variable's name"},
		{map[string]any{"user": "u", "ca_file": notPEM}, "holds no PEM certificate"},
		{map[string]any{"user": "u", "ca_file": notPEM + ".missing"}, "no such file"},
		{map[string]any{"user": "u", "cert_file": notPEM, "key_file": notPEM}, "cert_file"},
		{map[string]any{"user": "u", "cert_file": cert}, "go together"},
		{map[string]any{"user": "u", "sslmode": "disable", "ca_file": cert}, "ca_file needs TLS"},
		{map[string]any{"user": "u", "sslmode": "disable", "channel_binding": "require"}, "channel binding needs TLS"},
		{map[string]any{"user": "u", "table": "{table}"}, "brace"},
		{map[string]any{"user": "u", "key_columns": []string{"a", "a"}}, "named twice"},
		{map[string]any{"user": "u", "key_columns": []string{""}}, "without a name"},
	} {
		_, err := load(t, tc.params)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%v: %v", tc.params, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%v: %v, want %q", tc.params, err, tc.want)
		}
	}
}

func TestParam(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.FixedZone("x", 3600))
	for _, c := range []struct {
		v    fitsync.Value
		want any
	}{
		{fitsync.Null(), nil},
		{fitsync.Bool(true), true},
		{fitsync.Int(-3), int64(-3)},
		{fitsync.Uint(math.MaxUint64), uint64(math.MaxUint64)},
		{fitsync.Float(0.5), 0.5},
		{fitsync.Decimal("1.50"), pgwire.Numeric("1.50")},
		{fitsync.String("s"), "s"},
		{fitsync.Bytes([]byte{0, 1}), `\x0001`},
		{fitsync.Time(at), at},
		{fitsync.Date(2026, 3, 4), "2026-03-04"},
		{fitsync.JSON([]byte(`{"a":1}`)), pgwire.JSON(`{"a":1}`)},
		{fitsync.Array(fitsync.Int(1), fitsync.Null(), fitsync.Map(fitsync.Field{Name: "a", Value: fitsync.Int(1)})), []any{int64(1), nil, pgwire.JSON(`{"a":1}`)}},
		{fitsync.Map(fitsync.Field{Name: "b", Value: fitsync.Bytes([]byte("x"))}), pgwire.JSON(`{"b":"eA=="}`)},
	} {
		got, err := Param(c.v)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: %#v %v, want %#v", c.v, got, err, c.want)
		}
	}
	for _, v := range []fitsync.Value{fitsync.String("a\x00"), fitsync.Array(fitsync.String("\x00")),
		fitsync.Map(fitsync.Field{Name: "s", Value: fitsync.String("\x00")})} {
		if _, err := Param(v); err == nil {
			t.Errorf("%v: no error", v)
		}
	}
	if QuoteTable("shop.orders") != `"shop"."orders"` || QuoteTable(`a"b`) != `"a""b"` {
		t.Error(QuoteTable("shop.orders"))
	}
}

func col(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }

// rec makes a record of ns keyed by id.
func rec(ns string, op fitsync.Op, id int64, fields ...fitsync.Field) *fitsync.Record {
	r := &fitsync.Record{Namespace: ns, Op: op, Key: fitsync.Map(col("id", fitsync.Int(id)))}
	if op == fitsync.OpDelete {
		r.Before = fitsync.Map(fields...)
	} else {
		r.After = fitsync.Map(fields...)
	}
	return r
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	t.Cleanup(cancel)
	return c
}

func TestWrite(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"max_statement_rows": 2, "copy": "never"})
	v := func(s string) fitsync.Field { return col("v", fitsync.String(s)) }
	batch := []*fitsync.Record{
		rec("shop.a", fitsync.OpInsert, 1, v("a1")),
		rec("shop.a", fitsync.OpInsert, 2, v("a2")),
		rec("shop.a", fitsync.OpUpdate, 1, v("a1'")), // the same key: replaces the row in the statement
		rec("shop.a", fitsync.OpInsert, 3, v("a3")),  // a statement of its own: two rows at most
		{Namespace: "shop.a", Op: fitsync.OpDDL},
		rec("shop.b", fitsync.OpInsert, 1, col("id", fitsync.Int(1)), v("b1")),
		rec("shop.b", fitsync.OpInsert, 2), // only the key's column: DO NOTHING
		rec("shop.a", fitsync.OpDelete, 2, v("a2")),
		rec("shop.a", fitsync.OpDelete, 2, v("a2")),
		rec("shop.a", fitsync.OpMessage, 4, v("a4")),
	}
	if err := s.Write(ctx(t), batch); err != nil {
		t.Fatal(err)
	}
	if got := dump(f.table(`"shop"."a"`), "id", "v"); got != "[1|a1' 3|a3 4|a4]" {
		t.Error(got)
	}
	if got := dump(f.table(`"shop"."b"`), "id", "v"); got != "[1|b1 2|NULL]" {
		t.Error(got)
	}
	want := []string{
		"BEGIN",
		`INSERT INTO "shop"."a" ("v","id") VALUES ($1,$2),($3,$4) ON CONFLICT ("id") DO UPDATE SET "v" = EXCLUDED."v"`,
		`INSERT INTO "shop"."a" ("v","id") VALUES ($1,$2),($3,$4) ON CONFLICT ("id") DO UPDATE SET "v" = EXCLUDED."v"`,
		`INSERT INTO "shop"."b" ("id","v") VALUES ($1,$2) ON CONFLICT ("id") DO UPDATE SET "v" = EXCLUDED."v"`,
		`INSERT INTO "shop"."b" ("id") VALUES ($1) ON CONFLICT ("id") DO NOTHING`,
		`DELETE FROM "shop"."a" WHERE "id" IN ($1)`,
		`INSERT INTO "shop"."a" ("v","id") VALUES ($1,$2) ON CONFLICT ("id") DO UPDATE SET "v" = EXCLUDED."v"`,
		"COMMIT",
	}
	if got := f.statements(); !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
	// Written again, the same rows.
	if err := s.Write(ctx(t), batch); err != nil {
		t.Fatal(err)
	}
	if got := dump(f.table(`"shop"."a"`), "id", "v"); got != "[1|a1' 3|a3 4|a4]" {
		t.Error(got)
	}
	// Nothing to write: no transaction.
	f.clearLog()
	if err := s.Write(ctx(t), []*fitsync.Record{{Op: fitsync.OpDDL}}); err != nil || len(f.statements()) != 0 {
		t.Error(err, f.statements())
	}
	if err := s.Test(ctx(t)); err != nil {
		t.Error(err)
	}
	f.mu.Lock()
	conns := f.conns
	f.mu.Unlock()
	if conns != 1 {
		t.Errorf("%d connections, want 1 kept", conns)
	}
}

func TestKeys(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"key_columns": []string{"a", "b"}, "table": "t_{$namespace}"})
	r := &fitsync.Record{Namespace: "x", Op: fitsync.OpInsert, Key: fitsync.Map(col("a", fitsync.Int(1))),
		After: fitsync.Map(col("b", fitsync.String("k")), col("v", fitsync.Int(5)))}
	d := &fitsync.Record{Namespace: "x", Op: fitsync.OpDelete, Key: fitsync.Map(col("a", fitsync.Int(1))),
		Before: fitsync.Map(col("b", fitsync.String("k")))}
	if err := s.Write(ctx(t), []*fitsync.Record{r}); err != nil {
		t.Fatal(err)
	}
	if got := dump(f.table(`"t_x"`), "a", "b", "v"); got != "[1|k|5]" {
		t.Error(got)
	}
	if err := s.Write(ctx(t), []*fitsync.Record{d}); err != nil {
		t.Fatal(err)
	}
	if got := f.statements(); got[len(got)-2] != `DELETE FROM "t_x" WHERE ("a","b") IN (($1,$2))` || len(f.table(`"t_x"`)) != 0 {
		t.Errorf("%q", got)
	}
	// A scalar key, for one key column.
	s1 := sinkOf(t, f, map[string]any{"key_columns": []string{"id"}})
	sc := &fitsync.Record{Namespace: "y", Op: fitsync.OpInsert, Key: fitsync.Int(9), After: fitsync.Map(col("v", fitsync.Int(1)))}
	if err := s1.Write(ctx(t), []*fitsync.Record{sc}); err != nil {
		t.Fatal(err)
	}
	if got := dump(f.table(`"y"`), "id", "v"); got != "[9|1]" {
		t.Error(got)
	}
}

func TestRecordErrors(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{})
	sk := sinkOf(t, f, map[string]any{"key_columns": []string{"k"}})
	good := rec("t", fitsync.OpInsert, 1, col("v", fitsync.Int(1)), col("k", fitsync.Int(1)))
	for _, c := range []struct {
		s    *Sink
		r    *fitsync.Record
		want string
	}{
		{s, &fitsync.Record{Namespace: "t", Op: 99}, "operation"},
		{s, rec("", fitsync.OpInsert, 1), "not a table's name"},
		{s, rec("a.", fitsync.OpInsert, 1), "not a table's name"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.Map(col("id", fitsync.Int(1)))}, "not a map of columns"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.Int(1), After: fitsync.Map()}, "set key_columns"},
		{s, rec("t", fitsync.OpInsert, 1, col("v", fitsync.String("\x00"))), "column v"},
		{s, rec("t", fitsync.OpInsert, 1, col("v", fitsync.Int(1)), col("v", fitsync.Int(2))), "twice"},
		{s, &fitsync.Record{Namespace: "t", Op: fitsync.OpDelete, Key: fitsync.Map(col("id", fitsync.String("\x00")))}, "key column id"},
		{sk, rec("t", fitsync.OpInsert, 1, col("v", fitsync.Int(1))), "no key column k"},
		{sk, &fitsync.Record{Namespace: "t", Op: fitsync.OpInsert, Key: fitsync.Map(col("k", fitsync.String("\x00"))), After: fitsync.Map()}, "key column k"},
	} {
		err := c.s.Write(ctx(t), []*fitsync.Record{good, c.r})
		var be *fitsync.BatchError
		if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.r, err, c.want)
		}
	}
	if len(f.table(`"t"`)) != 1 {
		t.Error("the record before the bad one is not written")
	}
}

// bad fails each statement with an argument "bad" with the SQLSTATE code.
func bad(code string) func(string, []*string) string {
	return func(_ string, args []*string) string {
		for _, a := range args {
			if a != nil && *a == "bad" {
				return code
			}
		}
		return ""
	}
}

func TestServerErrors(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"copy": "never"})
	v := func(s string) fitsync.Field { return col("v", fitsync.String(s)) }
	batch := []*fitsync.Record{rec("t", fitsync.OpInsert, 1, v("a")), rec("t", fitsync.OpInsert, 2, v("b")),
		rec("t", fitsync.OpInsert, 3, v("bad")), rec("t", fitsync.OpInsert, 4, v("d"))}

	// Refused for good: the rows before it written, one at a time.
	f.setInject(bad("22P02"))
	err := s.Write(ctx(t), batch)
	var be *fitsync.BatchError
	if !errors.As(err, &be) || be.Index != 2 || !fitsync.IsPermanent(err) {
		t.Fatal(err)
	}
	if got := dump(f.table(`"t"`), "id", "v"); got != "[1|a 2|b]" {
		t.Error(got)
	}
	// Refused only together: written one at a time.
	f.setInject(func(sql string, args []*string) string {
		if strings.Count(sql, "$") > 2 {
			return "21000"
		}
		return ""
	})
	if err := s.Write(ctx(t), batch[3:]); err != nil {
		t.Error(err)
	}
	for _, c := range []struct {
		inject    func(string, []*string) string
		permanent bool
	}{
		{bad("42P01"), false}, // the table missing: tried again
		{func(sql string, _ []*string) string { // a connection lost
			if strings.HasPrefix(sql, "INSERT") {
				return "drop"
			}
			return ""
		}, false},
		{func(sql string, _ []*string) string { // BEGIN refused
			if sql == "BEGIN" {
				return "57P01"
			}
			return ""
		}, false},
		{func(sql string, _ []*string) string { // COMMIT refused
			if sql == "COMMIT" {
				return "40001"
			}
			return ""
		}, false},
	} {
		f.setInject(c.inject)
		err := s.Write(ctx(t), batch[2:3])
		if err == nil || fitsync.IsPermanent(err) != c.permanent || errors.As(err, &be) {
			t.Errorf("%v", err)
		}
	}
	// Errors while looking for the bad record: nothing written.
	for _, after := range []string{"BEGIN", "SAVEPOINT fit_sync", "ROLLBACK TO SAVEPOINT fit_sync", "COMMIT", "INSERT"} {
		// The rows are refused together, and the bad one alone; once
		// looking one at a time (after the first ROLLBACK), the first
		// statement named after fails as a connection's would.
		looking, done := false, false
		f.setInject(func(sql string, args []*string) string {
			switch {
			case sql == "ROLLBACK":
				looking = true
			case looking && !done && strings.HasPrefix(sql, after):
				done = true
				if after == "INSERT" {
					return "08006"
				}
				return "57P01"
			}
			return bad("23505")(sql, args)
		})
		before := dump(f.table(`"t"`), "id", "v")
		err := s.Write(ctx(t), []*fitsync.Record{rec("t", fitsync.OpInsert, 7, v("x")), rec("t", fitsync.OpInsert, 8, v("bad"))})
		if err == nil || errors.As(err, &be) || fitsync.IsPermanent(err) {
			t.Errorf("%s: %v", after, err)
		}
		if after != "COMMIT" && dump(f.table(`"t"`), "id", "v") != before {
			t.Errorf("%s: written", after)
		}
	}
	// No server.
	f.setInject(nil)
	s2 := sinkOf(t, f, map[string]any{})
	s2.pool.cfg.Addr = "127.0.0.1:1"
	if err := s2.Write(ctx(t), batch[:1]); err == nil {
		t.Error("no error without a server")
	}
	if err := s2.Test(ctx(t)); err == nil {
		t.Error("no error without a server")
	}
}

func TestPool(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"timeout": "5s"})
	s.ParallelWrites()
	if s.timeout() != 5*time.Second {
		t.Error(s.timeout())
	}
	c, err := s.pool.get(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	s.Cleanup()
	s.pool.put(c) // closed, not kept
	if len(s.pool.idle) != 0 {
		t.Error("a connection kept after Cleanup")
	}
}

func TestPermanent(t *testing.T) {
	for code, want := range map[string]bool{"22P02": true, "23505": true, "23502": true, "42P01": false, "40001": false, "21000": false} {
		if Permanent(&pgwire.Error{Code: code}) != want {
			t.Errorf("%s", code)
		}
	}
	if Permanent(errors.New("x")) {
		t.Error("not a server's error")
	}
}

func TestCopy(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"copy_min_rows": 3})
	var batch []*fitsync.Record
	for i := range 5 {
		batch = append(batch, rec("t", fitsync.OpInsert, int64(i), col("v", fitsync.String(fmt.Sprintf("v\t%d\\", i))),
			col("n", fitsync.Null()), col("b", fitsync.Bytes([]byte{1, 2}))))
	}
	batch = append(batch, rec("t", fitsync.OpInsert, 0, col("v", fitsync.String("again")), col("n", fitsync.Null()), col("b", fitsync.Bytes(nil))))
	if err := s.Write(ctx(t), batch); err != nil {
		t.Fatal(err)
	}
	if got := dump(f.table(`"t"`), "id", "v", "n", "b"); got != `[0|again|NULL|\x 1|v	1\|NULL|\x0102 2|v	2\|NULL|\x0102 3|v	3\|NULL|\x0102 4|v	4\|NULL|\x0102]` {
		t.Error(got)
	}
	st := f.statements()
	if len(st) != 5 || !strings.HasPrefix(st[1], `CREATE TEMPORARY TABLE "fit_sync_copy_1" ON COMMIT DROP AS SELECT "v","n","b","id" FROM "t" WITH NO DATA`) ||
		st[2] != `COPY "fit_sync_copy_1" ("v","n","b","id") FROM STDIN` ||
		st[3] != `INSERT INTO "t" ("v","n","b","id") SELECT "v","n","b","id" FROM "fit_sync_copy_1" ON CONFLICT ("id") DO UPDATE SET "v" = EXCLUDED."v", "n" = EXCLUDED."n", "b" = EXCLUDED."b"` {
		t.Errorf("%q", st)
	}
	// Two rows: not copied; with copy always, copied.
	f.clearLog()
	if err := s.Write(ctx(t), batch[:2]); err != nil || strings.Contains(strings.Join(f.statements(), ";"), "COPY") {
		t.Error(err, f.statements())
	}
	sa := sinkOf(t, f, map[string]any{"copy": "always"})
	f.clearLog()
	if err := sa.Write(ctx(t), batch[:1]); err != nil || !strings.Contains(strings.Join(f.statements(), ";"), "COPY") {
		t.Error(err, f.statements())
	}
	// Each step refused.
	for _, step := range []string{"CREATE", "COPY", "INSERT"} {
		f.setInject(func(sql string, _ []*string) string {
			if strings.HasPrefix(sql, step) {
				return "42501"
			}
			return ""
		})
		if err := sa.Write(ctx(t), batch[:1]); err == nil {
			t.Errorf("%s: no error", step)
		}
	}
	// A row COPY cannot write: aborted.
	f.setInject(nil)
	weird := rec("t", fitsync.OpInsert, 1, col("v", fitsync.Int(1)))
	if err := sa.Write(ctx(t), []*fitsync.Record{weird}); err != nil {
		t.Fatal(err)
	}
	it := sa.build(weird)
	it.vals[0] = struct{}{}
	c, err := sa.pool.get(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	c.Exec(ctx(t), "BEGIN")
	if err := sa.copyIn(ctx(t), c, []*item{&it}); err == nil {
		t.Error("no error for a value COPY cannot write")
	}
	c.Exec(ctx(t), "ROLLBACK")
	sa.pool.put(c)
}

// txRec makes a record of the source transaction id, the first or the
// last of it as said.
func txRec(id int64, v, tx string, first, last bool) *fitsync.Record {
	r := rec("t", fitsync.OpInsert, id, col("v", fitsync.String(v)))
	r.Tx = fitsync.Tx{ID: tx, First: first, Last: last}
	return r
}

func TestSourceTransactions(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"transactions": "source", "copy": "never"})
	rows := func() string { return dump(f.table(`"t"`), "id", "v") }

	// A transaction across three batches is written with its last record.
	if err := s.Write(ctx(t), []*fitsync.Record{txRec(1, "a", "", false, false), txRec(2, "b", "x", true, false)}); err != nil {
		t.Fatal(err)
	}
	if rows() != "[1|a]" {
		t.Error(rows())
	}
	if err := s.Write(ctx(t), []*fitsync.Record{txRec(3, "c", "x", false, false)}); err != nil || rows() != "[1|a]" {
		t.Error(err, rows())
	}
	// Tried again after an error, the held records with it.
	f.setInject(func(sql string, _ []*string) string {
		if sql == "COMMIT" {
			return "40001"
		}
		return ""
	})
	last := []*fitsync.Record{txRec(4, "d", "x", false, true), txRec(5, "e", "y", true, false)}
	if err := s.Write(ctx(t), last); err == nil {
		t.Fatal("no error")
	}
	f.setInject(nil)
	if err := s.Write(ctx(t), last); err != nil || rows() != "[1|a 2|b 3|c 4|d]" {
		t.Error(err, rows())
	}
	// The held transaction y is dropped when the pipeline goes back to its
	// checkpoint and sends it again from its first record.
	if err := s.Write(ctx(t), []*fitsync.Record{txRec(5, "e", "y", true, false), txRec(6, "f", "y", false, true)}); err != nil || rows() != "[1|a 2|b 3|c 4|d 5|e 6|f]" {
		t.Error(err, rows())
	}
	var be *fitsync.BatchError

	// A record refused for good: its transaction is not written, and its
	// records after it are dropped.
	f.setInject(bad("23514"))
	z := []*fitsync.Record{txRec(7, "g", "z", true, false), txRec(8, "bad", "z", false, false), txRec(9, "i", "z", false, false),
		txRec(10, "j", "z", false, true), txRec(11, "k", "", false, false)}
	err := s.Write(ctx(t), z)
	if !errors.As(err, &be) || be.Index != 1 || !fitsync.IsPermanent(err) || !strings.Contains(err.Error(), "transaction z") || s.drop != "z" {
		t.Fatal(err)
	}
	if err := s.Write(ctx(t), z[2:3]); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx(t), z[3:]); err != nil {
		t.Fatal(err)
	}
	if rows() != "[11|k 1|a 2|b 3|c 4|d 5|e 6|f]" {
		t.Error(rows())
	}
	// A held record refused: the transaction fails at the batch's first.
	if err := s.Write(ctx(t), []*fitsync.Record{txRec(12, "bad", "w", true, false)}); err != nil {
		t.Fatal(err)
	}
	err = s.Write(ctx(t), []*fitsync.Record{txRec(13, "m", "w", false, true)})
	if !errors.As(err, &be) || be.Index != 0 || !fitsync.IsPermanent(err) || s.drop != "" {
		t.Fatal(err, s.drop)
	}
	// A record that cannot be built: its transaction before it not
	// written, those before that are.
	f.setInject(nil)
	err = s.Write(ctx(t), []*fitsync.Record{txRec(14, "n", "", false, false), txRec(15, "o", "v", true, false),
		txRec(16, "p", "v", false, false), txRec(17, "q", "v", false, true)})
	if err != nil {
		t.Fatal(err)
	}
	broken := txRec(22, "p", "v2", false, false)
	broken.Op = 99
	err = s.Write(ctx(t), []*fitsync.Record{txRec(20, "n", "", false, false), txRec(21, "o", "v2", true, false), broken, txRec(23, "q", "v2", false, true)})
	if !errors.As(err, &be) || be.Index != 2 || !fitsync.IsPermanent(err) || s.drop != "v2" {
		t.Fatal(err, s.drop)
	}
	if err := s.Write(ctx(t), nil); err != nil {
		t.Fatal(err)
	}
	if rows() != "[11|k 14|n 15|o 16|p 17|q 1|a 20|n 2|b 3|c 4|d 5|e 6|f]" {
		t.Error(rows())
	}
	// A record without a transaction that cannot be built.
	err = s.Write(ctx(t), []*fitsync.Record{rec("t", 99, 18)})
	if !errors.As(err, &be) || be.Index != 0 || s.drop != "" {
		t.Error(err)
	}
	// Refused when the transaction ends the batch: nothing dropped after.
	f.setInject(bad("23514"))
	err = s.Write(ctx(t), []*fitsync.Record{txRec(19, "bad", "u", true, true)})
	if !errors.As(err, &be) || be.Index != 0 || s.drop != "" {
		t.Error(err)
	}
	// Other errors leave the held records held.
	f.setInject(func(sql string, _ []*string) string {
		if sql == "BEGIN" {
			return "57P01"
		}
		return ""
	})
	if err := s.Write(ctx(t), []*fitsync.Record{txRec(20, "r", "s", true, false)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx(t), []*fitsync.Record{txRec(21, "s", "s", false, true)}); err == nil || len(s.held) != 1 {
		t.Error(err, len(s.held))
	}
}

// TestPipelineAcrossKills writes generate's records, in transactions,
// through a pipeline killed at every crash point and started again from
// its checkpoint: the table ends as one written without a crash.
func TestPipelineAcrossKills(t *testing.T) {
	const total = 300
	for _, mode := range []string{"batch", "source"} {
		t.Run(mode, func(t *testing.T) {
			f := newFake(t)
			want := newFake(t)
			ref := sinkOf(t, want, map[string]any{})
			var all []*fitsync.Record
			for n := range int64(total) {
				all = append(all, fitsync.GenerateRecord("public.records", n, total, 4))
			}
			if err := ref.Write(ctx(t), all); err != nil {
				t.Fatal(err)
			}
			stateDir := t.TempDir()
			p := map[string]any{"name": "p", "checkpoint_interval": "1ms", "queue": map[string]any{"max_records": 40},
				"source": map[string]any{"module": "generate", "params": map[string]any{"records": total, "tx_size": 4, "namespace": "public.records"}},
				"sinks": []any{map[string]any{"name": "pg", "batch": map[string]any{"max_records": 7, "max_wait": "1ms"},
					"sink": map[string]any{"module": "postgres", "params": map[string]any{"address": f.addr(), "user": "fit", "sslmode": "disable",
						"transactions": mode, "copy_min_rows": 3}}}}}
			b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": stateDir, "pipelines": []any{p}}})
			cfg, err := config.Load(b)
			if err != nil {
				t.Fatal(err)
			}
			points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten,
				fitsync.CrashAcknowledged, fitsync.CrashCheckpointWritten, fitsync.CrashCheckpointSaved}
			for round := range 12 {
				var seen atomic.Int32
				at, nth := points[round%len(points)], int32(1+round*5)
				rt := fitsync.NewRuntime(fitsync.Options{Crash: func(_ string, p fitsync.Point) bool { return p == at && seen.Add(1) == nth }})
				if err := rt.Apply(cfg.Sync); err != nil {
					t.Fatal(err)
				}
				time.Sleep(20 * time.Millisecond)
				rt.Close(context.Background())
			}
			rt := fitsync.NewRuntime(fitsync.Options{})
			if err := rt.Apply(cfg.Sync); err != nil {
				t.Fatal(err)
			}
			cols := []string{"id", "name", "count", "score", "price", "even", "blob", "at", "day", "doc", "tags", "none"}
			expect := dump(want.table(`"public"."records"`), cols...)
			deadline := time.Now().Add(time.Minute) // a hang guard
			for dump(f.table(`"public"."records"`), cols...) != expect && time.Now().Before(deadline) {
				time.Sleep(2 * time.Millisecond)
			}
			rt.Close(context.Background())
			if got := dump(f.table(`"public"."records"`), cols...); got != expect {
				t.Fatalf("the table differs:\n%s\n%s", got, expect)
			}
		})
	}
}

package syncpostgres

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// srcLoad loads the source postgres with params, or returns its errors.
func srcLoad(t *testing.T, params map[string]any) (*Source, error) {
	t.Helper()
	p := map[string]any{"name": "p", "source": map[string]any{"module": "postgres", "params": params},
		"sinks": []any{map[string]any{"name": "s", "sink": map[string]any{"module": "memory"}}}}
	b, _ := json.Marshal(map[string]any{"sync": map[string]any{"state_dir": t.TempDir(), "pipelines": []any{p}}})
	cfg, err := config.Load(b)
	if err != nil {
		return nil, err
	}
	m, err := module.Load(fitsync.Sources, cfg.Sync.Pipelines[0].Source, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { module.Cleanup(m) })
	return m.(*Source), nil
}

func srcParams(extra map[string]any) map[string]any {
	p := map[string]any{"user": "u", "database": "db", "publication": "pub", "slot": "slot", "sslmode": "disable"}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func srcMust(t *testing.T, extra map[string]any) *Source {
	t.Helper()
	s, err := srcLoad(t, srcParams(extra))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func srcCertFiles(t *testing.T) (string, string) {
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

func TestSourceProvision(t *testing.T) {
	cert, key := srcCertFiles(t)
	notPEM := filepath.Join(t.TempDir(), "x")
	os.WriteFile(notPEM, []byte("x"), 0o600)
	t.Setenv("FIT_TEST_PG_SRC_PW", "secret")
	s := srcMust(t, map[string]any{"sslmode": "verify-full", "ca_file": cert, "cert_file": cert, "key_file": key,
		"password_env": "FIT_TEST_PG_SRC_PW", "address": "db.example:6432", "spool_dir": t.TempDir(), "tables": []string{"t", "s.u"}, "create": true})
	if s.cfg.Password != "secret" || s.cfg.Addr != "db.example:6432" || !s.cfg.Replication || s.cfg.TLS == nil || len(s.cfg.TLS.Certificates) != 1 || s.cfg.Params["TimeZone"] != "UTC" {
		t.Fatalf("%+v", s.cfg)
	}
	if s = srcMust(t, nil); s.cfg.Addr != "127.0.0.1:5432" || s.cfg.TLS != nil || s.cfg.ConnectTimeout != 10*time.Second ||
		s.statusInterval() != DefaultStatusInterval || s.lagWarning() != DefaultLagWarning {
		t.Fatalf("%+v", s.cfg)
	}
	if s = srcMust(t, map[string]any{"address": "db.example", "status_interval": "1s", "lag_warning": 5, "connect_timeout": "2s"}); s.cfg.Addr != "db.example" ||
		s.statusInterval() != time.Second || s.lagWarning() != 5 || s.cfg.ConnectTimeout != 2*time.Second {
		t.Fatalf("%+v", s.cfg)
	}
	for _, c := range []struct {
		params map[string]any
		want   string
	}{
		{map[string]any{"address": "a:b:c"}, "not a host and port"},
		{map[string]any{"password_env": "1x"}, "not an environment variable's name"},
		{map[string]any{"password_env": "FIT_TEST_PG_SRC_UNSET"}, "is not set"},
		{map[string]any{"ca_file": cert}, "needs TLS"},
		{map[string]any{"channel_binding": "require"}, "channel binding needs TLS"},
		{map[string]any{"sslmode": "require", "ca_file": "/nonexistent/ca.pem"}, "no such file"},
		{map[string]any{"sslmode": "require", "ca_file": notPEM}, "holds no PEM certificate"},
		{map[string]any{"sslmode": "require", "cert_file": cert}, "go together"},
		{map[string]any{"sslmode": "require", "cert_file": notPEM, "key_file": notPEM}, "cert_file"},
		{map[string]any{"slot": "Bad-Slot"}, "not a slot's name"},
		{map[string]any{"publication": strings.Repeat("p", 64)}, "not a publication's name"},
		{map[string]any{"tables": []string{"a", "public.a"}}, "named twice"},
		{map[string]any{"tables": []string{".a"}}, "not schema.table"},
		{map[string]any{"create": true}, "create needs the tables"},
		{map[string]any{"spool_dir": notPEM}, "not a directory"},
	} {
		if _, err := srcLoad(t, srcParams(c.params)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.params, err, c.want)
		}
	}
}

func TestDecodeText(t *testing.T) {
	tz := time.FixedZone("", 5*3600+30*60)
	for _, c := range []struct {
		oid  uint32
		in   string
		want fitsync.Value
	}{
		{pgwire.OIDBool, "t", fitsync.Bool(true)},
		{pgwire.OIDBool, "f", fitsync.Bool(false)},
		{pgwire.OIDBool, "x", fitsync.String("x")},
		{pgwire.OIDInt2, "-5", fitsync.Int(-5)},
		{pgwire.OIDInt8, "9223372036854775807", fitsync.Int(math.MaxInt64)},
		{pgwire.OIDInt4, "x", fitsync.String("x")},
		{pgwire.OIDOid, "4000000000", fitsync.Int(4000000000)},
		{pgwire.OIDOid, "-1", fitsync.String("-1")},
		{pgwire.OIDFloat8, "1.5", fitsync.Float(1.5)},
		{pgwire.OIDFloat4, "Infinity", fitsync.Float(math.Inf(1))},
		{pgwire.OIDFloat4, "-Infinity", fitsync.Float(math.Inf(-1))},
		{pgwire.OIDFloat8, "nope", fitsync.String("nope")},
		{pgwire.OIDNumeric, "12.50", fitsync.Decimal("12.50")},
		{pgwire.OIDBytea, `\x00ff`, fitsync.Bytes([]byte{0, 0xff})},
		{pgwire.OIDBytea, `\x0`, fitsync.String(`\x0`)},
		{pgwire.OIDJSONB, `{"a": 1}`, fitsync.JSON([]byte(`{"a": 1}`))},
		{pgwire.OIDDate, "2026-11-08", fitsync.Date(2026, 11, 8)},
		{pgwire.OIDDate, "infinity", fitsync.String("infinity")},
		{pgwire.OIDTimestamp, "2026-11-08 10:00:00.25", fitsync.Time(time.Date(2026, 11, 8, 10, 0, 0, 250e6, time.UTC))},
		{pgwire.OIDTimestamptz, "2026-11-08 10:00:00+00", fitsync.Time(time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC))},
		{pgwire.OIDTimestamptz, "2026-11-08 10:00:00+05:30", fitsync.Time(time.Date(2026, 11, 8, 10, 0, 0, 0, tz))},
		{pgwire.OIDTimestamptz, "2026-11-08 10:00:00-00:00:30", fitsync.Time(time.Date(2026, 11, 8, 10, 0, 0, 0, time.FixedZone("", -30)))},
		{pgwire.OIDTimestamptz, "0001-01-01 00:00:00+00 BC", fitsync.String("0001-01-01 00:00:00+00 BC")},
		{pgwire.OIDTimestamptz, "infinity", fitsync.String("infinity")},
		{pgwire.OIDTimestamptz, "2026-11-08 10:00:00+5", fitsync.String("2026-11-08 10:00:00+5")},
		{pgwire.OIDTimestamptz, "2026-11-08 10:00:00+05:00:00:00", fitsync.String("2026-11-08 10:00:00+05:00:00:00")},
		{pgwire.OIDTimestamptz, "2026-11-08 1x:00:00+05", fitsync.String("2026-11-08 1x:00:00+05")},
		{pgwire.OIDText, "hi", fitsync.String("hi")},
		{pgwire.OIDInterval, "1 day", fitsync.String("1 day")},
		{1007, "{1,NULL,3}", fitsync.Array(fitsync.Int(1), fitsync.Null(), fitsync.Int(3))},
		{1007, "[0:1]={1,2}", fitsync.Array(fitsync.Int(1), fitsync.Int(2))},
		{1007, "{{1,2},{3,4}}", fitsync.Array(fitsync.Array(fitsync.Int(1), fitsync.Int(2)), fitsync.Array(fitsync.Int(3), fitsync.Int(4)))},
		{1009, `{"a,b","q\"\\",NULL,"NULL",x}`, fitsync.Array(fitsync.String("a,b"), fitsync.String(`q"\`), fitsync.Null(), fitsync.String("NULL"), fitsync.String("x"))},
		{1009, "{}", fitsync.Array()},
		{1007, "[0:1]{1,2}", fitsync.String("[0:1]{1,2}")},
		{1007, "{1,2", fitsync.String("{1,2")},
		{1007, "{1,2}x", fitsync.String("{1,2}x")},
		{1007, "{{1}x}", fitsync.String("{{1}x}")},
		{1007, "{{1", fitsync.String("{{1")},
		{1009, `{"a`, fitsync.String(`{"a`)},
		{1009, `{"a\`, fitsync.String(`{"a\`)},
		{1009, `{"a"x}`, fitsync.String(`{"a"x}`)},
		{1007, "1", fitsync.String("1")},
		{1007, "{", fitsync.String("{")},
	} {
		if got := decodeText(c.oid, c.in); !got.Equal(c.want) || got.Kind() != c.want.Kind() {
			t.Errorf("%d %q: %v (%s), want %v", c.oid, c.in, got, got.Kind(), c.want)
		}
	}
	if v := decodeText(pgwire.OIDFloat8, "NaN"); !math.IsNaN(v.AsFloat()) {
		t.Errorf("NaN %v", v)
	}
}

// srcFake is a replication connection: catalog answers, a stream of
// messages, and the statuses sent.
type srcFake struct {
	version int
	mu      sync.Mutex
	sqls    []string
	answer  func(sql string) ([]pgwire.Result, error)

	sys      pgwire.System
	sysErr   error
	slotErr  error
	startErr error
	created  []pgwire.SlotOptions
	start    pgwire.LSN
	opts     []string

	msgs   chan any
	status chan pgwire.StandbyStatus
	closed chan struct{}
	once   sync.Once
}

func newSrcFake(answer func(sql string) ([]pgwire.Result, error)) *srcFake {
	return &srcFake{version: 160000, answer: answer, msgs: make(chan any, 1000),
		status: make(chan pgwire.StandbyStatus, 1000), closed: make(chan struct{})}
}

func (f *srcFake) ServerVersion() int { return f.version }
func (f *srcFake) SimpleQuery(_ context.Context, sql string) ([]pgwire.Result, error) {
	f.mu.Lock()
	f.sqls = append(f.sqls, sql)
	f.mu.Unlock()
	return f.answer(sql)
}
func (f *srcFake) IdentifySystem(context.Context) (pgwire.System, error) { return f.sys, f.sysErr }
func (f *srcFake) CreateReplicationSlot(_ context.Context, name string, o pgwire.SlotOptions) (pgwire.Slot, error) {
	f.created = append(f.created, o)
	return pgwire.Slot{Name: name, ConsistentPoint: 5}, f.slotErr
}
func (f *srcFake) StartReplication(_ context.Context, _ string, start pgwire.LSN, options ...string) error {
	f.start, f.opts = start, options
	return f.startErr
}
func (f *srcFake) ReceiveReplication(ctx context.Context) (any, error) {
	select {
	case m := <-f.msgs:
		if err, ok := m.(error); ok {
			return nil, err
		}
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (f *srcFake) SendStandbyStatus(_ context.Context, st pgwire.StandbyStatus) error {
	select {
	case f.status <- st:
	default:
	}
	return nil
}
func (f *srcFake) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

func (f *srcFake) queries() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.sqls, "\n")
}

// srcRows makes a result of text rows; "\x00" is NULL.
func srcRows(rows ...[]string) []pgwire.Result {
	var r pgwire.Result
	for _, row := range rows {
		var out [][]byte
		for _, v := range row {
			if v == "\x00" {
				out = append(out, nil)
			} else {
				out = append(out, []byte(v))
			}
		}
		r.Rows = append(r.Rows, out)
	}
	return []pgwire.Result{r}
}

// srcCatalog answers prepare's queries: a server of wal_level logical, a
// publication of public.t (keyed by id) and public.f (REPLICA IDENTITY
// FULL, no key), and the slot.
type srcCatalog struct {
	walLevel   string
	pub, slot  bool
	slotRow    []string
	idents     [][]string
	fail       string // a query starting so fails
	pubKeyless bool
}

func newSrcCatalog() *srcCatalog {
	return &srcCatalog{walLevel: "logical", pub: true, slot: true, slotRow: []string{"pgoutput", "logical", "db"},
		idents: [][]string{{"public", "t", "d", `["id"]`}, {"public", "f", "f", "\x00"}}}
}

func (c *srcCatalog) answer(sql string) ([]pgwire.Result, error) {
	if c.fail != "" && strings.HasPrefix(sql, c.fail) {
		return nil, errors.New("query failed: " + c.fail)
	}
	switch {
	case sql == "SHOW wal_level":
		return srcRows([]string{c.walLevel}), nil
	case strings.HasPrefix(sql, "SELECT pubupdate"):
		if !c.pub {
			return srcRows(), nil
		}
		if c.pubKeyless {
			return srcRows([]string{"f", "f"}), nil
		}
		return srcRows([]string{"t", "t"}), nil
	case strings.HasPrefix(sql, "SELECT schemaname"):
		return srcRows([]string{"public", "t"}, []string{"public", "f"}), nil
	case strings.HasPrefix(sql, "SELECT n.nspname"):
		return srcRows(c.idents...), nil
	case strings.HasPrefix(sql, "SELECT plugin"):
		if !c.slot {
			return srcRows(), nil
		}
		return srcRows(c.slotRow), nil
	case strings.HasPrefix(sql, "CREATE PUBLICATION"):
		return nil, nil
	}
	return nil, errors.New("unexpected " + sql)
}

// realDialSource is dialSource before tests replace it.
var realDialSource = dialSource

// srcUseFake makes dialSource return f, or err.
func srcUseFake(t *testing.T, f *srcFake, err error) {
	old := dialSource
	dialSource = func(context.Context, pgwire.Config) (replConn, error) {
		if err != nil {
			return nil, err
		}
		return f, nil
	}
	t.Cleanup(func() { dialSource = old })
}

// pgoutput message builders.
type srcMsg []byte

func newMsg(t byte) srcMsg              { return srcMsg{t} }
func (m srcMsg) u8(v byte) srcMsg       { return append(m, v) }
func (m srcMsg) u16(v uint16) srcMsg    { return binary.BigEndian.AppendUint16(m, v) }
func (m srcMsg) u32(v uint32) srcMsg    { return binary.BigEndian.AppendUint32(m, v) }
func (m srcMsg) u64(v uint64) srcMsg    { return binary.BigEndian.AppendUint64(m, v) }
func (m srcMsg) str(s string) srcMsg    { return append(append(m, s...), 0) }
func (m srcMsg) xid(x uint32) srcMsg    { return m.u32(x) } // streamed messages' xid
func (m srcMsg) data() *pgwire.XLogData { return &pgwire.XLogData{Data: m, ServerEnd: 1 << 20} }

// unchanged marks a TOASTed value not sent.
const unchanged = "\x01unchanged"

// tuple appends a tuple: nil is null, unchanged 'u', strings text.
func (m srcMsg) tuple(vals ...any) srcMsg {
	m = m.u16(uint16(len(vals)))
	for _, v := range vals {
		switch v := v.(type) {
		case nil:
			m = m.u8('n')
		case string:
			if v == unchanged {
				m = m.u8('u')
				continue
			}
			m = m.u8('t').u32(uint32(len(v)))
			m = append(m, v...)
		}
	}
	return m
}

const srcTS = 816000000000000 // 2025-11-09 10:40:00 UTC, in microseconds since 2000

func msgBegin(final uint64, xid uint32) srcMsg {
	return newMsg('B').u64(final).u64(srcTS).u32(xid)
}
func msgCommit(end uint64) srcMsg { return newMsg('C').u8(0).u64(end - 8).u64(end).u64(srcTS) }

// msgRel describes relation oid public.name: columns id (int4), v (text)
// and doc (jsonb), id the identity's.
func msgRel(oid uint32, name string, ident byte, keyCols ...string) srcMsg {
	m := newMsg('R').u32(oid).str("public").str(name).u8(ident).u16(3)
	for _, c := range []struct {
		name string
		oid  uint32
	}{{"id", pgwire.OIDInt4}, {"v", pgwire.OIDText}, {"doc", pgwire.OIDJSONB}} {
		var flags byte
		for _, k := range keyCols {
			if k == c.name {
				flags = 1
			}
		}
		m = m.u8(flags).str(c.name).u32(c.oid).u32(0xFFFFFFFF)
	}
	return m
}

func recString(r *fitsync.Record) string {
	key, _ := r.Key.AppendJSON(nil)
	s := r.Namespace + " " + r.Op.String() + " " + string(key)
	if !r.Before.IsNull() {
		b, _ := r.Before.AppendJSON(nil)
		s += " before " + string(b)
	}
	if !r.After.IsNull() {
		a, _ := r.After.AppendJSON(nil)
		s += " after " + string(a)
	}
	s += " tx " + r.Tx.ID
	if r.Tx.First {
		s += " first"
	}
	if r.Tx.Last {
		s += " last " + string(r.Position)
	}
	return s
}

// srcCollect runs s until it has emitted n records, and returns them and
// Run's error once it ends; stop ends it.
type srcCollect struct {
	mu   sync.Mutex
	recs []*fitsync.Record
	done chan error
	stop context.CancelFunc
}

func srcRun(s *Source, from fitsync.Position, once bool) *srcCollect {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	c := &srcCollect{done: make(chan error, 1), stop: cancel}
	go func() {
		c.done <- s.Run(ctx, &fitsync.Run{From: from, Once: once, Emit: func(r *fitsync.Record) error {
			c.mu.Lock()
			c.recs = append(c.recs, r)
			c.mu.Unlock()
			return nil
		}})
	}()
	return c
}

func (c *srcCollect) wait(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(time.Minute) // a hang guard
	for {
		c.mu.Lock()
		got := len(c.recs)
		var out []string
		if got >= n {
			for _, r := range c.recs {
				out = append(out, recString(r))
			}
		}
		c.mu.Unlock()
		if got >= n {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d records", got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func (c *srcCollect) end() error {
	c.stop()
	return <-c.done
}

// waitStatus waits for a status update flushing want.
func waitStatus(t *testing.T, f *srcFake, want pgwire.LSN) {
	t.Helper()
	deadline := time.After(time.Minute) // a hang guard
	for {
		select {
		case st := <-f.status:
			if st.Flush == want && st.Write == want && st.Apply == want {
				return
			}
		case <-deadline:
			t.Fatalf("no status flushing %v", want)
		}
	}
}

func TestSourceRun(t *testing.T) {
	cat := newSrcCatalog()
	f := newSrcFake(cat.answer)
	srcUseFake(t, f, nil)
	s := srcMust(t, map[string]any{"status_interval": "5ms", "spool_dir": t.TempDir()})
	for _, m := range []srcMsg{
		msgRel(1, "t", 'd', "id"),
		msgRel(2, "f", 'f', "id", "v", "doc"),
		// A transaction committed before the checkpoint: skipped.
		msgBegin(0x50, 9), newMsg('I').u32(1).u8('N').tuple("0", "old", nil), msgCommit(0x60),
		// Two inserts, an origin, a type.
		msgBegin(0x100, 10), newMsg('O').u64(1).str("origin"), newMsg('Y').u32(99).str("public").str("mood"),
		newMsg('I').u32(1).u8('N').tuple("1", "one", `{"a": 1}`),
		newMsg('I').u32(2).u8('N').tuple("7", "x", nil),
		msgCommit(0x110),
		// An update keeping its key, a TOASTed value unchanged; one changing
		// it; one of FULL identity filling the TOASTed value from before.
		msgBegin(0x200, 11),
		newMsg('U').u32(1).u8('N').tuple("1", "uno", unchanged),
		newMsg('U').u32(1).u8('K').tuple("1", nil, nil).u8('N').tuple("2", "uno", nil),
		newMsg('U').u32(2).u8('O').tuple("7", "x", `{"b": 2}`).u8('N').tuple("7", "y", unchanged),
		msgCommit(0x210),
		// Deletes by key and by the whole row, a truncate.
		msgBegin(0x300, 12),
		newMsg('D').u32(1).u8('K').tuple("2", nil, nil),
		newMsg('D').u32(2).u8('O').tuple("7", "x", nil),
		newMsg('T').u32(2).u8(3).u32(1).u32(2),
		msgCommit(0x310),
		// A transaction with no records.
		msgBegin(0x400, 13), msgCommit(0x410),
	} {
		f.msgs <- m.data()
	}
	run := srcRun(s, lsnPosition(0x60), false)
	got := run.wait(t, 10)
	want := []string{
		`public.t insert {"map":{"id":{"int":1}}} after {"map":{"id":{"int":1},"v":"one","doc":{"json":{"a":1}}}} tx 10 first`,
		`public.f insert {"map":{"id":{"int":7},"v":"x","doc":null}} after {"map":{"id":{"int":7},"v":"x","doc":null}} tx 10 last {"lsn":"0/110"}`,
		`public.t update {"map":{"id":{"int":1}}} after {"map":{"id":{"int":1},"v":"uno"}} tx 11 first`,
		`public.t delete {"map":{"id":{"int":1}}} before {"map":{"id":{"int":1}}} tx 11`,
		`public.t insert {"map":{"id":{"int":2}}} after {"map":{"id":{"int":2},"v":"uno","doc":null}} tx 11`,
		`public.f delete {"map":{"id":{"int":7},"v":"x","doc":{"json":{"b":2}}}} before {"map":{"id":{"int":7},"v":"x","doc":{"json":{"b":2}}}} tx 11`,
		`public.f insert {"map":{"id":{"int":7},"v":"y","doc":{"json":{"b":2}}}} after {"map":{"id":{"int":7},"v":"y","doc":{"json":{"b":2}}}} tx 11 last {"lsn":"0/210"}`,
		`public.t delete {"map":{"id":{"int":2}}} before {"map":{"id":{"int":2}}} tx 12 first`,
		`public.f delete {"map":{"id":{"int":7},"v":"x","doc":null}} before {"map":{"id":{"int":7},"v":"x","doc":null}} tx 12`,
		`public.t ddl null after {"map":{"statement":"TRUNCATE public.t RESTART IDENTITY CASCADE","truncate":true}} tx 12`,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("record %d\n got %s\nwant %s", i, got[i], want[i])
		}
	}
	if len(got) != 11 && len(got) != 10 {
		t.Fatalf("%d records", len(got))
	}
	got = run.wait(t, 11)
	if want := `public.f ddl null after {"map":{"statement":"TRUNCATE public.f RESTART IDENTITY CASCADE","truncate":true}} tx 12 last {"lsn":"0/310"}`; got[10] != want {
		t.Errorf("got %s\nwant %s", got[10], want)
	}
	if f.start != 0x60 || strings.Join(f.opts, " ") != `proto_version 2 publication_names "pub" streaming on` {
		t.Fatalf("started at %v with %q", f.start, f.opts)
	}
	run.mu.Lock()
	if at := run.recs[0].CommitTime; !at.Equal(time.Date(2025, 11, 9, 10, 40, 0, 0, time.UTC)) || run.recs[0].Source != "postgres" {
		t.Errorf("commit time %v", at)
	}
	run.mu.Unlock()

	// Nothing confirmed past the checkpoint: the slot is told it.
	waitStatus(t, f, 0x60)
	if l := s.Lag(); l != 1<<20-0x60 {
		t.Errorf("lag %d", l)
	}
	s.Confirm(lsnPosition(0x110))
	waitStatus(t, f, 0x110)
	s.Confirm(fitsync.Position(`garbage`)) // ignored
	s.Confirm(lsnPosition(0x310))
	// Everything confirmed: the empty transaction after it is released too,
	// then a keepalive's position, which is answered at once when asked.
	waitStatus(t, f, 0x410)
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x500, ReplyRequested: true}
	waitStatus(t, f, 0x500)
	// Mid-transaction, a keepalive does not move what is released.
	f.msgs <- msgBegin(0x600, 14).data()
	f.msgs <- newMsg('I').u32(1).u8('N').tuple("3", "three", nil).data()
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x700, ReplyRequested: true}
	waitStatus(t, f, 0x500)
	f.msgs <- msgCommit(0x610).data()
	run.wait(t, 12)
	// Handed on and not confirmed: the slot is told the checkpoint.
	waitFor(t, func() bool { return s.Lag() == 1<<20-0x310 })
	if err := run.end(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
	select {
	case <-f.closed:
	default:
		t.Fatal("the connection was not closed")
	}
	if q := f.queries(); !strings.Contains(q, "pg_publication_tables WHERE pubname = 'pub'") || strings.Contains(q, "CREATE") {
		t.Fatalf("queries %s", q)
	}
}

func TestSourceProtocol1(t *testing.T) {
	f := newSrcFake(newSrcCatalog().answer)
	f.version = 130000
	srcUseFake(t, f, nil)
	s := srcMust(t, nil)
	f.msgs <- errors.New("the connection broke")
	if err := s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }}); err == nil || err.Error() != "the connection broke" {
		t.Fatal(err)
	}
	if strings.Join(f.opts, " ") != `proto_version 1 publication_names "pub"` || f.start != 0 {
		t.Fatalf("%q", f.opts)
	}
	f.version = 170000
	s = srcMust(t, map[string]any{"streaming": "off"})
	f.msgs <- errors.New("the connection broke")
	s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return nil }})
	if strings.Join(f.opts, " ") != `proto_version 1 publication_names "pub"` {
		t.Fatalf("%q", f.opts)
	}
}

func TestSourceStreamed(t *testing.T) {
	f := newSrcFake(newSrcCatalog().answer)
	srcUseFake(t, f, nil)
	dir := t.TempDir()
	s := srcMust(t, map[string]any{"spool_dir": dir})
	// Transaction 20 streamed in two segments, its subtransaction 21
	// aborted; transaction 30 streamed and aborted whole; a transaction in
	// between not streamed. A relation in a stream carries its xid.
	rel := newMsg('R').xid(20)
	rel = append(rel, msgRel(1, "t", 'd', "id")[1:]...)
	ins := func(xid uint32, id string) srcMsg { return newMsg('I').xid(xid).u32(1).u8('N').tuple(id, "s", nil) }
	for _, m := range []srcMsg{
		newMsg('S').u32(20).u8(1), rel, ins(20, "1"), ins(21, "2"), newMsg('E'),
		newMsg('S').u32(30).u8(1), ins(30, "9"), newMsg('E'),
		// The relation came in transaction 20's stream: the plain
		// transaction needs its own.
		msgBegin(0x100, 25), msgRel(1, "t", 'd', "id"), newMsg('I').u32(1).u8('N').tuple("5", "plain", nil), msgCommit(0x110),
		newMsg('S').u32(20).u8(0), ins(20, "3"), ins(21, "4"), newMsg('E'),
		newMsg('A').u32(30).u32(30),
		newMsg('A').u32(20).u32(21),
		newMsg('A').u32(99).u32(99), // nothing streamed of it
		newMsg('c').u32(20).u8(0).u64(0x200).u64(0x210).u64(srcTS),
		newMsg('c').u32(40).u8(0).u64(0x300).u64(0x310).u64(srcTS), // nothing streamed: empty
	} {
		f.msgs <- m.data()
	}
	run := srcRun(s, nil, false)
	got := run.wait(t, 3)
	want := []string{
		`public.t insert {"map":{"id":{"int":5}}} after {"map":{"id":{"int":5},"v":"plain","doc":null}} tx 25 first last {"lsn":"0/110"}`,
		`public.t insert {"map":{"id":{"int":1}}} after {"map":{"id":{"int":1},"v":"s","doc":null}} tx 20 first`,
		`public.t insert {"map":{"id":{"int":3}}} after {"map":{"id":{"int":3},"v":"s","doc":null}} tx 20 last {"lsn":"0/210"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A stream left open when the run ends is removed.
	f.msgs <- newMsg('S').u32(50).u8(1).data()
	f.msgs <- ins(50, "8").data()
	waitFor(t, func() bool { e, _ := os.ReadDir(dir); return len(e) == 1 })
	run.end()
	if e, _ := os.ReadDir(dir); len(e) != 0 {
		t.Fatalf("spools left: %v", e)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Minute) // a hang guard
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("waited too long")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSourceOnce(t *testing.T) {
	f := newSrcFake(newSrcCatalog().answer)
	f.sys = pgwire.System{XLogPos: 0x300}
	srcUseFake(t, f, nil)
	s := srcMust(t, nil)
	f.msgs <- msgRel(1, "t", 'd', "id").data()
	f.msgs <- msgBegin(0x100, 10).data()
	f.msgs <- newMsg('I').u32(1).u8('N').tuple("1", "one", nil).data()
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x400} // mid-transaction: not the end
	f.msgs <- msgCommit(0x110).data()
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x300}
	run := srcRun(s, nil, true)
	if err := <-run.done; err != nil {
		t.Fatal(err)
	}
	if len(run.recs) != 1 {
		t.Fatalf("%d records", len(run.recs))
	}
	if st := <-f.status; !st.ReplyRequested {
		t.Fatal("a job's status asks for no reply")
	}
	f.sysErr = errors.New("no identity")
	if err := s.Run(context.Background(), &fitsync.Run{Once: true}); err == nil || err.Error() != "no identity" {
		t.Fatal(err)
	}
}

func TestSourceLagWarning(t *testing.T) {
	f := newSrcFake(newSrcCatalog().answer)
	srcUseFake(t, f, nil)
	s := srcMust(t, map[string]any{"lag_warning": 100, "status_interval": "1ms"})
	run := srcRun(s, lsnPosition(0x10), false)
	waitStatus(t, f, 0x10)
	// A transaction handed on, not confirmed, and the server far ahead.
	f.msgs <- msgRel(1, "t", 'd', "id").data()
	f.msgs <- msgBegin(0x20, 10).data()
	f.msgs <- newMsg('I').u32(1).u8('N').tuple("1", "one", nil).data()
	f.msgs <- msgCommit(0x30).data()
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x1000}
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.st.warned })
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x1001}
	s.Confirm(lsnPosition(1<<20 + 1))
	waitFor(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.st.warned })
	run.end()
}

func TestSourcePrepare(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name   string
		edit   func(*srcCatalog)
		params map[string]any
		want   string
	}{
		{"wal_level", func(c *srcCatalog) { c.walLevel = "replica" }, nil, "wal_level is not logical"},
		{"no publication", func(c *srcCatalog) { c.pub = false }, nil, "the publication pub does not exist"},
		{"table not published", nil, map[string]any{"tables": []string{"other"}}, "has no table public.other"},
		{"identity nothing", func(c *srcCatalog) { c.idents[0][2] = "n" }, nil, "REPLICA IDENTITY NOTHING"},
		{"no key", func(c *srcCatalog) { c.idents[0][3] = "\x00" }, nil, "has no primary key"},
		{"bad key", func(c *srcCatalog) { c.idents[0][3] = "[" }, nil, "the primary key of public.t"},
		{"no table", func(c *srcCatalog) { c.idents = c.idents[:1] }, nil, "the table public.f does not exist"},
		{"slot plugin", func(c *srcCatalog) { c.slotRow[0] = "wal2json" }, nil, "not logical of pgoutput"},
		{"no slot", func(c *srcCatalog) { c.slot = false }, nil, "the slot slot does not exist"},
		{"show fails", func(c *srcCatalog) { c.fail = "SHOW" }, nil, "query failed"},
		{"pub fails", func(c *srcCatalog) { c.fail = "SELECT pubupdate" }, nil, "query failed"},
		{"tables fail", func(c *srcCatalog) { c.fail = "SELECT schemaname" }, nil, "query failed"},
		{"idents fail", func(c *srcCatalog) { c.fail = "SELECT n.nspname" }, nil, "query failed"},
		{"slot fails", func(c *srcCatalog) { c.fail = "SELECT plugin" }, nil, "query failed"},
		{"create fails", func(c *srcCatalog) { c.pub, c.fail = false, "CREATE" }, map[string]any{"create": true, "tables": []string{"t"}}, "query failed"},
	} {
		cat := newSrcCatalog()
		if c.edit != nil {
			c.edit(cat)
		}
		f := newSrcFake(cat.answer)
		s := srcMust(t, c.params)
		if _, err := s.prepare(ctx, f, true); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}

	// A publication of inserts alone needs no keys; an empty result is no
	// row.
	cat := newSrcCatalog()
	cat.pubKeyless, cat.idents[0][2] = true, "n"
	if _, err := srcMust(t, nil).prepare(ctx, newSrcFake(cat.answer), true); err != nil {
		t.Fatal(err)
	}
	f := newSrcFake(func(sql string) ([]pgwire.Result, error) {
		if sql == "SHOW wal_level" {
			return nil, nil
		}
		return cat.answer(sql)
	})
	if _, err := srcMust(t, nil).prepare(ctx, f, true); err == nil {
		t.Fatal("no wal_level")
	}

	// Created when missing, with create.
	cat = newSrcCatalog()
	cat.pub, cat.slot = false, false
	cat.idents = [][]string{{"public", "t", "d", `["id"]`}, {"s", "u", "i", "\x00"}}
	f = newSrcFake(cat.answer)
	s := srcMust(t, map[string]any{"create": true, "tables": []string{"t", "s.u"}})
	st, err := s.prepare(ctx, f, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.queries(), `CREATE PUBLICATION "pub" FOR TABLE "public"."t", "s"."u"`) || len(f.created) != 1 ||
		f.created[0].Snapshot != pgwire.SnapshotNothing || st.slot == nil || st.slot.ConsistentPoint != 5 || st.pk["public.t"][0] != "id" {
		t.Fatalf("%s %+v %+v", f.queries(), f.created, st)
	}
	// Test creates nothing.
	f = newSrcFake(cat.answer)
	srcUseFake(t, f, nil)
	if err := s.Test(ctx); err != nil || strings.Contains(f.queries(), "CREATE") || len(f.created) != 0 {
		t.Fatalf("%v %s", err, f.queries())
	}
	f.slotErr = errors.New("no slot made")
	if _, err := s.prepare(ctx, f, true); err == nil || err.Error() != "no slot made" {
		t.Fatal(err)
	}
	// An empty publication has no tables to check.
	if err := s.identities(ctx, f, nil, true, &setup{}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceErrors(t *testing.T) {
	ctx := context.Background()
	s := srcMust(t, nil)
	srcUseFake(t, nil, errors.New("refused"))
	if err := s.Test(ctx); err == nil || err.Error() != "refused" {
		t.Fatal(err)
	}
	if err := s.Run(ctx, &fitsync.Run{}); err == nil || err.Error() != "refused" {
		t.Fatal(err)
	}
	for _, p := range []string{`x`, `{"lsn": "nope"}`} {
		if err := s.Run(ctx, &fitsync.Run{From: fitsync.Position(p)}); err == nil || !strings.Contains(err.Error(), "the checkpoint") {
			t.Fatal(err)
		}
	}
	cat := newSrcCatalog()
	cat.walLevel = "minimal"
	f := newSrcFake(cat.answer)
	srcUseFake(t, f, nil)
	if err := s.Run(ctx, &fitsync.Run{}); err == nil || !strings.Contains(err.Error(), "wal_level") {
		t.Fatal(err)
	}
	f = newSrcFake(newSrcCatalog().answer)
	f.startErr = errors.New("slot in use")
	srcUseFake(t, f, nil)
	if err := s.Run(ctx, &fitsync.Run{}); err == nil || err.Error() != "slot in use" {
		t.Fatal(err)
	}
	// A message the decoder refuses ends the run.
	f = newSrcFake(newSrcCatalog().answer)
	srcUseFake(t, f, nil)
	f.msgs <- newMsg('Z').data()
	if err := s.Run(ctx, &fitsync.Run{}); err == nil || !strings.Contains(err.Error(), "unknown pgoutput message") {
		t.Fatal(err)
	}
	// Emit's error ends it.
	f = newSrcFake(newSrcCatalog().answer)
	srcUseFake(t, f, nil)
	f.msgs <- msgRel(1, "t", 'd', "id").data()
	f.msgs <- msgBegin(1, 1).data()
	f.msgs <- newMsg('I').u32(1).u8('N').tuple("1", "a", nil).data()
	f.msgs <- newMsg('I').u32(1).u8('N').tuple("2", "a", nil).data()
	stop := errors.New("stopping")
	if err := s.Run(ctx, &fitsync.Run{Emit: func(*fitsync.Record) error { return stop }}); err != stop {
		t.Fatal(err)
	}

	// The real dial, to nothing listening.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	s = srcMust(t, map[string]any{"address": addr, "connect_timeout": "5s"})
	if _, err := realDialSource(ctx, s.cfg); err == nil {
		t.Fatal("connected to nothing")
	}
}

// TestDecoderErrors feeds the decoder malformed and unexpected messages.
func TestDecoderErrors(t *testing.T) {
	newDecoder := func() *decoder {
		d := &decoder{rels: map[uint32]*relation{}, pk: map[string][]string{}, spools: map[uint32]*spool{}, spoolDir: t.TempDir(),
			emit: func(*fitsync.Record) error { return nil }, onCommit: func(pgwire.LSN, bool) {}}
		d.handle(msgRel(1, "t", 'd', "id"))
		return d
	}
	inTx := func(m srcMsg) []srcMsg { return []srcMsg{msgBegin(1, 1), m} }
	for _, c := range []struct {
		name string
		msgs []srcMsg
		want string
	}{
		{"empty", []srcMsg{{}}, "cut short"},
		{"begin", []srcMsg{newMsg('B').u32(1)}, "cut short"},
		{"commit", []srcMsg{newMsg('C').u8(0)}, "cut short"},
		{"stream start", []srcMsg{newMsg('S')}, "cut short"},
		{"stream commit", []srcMsg{newMsg('c').u32(1)}, "cut short"},
		{"stream abort", []srcMsg{newMsg('A').u32(1)}, "cut short"},
		{"relation", []srcMsg{newMsg('R').u32(1).str("s")}, "cut short"},
		{"relation columns", []srcMsg{newMsg('R').u32(1).str("s").str("t").u8('d').u16(2).u8(0).str("a")}, "cut short"},
		{"unknown relation", inTx(newMsg('I').u32(7).u8('N').tuple("1")), "before its relation message"},
		{"insert", inTx(newMsg('I').u32(1).u8('X')), "malformed pgoutput message 'I'"},
		{"update", inTx(newMsg('U').u32(1).u8('X')), "malformed pgoutput message 'U'"},
		{"delete", inTx(newMsg('D').u32(1).u8('N')), "malformed pgoutput message 'D'"},
		{"short tuple", inTx(newMsg('I').u32(1).u8('N').tuple("1")), "malformed"},
		{"cut tuple", inTx(newMsg('I').u32(1).u8('N').u16(3).u8('t').u32(100)), "malformed"},
		{"binary", inTx(newMsg('I').u32(1).u8('N').u16(3).u8('b').u32(1).u8(1).u8('n').u8('n')), "not asked for"},
		{"binary old", inTx(newMsg('D').u32(1).u8('O').u16(3).u8('b').u32(1).u8(1).u8('n').u8('n')), "not asked for"},
		{"truncate", inTx(newMsg('T').u32(1000)), "cut short"},
		{"truncate unknown", inTx(newMsg('T').u32(1).u8(0).u32(7)), "before its relation message"},
		{"outside", []srcMsg{newMsg('I').u32(1).u8('N').tuple("1", nil, nil)}, "outside a transaction"},
		{"spool dir", []srcMsg{newMsg('S').u32(1).u8(1)}, "spool"},
	} {
		d := newDecoder()
		if c.name == "spool dir" {
			d.spoolDir = filepath.Join(t.TempDir(), "gone")
		}
		var err error
		for _, m := range c.msgs {
			if err = d.handle(m); err != nil {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
		d.close()
	}

	// A key the row lacks (a primary key not among the columns, an
	// identity column unchanged): no key.
	d := newDecoder()
	d.pk["public.k"] = []string{"nope"}
	d.handle(msgRel(2, "k", 'd', "v"))
	var got []*fitsync.Record
	d.emit = func(r *fitsync.Record) error { got = append(got, r); return nil }
	for _, m := range []srcMsg{msgBegin(1, 1), newMsg('I').u32(2).u8('N').tuple("1", unchanged, nil),
		newMsg('I').u32(2).u8('N').tuple("1", "x", nil), msgCommit(2)} {
		if err := d.handle(m); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 2 || !got[0].Key.IsNull() || recString(got[1]) != `public.k insert {"map":{"v":"x"}} after {"map":{"id":{"int":1},"v":"x","doc":null}} tx 1 last {"lsn":"0/2"}` {
		t.Fatalf("%d records: %s", len(got), recString(got[len(got)-1]))
	}
	// A table without a key at all.
	d.handle(msgRel(3, "n", 'n'))
	got = nil
	for _, m := range []srcMsg{msgBegin(3, 2), newMsg('I').u32(3).u8('N').tuple("1", "x", nil), msgCommit(4)} {
		d.handle(m)
	}
	if len(got) != 1 || !got[0].Key.IsNull() {
		t.Fatalf("%v", got)
	}
	// Emit's error at a commit, and in a stream's replay.
	stop := errors.New("stop")
	d.emit = func(*fitsync.Record) error { return stop }
	d.handle(msgBegin(5, 3))
	d.handle(newMsg('I').u32(3).u8('N').tuple("1", "x", nil))
	if err := d.handle(msgCommit(6)); err != stop {
		t.Fatal(err)
	}
	ins := func(id string) srcMsg { return newMsg('I').xid(4).u32(3).u8('N').tuple(id, "x", nil) }
	for _, m := range []srcMsg{newMsg('S').u32(4).u8(1), ins("1"), ins("2"), newMsg('E')} {
		d.handle(m)
	}
	if err := d.handle(newMsg('c').u32(4).u8(0).u64(7).u64(8).u64(srcTS)); err != stop {
		t.Fatal(err)
	}
	// A spool cut short.
	for _, m := range []srcMsg{newMsg('S').u32(5).u8(1), ins("1"), newMsg('E')} {
		d.handle(m)
	}
	sp := d.spools[5]
	sp.w.Flush()
	sp.f.Truncate(6)
	if err := d.handle(newMsg('c').u32(5).u8(0).u64(9).u64(10).u64(srcTS)); err == nil || !strings.Contains(err.Error(), "cut short") {
		t.Fatal(err)
	}
	// A spool closed under the decoder.
	for _, m := range []srcMsg{newMsg('S').u32(6).u8(1), ins("1")} {
		d.handle(m)
	}
	d.spools[6].f.Close()
	if err := d.handle(newMsg('c').u32(6).u8(0).u64(11).u64(12).u64(srcTS)); err == nil || !strings.Contains(err.Error(), "spool") {
		t.Fatal(err)
	}
	d.close()
}

func TestPositions(t *testing.T) {
	if l, err := parsePosition(lsnPosition(0x16_B374D848)); err != nil || l != 0x16_B374D848 || string(lsnPosition(l)) != `{"lsn":"16/B374D848"}` {
		t.Fatal(l, err)
	}
	if l, err := parsePosition(nil); err != nil || l != 0 {
		t.Fatal(l, err)
	}
	if (&Source{}).Lag() != 0 {
		t.Fatal("a lag before any run")
	}
	if s, n := splitTable("a.b.c"); s != "a" || n != "b.c" {
		t.Fatal(s, n)
	}
}

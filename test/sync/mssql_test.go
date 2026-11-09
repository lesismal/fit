package synctest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// mssqlServer is one server of FIT_SYNC_MSSQL (version=host:port,...), with
// sa's password from FIT_SYNC_MSSQL_PASSWORD and the CA of its certificate
// from FIT_SYNC_MSSQL_CA; test/sync/mssql.sh starts them.
type mssqlServer struct {
	name, addr, password string
	ca                   *x509.CertPool
}

func mssqlServers(t *testing.T) []mssqlServer {
	t.Helper()
	list := os.Getenv("FIT_SYNC_MSSQL")
	if list == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_MSSQL is not set")
		}
		t.Skip("no SQL Server: FIT_SYNC_MSSQL is not set (test/sync/mssql.sh sets it)")
	}
	pool := x509.NewCertPool()
	if f := os.Getenv("FIT_SYNC_MSSQL_CA"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pool.AppendCertsFromPEM(b)
	}
	var out []mssqlServer
	for _, s := range strings.Split(list, ",") {
		name, addr, ok := strings.Cut(s, "=")
		if !ok {
			t.Fatalf("FIT_SYNC_MSSQL: %q is not version=host:port", s)
		}
		out = append(out, mssqlServer{name: name, addr: addr, password: os.Getenv("FIT_SYNC_MSSQL_PASSWORD"), ca: pool})
	}
	return out
}

func eachMSSQL(t *testing.T, f func(t *testing.T, s mssqlServer)) {
	for _, s := range mssqlServers(t) {
		t.Run(s.name, func(t *testing.T) { f(t, s) })
	}
}

func (s mssqlServer) config(encrypt string) mssqlwire.Config {
	return mssqlwire.Config{Addr: s.addr, User: "sa", Password: s.password, Database: "fit_sync", Encrypt: encrypt,
		TLS: &tls.Config{RootCAs: s.ca, ServerName: "localhost"}, Timeout: time.Minute}
}

func (s mssqlServer) dial(t *testing.T, encrypt string) *mssqlwire.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, err := mssqlwire.Dial(ctx, s.config(encrypt))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func mssqlExec(t *testing.T, c *mssqlwire.Conn, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := c.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestMSSQLWireLogin(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		modes := []string{mssqlwire.EncryptOn, mssqlwire.EncryptOff}
		if os.Getenv("FIT_SYNC_MSSQL_STRICT") == "1" {
			// TDS 8 needs a certificate the server takes for strict
			// connections, which mssql.sh's is not yet.
			modes = append(modes, mssqlwire.EncryptStrict)
		}
		for _, mode := range modes {
			c := s.dial(t, mode)
			_, rows, err := c.QueryAll(context.Background(), "SELECT DB_NAME(), encrypt_option FROM sys.dm_exec_connections WHERE session_id = @@SPID")
			if err != nil {
				t.Fatal(mode, err)
			}
			if rows[0][0] != "fit_sync" || (rows[0][1] == "TRUE") != (mode != mssqlwire.EncryptOff) {
				t.Fatalf("%s: %v", mode, rows)
			}
		}
		cfg := s.config(mssqlwire.EncryptOn)
		cfg.Password = "wrong"
		if _, err := mssqlwire.Dial(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "18456") {
			t.Fatal(err)
		}
	})
}

func TestMSSQLWireTypes(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		tbl := fmt.Sprintf("types_%d", time.Now().UnixNano())
		mssqlExec(t, c, `CREATE TABLE dbo.`+tbl+` (id int PRIMARY KEY, ti tinyint, si smallint, bi bigint, b bit,
			r real, f float, d decimal(38,10), n numeric(5,2), m money, sm smallmoney,
			dt datetime, sdt smalldatetime, da date, tm time(7), dt2 datetime2(7), dto datetimeoffset(7),
			u uniqueidentifier, vc varchar(100), vcm varchar(max), c char(4), nv nvarchar(100), nvm nvarchar(max), nc nchar(3),
			vb varbinary(100), vbm varbinary(max), bn binary(3), x xml, tx text, ntx ntext, im image, rv rowversion, v sql_variant)`)
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+tbl) })
		u, _ := mssqlwire.ParseUUID("6F9619FF-8B86-D011-B42D-00C04FC964FF")
		at := time.Date(2026, 11, 10, 13, 14, 15, 123456700, time.UTC)
		zone := time.FixedZone("", 5*3600+30*60)
		long := strings.Repeat("long ünïcode 𝄞 ", 2000)
		blob := bytes.Repeat([]byte{0, 1, 2, 255}, 10000)
		params := []any{1, 200, -300, int64(-9007199254740993), true,
			float32(1.5), 2.25, mssqlwire.Decimal("-1234567890123456789012345678.0123456789"), mssqlwire.Decimal("123.45"), mssqlwire.Decimal("-922337203685477.5808"), mssqlwire.Decimal("214748.3647"),
			mssqlwire.Param{Value: at, Type: "datetime"}, mssqlwire.Param{Value: at, Type: "smalldatetime"}, mssqlwire.Param{Value: at, Type: "date"}, 13*time.Hour + 100, mssqlwire.Param{Value: at, Type: "datetime2(7)"}, at.In(zone),
			u, mssqlwire.Param{Value: "café €", Type: "varchar(100)"}, mssqlwire.Param{Value: strings.Repeat("v", 9000), Type: "varchar(max)"}, mssqlwire.Param{Value: "ab", Type: "char(4)"}, "日本語 𝄞", long, "abc",
			[]byte{1, 2, 3}, blob, mssqlwire.Param{Value: []byte{9, 8, 7}, Type: "binary(3)"}, "<a b=\"1\">x</a>", mssqlwire.Param{Value: "text", Type: "varchar(max)"}, "ntext", []byte{5, 6}, mssqlwire.Param{Value: int64(7), Type: "int"}}
		var marks []string
		for i := range params {
			marks = append(marks, fmt.Sprintf("@p%d", i+1))
		}
		mssqlExec(t, c, "INSERT INTO dbo."+tbl+" (id, ti, si, bi, b, r, f, d, n, m, sm, dt, sdt, da, tm, dt2, dto, u, vc, vcm, c, nv, nvm, nc, vb, vbm, bn, x, tx, ntx, im, v) VALUES ("+strings.Join(marks, ", ")+")", params...)
		mssqlExec(t, c, "INSERT INTO dbo."+tbl+" (id) VALUES (2)")
		cols, rows, err := c.QueryAll(context.Background(), "SELECT * FROM dbo."+tbl+" ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		want := []any{int64(1), int64(200), int64(-300), int64(-9007199254740993), true,
			float32(1.5), 2.25, mssqlwire.Decimal("-1234567890123456789012345678.0123456789"), mssqlwire.Decimal("123.45"), mssqlwire.Decimal("-922337203685477.5808"), mssqlwire.Decimal("214748.3647"),
			time.Date(2026, 11, 10, 13, 14, 15, 123000000, time.UTC), time.Date(2026, 11, 10, 13, 14, 0, 0, time.UTC), time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), 13*time.Hour + 100, at, at.In(zone),
			u, "café €", strings.Repeat("v", 9000), "ab  ", "日本語 𝄞", long, "abc",
			[]byte{1, 2, 3}, blob, []byte{9, 8, 7}, "<a b=\"1\">x</a>", "text", "ntext", []byte{5, 6}}
		for i, w := range want {
			got := rows[0][i]
			if tm, ok := w.(time.Time); ok {
				g, ok := got.(time.Time)
				if !ok || !g.Equal(tm) {
					t.Errorf("%s (%s): %#v, want %v", cols[i].Name, cols[i].TypeName(), got, w)
				}
				continue
			}
			if !reflect.DeepEqual(got, w) {
				t.Errorf("%s (%s): %#v, want %#v", cols[i].Name, cols[i].TypeName(), got, w)
			}
		}
		if rv, ok := rows[0][31].([]byte); !ok || len(rv) != 8 || cols[31].TypeName() != "timestamp" {
			t.Errorf("rowversion %#v %s", rows[0][31], cols[31].TypeName())
		}
		if rows[0][32] != int64(7) || cols[32].TypeName() != "sql_variant" {
			t.Errorf("sql_variant %#v %s", rows[0][32], cols[32].TypeName())
		}
		for i, v := range rows[1][1:] {
			if v != nil && i+1 != 31 {
				t.Errorf("%s: %#v in the row of NULLs", cols[i+1].Name, v)
			}
		}
		// The column types as the server describes them.
		names := map[string]string{}
		for _, col := range cols {
			names[col.Name] = col.TypeName()
		}
		for col, typ := range map[string]string{"d": "decimal(38,10)", "n": "numeric(5,2)", "tm": "time(7)", "dto": "datetimeoffset(7)",
			"vcm": "varchar(max)", "nv": "nvarchar(100)", "c": "char(4)", "vbm": "varbinary(max)", "x": "xml", "tx": "text"} {
			if names[col] != typ {
				t.Errorf("%s is %s, not %s", col, names[col], typ)
			}
		}
		// Values written by the server read back as their literals say.
		_, lit, err := c.QueryAll(context.Background(), `SELECT CAST('2026-01-02T03:04:05.1234567+01:00' AS datetimeoffset(7)), CAST(-0.5 AS decimal(3,1)),
			CAST(0.0001 AS smallmoney), CAST('00:00:00.999' AS time(3)), CAST(N'x' AS sql_variant), CAST('6F9619FF-8B86-D011-B42D-00C04FC964FF' AS uniqueidentifier)`)
		if err != nil {
			t.Fatal(err)
		}
		wantLit := []any{time.Date(2026, 1, 2, 2, 4, 5, 123456700, time.UTC), mssqlwire.Decimal("-0.5"), mssqlwire.Decimal("0.0001"), 999 * time.Millisecond, "x", u}
		for i, w := range wantLit {
			if tm, ok := w.(time.Time); ok && tm.Equal(lit[0][i].(time.Time)) {
				continue
			}
			if !reflect.DeepEqual(lit[0][i], w) {
				t.Errorf("literal %d: %#v, want %#v", i, lit[0][i], w)
			}
		}
	})
}

func TestMSSQLWireBulkAndTx(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		ctx := context.Background()
		tbl := fmt.Sprintf("bulk_%d", time.Now().UnixNano())
		mssqlExec(t, c, "CREATE TABLE dbo."+tbl+" (id int PRIMARY KEY, name nvarchar(50) NULL, note varchar(max) COLLATE Latin1_General_CS_AS NULL, amount decimal(12,2), at datetime2(3), raw varbinary(max) NULL, t text NULL)")
		t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+tbl) })
		b, err := c.BulkInsert(ctx, "dbo."+tbl, []string{"id", "name", "note", "amount", "at", "raw", "t"}, mssqlwire.BulkOptions{TabLock: true, Order: []string{"id"}, KeepNulls: true})
		if err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 11, 10, 1, 2, 3, 4000000, time.UTC)
		for i := 1; i <= 5000; i++ {
			var name any = fmt.Sprintf("name %d ü", i)
			if i%10 == 0 {
				name = nil
			}
			if err := b.WriteRow(i, name, strings.Repeat("n", i%300), mssqlwire.Decimal(fmt.Sprintf("%d.%02d", i, i%100)), at, bytes.Repeat([]byte{byte(i)}, i%50), "t"); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := b.Close(); err != nil || n != 5000 {
			t.Fatal(n, err)
		}
		_, rows, err := c.QueryAll(ctx, "SELECT COUNT(*), SUM(amount), COUNT(name), MAX(at) FROM dbo."+tbl)
		if err != nil || rows[0][0] != int64(5000) || rows[0][1] != mssqlwire.Decimal("12504975.00") || rows[0][2] != int64(4500) {
			t.Fatal(rows, err)
		}
		// A transaction rolled back leaves nothing; one committed stays.
		if err := c.Begin(ctx, "READ COMMITTED"); err != nil {
			t.Fatal(err)
		}
		mssqlExec(t, c, "INSERT INTO dbo."+tbl+" (id, amount, at) VALUES (@p1, 1, SYSDATETIME())", 100000)
		if err := c.Rollback(ctx); err != nil || c.InTransaction() {
			t.Fatal(err)
		}
		c.Begin(ctx, "")
		mssqlExec(t, c, "INSERT INTO dbo."+tbl+" (id, amount, at) VALUES (@p1, 1, SYSDATETIME())", 100001)
		if err := c.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		_, rows, _ = c.QueryAll(ctx, "SELECT id FROM dbo."+tbl+" WHERE id >= 100000")
		if len(rows) != 1 || rows[0][0] != int64(100001) {
			t.Fatal(rows)
		}
	})
}

func TestMSSQLWireCancel(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		c := s.dial(t, mssqlwire.EncryptOn)
		watch := s.dial(t, mssqlwire.EncryptOff)
		_, spid, err := c.QueryAll(context.Background(), "SELECT @@SPID")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		start := time.Now()
		_, err = c.Exec(ctx, "WAITFOR DELAY '00:01:00'")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 20*time.Second {
			t.Fatal(err, time.Since(start))
		}
		// The server cancelled it: no request of the session runs, and the
		// connection takes the next query.
		_, reqs, err := watch.QueryAll(context.Background(), "SELECT COUNT(*) FROM sys.dm_exec_requests WHERE session_id = @p1", spid[0][0])
		if err != nil || reqs[0][0] != int64(0) {
			t.Fatal(reqs, err)
		}
		if _, rows, err := c.QueryAll(context.Background(), "SELECT 1"); err != nil || rows[0][0] != int64(1) {
			t.Fatal(rows, err)
		}
		// Rows closed early cancel the rest of a long result.
		rows, err := c.Query(context.Background(), "SELECT a.object_id FROM sys.all_objects a CROSS JOIN sys.all_objects b")
		if err != nil || !rows.Next() {
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		// A session killed mid-result is an error, not a hang.
		rows, err = c.Query(context.Background(), "SELECT a.object_id, REPLICATE('x', 1000) FROM sys.all_objects a CROSS JOIN sys.all_objects b")
		if err != nil || !rows.Next() {
			t.Fatal(err)
		}
		mssqlExec(t, watch, fmt.Sprintf("KILL %d", spid[0][0]))
		done := make(chan error, 1)
		go func() {
			for rows.Next() {
			}
			done <- rows.Err()
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("no error after KILL")
			}
		case <-time.After(90 * time.Second):
			t.Fatal("a hang after KILL")
		}
	})
}

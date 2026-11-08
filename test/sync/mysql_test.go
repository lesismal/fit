package synctest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// mysqlServer is one server of FIT_SYNC_MYSQL (name=host:port,...), with
// root's password from FIT_SYNC_MYSQL_PASSWORD and the CA of its TLS from
// FIT_SYNC_MYSQL_CA; test/sync/mysql.sh starts them.
type mysqlServer struct {
	name, addr, password string
	ca                   *x509.CertPool
}

func mysqlServers(t *testing.T) []mysqlServer {
	t.Helper()
	list := os.Getenv("FIT_SYNC_MYSQL")
	if list == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatal("FIT_SYNC_MYSQL is not set")
		}
		t.Skip("no MySQL: FIT_SYNC_MYSQL is not set (test/sync/mysql.sh sets it)")
	}
	pool := x509.NewCertPool()
	if f := os.Getenv("FIT_SYNC_MYSQL_CA"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pool.AppendCertsFromPEM(b)
	}
	var out []mysqlServer
	for _, s := range strings.Split(list, ",") {
		name, addr, ok := strings.Cut(s, "=")
		if !ok {
			t.Fatalf("FIT_SYNC_MYSQL: %q is not name=host:port", s)
		}
		out = append(out, mysqlServer{name: name, addr: addr, password: os.Getenv("FIT_SYNC_MYSQL_PASSWORD"), ca: pool})
	}
	return out
}

// each runs f for each server, as a subtest of its name.
func eachMySQL(t *testing.T, f func(t *testing.T, s mysqlServer)) {
	for _, s := range mysqlServers(t) {
		t.Run(s.name, func(t *testing.T) { f(t, s) })
	}
}

// hangGuard is a context that ends a test stuck past any reasonable time.
func hangGuard(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

func (s mysqlServer) config(user, password string) mysqlwire.Config {
	return mysqlwire.Config{Addr: s.addr, User: user, Password: password}
}

// tls is the TLS a test connects with. MySQL 5.7's image offers no ECDHE
// suites, only RSA and DHE key exchange, of which Go has only RSA's, off by
// default: its suites are named for it.
func (s mysqlServer) tls() *tls.Config {
	c := &tls.Config{RootCAs: s.ca}
	if s.name == "mysql57" {
		c.CipherSuites = []uint16{tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384}
	}
	return c
}

func (s mysqlServer) dial(t *testing.T, cfg mysqlwire.Config) *mysqlwire.Conn {
	t.Helper()
	c, err := mysqlwire.Dial(hangGuard(t), cfg)
	if err != nil {
		t.Fatalf("%s: dial as %s: %v", s.name, cfg.User, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (s mysqlServer) root(t *testing.T) *mysqlwire.Conn {
	t.Helper()
	cfg := s.config("root", s.password)
	cfg.TLS = s.tls()
	return s.dial(t, cfg)
}

func mustExec(t *testing.T, c *mysqlwire.Conn, q string, args ...any) mysqlwire.Result {
	t.Helper()
	res, err := c.Exec(hangGuard(t), q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return res
}

// mysqlDB makes a database of its own for a test.
func mysqlDB(t *testing.T, c *mysqlwire.Conn, name string) {
	t.Helper()
	mustExec(t, c, "DROP DATABASE IF EXISTS "+name)
	mustExec(t, c, "CREATE DATABASE "+name+" CHARACTER SET utf8mb4")
	if err := c.UseDatabase(hangGuard(t), name); err != nil {
		t.Fatal(err)
	}
}

func isMySQL8(c *mysqlwire.Conn) bool {
	return !c.IsMariaDB() && !strings.HasPrefix(c.ServerVersion(), "5.")
}

func TestMySQLAuthentication(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		root := s.root(t)
		if !root.TLS() {
			t.Fatal("root's connection is not TLS")
		}
		const pw = "pässword-1!"
		type user struct {
			name, plugin string
		}
		users := []user{{"fit_native", "mysql_native_password"}}
		if !root.IsMariaDB() {
			users = append(users, user{"fit_sha256", "sha256_password"})
		}
		if isMySQL8(root) {
			users = append(users, user{"fit_sha2", "caching_sha2_password"})
		}
		for _, u := range users {
			mustExec(t, root, "DROP USER IF EXISTS "+u.name)
			if root.IsMariaDB() {
				mustExec(t, root, fmt.Sprintf("CREATE USER %s IDENTIFIED BY '%s'", u.name, pw))
			} else {
				mustExec(t, root, fmt.Sprintf("CREATE USER %s IDENTIFIED WITH %s BY '%s'", u.name, u.plugin, pw))
			}
		}
		pubText := ""
		if !root.IsMariaDB() {
			rows, err := root.Query(hangGuard(t), "SHOW STATUS LIKE 'Caching_sha2_password_rsa_public_key'")
			if err == nil {
				for rows.Next() {
					pubText = string(rows.Values()[1].([]byte))
				}
				rows.Close()
			}
			if pubText == "" {
				rows, err := root.Query(hangGuard(t), "SHOW STATUS LIKE 'Rsa_public_key'")
				if err != nil {
					t.Fatal(err)
				}
				for rows.Next() {
					pubText = string(rows.Values()[1].([]byte))
				}
				rows.Close()
			}
		}
		flush := func() { mustExec(t, root, "FLUSH PRIVILEGES") }
		for _, u := range users {
			t.Run(u.name, func(t *testing.T) {
				check := func(t *testing.T, cfg mysqlwire.Config) {
					t.Helper()
					c := s.dial(t, cfg)
					rows, err := c.Query(hangGuard(t), "SELECT CURRENT_USER()")
					if err != nil {
						t.Fatal(err)
					}
					if !rows.Next() || !strings.HasPrefix(string(rows.Values()[0].([]byte)), u.name+"@") {
						t.Fatalf("CURRENT_USER() is %v", rows.Values())
					}
					rows.Close()
				}
				// Over TLS: caching_sha2_password's full authentication,
				// then its fast path from the cache.
				flush()
				cfg := s.config(u.name, pw)
				cfg.TLS = s.tls()
				check(t, cfg)
				check(t, cfg)
				if u.plugin == "mysql_native_password" {
					check(t, s.config(u.name, pw))
				} else {
					// Without TLS: the server's key retrieved, or given.
					flush()
					cfg := s.config(u.name, pw)
					if _, err := mysqlwire.Dial(hangGuard(t), cfg); err == nil || !strings.Contains(err.Error(), "PublicKey") {
						t.Fatalf("no TLS and no key: %v", err)
					}
					cfg.AllowPublicKeyRetrieval = true
					check(t, cfg)
					flush()
					pub, err := mysqlwire.ParsePublicKey([]byte(pubText))
					if err != nil {
						t.Fatal(err)
					}
					cfg = s.config(u.name, pw)
					cfg.ServerPublicKey = pub
					check(t, cfg)
					if u.plugin == "caching_sha2_password" {
						check(t, s.config(u.name, pw)) // the fast path needs neither
					}
				}
				_, err := mysqlwire.Dial(hangGuard(t), mysqlwire.Config{Addr: s.addr, User: u.name, Password: "wrong", TLS: s.tls()})
				var se *mysqlwire.Error
				if !errors.As(err, &se) || se.Code != 1045 {
					t.Fatalf("a wrong password: %v", err)
				}
			})
		}
		// TLS verified against the name: a CA that is not the server's.
		bad := s.config("root", s.password)
		bad.TLS = &tls.Config{RootCAs: x509.NewCertPool()}
		if _, err := mysqlwire.Dial(hangGuard(t), bad); err == nil {
			t.Fatal("a server certificate from an unknown CA was accepted")
		}
	})
}

// mysqlTypes is the table every column type round-trips through.
const mysqlTypes = `CREATE TABLE types (
	id INT PRIMARY KEY,
	ti TINYINT, tiu TINYINT UNSIGNED, si SMALLINT, siu SMALLINT UNSIGNED,
	mi MEDIUMINT, miu MEDIUMINT UNSIGNED, i INT, iu INT UNSIGNED, bi BIGINT, biu BIGINT UNSIGNED,
	f FLOAT, d DOUBLE, de DECIMAL(65,30), bt BIT(64), b1 BOOL,
	c CHAR(10), vc VARCHAR(100), bn BINARY(4), vb VARBINARY(300),
	tt TINYTEXT, t TEXT, mt MEDIUMTEXT, lt LONGTEXT,
	tb TINYBLOB, bl BLOB, mb MEDIUMBLOB, lb LONGBLOB,
	e ENUM('a','b','c'), st SET('x','y','z'),
	dt DATE, dtm DATETIME(6), dt0 DATETIME, ts TIMESTAMP(6) NULL, tm TIME(6), y YEAR,
	j JSON, g GEOMETRY
)`

func seq(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i * 7)
	}
	return b
}

// mysqlRows are the rows of types: extremes, nulls and zeros.
func mysqlRows(big int) [][]any {
	point := []byte{0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xf0, 0x3f, 0, 0, 0, 0, 0, 0, 0, 0x40}
	full := []any{
		int64(1), int64(-128), uint64(255), int64(-32768), uint64(65535),
		int64(-8388608), uint64(16777215), int64(math.MinInt32), uint64(math.MaxUint32), int64(math.MinInt64), uint64(math.MaxUint64),
		float32(-1.5), float64(-1.25e-300), []byte("-12345678901234567890123456789012345.123456789012345678901234567890"),
		[]byte{0x80, 0, 0, 0, 0, 0, 0, 1}, int64(1),
		[]byte("abc"), []byte("héllo, 世界 😀"), []byte{0, 1, 2, 3}, seq(300),
		[]byte("tiny"), []byte(strings.Repeat("text ", 1000)), []byte(strings.Repeat("m", 70000)), []byte(strings.Repeat("long ", 100)),
		seq(255), seq(65535), seq(70000), seq(big),
		[]byte("b"), []byte("x,z"),
		mysqlwire.DateTime{Year: 2026, Month: 11, Day: 8},
		mysqlwire.DateTime{Year: 9999, Month: 12, Day: 31, Hour: 23, Minute: 59, Second: 59, Microsecond: 999999},
		mysqlwire.DateTime{Year: 1000, Month: 1, Day: 1, Hour: 1, Minute: 2, Second: 3},
		mysqlwire.DateTime{Year: 2038, Month: 1, Day: 19, Hour: 3, Minute: 14, Second: 7, Microsecond: 123456},
		-(838*time.Hour + 59*time.Minute + 58*time.Second + 999999*time.Microsecond), int64(2155),
		[]byte(`{"a": [1, 2.5, "x", null, true], "b": {"c": "d"}}`), point,
	}
	nulls := make([]any, len(full))
	nulls[0] = int64(2)
	zeros := []any{
		int64(3), int64(0), uint64(0), int64(0), uint64(0), int64(0), uint64(0), int64(0), uint64(0), int64(0), uint64(0),
		float32(0), float64(0), []byte("0.000000000000000000000000000000"), []byte{0, 0, 0, 0, 0, 0, 0, 0}, int64(0),
		[]byte(""), []byte(""), []byte{0, 0, 0, 0}, []byte{},
		[]byte(""), []byte(""), []byte(""), []byte(""), []byte{}, []byte{}, []byte{}, []byte{},
		[]byte(""), []byte(""),
		mysqlwire.DateTime{}, mysqlwire.DateTime{}, mysqlwire.DateTime{}, mysqlwire.DateTime{},
		time.Duration(0), int64(0), []byte(`[]`), point,
	}
	return [][]any{full, nulls, zeros}
}

// sameValue compares a value read with the one written, JSON by its
// meaning since servers rewrite it.
func sameValue(col mysqlwire.Column, want, got any) bool {
	if col.Type == mysqlwire.TypeJSON || col.Name == "j" {
		w, _ := want.([]byte)
		g, _ := got.([]byte)
		if w == nil || g == nil {
			return w == nil && g == nil
		}
		var a, b any
		if json.Unmarshal(w, &a) != nil || json.Unmarshal(g, &b) != nil {
			return false
		}
		return reflect.DeepEqual(a, b)
	}
	if w, ok := want.([]byte); ok {
		g, ok := got.([]byte)
		return ok && bytes.Equal(w, g)
	}
	return reflect.DeepEqual(want, got)
}

func TestMySQLTypesRoundTrip(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		c := s.root(t)
		mysqlDB(t, c, "fit_types")
		mustExec(t, c, "SET time_zone = '+00:00', sql_mode = ''")
		mustExec(t, c, mysqlTypes)
		// A LONGBLOB past one packet (16 MiB), each way.
		rows := mysqlRows(17 << 20)
		for _, r := range rows {
			if _, err := c.InsertRows(hangGuard(t), "INSERT INTO types VALUES", [][]any{r}, ""); err != nil {
				t.Fatal(err)
			}
		}
		check := func(t *testing.T, rs *mysqlwire.Rows, how string) {
			t.Helper()
			n := 0
			for rs.Next() {
				vals := rs.Values()
				want := rows[n]
				for i, col := range rs.Columns() {
					if !sameValue(col, want[i], vals[i]) {
						g, w := fmt.Sprint(vals[i]), fmt.Sprint(want[i])
						t.Errorf("%s: row %d, %s (type 0x%x): got %T %.100s, want %T %.100s", how, n, col.Name, col.Type, vals[i], g, want[i], w)
					}
				}
				n++
			}
			if err := rs.Close(); err != nil {
				t.Fatal(err)
			}
			if n != len(rows) {
				t.Fatalf("%s: %d rows, want %d", how, n, len(rows))
			}
		}
		st, err := c.Prepare(hangGuard(t), "SELECT * FROM types WHERE id >= ? ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		rs, err := st.Query(hangGuard(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		check(t, rs, "binary")
		rs, err = c.Query(hangGuard(t), "SELECT * FROM types ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		check(t, rs, "text")
		// Each value as a parameter finds its row.
		for i, col := range rs.Columns() {
			v := rows[0][i]
			q := "SELECT id FROM types WHERE " + col.Name + " = ?"
			switch col.Name {
			case "f", "j", "g", "bt":
				// FLOAT compares as a double, a BIT with a string as a
				// number; JSON and geometry not by =.
				continue
			}
			r, err := c.Query(hangGuard(t), q, v)
			if err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if !r.Next() || r.Values()[0] != int64(1) {
				t.Errorf("%s with %T: no row 1 (%v)", q, v, r.Err())
			}
			r.Close()
		}
		if err := st.Close(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
		// time.Time parameters are DATETIME in the configured zone.
		at := time.Date(2026, 11, 8, 12, 30, 45, 123456789, time.UTC)
		r, err := c.Query(hangGuard(t), "SELECT CAST(? AS DATETIME(6)), ? + INTERVAL 0 SECOND", at, true)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Next() {
			t.Fatal(r.Err())
		}
		if got := r.Values()[0].(mysqlwire.DateTime); got.Time(time.UTC) != at.Truncate(time.Microsecond) {
			t.Fatalf("time.Time: %v", got)
		}
		r.Close()
	})
}

func TestMySQLQueriesAndTransactions(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		c := s.root(t)
		mysqlDB(t, c, "fit_queries")
		mustExec(t, c, "CREATE TABLE kv (k INT PRIMARY KEY, v VARCHAR(20), n INT NOT NULL DEFAULT 0) ENGINE=InnoDB")

		// An error the server answers leaves the connection usable.
		_, err := c.Exec(hangGuard(t), "SELECT * FROM missing")
		var se *mysqlwire.Error
		if !errors.As(err, &se) || se.Code != 1146 || se.State != "42S02" {
			t.Fatalf("a missing table: %v", err)
		}
		if _, err := c.Query(hangGuard(t), "SELECT ? FROM missing", 1); !errors.As(err, &se) {
			t.Fatalf("a missing table, prepared: %v", err)
		}
		if err := c.Ping(hangGuard(t)); err != nil {
			t.Fatal(err)
		}

		// Many rows past the placeholder limit, in several statements,
		// then again as upserts.
		var rows [][]any
		for i := range 40000 {
			rows = append(rows, []any{i, fmt.Sprintf("v%d", i)})
		}
		res, err := c.InsertRows(hangGuard(t), "INSERT INTO kv (k, v) VALUES", rows, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.AffectedRows != 40000 {
			t.Fatalf("affected %d", res.AffectedRows)
		}
		res, err = c.InsertRows(hangGuard(t), "INSERT INTO kv (k, v) VALUES", rows[:100], "ON DUPLICATE KEY UPDATE n = n + 1")
		if err != nil || res.AffectedRows != 200 {
			t.Fatalf("upserts: %d, %v", res.AffectedRows, err)
		}
		count := func() int64 {
			r, err := c.Query(hangGuard(t), "SELECT COUNT(*), SUM(n) FROM kv")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if !r.Next() {
				t.Fatal(r.Err())
			}
			return r.Values()[0].(int64)
		}
		if n := count(); n != 40000 {
			t.Fatalf("%d rows", n)
		}

		// Transactions.
		if err := c.Begin(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
		if !c.InTransaction() {
			t.Fatal("not in a transaction after Begin")
		}
		mustExec(t, c, "INSERT INTO kv (k, v) VALUES (?, ?)", -1, "rolled back")
		if err := c.Rollback(hangGuard(t)); err != nil {
			t.Fatal(err)
		}
		if c.InTransaction() || count() != 40000 {
			t.Fatal("the rollback kept the row")
		}
		_ = c.Begin(hangGuard(t))
		res = mustExec(t, c, "INSERT INTO kv (k, v) VALUES (?, ?), (?, ?)", -1, "a", -2, "b")
		if res.AffectedRows != 2 {
			t.Fatalf("affected %d", res.AffectedRows)
		}
		if err := c.Commit(hangGuard(t)); err != nil || count() != 40002 {
			t.Fatalf("commit: %v", err)
		}

		// Several result sets from a procedure, and rows left unread.
		mustExec(t, c, "CREATE PROCEDURE p() BEGIN SELECT 1 AS a; SELECT k, v FROM kv WHERE k < 0 ORDER BY k; END")
		r, err := c.Query(hangGuard(t), "CALL p()")
		if err != nil {
			t.Fatal(err)
		}
		var sets []int
		for {
			n := 0
			for r.Next() {
				n++
			}
			if len(r.Columns()) > 0 {
				sets = append(sets, n)
			}
			if !r.NextResultSet() {
				break
			}
		}
		if err := r.Close(); err != nil || !reflect.DeepEqual(sets, []int{1, 2}) {
			t.Fatalf("CALL p(): %v, %v", sets, err)
		}
		r, err = c.Query(hangGuard(t), "SELECT * FROM kv")
		if err != nil {
			t.Fatal(err)
		}
		r.Next()
		if _, err := c.Exec(hangGuard(t), "SELECT 1"); !errors.Is(err, mysqlwire.ErrBusy) {
			t.Fatalf("a call with rows open: %v", err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
		// A statement executed many times.
		st, err := c.Prepare(hangGuard(t), "SELECT v FROM kv WHERE k = ?")
		if err != nil {
			t.Fatal(err)
		}
		for i := range 100 {
			r, err := st.Query(hangGuard(t), i)
			if err != nil {
				t.Fatal(err)
			}
			if !r.Next() || string(r.Values()[0].([]byte)) != fmt.Sprintf("v%d", i) {
				t.Fatalf("row %d: %v", i, r.Err())
			}
			r.Close()
		}
		st.Close(hangGuard(t))
		if _, err := st.Exec(hangGuard(t), 1); err == nil {
			t.Fatal("a closed statement ran")
		}
	})
}

func TestMySQLClosedMidResult(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		c := s.root(t)
		mysqlDB(t, c, "fit_closed")
		mustExec(t, c, "CREATE TABLE r (k INT PRIMARY KEY, v VARCHAR(200))")
		var rows [][]any
		for i := range 1000 {
			rows = append(rows, []any{i, strings.Repeat("x", 200)})
		}
		if _, err := c.InsertRows(hangGuard(t), "INSERT INTO r VALUES", rows, ""); err != nil {
			t.Fatal(err)
		}
		killer := s.root(t)
		for _, prepared := range []bool{false, true} {
			// About 400 MB of rows, more than any buffer holds.
			q := "SELECT a.v, b.v FROM r a, r b"
			var r *mysqlwire.Rows
			var err error
			if prepared {
				r, err = c.Query(hangGuard(t), q+" WHERE a.k >= ?", 0)
			} else {
				r, err = c.Query(hangGuard(t), q)
			}
			if err != nil {
				t.Fatal(err)
			}
			for range 10 {
				if !r.Next() {
					t.Fatal(r.Err())
				}
			}
			mustExec(t, killer, fmt.Sprintf("KILL %d", c.ConnectionID()))
			n := 0
			for r.Next() {
				n++
			}
			if r.Err() == nil || c.Err() == nil {
				t.Fatalf("prepared %v: the result ended without an error after %d more rows", prepared, n)
			}
			if err := c.Ping(hangGuard(t)); err == nil {
				t.Fatal("a ping on a killed connection")
			}
			c = s.root(t)
			_ = c.UseDatabase(hangGuard(t), "fit_closed")
		}

		// Timeouts: the context's and Config.Timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := c.Exec(ctx, "SELECT SLEEP(30)"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a context's deadline: %v", err)
		}
		cfg := s.config("root", s.password)
		cfg.TLS = s.tls()
		cfg.Timeout = 300 * time.Millisecond
		c = s.dial(t, cfg)
		if _, err := c.Exec(hangGuard(t), "SELECT SLEEP(30)"); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("Config.Timeout: %v", err)
		}
	})
}

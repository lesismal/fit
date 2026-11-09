package binlog_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// TestMySQLBinlogInterop runs the workload on each server of
// FIT_SYNC_MYSQL (test/sync/mysql.sh starts them), reads the binlog file
// it wrote with LOAD_FILE, and checks it decodes to the workload's rows.
// With FIT_SYNC_MYSQL_BINLOG_TESTDATA set to a directory, it writes each
// file there, as testdata's were:
//
//	FIT_SYNC_MYSQL_BINLOG_TESTDATA=$PWD/sync/mysql/binlog/testdata test/sync/mysql.sh -run MySQLBinlog
func TestMySQLBinlogInterop(t *testing.T) {
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
	w := loadWorkload(t)
	binlogLock(t)
	for _, s := range strings.Split(list, ",") {
		name, addr, _ := strings.Cut(s, "=")
		t.Run(name, func(t *testing.T) {
			cfg := mysqlwire.Config{Addr: addr, User: "root", Password: os.Getenv("FIT_SYNC_MYSQL_PASSWORD"),
				TLS: &tls.Config{RootCAs: pool}}
			if name == "mysql57" {
				// 5.7's image offers no ECDHE suites (see test/sync).
				cfg.TLS.CipherSuites = []uint16{tls.TLS_RSA_WITH_AES_128_GCM_SHA256, tls.TLS_RSA_WITH_AES_256_GCM_SHA384}
			}
			file := captureWorkload(t, w, name, cfg)
			if dir := os.Getenv("FIT_SYNC_MYSQL_BINLOG_TESTDATA"); dir != "" {
				if err := os.WriteFile(filepath.Join(dir, name+".binlog"), file, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			d := w.decodeFile(t, name, file)
			checkEvents(t, name, d.events)
			w.check(t, name, d.rows)
		})
	}
}

// captureWorkload runs the workload in a binlog file of its own and
// returns the file.
func captureWorkload(t *testing.T, w *workload, server string, cfg mysqlwire.Config) []byte {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := mysqlwire.Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	exec := func(q string) {
		t.Helper()
		if _, err := c.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	query := func(q string) [][]string {
		t.Helper()
		rs, err := c.Query(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var out [][]string
		for rs.Next() {
			var row []string
			for _, v := range rs.Values() {
				b, _ := v.([]byte)
				row = append(row, string(b))
			}
			out = append(out, row)
		}
		if err := rs.Close(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The servers with FULL metadata: 8.4 and MariaDB 11; 8.0 has
	// MySQL's default, MINIMAL, and 5.7 and MariaDB 10.11 none.
	if server == "mysql84" || server == "mariadb11" {
		old := query("SELECT @@GLOBAL.binlog_row_metadata")[0][0]
		exec("SET GLOBAL binlog_row_metadata = 'FULL'")
		defer func() { _, _ = c.Exec(context.Background(), "SET GLOBAL binlog_row_metadata = '"+old+"'") }()
	}
	exec("DROP DATABASE IF EXISTS " + workloadDB)
	exec("CREATE DATABASE " + workloadDB + " CHARACTER SET utf8mb4")
	if err := c.UseDatabase(ctx, workloadDB); err != nil {
		t.Fatal(err)
	}
	exec("SET SESSION time_zone = '+00:00'")
	exec("SET SESSION sql_mode = ''")
	for _, tb := range w.Tables {
		exec(tb.Create)
	}
	exec("FLUSH BINARY LOGS")
	logs := query("SHOW BINARY LOGS")
	file := logs[len(logs)-1][0]
	for _, st := range w.Steps {
		if applies(st.Only, server) {
			exec(st.SQL)
		}
	}
	exec("FLUSH BINARY LOGS")
	dir := path.Dir(query("SELECT @@log_bin_basename")[0][0])
	rs, err := c.Query(ctx, "SELECT LOAD_FILE(?)", dir+"/"+file)
	if err != nil {
		t.Fatal(err)
	}
	var b []byte
	for rs.Next() {
		v, _ := rs.Values()[0].([]byte)
		b = append([]byte(nil), v...)
	}
	if err := rs.Close(); err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatalf("LOAD_FILE(%s/%s) is NULL: is secure_file_priv off, and the file readable by all?", dir, file)
	}
	return b
}

// binlogLock keeps test/sync's source tests from purging binlogs while
// this test reads its own: they take the same lock file.
func binlogLock(t *testing.T) {
	t.Helper()
	p := filepath.Join(os.TempDir(), "fit-sync-mysql-binlog.lock")
	deadline := time.Now().Add(3 * time.Minute) // a lock left by a test killed is taken after it
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			f.Close()
			t.Cleanup(func() { os.Remove(p) })
			return
		}
		if time.Now().After(deadline) {
			os.Remove(p)
			deadline = time.Now().Add(3 * time.Minute)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

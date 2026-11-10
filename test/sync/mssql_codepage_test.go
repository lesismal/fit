package synctest

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// TestMSSQLSourceCodePage: varchar, char and varchar(max) columns of
// Chinese_PRC_CI_AS (code page 936) holding Chinese text reach the records
// as text, not bytes, in the snapshot and in the changes (I215 decision 5,
// the source casting them to nvarchar). With CDC, in fit_sync_cp936, a
// database of that collation: CDC keeps the non-ASCII characters of a
// varchar column only in the database's own collation (they are question
// marks in the change table otherwise, a limit Microsoft documents): its
// Latin varchar's ñ, which code page 936 lacks, is kept by the snapshot,
// lost by CDC, and the connection test warns of it (I215 decision 14);
// not of a column of another collation of the same code page
// (Chinese_PRC_BIN), whose characters CDC keeps. With change tracking, which reads the
// rows themselves, in fit_sync, whose collation is Latin, beside a Latin
// varchar decoded as before, of which there is no warning.
func TestMSSQLSourceCodePage(t *testing.T) {
	eachMSSQL(t, func(t *testing.T, s mssqlServer) {
		for i, mode := range []string{"cdc", "change_tracking"} {
			t.Run(mode, func(t *testing.T) {
				cfg := s.config(mssqlwire.EncryptOn)
				latin := ", l varchar(20) COLLATE Latin1_General_CI_AS NULL"
				if mode == "cdc" {
					cfg.Database = "fit_sync_cp936"
				}
				ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
				defer cancel()
				c, err := mssqlwire.Dial(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				table := fmt.Sprintf("cp936_%d", time.Now().UnixNano())
				mssqlExec(t, c, "CREATE TABLE dbo."+table+` (id int PRIMARY KEY,
v varchar(20) COLLATE Chinese_PRC_CI_AS NULL, f char(8) COLLATE Chinese_PRC_CI_AS NULL,
m varchar(max) COLLATE Chinese_PRC_CI_AS NULL, b varchar(20) COLLATE Chinese_PRC_BIN NULL`+latin+")")
				t.Cleanup(func() { c.Exec(context.Background(), "DROP TABLE dbo."+table) })
				values := func(id int64, n string) string {
					v := fmt.Sprintf("%d, N'中文%s', N'汉字%s', N'长文本%s', N'同页%s', N'cañón'", id, n, n, n, n)
					return "INSERT INTO dbo." + table + " VALUES (" + v + ")"
				}
				mssqlExec(t, c, values(1, "一"))
				if mode == "cdc" {
					mssqlExec(t, c, "EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = @p1, @role_name = NULL", table)
				} else {
					mssqlExec(t, c, "ALTER TABLE dbo."+table+" ENABLE CHANGE_TRACKING")
				}
				// check checks a record's text; the Latin column's with change
				// tracking, and in CDC's snapshot alone (lossy).
				check := func(r *fitsync.Record, n, pad string, lossy bool) {
					t.Helper()
					want := map[string]string{"v": "中文" + n, "f": "汉字" + n + pad, "m": "长文本" + n, "b": "同页" + n}
					if !lossy {
						want["l"] = "cañón"
					} else if l, _ := r.After.Get("l"); l.Text() == "cañón" {
						// ñ is not in code page 936 (ó is).
						t.Fatalf("the Latin column through CDC kept %q", l.Text())
					} else {
						t.Logf("the Latin column through CDC, its collation not the database's: %q", l.Text())
					}
					for col, w := range want {
						v, _ := r.After.Get(col)
						if v.Kind() != fitsync.KindString || v.Text() != w {
							t.Fatalf("%s of %s: kind %v %q, want the text %q", col, r.Key.Text(), v.Kind(), v.Text(), w)
						}
					}
				}
				src := mssqlSourceAt(t, s, map[string]any{"tables": []string{"dbo." + table}, "mode": mode, "database": cfg.Database})
				// The connection test warns of the Latin column with CDC,
				// and of nothing with change tracking.
				ws, err := src.(fitsync.WarningTester).TestWarnings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "cdc" {
					want := "the column l of dbo." + table + " is of the collation Latin1_General_CI_AS, not the database's Chinese_PRC_CI_AS: "
					if len(ws) != 1 || !strings.HasPrefix(ws[0], want) || !strings.Contains(ws[0], "reach the sinks as ?") {
						t.Fatalf("warnings %q, want one starting %q", ws, want)
					}
					t.Logf("warning: %s", ws[0])
				} else if len(ws) != 0 {
					t.Fatalf("warnings %q", ws)
				}
				var mu sync.Mutex
				var recs []*fitsync.Record
				got := make(chan struct{}, 1)
				done := make(chan error, 1)
				go func() {
					done <- src.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
						mu.Lock()
						recs = append(recs, r)
						mu.Unlock()
						select {
						case got <- struct{}{}:
						default:
						}
						return nil
					}, Start: func(fitsync.Position) error { return nil }})
				}()
				// record waits for the row id's record.
				record := func(id int64) *fitsync.Record {
					t.Helper()
					for {
						mu.Lock()
						for _, r := range recs {
							if k, _ := r.Key.Get("id"); k.AsInt() == id && r.Op != fitsync.OpDelete && r.Op != fitsync.OpDDL {
								mu.Unlock()
								return r
							}
						}
						mu.Unlock()
						select {
						case <-got:
						case err := <-done:
							t.Fatal(err)
						case <-ctx.Done():
							t.Fatalf("no record of row %d", id)
						}
					}
				}
				check(record(1), "一", "  ", false)
				mssqlExec(t, c, values(int64(2+i), "二"))
				check(record(int64(2+i)), "二", "  ", mode == "cdc")
				cancel()
				<-done
			})
		}
	})
}

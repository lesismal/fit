package synctest

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"
)

// TestMySQLTextInsertsMatchPrepared writes every column type's rows, and
// rows of the characters a literal escapes, once as parameters
// (InsertRows) and once as literals (InsertRowsText), under the default
// sql_mode and under NO_BACKSLASH_ESCAPES: the two tables read back the
// same, byte for byte, as the sink writes either way.
func TestMySQLTextInsertsMatchPrepared(t *testing.T) {
	eachMySQL(t, func(t *testing.T, s mysqlServer) {
		c := s.root(t)
		mysqlDB(t, c, "fit_literals")
		rows := mysqlRows(17 << 20)
		tricky := make([]any, len(rows[0]))
		tricky[0] = int64(4)
		col := func(name string) int {
			for i, n := range []string{"id", "ti", "tiu", "si", "siu", "mi", "miu", "i", "iu", "bi", "biu", "f", "d", "de", "bt", "b1",
				"c", "vc", "bn", "vb", "tt", "t", "mt", "lt", "tb", "bl", "mb", "lb", "e", "st", "dt", "dtm", "dt0", "ts", "tm", "y", "j", "g"} {
				if n == name {
					return i
				}
			}
			t.Fatalf("no column %s", name)
			return 0
		}
		all := make([]byte, 256)
		for i := range all {
			all[i] = byte(i)
		}
		tricky[col("f")] = float32(0.1)
		tricky[col("d")] = 0.1
		tricky[col("c")] = "a'\\\"%_\n"
		tricky[col("vc")] = "\x00\x1a\r\n'\"\\ \\' '' é😀 \\%\\_"
		tricky[col("vb")] = all
		tricky[col("t")] = strings.Repeat(`\'"`, 1000)
		tricky[col("bl")] = all
		tricky[col("j")] = `{"q": "it's \"x\" \\ y", "u": "é😀\u0000"}`
		tricky[col("de")] = "-0.000000000000000000000000000001"
		rows = append(rows, tricky)
		floats := make([]any, len(rows[0]))
		floats[0] = int64(5)
		floats[col("f")] = float32(math.MaxFloat32)
		floats[col("d")] = -math.MaxFloat64
		floats[col("bi")] = int64(math.MaxInt64)
		floats[col("b1")] = true
		rows = append(rows, floats)
		for _, mode := range []string{"", "NO_BACKSLASH_ESCAPES", "STRICT_ALL_TABLES", "STRICT_ALL_TABLES,NO_BACKSLASH_ESCAPES"} {
			t.Run("sql_mode="+mode, func(t *testing.T) {
				mustExec(t, c, "SET time_zone = '+00:00', sql_mode = '"+mode+"'")
				rows := rows
				if strings.Contains(mode, "STRICT") {
					// The zeros' row, whose empty ENUM strict mode refuses
					// either way.
					rows = append(rows[:2:2], rows[3:]...)
				}
				for _, table := range []string{"types", "types2"} {
					mustExec(t, c, "DROP TABLE IF EXISTS "+table)
					mustExec(t, c, strings.Replace(mysqlTypes, "CREATE TABLE types", "CREATE TABLE "+table, 1))
				}
				for _, r := range rows {
					if _, err := c.InsertRows(hangGuard(t), "INSERT INTO types VALUES", [][]any{r}, ""); err != nil {
						t.Fatalf("parameters, row %v: %v", r[0], err)
					}
				}
				// Every row in one statement, which the large one splits.
				if _, err := c.InsertRowsText(hangGuard(t), "INSERT INTO types2 VALUES", rows, ""); err != nil {
					t.Fatalf("literals: %v", err)
				}
				read := func(table string) [][]any {
					rs, err := c.Query(hangGuard(t), "SELECT * FROM "+table+" ORDER BY id")
					if err != nil {
						t.Fatal(err)
					}
					var out [][]any
					for rs.Next() {
						var r []any
						for _, v := range rs.Values() {
							if b, ok := v.([]byte); ok {
								v = bytes.Clone(b)
							}
							r = append(r, v)
						}
						out = append(out, r)
					}
					if err := rs.Close(); err != nil {
						t.Fatal(err)
					}
					return out
				}
				a, b := read("types"), read("types2")
				if len(a) != len(rows) || len(b) != len(rows) {
					t.Fatalf("%d and %d rows, want %d", len(a), len(b), len(rows))
				}
				for i := range a {
					for j := range a[i] {
						x, y := a[i][j], b[i][j]
						if xb, ok := x.([]byte); ok {
							if yb, ok := y.([]byte); !ok || !bytes.Equal(xb, yb) {
								t.Errorf("row %v column %d: %.80q as parameters, %.80q as literals", a[i][0], j, x, y)
							}
						} else if fmt.Sprint(x) != fmt.Sprint(y) {
							t.Errorf("row %v column %d: %v as parameters, %v as literals", a[i][0], j, x, y)
						}
					}
				}
			})
		}
	})
}

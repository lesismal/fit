package mysqlwire

import (
	"strings"
	"testing"
)

// BenchmarkPrepareParams prepares a statement of 4,000 parameters, as
// InsertRows does for a batch of 1,000 rows of four columns: the server
// sends a definition for each parameter, which the client reads and does
// not keep.
func BenchmarkPrepareParams(b *testing.B) {
	const n = 4000
	q := "INSERT INTO t VALUES " + strings.Repeat("(?,?,?,?),", n/4-1) + "(?,?,?,?)"
	addr := serve(&testing.T{}, func(f *fake) {
		f.login(fakeCaps | capDeprecateEOF)
		for {
			cmd, _ := f.cmd()
			if cmd != comStmtPrepare {
				return
			}
			f.prepareOK(1, n, nil)
		}
	})
	c, err := Dial(b.Context(), Config{Addr: addr, User: "u"})
	if err != nil {
		b.Fatal(err)
	}
	defer c.Close()
	b.ReportAllocs()
	for b.Loop() {
		st, err := c.Prepare(b.Context(), q)
		if err != nil || st.NumParams() != n {
			b.Fatal(st, err)
		}
	}
}

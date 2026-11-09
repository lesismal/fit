package syncmysql

import (
	"testing"

	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

// TestWireValueExported: the exported WireValue is the source's.
func TestWireValueExported(t *testing.T) {
	col := &mysqlwire.Column{Type: mysqlwire.TypeNewDecimal}
	if v := WireValue(col, []byte("1.50")); v.Text() != "1.50" || !v.Equal(wireValue(col, []byte("1.50"))) {
		t.Fatal(v)
	}
}

package syncmssql

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	mssqlwire "github.com/lesismal/fit/sync/mssql/wire"
)

// value makes a column's value a record's.
func value(col *mssqlwire.Column, v any) fitsync.Value {
	switch x := v.(type) {
	case nil:
		return fitsync.Null()
	case int64:
		return fitsync.Int(x)
	case bool:
		return fitsync.Bool(x)
	case float32:
		return fitsync.Float(float64(x))
	case float64:
		return fitsync.Float(x)
	case mssqlwire.Decimal:
		return fitsync.Decimal(string(x))
	case time.Time:
		if col.Type == mssqlwire.TypeDateN {
			return fitsync.Date(x.Year(), x.Month(), x.Day())
		}
		return fitsync.Time(x)
	case time.Duration:
		return fitsync.String(clock(x, int(col.Scale)))
	case mssqlwire.UUID:
		return fitsync.String(x.String())
	case string:
		return fitsync.String(x)
	case []byte:
		return fitsync.Bytes(x)
	}
	return fitsync.String(fmt.Sprint(v))
}

// clock writes a time of day as SQL Server does, hh:mm:ss and scale
// digits of fraction.
func clock(d time.Duration, scale int) string {
	ns := int64(d)
	s := fmt.Sprintf("%02d:%02d:%02d", ns/3600e9, ns/60e9%60, ns/1e9%60)
	if scale > 0 {
		s += "." + fmt.Sprintf("%09d", ns%1e9)[:min(scale, 9)]
	}
	return s
}

// paramOf makes a key's value a query parameter.
func paramOf(v fitsync.Value) any {
	switch v.Kind() {
	case fitsync.KindNull:
		return nil
	case fitsync.KindInt:
		return v.AsInt()
	case fitsync.KindBool:
		return v.AsBool()
	case fitsync.KindFloat:
		return v.AsFloat()
	case fitsync.KindDecimal:
		return mssqlwire.Decimal(v.Text())
	case fitsync.KindBytes:
		return []byte(v.Text())
	case fitsync.KindTime:
		return v.AsTime()
	case fitsync.KindDate:
		return mssqlwire.Param{Value: v.AsTime(), Type: "date"}
	}
	return v.Text()
}

// quoteName quotes a name in brackets.
func quoteName(s string) string { return mssqlwire.QuoteName(s) }

// lsn is a log sequence number, its 10 bytes as hex, which orders as the
// LSNs do.
type lsn string

func lsnOf(v any) lsn {
	b, _ := v.([]byte)
	return lsn(hex.EncodeToString(b))
}

func (l lsn) bytes() []byte {
	b, _ := hex.DecodeString(string(l))
	return b
}

// param is l as a binary(10) parameter.
func (l lsn) param() mssqlwire.Param {
	return mssqlwire.Param{Value: l.bytes(), Type: "binary(10)"}
}

// next is the LSN after l, as sys.fn_cdc_increment_lsn makes it.
func (l lsn) next() lsn {
	b := l.bytes()
	for i := len(b) - 1; i >= 0; i-- {
		b[i]++
		if b[i] != 0 {
			break
		}
	}
	return lsn(hex.EncodeToString(b))
}

// prev is the LSN before l.
func (l lsn) prev() lsn {
	b := l.bytes()
	for i := len(b) - 1; i >= 0; i-- {
		b[i]--
		if b[i] != 0xff {
			break
		}
	}
	return lsn(hex.EncodeToString(b))
}

// validLSN reports whether s is an LSN's hex.
func validLSN(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 10 && strings.ToLower(s) == s
}

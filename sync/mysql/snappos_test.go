package syncmysql

import (
	"bytes"
	"encoding/json"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

// appendSnapPos writes what json.Marshal of a snapPos does, for every
// kind of key and for table names json escapes.
func TestAppendSnapPos(t *testing.T) {
	keys := []fitsync.Value{
		fitsync.Null(),
		fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(7)}),
		fitsync.Map(fitsync.Field{Name: "a<b", Value: fitsync.String("x&\"y\" é")},
			fitsync.Field{Name: "n", Value: fitsync.Decimal("1.50")}),
		fitsync.Map(fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(`{"a": [1, 2]}`))}),
	}
	for _, ns := range []string{"shop.items", "a<b>.c&d", "é.\"t\""} {
		head := snapPosHead(ns)
		for _, k := range keys {
			kb, _ := k.MarshalJSON()
			want, _ := json.Marshal(snapPos{Table: ns, Key: kb})
			if got := appendSnapPos(head, k); !bytes.Equal(got, want) {
				t.Errorf("%s, %s: %s, want %s", ns, kb, got, want)
			}
		}
	}
	// A key that does not encode leaves the key out, as omitempty did.
	bad := fitsync.Map(fitsync.Field{Name: "j", Value: fitsync.JSON([]byte(`{`))})
	want, _ := json.Marshal(snapPos{Table: "t"})
	if got := appendSnapPos(snapPosHead("t"), bad); !bytes.Equal(got, want) {
		t.Errorf("a bad key: %s, want %s", got, want)
	}
}

func BenchmarkSnapPos(b *testing.B) {
	key := fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.Int(123456)})
	b.Run("marshal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			kb, _ := key.MarshalJSON()
			_, _ = json.Marshal(snapPos{Table: "shop.items", Key: kb})
		}
	})
	b.Run("append", func(b *testing.B) {
		head := snapPosHead("shop.items")
		b.ReportAllocs()
		for b.Loop() {
			_ = appendSnapPos(head, key)
		}
	})
}

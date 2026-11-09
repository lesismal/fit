package vecutil

import (
	"math"
	"slices"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

func TestLast(t *testing.T) {
	if got := Last([]string{"a", "b", "a", "c", "b"}); !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatal(got)
	}
	if got := Last(nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestKeys(t *testing.T) {
	f := func(name string, v fitsync.Value) fitsync.Field { return fitsync.Field{Name: name, Value: v} }
	for _, c := range []struct {
		key  fitsync.Value
		want string
	}{
		{fitsync.Int(7), "7"},
		{fitsync.Map(f("id", fitsync.String("a:b"))), "a:b"},
		{fitsync.Map(f("a", fitsync.String(`x:y\`)), f("b", fitsync.Int(2))), `x\:y\\:2`},
		{fitsync.Bytes([]byte{1, 2}), "AQI="},
		{fitsync.Array(fitsync.Int(1), fitsync.String("x")), `[1,"x"]`},
		{fitsync.Array(fitsync.Float(math.NaN())), `[{"float":"NaN"}]`},
	} {
		if got, ok := KeyText(c.key); !ok || got != c.want {
			t.Errorf("%v: %q", c.key, got)
		}
	}
	if _, ok := KeyText(fitsync.Null()); ok {
		t.Error("a null key")
	}
	if v, ok := Single(fitsync.Map(f("id", fitsync.Int(3)))); !ok || v.AsInt() != 3 {
		t.Error(v)
	}
	if v, ok := Single(fitsync.String("s")); !ok || v.Text() != "s" {
		t.Error(v)
	}
	if _, ok := Single(fitsync.Null()); ok {
		t.Error("a null key")
	}
	if _, ok := Single(fitsync.Map(f("a", fitsync.Int(1)), f("b", fitsync.Int(2)))); ok {
		t.Error("two fields")
	}
	row := fitsync.Map(f("a", fitsync.Int(1)))
	if !RowOf(&fitsync.Record{After: row}).Equal(row) || !RowOf(&fitsync.Record{Before: row}).Equal(row) {
		t.Error("RowOf")
	}
}

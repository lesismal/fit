package syncpgsink

import (
	"math"
	"reflect"
	"strings"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

// TestGroupsAcrossSkips: a DDL between rows of one table does not split
// their statement, and deletes of several keys go in one statement.
func TestGroupsAcrossSkips(t *testing.T) {
	f := newFake(t)
	s := sinkOf(t, f, map[string]any{"copy": "never"})
	v := func(s string) fitsync.Field { return col("v", fitsync.String(s)) }
	batch := []*fitsync.Record{
		rec("t", fitsync.OpInsert, 1, v("a")),
		{Namespace: "t", Op: fitsync.OpDDL},
		rec("t", fitsync.OpInsert, 2, v("b")),
		rec("t", fitsync.OpInsert, 3, v("c")),
		rec("t", fitsync.OpDelete, 1, v("a")),
		rec("t", fitsync.OpDelete, 3, v("c")),
	}
	if err := s.Write(ctx(t), batch); err != nil {
		t.Fatal(err)
	}
	if got := dump(f.table(`"t"`), "id", "v"); got != "[2|b]" {
		t.Error(got)
	}
	want := []string{
		"BEGIN",
		`INSERT INTO "t" ("v","id") VALUES ($1,$2),($3,$4),($5,$6) ON CONFLICT ("id") DO UPDATE SET "v" = EXCLUDED."v"`,
		`DELETE FROM "t" WHERE "id" IN ($1,$2)`,
		"COMMIT",
	}
	if got := f.statements(); !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
}

func TestParamNotJSON(t *testing.T) {
	// A map holding what JSON cannot say is refused, not written.
	_, err := Param(fitsync.Map(fitsync.Field{Name: "f", Value: fitsync.Float(math.NaN())}))
	if err == nil || !strings.Contains(err.Error(), "not a JSON number") {
		t.Fatalf("err %v", err)
	}
}

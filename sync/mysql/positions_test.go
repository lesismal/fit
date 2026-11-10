package syncmysql

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/mysql/binlog"
)

// TestSourcePositionOnly: a transaction of none of the read tables hands
// on the stream's position as a position-only record, so that a quiet
// table's checkpoint follows the binlog; one of the read tables' records
// carries the position itself.
func TestSourcePositionOnly(t *testing.T) {
	f := newSrcFake()
	st := newStream("bin.000001", 157, "8.4.3-log").
		gtid(uuidA, 6).query("shop", "BEGIN").table(10, "shop", "t", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 10, img(1, "a")).xid().
		gtid(uuidA, 7).query("other", "BEGIN").table(11, "other", "x", fullNames, 0).
		rows(binlog.WriteRowsEventV2, 11, img(1, "x")).xid().
		gtid(uuidA, 8).query("other", "CREATE TABLE y (id INT)").
		heartbeat()
	f.streams = [][]evItem{st.items}
	s := newSource(t, f, map[string]any{"tables": []string{"shop.*"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var recs []*fitsync.Record
	var pos []string
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, &fitsync.Run{Emit: func(r *fitsync.Record) error {
			mu.Lock()
			recs = append(recs, r)
			mu.Unlock()
			return nil
		}, Positions: func(p func() fitsync.Position) error {
			mu.Lock()
			pos = append(pos, string(p()))
			mu.Unlock()
			return nil
		}})
	}()
	for deadline := time.Now().Add(time.Minute); ; time.Sleep(time.Millisecond) {
		mu.Lock()
		n := len(pos)
		mu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no position-only records")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(recs) != 1 || !strings.Contains(string(recs[0].Position), uuidA+":1-6") {
		t.Fatalf("%v", recStrings(recs))
	}
	if !strings.Contains(pos[0], `"gtids":"`+uuidA+`:1-7"`) || !strings.Contains(pos[1], `"gtids":"`+uuidA+`:1-8"`) {
		t.Fatal(pos)
	}

	// Its error is the run's.
	f.streams = [][]evItem{st.items}
	boom := errors.New("boom")
	err := s.Run(context.Background(), &fitsync.Run{Emit: func(*fitsync.Record) error { return nil },
		Positions: func(func() fitsync.Position) error { return boom }})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
}

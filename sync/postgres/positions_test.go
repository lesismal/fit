package syncpostgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// TestSourcePositionOnly: transactions of no records (other tables'), and
// keepalives between transactions, hand on their end as position-only
// records, so that a quiet table's checkpoint follows the WAL; nothing at
// or before the checkpoint, inside a transaction or while a streamed one
// is spooled.
func TestSourcePositionOnly(t *testing.T) {
	cat := newSrcCatalog()
	f := newSrcFake(cat.answer)
	srcUseFake(t, f, nil)
	s := srcMust(t, map[string]any{"status_interval": "5ms", "spool_dir": t.TempDir()})
	for _, m := range []srcMsg{
		msgRel(1, "t", 'd', "id"),
		msgBegin(0x50, 9), msgCommit(0x60), // at the checkpoint
		msgBegin(0x100, 10), msgCommit(0x110),
		msgBegin(0x200, 11), newMsg('I').u32(1).u8('N').tuple("1", "one", nil), msgCommit(0x210),
	} {
		f.msgs <- m.data()
	}
	var mu sync.Mutex
	var recs []*fitsync.Record
	var pos []string
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- s.Run(ctx, &fitsync.Run{From: lsnPosition(0x60), Emit: func(r *fitsync.Record) error {
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
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x40} // before the checkpoint
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x300}
	f.msgs <- msgBegin(0x400, 12).data()
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x500} // mid-transaction
	f.msgs <- newMsg('I').u32(1).u8('N').tuple("2", "two", nil).data()
	f.msgs <- msgCommit(0x410).data()
	f.msgs <- &pgwire.Keepalive{ServerEnd: 0x600}
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(pos) == 3 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got := strings.Join(pos, " "); got != `{"lsn":"0/110"} {"lsn":"0/300"} {"lsn":"0/600"}` {
		t.Fatal(got)
	}
	if len(recs) != 2 {
		t.Fatalf("%d records", len(recs))
	}
}

// TestSourcePositionOnlyError: the position-only record's error is the
// run's, from a transaction's end and from a keepalive.
func TestSourcePositionOnlyError(t *testing.T) {
	for _, m := range []any{msgBegin(0x100, 10), &pgwire.Keepalive{ServerEnd: 0x300}} {
		cat := newSrcCatalog()
		f := newSrcFake(cat.answer)
		srcUseFake(t, f, nil)
		s := srcMust(t, map[string]any{"status_interval": "5ms", "spool_dir": t.TempDir()})
		if b, ok := m.(srcMsg); ok {
			f.msgs <- b.data()
			f.msgs <- msgCommit(0x110).data()
		} else {
			f.msgs <- m
		}
		boom := errors.New("boom")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		err := s.Run(ctx, &fitsync.Run{From: lsnPosition(0x60), Emit: func(*fitsync.Record) error { return nil },
			Positions: func(func() fitsync.Position) error { return boom }})
		cancel()
		if !errors.Is(err, boom) {
			t.Fatal(err)
		}
	}
}

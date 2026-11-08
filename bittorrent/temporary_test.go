package bittorrent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	fibbt "github.com/lesismal/fib/bittorrent"
)

// TestTemporary checks the hooks bt_stream uses for the torrents it adds on
// the fly: the bound, marking, removing with the data, the bytes wanted,
// the priorities, and the removal of those a stop left when the state is
// read again.
func TestTemporary(t *testing.T) {
	e := startEngine(t)
	dl := t.TempDir()
	torrent, _ := payload(t, dl, "movie.bin", 300000)
	other, _ := payload(t, dl, "other.bin", 200000)
	third, _ := payload(t, dl, "third.bin", 100000)
	dataDir := t.TempDir()
	r := startRole(t, e, clientJSON(dataDir, dl, ""), nil)

	s, err := r.AddTemporary(AddRequest{Torrent: torrent}, 1)
	if err != nil || !s.Temporary {
		t.Fatalf("%+v, %v", s, err)
	}
	if _, err := r.AddTemporary(AddRequest{Torrent: other}, 1); !errors.Is(err, ErrTooManyTemporary) {
		t.Fatalf("past max: %v", err)
	}
	o, err := r.Add(AddRequest{Torrent: other})
	if err != nil || o.Temporary {
		t.Fatalf("%+v, %v", o, err)
	}
	waitFor(t, "the torrent to check its data", func() bool {
		tor, err := r.Torrent(s.ID)
		return err == nil && tor.HaveAll()
	})
	if n := r.TemporaryBytes(); n != 300000 {
		t.Fatalf("temporary bytes %d", n)
	}
	if err := r.SetFilePriorities(s.ID, []int{fibbt.PriorityNone}); err != nil {
		t.Fatal(err)
	}
	if n := r.TemporaryBytes(); n != 0 {
		t.Fatalf("temporary bytes %d with its file left out", n)
	}
	if err := r.SetFilePriorities(s.ID, []int{1, 2}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("two priorities for one file: %v", err)
	}
	if err := r.SetFilePriorities(s.ID, []int{9}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("a priority of 9: %v", err)
	}
	if err := r.SetFilePriorities(s.ID, []int{fibbt.PriorityNormal}); err != nil {
		t.Fatal(err)
	}
	// Not temporary: RemoveTemporary leaves it.
	if removed, err := r.RemoveTemporary(o.ID); removed || err != nil {
		t.Fatalf("removed %v, %v", removed, err)
	}
	if err := r.SetTemporary(o.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetTemporary(o.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := r.SetTemporary("nope", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}

	// A stop leaves the temporary torrent: the next start removes it with
	// its data, and keeps the other.
	r.Stop(context.Background())
	r2 := startRole(t, e, clientJSON(dataDir, dl, ""), nil)
	if _, err := r2.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the temporary torrent is back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dl, "movie.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its data is left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, torrentDir, s.ID+".torrent")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its .torrent file is left: %v", err)
	}
	if _, err := r2.Get(o.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dl, "other.bin")); err != nil {
		t.Fatal(err)
	}

	t3, err := r2.AddTemporary(AddRequest{Torrent: third}, 5)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the torrent to check its data", func() bool {
		tor, err := r2.Torrent(t3.ID)
		return err == nil && tor.HaveAll()
	})
	if removed, err := r2.RemoveTemporary(t3.ID); !removed || err != nil {
		t.Fatalf("removed %v, %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dl, "third.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("its data is left: %v", err)
	}
}

package bittorrent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// TestBadState checks that a state that does not read keeps the role from
// starting, and is left as it was.
func TestBadState(t *testing.T) {
	e := startEngine(t)
	id := strings.Repeat("ab", 20)
	for name, body := range map[string]string{
		"garbage":   "{not json",
		"version":   `{"version": 99, "torrents": []}`,
		"bad id":    `{"version": 1, "torrents": [{"id": "XYZ", "dir": "/tmp"}]}`,
		"twice":     `{"version": 1, "torrents": [{"id": "` + id + `", "dir": "/tmp", "magnet": "m"}, {"id": "` + id + `", "dir": "/tmp", "magnet": "m"}]}`,
		"relative":  `{"version": 1, "torrents": [{"id": "` + id + `", "dir": "downloads", "magnet": "m"}]}`,
		"oversized": "",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, stateFile)
			if name == "oversized" {
				f, err := os.Create(p)
				if err != nil {
					t.Fatal(err)
				}
				// Sparse: the size is refused before anything is read.
				if err := f.Truncate(MaxStateBytes + 1); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			} else if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(p)
			r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
			err := r.Apply(member(t, clientJSON(dir, "", "")))
			r.Stop(context.Background())
			if !errors.Is(err, ErrState) {
				t.Fatalf("started on a bad state: %v", err)
			}
			after, _ := os.Stat(p)
			if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("the bad state was overwritten")
			}
		})
	}
}

// TestBrokenTorrentKept checks that a torrent whose own files do not read
// stays listed, with its error, until removed, while the others run.
func TestBrokenTorrentKept(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "ok.bin", 64<<10)
	data := t.TempDir()
	r := startRole(t, e, clientJSON(data, src, ""), nil)
	good, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	r.Stop(context.Background())

	// A torrent whose .torrent file is garbage, and one with no source.
	bad1, bad2 := strings.Repeat("11", 20), strings.Repeat("22", 20)
	if err := os.WriteFile(filepath.Join(data, torrentDir, bad1+".torrent"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := os.ReadFile(filepath.Join(data, stateFile))
	if err != nil {
		t.Fatal(err)
	}
	x := strconv.Quote(t.TempDir())
	s := strings.Replace(string(list), `"torrents": [`, `"torrents": [{"id": "`+bad1+`", "dir": `+x+`, "added": "2026-01-01T00:00:00Z"}, {"id": "`+bad2+`", "dir": `+x+`, "added": "2026-01-01T00:00:00Z"},`, 1)
	if err := os.WriteFile(filepath.Join(data, stateFile), []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	// A resume file that is garbage: the torrent is checked in full.
	if err := os.WriteFile(filepath.Join(data, resumeDir, good.ID+".resume"), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}

	r2 := startRole(t, e, clientJSON(data, src, ""), nil)
	got := r2.List()
	if len(got) != 3 || got[0].State != "error" || got[1].State != "error" || got[2].ID != good.ID {
		t.Fatalf("restored %+v", got)
	}
	if err := r2.Pause(bad1); !errors.Is(err, ErrBroken) {
		t.Fatalf("pause of a broken torrent: %v", err)
	}
	waitFor(t, "the good torrent's check", func() bool {
		tor, err := r2.Torrent(good.ID)
		return err == nil && tor.HaveAll()
	})
	for _, id := range []string{bad1, bad2} {
		if err := r2.Remove(id, false); err != nil {
			t.Fatal(err)
		}
	}
	if err := r2.Save(); err != nil {
		t.Fatal(err)
	}
	r2.Stop(context.Background())
	recs, err := loadState(data)
	if err != nil || len(recs) != 1 || recs[0].id != good.ID || len(recs[0].resume) == 0 || string(recs[0].resume) == "garbage" {
		t.Fatalf("saved %v %v", recs, err)
	}
}

// TestSaveFailure checks that a state directory that cannot be written is
// reported by Save, and saved once it can.
func TestSaveFailure(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "f.bin", 64<<10)
	data := filepath.Join(t.TempDir(), "state")
	r := startRole(t, e, strings.Replace(clientJSON(data, src, ""), `"50ms"`, `"1h"`, 1), nil)
	// The resume directory is a file: its writes fail.
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, resumeDir), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the check", func() bool {
		tor, err := r.Torrent(st.ID)
		return err == nil && tor.HaveAll()
	})
	if err := r.Save(); err == nil || !strings.Contains(err.Error(), "resume data") {
		t.Fatalf("save into a file: %v", err)
	}
	if err := os.Remove(filepath.Join(data, resumeDir)); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, resumeDir, st.ID+".resume")); err != nil {
		t.Fatal(err)
	}
}

func TestParseID(t *testing.T) {
	for id, ok := range map[string]bool{
		strings.Repeat("0a", 20): true,
		strings.Repeat("0A", 20): false,
		strings.Repeat("0a", 19): false,
		strings.Repeat("zz", 20): false,
		"":                       false,
	} {
		if _, got := parseID(id); got != ok {
			t.Errorf("%q: %v", id, got)
		}
	}
}

// TestDataDirChange moves the role to another data_dir: it starts from
// that directory's state, as a restart would, and the old one keeps its
// torrents.
func TestDataDirChange(t *testing.T) {
	e := startEngine(t)
	src := t.TempDir()
	torrent, _ := payload(t, src, "d.bin", 64<<10)
	one, two := t.TempDir(), t.TempDir()
	r := startRole(t, e, clientJSON(one, src, ""), nil)
	st, err := r.Add(AddRequest{Torrent: torrent})
	if err != nil {
		t.Fatal(err)
	}
	old := r.Client()
	if err := r.Apply(member(t, clientJSON(two, src, ""))); err != nil {
		t.Fatal(err)
	}
	if r.Client() == old || len(r.List()) != 0 {
		t.Fatalf("client %v, torrents %v", r.Client() == old, r.List())
	}
	recs, err := loadState(one)
	if err != nil || len(recs) != 1 || recs[0].id != st.ID {
		t.Fatalf("the old state: %v %v", recs, err)
	}
	// A bad state in the new directory leaves the role as it was.
	three := t.TempDir()
	if err := os.WriteFile(filepath.Join(three, stateFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	cur := r.Client()
	if err := r.Apply(member(t, clientJSON(three, src, ""))); !errors.Is(err, ErrState) || r.Client() != cur {
		t.Fatalf("a bad new state: %v", err)
	}
}

// TestConfigChecks checks what loading a config refuses of the member.
func TestConfigChecks(t *testing.T) {
	for doc, want := range map[string]string{
		`{"bittorrent": {"client": {"listen": "nope", "data_dir": "d"}}}`:                                                      "not a host and port",
		`{"bittorrent": {"client": {"listen": ":6881", "data_dir": " "}}}`:                                                     "data_dir may not be empty",
		`{"bittorrent": {"client": {"listen": ":6881", "data_dir": "d", "encryption": "off"}}}`:                                "encryption",
		`{"bittorrent": {"client": {"listen": ":6881", "data_dir": "d", "limits": {"max_torrents": 100001}}}}`:                 "max_torrents",
		`{"listeners": [{"name": "web", "address": ":6881"}], "bittorrent": {"client": {"listen": ":6881", "data_dir": "d"}}}`: "listens on TCP",
		`{"webrtc": {"media": {"listen": ":6881"}}, "bittorrent": {"client": {"listen": "127.0.0.1:6881", "data_dir": "d"}}}`:  "listens on UDP",
	} {
		_, err := config.Load([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
	c, err := config.Load([]byte(`{"bittorrent": {"client": {"listen": ":6881", "data_dir": "/var/lib/fit/bt"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	b := c.BitTorrent.Client
	if b.EncryptionMode() != "prefer" || !b.DHTOn() || !b.PEXOn() || !b.UTPOn() || b.LSD ||
		b.Downloads() != filepath.Join("/var/lib/fit/bt", "downloads") || b.Torrents() != 1000 ||
		b.DiskQueue() != 64<<20 || b.Save().Seconds() != 5 || !c.HasApps() {
		t.Fatalf("defaults %+v", b)
	}
}

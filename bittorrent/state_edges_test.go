package bittorrent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
)

// writeState writes a state of the entries, JSON objects, into dir.
func writeState(t *testing.T, dir, extra string, entries ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{"version": 1` + extra + `, "torrents": [` + strings.Join(entries, ", ") + `]}`
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestStateEdges checks what loadState refuses and keeps with an error,
// the temporary files it removes, and the peers a record keeps.
func TestStateEdges(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, torrentDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, resumeDir), 0o700); err != nil {
		t.Fatal(err)
	}
	// What a crash left of three atomic writes.
	stale := []string{filepath.Join(dir, ".state.json.tmp-1"), filepath.Join(dir, torrentDir, ".x.torrent.tmp-2"), filepath.Join(dir, resumeDir, ".x.resume.tmp-3")}
	for _, p := range stale {
		if err := os.WriteFile(p, []byte("half"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	x := strconv.Quote(t.TempDir())
	idDir, idOther, idLong := strings.Repeat("aa", 20), strings.Repeat("bb", 20), strings.Repeat("cc", 20)
	// Its .torrent file is a directory, which does not read.
	if err := os.MkdirAll(filepath.Join(dir, torrentDir, idDir+".torrent"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Its .torrent file is another torrent's.
	other, _ := payload(t, t.TempDir(), "other.bin", 32<<10)
	if err := os.WriteFile(filepath.Join(dir, torrentDir, idOther+".torrent"), other, 0o600); err != nil {
		t.Fatal(err)
	}
	peers := make([]string, maxKeptPeers+5)
	for i := range peers {
		peers[i] = "192.0.2.1:" + strconv.Itoa(1000+i)
	}
	ps, _ := json.Marshal(peers)
	writeState(t, dir, `, "alt_speed": "on"`,
		`{"id": "`+idDir+`", "dir": `+x+`}`,
		`{"id": "`+idOther+`", "dir": `+x+`}`,
		`{"id": "`+idLong+`", "dir": `+x+`, "magnet": "magnet:?xt=urn:btih:`+idLong+`&dn=`+strings.Repeat("n", MaxMagnetBytes)+`", "peers": `+string(ps)+`}`)
	recs, alt, err := loadState(dir)
	if err != nil || alt != AltSpeedOn || len(recs) != 3 {
		t.Fatalf("%v %q %v", recs, alt, err)
	}
	if recs[0].err == nil || !strings.Contains(recs[0].err.Error(), "its .torrent file") {
		t.Fatalf("a .torrent file that is a directory: %v", recs[0].err)
	}
	if recs[1].err == nil || !strings.Contains(recs[1].err.Error(), "is of torrent") {
		t.Fatalf("another torrent's .torrent file: %v", recs[1].err)
	}
	if recs[2].err == nil || !strings.Contains(recs[2].err.Error(), "magnet link is longer") || len(recs[2].peers) != maxKeptPeers {
		t.Fatalf("a magnet link too long: %v, %d peers", recs[2].err, len(recs[2].peers))
	}
	for _, p := range stale {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is left: %v", p, err)
		}
	}

	// The removal of a .torrent file that is a directory, not empty.
	if err := os.WriteFile(filepath.Join(dir, torrentDir, idDir+".torrent", "inside"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeFiles(dir, idDir); err == nil {
		t.Fatal("a directory in the way of a removal is not reported")
	}
	if err := removeFiles(dir, idOther); err != nil {
		t.Fatal(err)
	}

	d := t.TempDir()
	writeState(t, d, `, "alt_speed": "sometimes"`)
	if _, _, err := loadState(d); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "alt_speed") {
		t.Fatalf("a bad alt_speed: %v", err)
	}
	// More torrents than any member may hold.
	d = t.TempDir()
	many := make([]string, config.MaxBTTorrents+1)
	for i := range many {
		many[i] = `{}`
	}
	writeState(t, d, "", many...)
	if _, _, err := loadState(d); !errors.Is(err, ErrState) || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("too many: %v", err)
	}
}

// TestRestoreLogged checks the log of a start that cannot add a torrent
// of its state, and that drops the temporary torrents left from before.
func TestRestoreLogged(t *testing.T) {
	e := startEngine(t)
	data := t.TempDir()
	x := strconv.Quote(t.TempDir())
	bad, temp := strings.Repeat("dd", 20), strings.Repeat("ee", 20)
	writeState(t, data, "",
		`{"id": "`+bad+`", "dir": `+x+`, "magnet": "not a magnet link"}`,
		`{"id": "`+temp+`", "dir": `+x+`, "magnet": "magnet:?xt=urn:btih:`+temp+`", "temporary": true, "paused": true}`)
	// The temporary torrent's resume data is a directory, not empty: it
	// is not removed with it.
	if err := os.MkdirAll(filepath.Join(data, resumeDir, temp+".resume", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	r, logged := loggedRole(t, func(setup func(*Role)) *Role {
		return startRoleWith(t, e, member(t, clientJSON(data, "", "")), setup)
	})
	got := r.List()
	if len(got) != 1 || got[0].ID != bad || got[0].State != "error" {
		t.Fatalf("restored %+v", got)
	}
	for _, want := range []string{"torrent " + bad + " () not added", "temporary torrent " + temp + ": ", "removed 1 temporary torrents left from before"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("the log has no %q:\n%s", want, logged.String())
		}
	}
}

// TestScheduleHoldsEdges checks scheduleHolds read in the local zone when
// none is given, and a schedule whose window does not read skipped.
func TestScheduleHoldsEdges(t *testing.T) {
	a := &config.BitTorrentAltSpeed{Schedules: []config.BitTorrentSchedule{{From: "nonsense", To: "25:99"}, {From: "00:00", To: "00:00"}}}
	now := time.Date(2026, 3, 4, 12, 0, 0, 0, time.Local)
	if !scheduleHolds(a, nil, now) {
		t.Fatal("an all-day schedule does not hold, read in the local zone")
	}
	a.Schedules = a.Schedules[:1]
	if scheduleHolds(a, nil, now) {
		t.Fatal("a schedule that does not read holds")
	}
}

// TestRescanMissingLibrary checks that a scan of a media library that is
// not running is refused and logged.
func TestRescanMissingLibrary(t *testing.T) {
	e := startEngine(t)
	r, logged := loggedRole(t, func(setup func(*Role)) *Role {
		return startRoleWith(t, e, member(t, clientJSON(t.TempDir(), "", "")), setup)
	})
	if r.rescanLibrary("nowhere") {
		t.Fatal("a scan of no library")
	}
	if !strings.Contains(logged.String(), "media library nowhere is not running") {
		t.Fatalf("not logged:\n%s", logged.String())
	}
}

// TestMetricsBeforeStart checks that a role not started writes no metric.
func TestMetricsBeforeStart(t *testing.T) {
	e := startEngine(t)
	r := New(&app.Host{ID: ID, Engine: e, Upstream: e})
	defer r.Stop(context.Background())
	if s := scrape(r); s != "" {
		t.Fatalf("metrics before a start:\n%s", s)
	}
}

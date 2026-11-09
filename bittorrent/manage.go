package bittorrent

import (
	"fmt"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/medialib"
)

// The role's management of its torrents, run by its manager goroutine
// every tick and by the API's calls that change the list: the queue
// (max_active_downloads, max_active_seeds), the disk checks
// (min_free_bytes), the alternative rates (alt_speed), each torrent's
// limits (its own over its category's), and the media library scans when a
// torrent completes. It holds r.mu, as the API does, and never runs on a
// fib worker.

// TorrentLimits are a torrent's own limits, beside the client's, as the API
// and the state file take them: fib's TorrentLimits, zero taking the
// client's limit and a negative count, ratio or time no limit.
type TorrentLimits = config.BitTorrentTorrentLimits

// hold is why the role keeps a torrent paused.
type hold uint8

const (
	holdNone hold = iota
	// holdStart: added or restored, waiting for the role's first look.
	holdStart
	// holdQueue: past max_active_downloads or max_active_seeds.
	holdQueue
	// holdDisk: short of disk space.
	holdDisk
)

func (h hold) String() string {
	switch h {
	case holdStart:
		return "starting"
	case holdQueue:
		return "queued"
	case holdDisk:
		return "disk"
	}
	return ""
}

// The modes of the alternative rates, as the API sets them.
const (
	// AltSpeedAuto follows alt_speed's schedules.
	AltSpeedAuto = "auto"
	// AltSpeedOn holds the alternative rates whatever the schedules say.
	AltSpeedOn = "on"
	// AltSpeedOff leaves them off whatever the schedules say.
	AltSpeedOff = "off"
)

// defaultTick is how often the manager looks at the torrents.
const defaultTick = time.Second

// manageLoop runs manage every tick, and the watch_dir scan every
// watch_interval, until stop is closed.
func (r *Role) manageLoop(stop, done chan struct{}) {
	defer close(done)
	t := time.NewTicker(r.tick)
	defer t.Stop()
	var lastWatch time.Time
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		r.mu.Lock()
		if r.stopped || r.client == nil {
			r.mu.Unlock()
			continue
		}
		r.manageLocked()
		var w *watchJob
		if r.cfg.WatchDir != "" && time.Since(lastWatch) >= r.cfg.Watch() {
			w = r.watchJobLocked()
			lastWatch = time.Now()
		}
		r.mu.Unlock()
		if w != nil {
			r.runWatch(w)
		}
	}
}

// manageLocked looks at every torrent once. r.mu is held.
func (r *Role) manageLocked() {
	if r.client == nil {
		return
	}
	r.applyLimitsLocked()
	r.applyTorrentLimitsLocked()
	r.completionsLocked()
	r.scheduleLocked()
	r.metricsLocked()
}

// altActiveLocked reports whether the alternative rates hold now. r.mu is
// held.
func (r *Role) altActiveLocked() bool {
	a := r.cfg.AltSpeed
	switch {
	case a == nil:
		return false
	case r.altMode == AltSpeedOn:
		return true
	case r.altMode == AltSpeedOff:
		return false
	}
	return scheduleHolds(a, r.altLoc, r.now())
}

// scheduleHolds reports whether one of a's schedules holds at t, read in
// loc.
func scheduleHolds(a *config.BitTorrentAltSpeed, loc *time.Location, t time.Time) bool {
	if loc == nil {
		loc = time.Local
	}
	t = t.In(loc)
	wd := t.Weekday()
	m := t.Hour()*60 + t.Minute()
	for i := range a.Schedules {
		days, from, to, err := a.Schedules[i].Window()
		if err != nil {
			continue
		}
		if from < to {
			if days[wd] && from <= m && m < to {
				return true
			}
			continue
		}
		// Past midnight: from the day it begins to the next.
		if days[wd] && m >= from || days[(wd+6)%7] && m < to {
			return true
		}
	}
	return false
}

// effectiveLimits returns the client's limits now: limits', with
// alt_speed's rates in their place while those hold. r.mu is held.
func (r *Role) effectiveLimitsLocked() fibbt.Limits {
	l := limitsOf(r.cfg)
	if r.altActiveLocked() {
		l.UploadRate, l.DownloadRate = r.cfg.AltSpeed.UploadRate, r.cfg.AltSpeed.DownloadRate
	}
	return l
}

// applyLimitsLocked gives the client the limits it should have now, when
// they changed. r.mu is held.
func (r *Role) applyLimitsLocked() {
	l := r.effectiveLimitsLocked()
	on := r.altActiveLocked()
	if on != r.altOn {
		r.altOn = on
		state := "off"
		if on {
			state = "on"
		}
		if r.log != nil {
			if on {
				r.log.Infof("the alternative rates hold: upload %d B/s, download %d B/s (0 is unlimited)", l.UploadRate, l.DownloadRate)
			} else {
				r.log.Infof("the alternative rates no longer hold")
			}
		}
		r.event(Event{Type: EventAltSpeed, State: state})
	}
	if l != r.limits {
		r.client.SetLimits(l)
		r.limits = l
	}
}

// torrentLimitsLocked returns the fib limits of rec: its own over its
// category's. r.mu is held.
func (r *Role) torrentLimitsLocked(rec *record) fibbt.TorrentLimits {
	var l TorrentLimits
	if c := r.cfg.Category(rec.category); c != nil && c.Limits != nil {
		l = *c.Limits
	}
	l = l.Over(rec.limits)
	return fibbt.TorrentLimits{UploadRate: l.UploadRate, DownloadRate: l.DownloadRate, MaxConns: l.MaxConns,
		UploadSlots: l.UploadSlots, SeedRatio: l.SeedRatio, SeedTime: l.SeedTime.Std()}
}

// applyTorrentLimitsLocked gives each torrent the limits it should have,
// when they changed. r.mu is held.
func (r *Role) applyTorrentLimitsLocked() {
	for _, rec := range r.recs {
		if rec.t == nil {
			continue
		}
		if l := r.torrentLimitsLocked(rec); l != rec.applied {
			rec.t.SetLimits(l)
			rec.applied = l
		}
	}
}

// complete reports whether t has every piece it wants.
func complete(t *fibbt.Torrent) bool {
	if t.Info() == nil {
		return false
	}
	select {
	case <-t.Done():
		return true
	default:
		return false
	}
}

// completionsLocked notes the torrents that completed since the last look,
// and has their category's media library scanned again. A torrent once
// complete stays so, through the check of a restart that has yet to find
// its pieces, until a file priority changes (SetFilePriority). r.mu is
// held.
func (r *Role) completionsLocked() {
	scan := map[string]bool{}
	for _, rec := range r.recs {
		if rec.t == nil || rec.err != nil || rec.completed || !complete(rec.t) {
			continue
		}
		rec.completed, r.listDirty = true, true
		if r.log != nil {
			r.log.Infof("torrent %s (%s) completed", rec.id, rec.name)
		}
		r.event(torrentEvent(EventCompleted, rec))
		if c := r.cfg.Category(rec.category); c != nil && c.MediaLibrary != "" {
			scan[c.MediaLibrary] = true
		}
	}
	for lib := range scan {
		if !r.rescan(lib) && r.log != nil {
			r.log.Debugf("media library %s: a scan is already waiting, or it is not running", lib)
		}
	}
}

// rescanLibrary asks the media library name of the role's engine for a
// scan.
func (r *Role) rescanLibrary(name string) bool {
	l := medialib.Lookup(r.key, name)
	if l == nil {
		if r.log != nil {
			r.log.Warnf("media library %s is not running: it is not scanned", name)
		}
		return false
	}
	return l.Rescan()
}

// noteSeedLimitLocked takes a torrent that fib paused on its own, by its
// seed limit, for paused, so that it stays paused across restarts and the
// queue does not start it again. r.mu is held.
func (r *Role) noteSeedLimitLocked(rec *record) {
	if rec.t == nil || rec.paused || rec.held != holdNone {
		return
	}
	if st := rec.t.State(); st == fibbt.StatePaused || st == fibbt.StateStopped {
		rec.paused, rec.resumeDirty, r.listDirty = true, true, true
	}
}

// scheduleLocked starts and pauses the torrents by the queue and the disk:
// in the order of the list, the first max_active_downloads downloads that
// have the disk space they need, and the first max_active_seeds seeds, run;
// the rest are held. A torrent the API paused, or its seed limit did, is
// left alone. r.mu is held.
func (r *Role) scheduleLocked() {
	if r.client == nil {
		return
	}
	maxDL, maxSeed := r.cfg.ActiveDownloads(), r.cfg.ActiveSeeds()
	minFree := r.cfg.FreeBytes()
	type disk struct {
		free     int64
		ok       bool
		reserved int64
	}
	disks := map[string]*disk{}
	diskOf := func(dir string) *disk {
		d := disks[dir]
		if d == nil {
			free, ok := r.diskFree(dir)
			d = &disk{free: int64(min(free, 1<<62)), ok: ok}
			disks[dir] = d
		}
		return d
	}
	nDL, nSeed := 0, 0
	for _, rec := range r.recs {
		t := rec.t
		if t == nil || rec.err != nil {
			continue
		}
		r.noteSeedLimitLocked(rec)
		if rec.paused {
			rec.held, rec.holdMsg = holdNone, ""
			continue
		}
		st := t.Stats()
		if st.State == fibbt.StateError || st.State == fibbt.StateRemoved {
			continue
		}
		// A torrent bt_stream added on the fly has a viewer waiting: it
		// is not queued and takes no slot (max_temporary and
		// max_temporary_bytes bound such torrents), but the disk check
		// holds it as any other.
		queued := !rec.temporary
		if t.Info() != nil && st.Completed >= st.Wanted {
			if queued && maxSeed > 0 && nSeed >= maxSeed {
				r.holdLocked(rec, holdQueue, "")
				continue
			}
			if queued {
				nSeed++
			}
			r.startLocked(rec)
			continue
		}
		if queued && maxDL > 0 && nDL >= maxDL {
			r.holdLocked(rec, holdQueue, "")
			continue
		}
		if d := diskOf(rec.dir); d.ok {
			var remaining int64
			if t.Info() != nil {
				remaining = max(st.Wanted-st.Completed, 0)
			}
			// A running download goes on while min_free_bytes is left; one
			// held starts once what it still has to download fits too.
			need := minFree
			if rec.held != holdNone {
				need += remaining
			}
			if free := d.free - d.reserved; free < need {
				r.holdLocked(rec, holdDisk, fmt.Sprintf("short of disk space: %d bytes free on the disk of %s, less than min_free_bytes (%d) with the %d it still has to download",
					max(free, 0), rec.dir, minFree, remaining))
				continue
			}
			if rec.held != holdNone {
				d.reserved += remaining
			}
		}
		if queued {
			nDL++
		}
		r.startLocked(rec)
	}
}

// holdLocked keeps rec paused for why. r.mu is held.
func (r *Role) holdLocked(rec *record, why hold, msg string) {
	if rec.held == holdNone {
		rec.t.Pause()
		rec.resumeDirty = true
	}
	if why == holdDisk && msg != "" && rec.holdMsg == "" && r.log != nil {
		r.log.Warnf("torrent %s (%s) %s", rec.id, rec.name, msg)
	}
	if why != holdDisk {
		msg = ""
	}
	rec.held, rec.holdMsg = why, msg
}

// startLocked runs rec, when the role held it. r.mu is held.
func (r *Role) startLocked(rec *record) {
	if rec.held == holdNone {
		return
	}
	if rec.held == holdDisk && r.log != nil {
		r.log.Infof("torrent %s (%s) has the disk space it needs again: started", rec.id, rec.name)
	}
	rec.t.Start()
	rec.held, rec.holdMsg = holdNone, ""
}

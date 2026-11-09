package bittorrent

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"

	"github.com/lesismal/fit/app/console"
)

// The role's API, for the Admin API, the CLI and the pages to build on.
// Its methods read and write the state files, and fib's client, which may
// wait on its disk pool: they must not be called on a fib worker.

// The errors of the role's API.
var (
	// ErrNotRunning is a call while the role runs no client.
	ErrNotRunning = errors.New("bittorrent: the client does not run")
	// ErrNotFound is a torrent the role does not have.
	ErrNotFound = errors.New("bittorrent: no such torrent")
	// ErrTooMany is an add past limits.max_torrents.
	ErrTooMany = errors.New("bittorrent: max_torrents torrents are held already")
	// ErrExists is a torrent added twice.
	ErrExists = fibbt.ErrTorrentExists
	// ErrBadRequest is a request the API cannot take, as a link that is not
	// a magnet link or bytes that are not a .torrent file.
	ErrBadRequest = errors.New("bittorrent: bad request")
	// ErrTooManyTemporary is an AddTemporary past its bound.
	ErrTooManyTemporary = errors.New("bittorrent: as many temporary torrents as allowed are held already")
	// ErrBroken is a torrent its state could not be added back with: it
	// may only be removed.
	ErrBroken = errors.New("bittorrent: the torrent could not be restored")
)

// AddRequest is a torrent to add: a magnet link or a .torrent file's bytes.
type AddRequest struct {
	// Magnet is a magnet link (BEP 9).
	Magnet string `json:"magnet,omitempty"`
	// Torrent is a .torrent file, at most MaxTorrentBytes.
	Torrent []byte `json:"torrent,omitempty"`
	// Paused adds the torrent without starting it.
	Paused bool `json:"paused,omitempty"`
	// Sequential downloads its pieces in order.
	Sequential bool `json:"sequential,omitempty"`
	// Priorities are its files' priorities by their index in the info
	// dictionary's file list, fib's PriorityNone (0) to PriorityHigh (7).
	Priorities []int `json:"priorities,omitempty"`
	// Peers are addresses to connect to beside those discovery finds.
	Peers []string `json:"peers,omitempty"`
	// Category is the category to add it in, which decides its
	// directory, its limits and the media library scanned when it
	// completes; none, download_dir, when empty.
	Category string `json:"category,omitempty"`
	// Limits are its own limits, over its category's.
	Limits *TorrentLimits `json:"limits,omitempty"`
}

// Status is a torrent as the API reports it.
type Status struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	InfoHash   string    `json:"info_hash,omitempty"`
	InfoHashV2 string    `json:"info_hash_v2,omitempty"`
	Magnet     string    `json:"magnet,omitempty"`
	Dir        string    `json:"dir"`
	State      string    `json:"state"`
	Error      string    `json:"error,omitempty"`
	Paused     bool      `json:"paused"`
	Sequential bool      `json:"sequential"`
	Added      time.Time `json:"added"`
	// Temporary is a torrent bt_stream added on the fly, removed with its
	// data after its last stream.
	Temporary bool `json:"temporary,omitempty"`
	// Category is its category, if any.
	Category string `json:"category,omitempty"`
	// Limits are its own limits, if it has any; Effective what it goes by,
	// its own over its category's.
	Limits    *TorrentLimits `json:"limits,omitempty"`
	Effective *TorrentLimits `json:"effective_limits,omitempty"`
	// QueuePosition is its place in the list, from 0, which the queue
	// goes by.
	QueuePosition int `json:"queue_position"`
	// Held is why the role keeps it paused, if it does: "starting",
	// "queued" (past max_active_downloads or max_active_seeds) or "disk"
	// (short of disk space, Error saying how).
	Held string `json:"held,omitempty"`
	// Complete says it has every piece it wants.
	Complete bool `json:"complete"`

	Size         int64   `json:"size"`
	Wanted       int64   `json:"wanted"`
	Completed    int64   `json:"completed"`
	Progress     float64 `json:"progress"`
	Pieces       int     `json:"pieces"`
	PiecesHave   int     `json:"pieces_have"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
	DownloadRate float64 `json:"download_rate"`
	UploadRate   float64 `json:"upload_rate"`
	Peers        int     `json:"peers"`
	Seeders      int     `json:"seeders"`

	// Files are the torrent's files, padding left out: Get's only.
	Files []FileStatus `json:"files,omitempty"`
}

// FileStatus is a file of a torrent.
type FileStatus struct {
	// Index is the file's index in the info dictionary's file list, which
	// priorities are set by.
	Index     int    `json:"index"`
	Path      string `json:"path"`
	Length    int64  `json:"length"`
	Completed int64  `json:"completed"`
	Priority  int    `json:"priority"`
}

// status reports rec. r.mu is held.
func (r *Role) status(rec *record, files bool, pos int) Status {
	s := Status{ID: rec.id, Name: rec.name, Magnet: rec.magnet, Dir: rec.dir, Paused: rec.paused,
		Sequential: rec.sequential, Added: rec.added, Category: rec.category, Held: rec.held.String(),
		Complete: rec.completed, QueuePosition: pos, Temporary: rec.temporary}
	if !rec.limits.IsZero() {
		l := rec.limits
		s.Limits = &l
	}
	if r.cfg != nil {
		var l TorrentLimits
		if c := r.cfg.Category(rec.category); c != nil && c.Limits != nil {
			l = *c.Limits
		}
		if l = l.Over(rec.limits); !l.IsZero() {
			s.Effective = &l
		}
	}
	if rec.mi != nil {
		if h := rec.mi.Info.InfoHashV1(); rec.mi.Info.HasV1() {
			s.InfoHash = h.String()
		}
		if h := rec.mi.Info.InfoHashV2(); rec.mi.Info.HasV2() {
			s.InfoHashV2 = h.String()
		}
		if s.Magnet == "" {
			s.Magnet = rec.mi.Magnet().String()
		}
	}
	if rec.err != nil {
		s.State, s.Error = "error", rec.err.Error()
		return s
	}
	t := rec.t
	if t == nil {
		s.State = "stopped"
		return s
	}
	st := t.Stats()
	s.State = strings.ToLower(st.State.String())
	if rec.held == holdQueue {
		s.State = "queued"
	}
	if err := t.Err(); err != nil {
		s.Error = err.Error()
	} else if rec.held == holdDisk {
		s.Error = rec.holdMsg
	} else {
		s.Error = r.lastStorageErr(rec.id)
	}
	if s.InfoHash == "" && !t.InfoHash().IsZero() {
		s.InfoHash = t.InfoHash().String()
	}
	if s.InfoHashV2 == "" && !t.InfoHashV2().IsZero() {
		s.InfoHashV2 = t.InfoHashV2().String()
	}
	if s.Name == "" {
		s.Name = t.Name()
	}
	s.Size, s.Wanted, s.Completed, s.Progress = st.Size, st.Wanted, st.Completed, st.Progress
	s.Pieces, s.PiecesHave = st.Pieces, st.PiecesHave
	s.Downloaded, s.Uploaded, s.DownloadRate, s.UploadRate = st.Downloaded, st.Uploaded, st.DownloadRate, st.UploadRate
	s.Peers, s.Seeders = st.Peers, st.Seeders
	if files {
		for _, f := range t.Files() {
			s.Files = append(s.Files, FileStatus{Index: f.Index(), Path: f.Path(), Length: f.Length(),
				Completed: f.BytesCompleted(), Priority: f.Priority()})
		}
	}
	return s
}

// Add adds a torrent from a magnet link or a .torrent file, downloading
// into its category's directory, or download_dir, and saves the list at
// once. It starts at once unless req says paused, the queue is full or
// the disk short.
func (r *Role) Add(req AddRequest) (Status, error) { return r.add(req, 0) }

// AddTemporary adds a torrent as Add does, marked temporary, as bt_stream
// adds one from a magnet link on the fly, unless max temporary torrents
// are held already (ErrTooManyTemporary). A temporary torrent is removed
// with its data by RemoveTemporary, which bt_stream calls once its last
// stream has ended and lingered, and when a state is read, at start.
func (r *Role) AddTemporary(req AddRequest, limit int) (Status, error) {
	return r.add(req, max(limit, 1))
}

// add is Add, or AddTemporary when maxTemp is not 0.
func (r *Role) add(req AddRequest, maxTemp int) (Status, error) {
	if (req.Magnet == "") == (len(req.Torrent) == 0) {
		return Status{}, fmt.Errorf("%w: give a magnet link or a .torrent file", ErrBadRequest)
	}
	rec := &record{magnet: req.Magnet, priorities: slices.Clone(req.Priorities), sequential: req.Sequential,
		paused: req.Paused, added: time.Now().UTC().Truncate(time.Second), category: req.Category}
	if req.Limits != nil {
		rec.limits = req.Limits.Over(TorrentLimits{})
	}
	for _, p := range req.Priorities {
		if p < fibbt.PriorityNone || p > fibbt.PriorityHigh {
			return Status{}, fmt.Errorf("%w: a priority is 0 to 7, not %d", ErrBadRequest, p)
		}
	}
	var torrentBytes []byte
	if req.Magnet != "" {
		if len(req.Magnet) > MaxMagnetBytes {
			return Status{}, fmt.Errorf("%w: a magnet link longer than %d bytes", ErrBadRequest, MaxMagnetBytes)
		}
		m, err := metainfo.ParseMagnet(req.Magnet)
		if err != nil {
			return Status{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		rec.id, rec.name = idOf(m.InfoHash, m.InfoHashV2), m.Name
	} else {
		if len(req.Torrent) > MaxTorrentBytes {
			return Status{}, fmt.Errorf("%w: a .torrent file larger than %d bytes", ErrBadRequest, MaxTorrentBytes)
		}
		mi, err := metainfo.Parse(req.Torrent)
		if err != nil {
			return Status{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
		}
		if len(req.Priorities) > len(mi.Info.FileList()) {
			return Status{}, fmt.Errorf("%w: %d priorities for %d files", ErrBadRequest, len(req.Priorities), len(mi.Info.FileList()))
		}
		rec.id, rec.name, rec.mi = idOf(mi.Info.InfoHashV1(), mi.Info.InfoHashV2()), mi.Info.Name, mi
		torrentBytes = req.Torrent
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return Status{}, ErrNotRunning
	}
	if old := r.byID[rec.id]; old != nil {
		// A torrent bt_stream added on the fly, added by hand now: it is
		// kept as any other, as it is.
		if maxTemp == 0 && old.temporary {
			old.temporary = false
			if err := r.saveListLocked(); err != nil {
				return Status{}, err
			}
			if r.log != nil {
				r.log.Infof("torrent %s (%s), added on the fly, is kept now", old.id, old.name)
			}
			return r.status(old, false, slices.Index(r.recs, old)), nil
		}
		return Status{}, ErrExists
	}
	if len(r.recs) >= r.cfg.Torrents() {
		return Status{}, ErrTooMany
	}
	if maxTemp > 0 {
		if r.temporaries() >= maxTemp {
			return Status{}, ErrTooManyTemporary
		}
		rec.temporary = true
	}
	base := r.cfg.Downloads()
	if rec.category != "" {
		c := r.cfg.Category(rec.category)
		if c == nil {
			return Status{}, fmt.Errorf("%w: no category is named %q", ErrBadRequest, rec.category)
		}
		base = r.cfg.CategoryDir(c)
	}
	dir, err := filepath.Abs(base)
	if err != nil {
		return Status{}, err
	}
	rec.dir = dir
	if err := mkdir(dir); err != nil {
		return Status{}, err
	}
	// The .torrent file first: a list naming a torrent always has its
	// source.
	if torrentBytes != nil {
		if err := writeAtomic(filepath.Join(r.dataDir, torrentDir), rec.id+".torrent", torrentBytes); err != nil {
			return Status{}, fmt.Errorf("saving its .torrent file: %w", err)
		}
		rec.torrentSaved = true
	}
	if err := r.restore(rec, req.Peers); err != nil {
		if torrentBytes != nil {
			_ = removeFiles(r.dataDir, rec.id)
		}
		if errors.Is(err, fibbt.ErrTorrentExists) {
			return Status{}, ErrExists
		}
		return Status{}, fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	r.recs = append(r.recs, rec)
	r.byID[rec.id] = rec
	r.listDirty = true
	r.scheduleLocked()
	if err := saveList(r.dataDir, r.recs, r.altMode); err != nil {
		// Saved again by the saver.
		if r.log != nil {
			r.log.Errorf("saving the state in %s: %v", r.dataDir, err)
		}
	} else {
		r.listDirty = false
	}
	e := torrentEvent(EventAdded, rec)
	e.State, _ = r.stateLocked(rec)
	rec.lastState = e.State
	r.event(e)
	return r.status(rec, false, len(r.recs)-1), nil
}

// List reports every torrent, in the order of the list, which the queue
// goes by: the order they were added in, unless SetQueuePosition moved one.
func (r *Role) List() []Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Status, 0, len(r.recs))
	for i, rec := range r.recs {
		out = append(out, r.status(rec, false, i))
	}
	return out
}

// Get reports the torrent id, with its files.
func (r *Role) Get(id string) (Status, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byID[id]
	if rec == nil {
		return Status{}, ErrNotFound
	}
	return r.status(rec, true, slices.Index(r.recs, rec)), nil
}

// Torrent returns fib's torrent id, for what reads its data, as a stream:
// a config that makes a new client replaces it.
func (r *Role) Torrent(id string) (*fibbt.Torrent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.running(id)
	if err != nil {
		return nil, err
	}
	return rec.t, nil
}

// running returns the record id while its torrent runs in the client.
// r.mu is held.
func (r *Role) running(id string) (*record, error) {
	rec := r.byID[id]
	switch {
	case rec == nil:
		return nil, ErrNotFound
	case rec.err != nil:
		return nil, fmt.Errorf("%w: %v", ErrBroken, rec.err)
	case rec.t == nil:
		return nil, ErrNotRunning
	}
	return rec, nil
}

// change runs fn on the running torrent id and saves the list at once.
func (r *Role) change(id string, fn func(rec *record) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.running(id)
	if err != nil {
		return err
	}
	if err := fn(rec); err != nil {
		return err
	}
	r.noteStatesLocked()
	return r.saveListLocked()
}

// saveListLocked saves the list at once. r.mu is held.
func (r *Role) saveListLocked() error {
	r.listDirty = true
	if err := saveList(r.dataDir, r.recs, r.altMode); err != nil {
		return fmt.Errorf("saving the state: %w", err)
	}
	r.listDirty = false
	return nil
}

// Pause pauses the torrent id, closing its connections; it stays paused
// across restarts until Resume.
func (r *Role) Pause(id string) error {
	return r.change(id, func(rec *record) error {
		rec.t.Pause()
		rec.paused, rec.resumeDirty = true, true
		rec.held, rec.holdMsg = holdNone, ""
		r.scheduleLocked()
		return nil
	})
}

// Resume starts the torrent id again, when the queue and the disk let it:
// else it waits, held, for its turn.
func (r *Role) Resume(id string) error {
	return r.change(id, func(rec *record) error {
		if !rec.paused {
			return nil
		}
		rec.paused = false
		if rec.held == holdNone {
			rec.held = holdStart
		}
		r.scheduleLocked()
		return nil
	})
}

// SetCategory moves the torrent id to the category name, or out of any
// with "": it takes the category's limits, and its media library is
// scanned when the torrent completes. Its files stay in the directory it
// was added with.
func (r *Role) SetCategory(id, name string) error {
	return r.change(id, func(rec *record) error {
		if name != "" && r.cfg.Category(name) == nil {
			return fmt.Errorf("%w: no category is named %q", ErrBadRequest, name)
		}
		rec.category = name
		r.applyTorrentLimitsLocked()
		return nil
	})
}

// SetTorrentLimits sets the torrent id's own limits, over its category's;
// the zero TorrentLimits clears them.
func (r *Role) SetTorrentLimits(id string, l TorrentLimits) error {
	return r.change(id, func(rec *record) error {
		rec.limits = l.Over(TorrentLimits{})
		r.applyTorrentLimitsLocked()
		return nil
	})
}

// SetQueuePosition moves the torrent id to position pos of the list, from
// 0, which the queue goes by: past the end is the end. The torrents ahead
// of max_active_downloads and max_active_seeds run.
func (r *Role) SetQueuePosition(id string, pos int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byID[id]
	if rec == nil {
		return ErrNotFound
	}
	if pos < 0 {
		return fmt.Errorf("%w: a queue position is 0 or more, not %d", ErrBadRequest, pos)
	}
	r.recs = slices.DeleteFunc(r.recs, func(x *record) bool { return x == rec })
	pos = min(pos, len(r.recs))
	r.recs = slices.Insert(r.recs, pos, rec)
	r.scheduleLocked()
	r.noteStatesLocked()
	return r.saveListLocked()
}

// AltSpeedStatus is the alternative rates as the API reports them.
type AltSpeedStatus struct {
	// Mode is AltSpeedAuto, AltSpeedOn or AltSpeedOff.
	Mode string `json:"mode"`
	// Configured says the config has alt_speed: without it the
	// alternative rates never hold.
	Configured bool `json:"configured"`
	// Active says they hold now.
	Active       bool `json:"active"`
	UploadRate   int  `json:"upload_rate"`
	DownloadRate int  `json:"download_rate"`
}

// AltSpeed reports the alternative rates.
func (r *Role) AltSpeed() (AltSpeedStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return AltSpeedStatus{}, ErrNotRunning
	}
	s := AltSpeedStatus{Mode: AltSpeedAuto, Configured: r.cfg.AltSpeed != nil, Active: r.altActiveLocked(),
		UploadRate: r.limits.UploadRate, DownloadRate: r.limits.DownloadRate}
	if r.altMode != "" {
		s.Mode = r.altMode
	}
	return s, nil
}

// SetAltSpeed turns the alternative rates on or off whatever their
// schedules say, or back to the schedules with AltSpeedAuto; the choice is
// kept in the state across restarts.
func (r *Role) SetAltSpeed(mode string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return ErrNotRunning
	}
	switch mode {
	case AltSpeedAuto:
		mode = ""
	case AltSpeedOn, AltSpeedOff:
	default:
		return fmt.Errorf("%w: the alternative rates' mode is auto, on or off, not %q", ErrBadRequest, mode)
	}
	r.altMode = mode
	r.applyLimitsLocked()
	return r.saveListLocked()
}

// SetSequential turns the sequential download of the torrent id on or off.
func (r *Role) SetSequential(id string, on bool) error {
	return r.change(id, func(rec *record) error {
		rec.t.SetSequential(on)
		rec.sequential = on
		return nil
	})
}

// SetFilePriority sets the priority of file i, by its index in the info
// dictionary's file list, of the torrent id, once its info is known.
func (r *Role) SetFilePriority(id string, i, priority int) error {
	return r.change(id, func(rec *record) error {
		if priority < fibbt.PriorityNone || priority > fibbt.PriorityHigh {
			return fmt.Errorf("%w: a priority is 0 to 7, not %d", ErrBadRequest, priority)
		}
		info := rec.t.Info()
		if info == nil {
			return fmt.Errorf("%w: its info is not known yet", ErrBadRequest)
		}
		n := len(info.FileList())
		if i < 0 || i >= n {
			return fmt.Errorf("%w: file %d of %d", ErrBadRequest, i, n)
		}
		if len(rec.priorities) != n {
			p := make([]int, n)
			for j := range p {
				p[j] = fibbt.PriorityNormal
				if j < len(rec.priorities) {
					p[j] = rec.priorities[j]
				}
			}
			rec.priorities = p
		}
		rec.t.SetFilePriority(i, priority)
		// It may want pieces it lacks now: its next completion counts.
		rec.completed = false
		rec.priorities[i] = priority
		return nil
	})
}

// SetFilePriorities sets the priorities of the files of the torrent id,
// once its info is known, by their index in the info dictionary's file
// list, one for each file, and saves the list once, as bt_stream does to
// download only the files a temporary torrent streams.
func (r *Role) SetFilePriorities(id string, priorities []int) error {
	return r.change(id, func(rec *record) error {
		info := rec.t.Info()
		if info == nil {
			return fmt.Errorf("%w: its info is not known yet", ErrBadRequest)
		}
		if n := len(info.FileList()); len(priorities) != n {
			return fmt.Errorf("%w: %d priorities for %d files", ErrBadRequest, len(priorities), n)
		}
		for _, p := range priorities {
			if p < fibbt.PriorityNone || p > fibbt.PriorityHigh {
				return fmt.Errorf("%w: a priority is 0 to 7, not %d", ErrBadRequest, p)
			}
		}
		for i, p := range priorities {
			rec.t.SetFilePriority(i, p)
		}
		rec.priorities = slices.Clone(priorities)
		return nil
	})
}

// Remove removes the torrent id, and with deleteData its downloaded files,
// and saves the list at once. A torrent that could not be restored is
// removed from the list; its files, if any, only with deleteData once it
// runs again.
func (r *Role) Remove(id string, deleteData bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.removeLocked(id, deleteData)
}

// removeLocked is Remove. r.mu is held.
func (r *Role) removeLocked(id string, deleteData bool) error {
	rec := r.byID[id]
	if rec == nil {
		return ErrNotFound
	}
	var errs []error
	// What it counted, before fib forgets it.
	r.accountLocked(rec, nil)
	if rec.t != nil {
		if err := rec.t.Remove(deleteData); err != nil {
			errs = append(errs, fmt.Errorf("removing its data: %w", err))
		}
		rec.t = nil
	}
	r.removeRecord(rec)
	r.evMu.Lock()
	delete(r.storageErr, id)
	r.evMu.Unlock()
	// Its slot in the queue frees.
	r.scheduleLocked()
	if err := saveList(r.dataDir, r.recs, r.altMode); err != nil {
		errs = append(errs, fmt.Errorf("saving the state: %w", err))
	} else {
		r.listDirty = false
	}
	if err := removeFiles(r.dataDir, id); err != nil {
		errs = append(errs, err)
	}
	e := torrentEvent(EventRemoved, rec)
	e.DataDeleted = deleteData
	r.event(e)
	r.noteStatesLocked()
	return errors.Join(errs...)
}

// temporaries counts the temporary torrents. r.mu is held.
func (r *Role) temporaries() int {
	n := 0
	for _, rec := range r.recs {
		if rec.temporary {
			n++
		}
	}
	return n
}

// SetTemporary marks the torrent id temporary or, with on false, keeps it
// as any other, and saves the list at once.
func (r *Role) SetTemporary(id string, on bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byID[id]
	if rec == nil {
		return ErrNotFound
	}
	if rec.temporary == on {
		return nil
	}
	rec.temporary = on
	r.listDirty = true
	if err := saveList(r.dataDir, r.recs, r.altMode); err != nil {
		return fmt.Errorf("saving the state: %w", err)
	}
	r.listDirty = false
	return nil
}

// RemoveTemporary removes the torrent id with its data when it is still
// temporary, and reports whether it did.
func (r *Role) RemoveTemporary(id string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec := r.byID[id]; rec == nil || !rec.temporary {
		return false, nil
	}
	return true, r.removeLocked(id, true)
}

// TemporaryBytes returns the bytes the temporary torrents want, those
// whose info is known: what they take on the disk once downloaded.
func (r *Role) TemporaryBytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, rec := range r.recs {
		if rec.temporary && rec.t != nil && rec.t.Info() != nil {
			n += rec.t.Stats().Wanted
		}
	}
	return n
}

// Save saves what changed now, as the saver does every save_interval.
func (r *Role) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return ErrNotRunning
	}
	r.saveLocked(false)
	if r.lastErr != "" {
		return errors.New(r.lastErr)
	}
	return nil
}

// routes adds the role's read-only API to its console: the pages of I204
// build on it.
func (r *Role) routes(api *console.API) {
	api.Handle("GET", "/api/torrents", func(q *console.Request) {
		q.Go(func() { _ = q.JSON(http.StatusOK, map[string]any{"torrents": r.List()}) })
	})
	api.HandlePrefix("GET", "/api/torrents/", func(q *console.Request) {
		// /api/torrents/<id> on the role's console, and
		// /api/m/bittorrent.client/torrents/<id> as a module.
		p := q.Request.URL.Path
		id := p[strings.LastIndex(p, "/torrents/")+len("/torrents/"):]
		q.Go(func() {
			s, err := r.Get(id)
			if err != nil {
				_ = q.JSON(http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			_ = q.JSON(http.StatusOK, s)
		})
	})
}

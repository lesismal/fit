// Package bittorrent is fit's BitTorrent client role, bittorrent.client in
// fit's config: fib's BitTorrent client (github.com/lesismal/fib/bittorrent)
// on the listeners' fib engine (Config.SharedEngine), one port for TCP and
// UDP, its torrents kept in files under data_dir so that a restart resumes
// them without hashing again. See docs/decisions/037-bittorrent.md and
// docs/bittorrent.md.
//
// Nothing here blocks a fib worker. fib's client reads and writes storage
// and hashes pieces on its bounded disk pool; the state files are read and
// written by Apply and Stop, on the control plane, by the role's API, which
// must not be called on a fib worker, and by a saver goroutine of the role's,
// at most every save_interval when something changed.
//
// A reload changes in place what fib's client changes while it runs: the
// limits (Client.SetLimits), the access (the Filter's gate, then
// Client.Refilter), the save interval and the console. A change of what fib
// reads only when it makes a client (listen, encryption, dht, pex, utp, lsd,
// disk_workers, disk_queue_bytes) makes a new client: the torrents are saved
// from the old one once it is closed, and added to the new one with their
// resume data. A change of data_dir makes a new client on the new
// directory's state, as a restart would.
package bittorrent

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/bencode"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/app/roleconsole"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// ID is the role's, and its member's path in the config.
const ID = "bittorrent.client"

func init() {
	app.Register(app.Info{
		ID: ID,
		Member: func(c *config.Config) any {
			if c.BitTorrent == nil {
				return (*config.BitTorrentClient)(nil)
			}
			return c.BitTorrent.Client
		},
		New: func(h *app.Host) (app.Role, error) { return New(h), nil },
		// The access's list files, read when the role applies.
		CertFiles: func(c *config.Config) []string {
			if c.BitTorrent == nil || c.BitTorrent.Client == nil {
				return nil
			}
			return c.BitTorrent.Client.Access.Files()
		},
	})
}

// The roles, by the engine the routes' modules are given (app.Host.Upstream,
// module.Context.Engine), for the modules and the Admin API to find.
var (
	rolesMu sync.Mutex
	roles   = map[*fib.Engine]*Role{}
)

// Lookup returns the BitTorrent client role of engine, or nil.
func Lookup(engine *fib.Engine) *Role {
	rolesMu.Lock()
	defer rolesMu.Unlock()
	return roles[engine]
}

// Role is the bittorrent.client role.
type Role struct {
	host   *app.Host
	engine *fib.Engine
	key    *fib.Engine
	log    *logs.Logger

	// gate is the policy the client's Filter asks, set by every Apply.
	gate    access.Gate
	console *roleconsole.Console

	// wrap, for tests, wraps the storage of every torrent.
	wrap func(fibbt.StorageOpener) fibbt.StorageOpener

	// mu orders Apply, Stop, the API and the saver, and guards what
	// follows.
	mu        sync.Mutex
	stopped   bool
	cfg       *config.BitTorrentClient
	clientKey string
	limits    fibbt.Limits
	client    *fibbt.Client
	dataDir   string
	recs      []*record
	byID      map[string]*record
	listDirty bool
	interval  time.Duration
	lastErr   string

	saverStop chan struct{}
	saverDone chan struct{}

	// evMu guards storageErr, which the client's events set: never held
	// while calling the client, whose event goroutine takes it.
	evMu       sync.Mutex
	storageErr map[string]string
}

// New returns the role, which its first Apply starts.
func New(h *app.Host) *Role {
	r := &Role{host: h, engine: h.Engine, key: h.Upstream, log: h.Log, byID: map[string]*record{},
		storageErr: map[string]string{}}
	if r.key == nil {
		r.key = h.Engine
	}
	r.console = roleconsole.New(h, ID, r.routes)
	return r
}

// Module returns the role's module of the workbench.
func (r *Role) Module() *console.Module { return r.console.Module() }

// clientKey is what fib reads only when it makes a client: a change of it
// makes a new one.
func clientKey(cfg *config.BitTorrentClient) string {
	return fmt.Sprint(cfg.Listen, "|", cfg.EncryptionMode(), "|", cfg.DHTOn(), cfg.PEXOn(), cfg.UTPOn(), cfg.LSD, "|",
		cfg.DiskWorkers, "|", cfg.DiskQueue(), "|", filepath.Clean(cfg.DataDir))
}

// limitsOf returns fib's limits of cfg.
func limitsOf(cfg *config.BitTorrentClient) fibbt.Limits {
	l := cfg.Limits
	if l == nil {
		return fibbt.Limits{}
	}
	return fibbt.Limits{UploadRate: l.UploadRate, DownloadRate: l.DownloadRate, MaxConns: l.MaxConns,
		MaxConnsPerTorrent: l.MaxConnsPerTorrent, MaxHalfOpen: l.MaxHalfOpen, UploadSlots: l.UploadSlots,
		SeedRatio: l.SeedRatio, SeedTime: l.SeedTime.Std()}
}

// Apply moves the role to member, a *config.BitTorrentClient.
func (r *Role) Apply(member any) error {
	cfg, ok := member.(*config.BitTorrentClient)
	if !ok || cfg == nil {
		return errors.New("bittorrent.client: no member")
	}
	policy, err := r.host.Access(cfg.Access)
	if err != nil {
		return fmt.Errorf("access: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return app.ErrStopped
	}
	// Before a client starts, which goes by it from its first peer.
	r.gate.Set(policy)
	key := clientKey(cfg)
	switch {
	case r.client == nil:
		if err := r.start(cfg); err != nil {
			return err
		}
	case key != r.clientKey:
		if err := r.restart(cfg); err != nil {
			return fmt.Errorf("a new client: %w", err)
		}
	default:
		if l := limitsOf(cfg); l != r.limits {
			r.client.SetLimits(l)
			r.limits = l
		}
	}
	r.clientKey = key
	// The peers connected that the policy now refuses.
	r.client.Refilter()
	r.cfg = cfg
	r.interval = cfg.Save()
	if r.saverStop == nil {
		r.saverStop, r.saverDone = make(chan struct{}), make(chan struct{})
		go r.saveLoop(r.saverStop, r.saverDone)
	}
	rolesMu.Lock()
	roles[r.key] = r
	rolesMu.Unlock()
	if err := r.console.Apply(cfg.Console); err != nil {
		return fmt.Errorf("console: %w", err)
	}
	return nil
}

// start makes the first client and restores the state of data_dir into
// it. r.mu is held.
func (r *Role) start(cfg *config.BitTorrentClient) error {
	recs, err := loadState(cfg.DataDir)
	if err != nil {
		return fmt.Errorf("%w; fix it or move it away, and apply the config again", err)
	}
	c, err := r.newClient(cfg)
	if err != nil {
		return err
	}
	r.client, r.limits, r.dataDir = c, limitsOf(cfg), cfg.DataDir
	r.setRecords(recs)
	r.restoreAll()
	r.dropTemporaries()
	if r.log != nil {
		r.log.Infof("listening on %s, %d torrents", c.Addr(), len(r.recs))
	}
	return nil
}

// restart replaces the client with one made for cfg, the torrents moved to
// it with their resume data, or, for a new data_dir, those of its state.
// When the new client cannot be made, the old one is made again. r.mu is
// held.
func (r *Role) restart(cfg *config.BitTorrentClient) error {
	newDir := filepath.Clean(cfg.DataDir) != filepath.Clean(r.dataDir)
	var recs []*record
	if newDir {
		var err error
		if recs, err = loadState(cfg.DataDir); err != nil {
			return err
		}
	}
	old := r.cfg
	r.closeClient()
	c, err := r.newClient(cfg)
	if err != nil {
		// The port may be taken: back to the client before, on the same
		// torrents.
		if back, berr := r.newClient(old); berr == nil {
			r.client = back
			r.client.SetLimits(r.limits)
			r.restoreAll()
		} else if r.log != nil {
			r.log.Errorf("the client before could not be made again either: %v", berr)
		}
		return err
	}
	r.client, r.limits = c, limitsOf(cfg)
	if newDir {
		r.dataDir = cfg.DataDir
		r.setRecords(recs)
	}
	r.restoreAll()
	if newDir {
		r.dropTemporaries()
	}
	if r.log != nil {
		r.log.Infof("a new client listening on %s, %d torrents", c.Addr(), len(r.recs))
	}
	return nil
}

// newClient makes a client of cfg on the role's engine.
func (r *Role) newClient(cfg *config.BitTorrentClient) (*fibbt.Client, error) {
	fc := fibbt.Config{
		DataDir:        cfg.Downloads(),
		ListenAddr:     cfg.Listen,
		DisableUTP:     !cfg.UTPOn(),
		DisableDHT:     !cfg.DHTOn(),
		DisablePEX:     !cfg.PEXOn(),
		DisableLSD:     !cfg.LSD,
		DiskWorkers:    cfg.DiskWorkers,
		DiskQueueBytes: cfg.DiskQueue(),
		SharedEngine:   r.engine,
		Filter:         r.gate.Allows,
		OnEvent:        r.onEvent,
	}
	switch cfg.EncryptionMode() {
	case config.BTEncryptionAllow:
		fc.Encryption = fibbt.EncryptionAllow
	case config.BTEncryptionRequire:
		fc.Encryption = fibbt.EncryptionRequire
	default:
		fc.Encryption = fibbt.EncryptionPrefer
	}
	if l := cfg.Limits; l != nil {
		fc.UploadRate, fc.DownloadRate = l.UploadRate, l.DownloadRate
		fc.MaxConns, fc.MaxConnsPerTorrent, fc.MaxHalfOpen = l.MaxConns, l.MaxConnsPerTorrent, l.MaxHalfOpen
		fc.UploadSlots, fc.SeedRatio, fc.SeedTime = l.UploadSlots, l.SeedRatio, l.SeedTime.Std()
	}
	return fibbt.NewClient(fc)
}

// onEvent hears the client's events, on its event goroutine: the errors
// of a torrent's storage, which do not stop it, are kept for its status
// until a piece passes its check again. It must not take r.mu, which is
// held while a client is closed.
func (r *Role) onEvent(e fibbt.Event) {
	if e.Torrent == nil {
		return
	}
	switch e.Type {
	case fibbt.EventError:
		if errors.Is(e.Err, fibbt.ErrResumeData) {
			if r.log != nil {
				r.log.Warnf("torrent %s: its resume data is not used, its data is checked: %v", e.Torrent.Name(), e.Err)
			}
			return
		}
		id := idOf(e.Torrent.InfoHash(), e.Torrent.InfoHashV2())
		msg := e.Err.Error()
		r.evMu.Lock()
		changed := r.storageErr[id] != msg
		r.storageErr[id] = msg
		r.evMu.Unlock()
		if changed && r.log != nil {
			r.log.Errorf("torrent %s: %s", e.Torrent.Name(), msg)
		}
	case fibbt.EventPieceVerified, fibbt.EventCompleted, fibbt.EventStateChanged:
		id := idOf(e.Torrent.InfoHash(), e.Torrent.InfoHashV2())
		r.evMu.Lock()
		delete(r.storageErr, id)
		r.evMu.Unlock()
	}
}

// lastStorageErr returns the storage error kept for torrent id, or "".
func (r *Role) lastStorageErr(id string) string {
	r.evMu.Lock()
	defer r.evMu.Unlock()
	return r.storageErr[id]
}

// opener returns the storage of a torrent downloading into dir.
func (r *Role) opener(dir string) fibbt.StorageOpener {
	o := fibbt.NewFileStorage(dir, fibbt.FileStorageOptions{})
	if r.wrap != nil {
		o = r.wrap(o)
	}
	return o
}

// setRecords replaces the records. r.mu is held.
func (r *Role) setRecords(recs []*record) {
	r.recs = recs
	r.byID = make(map[string]*record, len(recs))
	for _, rec := range recs {
		r.byID[rec.id] = rec
	}
}

// restoreAll adds every record that can be to the client. r.mu is held.
func (r *Role) restoreAll() {
	for _, rec := range r.recs {
		if rec.mi == nil && rec.magnet == "" {
			continue // its error says why
		}
		if err := r.restore(rec, nil); err != nil {
			rec.err = err
			if r.log != nil {
				r.log.Warnf("torrent %s (%s) not added: %v", rec.id, rec.name, err)
			}
		}
	}
}

// restore adds rec to the client, with its resume data. r.mu is held.
func (r *Role) restore(rec *record, peers []string) error {
	opts := &fibbt.AddOptions{Paused: rec.paused, Sequential: rec.sequential, FilePriorities: rec.priorities,
		Resume: rec.resume, Storage: r.opener(rec.dir), Peers: peers}
	var t *fibbt.Torrent
	var err error
	if rec.mi != nil {
		t, err = r.client.AddTorrent(rec.mi, opts)
	} else {
		t, err = r.client.AddMagnet(rec.magnet, opts)
	}
	if err != nil {
		return err
	}
	rec.t, rec.err, rec.genKnown = t, nil, false
	return nil
}

// dropTemporaries removes, with their data, the torrents bt_stream added
// on the fly that a stop or a crash left in a state just read: no stream
// reads them any more. r.mu is held.
func (r *Role) dropTemporaries() {
	n := 0
	for _, rec := range slices.Clone(r.recs) {
		if !rec.temporary {
			continue
		}
		if rec.t != nil {
			if err := rec.t.Remove(true); err != nil && r.log != nil {
				r.log.Warnf("temporary torrent %s: removing its data: %v", rec.id, err)
			}
			rec.t = nil
		}
		r.removeRecord(rec)
		if err := removeFiles(r.dataDir, rec.id); err != nil && r.log != nil {
			r.log.Warnf("temporary torrent %s: %v", rec.id, err)
		}
		n++
	}
	if n == 0 {
		return
	}
	if err := saveList(r.dataDir, r.recs); err == nil {
		r.listDirty = false
	}
	if r.log != nil {
		r.log.Infof("removed %d temporary torrents left from before", n)
	}
}

// closeClient closes the client, then saves every torrent's resume data,
// which no write changes any more. r.mu is held.
func (r *Role) closeClient() {
	if r.client == nil {
		return
	}
	_ = r.client.Close()
	r.client = nil
	r.saveLocked(true)
	for _, rec := range r.recs {
		rec.t = nil
	}
}

// Stop saves the state and closes the client and the console.
func (r *Role) Stop(ctx context.Context) {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	stop, done := r.saverStop, r.saverDone
	r.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeClient()
	r.console.Stop(ctx)
	rolesMu.Lock()
	if roles[r.key] == r {
		delete(roles, r.key)
	}
	rolesMu.Unlock()
}

// saveLoop saves the state every interval, when something changed.
func (r *Role) saveLoop(stop, done chan struct{}) {
	defer close(done)
	r.mu.Lock()
	d := r.interval
	r.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		r.mu.Lock()
		if !r.stopped && r.client != nil {
			r.saveLocked(false)
		}
		d = r.interval
		r.mu.Unlock()
		t.Reset(d)
	}
}

// saveLocked saves what changed: the .torrent files of magnet links whose
// info arrived, the resume data of torrents with pieces verified since
// their last save (of every torrent when final), and the list when it
// changed. An error is logged, once until it changes, and saving goes on
// with the rest; what failed is saved again next time. r.mu is held.
func (r *Role) saveLocked(final bool) {
	var errs []string
	fail := func(what string, err error) { errs = append(errs, what+": "+err.Error()) }
	for _, rec := range r.recs {
		t := rec.t
		if t == nil {
			continue
		}
		if name := t.Name(); name != "" && name != rec.name {
			rec.name, r.listDirty = name, true
		}
		if st := t.State(); (st == fibbt.StatePaused || st == fibbt.StateStopped) && !rec.paused {
			// Paused by its seed limit: it stays paused after a restart.
			rec.paused, rec.resumeDirty, r.listDirty = true, true, true
		}
		if t.Info() == nil {
			continue
		}
		if !rec.torrentSaved {
			if rec.mi == nil {
				rec.mi = t.MetaInfo()
			}
			data, err := bencode.Marshal(rec.mi)
			if err == nil {
				err = writeAtomic(filepath.Join(r.dataDir, torrentDir), rec.id+".torrent", data)
			}
			if err != nil {
				fail("the .torrent file of "+rec.id, err)
			} else {
				rec.torrentSaved = true
			}
		}
		gen := t.ResumeGen()
		if !final && !rec.resumeDirty && rec.genKnown && gen == rec.savedGen {
			continue
		}
		data, err := t.ResumeData()
		if err != nil {
			// Checking its storage, as when its resume data was not
			// used: what was saved before stays.
			continue
		}
		if err := writeAtomic(filepath.Join(r.dataDir, resumeDir), rec.id+".resume", data); err != nil {
			fail("the resume data of "+rec.id, err)
			continue
		}
		rec.resume, rec.savedGen, rec.genKnown, rec.resumeDirty = data, gen, true, false
	}
	if r.listDirty || final {
		if err := saveList(r.dataDir, r.recs); err != nil {
			fail(stateFile, err)
		} else {
			r.listDirty = false
		}
	}
	msg := strings.Join(errs, "; ")
	if msg != r.lastErr && r.log != nil {
		if msg != "" {
			r.log.Errorf("saving the state in %s: %s", r.dataDir, msg)
		} else if r.lastErr != "" {
			r.log.Infof("the state in %s is saved again", r.dataDir)
		}
	}
	r.lastErr = msg
}

// Addr returns the address the client accepts peers on, TCP and UDP, or
// the zero address while it does not run.
func (r *Role) Addr() netip.AddrPort {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client == nil {
		return netip.AddrPort{}
	}
	return r.client.Addr()
}

// Client returns fib's client, or nil while the role does not run. A
// config that makes a new client replaces it: hold it only briefly.
func (r *Role) Client() *fibbt.Client {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.client
}

// removeRecord takes rec out of the records. r.mu is held.
func (r *Role) removeRecord(rec *record) {
	r.recs = slices.DeleteFunc(r.recs, func(x *record) bool { return x == rec })
	delete(r.byID, rec.id)
	r.listDirty = true
}

// mkdir makes dir, for a torrent's downloads.
func mkdir(dir string) error { return os.MkdirAll(dir, 0o755) }

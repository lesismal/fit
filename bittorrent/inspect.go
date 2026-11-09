package bittorrent

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	fibbt "github.com/lesismal/fib/bittorrent"
	"github.com/lesismal/fib/bittorrent/metainfo"
	"github.com/lesismal/fib/bittorrent/tracker"
)

// A torrent's peers, trackers, peer sources and web seeds, and a check of
// its data again, for the Admin API (I203; the trackers' state and the
// check in place since I210, fib A87 and A88). As the rest of the role's API, they must not be called
// on a fib worker.

// PeerStatus is a peer connected to a torrent, as fib reports it.
type PeerStatus struct {
	Addr string `json:"addr"`
	// ID is the peer's ID in hex, Client the client it names, if known.
	ID           string  `json:"id"`
	Client       string  `json:"client,omitempty"`
	Outgoing     bool    `json:"outgoing"`
	UTP          bool    `json:"utp"`
	Encrypted    bool    `json:"encrypted"`
	Seed         bool    `json:"seed"`
	Pieces       int     `json:"pieces"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
	DownloadRate float64 `json:"download_rate"`
	UploadRate   float64 `json:"upload_rate"`
	AmChoking    bool    `json:"am_choking"`
	PeerChoking  bool    `json:"peer_choking"`
	Snubbed      bool    `json:"snubbed"`
}

// Peers reports the peers connected to the torrent id, in the order fib
// gives them; none while it does not run.
func (r *Role) Peers(id string) ([]PeerStatus, error) {
	r.mu.Lock()
	rec := r.byID[id]
	if rec == nil {
		r.mu.Unlock()
		return nil, ErrNotFound
	}
	t := rec.t
	r.mu.Unlock()
	out := []PeerStatus{}
	if t == nil {
		return out, nil
	}
	for _, p := range t.Peers() {
		out = append(out, PeerStatus{Addr: p.Addr.String(), ID: hex.EncodeToString(p.ID[:]), Client: p.Client,
			Outgoing: p.Outgoing, UTP: p.UTP, Encrypted: p.Encrypted, Seed: p.Seed, Pieces: p.Pieces,
			Downloaded: p.Downloaded, Uploaded: p.Uploaded, DownloadRate: p.DownloadRate, UploadRate: p.UploadRate,
			AmChoking: p.AmChoking, PeerChoking: p.PeerChoking, Snubbed: p.Snubbed})
	}
	return out, nil
}

// ErrCannotCheck is a recheck fib refuses: of a torrent in its error state
// (resume it first) or being removed.
var ErrCannotCheck = errors.New("bittorrent: the torrent's data cannot be checked now")

// Announces is the state of a torrent's announces to a tracker under one
// info hash, as fib keeps it (fib's TrackerStatus).
type Announces struct {
	// Updating is set while an announce to the tracker is under way.
	Updating bool `json:"updating"`
	// LastAnnounce is when the last announce ended, absent before the
	// first; Error is its error, "" when the tracker answered, Refused set
	// when the error is the tracker's refusal (its failure reason), and
	// Warning the warning its last answer carried. Fails counts the
	// announces that failed since the last it answered.
	LastAnnounce *time.Time `json:"last_announce,omitempty"`
	Error        string     `json:"error,omitempty"`
	Refused      bool       `json:"refused,omitempty"`
	Warning      string     `json:"warning,omitempty"`
	Fails        int        `json:"fails"`
	// NextAnnounce is when the torrent announces next, absent while it
	// does not run; the tiers are tried in turn (BEP 12), so a tracker is
	// asked only when those before it do not answer.
	NextAnnounce *time.Time `json:"next_announce,omitempty"`
	// Interval and MinInterval are those of its last answer, in seconds.
	Interval    int64 `json:"interval"`
	MinInterval int64 `json:"min_interval"`
	// Peers is how many peers its last answer listed.
	Peers int `json:"peers"`
	// Seeders and Leechers are the counts of its last answer or scrape,
	// Downloaded the completed downloads of its last scrape; -1 when not
	// known.
	Seeders    int `json:"seeders"`
	Leechers   int `json:"leechers"`
	Downloaded int `json:"downloaded"`
	// Scraped is when it last answered a scrape.
	Scraped *time.Time `json:"scraped,omitempty"`
}

// TrackerStatus is a tracker of a torrent and the state of its announces.
type TrackerStatus struct {
	URL string `json:"url"`
	// Tier is its tier (BEP 12), from 0.
	Tier int `json:"tier"`
	// Announces is the state under the torrent's v1 (or only) info hash.
	Announces
	// V2 is the state under a hybrid torrent's truncated v2 hash, which it
	// is announced under too; absent for a torrent of one hash.
	V2 *Announces `json:"v2,omitempty"`
}

// SourceStatus is a source of a torrent's peers.
type SourceStatus struct {
	// Source is tracker, dht, pex, lsd, manual (added by hand or a magnet
	// link's x.pe) or other.
	Source string `json:"source"`
	// Enabled is set when the source is used for the torrent (on in the
	// config, not one a private torrent goes without, trackers to ask).
	Enabled bool `json:"enabled"`
	// Candidates is how many of the peer addresses the torrent keeps came
	// from the source (among others, maybe), Connected how many of those
	// it is connected to.
	Candidates int `json:"candidates"`
	Connected  int `json:"connected"`
}

// WebSeedStatus is a web seed (BEP 19) of a torrent.
type WebSeedStatus struct {
	URL string `json:"url"`
	// Active is how many pieces are being fetched from it.
	Active int `json:"active"`
	// Fails counts its failed fetches and the pieces it sent that failed
	// their check; at 5 it is no longer used (Dropped). Error is the last
	// failure; NextTry, when set, is when it is used again after one.
	Fails   int        `json:"fails"`
	Dropped bool       `json:"dropped"`
	Error   string     `json:"error,omitempty"`
	NextTry *time.Time `json:"next_try,omitempty"`
}

// Trackers is what Role.Trackers reports of a torrent.
type Trackers struct {
	Trackers []TrackerStatus `json:"trackers"`
	Sources  []SourceStatus  `json:"sources"`
	WebSeeds []WebSeedStatus `json:"web_seeds"`
}

// timeOf returns t, or nil when it is zero.
func timeOf(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// announcesOf converts fib's state of a tracker's announces.
func announcesOf(s fibbt.TrackerStatus) Announces {
	a := Announces{Updating: s.Updating, LastAnnounce: timeOf(s.Last), Warning: s.Warning, Fails: s.Fails,
		NextAnnounce: timeOf(s.Next), Interval: int64(s.Interval / time.Second), MinInterval: int64(s.MinInterval / time.Second),
		Peers: s.Peers, Seeders: s.Seeders, Leechers: s.Leechers, Downloaded: s.Downloaded, Scraped: timeOf(s.Scraped)}
	if s.Err != nil {
		a.Error = s.Err.Error()
		var te *tracker.Error
		a.Refused = errors.As(s.Err, &te)
	}
	return a
}

// unknownAnnounces is the state of a tracker not announced to.
var unknownAnnounces = Announces{Seeders: -1, Leechers: -1, Downloaded: -1}

// Trackers reports the trackers of the torrent id with the state of their
// announces, in tier order, the sources of its peers and its web seeds
// with theirs, as fib keeps them (A87). While the torrent is not in the
// client (a broken one), it lists the trackers and web seeds its .torrent
// file or magnet link names, their state unknown, and no sources.
func (r *Role) Trackers(id string) (Trackers, error) {
	r.mu.Lock()
	rec := r.byID[id]
	if rec == nil {
		r.mu.Unlock()
		return Trackers{}, ErrNotFound
	}
	t := rec.t
	var mi *metainfo.MetaInfo
	magnet := ""
	if t == nil {
		mi, magnet = rec.mi, rec.magnet
	}
	r.mu.Unlock()
	out := Trackers{Trackers: []TrackerStatus{}, Sources: []SourceStatus{}, WebSeeds: []WebSeedStatus{}}
	if t == nil {
		trackers, seeds := listed(mi, magnet)
		for _, tr := range trackers {
			tr.Announces = unknownAnnounces
			out.Trackers = append(out.Trackers, tr)
		}
		for _, u := range seeds {
			if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				out.WebSeeds = append(out.WebSeeds, WebSeedStatus{URL: u})
			}
		}
		return out, nil
	}
	// fib's own locks, not the role's: a torrent removed meanwhile answers
	// what it last knew.
	for _, ti := range t.Trackers() {
		ts := TrackerStatus{URL: ti.URL, Tier: ti.Tier, Announces: announcesOf(ti.TrackerStatus)}
		if ti.V2 != nil {
			v2 := announcesOf(*ti.V2)
			ts.V2 = &v2
		}
		out.Trackers = append(out.Trackers, ts)
	}
	for _, s := range t.Sources() {
		out.Sources = append(out.Sources, SourceStatus{Source: s.Source.String(), Enabled: s.Enabled,
			Candidates: s.Candidates, Connected: s.Connected})
	}
	for _, w := range t.WebSeeds() {
		ws := WebSeedStatus{URL: w.URL, Active: w.Active, Fails: w.Fails, Dropped: w.Dropped}
		if w.Err != nil {
			ws.Error = w.Err.Error()
		}
		if w.NextTry.After(time.Now()) {
			ws.NextTry = &w.NextTry
		}
		out.WebSeeds = append(out.WebSeeds, ws)
	}
	return out, nil
}

// listed returns the trackers, by tier, and the web seeds a .torrent
// file's metainfo and a magnet link name.
func listed(mi *metainfo.MetaInfo, magnet string) (trackers []TrackerStatus, webSeeds []string) {
	if mi != nil {
		tiers := mi.AnnounceList
		if len(tiers) == 0 && mi.Announce != "" {
			tiers = [][]string{{mi.Announce}}
		}
		for i, tier := range tiers {
			for _, u := range tier {
				trackers = append(trackers, TrackerStatus{URL: u, Tier: i})
			}
		}
		webSeeds = append(webSeeds, mi.URLList...)
	}
	if magnet != "" {
		if m, err := metainfo.ParseMagnet(magnet); err == nil {
			for _, u := range m.Trackers {
				if !slices.ContainsFunc(trackers, func(t TrackerStatus) bool { return t.URL == u }) {
					trackers = append(trackers, TrackerStatus{URL: u, Tier: len(trackers)})
				}
			}
			for _, u := range m.WebSeeds {
				if !slices.Contains(webSeeds, u) {
					webSeeds = append(webSeeds, u)
				}
			}
		}
	}
	return trackers, webSeeds
}

// Recheck checks the data of the torrent id again, every piece, in place
// (fib's Torrent.Recheck, A88): the torrent keeps its connections, fib's
// counters, its place, priorities, limits and pause; it is checking until
// the check ends, then downloading what failed or seeding. Its resume data
// is removed at the start, so that a restart in the middle checks it all
// again, and saved anew once the check is over. A magnet link whose info
// has not arrived has nothing to check (ErrBadRequest); a torrent fib
// cannot check now is ErrCannotCheck.
func (r *Role) Recheck(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.running(id)
	if err != nil {
		return err
	}
	if rec.t.Info() == nil {
		return fmt.Errorf("%w: its info is not known yet: nothing to check", ErrBadRequest)
	}
	if err := rec.t.Recheck(); err != nil {
		return fmt.Errorf("%w: %v", ErrCannotCheck, err)
	}
	// Resume data from before the check would skip it after a restart; the
	// saver writes it again once fib gives it (it errs while checking).
	rec.resume, rec.resumeDirty = nil, true
	if err := os.Remove(filepath.Join(r.dataDir, resumeDir, rec.id+".resume")); err != nil && !errors.Is(err, fs.ErrNotExist) && r.log != nil {
		r.log.Warnf("torrent %s (%s): removing its resume data: %v", rec.id, rec.name, err)
	}
	r.evMu.Lock()
	delete(r.storageErr, id)
	r.evMu.Unlock()
	if r.log != nil {
		r.log.Infof("torrent %s (%s): checking its data again", rec.id, rec.name)
	}
	r.noteStatesLocked()
	return nil
}

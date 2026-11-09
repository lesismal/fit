package bittorrent

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/lesismal/fib/bittorrent/metainfo"
)

// A torrent's peers and trackers, and a check of its data again, for the
// Admin API (I203). As the rest of the role's API, they must not be called
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

// TrackerStatus is a tracker of a torrent, as its .torrent file or magnet
// link lists it.
type TrackerStatus struct {
	URL string `json:"url"`
	// Tier is its tier (BEP 12), from 0.
	Tier int `json:"tier"`
}

// Trackers reports the trackers of the torrent id, by tier, and its web
// seeds: what its .torrent file or magnet link lists. fib reports nothing
// of their announces (see docs/fib-api-requests.md).
func (r *Role) Trackers(id string) (trackers []TrackerStatus, webSeeds []string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := r.byID[id]
	if rec == nil {
		return nil, nil, ErrNotFound
	}
	trackers, webSeeds = []TrackerStatus{}, []string{}
	mi := rec.mi
	if mi == nil && rec.t != nil {
		mi = rec.t.MetaInfo()
	}
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
	if rec.magnet != "" {
		if m, err := metainfo.ParseMagnet(rec.magnet); err == nil {
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
	return trackers, webSeeds, nil
}

// Recheck checks the data of the torrent id again, every piece: fib has no
// check of a running torrent, so the torrent is taken out of the client,
// keeping its files, and added again without its resume data, which is
// removed so that a restart in the middle checks again too. Its
// connections and fib's counts of what it downloaded and uploaded start
// again; its place, priorities, limits and pause stay. A magnet link whose
// info has not arrived has nothing to check (ErrBadRequest).
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
	if rec.mi == nil {
		rec.mi = rec.t.MetaInfo()
	}
	r.accountLocked(rec, nil)
	if err := rec.t.Remove(false); err != nil && r.log != nil {
		r.log.Warnf("torrent %s (%s): closing its storage for a check: %v", rec.id, rec.name, err)
	}
	rec.t, rec.resume, rec.genKnown = nil, nil, false
	if err := os.Remove(filepath.Join(r.dataDir, resumeDir, rec.id+".resume")); err != nil && !errors.Is(err, fs.ErrNotExist) && r.log != nil {
		r.log.Warnf("torrent %s (%s): removing its resume data: %v", rec.id, rec.name, err)
	}
	r.evMu.Lock()
	delete(r.storageErr, id)
	r.evMu.Unlock()
	if err := r.restore(rec, nil); err != nil {
		rec.err = err
		r.noteStatesLocked()
		return fmt.Errorf("adding it again: %w", err)
	}
	if r.log != nil {
		r.log.Infof("torrent %s (%s): checking its data again", rec.id, rec.name)
	}
	r.scheduleLocked()
	r.noteStatesLocked()
	return r.saveListLocked()
}

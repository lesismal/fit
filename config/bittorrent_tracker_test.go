package config

import (
	"strings"
	"testing"
	"time"
)

// TestBitTorrentTrackerChecks checks what the config refuses of the
// tracker, and its defaults.
func TestBitTorrentTrackerChecks(t *testing.T) {
	tracker := func(js string) string { return `{"bittorrent": {"tracker": ` + js + `}}` }
	h40, h64 := strings.Repeat("ab", 20), strings.Repeat("cd", 32)
	for _, bad := range []struct{ doc, want string }{
		{tracker(`{"listen": "nope"}`), "is not a host and port"},
		{tracker(`{"interval": "-1s"}`), "is negative"},
		{tracker(`{"interval": "10m", "min_interval": "20m"}`), "min_interval 20m0s is longer than the interval"},
		{tracker(`{"interval": "2h"}`), "peer_ttl 1h0m0s is not longer than the interval"},
		{tracker(`{"interval": "10m", "peer_ttl": "10m"}`), "is not longer than the interval"},
		{tracker(`{"auth": "nope"}`), `there is no media_auth policy named "nope"`},
		{tracker(`{"allow": ["xyz"]}`), "is not an info hash"},
		{tracker(`{"allow": ["` + h40[:39] + `"]}`), "is not an info hash"},
		{tracker(`{"allow_files": [" "]}`), "a file name may not be empty"},
		{tracker(`{"max_torrents": 2000000}`), "max_torrents"},
		{tracker(`{"max_peers_per_torrent": 70000}`), "max_peers_per_torrent"},
		{tracker(`{"access": {"allow": ["nope"]}}`), "nope"},
		{tracker(`{"console": {"address": ":0", "users": []}}`), "user"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	c, err := Load([]byte(`{"media_auth": [{"name": "bt", "tokens": [{"token": "0123456789abcdef"}]}], "bittorrent": {"tracker": {"listen": ":6969", "auth": "bt", "allow": ["` + h40 + `", "` + h64 + `"], "allow_files": ["a.txt"], "access": {"deny_files": ["d.txt"]}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tr := c.BitTorrent.Tracker
	if tr.IntervalOr() != 30*time.Minute || tr.TTL() != time.Hour || tr.Torrents() != DefaultBTTrackerMaxTorrents ||
		tr.PeersPerTorrent() != DefaultBTTrackerMaxPeersPerTorrent || tr.Peers() != DefaultBTTrackerMaxPeers ||
		tr.NumWant() != 200 || tr.Scrape() != 64 {
		t.Fatalf("defaults %+v", tr)
	}
	if f := tr.Files(); len(f) != 2 || f[0] != "a.txt" || f[1] != "d.txt" {
		t.Fatalf("files %v", f)
	}
	if !c.HasApps() || len(c.RoleConsoles()) != 0 {
		t.Fatal("the tracker is not a role")
	}
	if h, ok := ParseInfoHash(" " + h64 + " "); !ok || h[0] != 0xcd || h[19] != 0xcd {
		t.Fatalf("v2 hash %x %v", h, ok)
	}
	var nilTracker *BitTorrentTracker
	if nilTracker.Files() != nil {
		t.Fatal("files of nil")
	}
}

// TestBitTorrentTrackerBinds puts the tracker's UDP port in the address
// clash check: it clashes with another UDP port, not a TCP one, and its
// console with another TCP port.
func TestBitTorrentTrackerBinds(t *testing.T) {
	var c Config
	c.BitTorrent = &BitTorrent{
		Client:  &BitTorrentClient{Listen: ":6881", DataDir: "d"},
		Tracker: &BitTorrentTracker{Listen: "127.0.0.1:6881", Console: &RoleConsole{Address: ":7000"}},
	}
	c.Listeners = []Listener{{Name: "web", Address: ":7000", Protocols: []string{ProtocolHTTP1}}}
	errs := bindErrors(&c)
	if len(errs) != 2 || !strings.Contains(errs[0], "listens on TCP :7000 already") ||
		!strings.Contains(errs[1], "bittorrent.client listens on UDP :6881 already") {
		t.Fatalf("%q", errs)
	}
	c.BitTorrent.Tracker = &BitTorrentTracker{Listen: ":6969"}
	c.Listeners[0].Address = ":6969"
	if errs := bindErrors(&c); len(errs) != 0 {
		t.Fatalf("a TCP listener and the tracker's UDP port clash: %q", errs)
	}
	c.BitTorrent.Tracker.Listen = ""
	if errs := bindErrors(&c); len(errs) != 0 {
		t.Fatalf("a tracker without UDP: %q", errs)
	}
}

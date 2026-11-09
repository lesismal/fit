package workbench

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The BitTorrent pages (I204) word everything they show in English and
// Chinese: each key they look up is in their WORDS with both, or one of the
// core's; each word is used; the placeholders of both match. They call the
// Admin API's endpoints they show, and the console's event stream.
func TestBitTorrentWords(t *testing.T) {
	raw, err := fs.ReadFile(Pages(), "bittorrent/bittorrent.js")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	core, _ := fs.ReadFile(Pages(), "core/ui.js")
	words := pageWords(t, src)
	if len(words) < 150 {
		t.Fatalf("%d words", len(words))
	}
	ph := regexp.MustCompile(`\{[a-z]+\}`)
	// Keys built at run time: a state's, a hold's, an event's, a
	// priority's, a refusal's, the alternative rates' mode, a tab's.
	dynamic := []string{"s.", "held.", "ev.", "prio.", "k.ref.", "alt.", "o.", "res.", "src."}
	for k, w := range words {
		if w[0] == "" || w[1] == "" {
			t.Errorf("%s: %q %q", k, w[0], w[1])
		}
		if a, b := ph.FindAllString(w[0], -1), ph.FindAllString(w[1], -1); strings.Join(sorted(a), ",") != strings.Join(sorted(b), ",") {
			t.Errorf("%s: the placeholders differ: %v %v", k, a, b)
		}
		used := strings.Count(src, "'"+k+"'") > 1 ||
			(strings.HasPrefix(k, "n.") && strings.Contains(src, "'"+strings.TrimPrefix(k, "n.")+"'"))
		for _, p := range dynamic {
			used = used || strings.HasPrefix(k, p)
		}
		if !used {
			t.Errorf("%s is never shown", k)
		}
	}
	for _, m := range regexp.MustCompile(`\bt\('([a-zA-Z][a-zA-Z0-9.]*[a-zA-Z0-9])'`).FindAllStringSubmatch(src, -1) {
		if _, ok := words[m[1]]; !ok && !strings.Contains(string(core), "'"+m[1]+"':") {
			t.Errorf("t('%s') has no words", m[1])
		}
	}
	// Every state a torrent can be in, every hold, every event and every
	// priority the role knows has its words.
	need := []string{}
	for _, s := range []string{"metadata", "checking", "downloading", "seeding", "queued", "paused", "stopped", "error"} {
		need = append(need, "s."+s)
	}
	for _, s := range []string{"starting", "queued", "disk"} {
		need = append(need, "held."+s)
	}
	for _, s := range []string{"added", "removed", "state", "completed", "error", "peer_banned", "seed_limit", "alt_speed", "events_dropped", "reset", "rss_added"} {
		need = append(need, "ev."+s)
	}
	for _, o := range []string{"added", "exists", "duplicate", "unmatched", "no_source", "failed"} {
		need = append(need, "o."+o)
	}
	for _, r := range []string{"ok", "not_modified", "error"} {
		need = append(need, "res."+r)
	}
	// Every peer source fib's Torrent.Sources names (I210).
	for _, s := range []string{"tracker", "dht", "pex", "lsd", "manual", "other"} {
		need = append(need, "src."+s)
	}
	need = append(need, "prio.0", "prio.1", "prio.4", "prio.7", "alt.auto", "alt.on", "alt.off", "k.ref.unregistered", "k.ref.passkey", "k.ref.access")
	for _, k := range need {
		if _, ok := words[k]; !ok {
			t.Errorf("%s has no words", k)
		}
	}
	// The pages call the endpoints they show.
	for _, ep := range []string{"'/bittorrent/torrents?'", "'/bittorrent/torrents/'", "'/bittorrent/torrents'", "'/peers'", "'/trackers'",
		"'/bittorrent/alt_speed'", "'/bittorrent/publish'", "'/torrents/'", "'/bittorrent/tracker/stats'", "'/bittorrent/tracker/torrents?offset='",
		"'/bittorrent/tracker/passkey?user='", "'/bittorrent/events'", "'Last-Event-ID'", "action: 'recheck'", "'?delete_data=1'",
		"{ action: action }", "'application/x-bittorrent'", "priorities:", "queue_position:", "sequential:", "limits: null", "category:",
		"members: ['bittorrent']", "C.section('bittorrent')",
		"r.sources", "r.web_seeds", "x.v2", "a.last_announce", "a.next_announce", "a.refused", "a.warning", "a.fails", "a.interval",
		"a.seeders", "a.leechers", "a.downloaded", "x.candidates", "x.connected", "x.next_try", "x.dropped",
		"'/bittorrent/rss'", "'/bittorrent/rss/items?'", "'/bittorrent/rss/feeds/'", "'/poll'", "'/bittorrent/rss/' + kind", "{ enabled: v }"} {
		if !strings.Contains(src, ep) {
			t.Errorf("bittorrent/bittorrent.js does not use %s", ep)
		}
	}
}

// The assistant's BitTorrent tools are worded on the Functions page, and
// its propose tools' steps in the proposals' cards.
func TestBitTorrentAssistantWords(t *testing.T) {
	fn, _ := fs.ReadFile(Pages(), "ai/functions.js")
	pr, _ := fs.ReadFile(Pages(), "ai/proposals.js")
	if !strings.Contains(string(fn), "'g.bittorrent'") {
		t.Error("functions.js has no words for the group bittorrent")
	}
	for _, n := range []string{"bt_torrents", "bt_torrent", "bt_tracker_stats", "bt_rss", "bt_add_propose", "bt_pause_propose", "bt_resume_propose"} {
		if !strings.Contains(string(fn), "'t."+n+"'") {
			t.Errorf("functions.js has no words for %s", n)
		}
	}
	for _, s := range []string{"'tool.bt_add_propose'", "'tool.bt_pause_propose'", "'tool.bt_resume_propose'", "case 'bt_add'", "case 'bt_pause'",
		"case 'bt_resume'", "'pr.m.btAdd'", "'pr.m.btPause'", "'pr.m.btResume'"} {
		if !strings.Contains(string(pr), s) {
			t.Errorf("proposals.js lacks %s", s)
		}
	}
}

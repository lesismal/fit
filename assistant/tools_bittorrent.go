package assistant

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
)

// The BitTorrent tools (I204) read the bittorrent.client and
// bittorrent.tracker roles (I197-I203) through the Admin API in process,
// as the workbench's BitTorrent pages do: the torrents in short, one
// torrent with its files, peers and trackers, and the tracker's totals.
// Adding, pausing and resuming torrents are propose tools
// (proposals_bt.go): steps of the conversation's proposal, applied by a
// person after a second confirm. Nothing that may hold a secret reaches the
// model: a torrent's magnet link (its trackers' URLs may carry a private
// tracker's passkey) is left out, a tracker's URL keeps its scheme, host
// and last path element only, a web seed's loses its query and user, and a
// user's passkey is never read.

// GroupBitTorrent is the BitTorrent tools' group.
const GroupBitTorrent = "bittorrent"

// The bounds of the BitTorrent tools' answers.
const (
	// defaultBTTorrents and maxBTTorrents bound bt_torrents' page.
	defaultBTTorrents = 50
	maxBTTorrents     = 200
	// maxBTFiles and maxBTPeers bound bt_torrent's files and peers.
	maxBTFiles = 100
	maxBTPeers = 50
	// maxBTSwarms bounds the tracker's swarms bt_tracker_stats lists.
	maxBTSwarms = 50
)

// btTools returns the BitTorrent tools: the read tools here, and the
// propose tools of proposals_bt.go.
func btTools() []*Tool {
	return append([]*Tool{
		{Name: "bt_torrents", Group: GroupBitTorrent, Level: config.AssistantRead,
			Description: "The BitTorrent client's torrents (bittorrent.client), in queue order: each one's ID, name, state (metadata, checking, downloading, seeding, queued, paused, stopped, error), why the role holds it (queued past max_active_downloads or max_active_seeds, disk short of space), its error, category, progress, size, bytes wanted and completed, download and upload rates (bytes a second), peers and seeders, ratio (uploaded over size, as seed_ratio counts it) and the seconds left at the present rate; filtered by state (several with commas), category (\"\" for none) and q (in the name), with the total, and the alternative rates' mode and whether they hold.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"state":{"type":"string","description":"states with commas, as downloading,queued"},"category":{"type":"string","description":"a category's name; \"\" for torrents of none; every torrent when left out"},"q":{"type":"string","description":"in the name, any case"},"offset":{"type":"integer"},"limit":{"type":"integer","description":"the most torrents, 50 when left out, at most 200"}}}`),
			Run:         toolBTTorrents},
		{Name: "bt_torrent", Group: GroupBitTorrent, Level: config.AssistantRead,
			Description: "One torrent of the client whole, by its ID as bt_torrents lists it: its status (state, held, error, progress, pieces had of all, rates, totals, ratio, category, its own limits and those it goes by, queue position, sequential, directory), its files (the first 100: index, path, length, completed, priority 0 not downloaded, 1 low, 4 normal, 7 high), its peers (the first 50: address, client, direction, uTP, encryption, seed, pieces, rates, choking) and its trackers by tier and web seeds, as its .torrent file or magnet link names them (a tracker's URL as scheme, host and last path element).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"torrent":{"type":"string","description":"the torrent's ID: 40 hex digits, or its v2 hash"}},"required":["torrent"]}`),
			Run:         toolBTTorrent},
		{Name: "bt_tracker_stats", Group: GroupBitTorrent, Level: config.AssistantRead,
			Description: "The BitTorrent tracker's totals (bittorrent.tracker): torrents and peers in its store against its bounds, announces and scrapes served, refusals by reason (unregistered, passkey, access), whether it is private, the allow-list's size and the UDP address; and the swarms (the allow-list's torrents, or a public tracker's by info hash; the first 50: seeders, leechers, completed). Never a passkey.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run:         toolBTTrackerStats},
	}, btProposeTools()...)
}

// btID returns the torrent's ID id names, checked, in lower case: 40 hex
// digits, or a v2 hash's 64, cut to 40, as the Admin API takes either.
func btID(id string) (string, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if (len(id) != 40 && len(id) != 64) || !isHex(id) {
		return "", errors.New("torrent is a torrent's ID, 40 hex digits (or its v2 hash, 64), as bt_torrents lists them")
	}
	return id[:40], nil
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// btShortKeys are the members of a torrent's status in bt_torrents' short
// form, beside ratio and eta_seconds.
var btShortKeys = []string{"id", "name", "state", "held", "error", "paused", "sequential", "temporary", "category", "complete", "progress",
	"size", "wanted", "completed", "download_rate", "upload_rate", "peers", "seeders", "queue_position"}

// btTorrentView is the part of a torrent's status the tools compute from.
type btTorrentView struct {
	Size         int64   `json:"size"`
	Wanted       int64   `json:"wanted"`
	Completed    int64   `json:"completed"`
	Uploaded     int64   `json:"uploaded"`
	DownloadRate float64 `json:"download_rate"`
	State        string  `json:"state"`
}

// btDerived adds ratio and eta_seconds to s, a torrent's status: ratio is
// uploaded over size, as seed_ratio counts it, and eta the seconds left of
// what it wants at its present rate, while it downloads.
func btDerived(s map[string]json.RawMessage) {
	var v btTorrentView
	raw, _ := json.Marshal(s)
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	if v.Size > 0 {
		s["ratio"] = json.RawMessage(strconv.FormatFloat(math.Round(float64(v.Uploaded)/float64(v.Size)*1000)/1000, 'f', -1, 64))
	}
	if left := v.Wanted - v.Completed; v.State == "downloading" && left > 0 && v.DownloadRate >= 1 {
		s["eta_seconds"] = json.RawMessage(strconv.FormatInt(int64(math.Ceil(float64(left)/v.DownloadRate)), 10))
	}
}

func toolBTTorrents(c *Call) (string, error) {
	var args struct {
		State    string  `json:"state"`
		Category *string `json:"category"`
		Q        string  `json:"q"`
		Offset   int     `json:"offset"`
		Limit    int     `json:"limit"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	offset, limit, err := pageArgs(args.Offset, args.Limit, defaultBTTorrents, maxBTTorrents)
	if err != nil {
		return "", err
	}
	q := url.Values{"offset": {strconv.Itoa(offset)}, "limit": {strconv.Itoa(limit)}}
	if args.State != "" {
		q.Set("state", args.State)
	}
	if args.Category != nil {
		q.Set("category", *args.Category)
	}
	if args.Q != "" {
		q.Set("q", args.Q)
	}
	var list struct {
		Torrents []map[string]json.RawMessage `json:"torrents"`
		Total    int                          `json:"total"`
		Offset   int                          `json:"offset"`
	}
	if err := c.a.apiGet(c.Context, "/bittorrent/torrents?"+q.Encode(), &list); err != nil {
		return "", err
	}
	short := make([]map[string]json.RawMessage, 0, len(list.Torrents))
	for _, t := range list.Torrents {
		s := map[string]json.RawMessage{}
		for _, k := range btShortKeys {
			if v, ok := t[k]; ok {
				s[k] = v
			}
		}
		// ratio needs uploaded, which the short form leaves out.
		s["uploaded"] = t["uploaded"]
		btDerived(s)
		delete(s, "uploaded")
		short = append(short, s)
	}
	out := map[string]any{"torrents": short, "total": list.Total, "offset": list.Offset}
	// The alternative rates, for "why is it slow": the role answers them
	// while its client runs.
	var alt json.RawMessage
	if c.a.apiGet(c.Context, "/bittorrent/alt_speed", &alt) == nil {
		out["alt_speed"] = alt
	}
	return jsonText(out), nil
}

func toolBTTorrent(c *Call) (string, error) {
	var args struct {
		Torrent string `json:"torrent"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	id, err := btID(args.Torrent)
	if err != nil {
		return "", err
	}
	base := "/bittorrent/torrents/" + id
	var st map[string]json.RawMessage
	if err := c.a.apiGet(c.Context, base, &st); err != nil {
		return "", err
	}
	delete(st, "magnet")
	btDerived(st)
	out := map[string]any{}
	var files []json.RawMessage
	_ = json.Unmarshal(st["files"], &files)
	delete(st, "files")
	out["status"] = st
	if len(files) > maxBTFiles {
		out["files_total"] = len(files)
		files = files[:maxBTFiles]
	}
	out["files"] = files
	var peers struct {
		Peers []map[string]json.RawMessage `json:"peers"`
	}
	if err := c.a.apiGet(c.Context, base+"/peers", &peers); err != nil {
		return "", err
	}
	if len(peers.Peers) > maxBTPeers {
		out["peers_total"] = len(peers.Peers)
		peers.Peers = peers.Peers[:maxBTPeers]
	}
	for _, p := range peers.Peers {
		delete(p, "id")
	}
	out["peers"] = peers.Peers
	var tr struct {
		Trackers []struct {
			URL  string `json:"url"`
			Tier int    `json:"tier"`
		} `json:"trackers"`
		WebSeeds []string `json:"web_seeds"`
	}
	if err := c.a.apiGet(c.Context, base+"/trackers", &tr); err != nil {
		return "", err
	}
	for i := range tr.Trackers {
		tr.Trackers[i].URL = trackerURL(tr.Trackers[i].URL)
	}
	for i := range tr.WebSeeds {
		tr.WebSeeds[i] = webSeedURL(tr.WebSeeds[i])
	}
	out["trackers"], out["web_seeds"] = tr.Trackers, tr.WebSeeds
	return jsonText(out), nil
}

// trackerURL returns a tracker's URL as the model is shown it: its scheme,
// host and last path element, the elements before it, where a private
// tracker puts its passkey, as [redacted], and no query or user.
func trackerURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[redacted]"
	}
	out := u.Scheme + "://" + u.Host
	path := strings.Trim(u.Path, "/")
	if path == "" {
		return out
	}
	parts := strings.Split(path, "/")
	if len(parts) > 1 {
		out += "/[redacted]"
	}
	return out + "/" + parts[len(parts)-1]
}

// webSeedURL returns a web seed's URL without its user or query.
func webSeedURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[redacted]"
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String()
}

func toolBTTrackerStats(c *Call) (string, error) {
	var stats json.RawMessage
	if err := c.a.apiGet(c.Context, "/bittorrent/tracker/stats", &stats); err != nil {
		return "", err
	}
	var swarms struct {
		Torrents []json.RawMessage `json:"torrents"`
		Total    int               `json:"total"`
	}
	if err := c.a.apiGet(c.Context, "/bittorrent/tracker/torrents?limit="+strconv.Itoa(maxBTSwarms), &swarms); err != nil {
		return "", err
	}
	return jsonText(map[string]any{"stats": stats, "swarms": swarms.Torrents, "swarms_total": swarms.Total}), nil
}

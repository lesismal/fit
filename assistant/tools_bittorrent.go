package assistant

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"regexp"
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
			Description: "One torrent of the client whole, by its ID as bt_torrents lists it: its status (state, held, error, progress, pieces had of all, rates, totals, ratio, category, its own limits and those it goes by, queue position, sequential, directory), its files (the first 100: index, path, length, completed, priority 0 not downloaded, 1 low, 4 normal, 7 high), its peers (the first 50: address, client, direction, uTP, encryption, seed, pieces, rates, choking), its trackers by tier with their announces (last and next, error, refused, fails, interval, seeders, leechers, downloaded; -1 unknown; v2 for a hybrid's second hash), peer sources and web seeds with their state (a tracker's URL as scheme, host and last path element).",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"torrent":{"type":"string","description":"the torrent's ID: 40 hex digits, or its v2 hash"}},"required":["torrent"]}`),
			Run:         toolBTTorrent},
		{Name: "bt_tracker_stats", Group: GroupBitTorrent, Level: config.AssistantRead,
			Description: "The BitTorrent tracker's totals (bittorrent.tracker): torrents and peers in its store against its bounds, announces and scrapes served, refusals by reason (unregistered, passkey, access), whether it is private, the allow-list's size and the UDP address; and the swarms (the allow-list's torrents, or a public tracker's by info hash; the first 50: seeders, leechers, completed). Never a passkey.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
			Run:         toolBTTrackerStats},
		{Name: "bt_rss", Group: GroupBitTorrent, Level: config.AssistantRead,
			Description: "The BitTorrent client's RSS auto-download (bittorrent.client.rss): each feed's name, title, whether it is polled (and whether the Admin API set that), interval, last poll, its result (ok, not_modified, error) and error, next poll, items seen, torrents added and items failed since fit started; each rule's feeds, category, episodes remembered and torrents added; and the latest items seen (title, feed, outcome: added, exists, duplicate episode, unmatched, no_source, failed; the rule, the torrent's ID, the error). A feed's URL as scheme, host and last path element only.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"feed":{"type":"string","description":"only this feed's items"},"outcome":{"type":"string","description":"only items of this outcome"},"items":{"type":"integer","description":"the most items, 20 when left out, at most 100; 0 for none"}}}`),
			Run:         toolBTRSS},
	}, btProposeTools()...)
}

func toolBTRSS(c *Call) (string, error) {
	var args struct {
		Feed    string `json:"feed"`
		Outcome string `json:"outcome"`
		Items   *int   `json:"items"`
	}
	if err := c.Decode(&args); err != nil {
		return "", err
	}
	n := 20
	if args.Items != nil {
		n = *args.Items
	}
	if n < 0 || n > 100 {
		return "", errors.New("items is 0 to 100")
	}
	var st struct {
		Configured bool                         `json:"configured"`
		Feeds      []map[string]json.RawMessage `json:"feeds"`
		Rules      []json.RawMessage            `json:"rules"`
		Error      string                       `json:"error,omitempty"`
	}
	if err := c.a.apiGet(c.Context, "/bittorrent/rss", &st); err != nil {
		return "", err
	}
	for _, f := range st.Feeds {
		var u string
		_ = json.Unmarshal(f["url"], &u)
		f["url"], _ = json.Marshal(trackerURL(u))
	}
	out := map[string]any{"configured": st.Configured, "feeds": st.Feeds, "rules": st.Rules}
	if st.Error != "" {
		out["save_error"] = st.Error
	}
	if n > 0 && st.Configured {
		q := url.Values{"limit": {strconv.Itoa(n)}}
		if args.Feed != "" {
			q.Set("feed", args.Feed)
		}
		if args.Outcome != "" {
			q.Set("outcome", args.Outcome)
		}
		var items struct {
			Items []map[string]json.RawMessage `json:"items"`
			Total int                          `json:"total"`
		}
		if err := c.a.apiGet(c.Context, "/bittorrent/rss/items?"+q.Encode(), &items); err != nil {
			return "", err
		}
		for _, it := range items.Items {
			delete(it, "key") // a link, which may hold a passkey
		}
		out["items"], out["items_total"] = items.Items, items.Total
	}
	return jsonText(out), nil
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
	// Each tracker's state (I210) passes as it is, but for its URL and any
	// URL or secret of it in its error or warning; a member running an
	// earlier fit answers web seeds as bare URLs.
	var tr struct {
		Trackers []map[string]json.RawMessage `json:"trackers"`
		Sources  json.RawMessage              `json:"sources"`
		WebSeeds []json.RawMessage            `json:"web_seeds"`
	}
	if err := c.a.apiGet(c.Context, base+"/trackers", &tr); err != nil {
		return "", err
	}
	for _, t := range tr.Trackers {
		var raw string
		_ = json.Unmarshal(t["url"], &raw)
		t["url"], _ = json.Marshal(trackerURL(raw))
		redactMembers(t, raw)
		var v2 map[string]json.RawMessage
		if json.Unmarshal(t["v2"], &v2) == nil && v2 != nil {
			redactMembers(v2, raw)
			t["v2"], _ = json.Marshal(v2)
		}
	}
	seeds := make([]any, 0, len(tr.WebSeeds))
	for _, w := range tr.WebSeeds {
		var u string
		if json.Unmarshal(w, &u) == nil {
			seeds = append(seeds, webSeedURL(u))
			continue
		}
		var ws map[string]json.RawMessage
		if json.Unmarshal(w, &ws) != nil || ws == nil {
			continue
		}
		_ = json.Unmarshal(ws["url"], &u)
		ws["url"], _ = json.Marshal(webSeedURL(u))
		redactMembers(ws, u)
		seeds = append(seeds, ws)
	}
	if tr.Trackers == nil {
		tr.Trackers = []map[string]json.RawMessage{}
	}
	out["trackers"], out["web_seeds"] = tr.Trackers, seeds
	if len(tr.Sources) > 0 {
		out["sources"] = tr.Sources
	}
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

// urlInText finds the URLs in an error's or a warning's text.
var urlInText = regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'<>]+`)

// redactText returns an error's or a warning's text of the tracker or web
// seed of URL raw as the model is shown it: every URL in it as trackerURL
// gives it, and the parts of raw that may be secret (its user, the path
// elements before the last, its query's values) as [redacted] wherever
// they still appear.
func redactText(text, raw string) string {
	text = urlInText.ReplaceAllStringFunc(text, func(u string) string {
		t := strings.TrimRight(u, ".,;:)]")
		return trackerURL(t) + u[len(t):]
	})
	u, err := url.Parse(raw)
	if err != nil {
		return text
	}
	var secrets []string
	if u.User != nil {
		secrets = append(secrets, u.User.String())
	}
	if parts := strings.Split(strings.Trim(u.Path, "/"), "/"); len(parts) > 1 {
		secrets = append(secrets, parts[:len(parts)-1]...)
	}
	for _, vs := range u.Query() {
		secrets = append(secrets, vs...)
	}
	for _, sec := range secrets {
		// Short ones would cut ordinary words.
		if len(sec) >= 6 {
			text = strings.ReplaceAll(text, sec, "[redacted]")
		}
	}
	return text
}

// redactMembers redacts the error and warning of m, a tracker's or a web
// seed's state, of URL raw.
func redactMembers(m map[string]json.RawMessage, raw string) {
	for _, k := range []string{"error", "warning"} {
		var s string
		if json.Unmarshal(m[k], &s) == nil && s != "" {
			m[k], _ = json.Marshal(redactText(s, raw))
		}
	}
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

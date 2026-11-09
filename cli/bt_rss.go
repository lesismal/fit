package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/lesismal/fit/bittorrent"
)

// fit bt rss (I208): the RSS auto-download's feeds, rules and items,
// through the Admin API's /bittorrent/rss endpoints.
//
//	fit bt rss [ls]                          the feeds and the rules
//	fit bt rss items [-feed F] [-outcome O] [-limit N]
//	fit bt rss poll <feed>...                poll feeds now
//	fit bt rss enable|disable|reset [-rule] <name>...

// btRSSFlags are fit bt rss's own flags.
type btRSSFlags struct {
	feed, outcome string
	limit         int
	rule          bool
}

func (f *btRSSFlags) add(b *btFlags) {
	b.fs.StringVar(&f.feed, "feed", "", "rss items: only the items of this feed")
	b.fs.StringVar(&f.outcome, "outcome", "", "rss items: only the items of this outcome: added, exists, duplicate, unmatched, no_source or failed")
	b.fs.IntVar(&f.limit, "limit", 0, "rss items: at most this many, the newest first (100 when left out, at most 1000)")
	b.fs.BoolVar(&f.rule, "rule", false, "rss enable, disable and reset: name rules, not feeds")
}

// checkRSSArgs checks fit bt rss's arguments before the Admin API is
// reached: a usage error is exit code 2.
func checkRSSArgs(names []string, f *btRSSFlags, stderr io.Writer) bool {
	sub := "ls"
	if len(names) > 0 {
		sub = names[0]
	}
	switch sub {
	case "ls", "items":
		if len(names) > 1 {
			fmt.Fprintf(stderr, "fit bt rss %s: takes no arguments\n", sub)
			return false
		}
	case "poll", "enable", "disable", "reset":
		if len(names) < 2 {
			fmt.Fprintf(stderr, "fit bt rss %s: name a feed%s\n", sub, map[bool]string{true: " or, with -rule, a rule", false: ""}[sub != "poll"])
			return false
		}
	default:
		fmt.Fprintf(stderr, "fit bt rss: unknown command %q: ls, items, poll, enable, disable or reset\n", sub)
		return false
	}
	if f.rule && sub != "enable" && sub != "disable" && sub != "reset" {
		fmt.Fprintf(stderr, "fit bt rss %s: -rule goes with enable, disable and reset\n", sub)
		return false
	}
	if (f.feed != "" || f.outcome != "" || f.limit != 0) && sub != "items" {
		fmt.Fprintf(stderr, "fit bt rss %s: -feed, -outcome and -limit go with items\n", sub)
		return false
	}
	if f.limit < 0 || f.limit > 1000 {
		fmt.Fprintln(stderr, "fit bt rss items: -limit is 1 to 1000")
		return false
	}
	return true
}

// rss runs fit bt rss.
func (b *btCmd) rss(names []string, f *btRSSFlags) int {
	sub := "ls"
	if len(names) > 0 {
		sub, names = names[0], names[1:]
	}
	b.cmd = "rss " + sub
	switch sub {
	case "ls":
		out, ok := b.call(http.MethodGet, "/bittorrent/rss", nil, "")
		if !ok {
			return 1
		}
		if b.asJSON {
			_, _ = b.stdout.Write(out)
			return 0
		}
		var st bittorrent.RSSStatus
		_ = json.Unmarshal(out, &st)
		printRSS(b.stdout, &st)
		return 0
	case "items":
		q := url.Values{}
		if f.feed != "" {
			q.Set("feed", f.feed)
		}
		if f.outcome != "" {
			q.Set("outcome", f.outcome)
		}
		if f.limit > 0 {
			q.Set("limit", strconv.Itoa(f.limit))
		}
		path := "/bittorrent/rss/items"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		out, ok := b.call(http.MethodGet, path, nil, "")
		if !ok {
			return 1
		}
		if b.asJSON {
			_, _ = b.stdout.Write(out)
			return 0
		}
		var list struct {
			Items []bittorrent.RSSItem `json:"items"`
			Total int                  `json:"total"`
		}
		_ = json.Unmarshal(out, &list)
		printRSSItems(b.stdout, list.Items, list.Total)
		return 0
	}
	code := 0
	for _, name := range names {
		var out []byte
		var ok bool
		kind := "feeds"
		if f.rule {
			kind = "rules"
		}
		p := "/bittorrent/rss/" + kind + "/" + url.PathEscape(name)
		switch sub {
		case "poll":
			out, ok = b.call(http.MethodPost, p+"/poll", nil, "")
		case "enable":
			out, ok = b.call(http.MethodPut, p, []byte(`{"enabled": true}`), "application/json")
		case "disable":
			out, ok = b.call(http.MethodPut, p, []byte(`{"enabled": false}`), "application/json")
		case "reset":
			out, ok = b.call(http.MethodPut, p, []byte(`{"enabled": null}`), "application/json")
		}
		if !ok {
			code = 1
			continue
		}
		if b.asJSON {
			_, _ = b.stdout.Write(out)
			continue
		}
		if sub == "poll" {
			var res bittorrent.RSSPollResult
			_ = json.Unmarshal(out, &res)
			printRSSPoll(b.stdout, &res)
			if res.Result == bittorrent.RSSResultError {
				code = 1
			}
			continue
		}
		var st struct {
			Enabled bool `json:"enabled"`
			Toggled bool `json:"toggled"`
		}
		_ = json.Unmarshal(out, &st)
		state := "off"
		if st.Enabled {
			state = "on"
		}
		how := "as the config says"
		if st.Toggled {
			how = "whatever the config says"
		}
		fmt.Fprintf(b.stdout, "%s %s: %s, %s\n", kind[:len(kind)-1], name, state, how)
	}
	return code
}

// whenText writes a time as fit bt writes them, "-" for none.
func whenText(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

func onText(on, toggled bool) string {
	s := "off"
	if on {
		s = "on"
	}
	if toggled {
		s += "*"
	}
	return s
}

func printRSS(w io.Writer, st *bittorrent.RSSStatus) {
	if !st.Configured {
		fmt.Fprintln(w, "the config has no rss")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FEED\tON\tEVERY\tLAST POLL\tRESULT\tNEXT POLL\tSEEN\tADDED\tFAILED\tURL")
	for _, f := range st.Feeds {
		res := f.LastResult
		if res == "" {
			res = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\n", f.Name, onText(f.Enabled, f.Toggled), f.Interval, whenText(f.LastPoll), res,
			whenText(f.NextPoll), f.Seen, f.Added, f.Failed, f.URL)
	}
	_ = tw.Flush()
	for _, f := range st.Feeds {
		if f.LastError != "" {
			fmt.Fprintf(w, "feed %s: %s\n", f.Name, f.LastError)
		}
	}
	if len(st.Rules) > 0 {
		fmt.Fprintln(w)
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RULE\tON\tFEEDS\tCATEGORY\tEPISODES\tADDED\tLAST ADDED")
		for _, r := range st.Rules {
			feeds := "all"
			if len(r.Feeds) > 0 {
				feeds = fmt.Sprint(r.Feeds)
			}
			eps := "-"
			if r.Episodes {
				eps = strconv.Itoa(r.EpisodesSeen)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\t%s\n", r.Name, onText(r.Enabled, r.Toggled), feeds, r.Category, eps, r.Added, whenText(r.LastAdded))
		}
		_ = tw.Flush()
	}
	fmt.Fprintln(w, "(* turned on or off through the Admin API, whatever the config says)")
	if st.Error != "" {
		fmt.Fprintf(w, "saving the state: %s\n", st.Error)
	}
}

func printRSSItems(w io.Writer, items []bittorrent.RSSItem, total int) {
	if len(items) == 0 {
		fmt.Fprintln(w, "no items")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "SEEN\tFEED\tOUTCOME\tRULE\tTORRENT\tTITLE")
	for _, it := range items {
		title := it.Title
		if len(title) > 64 {
			title = title[:61] + "..."
		}
		torrent := it.Torrent
		if len(torrent) > 12 {
			torrent = torrent[:12]
		}
		outcome := it.Outcome
		if it.Error != "" {
			outcome += " (" + it.Error + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", whenText(it.Seen), it.Feed, outcome, it.Rule, torrent, title)
	}
	_ = tw.Flush()
	if total > len(items) {
		fmt.Fprintf(w, "%d of %d items\n", len(items), total)
	}
}

func printRSSPoll(w io.Writer, r *bittorrent.RSSPollResult) {
	switch r.Result {
	case bittorrent.RSSResultError:
		fmt.Fprintf(w, "feed %s: error: %s\n", r.Feed, r.Error)
	case bittorrent.RSSResultNotModified:
		fmt.Fprintf(w, "feed %s: not modified\n", r.Feed)
	default:
		fmt.Fprintf(w, "feed %s: %d items, %d new, %d added, %d failed, %d left for the next poll\n", r.Feed, r.Items, r.New, r.Added, r.Failed, r.Deferred)
		if r.Error != "" {
			fmt.Fprintf(w, "  %s\n", r.Error)
		}
	}
}

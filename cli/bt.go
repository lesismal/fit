package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/config"
)

const btUsage = `Usage: fit bt <command> [flags] [arguments]

See and steer a running fit's BitTorrent client, through its Admin API:

	fit bt ls [-state S[,S]] [-category C] [-q TEXT]
	                                       the torrents, in queue order
	fit bt add [-category C] [-paused] [-sequential] [-up RATE] [-down RATE]
	        <magnet link | http(s) URL | file.torrent>...
	                                       add torrents
	fit bt info <torrent>                  a torrent's status and files
	fit bt pause <torrent>...              pause torrents
	fit bt resume <torrent>...             resume them, as the queue and the
	                                       disk let them
	fit bt rm [-delete-data] <torrent>...  remove torrents, and with
	                                       -delete-data their files
	fit bt limit [-up RATE] [-down RATE] [-conns N] [-slots N] [-ratio X]
	        [-seed-time D] [-clear] <torrent>
	                                       set a torrent's own limits, over its
	                                       category's; -clear removes them
	fit bt alt [auto | on | off]           show the alternative rates, or turn
	                                       them on or off whatever their
	                                       schedules say, or back to them

A <torrent> is its ID (its info hash), a unique start of it, or its name.
A RATE is bytes per second, with K, M or G (KiB, MiB, GiB) after it if
wanted; 0 takes the category's or the client's. Every command takes -c, the
bootstrap config whose admin says where the Admin API is, -timeout, -json
and the TLS flags of fit check. Exits 1 when the Admin API refuses.
`

// btFlags are the flags every fit bt command takes.
type btFlags struct {
	fs       *flag.FlagSet
	cfgPaths configPaths
	timeout  *time.Duration
	tls      checkTLS
	asJSON   *bool
}

func newBTFlags(cmd string, stderr io.Writer) *btFlags {
	f := &btFlags{fs: flag.NewFlagSet("bt "+cmd, flag.ContinueOnError)}
	f.fs.SetOutput(stderr)
	f.fs.Usage = func() { fmt.Fprint(stderr, btUsage) }
	f.fs.Var(&f.cfgPaths, "c", "the bootstrap config file, or a directory of them, fit was started with, whose admin says where its Admin API is (fit.json when none is given); give -c again for more, which are merged")
	f.timeout = f.fs.Duration("timeout", 90*time.Second, "how long fit bt waits for the Admin API, a .torrent file downloaded by URL included")
	f.fs.StringVar(&f.tls.CA, "ca", "", "with admin.cert_file, a PEM file with the CAs the Admin API's certificate is verified with")
	f.fs.StringVar(&f.tls.ServerName, "server-name", "", "with admin.cert_file, the name the Admin API's certificate is verified for")
	f.fs.StringVar(&f.tls.Cert, "cert", "", "with admin.client_ca_file, a PEM file with the client certificate fit bt presents")
	f.fs.StringVar(&f.tls.Key, "key", "", "the private key of -cert")
	f.asJSON = f.fs.Bool("json", false, "print the Admin API's answer as JSON")
	return f
}

// api reaches the Admin API the bootstrap config names.
func (f *btFlags) api(stderr io.Writer) (*checkAPI, bool) {
	paths := f.cfgPaths.orDefault()
	set, _, err := loadSet(paths)
	if err != nil {
		printConfigError(stderr, "fit bt", strings.Join(paths, ", "), err)
		return nil, false
	}
	a, err := checkAdminOf(set.JSON)
	if err == nil {
		var api *checkAPI
		if api, err = newCheckAPI(a, f.tls, *f.timeout); err == nil {
			return api, true
		}
	}
	fmt.Fprintf(stderr, "fit bt: %s\n", strings.ReplaceAll(err.Error(), "fit check", "fit bt"))
	return nil, false
}

// parseArgs parses args, flags and arguments mixed, and returns the
// arguments.
func (f *btFlags) parseArgs(args []string) ([]string, bool) {
	var names []string
	for rest := args; ; {
		if err := f.fs.Parse(rest); err != nil {
			return nil, false
		}
		if f.fs.NArg() == 0 {
			return names, true
		}
		names = append(names, f.fs.Arg(0))
		rest = f.fs.Args()[1:]
	}
}

// rateFlag is a rate in bytes per second, with K, M or G after it.
type rateFlag struct {
	n   int
	set bool
}

func (r *rateFlag) String() string { return strconv.Itoa(r.n) }

func (r *rateFlag) Set(s string) error {
	n, err := parseRate(s)
	if err != nil {
		return err
	}
	r.n, r.set = n, true
	return nil
}

// parseRate reads a number of bytes, with K, M or G (or KiB, MiB, GiB)
// after it, binary multiples; a negative one is no limit.
func parseRate(s string) (int, error) {
	t := strings.TrimSpace(s)
	mult := 1
	for _, u := range []struct {
		suffix string
		m      int
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30}, {"k", 1 << 10}} {
		if v, ok := strings.CutSuffix(t, u.suffix); ok {
			t, mult = strings.TrimSpace(v), u.m
			break
		}
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || f*float64(mult) > 1<<40 || f*float64(mult) < -(1<<40) {
		return 0, fmt.Errorf("%q is not a rate: bytes per second, as 524288, 512K or 1.5M", s)
	}
	return int(f * float64(mult)), nil
}

// runBT runs the fit bt commands.
func runBT(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, btUsage)
		return 2
	}
	cmd := args[0]
	switch cmd {
	case "ls", "add", "info", "pause", "resume", "rm", "limit", "alt":
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, btUsage)
		return 0
	default:
		fmt.Fprintf(stderr, "fit bt: unknown command %q\n\n%s", cmd, btUsage)
		return 2
	}
	f := newBTFlags(cmd, stderr)
	var (
		state, category, query string
		paused, sequential     bool
		deleteData, clear      bool
		up, down               rateFlag
		conns, slots           int
		ratio                  float64
		seedTime               string
	)
	switch cmd {
	case "ls":
		f.fs.StringVar(&state, "state", "", "only torrents in these states, with commas: "+strings.Join(bittorrent.States, ", "))
		f.fs.StringVar(&category, "category", "", "only torrents of this category")
		f.fs.StringVar(&query, "q", "", "only torrents whose name has this, in any case")
	case "add":
		f.fs.StringVar(&category, "category", "", "the category to add them in")
		f.fs.BoolVar(&paused, "paused", false, "add them paused")
		f.fs.BoolVar(&sequential, "sequential", false, "download their pieces in order")
		f.fs.Var(&up, "up", "their own upload rate")
		f.fs.Var(&down, "down", "their own download rate")
	case "rm":
		f.fs.BoolVar(&deleteData, "delete-data", false, "delete the torrents' files too")
	case "limit":
		f.fs.Var(&up, "up", "the torrent's upload rate; negative is the client's alone")
		f.fs.Var(&down, "down", "the torrent's download rate; negative is the client's alone")
		f.fs.IntVar(&conns, "conns", 0, "the torrent's peer connections; negative is the client's max_conns alone")
		f.fs.IntVar(&slots, "slots", 0, "how many peers it uploads to at once; negative unchokes every interested peer")
		f.fs.Float64Var(&ratio, "ratio", 0, "pause it once it has uploaded this many times its size; negative seeds for ever")
		f.fs.StringVar(&seedTime, "seed-time", "", "pause it once it has seeded this long, as 12h; negative (-1s) seeds for ever")
		f.fs.BoolVar(&clear, "clear", false, "remove the torrent's own limits")
	}
	names, ok := f.parseArgs(args[1:])
	if !ok {
		return 2
	}
	switch {
	case cmd == "ls" && len(names) > 0:
		fmt.Fprintln(stderr, "fit bt ls: takes no arguments; filter with -state, -category and -q")
		return 2
	case cmd == "alt" && len(names) > 1:
		fmt.Fprintln(stderr, "fit bt alt: give auto, on or off, or nothing")
		return 2
	case cmd == "alt" && len(names) == 1 && names[0] != "auto" && names[0] != "on" && names[0] != "off":
		fmt.Fprintf(stderr, "fit bt alt: the mode is auto, on or off, not %q\n", names[0])
		return 2
	case (cmd == "info" || cmd == "limit") && len(names) != 1:
		fmt.Fprintf(stderr, "fit bt %s: name one torrent\n", cmd)
		return 2
	case cmd != "ls" && cmd != "alt" && len(names) == 0:
		fmt.Fprintf(stderr, "fit bt %s: name a torrent\n", cmd)
		return 2
	case cmd == "limit" && clear && (up.set || down.set || conns != 0 || slots != 0 || ratio != 0 || seedTime != ""):
		fmt.Fprintln(stderr, "fit bt limit: -clear goes alone")
		return 2
	case cmd == "limit" && !clear && !up.set && !down.set && conns == 0 && slots == 0 && ratio == 0 && seedTime == "":
		fmt.Fprintln(stderr, "fit bt limit: give a limit, or -clear")
		return 2
	}
	if seedTime != "" {
		if _, err := time.ParseDuration(seedTime); err != nil {
			fmt.Fprintf(stderr, "fit bt limit: -seed-time %q is not a duration, as 12h\n", seedTime)
			return 2
		}
	}
	api, ok := f.api(stderr)
	if !ok {
		return 2
	}
	defer api.close()
	b := &btCmd{api: api, cmd: cmd, stdout: stdout, stderr: stderr, asJSON: *f.asJSON}
	switch cmd {
	case "ls":
		q := url.Values{}
		if state != "" {
			q.Set("state", state)
		}
		if category != "" {
			q.Set("category", category)
		}
		if query != "" {
			q.Set("q", query)
		}
		path := "/bittorrent/torrents"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		out, ok := b.call(http.MethodGet, path, nil, "")
		if !ok {
			return 1
		}
		if b.asJSON {
			_, _ = stdout.Write(out)
			return 0
		}
		var list struct {
			Torrents []bittorrent.Status `json:"torrents"`
			Total    int                 `json:"total"`
		}
		_ = json.Unmarshal(out, &list)
		printTorrents(stdout, list.Torrents, list.Total)
		return 0
	case "add":
		return b.add(names, category, paused, sequential, up, down)
	case "alt":
		method, body := http.MethodGet, []byte(nil)
		if len(names) == 1 {
			method, body = http.MethodPut, []byte(`{"mode": `+strconv.Quote(names[0])+`}`)
		}
		out, ok := b.call(method, "/bittorrent/alt_speed", body, "application/json")
		if !ok {
			return 1
		}
		if b.asJSON {
			_, _ = stdout.Write(out)
			return 0
		}
		var a bittorrent.AltSpeedStatus
		_ = json.Unmarshal(out, &a)
		holds := "do not hold"
		if a.Active {
			holds = "hold"
		}
		fmt.Fprintf(stdout, "mode    %s\nnow     the alternative rates %s; the client goes by upload %s, download %s\n", a.Mode, holds, rateText(float64(a.UploadRate)), rateText(float64(a.DownloadRate)))
		if !a.Configured {
			fmt.Fprintln(stdout, "        (the config has no alt_speed: the alternative rates never hold)")
		}
		return 0
	}
	ids, ok := b.resolve(names)
	if !ok {
		return 1
	}
	code := 0
	for _, id := range ids {
		p := "/bittorrent/torrents/" + id
		var out []byte
		switch cmd {
		case "info":
			out, ok = b.call(http.MethodGet, p, nil, "")
		case "pause", "resume":
			out, ok = b.call(http.MethodPost, p, []byte(`{"action": "`+cmd+`"}`), "application/json")
		case "rm":
			if deleteData {
				p += "?delete_data=1"
			}
			out, ok = b.call(http.MethodDelete, p, nil, "")
		case "limit":
			body := []byte(`{"limits": null}`)
			if !clear {
				l := map[string]any{"upload_rate": up.n, "download_rate": down.n, "max_conns": conns, "upload_slots": slots, "seed_ratio": ratio}
				if seedTime != "" {
					l["seed_time"] = seedTime
				}
				body, _ = json.Marshal(map[string]any{"limits": l})
			}
			out, ok = b.call(http.MethodPatch, p, body, "application/json")
		}
		if !ok {
			code = 1
			continue
		}
		if b.asJSON {
			_, _ = stdout.Write(out)
			continue
		}
		switch cmd {
		case "info":
			var st bittorrent.Status
			_ = json.Unmarshal(out, &st)
			printTorrent(stdout, &st)
		case "rm":
			fmt.Fprintf(stdout, "%s removed\n", id)
		default:
			var st bittorrent.Status
			_ = json.Unmarshal(out, &st)
			fmt.Fprintf(stdout, "%s %s: %s\n", id, st.Name, st.State)
		}
	}
	return code
}

// btCmd is a fit bt command's Admin API and outputs.
type btCmd struct {
	api            *checkAPI
	cmd            string
	stdout, stderr io.Writer
	asJSON         bool
}

// call sends a request and returns its answer's body, or reports why it
// failed.
func (b *btCmd) call(method, path string, body []byte, contentType string) ([]byte, bool) {
	out, status, err := b.api.send(method, path, body, contentType)
	if err == nil && (status < 200 || status > 299) {
		err = b.api.refused(status, out)
	}
	if err != nil {
		fmt.Fprintf(b.stderr, "fit bt %s: %v\n", b.cmd, err)
		return nil, false
	}
	return out, true
}

// resolve returns the IDs of the torrents names name: an ID or info hash,
// a unique start of one, or a name.
func (b *btCmd) resolve(names []string) ([]string, bool) {
	var list []bittorrent.Status
	listed := false
	ids := make([]string, 0, len(names))
	for _, n := range names {
		if h, ok := config.ParseInfoHash(n); ok {
			ids = append(ids, fmt.Sprintf("%x", h))
			continue
		}
		if !listed {
			out, ok := b.call(http.MethodGet, "/bittorrent/torrents?limit=10000", nil, "")
			if !ok {
				return nil, false
			}
			var l struct {
				Torrents []bittorrent.Status `json:"torrents"`
			}
			_ = json.Unmarshal(out, &l)
			list, listed = l.Torrents, true
		}
		var found []string
		lower := strings.ToLower(n)
		for _, st := range list {
			if st.Name == n || len(lower) >= 4 && strings.HasPrefix(st.ID, lower) {
				found = append(found, st.ID)
			}
		}
		switch len(found) {
		case 0:
			fmt.Fprintf(b.stderr, "fit bt %s: no torrent is %q\n", b.cmd, n)
			return nil, false
		case 1:
			ids = append(ids, found[0])
		default:
			fmt.Fprintf(b.stderr, "fit bt %s: %q names %d torrents: give more of the ID\n", b.cmd, n, len(found))
			return nil, false
		}
	}
	return ids, true
}

// add adds each source.
func (b *btCmd) add(sources []string, category string, paused, sequential bool, up, down rateFlag) int {
	code := 0
	for _, src := range sources {
		q := url.Values{}
		var body []byte
		contentType := "application/json"
		req := map[string]any{"category": category, "paused": paused, "sequential": sequential}
		if up.set || down.set {
			req["limits"] = map[string]any{"upload_rate": up.n, "download_rate": down.n}
		}
		switch {
		case strings.HasPrefix(src, "magnet:"):
			req["magnet"] = src
		case strings.HasPrefix(src, "http://"), strings.HasPrefix(src, "https://"):
			req["url"] = src
		default:
			data, err := readTorrentFile(src)
			if err != nil {
				fmt.Fprintf(b.stderr, "fit bt add: %v\n", err)
				code = 1
				continue
			}
			if up.set || down.set {
				// The limits do not go in a query: JSON then.
				req["torrent"] = data
			} else {
				body, contentType = data, "application/x-bittorrent"
				if category != "" {
					q.Set("category", category)
				}
				if paused {
					q.Set("paused", "1")
				}
				if sequential {
					q.Set("sequential", "1")
				}
			}
		}
		if body == nil {
			body, _ = json.Marshal(req)
		}
		path := "/bittorrent/torrents"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		out, ok := b.call(http.MethodPost, path, body, contentType)
		if !ok {
			code = 1
			continue
		}
		if b.asJSON {
			_, _ = b.stdout.Write(out)
			continue
		}
		var st bittorrent.Status
		_ = json.Unmarshal(out, &st)
		fmt.Fprintf(b.stdout, "%s %s: added, %s\n", st.ID, st.Name, st.State)
	}
	return code
}

// readTorrentFile reads a .torrent file, at most as large as the Admin API
// takes one.
func readTorrentFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, bittorrent.MaxURLTorrentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > bittorrent.MaxURLTorrentBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes, the most the Admin API takes", path, bittorrent.MaxURLTorrentBytes)
	}
	if len(data) == 0 {
		return nil, errors.New(path + " is empty")
	}
	return data, nil
}

// send sends a request with body of contentType to the Admin API and
// returns its answer's body, whatever its status.
func (api *checkAPI) send(method, path string, body []byte, contentType string) ([]byte, int, error) {
	var in io.Reader
	if body != nil {
		in = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, api.baseURL+path, in)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", contentType)
	}
	if api.token != "" {
		req.Header.Set("Authorization", "Bearer "+api.token)
	}
	resp, err := api.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, checkMaxBytes+1))
	if err != nil {
		return nil, 0, fmt.Errorf("reading the Admin API's answer: %w", err)
	}
	if int64(len(data)) > checkMaxBytes {
		return nil, 0, fmt.Errorf("the Admin API's answer is larger than %d bytes", checkMaxBytes)
	}
	return data, resp.StatusCode, nil
}

// rateText writes a rate in bytes per second.
func rateText(v float64) string {
	if v <= 0 {
		return "unlimited"
	}
	return sizeText(v) + "/s"
}

// sizeText writes a number of bytes in binary units.
func sizeText(v float64) string {
	for _, u := range []string{"B", "KiB", "MiB", "GiB", "TiB"} {
		if v < 1024 || u == "TiB" {
			if u == "B" {
				return fmt.Sprintf("%.0f %s", v, u)
			}
			return fmt.Sprintf("%.1f %s", v, u)
		}
		v /= 1024
	}
	return ""
}

// speedText writes a transfer rate, "-" for none.
func speedText(v float64) string {
	if v < 1 {
		return "-"
	}
	return sizeText(v) + "/s"
}

func printTorrents(w io.Writer, list []bittorrent.Status, total int) {
	if len(list) == 0 {
		fmt.Fprintln(w, "no torrents")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATE\tPROGRESS\tSIZE\tDOWN\tUP\tPEERS\tCATEGORY")
	for _, s := range list {
		name := s.Name
		if len(name) > 48 {
			name = name[:45] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%.1f%%\t%s\t%s\t%s\t%d\t%s\n", s.ID[:min(12, len(s.ID))], name, s.State, s.Progress*100,
			sizeText(float64(s.Size)), speedText(s.DownloadRate), speedText(s.UploadRate), s.Peers, s.Category)
	}
	_ = tw.Flush()
	if total > len(list) {
		fmt.Fprintf(w, "%d of %d torrents\n", len(list), total)
	}
}

func printTorrent(w io.Writer, s *bittorrent.Status) {
	fmt.Fprintf(w, "id         %s\nname       %s\nstate      %s", s.ID, s.Name, s.State)
	if s.Held != "" && s.Held != s.State {
		fmt.Fprintf(w, " (held: %s)", s.Held)
	}
	fmt.Fprintln(w)
	if s.Error != "" {
		fmt.Fprintf(w, "error      %s\n", s.Error)
	}
	if s.InfoHash != "" {
		fmt.Fprintf(w, "info hash  %s\n", s.InfoHash)
	}
	if s.InfoHashV2 != "" {
		fmt.Fprintf(w, "v2 hash    %s\n", s.InfoHashV2)
	}
	fmt.Fprintf(w, "dir        %s\n", s.Dir)
	if s.Category != "" {
		fmt.Fprintf(w, "category   %s\n", s.Category)
	}
	fmt.Fprintf(w, "progress   %.1f%%, %s of %s wanted, %d of %d pieces\n", s.Progress*100, sizeText(float64(s.Completed)), sizeText(float64(s.Wanted)), s.PiecesHave, s.Pieces)
	fmt.Fprintf(w, "transfer   down %s at %s, up %s at %s\n", sizeText(float64(s.Downloaded)), speedText(s.DownloadRate), sizeText(float64(s.Uploaded)), speedText(s.UploadRate))
	fmt.Fprintf(w, "peers      %d, %d of them seeds\n", s.Peers, s.Seeders)
	fmt.Fprintf(w, "queue      %d", s.QueuePosition)
	if s.Sequential {
		fmt.Fprint(w, ", sequential")
	}
	if s.Paused {
		fmt.Fprint(w, ", paused")
	}
	fmt.Fprintln(w)
	if l := s.Effective; l != nil {
		fmt.Fprintf(w, "limits     up %s, down %s, conns %d, slots %d, ratio %g, seed time %s\n", rateText(float64(l.UploadRate)), rateText(float64(l.DownloadRate)),
			l.MaxConns, l.UploadSlots, l.SeedRatio, l.SeedTime)
	}
	if len(s.Files) > 0 {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "FILE\tSIZE\tDONE\tPRIORITY\tPATH")
		for _, f := range s.Files {
			done := 100.0
			if f.Length > 0 {
				done = float64(f.Completed) * 100 / float64(f.Length)
			}
			fmt.Fprintf(tw, "%d\t%s\t%.1f%%\t%d\t%s\n", f.Index, sizeText(float64(f.Length)), done, f.Priority, f.Path)
		}
		_ = tw.Flush()
	}
}

package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/lesismal/fit/cluster"
)

const clusterUsage = `Usage: fit cluster <command> [flags]

Manage the members of a running manager of other fits, one with
admin.cluster, through its Admin API, as fit check reaches it: the bootstrap
config given with -c says where, and the token in its admin.token_file. The
Admin API acts as a cluster-wide admin, whatever the roles of the console's
users say, and the manager's audit log records its actions as "Admin API".

	fit cluster nodes                    the members: URL, access and labels
	fit cluster state                    each member's state, as the poller last read it
	fit cluster groups                   the groups and their members
	fit cluster diff [-node a,b | -template T]
	                                     each member's parts that differ from its templates
	fit cluster certs [-within 30d]      every member's certificates, the soonest to expire
	                                     first; with -within, exit 1 when one expires in it
	fit cluster metrics [-node a,b] [-group g]
	                                     requests, 4xx and 5xx a second, connections and
	                                     bytes queued, per member and group, at the last poll
	fit cluster logs -node a,b [-level L] [-logger L] [-contains T] [-limit N] [-f]
	                                     the members' logs merged by time; -f follows them
	fit cluster rollout list             the rollouts the manager keeps
	fit cluster rollout start (-template T [-group G] | -sync [-group G]) [-node a,b]
	                          [strategy] [-yes] [-wait]
	fit cluster rollout start -id ID [strategy] [-yes] [-wait]
	                                     plan a rollout (or take one planned), show each
	                                     member's plan, ask, and start it
	fit cluster rollout status [-wait] ID
	fit cluster rollout <pause|resume|go|abort|rollback> ID

A rollout's strategy: -mode all|rolling|canary, -batch-size N, -batch-percent P,
-pause 30s, -canary N, -on-failure batch|all, -gate-wait 5s, -max-5xx 0.01,
-window 30s, -min-requests 20, -exclude a,b.

Every command takes -c, -timeout, -json and the TLS flags of fit check.
`

// clusterFlags are the flags every fit cluster command takes.
type clusterFlags struct {
	fs       *flag.FlagSet
	cfgPaths configPaths
	timeout  *time.Duration
	tls      checkTLS
	asJSON   *bool
}

func newClusterFlags(name string, stderr io.Writer) *clusterFlags {
	f := &clusterFlags{fs: flag.NewFlagSet("cluster "+name, flag.ContinueOnError)}
	f.fs.SetOutput(stderr)
	f.fs.Var(&f.cfgPaths, "c", "the bootstrap config file, or a directory of them, the manager was started with, whose admin says where its Admin API is (fit.json when none is given); give -c again for more, which are merged")
	f.timeout = f.fs.Duration("timeout", 60*time.Second, "how long fit cluster waits for each answer of the Admin API")
	f.fs.StringVar(&f.tls.CA, "ca", "", "with admin.cert_file, a PEM file with the CAs the Admin API's certificate is verified with")
	f.fs.StringVar(&f.tls.ServerName, "server-name", "", "with admin.cert_file, the name the Admin API's certificate is verified for")
	f.fs.StringVar(&f.tls.Cert, "cert", "", "with admin.client_ca_file, a PEM file with the client certificate fit cluster presents")
	f.fs.StringVar(&f.tls.Key, "key", "", "the private key of -cert")
	f.asJSON = f.fs.Bool("json", false, "print the Admin API's answer as JSON")
	return f
}

// clusterClient is the manager's Admin API, as fit cluster calls it.
type clusterClient struct {
	api    *checkAPI
	stderr io.Writer
	cmd    string
}

// call sends a request and decodes a 2xx answer into v, when not nil; it
// returns the answer's body.
func (c *clusterClient) call(method, path string, body any, v any) ([]byte, error) {
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body) // the request types marshal
	}
	out, status, err := c.api.do(method, cluster.AdminAPIPrefix+path, data, "")
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound && strings.Contains(string(out), "admin.cluster is not set") {
		return nil, errors.New("the fit the config names manages no members: its admin sets no cluster")
	}
	if status < 200 || status > 299 {
		return nil, c.api.refused(status, out)
	}
	if v != nil {
		if err := json.Unmarshal(out, v); err != nil {
			return nil, fmt.Errorf("the Admin API's answer is not the JSON expected: %w", err)
		}
	}
	return out, nil
}

// clusterSleep waits between two reads of a rollout or a tail; tests
// shorten it.
var clusterSleep = time.Sleep

// clusterFollowRounds, when not 0, ends logs -f after that many reads, for
// tests; otherwise it follows until SIGINT.
var clusterFollowRounds = 0

// runCluster manages a running manager's members through its Admin API.
func runCluster(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, clusterUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd := args[0]
	if cmd == "rollout" {
		return runClusterRollout(args[1:], stdout, stderr)
	}
	f := newClusterFlags(cmd, stderr)
	var node, group, template, within, level, logger, contains *string
	var limit *int
	var follow *bool
	switch cmd {
	case "nodes", "state", "groups":
	case "diff":
		node = f.fs.String("node", "", "the members to compare, separated by commas; every member when left out")
		template = f.fs.String("template", "", "the members of a template, in place of -node")
	case "certs":
		within = f.fs.String("within", "", "exit 1 when a certificate expires within this, as 30d or 72h")
	case "metrics":
		node = f.fs.String("node", "", "the members, separated by commas")
		group = f.fs.String("group", "", "the groups, separated by commas; every member and group when neither is given")
	case "logs":
		node = f.fs.String("node", "", "the members whose logs to tail, separated by commas (required)")
		level = f.fs.String("level", "", "the least level: debug, info, warn or error")
		logger = f.fs.String("logger", "", "only this logger's messages, as server or tls")
		contains = f.fs.String("contains", "", "only messages with this text, case ignored")
		limit = f.fs.Int("limit", 0, "at most this many messages from each member, the latest")
		follow = f.fs.Bool("f", false, "follow: print what comes next, until interrupted")
	default:
		fmt.Fprintf(stderr, "fit cluster: unknown command %q\n\n%s", cmd, clusterUsage)
		return 2
	}
	if err := f.fs.Parse(args[1:]); err != nil {
		return 2
	}
	if f.fs.NArg() > 0 {
		fmt.Fprintf(stderr, "fit cluster %s: unexpected argument %q\n", cmd, f.fs.Arg(0))
		return 2
	}
	var withinDur time.Duration
	switch {
	case cmd == "logs" && *node == "":
		fmt.Fprintln(stderr, "fit cluster logs: -node is required")
		return 2
	case cmd == "diff" && *node != "" && *template != "":
		fmt.Fprintln(stderr, "fit cluster diff: give -node or -template, not both")
		return 2
	case cmd == "certs" && *within != "":
		d, err := parseDays(*within)
		if err != nil {
			fmt.Fprintf(stderr, "fit cluster certs: -within: %v\n", err)
			return 2
		}
		withinDur = d
	}
	c, ok := f.client(cmd, stderr)
	if !ok {
		return 2
	}
	defer c.api.close()
	var code int
	var err error
	switch cmd {
	case "nodes":
		code, err = c.nodes(stdout, *f.asJSON)
	case "state":
		code, err = c.state(stdout, *f.asJSON)
	case "groups":
		code, err = c.groups(stdout, *f.asJSON)
	case "diff":
		code, err = c.diff(stdout, *f.asJSON, splitList(*node), *template)
	case "certs":
		code, err = c.certs(stdout, *f.asJSON, withinDur)
	case "metrics":
		code, err = c.metrics(stdout, *f.asJSON, splitList(*node), splitList(*group))
	case "logs":
		q := url.Values{"node": {*node}}
		for k, v := range map[string]string{"level": *level, "logger": *logger, "contains": *contains} {
			if v != "" {
				q.Set(k, v)
			}
		}
		if *limit > 0 {
			q.Set("limit", fmt.Sprint(*limit))
		}
		code, err = c.logs(stdout, *f.asJSON, q, *follow)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit cluster %s: %v\n", cmd, err)
		return 1
	}
	return code
}

// client reaches the Admin API the bootstrap config names.
func (f *clusterFlags) client(cmd string, stderr io.Writer) (*clusterClient, bool) {
	paths := f.cfgPaths.orDefault()
	set, _, err := loadSet(paths)
	if err != nil {
		printConfigError(stderr, "fit cluster", strings.Join(paths, ", "), err)
		return nil, false
	}
	a, err := checkAdminOf(set.JSON)
	if err == nil {
		var api *checkAPI
		if api, err = newCheckAPI(a, f.tls, *f.timeout); err == nil {
			return &clusterClient{api: api, stderr: stderr, cmd: cmd}, true
		}
	}
	fmt.Fprintf(stderr, "fit cluster %s: %s\n", cmd, strings.ReplaceAll(err.Error(), "fit check", "fit cluster"))
	return nil, false
}

// splitList splits a list separated by commas, without empty items.
func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseDays parses a duration, or whole days as "30d".
func parseDays(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		if _, err := fmt.Sscanf(d, "%d", &n); err != nil || n < 0 || fmt.Sprint(n) != d {
			return 0, fmt.Errorf("%q is not a number of days, as 30d", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%q is not a duration, as 72h, or days, as 30d", s)
	}
	return d, nil
}

func writeRaw(w io.Writer, out []byte) {
	_, _ = w.Write(out)
	if len(out) == 0 || out[len(out)-1] != '\n' {
		fmt.Fprintln(w)
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func labelsText(l map[string]string) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(l)) {
		parts = append(parts, k+"="+l[k])
	}
	return dash(strings.Join(parts, ","))
}

func (c *clusterClient) nodes(w io.Writer, raw bool) (int, error) {
	var b struct {
		ReadOnly string               `json:"read_only"`
		Nodes    []cluster.MemberView `json:"nodes"`
	}
	out, err := c.call("GET", "/nodes", nil, &b)
	if err != nil || raw {
		writeRawIf(w, out, raw)
		return 0, err
	}
	readOnlyNote(c.stderr, b.ReadOnly)
	if len(b.Nodes) == 0 {
		fmt.Fprintln(w, "no members yet")
		return 0, nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tURL\tACCESS\tLABELS")
	for _, n := range b.Nodes {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", n.Name, dash(n.URL), n.Access, labelsText(n.Labels))
	}
	return 0, tw.Flush()
}

func writeRawIf(w io.Writer, out []byte, raw bool) {
	if raw && out != nil {
		writeRaw(w, out)
	}
}

func readOnlyNote(w io.Writer, why string) {
	if why != "" {
		fmt.Fprintf(w, "fit cluster: the manager is read-only: %s\n", why)
	}
}

// state prints each member's state; it exits 1 when one is not up.
func (c *clusterClient) state(w io.Writer, raw bool) (int, error) {
	var b struct {
		ReadOnly string          `json:"read_only"`
		Nodes    []cluster.State `json:"nodes"`
	}
	out, err := c.call("GET", "/state", nil, &b)
	if err != nil {
		return 0, err
	}
	code := 0
	for _, s := range b.Nodes {
		if s.Status != cluster.StatusUp {
			code = 1
		}
	}
	if raw {
		writeRaw(w, out)
		return code, nil
	}
	readOnlyNote(c.stderr, b.ReadOnly)
	if len(b.Nodes) == 0 {
		fmt.Fprintln(w, "no members yet")
		return 0, nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSTATUS\tFIT\tCONFIG\tDRIFT\tNEAREST CERT\tCONNS\tLATENCY\tPROBLEM")
	for _, s := range b.Nodes {
		fit, cfg, drift, cert, conns := "-", "-", "-", "-", "-"
		if s.Node != nil {
			fit = dash(s.Node.FitVersion)
		}
		if s.Config != nil {
			cfg = fmt.Sprintf("v%d", s.Config.Version)
		}
		if s.Drift != nil {
			drift = fmt.Sprint(s.Drift.Count)
		}
		if s.Certificates != nil && !s.Certificates.NearestExpiry.IsZero() {
			cert = s.Certificates.NearestExpiry.UTC().Format("2006-01-02")
		}
		if s.Connections != nil {
			conns = fmt.Sprint(s.Connections.Open)
		}
		prob := "-"
		if s.Error != nil {
			prob = s.Error.Kind + ": " + oneLine(s.Error.Message, 80)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%dms\t%s\n", s.Name, s.Status, fit, cfg, drift, cert, conns, s.LatencyMs, prob)
	}
	return code, tw.Flush()
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func (c *clusterClient) groups(w io.Writer, raw bool) (int, error) {
	var b struct {
		Groups []cluster.GroupView `json:"groups"`
	}
	out, err := c.call("GET", "/groups", nil, &b)
	if err != nil || raw {
		writeRawIf(w, out, raw)
		return 0, err
	}
	if len(b.Groups) == 0 {
		fmt.Fprintln(w, "no groups yet")
		return 0, nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSELECTOR\tMEMBERS\tMISSING")
	for _, g := range b.Groups {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", g.Name, dash(g.Selector), dash(strings.Join(g.Nodes, ",")), dash(strings.Join(g.Missing, ",")))
	}
	return 0, tw.Flush()
}

// diff prints each member's parts that are not in sync with its
// templates; it exits 1 when one is not.
func (c *clusterClient) diff(w io.Writer, raw bool, nodes []string, template string) (int, error) {
	var b struct {
		Nodes   []cluster.MemberSync `json:"nodes"`
		Pending []string             `json:"pending"`
	}
	out, err := c.call("POST", "/sync", map[string]any{"nodes": nodes, "template": template}, &b)
	if err != nil {
		return 0, err
	}
	code := 0
	for _, s := range b.Nodes {
		if s.State != cluster.SyncInSync && s.State != cluster.SyncUnmanaged {
			code = 1
		}
	}
	if len(b.Pending) > 0 {
		code = 1
	}
	if raw {
		writeRaw(w, out)
		return code, nil
	}
	for _, s := range b.Nodes {
		fmt.Fprintf(w, "%s: %s", s.Node, s.State)
		if s.Error != nil {
			fmt.Fprintf(w, " (%s: %s)", s.Error.Kind, oneLine(s.Error.Message, 120))
		}
		fmt.Fprintln(w)
		for _, e := range s.Errors {
			fmt.Fprintf(w, "  error: %s\n", oneLine(e, 200))
		}
		for _, p := range s.Parts {
			if p.State == cluster.SyncInSync {
				continue
			}
			fmt.Fprintf(w, "  %s %s (template %s)", p.State, p.Path, p.Template)
			if p.Reason != "" {
				fmt.Fprintf(w, ": %s", oneLine(p.Reason, 120))
			}
			fmt.Fprintln(w)
			if len(p.Have) > 0 {
				fmt.Fprintf(w, "    - %s\n", oneLine(string(p.Have), 200))
			}
			if len(p.Want) > 0 {
				fmt.Fprintf(w, "    + %s\n", oneLine(string(p.Want), 200))
			}
		}
	}
	for _, n := range b.Pending {
		fmt.Fprintf(w, "%s: not read in time\n", n)
	}
	return code, nil
}

// certs prints the inventory; with within, it exits 1 when a certificate
// expires in it.
func (c *clusterClient) certs(w io.Writer, raw bool, within time.Duration) (int, error) {
	var b cluster.CertsBody
	out, err := c.call("GET", "/certificates", nil, &b)
	if err != nil {
		return 0, err
	}
	code := 0
	if within > 0 {
		for _, ce := range b.Certificates {
			if ce.NotAfter.Sub(b.Now) < within {
				code = 1
			}
		}
	}
	if raw {
		writeRaw(w, out)
		return code, nil
	}
	if len(b.Certificates) == 0 {
		fmt.Fprintln(w, "no member serves a certificate, as last read")
	} else {
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "EXPIRES\tDAYS\tNODE\tLISTENER\tNAMES\tISSUER")
		for _, ce := range b.Certificates {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", ce.NotAfter.UTC().Format(time.RFC3339), ce.DaysLeft, ce.Node, dash(ce.Listener),
				dash(strings.Join(ce.Names, ",")), dash(ce.Issuer))
		}
		if err := tw.Flush(); err != nil {
			return 0, err
		}
	}
	for _, n := range b.Nodes {
		if n.Read.IsZero() {
			fmt.Fprintf(w, "%s: its certificates were not read yet (%s)\n", n.Name, n.Status)
		}
	}
	return code, nil
}

func valueText(v []cluster.Value, f string) string {
	if len(v) == 0 || !v[len(v)-1].OK {
		return "-"
	}
	return fmt.Sprintf(f, v[len(v)-1].V)
}

func (c *clusterClient) metrics(w io.Writer, raw bool, nodes, groups []string) (int, error) {
	q := url.Values{"points": {"2"}}
	if len(nodes) > 0 {
		q.Set("node", strings.Join(nodes, ","))
	}
	if len(groups) > 0 {
		q.Set("group", strings.Join(groups, ","))
	}
	var b cluster.MetricsBody
	out, err := c.call("GET", "/metrics?"+q.Encode(), nil, &b)
	if err != nil || raw {
		writeRawIf(w, out, raw)
		return 0, err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tSTATUS\tREQ/S\t4XX/S\t5XX/S\tCONNS\tQUEUED BYTES\tREQUESTS")
	for _, n := range b.Nodes {
		s := &n.Series
		total := "-"
		if n.Counters != nil && n.Counters.Requests != nil {
			total = fmt.Sprint(n.Counters.Requests.Total)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", n.Name, n.Status, valueText(s.Requests, "%.2f"), valueText(s.Errors4xx, "%.2f"),
			valueText(s.Errors5xx, "%.2f"), valueText(s.Connections, "%.0f"), valueText(s.PendingBytes, "%.0f"), total)
	}
	for _, g := range b.Groups {
		s := &g.Series
		fmt.Fprintf(tw, "group %s\t%d members\t%s\t%s\t%s\t%s\t%s\t-\n", g.Name, len(g.Nodes), valueText(s.Requests, "%.2f"), valueText(s.Errors4xx, "%.2f"),
			valueText(s.Errors5xx, "%.2f"), valueText(s.Connections, "%.0f"), valueText(s.PendingBytes, "%.0f"))
	}
	return 0, tw.Flush()
}

// logs prints the members' logs merged by time, and with follow what
// comes next, until SIGINT; it exits 1 when a member could not be read.
func (c *clusterClient) logs(w io.Writer, raw bool, q url.Values, follow bool) (int, error) {
	var stop chan os.Signal
	if follow {
		stop = make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt)
		defer signal.Stop(stop)
	}
	code := 0
	reported := map[string]string{}
	for round := 1; ; round++ {
		var b cluster.TailBody
		out, err := c.call("GET", "/logs?"+q.Encode(), nil, &b)
		if err != nil {
			return 1, err
		}
		if raw {
			writeRaw(w, out)
		} else {
			for _, r := range b.Records {
				fmt.Fprintf(w, "%s %s %s %s: %s\n", r.Time.UTC().Format(time.RFC3339Nano), r.Node, strings.ToUpper(r.Level), r.Logger, r.Message)
			}
		}
		for _, n := range b.Nodes {
			msg := ""
			if n.Error != nil {
				code, msg = 1, n.Error.Kind+": "+oneLine(n.Error.Message, 200)
			}
			if reported[n.Name] != msg {
				if msg != "" {
					fmt.Fprintf(c.stderr, "fit cluster logs: %s: %s\n", n.Name, msg)
				}
				reported[n.Name] = msg
			}
		}
		if !follow || clusterFollowRounds > 0 && round >= clusterFollowRounds {
			return code, nil
		}
		q.Set("cursor", b.Cursor)
		select {
		case <-stop:
			return code, nil
		default:
		}
		clusterSleep(2 * time.Second)
	}
}

// ---- Rollouts ----

// runClusterRollout plans, starts, follows and steers rollouts.
func runClusterRollout(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "-help" || args[0] == "--help" {
		fmt.Fprint(stderr, clusterUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	sub := args[0]
	f := newClusterFlags("rollout "+sub, stderr)
	var o rolloutFlags
	wait := f.fs.Bool("wait", false, "follow the rollout until it ends, or waits for a person's go")
	switch sub {
	case "start":
		o.add(f.fs)
	case "list", "status", cluster.SteerPause, cluster.SteerResume, cluster.SteerGo, cluster.SteerAbort, cluster.SteerRollback:
	default:
		fmt.Fprintf(stderr, "fit cluster rollout: unknown command %q\n\n%s", sub, clusterUsage)
		return 2
	}
	if err := f.fs.Parse(args[1:]); err != nil {
		return 2
	}
	var id string
	switch {
	case sub == "list" || sub == "start":
		if f.fs.NArg() > 0 {
			fmt.Fprintf(stderr, "fit cluster rollout %s: unexpected argument %q\n", sub, f.fs.Arg(0))
			return 2
		}
	case f.fs.NArg() == 0:
		fmt.Fprintf(stderr, "fit cluster rollout %s: give the rollout's ID, as fit cluster rollout list shows it\n", sub)
		return 2
	default:
		rest := f.fs.Args()
		id = rest[0]
		if err := f.fs.Parse(rest[1:]); err != nil {
			return 2
		}
		if f.fs.NArg() > 0 {
			fmt.Fprintf(stderr, "fit cluster rollout %s: unexpected argument %q\n", sub, f.fs.Arg(0))
			return 2
		}
	}
	var req *cluster.RolloutRequest
	var start *cluster.StartRequest
	if sub == "start" {
		var err error
		if req, start, err = o.requests(); err != nil {
			fmt.Fprintf(stderr, "fit cluster rollout start: %v\n", err)
			return 2
		}
	}
	c, ok := f.client("rollout "+sub, stderr)
	if !ok {
		return 2
	}
	defer c.api.close()
	var code int
	var err error
	switch sub {
	case "list":
		code, err = c.rolloutList(stdout, *f.asJSON)
	case "status":
		code, err = c.rolloutStatus(stdout, *f.asJSON, id, *wait)
	case "start":
		code, err = c.rolloutStart(stdout, *f.asJSON, o.id, req, start, o.yes, *wait)
	default:
		var ro cluster.Rollout
		var out []byte
		if out, err = c.call("POST", "/rollouts/"+url.PathEscape(id)+"/"+sub, nil, &ro); err == nil {
			if *f.asJSON && !*wait {
				writeRaw(stdout, out)
			} else if !*f.asJSON {
				fmt.Fprintf(stdout, "rollout %s: %s\n", ro.ID, ro.State)
			}
			if *wait {
				code, err = c.rolloutStatus(stdout, *f.asJSON, id, true)
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit cluster rollout %s: %v\n", sub, err)
		return 1
	}
	return code
}

// rolloutFlags are fit cluster rollout start's.
type rolloutFlags struct {
	id, template, group, nodes, exclude, description string
	sync, yes                                        bool
	mode, onFailure                                  string
	batchSize, batchPercent, canary, minRequests     int
	pause, gateWait, window                          time.Duration
	max5xx                                           float64
}

func (o *rolloutFlags) add(fs *flag.FlagSet) {
	fs.StringVar(&o.id, "id", "", "start the rollout planned already with this ID, in place of planning one")
	fs.StringVar(&o.template, "template", "", "plan the rollout of this template's change to its members")
	fs.BoolVar(&o.sync, "sync", false, "plan a sync: members brought in sync with their templates")
	fs.StringVar(&o.group, "group", "", "the members of this group (a template's must be its groups')")
	fs.StringVar(&o.nodes, "node", "", "these members, separated by commas")
	fs.StringVar(&o.description, "description", "", "what the rollout is for, as the pages show it")
	fs.StringVar(&o.exclude, "exclude", "", "members of the plan to leave out, separated by commas")
	fs.StringVar(&o.mode, "mode", cluster.StrategyAll, "all, rolling or canary")
	fs.IntVar(&o.batchSize, "batch-size", 0, "rolling and canary: members a batch")
	fs.IntVar(&o.batchPercent, "batch-percent", 0, "rolling and canary: a batch as a percentage of the members")
	fs.DurationVar(&o.pause, "pause", 0, "rolling and canary: the pause between batches")
	fs.IntVar(&o.canary, "canary", 0, "canary: the members of the first batch, after which the rollout waits for fit cluster rollout go")
	fs.StringVar(&o.onFailure, "on-failure", cluster.OnFailureBatch, "what a failed gate rolls back: batch or all")
	fs.DurationVar(&o.gateWait, "gate-wait", cluster.DefaultGateWaitMs*time.Millisecond, "how long after a batch its gate checks its members")
	fs.Float64Var(&o.max5xx, "max-5xx", 0, "fail the gate when a member's share of 5xx over -window is above this, as 0.01")
	fs.DurationVar(&o.window, "window", 0, "with -max-5xx, the window its share is judged over (30s when left out)")
	fs.IntVar(&o.minRequests, "min-requests", 0, "with -max-5xx, the requests a window needs to be judged (20 when left out)")
	fs.BoolVar(&o.yes, "yes", false, "start without asking, once the plan is shown")
}

// requests returns the plan's request, nil for -id, and the start's.
func (o *rolloutFlags) requests() (*cluster.RolloutRequest, *cluster.StartRequest, error) {
	start := &cluster.StartRequest{Exclude: splitList(o.exclude), Strategy: cluster.Strategy{Mode: o.mode, BatchSize: o.batchSize,
		BatchPercent: o.batchPercent, PauseMs: o.pause.Milliseconds(), Canary: o.canary, OnFailure: o.onFailure,
		Gate: cluster.Gate{WaitMs: o.gateWait.Milliseconds(), Max5xxRatio: o.max5xx, WindowMs: o.window.Milliseconds(), MinRequests: o.minRequests}}}
	kinds := 0
	for _, set := range []bool{o.id != "", o.template != "", o.sync} {
		if set {
			kinds++
		}
	}
	switch {
	case kinds != 1:
		return nil, nil, errors.New("give one of -template, -sync and -id")
	case o.id != "" && (o.group != "" || o.nodes != "" || o.description != ""):
		return nil, nil, errors.New("-id starts a rollout planned already: -group, -node and -description plan one")
	case o.id != "":
		return nil, start, nil
	}
	req := &cluster.RolloutRequest{Kind: cluster.RolloutSync, Group: o.group, Nodes: splitList(o.nodes), Description: o.description}
	if o.template != "" {
		req.Kind, req.Template = cluster.RolloutTemplate, o.template
	}
	return req, start, nil
}

func (c *clusterClient) rolloutList(w io.Writer, raw bool) (int, error) {
	var b struct {
		Rollouts []cluster.Rollout `json:"rollouts"`
	}
	out, err := c.call("GET", "/rollouts", nil, &b)
	if err != nil || raw {
		writeRawIf(w, out, raw)
		return 0, err
	}
	if len(b.Rollouts) == 0 {
		fmt.Fprintln(w, "no rollouts yet")
		return 0, nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tKIND\tSTATE\tCREATED\tMEMBERS\tWHAT")
	for _, r := range b.Rollouts {
		what := r.Template
		if r.Kind == cluster.RolloutCopy {
			what = r.From + ": " + strings.Join(r.Paths, ",")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", r.ID, r.Kind, r.State, r.Created.UTC().Format(time.RFC3339), len(r.Members), dash(what))
	}
	return 0, tw.Flush()
}

// rolloutOver reports whether a rollout has ended, or waits for a person.
func rolloutOver(r *cluster.Rollout) bool {
	switch r.State {
	case cluster.RolloutPlanning, cluster.RolloutRunning, cluster.RolloutRollingBack:
		return false
	}
	return true
}

// rolloutCode is the exit code a rollout's state means.
func rolloutCode(r *cluster.Rollout) int {
	switch r.State {
	case cluster.RolloutFailed, cluster.RolloutAborted, cluster.RolloutRolledBack, cluster.RolloutExpired:
		return 1
	}
	return 0
}

// rolloutStatus prints a rollout, and with wait follows it until it ends.
func (c *clusterClient) rolloutStatus(w io.Writer, raw bool, id string, wait bool) (int, error) {
	var last cluster.Rollout
	var lastOut []byte
	shown := ""
	for {
		var r cluster.Rollout
		out, err := c.call("GET", "/rollouts/"+url.PathEscape(id), nil, &r)
		if err != nil {
			return 0, err
		}
		last, lastOut = r, out
		if wait && !raw {
			// A line when the rollout moves on.
			line := fmt.Sprintf("rollout %s: %s, batch %d of %d, %s", r.ID, r.State, min(r.Batch+1, max(len(r.Batches), 1)), len(r.Batches), countsText(r.Counts))
			if line != shown {
				fmt.Fprintln(w, line)
				shown = line
			}
		}
		if !wait || rolloutOver(&r) {
			break
		}
		clusterSleep(time.Second)
	}
	if raw {
		writeRaw(w, lastOut)
		return rolloutCode(&last), nil
	}
	printRollout(w, &last)
	return rolloutCode(&last), nil
}

func countsText(c cluster.RolloutCounts) string {
	var parts []string
	for _, k := range slices.Sorted(maps.Keys(c)) {
		parts = append(parts, fmt.Sprintf("%d %s", c[k], k))
	}
	return dash(strings.Join(parts, ", "))
}

// printRollout prints a rollout and each member's plan or outcome.
func printRollout(w io.Writer, r *cluster.Rollout) {
	fmt.Fprintf(w, "rollout %s (%s", r.ID, r.Kind)
	if r.Template != "" {
		fmt.Fprintf(w, " of %s", r.Template)
	}
	if r.Group != "" {
		fmt.Fprintf(w, ", group %s", r.Group)
	}
	fmt.Fprintf(w, "): %s", r.State)
	if r.Reason != "" {
		fmt.Fprintf(w, ": %s", oneLine(r.Reason, 200))
	}
	fmt.Fprintln(w)
	if r.State == cluster.RolloutWaiting {
		fmt.Fprintf(w, "the canary passed its gate: go on with fit cluster rollout go %s, or abort it\n", r.ID)
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NODE\tSTATUS\tVERSION\tCHANGES\tNOTE")
	for _, x := range r.Members {
		changed := x.Changed
		for _, ch := range x.Changes {
			if !slices.Contains(changed, ch.Path) {
				changed = append(changed, ch.Path)
			}
		}
		ver := "-"
		switch {
		case x.Applied != 0:
			ver = fmt.Sprintf("v%d → v%d", x.Version, x.Applied)
		case x.Version != 0:
			ver = fmt.Sprintf("v%d", x.Version)
		}
		if x.RolledBack != 0 {
			ver += fmt.Sprintf(" → v%d (rolled back)", x.RolledBack)
		}
		var notes []string
		for _, e := range x.Errors {
			notes = append(notes, dash(e.Path)+": "+e.Message)
		}
		if x.Error != nil {
			notes = append(notes, x.Error.Message)
		}
		if g := x.Gate; g != nil && !g.Passed && len(g.Reasons) > 0 {
			notes = append(notes, "gate: "+strings.Join(g.Reasons, "; "))
		}
		if x.Note != "" {
			notes = append(notes, x.Note)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", x.Node, x.Status, ver, dash(strings.Join(changed, ",")), dash(oneLine(strings.Join(notes, " | "), 160)))
	}
	_ = tw.Flush()
}

// rolloutPlanWait bounds how long rollout start waits for a plan.
var rolloutPlanWait = 5 * time.Minute

// rolloutStart plans a rollout, or takes the one id names, prints its
// plan, asks unless yes, starts it, and with wait follows it.
func (c *clusterClient) rolloutStart(w io.Writer, raw bool, id string, req *cluster.RolloutRequest, start *cluster.StartRequest, yes, wait bool) (int, error) {
	var r cluster.Rollout
	if req != nil {
		if _, err := c.call("POST", "/rollouts", req, &r); err != nil {
			return 0, err
		}
		id = r.ID
		if !raw {
			fmt.Fprintf(w, "planning rollout %s…\n", id)
		}
	}
	deadline := time.Now().Add(rolloutPlanWait)
	for {
		if _, err := c.call("GET", "/rollouts/"+url.PathEscape(id), nil, &r); err != nil {
			return 0, err
		}
		if r.State != cluster.RolloutPlanning {
			break
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("the rollout %s is still being planned after %s: follow it with fit cluster rollout status %s", id, rolloutPlanWait, id)
		}
		clusterSleep(500 * time.Millisecond)
	}
	if !raw {
		printRollout(w, &r)
	}
	if r.State != cluster.RolloutPlanned {
		return 1, fmt.Errorf("the rollout %s is %s: only a planned rollout starts", id, r.State)
	}
	ready := 0
	for _, x := range r.Members {
		if x.Status == cluster.MemberReady && !slices.Contains(start.Exclude, x.Node) {
			ready++
		}
	}
	if ready == 0 {
		return 1, fmt.Errorf("no member of the rollout %s has anything to apply: nothing was started", id)
	}
	if !yes {
		fmt.Fprintf(w, "Start the rollout %s on %d members, %s? [y/N] ", id, ready, start.Strategy.Mode)
		line, _ := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintf(w, "not started: the plan is kept until it expires; start it with fit cluster rollout start -id %s, or discard it on the Rollouts page\n", id)
			return 1, nil
		}
	}
	out, err := c.call("POST", "/rollouts/"+url.PathEscape(id)+"/start", start, &r)
	if err != nil {
		return 0, err
	}
	if !wait {
		if raw {
			writeRaw(w, out)
		} else {
			fmt.Fprintf(w, "rollout %s started: follow it with fit cluster rollout status -wait %s\n", id, id)
		}
		return 0, nil
	}
	return c.rolloutStatus(w, raw, id, true)
}

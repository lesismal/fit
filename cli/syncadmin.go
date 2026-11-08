package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

const syncAdminUsage = `Usage: fit sync <command> [flags] [<pipeline>]

Run one pipeline on its own:

	fit sync run [-c config]... [--once] <pipeline>     (fit sync run -h)

See and steer the pipelines of a running fit, through its Admin API:

	fit sync status [<pipeline>]           every pipeline's state, or one's in full
	fit sync pause <pipeline>              hold its source; what it read is written
	fit sync resume <pipeline>             let it go on; one stopped on an error
	                                       starts again from its checkpoint
	fit sync snapshot [-confirm] <pipeline>
	                                       read its source again from the start
	fit sync checkpoint <pipeline>         show its checkpoint
	fit sync checkpoint -set JSON [-confirm] <pipeline>
	fit sync checkpoint -reset [-confirm] <pipeline>
	                                       set it, or remove it, and restart the
	                                       pipeline from there
	fit sync test <pipeline>               test its connectors' connections
	fit sync dry-run [-limit N] [-wait D] [-from-start] <pipeline>
	                                       read its first records through its
	                                       transforms, writing nothing

A change of the checkpoint that would skip or replay records is refused
without -confirm. Every command takes -c, the bootstrap config whose admin
says where the Admin API is, -timeout, -json and the TLS flags of fit check.
Exits 1 when the Admin API refuses, or a connection test fails.
`

// syncFlags are the flags every fit sync command of the Admin API takes.
type syncFlags struct {
	fs       *flag.FlagSet
	cfgPaths configPaths
	timeout  *time.Duration
	tls      checkTLS
	asJSON   *bool
}

func newSyncFlags(cmd string, stderr io.Writer) *syncFlags {
	f := &syncFlags{fs: flag.NewFlagSet("sync "+cmd, flag.ContinueOnError)}
	f.fs.SetOutput(stderr)
	f.fs.Usage = func() { fmt.Fprint(stderr, syncAdminUsage) }
	f.fs.Var(&f.cfgPaths, "c", "the bootstrap config file, or a directory of them, fit was started with, whose admin says where its Admin API is (fit.json when none is given); give -c again for more, which are merged")
	f.timeout = f.fs.Duration("timeout", 90*time.Second, "how long fit sync waits for the Admin API")
	f.fs.StringVar(&f.tls.CA, "ca", "", "with admin.cert_file, a PEM file with the CAs the Admin API's certificate is verified with")
	f.fs.StringVar(&f.tls.ServerName, "server-name", "", "with admin.cert_file, the name the Admin API's certificate is verified for")
	f.fs.StringVar(&f.tls.Cert, "cert", "", "with admin.client_ca_file, a PEM file with the client certificate fit sync presents")
	f.fs.StringVar(&f.tls.Key, "key", "", "the private key of -cert")
	f.asJSON = f.fs.Bool("json", false, "print the Admin API's answer as JSON")
	return f
}

// api reaches the Admin API the bootstrap config names.
func (f *syncFlags) api(stderr io.Writer) (*checkAPI, bool) {
	paths := f.cfgPaths.orDefault()
	set, _, err := loadSet(paths)
	if err != nil {
		printConfigError(stderr, "fit sync", strings.Join(paths, ", "), err)
		return nil, false
	}
	a, err := checkAdminOf(set.JSON)
	if err == nil {
		var api *checkAPI
		if api, err = newCheckAPI(a, f.tls, *f.timeout); err == nil {
			return api, true
		}
	}
	fmt.Fprintf(stderr, "fit sync: %s\n", strings.ReplaceAll(err.Error(), "fit check", "fit sync"))
	return nil, false
}

// syncAdmin runs the fit sync commands that go through the Admin API.
func syncAdmin(cmd string, args []string, stdout, stderr io.Writer) int {
	f := newSyncFlags(cmd, stderr)
	confirm, set, reset, limit, wait, fromStart := new(bool), new(string), new(bool), new(int), new(time.Duration), new(bool)
	switch cmd {
	case "snapshot":
		f.fs.BoolVar(confirm, "confirm", false, "read the source again although the sinks have its records")
	case "checkpoint":
		f.fs.BoolVar(confirm, "confirm", false, "set or reset the checkpoint although that skips or replays records")
		f.fs.StringVar(set, "set", "", "the position to set, JSON in the source's own form, as fit sync checkpoint shows it")
		f.fs.BoolVar(reset, "reset", false, "remove the checkpoint: the source starts where its params say")
	case "dry-run":
		f.fs.IntVar(limit, "limit", 0, fmt.Sprintf("how many source records to read (%d when 0, at most %d)", fitsync.DefaultDryRunRecords, fitsync.MaxDryRunRecords))
		f.fs.DurationVar(wait, "wait", 0, fmt.Sprintf("how long to wait for them (%s when 0, at most %s)", fitsync.DefaultDryRunTimeout, fitsync.MaxDryRunTimeout))
		f.fs.BoolVar(fromStart, "from-start", false, "read from where the source starts, not from the checkpoint")
	}
	var names []string
	for rest := args; ; {
		if err := f.fs.Parse(rest); err != nil {
			return 2
		}
		if f.fs.NArg() == 0 {
			break
		}
		names = append(names, f.fs.Arg(0))
		rest = f.fs.Args()[1:]
	}
	switch {
	case len(names) > 1, len(names) == 0 && cmd != "status":
		fmt.Fprintf(stderr, "fit sync %s: name one pipeline\n", cmd)
		return 2
	case *set != "" && *reset:
		fmt.Fprintf(stderr, "fit sync checkpoint: give -set or -reset, not both\n")
		return 2
	case *set != "" && !json.Valid([]byte(*set)):
		fmt.Fprintf(stderr, "fit sync checkpoint: -set %s is not JSON\n", *set)
		return 2
	}
	api, ok := f.api(stderr)
	if !ok {
		return 2
	}
	defer api.close()
	path := "/sync/pipelines"
	if len(names) == 1 {
		path += "/" + url.PathEscape(names[0])
	}
	method := http.MethodGet
	var body any
	switch cmd {
	case "pause", "resume":
		method, body = http.MethodPost, map[string]any{"action": cmd}
	case "snapshot":
		method, body = http.MethodPost, map[string]any{"action": cmd, "confirm": *confirm}
	case "checkpoint":
		path += "/checkpoint"
		switch {
		case *set != "":
			method, body = http.MethodPut, map[string]any{"position": json.RawMessage(*set), "confirm": *confirm}
		case *reset:
			method = http.MethodDelete
			if *confirm {
				path += "?confirm=1"
			}
		}
	case "test":
		method, path = http.MethodPost, path+"/test"
	case "dry-run":
		method, path = http.MethodPost, path+"/dry-run"
		b := map[string]any{"limit": *limit}
		if *wait > 0 {
			b["timeout"] = wait.String()
		}
		if *fromStart {
			b["from"] = "start"
		}
		body = b
	}
	var data []byte
	if body != nil {
		data, _ = json.Marshal(body) // strings, numbers and JSON already checked
	}
	out, status, err := api.do(method, path, data, "")
	if err == nil && status != http.StatusOK {
		err = api.refused(status, out)
		if status == http.StatusConflict {
			err = fmt.Errorf("%w; give -confirm to go ahead", err)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit sync %s: %v\n", cmd, err)
		return 1
	}
	if *f.asJSON {
		_, _ = stdout.Write(out)
		return 0
	}
	return printSync(cmd, out, stdout, stderr)
}

// printSync prints the Admin API's answer to cmd.
func printSync(cmd string, out []byte, stdout, stderr io.Writer) int {
	switch cmd {
	case "status":
		var all struct {
			Pipelines []json.RawMessage `json:"pipelines"`
		}
		if err := json.Unmarshal(out, &all); err == nil && all.Pipelines != nil {
			printSyncTable(stdout, all.Pipelines)
			return 0
		}
		printSyncStatus(stdout, out)
	case "pause", "resume", "snapshot":
		printSyncStatus(stdout, out)
	case "checkpoint":
		var c struct {
			Checkpoint *fitsync.Checkpoint `json:"checkpoint"`
			// A reset answers with the pipeline's state.
			State string `json:"state"`
		}
		_ = json.Unmarshal(out, &c)
		switch {
		case c.Checkpoint != nil:
			fmt.Fprintf(stdout, "position %s\nsaved    %s\n", c.Checkpoint.Position, c.Checkpoint.Saved.Format(time.RFC3339))
			if j := c.Checkpoint.Job; j != nil {
				fmt.Fprintf(stdout, "job      %d read, %d written, %d failed\n", j.Read, j.Written, j.Failed)
			}
		case c.State != "":
			fmt.Fprintf(stdout, "the checkpoint is removed; the pipeline is %s\n", c.State)
		default:
			fmt.Fprintln(stdout, "no checkpoint yet")
		}
	case "test":
		var t struct {
			OK      bool                 `json:"ok"`
			Results []fitsync.TestResult `json:"results"`
		}
		_ = json.Unmarshal(out, &t)
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PART\tMODULE\tRESULT")
		for _, r := range t.Results {
			res := "ok"
			switch {
			case !r.Tested:
				res = "not tested: " + r.Err
			case !r.OK:
				res = "failed: " + r.Err
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Part, r.Module, res)
		}
		_ = tw.Flush()
		if !t.OK {
			fmt.Fprintln(stderr, "fit sync test: a connection test failed")
			return 1
		}
	case "dry-run":
		var d fitsync.DryRun
		_ = json.Unmarshal(out, &d)
		for _, r := range d.Records {
			fmt.Fprintf(stdout, "in   %s\n", r.In)
			if r.Err != "" {
				fmt.Fprintf(stdout, "  error %s\n", r.Err)
			}
			for _, o := range r.Out {
				fmt.Fprintf(stdout, "  out %s\n", o)
			}
		}
		end := fmt.Sprintf("%d records read", len(d.Records))
		switch {
		case d.Ended:
			end += ", to the source's end"
		case d.TimedOut:
			end += ", all there were before the wait ended"
		}
		fmt.Fprintln(stdout, end)
		if d.SourceErr != "" {
			fmt.Fprintf(stderr, "fit sync dry-run: the source failed: %s\n", d.SourceErr)
			return 1
		}
	}
	return 0
}

// syncStatus is what printing a pipeline's state reads of it.
type syncStatus struct {
	fitsync.Status
	Checkpoint json.RawMessage `json:"checkpoint"`
}

func printSyncTable(w io.Writer, pipelines []json.RawMessage) {
	if len(pipelines) == 0 {
		fmt.Fprintln(w, "no pipeline runs")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PIPELINE\tSTATE\tREAD\tACKED\tFAILED\tRATE/S\tLAG\tQUEUE\tERRORS")
	for _, raw := range pipelines {
		var s syncStatus
		_ = json.Unmarshal(raw, &s)
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%.1f\t%s\t%d\t%d\n", s.Name, s.State, s.Read, s.Acknowledged, s.Failed, s.Throughput,
			lagText(s.LagSeconds), s.Queue.Records, s.Errors)
	}
	_ = tw.Flush()
}

func lagText(sec float64) string {
	if sec == 0 {
		return "-"
	}
	return (time.Duration(sec * float64(time.Second))).Round(time.Millisecond).String()
}

func printSyncStatus(w io.Writer, raw []byte) {
	var s syncStatus
	_ = json.Unmarshal(raw, &s)
	fmt.Fprintf(w, "pipeline   %s\nstate      %s\n", s.Name, s.State)
	if len(s.Checkpoint) > 0 {
		fmt.Fprintf(w, "checkpoint %s\n", s.Checkpoint)
	}
	fmt.Fprintf(w, "records    %d read, %d acknowledged, %d failed, %.1f/s\n", s.Read, s.Acknowledged, s.Failed, s.Throughput)
	fmt.Fprintf(w, "queue      %d of %d records, %d of %d bytes\n", s.Queue.Records, s.Queue.MaxRecords, s.Queue.Bytes, s.Queue.MaxBytes)
	if s.LagSeconds > 0 {
		fmt.Fprintf(w, "lag        %s, last commit %s\n", lagText(s.LagSeconds), s.LastCommit.Format(time.RFC3339))
	}
	if j := s.Job; j != nil {
		fmt.Fprintf(w, "job        %d read, %d written, %d failed", j.Read, j.Written, j.Failed)
		if len(j.Chunks) > 0 {
			fmt.Fprintf(w, ", %d of %d chunks", j.ChunksDone, len(j.Chunks))
		}
		if j.ETA != "" {
			fmt.Fprintf(w, ", ETA %s", j.ETA)
		}
		fmt.Fprintln(w)
	}
	if !s.Next.IsZero() {
		fmt.Fprintf(w, "next       %s\n", s.Next.Format(time.RFC3339))
	}
	if s.Errors > 0 || s.Err != "" {
		fmt.Fprintf(w, "errors     %d, the last: %s\n", s.Errors, strings.TrimSpace(s.Err))
	}
}

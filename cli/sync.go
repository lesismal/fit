package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	fitsync "github.com/lesismal/fit/sync"
)

const syncUsage = `Usage:

	fit sync run [-c config]... [--once] <pipeline>

Runs one pipeline of the config's sync, on its own, without serving the
config's listeners or anything else, until it ends or SIGINT or SIGTERM
stops it, its checkpoint saved. --once runs it as a job (mode once) even
when the config says stream: its source is read to its end, a snapshot in
chunks where the source splits one, and the command exits. A job that
finished before starts anew; one interrupted goes on from its checkpoint.
A schedule is not waited for. The log goes to stderr, and the job's
progress to stdout at its end.

Exits 0 when every record was written; 3 when the job ended but records
failed (on_error skip or dead_letter), their count on stderr; 1 when the
pipeline stopped (on_error stop, a source failing past retry.max_attempts)
or was interrupted while a job; 2 when used wrongly.
`

// syncFailedExit is fit sync run's exit code when records failed.
const syncFailedExit = 3

func runSync(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "run" {
		if len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help") {
			fmt.Fprint(stdout, syncUsage)
			return 0
		}
		fmt.Fprint(stderr, syncUsage)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return syncRun(ctx, args[1:], stdout, stderr)
}

// syncRun is fit sync run, until ctx ends.
func syncRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("sync run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, syncUsage); flags.PrintDefaults() }
	var cfgPaths configPaths
	flags.Var(&cfgPaths, "c", fmt.Sprintf(configFlagUsage, "take the pipeline from"))
	once := flags.Bool("once", false, "run the pipeline as a job, to its source's end, and exit")
	var names []string
	for rest := args; ; {
		if err := flags.Parse(rest); err != nil {
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		names = append(names, flags.Arg(0))
		rest = flags.Args()[1:]
	}
	if len(names) != 1 {
		fmt.Fprintf(stderr, "fit sync run: name one pipeline, not %d\n", len(names))
		return 2
	}
	name := names[0]
	paths := cfgPaths.orDefault()
	file := strings.Join(paths, ", ")
	set, _, err := loadSet(paths)
	var c *config.Config
	if err == nil {
		c, err = config.Load(set.JSON)
		err = set.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, "fit sync run", file, err)
		return 1
	}
	if c.Sync == nil {
		fmt.Fprintf(stderr, "fit sync run: %s has no sync\n", file)
		return 1
	}
	s := *c.Sync
	s.Pipelines = append([]config.SyncPipeline(nil), s.Pipelines...)
	for i := range s.Pipelines {
		if p := &s.Pipelines[i]; p.Name == name && *once && !p.Once() {
			p.Mode, p.Job = config.SyncOnce, nil
		}
	}
	root, err := logs.New(nil, stderr, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "fit sync run: %v\n", err)
		return 1
	}
	defer root.Close()
	started := time.Now()
	attempts := 0
	for _, p := range s.Pipelines {
		if p.Name == name {
			attempts = p.Attempts()
		}
	}
	st, err := fitsync.RunPipeline(ctx, &s, name, fitsync.Options{Log: root.Logger(config.LoggerSync), SourceAttempts: attempts})
	if err != nil {
		printConfigError(stderr, "fit sync run", file, set.MapErrors(err))
		return 1
	}
	took := time.Since(started).Round(time.Millisecond)
	if j := st.Job; j != nil {
		chunks := ""
		if len(j.Chunks) > 0 {
			chunks = fmt.Sprintf(", %d of %d chunks", j.ChunksDone, len(j.Chunks))
		}
		fmt.Fprintf(stdout, "pipeline %s: %s: %d read, %d written, %d failed%s, in %s\n", name, st.State, j.Read, j.Written, j.Failed, chunks, took)
	} else {
		fmt.Fprintf(stdout, "pipeline %s: %s, in %s\n", name, st.State, took)
	}
	switch {
	case st.State == "stopped":
		fmt.Fprintf(stderr, "fit sync run: pipeline %s stopped: %s\n", name, st.Err)
		return 1
	case st.State == "finished" && st.Job != nil && st.Job.Failed > 0:
		fmt.Fprintf(stderr, "fit sync run: %d records failed\n", st.Job.Failed)
		return syncFailedExit
	case st.State == "finished" || st.Job == nil:
		return 0
	}
	fmt.Fprintf(stderr, "fit sync run: the job was interrupted; run it again to go on from its checkpoint\n")
	return 1
}

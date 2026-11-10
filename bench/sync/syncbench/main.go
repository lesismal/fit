// Command syncbench is the sync benchmark's driver (bench/sync/README.md).
// bench/sync/run.sh starts the servers and each mover (fit, Filebeat,
// Promtail, Alloy, Canal, Debezium, DataX, ...) in Docker, and runs syncbench beside them, in a
// container on the same private network, for what is measured:
//
//	syncbench prepare -scenario S [flags]   tables, indices, files, a batch job's rows
//	syncbench measure -scenario S [flags]   the load, the count at the sink, CPU and memory
//	syncbench merge -out F [flags] R...     rounds into one results file for benchreport
//
// measure writes the records a mover is to move, counts them where the
// mover writes them, and samples the mover's containers through the Docker
// API: their CPU while they drain the records, and their peak anonymous
// memory. With -pause the mover's containers are paused while the records
// are written, so that it then reads a backlog at its own pace rather than
// at the writer's. A steady phase then writes at a fixed rate and measures
// the lag from each write's commit to the sink's count reaching it. fit's
// CPU and heap profiles are saved with -pprof.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// run is the command: its subcommand, then that's flags.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: syncbench prepare|measure|merge [flags]")
		return 2
	}
	var err error
	switch args[0] {
	case "prepare", "measure":
		var o *options
		if o, err = parseOptions(args[0], args[1:], stderr); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		if args[0] == "prepare" {
			err = prepare(ctx, o)
		} else {
			err = measureCmd(ctx, o, stdout)
		}
	case "merge":
		err = mergeCmd(args[1:], stdout, stderr)
		if err == flag.ErrHelp {
			return 0
		}
		if err == errUsage {
			return 2
		}
	default:
		fmt.Fprintf(stderr, "syncbench: unknown command %q\n", args[0])
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "syncbench:", err)
		return 1
	}
	return 0
}

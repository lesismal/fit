package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

func runRun(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, stop, args, stdout, stderr)
}

// serve runs the config args name until ctx ends, then shuts down. It calls
// stopSignals once shutdown begins, so that a second signal kills the
// process rather than waiting for the shutdown.
func serve(ctx context.Context, stopSignals func(), args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("c", "fit.json", "the config file to run")
	grace := flags.Duration("shutdown-timeout", 30*time.Second, "how long shutdown waits for requests in flight before closing their connections")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit run: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	c, err := config.LoadFile(*file)
	if err != nil {
		printConfigError(stderr, "fit run", *file, err)
		return 1
	}
	// Upstream connections are dialed through an engine of their own, since
	// the listeners' engine is made from the compiled graph. It stops after
	// the graph is released, which closes the pools.
	engine, stopEngine, err := upstream.StartEngine()
	if err != nil {
		fmt.Fprintf(stderr, "fit run: %v\n", err)
		return 1
	}
	defer stopEngine()
	g, err := fitruntime.Compile(c, fitruntime.Env{Engine: engine})
	if err != nil {
		printConfigError(stderr, "fit run", *file, err)
		return 1
	}
	rt := new(fitruntime.Runtime)
	rt.Swap(g)
	defer rt.Close()

	s, err := listener.Start(rt)
	if err != nil {
		fmt.Fprintf(stderr, "fit run: %v\n", err)
		return 1
	}
	for _, a := range s.Addrs() {
		fmt.Fprintf(stdout, "fit: listener %q on %s (%s)\n", a.Name, a.Addr, strings.Join(a.Protocols, ", "))
	}

	<-ctx.Done()
	stopSignals()
	fmt.Fprintln(stdout, "fit: shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), *grace)
	defer cancel()
	if err := s.Shutdown(sctx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintf(stderr, "fit run: shutdown took longer than %s; connections still open were closed\n", *grace)
		} else {
			fmt.Fprintf(stderr, "fit run: %v\n", err)
		}
		return 1
	}
	fmt.Fprintln(stdout, "fit: stopped")
	return 0
}

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
	"github.com/lesismal/fit/observe"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

func runRun(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	reload := make(chan struct{}, 1)
	go func() {
		for range hup {
			select {
			case reload <- struct{}{}:
			default: // one is pending already
			}
		}
	}()
	return serve(ctx, stop, reload, args, stdout, stderr)
}

// serve runs the config args name until ctx ends, then shuts down, and
// reloads the config each time reload delivers. It calls stopSignals once
// shutdown begins, so that a second signal kills the process rather than
// waiting for the shutdown.
func serve(ctx context.Context, stopSignals func(), reload <-chan struct{}, args []string, stdout, stderr io.Writer) int {
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
	// Upstream connections are dialed through an engine of their own, so
	// that a proxied response waiting for a slow client waits on that
	// engine's pool, not the listeners'. It stops after the graph is
	// released, which closes the pools.
	engine, stopEngine, err := upstream.StartEngine()
	if err != nil {
		fmt.Fprintf(stderr, "fit run: %v\n", err)
		return 1
	}
	defer stopEngine()
	env := fitruntime.Env{Engine: engine}
	if c.AccessLog != nil {
		if env.AccessLog, err = observe.NewAccessLog(c.AccessLog); err != nil {
			fmt.Fprintf(stderr, "fit run: access log: %v\n", err)
			return 1
		}
		// Closed after the graph, so that every response finished has its
		// line written.
		defer env.AccessLog.Close()
	}
	g, err := fitruntime.Compile(c, env)
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
	s.SetDrainTimeout(*grace)
	for _, a := range s.Addrs() {
		printListener(stdout, a)
	}

	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-reload:
			reloadConfig(*file, c, env, rt, s, stdout, stderr)
		}
	}
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

func printListener(w io.Writer, a listener.Addr) {
	fmt.Fprintf(w, "fit: listener %q on %s (%s)\n", a.Name, a.Addr, strings.Join(a.Protocols, ", "))
}

// reloadConfig loads and compiles file and moves the server to it. On any
// failure the running config stays, and the error goes to stderr. first is
// the config fit started with, whose access log stays until a restart.
func reloadConfig(file string, first *config.Config, env fitruntime.Env, rt *fitruntime.Runtime, s *listener.Server, stdout, stderr io.Writer) {
	fmt.Fprintf(stdout, "fit: reloading %s\n", file)
	c, err := config.LoadFile(file)
	var g *fitruntime.Graph
	if err == nil {
		g, err = fitruntime.Compile(c, env)
	}
	if err != nil {
		fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
		printConfigError(stderr, "fit run", file, err)
		return
	}
	added, removed, err := s.Reload(g)
	if err != nil {
		fmt.Fprintf(stderr, "fit: reload failed; the running config stays: %v\n", err)
		return
	}
	for _, a := range removed {
		fmt.Fprintf(stdout, "fit: listener %q on %s removed\n", a.Name, a.Addr)
	}
	for _, a := range added {
		printListener(stdout, a)
	}
	if !sameAccessLog(first.AccessLog, c.AccessLog) {
		fmt.Fprintln(stderr, "fit: access_log changed; the change takes effect when fit restarts")
	}
	version := uint64(0)
	if cur := rt.Current(); cur != nil {
		version = cur.Version
		cur.Release()
	}
	fmt.Fprintf(stdout, "fit: reloaded %s (config version %d)\n", file, version)
}

func sameAccessLog(a, b *config.AccessLog) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Output == b.Output && a.FlushInterval == b.FlushInterval && a.BufferBytes == b.BufferBytes
}

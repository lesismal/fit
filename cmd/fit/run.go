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

	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/provider/file"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/tlsmgr"
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
	cfgFile := flags.String("c", "fit.json", "the config file to run")
	grace := flags.Duration("shutdown-timeout", 30*time.Second, "how long shutdown waits for requests in flight before closing their connections, and how long requests may keep using a config after a reload")
	watch := flags.Bool("watch", false, "apply the config file again each time it changes, as SIGHUP does")
	watchInterval := flags.Duration("watch-interval", file.DefaultInterval, "with -watch, how often the file is looked at")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit run: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	data, err := os.ReadFile(*cfgFile)
	var c *config.Config
	if err == nil {
		c, err = config.Load(data)
	}
	if err != nil {
		printConfigError(stderr, "fit run", *cfgFile, err)
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
	// The certificate manager is there whether or not the config lists
	// names for it, so that a reload may add some; with none it only waits.
	if env.ACME, err = tlsmgr.NewACME(c.ACME, nil, func(format string, args ...any) {
		fmt.Fprintf(stderr, "fit: "+format+"\n", args...)
	}); err != nil {
		printConfigError(stderr, "fit run", *cfgFile, err)
		return 1
	}
	defer env.ACME.Close()
	g, err := fitruntime.Compile(c, env)
	if err != nil {
		printConfigError(stderr, "fit run", *cfgFile, err)
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
	var keep int
	if c.Admin != nil {
		keep = c.Admin.HistoryLen()
	}
	configs := admin.NewConfigs(rt, s, env, c, data, admin.SourceFile, keep)
	var as *admin.Server
	if c.Admin != nil {
		as, err = admin.Start(c.Admin, configs, func(r admin.Result) {
			printApplied(stdout, stderr, r)
			fmt.Fprintf(stdout, "fit: loaded a config from the admin API (config version %d)\n", r.Version)
		})
		if err != nil {
			fmt.Fprintf(stderr, "fit run: %v\n", err)
			_ = s.Close()
			return 1
		}
		fmt.Fprintf(stdout, "fit: admin API on %s\n", as.Addr())
	}

	var watcher *file.Watcher
	stopWatch := func() {}
	if *watch {
		watcher = file.New(*cfgFile, data, file.Options{Interval: *watchInterval}, func(data []byte) error {
			fmt.Fprintf(stdout, "fit: %s changed; reloading\n", *cfgFile)
			applyFile(*cfgFile, data, configs, stdout, stderr)
			return nil // applyFile reported it
		}, func(err error) {
			fmt.Fprintf(stderr, "fit: watching %s: %v\n", *cfgFile, err)
		})
		wctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			watcher.Run(wctx)
		}()
		stopWatch = func() {
			cancel()
			<-done
		}
		fmt.Fprintf(stdout, "fit: watching %s\n", *cfgFile)
	}

	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-reload:
			reloadConfig(*cfgFile, configs, watcher, stdout, stderr)
		}
	}
	stopSignals()
	// No config is applied while the server shuts down.
	stopWatch()
	fmt.Fprintln(stdout, "fit: shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), *grace)
	defer cancel()
	if as != nil {
		// First, so that no config is applied while the listeners shut down.
		_ = as.Shutdown(sctx)
	}
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

// reloadConfig loads the config file at path and applies it. On any failure
// the running config stays, and the error goes to stderr. A watcher of the
// file, when there is one, is told, so that it does not apply the same again.
func reloadConfig(path string, configs *admin.Configs, watcher *file.Watcher, stdout, stderr io.Writer) {
	fmt.Fprintf(stdout, "fit: reloading %s\n", path)
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
		printConfigError(stderr, "fit run", path, err)
		return
	}
	if watcher != nil {
		watcher.Applied(data)
	}
	applyFile(path, data, configs, stdout, stderr)
}

// applyFile applies data, read from the file at path, and says what
// happened.
func applyFile(path string, data []byte, configs *admin.Configs, stdout, stderr io.Writer) {
	r, err := configs.Apply(data, admin.SourceFile)
	if err != nil {
		var errs config.Errors
		if errors.As(err, &errs) {
			fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
			printConfigError(stderr, "fit run", path, err)
		} else {
			fmt.Fprintf(stderr, "fit: reload failed; the running config stays: %v\n", err)
		}
		return
	}
	printApplied(stdout, stderr, r)
	fmt.Fprintf(stdout, "fit: reloaded %s (config version %d)\n", path, r.Version)
}

// printApplied prints what applying a config changed.
func printApplied(stdout, stderr io.Writer, r admin.Result) {
	for _, a := range r.Removed {
		fmt.Fprintf(stdout, "fit: listener %q on %s removed\n", a.Name, a.Addr)
	}
	for _, a := range r.Added {
		printListener(stdout, a)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(stderr, "fit: %s\n", w)
	}
}

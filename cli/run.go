package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/admin"
	"github.com/lesismal/fit/admin/store"
	"github.com/lesismal/fit/app"
	appconsole "github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/cluster"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/provider/file"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/tlsmgr"
	"github.com/lesismal/fit/upstream"
	"golang.org/x/crypto/bcrypt"
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

// serve runs the config args name, from one file or several, until ctx
// ends, then shuts down, and reloads the config each time reload delivers. It calls stopSignals once
// shutdown begins, so that a second signal kills the process rather than
// waiting for the shutdown.
func serve(ctx context.Context, stopSignals func(), reload <-chan struct{}, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfgPaths configPaths
	flags.Var(&cfgPaths, "c", fmt.Sprintf(configFlagUsage, "run"))
	grace := flags.Duration("shutdown-timeout", 30*time.Second, "how long shutdown waits for requests in flight before closing their connections, and how long requests may keep using a config after a reload")
	watch := flags.Bool("watch", false, "apply the config again each time one of its files changes, or a directory's files are added or taken out, as SIGHUP does")
	watchInterval := flags.Duration("watch-interval", file.DefaultInterval, "how often watched files are looked at: the config's, with -watch, and certificate files, with -watch-certs")
	watchCerts := flags.Bool("watch-certs", true, "apply the running config again each time the certificate or key files it loaded change")
	adminOn := flags.Bool("admin", false, "serve the Admin API and the console with their defaults where the config leaves them out, and keep the config in a store; with no console user in the config, the user admin logs in with the password admin, which must be changed within -timeout")
	timeout := flags.Duration("timeout", 10*time.Minute, "with -admin, how long the console's default password, admin, works: it must be changed within that time, before the console or the Admin API can be used, or fit must be restarted")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit run: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	timeoutSet := false
	flags.Visit(func(f *flag.Flag) { timeoutSet = timeoutSet || f.Name == "timeout" })
	if timeoutSet && !*adminOn {
		fmt.Fprintln(stderr, "fit run: -timeout is the default password's time, which needs -admin")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "fit run: -timeout must be more than 0")
		return 2
	}
	started := time.Now()

	paths := cfgPaths.orDefault()
	cfgName := strings.Join(paths, ", ")
	// The config files, a .store among them aside, and the store that goes
	// with them: the one admin.store names, a .store given with -c, or the
	// one beside the files, when it exists or -admin is given. With a
	// store, the config runs from what it holds merged onto the files'.
	set, srcs, spec, err := loadBoot(paths, len(cfgPaths) > 0, *adminOn)
	var boot *storeBoot
	var tempUser string
	if err == nil {
		var std []byte
		if std, err = config.Standardize(set.JSON); err == nil && *adminOn {
			std, tempUser, err = withDefaultAdmin(std)
		}
		if err == nil {
			boot, err = openStore(std, spec)
		}
	}
	mapErrors := set.MapErrors
	var c *config.Config
	if err == nil {
		if boot != nil && !boot.seeded {
			cfgName, mapErrors = boot.st.Path(), func(err error) error { return err }
			c, err = config.Load(boot.full())
		} else if boot != nil {
			// The files' config, with the admin -admin may have added:
			// its errors are mapped to the files by their paths.
			c, err = config.Load(boot.full())
			err = mapErrors(err)
		} else {
			c, err = config.Load(set.JSON)
			err = mapErrors(err)
		}
	}
	if err == nil && boot == nil && c.Admin != nil && c.Admin.Cluster != nil {
		err = errors.New("admin.cluster: a manager keeps its members' credentials in a store: set admin.store, or run with -admin, which keeps one beside the config")
	}
	if err == nil && boot != nil && *watch {
		err = fmt.Errorf("-watch watches config files, but the config is kept in the store %s; send SIGHUP to read the store again", boot.st.Path())
	}
	if err != nil {
		printConfigError(stderr, "fit run", cfgName, err)
		return 1
	}
	// fit's own log opens before the engine starts, so that fib's
	// messages go to it too; from here on what serve prints goes to it, as
	// the server logger's.
	root, err := logs.New(c.Log, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "fit run: log: %v\n", err)
		return 1
	}
	defer root.Close()
	logs.SetDefault(root)
	defer logs.SetDefault(nil)
	server := root.Logger(config.LoggerServer)
	for _, out := range c.Log.FileOutputs() {
		if out.Rotate == nil {
			server.Warnf("log file %s has no rotate: it will grow without bound", out.Path)
		}
	}
	stdout, stderr = logWriter{server, logs.LevelInfo}, logWriter{server, logs.LevelError}
	printWarnings(warnWriter(stderr), set)
	data := set.JSON
	if boot != nil {
		data = boot.base
		if boot.seeded {
			if err := boot.st.Write(boot.base); err != nil {
				fmt.Fprintf(stderr, "fit run: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "fit: the config of %s is kept in the store %s from now on\n", strings.Join(paths, ", "), boot.st.Path())
		} else if boot.merged {
			if err := boot.st.Write(boot.base); err != nil {
				fmt.Fprintf(stderr, "fit run: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "fit: the store %s merged onto %s, the store's values winning\n", boot.st.Path(), strings.Join(paths, ", "))
		}
	}
	// Upstream connections are dialed through an engine of their own, so
	// that a proxied response waiting for a slow client waits on that
	// engine's pool, not the listeners'. It stops after the graph is
	// released, which closes the pools.
	engine, stopEngine, err := upstream.StartEngineWith(c.Engine)
	if err != nil {
		fmt.Fprintf(stderr, "fit run: %v\n", err)
		return 1
	}
	defer stopEngine()
	env := fitruntime.Env{Engine: engine, Log: root}
	telemetry := c.Telemetry
	if c.Admin != nil || telemetry != nil && telemetry.Metrics != nil {
		// Where requests are counted, by the configs that have metrics,
		// for the Admin API's /metrics and OTLP: a reload may turn it on
		// or off.
		env.Metrics = observe.NewMetrics()
	}
	if c.AccessLog != nil {
		if env.AccessLog, err = observe.NewAccessLogFiles(c.AccessLog, root.Files()); err != nil {
			fmt.Fprintf(stderr, "fit run: access log: %v\n", err)
			return 1
		}
		if config.IsFileOutput(c.AccessLog.Output) && c.AccessLog.Rotate == nil {
			server.Warnf("access log file %s has no rotate: it will grow without bound", c.AccessLog.Output)
		}
		// Closed after the graph, so that every response finished has its
		// line written.
		defer env.AccessLog.Close()
	}
	tlsLog, telemetryLog := root.Logger(config.LoggerTLS), root.Logger(config.LoggerTelemetry)
	observe.Version, _ = versions()
	if telemetry != nil && telemetry.Traces != nil {
		if env.Tracer, err = observe.NewTracer(telemetry, telemetryLog); err != nil {
			printConfigError(stderr, "fit run", cfgName, mapErrors(telemetry.At.Errorf("%v", err)))
			return 1
		}
		// Closed after the graph, so that every span finished is exported.
		defer env.Tracer.Close()
	}
	// OCSP responses are stapled to every certificate that names a
	// responder, from files or obtained.
	env.OCSP = tlsmgr.NewOCSP(tlsLog)
	defer env.OCSP.Close()
	// The certificate manager is there whether or not the config lists
	// names for it, so that a reload may add some; with none it only waits.
	if env.ACME, err = tlsmgr.NewACME(c.ACME, nil, tlsLog); err != nil {
		printConfigError(stderr, "fit run", cfgName, mapErrors(err))
		return 1
	}
	defer env.ACME.Close()
	env.ACME.UseOCSP(env.OCSP)
	g, err := fitruntime.Compile(c, env)
	if err != nil {
		printConfigError(stderr, "fit run", cfgName, mapErrors(err))
		return 1
	}
	if boot != nil && !boot.seeded {
		if ws := c.Warnings(); len(ws) > 0 {
			printConfigError(warnWriter(stderr), "fit", cfgName, ws)
		}
	} else {
		printDeprecated(warnWriter(stderr), set, c.Warnings())
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
	printConfig(stdout, c, s)
	fmt.Fprintf(stdout, "fit: upstream engine{%s}\n", env.Engine.Settings().String())
	var keep int
	if c.Admin != nil {
		keep = c.Admin.HistoryLen()
	}
	source := admin.SourceFile
	if boot != nil {
		source = admin.SourceStore
	}
	configs := admin.NewConfigs(rt, s, env, c, data, source, keep)
	if boot != nil {
		configs.UseStore(boot.admin, boot.st.Write)
		// The console's passwords changed since the bootstrap file was
		// written are kept beside the config in the store.
		configs.UseConsoleUsers(boot.st.Users(), boot.st.SetUsers)
		configs.UseSealer(boot.st)
		// The default user's password is temporary until it is changed,
		// which keeps the changed hash in the store.
		if tempUser != "" && !slices.ContainsFunc(boot.users, func(u store.User) bool { return u.Username == tempUser }) {
			until := started.Add(*timeout)
			configs.UseTemporaryPassword(tempUser, until)
			fmt.Fprintf(warnWriter(stderr), "fit: the console user %q has the password %q until it is changed, which must be by %s; until then the console asks for that change and the Admin API refuses every request\n",
				tempUser, admin.DefaultConsolePassword, until.Format(time.RFC3339))
		}
	}
	var nodeID string
	if c.Admin != nil {
		// Who this fit is, for the Admin API's /node: its ID is the
		// config's, or made once and kept in the store, or beside the
		// config without one.
		var st *store.Store
		if boot != nil {
			st = boot.st
		}
		id, kept, err := c.Admin.NodeID, admin.IDInConfig, error(nil)
		if id == "" {
			id, kept, err = admin.NodeID(st, app.BesidePrefix(paths[0]))
		}
		if err != nil {
			fmt.Fprintf(warnWriter(stderr), "fit: the node ID could not be kept (%v): %s lasts until fit stops\n", err, id)
		}
		fitVersion, fibVersion := versions()
		configs.UseNode(admin.Node{ID: id, IDKept: kept, FitVersion: fitVersion, FibVersion: fibVersion, Started: started})
		nodeID = id
	}
	// The llm and tunnel roles run on the listeners' engine, and
	// write what their pages change back into the config through configs.
	apps := app.NewManager(s.Engine(), env.Engine, root, configs.EditMember)
	apps.SetDrainTimeout(*grace)
	if boot == nil {
		// Without a store, what the roles make and must keep, such as a
		// self-signed certificate their peers pin, goes beside the config.
		apps.KeepBeside(paths[0])
	}
	defer apps.Stop(context.Background()) // stopped already, unless serve fails before shutdown
	if err := apps.Apply(c); err != nil {
		fmt.Fprintf(stderr, "fit run: %v\n", err)
		apps.Stop(context.Background())
		_ = s.Close()
		return 1
	}
	configs.UseApps(apps)
	for _, id := range apps.Running() {
		fmt.Fprintf(stdout, "fit: running %s\n", id)
	}
	var as *admin.Server
	if c.Admin != nil {
		adminLog := root.Logger(config.LoggerAdmin)
		as, err = admin.Start(c.Admin, configs, func(r admin.Result) {
			printApplied(stdout, stderr, r)
			adminLog.Infof("loaded a config from the admin API (config version %d)", r.Version)
		}, adminLog.Warnf)
		if err != nil {
			fmt.Fprintf(stderr, "fit run: %v\n", err)
			_ = s.Close()
			return 1
		}
		adminLog.Infof("admin API on %s", as.Addr())
	}
	var console *admin.Console
	var memberLink *cluster.MemberLink
	// The manager of other fits, admin.cluster, served in the console as
	// the module cluster; the config checked that it has a store and a
	// console.
	var manager *cluster.Manager
	if c.Admin != nil && c.Admin.Cluster != nil && c.Admin.Console != nil && boot != nil {
		adminLog := root.Logger(config.LoggerAdmin)
		// The settings, the store and the engine are there: New fails only
		// for link_address, which it listens on.
		manager, err = cluster.New(cluster.Options{Engine: s.Engine(), Settings: c.Admin.Cluster, Store: boot.st, Logf: adminLog.Infof, NodeID: nodeID})
		if err != nil {
			fmt.Fprintf(stderr, "fit run: %v\n", err)
			_ = as.Shutdown(context.Background())
			_ = s.Close()
			return 1
		}
		defer manager.Close() // closed already, unless serve fails before shutdown
		fmt.Fprintf(stdout, "fit: managing %d members (admin.cluster)\n", len(manager.Members()))
		if info := manager.LinkInfo(""); info.Enabled {
			fmt.Fprintf(stdout, "fit: members link to %s (certificate %s)\n", info.Address, info.Fingerprint)
		}
	}
	if c.Admin != nil && c.Admin.Console != nil {
		adminLog := root.Logger(config.LoggerAdmin)
		modules := apps.Modules
		if manager != nil {
			modules = func() []*appconsole.Module { return append([]*appconsole.Module{manager.Module()}, apps.Modules()...) }
		}
		// A manager's users do what their roles let them (I148): the
		// console asks it about each request, and the assistant never
		// does more for a user than the user may.
		var extras admin.ConsoleExtras
		if manager != nil {
			extras = admin.ConsoleExtras{Gate: manager.ConsoleGate, AssistantCap: manager.AssistantCap, Cluster: manager.Assistant()}
		}
		console, err = admin.StartConsoleWith(s.Engine(), c.Admin.Console, c.Admin, configs, modules, func(r admin.Result) {
			printApplied(stdout, stderr, r)
		}, adminLog.Infof, extras)
		if err != nil {
			fmt.Fprintf(stderr, "fit run: %v\n", err)
			_ = as.Shutdown(context.Background())
			_ = s.Close()
			return 1
		}
		adminLog.Infof("console on %s", console.URL())
		if manager != nil {
			manager.SetConsole(console.Users, console.AssistantLevel)
		}
	}
	// A member of a manager's that links out to it, admin.cluster_member:
	// the manager reaches the Admin API, in process, over the link.
	if c.Admin != nil && c.Admin.ClusterMember != nil {
		adminLog := root.Logger(config.LoggerAdmin)
		keep := cluster.MemoryKeeper()
		switch prefix := app.BesidePrefix(paths[0]); {
		case boot != nil:
			keep = cluster.StoreKeeper(boot.st)
		case prefix != "":
			keep = cluster.FileKeeper(prefix + cluster.MemberFileSuffix)
		default:
			fmt.Fprintf(warnWriter(stderr), "fit: the credential admin.cluster_member enrolls for is kept nowhere, with no store and a config on standard input: it lasts until fit stops\n")
		}
		host, _ := os.Hostname()
		fitVersion, _ := versions()
		link, err := cluster.StartMember(cluster.MemberOptions{Engine: s.Engine(), Settings: c.Admin.ClusterMember,
			API:             admin.LinkAPI(c.Admin, configs, func(r admin.Result) { printApplied(stdout, stderr, r) }, adminLog.Infof),
			MaxRequestBytes: c.Admin.ConfigBytes() + 64<<10, Keeper: keep,
			Node: cluster.MemberNode{ID: nodeID, Hostname: host, FitVersion: fitVersion}, Logf: adminLog.Infof})
		if err != nil {
			fmt.Fprintf(stderr, "fit run: %v\n", err)
			if console != nil {
				_ = console.Shutdown(context.Background())
			}
			if as != nil {
				_ = as.Shutdown(context.Background())
			}
			_ = s.Close()
			return 1
		}
		defer link.Close() // closed already, unless serve fails before shutdown
		memberLink = link
		fmt.Fprintf(stdout, "fit: linking to the manager at %s (admin.cluster_member)\n", c.Admin.ClusterMember.Manager)
	}
	if telemetry != nil && telemetry.Metrics != nil {
		exporter, err := observe.NewMetricsExporter(telemetry, configs.WriteMetricsTo, telemetryLog)
		if err != nil {
			printConfigError(stderr, "fit run", cfgName, mapErrors(telemetry.At.Errorf("%v", err)))
			if console != nil {
				_ = console.Shutdown(context.Background())
			}
			if as != nil {
				_ = as.Shutdown(context.Background())
			}
			_ = s.Close()
			return 1
		}
		defer exporter.Close()
	}
	if telemetry != nil {
		telemetryLog.Infof("exporting telemetry to %s", telemetry.URL())
	}
	stopProviders, err := startProviders(c, configs, stdout, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "fit run: %v\n", err)
		if console != nil {
			_ = console.Shutdown(context.Background())
		}
		if as != nil {
			_ = as.Shutdown(context.Background())
		}
		_ = s.Close()
		return 1
	}

	var watcher *file.Watcher
	stopWatch := func() {}
	if *watch {
		opts := file.Options{Interval: *watchInterval, Expand: adapter.Files, MaxBytes: adapter.MaxBytes}
		watcher = file.New(paths, watchedFiles(srcs), opts, func(files []file.File) error {
			fmt.Fprintf(stdout, "fit: %s changed; reloading\n", cfgName)
			applyFiles(cfgName, sources(files), configs, stdout, stderr)
			return nil // applyFiles reported it
		}, func(err error) {
			fmt.Fprintf(stderr, "fit: watching %s: %v\n", cfgName, err)
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
		fmt.Fprintf(stdout, "fit: watching %s\n", cfgName)
	}

	stopCertWatch := func() {}
	if *watchCerts {
		cw := tlsmgr.NewFileWatcher(tlsmgr.WatchOptions{Interval: *watchInterval}, func() (uint64, map[string][sha256.Size]byte) {
			g := rt.Current()
			if g == nil {
				return 0, nil
			}
			defer g.Release()
			return g.Version, g.CertFiles
		}, func() error {
			tlsLog.Infof("certificate files changed; reloading")
			r, err := configs.Reapply(admin.SourceCertificates)
			if err != nil {
				return err
			}
			printApplied(stdout, stderr, r)
			tlsLog.Infof("reloaded certificate files (config version %d)", r.Version)
			return nil
		}, func(err error) {
			tlsLog.Warnf("certificate files: %v; the running certificates stay", err)
		})
		cctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			cw.Run(cctx)
		}()
		stopCertWatch = func() {
			cancel()
			<-done
		}
	}

	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-reload:
			if boot != nil {
				reloadStore(boot, configs, stdout, stderr)
			} else {
				reloadConfig(paths, configs, watcher, stdout, stderr)
			}
		}
	}
	stopSignals()
	// No config is applied while the server shuts down.
	stopWatch()
	stopProviders()
	stopCertWatch()
	fmt.Fprintln(stdout, "fit: shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), *grace)
	defer cancel()
	// First, so that no config is applied while the listeners shut down.
	if console != nil {
		_ = console.Shutdown(sctx)
	}
	if as != nil {
		_ = as.Shutdown(sctx)
	}
	// The link to a manager serves the Admin API too.
	if memberLink != nil {
		memberLink.Close()
	}
	// The manager's calls go out on the listeners' engine: closed before
	// it is.
	if manager != nil {
		manager.Close()
	}
	// Then the roles, whose bindings are on the listeners' engine.
	apps.Stop(sctx)
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

// printConfig prints every basic value the listeners' engine runs with: the
// limits that bound what clients may make fit hold, and fib's effective
// sizing after the config's values and the machine's have been applied. The
// upstream engine's line follows, since it is sized separately.
func printConfig(w io.Writer, c *config.Config, s *listener.Server) {
	l := c.Limits
	fmt.Fprintf(w, "fit: config listeners=%d limits{max_pending_bytes=%d max_conns=%d max_header_bytes=%d max_body_bytes=%d max_write_queue_bytes=%d write_stall_timeout=%s} engine{%s}\n",
		len(s.Addrs()), l.PendingBytes(), l.Conns(), l.HeaderBytes(), l.BodyBytes(), l.QueueBytes(), l.WriteStall(), s.Settings().String())
}

// reloadConfig loads the config paths name and applies it. On any failure
// the running config stays, and the error goes to stderr. A watcher of the
// files, when there is one, is told, so that it does not apply the same
// again.
func reloadConfig(paths []string, configs *admin.Configs, watcher *file.Watcher, stdout, stderr io.Writer) {
	name := strings.Join(paths, ", ")
	fmt.Fprintf(stdout, "fit: reloading %s\n", name)
	srcs, err := adapter.ReadSources(paths)
	if err != nil {
		fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
		printConfigError(stderr, "fit run", name, err)
		return
	}
	if watcher != nil {
		watcher.Applied(watchedFiles(srcs))
	}
	applyFiles(name, srcs, configs, stdout, stderr)
}

// reloadStore reads the store again and applies what it holds. On any
// failure the running config stays, and the error goes to stderr.
func reloadStore(b *storeBoot, configs *admin.Configs, stdout, stderr io.Writer) {
	fmt.Fprintf(stdout, "fit: reloading the store %s\n", b.st.Path())
	data, err := b.st.Read()
	var r admin.Result
	if err == nil {
		r, err = configs.Apply(data, admin.SourceStore)
	}
	if err != nil {
		fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
		printConfigError(stderr, "fit run", b.st.Path(), err)
		return
	}
	printApplied(stdout, stderr, r)
	fmt.Fprintf(stdout, "fit: reloaded the store %s (config version %d)\n", b.st.Path(), r.Version)
}

// applyFiles applies the config srcs hold, each file adapted by its
// extension, and says what happened; name names them.
func applyFiles(name string, srcs []adapter.Source, configs *admin.Configs, stdout, stderr io.Writer) {
	set, err := adapter.AdaptSet(srcs)
	if err != nil {
		fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
		printConfigError(stderr, "fit run", name, err)
		return
	}
	printWarnings(warnWriter(stderr), set)
	r, err := configs.Apply(set.JSON, admin.SourceFile)
	if err != nil {
		var errs config.Errors
		if errors.As(err, &errs) {
			fmt.Fprintln(stderr, "fit: reload failed; the running config stays")
			printConfigError(stderr, "fit run", name, set.MapErrors(err))
		} else {
			fmt.Fprintf(stderr, "fit: reload failed; the running config stays: %v\n", err)
		}
		return
	}
	printDeprecated(warnWriter(stderr), set, r.Deprecated)
	r.Deprecated = nil
	printApplied(stdout, stderr, r)
	fmt.Fprintf(stdout, "fit: reloaded %s (config version %d)\n", name, r.Version)
}

// printApplied prints what applying a config changed: its warnings as
// warnings, when stderr is the log's.
func printApplied(stdout, stderr io.Writer, r admin.Result) {
	stderr = warnWriter(stderr)
	for _, a := range r.Removed {
		fmt.Fprintf(stdout, "fit: listener %q on %s removed\n", a.Name, a.Addr)
	}
	for _, a := range r.Added {
		printListener(stdout, a)
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(stderr, "fit: %s\n", w)
	}
	for _, e := range r.Deprecated {
		fmt.Fprintf(stderr, "fit: %s\n", e)
	}
}

// withDefaultAdmin returns the config std, standard JSON, with admin and
// admin.console set, as -admin asks, and the default console user, whose
// password is temporary, when the config names no user.
func withDefaultAdmin(std []byte) ([]byte, string, error) {
	// A short password at the default cost: hashing it cannot fail.
	hash, _ := bcrypt.GenerateFromPassword([]byte(admin.DefaultConsolePassword), bcrypt.DefaultCost)
	return admin.WithDefaultConsole(std, string(hash))
}

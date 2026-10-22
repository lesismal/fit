package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"

	"github.com/lesismal/fit/plugin/build"
)

const buildUsage = `Usage: fit build [flags]

Builds a fit with more modules, or fewer, into a binary, as xcaddy builds
Caddy: a main package importing fit, the modules and providers fit ships
with, and the modules named with -with, built with the go command found on
PATH. GOOS and GOARCH in the environment build it for another system.

Flags:

	-o file       the binary to write; fit when left out
	-with module  a module to build in, repeated for each: path, path@version
	              or path=dir for one whose source is in dir. The module's
	              root package is imported, and registers its modules.
	-without pkg  leave out a package fit's build imports, repeated for each:
	              github.com/lesismal/fit/modules/standard (every handler and
	              middleware) or github.com/lesismal/fit/provider/standard
	              (every provider)
	-fit v        the fit to build on: a version of github.com/lesismal/fit,
	              or a directory holding its source; this fit's own version
	              when left out
	-keep dir     generate the main module in dir and keep it there
`

// stringList is a flag that may be given more than once.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

func runBuild(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, buildUsage) }
	output := fs.String("o", "fit", "")
	fitFlag := fs.String("fit", "", "")
	keep := fs.String("keep", "", "")
	var with, without stringList
	fs.Var(&with, "with", "")
	fs.Var(&without, "without", "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "fit build: unexpected %q\n\n%s", fs.Arg(0), buildUsage)
		return 2
	}
	o := build.Options{Output: *output, Without: without, Keep: *keep, Stderr: stderr}
	for _, w := range with {
		m, err := build.ParseModule(w)
		if err != nil {
			fmt.Fprintf(stderr, "fit build: %v\n", err)
			return 2
		}
		o.With = append(o.With, m)
	}
	switch v := *fitFlag; {
	case v == "":
		if o.Fit = ownFit(); o.Fit.Version == "" && o.Fit.Dir == "" {
			fmt.Fprintln(stderr, "fit build: this fit does not know its own version; name one with -fit, or a directory of fit's source")
			return 2
		}
	case strings.HasPrefix(v, "v") && !strings.ContainsAny(v, `/\`):
		o.Fit.Version = v
	default:
		o.Fit.Dir = v
	}
	o.Version = o.Fit.Version
	if o.Version == "" {
		o.Version = "devel"
	}
	o.Version += "+build"
	if _, err := build.Imports(o.With, o.Without); err != nil {
		fmt.Fprintf(stderr, "fit build: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Fprintf(stdout, "fit build: building %s on fit %s\n", o.Output, describe(o.Fit))
	if err := build.Build(ctx, o); err != nil {
		fmt.Fprintf(stderr, "fit build: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "fit build: wrote %s\n", o.Output)
	return 0
}

// ownFit returns the fit this binary was built from, as its build recorded
// it: fit's own module, or the one a build of fit build's required. A
// version Go made up for a build from a checkout with changes, ending in
// +dirty, is taken without them.
func ownFit() build.Module {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return build.Module{}
	}
	m := build.Module{Version: info.Main.Version}
	if info.Main.Path != build.FitModule {
		m = build.Module{}
		for _, dep := range info.Deps {
			if dep.Path != build.FitModule {
				continue
			}
			m.Version = dep.Version
			if dep.Replace != nil {
				if dep.Replace.Version == "" {
					m = build.Module{Dir: dep.Replace.Path}
				} else {
					m.Version = dep.Replace.Version
				}
			}
		}
	}
	m.Version = strings.TrimSuffix(m.Version, "+dirty")
	if m.Version == "(devel)" || m.Version == build.NoVersion {
		m.Version = ""
	}
	return m
}

func describe(m build.Module) string {
	if m.Dir != "" {
		return "from " + m.Dir
	}
	return m.Version
}

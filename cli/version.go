package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// Version is fit's version, which fit's main package sets from its own
// version, set at build time with -ldflags "-X main.version=v0.1.0". Left
// empty, the module version Go recorded in the binary is used instead.
var Version string

const fibModule = "github.com/lesismal/fib"

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fitVersion, fibVersion := versions()
	fmt.Fprintf(stdout, "fit %s\nfib %s\n%s %s/%s\n", fitVersion, fibVersion,
		runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return 0
}

// versions returns the versions of fit and of the fib linked into it, as the
// build recorded them.
func versions() (fit, fib string) {
	fit, fib = Version, "unknown"
	info, ok := debug.ReadBuildInfo()
	if !ok {
		if fit == "" {
			fit = "unknown"
		}
		return fit, fib
	}
	if fit == "" {
		fit = info.Main.Version
	}
	for _, dep := range info.Deps {
		if dep.Path != fibModule {
			continue
		}
		fib = dep.Version
		if dep.Replace != nil {
			fib = dep.Replace.Path
			if dep.Replace.Version != "" {
				fib += " " + dep.Replace.Version
			}
			fib = "replaced by " + fib
		}
	}
	return fit, fib
}

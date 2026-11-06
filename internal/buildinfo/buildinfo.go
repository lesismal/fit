// Package buildinfo reads the versions of fit and of the fib linked into it,
// as the build recorded them, for fit version and the Admin API's /node.
package buildinfo

import "runtime/debug"

const fibModule = "github.com/lesismal/fib"

// Versions returns fit's version, fit when it is not empty and else the
// module version Go recorded in the binary, and fib's; "unknown" for what the
// build did not record.
func Versions(fit string) (fitVersion, fibVersion string) {
	info, ok := debug.ReadBuildInfo()
	return versionsOf(fit, info, ok)
}

func versionsOf(fit string, info *debug.BuildInfo, ok bool) (string, string) {
	fib := "unknown"
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

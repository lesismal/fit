// Package cli is fit's command line: run, validate, build and the rest.
// Command fit is a main package that calls Main with the modules and
// providers fit ships with imported; one fit build generates is the same,
// with modules of its own besides, and Main serves both.
package cli

import (
	"fmt"
	"io"
)

const usage = `fit is a reverse proxy, static file server and API gateway built on fib.

Usage:

	fit <command> [arguments]

Commands:

	run        serve a config, from files or directories, until SIGINT or SIGTERM
	validate   check a config without running it
	check      ask a running fit, through its Admin API, whether what it runs
	           agrees with the config it runs from, and list the differences
	adapt      convert a config between .conf, YAML and native JSON, or from
	           nginx or a Caddyfile into one of them
	schema     print the JSON Schema of the native config
	build      build a fit with more modules, or fewer, as fit build -h says
	version    print the versions of fit, fib and Go
	hash-password
	           print the bcrypt hash of a password, asked for twice, masked, on
	           a terminal, or read from standard input, for basic_auth and the
	           console's users
	media-token
	           print a token a media_auth policy's secret signs, for whip, whep
	           and the other media handlers and roles
	store      export or import the config kept encrypted in admin.store
	help       print this help
`

// Main runs the command args names, args without the program's name, and
// returns the process's exit code: 0 on success, 1 when the command failed,
// 2 when it was used wrongly.
func Main(args []string, stdout, stderr io.Writer) int {
	return run(args, stdout, stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "adapt":
		return runAdapt(args[1:], stdout, stderr)
	case "schema":
		return runSchema(args[1:], stdout, stderr)
	case "build":
		return runBuild(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "hash-password":
		return runHashPassword(args[1:], stdout, stderr)
	case "media-token":
		return runMediaToken(args[1:], stdout, stderr)
	case "store":
		return runStore(args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "fit: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

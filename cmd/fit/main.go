// Command fit is a reverse proxy, load balancer, TLS terminator, static file
// server and API gateway built on fib.
package main

import (
	"fmt"
	"io"
	"os"

	_ "github.com/lesismal/fit/modules/standard"
)

const usage = `fit is a reverse proxy, static file server and API gateway built on fib.

Usage:

	fit <command> [arguments]

Commands:

	run        serve a config file until SIGINT or SIGTERM
	validate   check a config file without running it
	schema     print the JSON Schema of the native config
	version    print the versions of fit, fib and Go
	hash-password
	           print the bcrypt hash of the password read from standard input,
	           for basic_auth
	help       print this help
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run runs the command args names and returns the process's exit code: 0 on
// success, 1 when the command failed, 2 when it was used wrongly.
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
	case "schema":
		return runSchema(args[1:], stdout, stderr)
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "hash-password":
		return runHashPassword(args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "fit: unknown command %q\n\n%s", args[0], usage)
		return 2
	}
}

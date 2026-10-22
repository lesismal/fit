// Command fit is a reverse proxy, load balancer, TLS terminator, static file
// server and API gateway built on fib, with every module and provider fit
// ships with.
package main

import (
	"os"

	"github.com/lesismal/fit/cli"
	_ "github.com/lesismal/fit/modules/standard"
	_ "github.com/lesismal/fit/provider/standard"
)

// version is fit's version, set at build time with
// -ldflags "-X main.version=v0.1.0".
var version string

func main() {
	cli.Version = version
	os.Exit(cli.Main(os.Args[1:], os.Stdout, os.Stderr))
}

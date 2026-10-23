package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/lesismal/fit/adapter"
	_ "github.com/lesismal/fit/adapter/caddyfile"
	"github.com/lesismal/fit/adapter/fitconf"
	_ "github.com/lesismal/fit/adapter/nginx"
	"github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
)

const adaptUsage = `Usage: fit adapt [flags]

Converts a config between fit's formats, .konf, YAML and native JSON,
checked as fit run would load it, and converts an nginx config (-from
nginx) or a Caddyfile (-from caddyfile), of which a common subset
converts, to help moving to fit: fit run loads only its own formats.
What could not be converted is listed on standard error. Several files,
or directories of them, are merged into one config, as fit run merges
them.

Flags:

	-c path      a config file or a directory of them, or - for standard
	             input; give -c again for more; fit.json when left out
	-from name   the format of a single file or of standard input: json, or
	             an adapter's (%s); by the file's extension when left out,
	             json for standard input
	-to name     what to write: json, yaml or fit; json when left out
	-o file      where to write it; standard output when left out
`

func runAdapt(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("adapt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintf(stderr, adaptUsage, adapterList()) }
	var cfgPaths configPaths
	flags.Var(&cfgPaths, "c", "")
	from := flags.String("from", "", "")
	to := flags.String("to", "json", "")
	out := flags.String("o", "", "")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit adapt: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if *to != "json" && *to != "yaml" && *to != "fit" {
		fmt.Fprintf(stderr, "fit adapt: -to %q: json, yaml or fit\n", *to)
		return 2
	}
	paths := cfgPaths.orDefault()
	name := strings.Join(paths, ", ")
	var set *adapter.Set
	var err error
	switch {
	case len(paths) > 1 && slices.Contains(paths, "-"):
		fmt.Fprintln(stderr, "fit adapt: -c - reads standard input alone")
		return 2
	case len(paths) == 1 && paths[0] == "-":
		var data []byte
		if data, err = io.ReadAll(io.LimitReader(os.Stdin, adapter.MaxBytes)); err == nil {
			set, err = adaptOne(name, *from, data)
		}
	case *from != "":
		if len(paths) > 1 {
			fmt.Fprintln(stderr, "fit adapt: -from is for a single file; several take their formats from their extensions")
			return 2
		}
		var data []byte
		if data, err = os.ReadFile(paths[0]); err == nil {
			set, err = adaptOne(name, *from, data)
		}
	default:
		set, _, err = loadSet(paths)
	}
	if err == nil {
		_, err = config.Load(set.JSON)
		err = set.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, "fit adapt", name, err)
		return 1
	}
	printWarnings(stderr, set)

	var result []byte
	switch *to {
	case "json":
		var b bytes.Buffer
		if err := json.Indent(&b, set.JSON, "", "  "); err != nil {
			fmt.Fprintf(stderr, "fit adapt: %v\n", err)
			return 1
		}
		result = append(b.Bytes(), '\n')
	case "yaml":
		result, err = yaml.FromJSON(set.JSON)
	case "fit":
		result, err = fitconf.FromJSON(set.JSON)
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit adapt: %v\n", err)
		return 1
	}
	if *out == "" {
		_, _ = stdout.Write(result)
		return 0
	}
	if err := os.WriteFile(*out, result, 0o644); err != nil {
		fmt.Fprintf(stderr, "fit adapt: %v\n", err)
		return 1
	}
	return 0
}

// adaptOne adapts data, from the file name, with the adapter from: a set
// of one.
func adaptOne(name, from string, data []byte) (*adapter.Set, error) {
	r, err := adapter.Adapt(from, data)
	if err != nil {
		return nil, err
	}
	return &adapter.Set{Files: []string{name}, Parts: []*adapter.Result{r}, JSON: r.JSON}, nil
}

func adapterList() string {
	names := adapter.Names()
	if len(names) == 0 {
		return "none"
	}
	s := names[0]
	for _, n := range names[1:] {
		s += ", " + n
	}
	return s
}

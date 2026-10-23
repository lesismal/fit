package cli

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lesismal/fit/adapter"
	_ "github.com/lesismal/fit/adapter/caddyfile"
	_ "github.com/lesismal/fit/adapter/nginx"
	"github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/config"
)

const adaptUsage = `Usage: fit adapt [flags]

Converts a config to fit's native JSON, checked as fit run would load it,
or writes native JSON as YAML: a YAML config (.yaml, .yml), an nginx
config (.conf) or a Caddyfile (a file named Caddyfile, or .caddyfile), of
which a common subset converts. What could not be converted is listed on
standard error.

Flags:

	-c file      the config to convert, or - for standard input; fit.json
	             when left out
	-from name   its format: json, or an adapter's (%s); by the file's
	             extension when left out, json for standard input
	-to name     what to write: json, or yaml; json when left out
	-o file      where to write it; standard output when left out
`

func runAdapt(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("adapt", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintf(stderr, adaptUsage, adapterList()) }
	file := flags.String("c", "fit.json", "")
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
	if *to != "json" && *to != "yaml" {
		fmt.Fprintf(stderr, "fit adapt: -to %q: json or yaml\n", *to)
		return 2
	}
	var data []byte
	var err error
	if *file == "-" {
		data, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<20))
	} else {
		data, err = os.ReadFile(*file)
		if *from == "" {
			*from = adapter.ForPath(*file)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "fit adapt: %v\n", err)
		return 1
	}
	adapted, err := adapter.Adapt(*from, data)
	if err == nil {
		_, err = config.Load(adapted.JSON)
		err = adapted.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, "fit adapt", *file, err)
		return 1
	}
	printWarnings(stderr, *file, adapted)

	var result []byte
	switch *to {
	case "json":
		var b bytes.Buffer
		if err := json.Indent(&b, adapted.JSON, "", "  "); err != nil {
			fmt.Fprintf(stderr, "fit adapt: %v\n", err)
			return 1
		}
		result = append(b.Bytes(), '\n')
	case "yaml":
		if result, err = yaml.FromJSON(adapted.JSON); err != nil {
			fmt.Fprintf(stderr, "fit adapt: %v\n", err)
			return 1
		}
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

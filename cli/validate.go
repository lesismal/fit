package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/schema"
)

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("c", "fit.json", "the config file to check: native JSON (comments and trailing commas allowed), YAML (.yaml, .yml), nginx (.conf) or a Caddyfile")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit validate: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	adapted, err := adapter.ReadFile(*file)
	var c *config.Config
	if err == nil {
		if c, err = config.Load(adapted.JSON); err == nil {
			// Compiling provisions every module, which checks their params
			// too.
			var g *fitruntime.Graph
			if g, err = fitruntime.Compile(c, fitruntime.Env{}); err == nil {
				g.Release()
			}
		}
		err = adapted.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, "fit validate", *file, err)
		return 1
	}
	printWarnings(stderr, *file, adapted)
	printDeprecated(stderr, *file, adapted, c.Warnings())
	fmt.Fprintf(stdout, "%s: valid\n", *file)
	return 0
}

// printWarnings prints what adapting the config in file left out or
// doubted.
func printWarnings(stderr io.Writer, file string, r *adapter.Result) {
	if r == nil {
		return
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(stderr, "%s: warning: %s\n", file, w)
	}
}

// printDeprecated prints the renamed names the config in file uses, each
// at its position in the file, which adapted maps them to.
func printDeprecated(stderr io.Writer, file string, adapted *adapter.Result, ws config.Errors) {
	if len(ws) > 0 {
		printConfigError(stderr, "fit", file, adapted.MapErrors(ws))
	}
}

// printConfigError prints why the config in file could not be loaded or
// compiled: each error with its position, or why the file could not be read.
func printConfigError(stderr io.Writer, cmd, file string, err error) {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return
	}
	for _, e := range config.AsErrors(err) {
		sep := ":"
		if e.Line == 0 {
			sep = ": "
		}
		fmt.Fprintf(stderr, "%s%s%s\n", file, sep, e)
	}
}

func runSchema(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	flags.SetOutput(stderr)
	reference := flags.Bool("reference", false, "print the config reference, in Markdown, rather than the JSON Schema")
	template := flags.String("template", "", "print a config template, json or yaml, with every field commented, rather than the JSON Schema")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	generate := schema.Generate
	switch {
	case *reference:
		generate = schema.Reference
	case *template != "":
		generate = func() ([]byte, error) { return schema.Template(*template) }
	}
	out, err := generate()
	if err != nil {
		fmt.Fprintf(stderr, "fit schema: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(out)
	return 0
}

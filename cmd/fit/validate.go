package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"

	"github.com/lesismal/fit/config"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/schema"
)

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	file := flags.String("c", "fit.json", "the config file to check")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit validate: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	c, err := config.LoadFile(*file)
	if err == nil {
		// Compiling provisions every module, which checks their params too.
		var g *fitruntime.Graph
		if g, err = fitruntime.Compile(c); err == nil {
			g.Release()
		}
	}
	if err != nil {
		printConfigError(stderr, "fit validate", *file, err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: valid\n", *file)
	return 0
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
	if err := flags.Parse(args); err != nil {
		return 2
	}
	out, err := schema.Generate()
	if err != nil {
		fmt.Fprintf(stderr, "fit schema: %v\n", err)
		return 1
	}
	_, _ = stdout.Write(out)
	return 0
}

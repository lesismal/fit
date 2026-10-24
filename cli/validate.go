package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/config"
	fitruntime "github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/schema"
)

func runValidate(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var cfgPaths configPaths
	flags.Var(&cfgPaths, "c", fmt.Sprintf(configFlagUsage, "check"))
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "fit validate: unexpected argument %q\n", flags.Arg(0))
		return 2
	}

	paths := cfgPaths.orDefault()
	name := strings.Join(paths, ", ")
	set, _, err := loadSet(paths)
	var c *config.Config
	if err == nil {
		if c, err = config.Load(set.JSON); err == nil {
			// Compiling provisions every module, which checks their params
			// too.
			var g *fitruntime.Graph
			if g, err = fitruntime.Compile(c, fitruntime.Env{}); err == nil {
				g.Release()
			}
		}
		err = set.MapErrors(err)
	}
	if err != nil {
		printConfigError(stderr, "fit validate", name, err)
		return 1
	}
	printWarnings(stderr, set)
	printDeprecated(stderr, set, c.Warnings())
	fmt.Fprintf(stdout, "%s: valid\n", name)
	return 0
}

// printWarnings prints what adapting each of a config's files left out or
// doubted.
func printWarnings(stderr io.Writer, set *adapter.Set) {
	if set == nil {
		return
	}
	for i, r := range set.Parts {
		for _, w := range r.Warnings {
			fmt.Fprintf(stderr, "%s: warning: %s\n", set.Files[i], w)
		}
	}
}

// printDeprecated prints the renamed names the config uses, each at its
// position in its file.
func printDeprecated(stderr io.Writer, set *adapter.Set, ws config.Errors) {
	if len(ws) > 0 {
		printConfigError(stderr, "fit", set.Name(), set.MapErrors(ws))
	}
}

// printConfigError prints why the config in file could not be loaded or
// compiled: each error with its position, in the file it names or in file,
// or why the file could not be read.
func printConfigError(stderr io.Writer, cmd, file string, err error) {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		fmt.Fprintf(stderr, "%s: %v\n", cmd, err)
		return
	}
	for _, e := range config.AsErrors(err) {
		if e.File != "" {
			fmt.Fprintln(stderr, e)
			continue
		}
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
	template := flags.String("template", "", "print a config template, fit, yaml or json, with every field commented, rather than the JSON Schema")
	dir := flags.String("dir", "", "with -template, write the template as a directory of files by kind, one for each site, into this directory, which must not have them yet")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dir != "" {
		if *template == "" {
			fmt.Fprintln(stderr, "fit schema: -dir needs -template")
			return 2
		}
		if err := writeTemplateDir(*template, *dir); err != nil {
			fmt.Fprintf(stderr, "fit schema: %v\n", err)
			return 1
		}
		return 0
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

// writeTemplateDir writes the directory template in format into dir,
// refusing to replace a file there.
func writeTemplateDir(format, dir string) error {
	files, err := schema.TemplateDir(format)
	if err != nil {
		return err
	}
	names := slices.Sorted(maps.Keys(files))
	for _, name := range names {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return fmt.Errorf("%s is there already", filepath.Join(dir, name))
		}
	}
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

package cli

import (
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/provider/file"
)

// configPaths is the -c flag: a config's files and directories, each -c
// one of them, fit.json when none is given.
type configPaths []string

func (p *configPaths) String() string { return strings.Join(*p, ", ") }

func (p *configPaths) Set(v string) error {
	*p = append(*p, v)
	return nil
}

// orDefault returns the paths given, or fit.json.
func (p configPaths) orDefault() []string {
	if len(p) == 0 {
		return []string{"fit.json"}
	}
	return p
}

const configFlagUsage = "a config file, or a directory of them, to %s; give -c again for more, which are merged (fit.json when none is given). " +
	"A file is fit's .fit format, YAML (.yaml, .yml) or JSON (comments and trailing commas allowed); " +
	"a directory's are those, in its subdirectories too; an nginx config (.conf) or a Caddyfile named is converted"

// loadSet reads and adapts the config paths name, merged.
func loadSet(paths []string) (*adapter.Set, []adapter.Source, error) {
	srcs, err := adapter.ReadSources(paths)
	if err != nil {
		return nil, nil, err
	}
	set, err := adapter.AdaptSet(srcs)
	if err != nil {
		return nil, nil, err
	}
	return set, srcs, nil
}

func watchedFiles(srcs []adapter.Source) []file.File {
	files := make([]file.File, len(srcs))
	for i, s := range srcs {
		files[i] = file.File{Path: s.Path, Data: s.Data}
	}
	return files
}

func sources(files []file.File) []adapter.Source {
	srcs := make([]adapter.Source, len(files))
	for i, f := range files {
		srcs[i] = adapter.Source{Path: f.Path, Data: f.Data}
	}
	return srcs
}

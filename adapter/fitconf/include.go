package fitconf

import (
	"slices"

	"github.com/lesismal/fit/adapter"
)

// maxIncludeDepth bounds how deep includes nest: a file including one that
// includes another, and so on.
const maxIncludeDepth = 16

func init() {
	adapter.RegisterFiles("fit", refs, AdaptFile)
}

// refs returns the files a .konf file names, as written: those import, at
// the top, imports, and those include, anywhere, includes.
func refs(data []byte) (imports, includes []string, err error) {
	ds, err := parse("", data)
	if err != nil {
		return nil, nil, err
	}
	var walk func(ds []*directive, top bool)
	walk = func(ds []*directive, top bool) {
		for _, d := range ds {
			switch {
			case d.name.quoted:
			case top && d.name.text == "import":
				for _, a := range d.args {
					imports = append(imports, a.text)
				}
				continue
			case d.name.text == "include":
				for _, a := range d.args {
					includes = append(includes, a.text)
				}
				continue
			}
			walk(d.block, false)
		}
	}
	walk(ds, true)
	return imports, includes, nil
}

// reader reads a .konf file with the files it includes.
type reader struct {
	include adapter.Includer
	// stack are the files being read, the config's own first, each
	// including the next.
	stack []string
	// words are the words read, every file's together.
	words int
}

// read parses file, data, with its includes read in their places. top says
// its directives are at the top of the config.
func (r *reader) read(file string, data []byte, top bool) ([]*directive, error) {
	items, err := lex(file, data)
	if err != nil {
		return nil, err
	}
	if r.words += len(items); r.words > maxTokens && len(items) > 0 {
		t := items[0].tok
		return nil, errTok(t, "the config holds more than %d words, with the files it includes", maxTokens)
	}
	ds, err := parseItems(items)
	if err != nil {
		return nil, err
	}
	return r.splice(ds, top)
}

// splice returns ds with each include replaced with the directives of the
// files it names, and each import, at the top of the config's own file,
// left out: the config's files are found with the files it imports
// (adapter.Files), which are merged after it.
//
//	import <paths or globs...>    at the top: config files of their own
//	include <paths or globs...>   anywhere: .konf directives read here
func (r *reader) splice(ds []*directive, top bool) ([]*directive, error) {
	out := make([]*directive, 0, len(ds))
	for _, d := range ds {
		switch {
		case d.name.quoted:
		case d.name.text == "import":
			if !top || len(r.stack) > 1 {
				return nil, errTok(d.name, "import is given at the top of a config file only, not in a block or a file included: it imports whole config files")
			}
			if len(d.args) == 0 || d.hasBlock {
				return nil, errTok(d.name, "import takes the config files to import, as in import sites/*.konf")
			}
			if r.include == nil {
				return nil, errTok(d.name, "import reads other files: read the config from its file, as fit run -c does")
			}
			continue
		case d.name.text == "include":
			if len(d.args) == 0 || d.hasBlock {
				return nil, errTok(d.name, "include takes the files to include, as in include snippets/*.konf")
			}
			inner, err := r.includeFiles(d, top)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		if d.hasBlock {
			block, err := r.splice(d.block, false)
			if err != nil {
				return nil, err
			}
			d.block = block
		}
		out = append(out, d)
	}
	return out, nil
}

// includeFiles returns the directives of the files include d names.
func (r *reader) includeFiles(d *directive, top bool) ([]*directive, error) {
	if r.include == nil {
		return nil, errTok(d.name, "include reads other files: read the config from its file, as fit run -c does")
	}
	if len(r.stack) > maxIncludeDepth {
		return nil, errTok(d.name, "includes nested more than %d deep", maxIncludeDepth)
	}
	from := r.stack[len(r.stack)-1]
	var out []*directive
	for _, a := range d.args {
		srcs, err := r.include(from, a.text)
		if err != nil {
			return nil, errTok(a, "include %s: %v", a.text, err)
		}
		for _, src := range srcs {
			if slices.Contains(r.stack, src.Path) {
				return nil, errTok(a, "%s includes itself", src.Path)
			}
			r.stack = append(r.stack, src.Path)
			inner, err := r.read(src.Path, src.Data, top)
			r.stack = r.stack[:len(r.stack)-1]
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
		}
	}
	return out, nil
}

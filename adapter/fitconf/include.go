package fitconf

import (
	"slices"
	"strings"

	"github.com/lesismal/fit/adapter"
)

// maxIncludeDepth bounds how deep includes nest: a file including one that
// includes another, and so on.
const maxIncludeDepth = 16

func init() {
	adapter.RegisterFiles("fit", refs, AdaptFile)
}

// refs returns the files a .konf file names, as written: those import, at
// the top, imports, and those include, anywhere, includes, but the snippets
// the file defines.
func refs(data []byte) (imports, includes []string, err error) {
	ds, err := parse("", data)
	if err != nil {
		return nil, nil, err
	}
	snippets := map[string]bool{}
	for _, d := range ds {
		if !d.name.quoted && d.name.text == "snippet" && len(d.args) == 1 {
			snippets[d.args[0].text] = true
		}
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
					if !snippets[a.text] {
						includes = append(includes, a.text)
					}
				}
				continue
			}
			walk(d.block, false)
		}
	}
	walk(ds, true)
	return imports, includes, nil
}

// snippet is a block of directives defined once and included by its name.
type snippet struct {
	file  string // the file it was defined in, which its includes are relative to
	block []*directive
}

// frame is a file, or a snippet, being read.
type frame struct {
	name string // the file, or "snippet <name>"
	file string // the file, or the one the snippet was defined in
}

// reader reads a .konf file with the files and snippets it includes.
type reader struct {
	include adapter.Includer
	// stack are the files and snippets being read, the config's own file
	// first, each including the next.
	stack []frame
	// snippets are those defined so far, in reading order.
	snippets map[string]*snippet
	// words are the words read, every file's together.
	words int
}

func newReader(path string, include adapter.Includer) *reader {
	return &reader{include: include, stack: []frame{{name: path, file: path}}, snippets: map[string]*snippet{}}
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
// snippets and files it names, each snippet's definition taken out, and
// each import, at the top of the config's own file, left out: the config's
// files are found with the files it imports (adapter.Files), which are
// merged after it.
//
//	import <paths or globs...>      at the top: config files of their own
//	snippet <name> { ... }          at the top: directives to include by name
//	include <snippets, paths or globs...>
//	                                anywhere: those directives, read here
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
		case d.name.text == "snippet":
			if err := r.define(d, top); err != nil {
				return nil, err
			}
			continue
		case d.name.text == "include":
			if len(d.args) == 0 || d.hasBlock {
				return nil, errTok(d.name, "include takes the snippets or the files to include, as in include tls-common or include snippets/*.konf")
			}
			inner, err := r.includeAll(d, top)
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

// define defines the snippet d.
func (r *reader) define(d *directive, top bool) error {
	if !top {
		return errTok(d.name, "snippet is given at the top of a file only, not in a block")
	}
	if len(d.args) != 1 || !d.hasBlock {
		return errTok(d.name, "snippet takes its name and a block, as in snippet pool { max_conns 1024 }")
	}
	name := d.args[0]
	if name.text == "" || strings.ContainsAny(name.text, `*?[/\`) {
		return errTok(name, "%q cannot name a snippet: a name holds no /, \\, *, ? or [", name.text)
	}
	if _, ok := r.snippets[name.text]; ok {
		return errTok(name, "snippet %s is defined twice", name.text)
	}
	r.snippets[name.text] = &snippet{file: r.stack[len(r.stack)-1].file, block: d.block}
	return nil
}

// includeAll returns the directives of the snippets and files include d
// names: a snippet defined so far by its name, else a file.
func (r *reader) includeAll(d *directive, top bool) ([]*directive, error) {
	if len(r.stack) > maxIncludeDepth {
		return nil, errTok(d.name, "includes nested more than %d deep", maxIncludeDepth)
	}
	var out []*directive
	for _, a := range d.args {
		if sn, ok := r.snippets[a.text]; ok {
			inner, err := r.useSnippet(a, sn, top)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		inner, err := r.includeFiles(a, top)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// useSnippet returns a copy of snippet sn's directives, its includes read,
// for the include at a.
func (r *reader) useSnippet(a token, sn *snippet, top bool) ([]*directive, error) {
	name := "snippet " + a.text
	if slices.ContainsFunc(r.stack, func(f frame) bool { return f.name == name }) {
		return nil, errTok(a, "%s includes itself", name)
	}
	r.stack = append(r.stack, frame{name: name, file: sn.file})
	defer func() { r.stack = r.stack[:len(r.stack)-1] }()
	n := 0
	ds := cloneDirectives(sn.block, &n)
	if r.words += n; r.words > maxTokens {
		return nil, errTok(a, "the config holds more than %d words, with the snippets and files it includes", maxTokens)
	}
	return r.splice(ds, top)
}

// includeFiles returns the directives of the files a names.
func (r *reader) includeFiles(a token, top bool) ([]*directive, error) {
	if r.include == nil {
		return nil, errTok(a, "include %s: there is no snippet %s, and include reads other files only from a config's file, as fit run -c reads it", a.text, a.text)
	}
	from := r.stack[len(r.stack)-1].file
	srcs, err := r.include(from, a.text)
	if err != nil {
		return nil, errTok(a, "include %s: there is no snippet %s defined before it, nor such a file: %v", a.text, a.text, err)
	}
	var out []*directive
	for _, src := range srcs {
		if slices.ContainsFunc(r.stack, func(f frame) bool { return f.name == src.Path }) {
			return nil, errTok(a, "%s includes itself", src.Path)
		}
		r.stack = append(r.stack, frame{name: src.Path, file: src.Path})
		inner, err := r.read(src.Path, src.Data, top)
		r.stack = r.stack[:len(r.stack)-1]
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// cloneDirectives returns a deep copy of ds, adding the words it holds to
// *n.
func cloneDirectives(ds []*directive, n *int) []*directive {
	out := make([]*directive, len(ds))
	for i, d := range ds {
		c := *d
		c.args = slices.Clone(d.args)
		*n += 1 + len(d.args)
		if d.hasBlock {
			c.block = cloneDirectives(d.block, n)
		}
		out[i] = &c
	}
	return out
}

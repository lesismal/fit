package fitconf

import (
	"slices"
	"strings"

	"github.com/lesismal/fit/adapter"
)

// maxIncludeDepth bounds how deep includes and defs nest: a file including
// one that includes another, and a def using another, and so on.
const maxIncludeDepth = 16

func init() {
	adapter.RegisterFiles("fit", refs, AdaptFile)
}

// refs returns the files a .conf file names, as written: those import, at
// the top, imports, and those include, anywhere, includes. use reads a def,
// which is no file, so it names none.
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

// def is a block of directives defined once, with def, and read by name,
// with use.
type def struct {
	file  string // the file it was defined in, which its includes are relative to
	block []*directive
}

// frame is a file, or a def, being read.
type frame struct {
	name string // the file, or "def <name>"
	file string // the file, or the one the def was defined in
}

// reader reads a .conf file with the files it includes and the defs it uses.
type reader struct {
	include adapter.Includer
	// stack are the files and defs being read, the config's own file
	// first, each including or using the next.
	stack []frame
	// defs are those defined so far, in reading order.
	defs map[string]*def
	// words are the words read, every file's together.
	words int
}

func newReader(path string, include adapter.Includer) *reader {
	return &reader{include: include, stack: []frame{{name: path, file: path}}, defs: map[string]*def{}}
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

// splice returns ds with each use replaced with the directives of the defs
// it names, each include with those of the files it names, each def's
// definition taken out, and each import, at the top of the config's own
// file, left out: the config's files are found with the files it imports
// (adapter.Files), which are merged after it.
//
//	import <paths or globs...>      at the top: config files of their own
//	def <name> { ... }              at the top: directives to use by name
//	use <defs...>                   anywhere: those directives, read here
//	include <paths or globs...>     anywhere: those files' directives, read here
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
				return nil, errTok(d.name, "import takes the config files to import, as in import sites/*.conf")
			}
			if r.include == nil {
				return nil, errTok(d.name, "import reads other files: read the config from its file, as fit run -c does")
			}
			continue
		case d.name.text == "def":
			if err := r.define(d, top); err != nil {
				return nil, err
			}
			continue
		case d.name.text == "use":
			if len(d.args) == 0 || d.hasBlock {
				return nil, errTok(d.name, "use takes the defs to read, as in use tls-common")
			}
			inner, err := r.useAll(d, top)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		case d.name.text == "include":
			if len(d.args) == 0 || d.hasBlock {
				return nil, errTok(d.name, "include takes the files to include, as in include routes/*.conf")
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

// define defines the def d.
func (r *reader) define(d *directive, top bool) error {
	if !top {
		return errTok(d.name, "def is given at the top of a file only, not in a block")
	}
	if len(d.args) != 1 || !d.hasBlock {
		return errTok(d.name, "def takes its name and a block, as in def pool { max_conns 1024 }")
	}
	name := d.args[0]
	if name.text == "" || strings.ContainsAny(name.text, `*?[/\`) {
		return errTok(name, "%q cannot name a def: a name holds no /, \\, *, ? or [", name.text)
	}
	if _, ok := r.defs[name.text]; ok {
		return errTok(name, "def %s is defined twice", name.text)
	}
	r.defs[name.text] = &def{file: r.stack[len(r.stack)-1].file, block: d.block}
	return nil
}

// useAll returns the directives of the defs use d names, each of which is
// defined before it.
func (r *reader) useAll(d *directive, top bool) ([]*directive, error) {
	if len(r.stack) > maxIncludeDepth {
		return nil, errTok(d.name, "uses and includes nested more than %d deep", maxIncludeDepth)
	}
	var out []*directive
	for _, a := range d.args {
		df, ok := r.defs[a.text]
		if !ok {
			return nil, errTok(a, "use %s: there is no def %s defined before it", a.text, a.text)
		}
		inner, err := r.useDef(a, df, top)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// useDef returns a copy of def df's directives, its includes and uses read,
// for the use at a.
func (r *reader) useDef(a token, df *def, top bool) ([]*directive, error) {
	name := "def " + a.text
	if slices.ContainsFunc(r.stack, func(f frame) bool { return f.name == name }) {
		return nil, errTok(a, "%s uses itself", name)
	}
	r.stack = append(r.stack, frame{name: name, file: df.file})
	defer func() { r.stack = r.stack[:len(r.stack)-1] }()
	n := 0
	ds := cloneDirectives(df.block, &n)
	if r.words += n; r.words > maxTokens {
		return nil, errTok(a, "the config holds more than %d words, with the defs and files it uses and includes", maxTokens)
	}
	return r.splice(ds, top)
}

// includeAll returns the directives of the files include d names.
func (r *reader) includeAll(d *directive, top bool) ([]*directive, error) {
	if len(r.stack) > maxIncludeDepth {
		return nil, errTok(d.name, "uses and includes nested more than %d deep", maxIncludeDepth)
	}
	var out []*directive
	for _, a := range d.args {
		inner, err := r.includeFile(a, top)
		if err != nil {
			return nil, err
		}
		out = append(out, inner...)
	}
	return out, nil
}

// includeFile returns the directives of the file a names.
func (r *reader) includeFile(a token, top bool) ([]*directive, error) {
	if r.include == nil {
		return nil, errTok(a, "include %s: include reads other files only from a config's file, as fit run -c reads it", a.text)
	}
	from := r.stack[len(r.stack)-1].file
	srcs, err := r.include(from, a.text)
	if err != nil {
		return nil, errTok(a, "include %s: %v", a.text, err)
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

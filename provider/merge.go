package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
)

// Kinds are the members of a config a part may hold, the arrays whose
// elements are merged.
var Kinds = []string{"listeners", "routes", "upstreams", "services"}

// MaxPartBytes bounds a part: one larger is refused.
const MaxPartBytes = 16 << 20

// A Part is what a provider provides, as Parse read it.
type Part struct {
	// Provider names the provider the part came from.
	Provider string
	doc      *document
}

// Bytes returns the size of the part as written.
func (p *Part) Bytes() int { return len(p.doc.data) }

// Data returns the part as written. The caller must not change it.
func (p *Part) Data() []byte { return p.doc.data }

// Parse reads data, what the provider name provides: a JSON object whose
// members are among Kinds, each an array of objects. Empty data provides
// nothing. Parse keeps data, which the caller must not change afterwards.
// Its errors are config.Errors, with positions in data.
func Parse(name string, data []byte) (*Part, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	if len(data) > MaxPartBytes {
		return nil, config.Errors{{Msg: fmt.Sprintf("the part is larger than %d bytes", MaxPartBytes)}}
	}
	d, err := scan(data, true)
	if err != nil {
		return nil, err
	}
	return &Part{Provider: name, doc: d}, nil
}

// document is a config, or a part, scanned for what merging needs: where
// its top-level object ends, and where the arrays of Kinds and their
// elements are.
type document struct {
	data    []byte
	keys    []string // the top-level members, in order
	end     int      // the offset of the top-level object's '}'
	members int
	arrays  [4]*array // by the index of their kind in Kinds; nil when left out
}

type array struct {
	open, close int // the offsets of '[' and ']'
	elems       []element
}

type element struct {
	start, end int // where it is in its document
	name       string
	named      bool
}

func (e element) raw(data []byte) []byte { return data[e.start:e.end] }

// scan scans data, a JSON object. A part may hold only Kinds, and their
// elements must be objects; a base config may hold anything, but the
// members of Kinds it has must be arrays.
func scan(data []byte, part bool) (*document, error) {
	d := &document{data: data}
	errAt := func(off int, path, format string, args ...any) error {
		line, col := position(data, off)
		return config.Errors{{Path: path, Line: line, Col: col, Msg: fmt.Sprintf(format, args...)}}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	syntax := func(err error) error {
		off := int(dec.InputOffset())
		var se *json.SyntaxError
		if errors.As(err, &se) {
			off = int(se.Offset)
		}
		return errAt(min(off, len(data)), "", "%v", err)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, syntax(err)
	}
	if tok != json.Delim('{') {
		return nil, errAt(skipSpace(data, 0), "", "must be a JSON object")
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, syntax(err)
		}
		key := tok.(string) // the decoder gives only strings as keys
		keyOff := int(dec.InputOffset())
		d.keys = append(d.keys, key)
		d.members++
		k := slices.Index(Kinds, key)
		if k < 0 {
			if part {
				return nil, errAt(keyOff, key, "a provider may provide only listeners, routes, upstreams and services")
			}
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, syntax(err)
			}
			continue
		}
		if d.arrays[k] != nil {
			return nil, errAt(keyOff, key, "is given twice")
		}
		tok, err = dec.Token()
		if err != nil {
			return nil, syntax(err)
		}
		if tok != json.Delim('[') {
			return nil, errAt(keyOff, key, "must be an array, for what providers provide to be merged into it")
		}
		a := &array{open: int(dec.InputOffset()) - 1}
		for dec.More() {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, syntax(err)
			}
			end := int(dec.InputOffset())
			e := element{start: end - len(raw), end: end}
			path := key + "[" + strconv.Itoa(len(a.elems)) + "]"
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
				if part {
					return nil, errAt(e.start, path, "must be an object")
				}
			} else if n, ok := fields["name"]; ok {
				if err := json.Unmarshal(n, &e.name); err != nil {
					if part {
						return nil, errAt(e.start, path+".name", "must be a string")
					}
				} else {
					e.named = e.name != ""
				}
			}
			a.elems = append(a.elems, e)
		}
		if _, err := dec.Token(); err != nil {
			return nil, syntax(err)
		}
		a.close = int(dec.InputOffset()) - 1
		d.arrays[k] = a
	}
	if _, err := dec.Token(); err != nil {
		return nil, syntax(err)
	}
	d.end = int(dec.InputOffset()) - 1
	if _, err := dec.Token(); err != io.EOF {
		return nil, errAt(skipSpace(data, d.end+1), "", "has more after the top-level object")
	}
	return d, nil
}

// Source says where an element, or a member, of a merged config came from.
type Source struct {
	// Path is a JSON pointer to it in the merged config.
	Path string `json:"path"`
	// Source is the provider it came from, or the source of the base
	// config: file, admin or rollback.
	Source string `json:"source"`
	// From is a JSON pointer to it in what its provider provided; empty
	// for the base config.
	From string `json:"from,omitempty"`
}

// Shadowed is a provider's element left out of a merged config because its
// name was taken.
type Shadowed struct {
	// Source is the provider, and From a JSON pointer to the element in
	// what it provided.
	Source string `json:"source"`
	From   string `json:"from"`
	Name   string `json:"name"`
	// By is the source whose element of that name is in the config.
	By string `json:"by"`
}

// Merged is a base config with parts merged into it.
type Merged struct {
	// Data is the merged config: the base config as written, with the
	// parts' elements, as written, added to its arrays.
	Data     []byte
	Sources  []Source
	Shadowed []Shadowed

	base     *document
	segments []segment
	origins  [4][]origin // the added elements of each kind, in order
}

// segment is a span of the merged config and where it came from: the base
// config, an element of a part, or neither, the commas and keys merging
// writes.
type segment struct {
	start, end int // in Merged.Data
	part       *Part
	from       int // where it starts in the base config, or in part; -1 for neither
}

type origin struct {
	part  *Part
	index int // in the part's array
}

// Merge merges parts into base, the base config, whose source is
// baseSource; parts come in their order of precedence, the first winning.
// base must be valid JSON. Merge keeps base and parts, which the caller
// must not change afterwards. Its errors are config.Errors.
func Merge(base []byte, baseSource string, parts []*Part) (*Merged, error) {
	bd, err := scan(base, false)
	if err != nil {
		return nil, err
	}
	m := &Merged{base: bd}

	// What each part adds, kind by kind, and what it leaves out.
	type pick struct {
		part *Part
		elem element
		idx  int
	}
	var picks [4][]pick
	for k := range Kinds {
		taken := map[string]string{}
		if a := bd.arrays[k]; a != nil {
			for _, e := range a.elems {
				if e.named {
					taken[e.name] = baseSource
				}
			}
		}
		for _, p := range parts {
			a := p.doc.arrays[k]
			if a == nil {
				continue
			}
			for i, e := range a.elems {
				if e.named {
					if by, ok := taken[e.name]; ok && by != p.Provider {
						m.Shadowed = append(m.Shadowed, Shadowed{Source: p.Provider, From: pointer(Kinds[k], i), Name: e.name, By: by})
						continue
					}
					taken[e.name] = p.Provider
				}
				picks[k] = append(picks[k], pick{p, e, i})
			}
		}
	}

	// Where the picks go into base: at the end of an array, after its last
	// element or inside it when empty, or, for a kind base leaves out, as a
	// member added at the end of the object.
	type insertion struct {
		at   int
		kind int
	}
	var ins []insertion
	for k := range Kinds {
		if len(picks[k]) == 0 {
			continue
		}
		at := bd.end
		if a := bd.arrays[k]; a != nil {
			at = a.close
			if len(a.elems) == 0 {
				at = a.open + 1
			}
		}
		ins = append(ins, insertion{at, k})
	}
	slices.SortStableFunc(ins, func(a, b insertion) int { return a.at - b.at })

	if len(ins) == 0 {
		// Nothing is added: the merged config is base itself.
		m.Data = base
		m.segments = []segment{{start: 0, end: len(base), from: 0}}
	} else {
		var out bytes.Buffer
		n := len(base)
		for _, p := range parts {
			n += p.Bytes()
		}
		out.Grow(n)
		from := 0
		copyBase := func(to int) {
			if to > from {
				m.segments = append(m.segments, segment{start: out.Len(), end: out.Len() + to - from, from: from})
				out.Write(base[from:to])
				from = to
			}
		}
		glue := func(s string) {
			m.segments = append(m.segments, segment{start: out.Len(), end: out.Len() + len(s), from: -1})
			out.WriteString(s)
		}
		members := bd.members
		for _, in := range ins {
			copyBase(in.at)
			a := bd.arrays[in.kind]
			comma := a != nil && len(a.elems) > 0
			if a == nil {
				if members > 0 {
					glue(",")
				}
				glue(strconv.Quote(Kinds[in.kind]) + ":[")
				members++
			}
			for _, p := range picks[in.kind] {
				if comma {
					glue(",")
				}
				comma = true
				raw := p.elem.raw(p.part.doc.data)
				m.segments = append(m.segments, segment{start: out.Len(), end: out.Len() + len(raw), part: p.part, from: p.elem.start})
				out.Write(raw)
				m.origins[in.kind] = append(m.origins[in.kind], origin{p.part, p.idx})
			}
			if a == nil {
				glue("]")
			}
		}
		copyBase(len(base))
		m.Data = out.Bytes()
	}

	// The sources, in the order of the merged config's members.
	keys := slices.Clone(bd.keys)
	for _, in := range ins {
		if bd.arrays[in.kind] == nil {
			keys = append(keys, Kinds[in.kind])
		}
	}
	for _, key := range keys {
		k := slices.Index(Kinds, key)
		if k < 0 {
			m.Sources = append(m.Sources, Source{Path: "/" + key, Source: baseSource})
			continue
		}
		n := 0
		if a := bd.arrays[k]; a != nil {
			for range a.elems {
				m.Sources = append(m.Sources, Source{Path: pointer(key, n), Source: baseSource})
				n++
			}
		}
		for _, o := range m.origins[k] {
			m.Sources = append(m.Sources, Source{Path: pointer(key, n), Source: o.part.Provider, From: pointer(key, o.index)})
			n++
		}
	}
	return m, nil
}

func pointer(kind string, i int) string { return "/" + kind + "/" + strconv.Itoa(i) }

// errorPath matches a config error's path that starts in an element of one
// of Kinds.
var errorPath = regexp.MustCompile(`^(listeners|routes|upstreams|services)\[(\d+)\]`)

// Errors maps err, what loading m.Data gave, to what was written: an error
// in the base config gets its position there, and one in a provider's
// element gets its path and position in what that provider provided, and
// says which provider that was. An err that is not config errors is
// returned as it is.
func (m *Merged) Errors(err error) error {
	var es config.Errors
	var e1 *config.Error
	switch {
	case errors.As(err, &es):
	case errors.As(err, &e1):
		es = config.Errors{e1}
	default:
		return err
	}
	out := make(config.Errors, len(es))
	for i, e := range es {
		out[i] = m.mapError(e)
	}
	return out
}

func (m *Merged) mapError(e *config.Error) *config.Error {
	me := *e
	var seg *segment
	off := -1
	if e.Line > 0 {
		off = offset(m.Data, e.Line, e.Col)
		i, found := slices.BinarySearchFunc(m.segments, off, func(s segment, off int) int {
			switch {
			case s.end <= off:
				return -1
			case s.start > off:
				return 1
			}
			return 0
		})
		if found {
			seg = &m.segments[i]
		}
	}
	var part *Part
	if sm := errorPath.FindStringSubmatchIndex(e.Path); sm != nil {
		k := slices.Index(Kinds, e.Path[sm[2]:sm[3]])
		n, _ := strconv.Atoi(e.Path[sm[4]:sm[5]])
		if a := m.base.arrays[k]; a != nil {
			n -= len(a.elems)
		}
		if n >= 0 && n < len(m.origins[k]) {
			o := m.origins[k][n]
			part = o.part
			me.Path = Kinds[k] + "[" + strconv.Itoa(o.index) + "]" + e.Path[sm[1]:]
		}
	} else if seg != nil {
		part = seg.part
	}
	me.Line, me.Col = 0, 0
	switch {
	case seg == nil || seg.from < 0:
	case part == nil && seg.part == nil:
		me.Line, me.Col = position(m.base.data, seg.from+off-seg.start)
	case part != nil && seg.part == part:
		me.Line, me.Col = position(part.doc.data, seg.from+off-seg.start)
	}
	if part != nil {
		me.Msg += " (provided by " + part.Provider + ")"
	}
	return &me
}

// position returns the 1-based line and column, counted in characters, of
// off in data, as config's errors give them.
func position(data []byte, off int) (line, col int) {
	off = min(max(off, 0), len(data))
	start := bytes.LastIndexByte(data[:off], '\n') + 1
	return bytes.Count(data[:off], []byte{'\n'}) + 1, utf8.RuneCount(data[start:off]) + 1
}

// offset is position's inverse.
func offset(data []byte, line, col int) int {
	off := 0
	for ; line > 1; line-- {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			return len(data)
		}
		off += i + 1
	}
	for ; col > 1 && off < len(data) && data[off] != '\n'; col-- {
		_, n := utf8.DecodeRune(data[off:])
		off += n
	}
	return off
}

func skipSpace(data []byte, off int) int {
	for off < len(data) && (data[off] == ' ' || data[off] == '\t' || data[off] == '\r' || data[off] == '\n') {
		off++
	}
	return off
}

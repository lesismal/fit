package fitconf

import (
	"bytes"
	"strings"
)

// compactWidth is the widest a block written on one line may be, its
// indentation counted as four columns a tab.
const compactWidth = 80

// compact writes a .conf file again with each block that fits within
// compactWidth on one line, its directives parted with ;, as in
// match { path /api/* }. At the top, a blank line goes between directives
// but those of one line of the same name, as listeners written each on one.
func compact(src []byte) ([]byte, error) {
	ds, err := parse("", src)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	writeCompact(&b, ds, 0)
	return b.Bytes(), nil
}

func writeCompact(b *bytes.Buffer, ds []*directive, depth int) {
	indent := strings.Repeat("\t", depth)
	prevName, prevOneLine := "", true
	for i, d := range ds {
		one, ok := inline(d)
		ok = ok && (!d.hasBlock || 4*depth+len(one) <= compactWidth)
		if depth == 0 && i > 0 && (!ok || !prevOneLine || d.name.text != prevName) {
			b.WriteByte('\n')
		}
		prevName, prevOneLine = d.name.text, ok
		if ok {
			b.WriteString(indent + one + "\n")
			continue
		}
		b.WriteString(indent + strings.Join(words(d), " ") + " {\n")
		writeCompact(b, d.block, depth+1)
		b.WriteString(indent + "}\n")
	}
}

// words returns a directive's name and arguments as written.
func words(d *directive) []string {
	ws := []string{tokenWord(d.name)}
	for _, a := range d.args {
		ws = append(ws, tokenWord(a))
	}
	return ws
}

// tokenWord writes t so that it reads back as it was read: quoted if it
// was.
func tokenWord(t token) string {
	if t.quoted {
		return quote(t.text)
	}
	return t.text
}

// inline returns d on one line, or false when a block in it is wider than
// compactWidth.
func inline(d *directive) (string, bool) {
	s := strings.Join(words(d), " ")
	if !d.hasBlock {
		return s, true
	}
	if len(d.block) == 0 {
		return s + " {}", true
	}
	parts := make([]string, len(d.block))
	for i, e := range d.block {
		p, ok := inline(e)
		if !ok || len(p) > compactWidth {
			return "", false
		}
		parts[i] = p
	}
	return s + " { " + strings.Join(parts, "; ") + " }", true
}

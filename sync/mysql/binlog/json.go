package binlog

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	fitsync "github.com/lesismal/fit/sync"
)

// MySQL's binary JSON (sql-common/json_binary): a type byte, then the
// value; objects and arrays are a count and a size, then an entry per key
// (its offset and length) and per value (its type and its offset, or the
// value itself when it fits), offsets from the container's start.

// JSON value types.
const (
	jsonSmallObject = 0x00
	jsonLargeObject = 0x01
	jsonSmallArray  = 0x02
	jsonLargeArray  = 0x03
	jsonLiteral     = 0x04
	jsonInt16       = 0x05
	jsonUint16      = 0x06
	jsonInt32       = 0x07
	jsonUint32      = 0x08
	jsonInt64       = 0x09
	jsonUint64      = 0x0a
	jsonDouble      = 0x0b
	jsonString      = 0x0c
	jsonOpaque      = 0x0f
)

// jnode is a JSON document parsed: an object (keys and vals), an array
// (vals) or a scalar (its text).
type jnode struct {
	kind byte // 'o', 'a' or 's'
	keys []string
	vals []*jnode
	text string
}

// jsonParser parses one document within a budget of its text's length:
// offsets may point many entries at one value, which must not make the
// text grow past what the bytes can mean.
type jsonParser struct {
	budget int
}

func jsonBudget(n int) int { return 16*n + 64 }

// json reads a JSON column: its length in Meta bytes, then the binary
// document. It returns the document's text and its bytes.
func (dec *rowDecoder) json(r *rd, c *Column) (fitsync.Value, []byte, error) {
	b, err := blob(r, c)
	if err != nil {
		return fitsync.Value{}, nil, err
	}
	if len(b) == 0 {
		// An empty value is what MySQL writes for a JSON column without
		// one, read as the JSON null.
		return fitsync.JSON([]byte("null")), b, nil
	}
	n, err := parseJSON(b)
	if err != nil {
		return fitsync.Value{}, nil, err
	}
	return fitsync.JSON([]byte(n.String())), b, nil
}

// parseJSON parses a binary JSON document.
func parseJSON(b []byte) (*jnode, error) {
	if len(b) < 1 {
		return nil, malformed("an empty JSON document")
	}
	p := &jsonParser{budget: jsonBudget(len(b))}
	return p.value(b[0], b[1:], 0)
}

func (p *jsonParser) spend(n int) error {
	p.budget -= n
	if p.budget < 0 {
		return fmt.Errorf("%w: a JSON document's text", ErrTooLarge)
	}
	return nil
}

func (p *jsonParser) value(t byte, b []byte, depth int) (*jnode, error) {
	if depth > maxJSONDepth {
		return nil, malformed("a JSON document deeper than %d", maxJSONDepth)
	}
	switch t {
	case jsonSmallObject, jsonLargeObject, jsonSmallArray, jsonLargeArray:
		return p.container(t, b, depth)
	case jsonLiteral:
		if len(b) < 1 {
			break
		}
		return p.literal(b[0])
	case jsonInt16:
		if len(b) < 2 {
			break
		}
		return p.scalar(strconv.FormatInt(int64(int16(binary.LittleEndian.Uint16(b))), 10))
	case jsonUint16:
		if len(b) < 2 {
			break
		}
		return p.scalar(strconv.FormatUint(uint64(binary.LittleEndian.Uint16(b)), 10))
	case jsonInt32:
		if len(b) < 4 {
			break
		}
		return p.scalar(strconv.FormatInt(int64(int32(binary.LittleEndian.Uint32(b))), 10))
	case jsonUint32:
		if len(b) < 4 {
			break
		}
		return p.scalar(strconv.FormatUint(uint64(binary.LittleEndian.Uint32(b)), 10))
	case jsonInt64:
		if len(b) < 8 {
			break
		}
		return p.scalar(strconv.FormatInt(int64(binary.LittleEndian.Uint64(b)), 10))
	case jsonUint64:
		if len(b) < 8 {
			break
		}
		return p.scalar(strconv.FormatUint(binary.LittleEndian.Uint64(b), 10))
	case jsonDouble:
		if len(b) < 8 {
			break
		}
		f := math.Float64frombits(binary.LittleEndian.Uint64(b))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			break
		}
		return p.scalar(strconv.FormatFloat(f, 'g', -1, 64))
	case jsonString:
		s, ok := jsonVarString(b)
		if !ok {
			break
		}
		return p.scalar(quote(s))
	case jsonOpaque:
		if len(b) < 1 {
			break
		}
		data, ok := jsonVarString(b[1:])
		if !ok {
			break
		}
		s, err := opaque(b[0], data)
		if err != nil {
			return nil, err
		}
		return p.scalar(s)
	default:
		return nil, malformed("JSON type %d", t)
	}
	return nil, malformed("a JSON value of type %d", t)
}

func (p *jsonParser) scalar(s string) (*jnode, error) {
	if err := p.spend(len(s)); err != nil {
		return nil, err
	}
	return &jnode{kind: 's', text: s}, nil
}

func (p *jsonParser) literal(c byte) (*jnode, error) {
	switch c {
	case 0:
		return p.scalar("null")
	case 1:
		return p.scalar("true")
	case 2:
		return p.scalar("false")
	}
	return nil, malformed("JSON literal %d", c)
}

// jsonVarString reads a length, seven bits a byte, the lowest first, then
// as many bytes.
func jsonVarString(b []byte) ([]byte, bool) {
	var n uint64
	for i := 0; i < 5; i++ {
		if i >= len(b) {
			return nil, false
		}
		n |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			b = b[i+1:]
			if n > uint64(len(b)) {
				return nil, false
			}
			return b[:n], true
		}
	}
	return nil, false
}

func (p *jsonParser) container(t byte, b []byte, depth int) (*jnode, error) {
	large := t == jsonLargeObject || t == jsonLargeArray
	obj := t == jsonSmallObject || t == jsonLargeObject
	w := 2
	if large {
		w = 4
	}
	word := func(at int) (int, bool) {
		if at < 0 || at+w > len(b) {
			return 0, false
		}
		if large {
			return int(binary.LittleEndian.Uint32(b[at:])), true
		}
		return int(binary.LittleEndian.Uint16(b[at:])), true
	}
	count, ok1 := word(0)
	size, ok2 := word(w)
	if !ok1 || !ok2 || size > len(b) || count < 0 || size < 0 {
		return nil, malformed("a JSON container")
	}
	b = b[:size]
	keyEntry := w + 2
	valEntry := 1 + w
	head := 2 * w
	if obj {
		head += count * keyEntry
	}
	if count > len(b) || head+count*valEntry > len(b) {
		return nil, malformed("a JSON container of %d elements", count)
	}
	if err := p.spend(2 + count); err != nil {
		return nil, err
	}
	n := &jnode{kind: 'a', vals: make([]*jnode, count)}
	if obj {
		n.kind = 'o'
		n.keys = make([]string, count)
		for i := range count {
			at := 2*w + i*keyEntry
			off, _ := word(at)
			kl := int(binary.LittleEndian.Uint16(b[at+w:]))
			if off+kl > len(b) || off < 0 {
				return nil, malformed("a JSON key")
			}
			k := quote(b[off : off+kl])
			if err := p.spend(len(k) + 1); err != nil {
				return nil, err
			}
			n.keys[i] = k
		}
	}
	for i := range count {
		at := head + i*valEntry
		vt := b[at]
		inline := b[at+1 : at+1+w]
		var v *jnode
		var err error
		switch {
		case vt == jsonLiteral || vt == jsonInt16 || vt == jsonUint16 ||
			large && (vt == jsonInt32 || vt == jsonUint32):
			v, err = p.value(vt, inline, depth+1)
		default:
			off, _ := word(at + 1)
			if off >= len(b) {
				return nil, malformed("a JSON value's offset")
			}
			v, err = p.value(vt, b[off:], depth+1)
		}
		if err != nil {
			return nil, err
		}
		n.vals[i] = v
	}
	return n, nil
}

// opaque is the text of an opaque JSON value of MySQL type t: a DECIMAL as
// its number, a date or time as a string as MySQL writes it, anything
// else as "base64:type<t>:<base64>".
func opaque(t byte, data []byte) (string, error) {
	switch t {
	case TypeNewDecimal:
		if len(data) < 2 {
			return "", malformed("a JSON decimal")
		}
		r := rd{b: data[2:]}
		return decodeDecimal(&r, int(data[0]), int(data[1]))
	case TypeDate, TypeDateTime, TypeTimestamp, TypeTime:
		if len(data) < 8 {
			return "", malformed("a JSON date or time")
		}
		v := int64(binary.LittleEndian.Uint64(data))
		neg := v < 0
		if neg {
			v = -v
		}
		us := int(v % (1 << 24))
		x := v >> 24
		if t == TypeTime {
			return quote([]byte(formatTime(neg, int(x>>12%(1<<10)), int(x>>6%64), int(x%64), us, 6))), nil
		}
		ymd, hms := x>>17, x%(1<<17)
		ym := ymd >> 5
		s := fmt.Sprintf("%04d-%02d-%02d", ym/13, ym%13, ymd%32)
		if t != TypeDate {
			s += fmt.Sprintf(" %02d:%02d:%02d.%06d", hms>>12, hms>>6%64, hms%64, us)
		}
		return quote([]byte(s)), nil
	}
	return quote([]byte(fmt.Sprintf("base64:type%d:%s", t, base64.StdEncoding.EncodeToString(data)))), nil
}

// quote is s as a JSON string; bytes that are not UTF-8 become U+FFFD.
func quote(s []byte) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for len(s) > 0 {
		r, n := utf8.DecodeRune(s)
		s = s[n:]
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// String is n's JSON text, as MySQL writes it.
func (n *jnode) String() string {
	var b strings.Builder
	n.write(&b)
	return b.String()
}

func (n *jnode) write(b *strings.Builder) {
	switch n.kind {
	case 'o':
		b.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k)
			b.WriteString(": ")
			n.vals[i].write(b)
		}
		b.WriteByte('}')
	case 'a':
		b.WriteByte('[')
		for i, v := range n.vals {
			if i > 0 {
				b.WriteString(", ")
			}
			v.write(b)
		}
		b.WriteByte(']')
	default:
		b.WriteString(n.text)
	}
}

// Partial JSON updates (binlog_row_value_options=PARTIAL_JSON): the after
// value of a JSON column is a list of changes to the before value, each
// an operation, a path and (but for a removal) a binary value.
const (
	jsonDiffReplace = 0
	jsonDiffInsert  = 1
	jsonDiffRemove  = 2
)

// partialJSON reads column i's changes and applies them to the before
// image's value.
func (dec *rowDecoder) partialJSON(r *rd, i int) (fitsync.Value, error) {
	b := r.take(int(r.u32()))
	if r.bad {
		return fitsync.Value{}, malformed("a partial JSON update")
	}
	if i >= len(dec.before) || dec.before[i] == nil {
		return fitsync.Value{}, fmt.Errorf("%w: a partial JSON update without the value before it (binlog_row_image must be FULL)", ErrUnsupported)
	}
	doc, err := parseJSON(dec.before[i])
	if err != nil {
		return fitsync.Value{}, err
	}
	budget := jsonBudget(len(dec.before[i]) + len(b))
	d := rd{b: b}
	for len(d.b) > 0 {
		op := d.u8()
		path := d.lenEncBytes()
		var v *jnode
		if op != jsonDiffRemove {
			vb := d.lenEncBytes()
			if d.bad || len(vb) == 0 {
				return fitsync.Value{}, malformed("a partial JSON update's value")
			}
			p := &jsonParser{budget: budget}
			if v, err = p.value(vb[0], vb[1:], 0); err != nil {
				return fitsync.Value{}, err
			}
		}
		if d.bad || op > jsonDiffRemove {
			return fitsync.Value{}, malformed("a partial JSON update")
		}
		if doc, err = applyDiff(doc, op, string(path), v); err != nil {
			return fitsync.Value{}, err
		}
	}
	return fitsync.JSON([]byte(doc.String())), nil
}

// pathLeg is a step of a JSON path: a member's key (quoted, as the
// document's keys are kept) or an array's index.
type pathLeg struct {
	key   string
	index int
	isKey bool
}

// parsePath reads a path as MySQL writes a partial update's: $, then
// .key, ."quoted key" or [n] legs.
func parsePath(s string) ([]pathLeg, error) {
	if !strings.HasPrefix(s, "$") {
		return nil, malformed("JSON path %q", s)
	}
	s = s[1:]
	var legs []pathLeg
	for len(s) > 0 {
		switch s[0] {
		case '.':
			s = s[1:]
			var key string
			if strings.HasPrefix(s, `"`) {
				end := 1
				for end < len(s) && s[end] != '"' {
					if s[end] == '\\' {
						end++
					}
					end++
				}
				if end >= len(s) {
					return nil, malformed("JSON path's key")
				}
				k, err := strconv.Unquote(s[:end+1])
				if err != nil {
					return nil, malformed("JSON path's key %s", s[:end+1])
				}
				key, s = k, s[end+1:]
			} else {
				end := strings.IndexAny(s, ".[")
				if end < 0 {
					end = len(s)
				}
				key, s = s[:end], s[end:]
			}
			legs = append(legs, pathLeg{key: quote([]byte(key)), isKey: true})
		case '[':
			end := strings.IndexByte(s, ']')
			if end < 0 {
				return nil, malformed("JSON path's index")
			}
			n, err := strconv.Atoi(strings.TrimSpace(s[1:end]))
			if err != nil || n < 0 {
				return nil, malformed("JSON path's index %q", s[1:end])
			}
			legs = append(legs, pathLeg{index: n})
			s = s[end+1:]
		default:
			return nil, malformed("JSON path at %q", s)
		}
	}
	return legs, nil
}

// applyDiff applies one change to doc and returns the document after it.
func applyDiff(doc *jnode, op byte, path string, v *jnode) (*jnode, error) {
	legs, err := parsePath(path)
	if err != nil {
		return nil, err
	}
	if len(legs) == 0 {
		if op != jsonDiffReplace {
			return nil, malformed("a JSON update of the whole document")
		}
		return v, nil
	}
	parent := doc
	for _, l := range legs[:len(legs)-1] {
		i := parent.find(l)
		if i < 0 {
			return nil, malformed("JSON path %s not in the document", path)
		}
		parent = parent.vals[i]
	}
	last := legs[len(legs)-1]
	i := parent.find(last)
	switch {
	case op == jsonDiffRemove && i >= 0:
		parent.vals = append(parent.vals[:i], parent.vals[i+1:]...)
		if parent.kind == 'o' {
			parent.keys = append(parent.keys[:i], parent.keys[i+1:]...)
		}
	case op == jsonDiffReplace && i >= 0:
		parent.vals[i] = v
	case op == jsonDiffInsert && i < 0 && parent.kind == 'o' && last.isKey:
		// Keys are kept in the binary form's order, by length, then by
		// bytes.
		at := len(parent.keys)
		for j, k := range parent.keys {
			if len(k) > len(last.key) || len(k) == len(last.key) && k > last.key {
				at = j
				break
			}
		}
		parent.keys = append(parent.keys[:at], append([]string{last.key}, parent.keys[at:]...)...)
		parent.vals = append(parent.vals[:at], append([]*jnode{v}, parent.vals[at:]...)...)
	case op == jsonDiffInsert && parent.kind == 'a' && !last.isKey:
		at := min(last.index, len(parent.vals))
		parent.vals = append(parent.vals[:at], append([]*jnode{v}, parent.vals[at:]...)...)
	default:
		return nil, malformed("JSON update %d of %s", op, path)
	}
	return doc, nil
}

// find is the position of leg l in n, -1 if none.
func (n *jnode) find(l pathLeg) int {
	switch {
	case n.kind == 'o' && l.isKey:
		for i, k := range n.keys {
			if k == l.key {
				return i
			}
		}
	case n.kind == 'a' && !l.isKey && l.index < len(n.vals):
		return l.index
	}
	return -1
}

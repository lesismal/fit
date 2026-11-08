package mongowire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	fitsync "github.com/lesismal/fit/sync"
)

// BSON and the record's values.
//
// Reading, a BSON value becomes the record value of its kind: a double a
// float, an int32 or int64 an int, a decimal128 a decimal, a string a
// string, binary of subtype 0 bytes, a datetime a time in UTC, a document
// a map (its fields in order), an array an array, a bool a bool, null null.
// A type with no match is kept as a tagged value: a map of one field named
// as MongoDB's canonical extended JSON names it, as {"$oid": "<24 hex
// digits>"}, {"$timestamp": {"t": <int>, "i": <int>}}, {"$binary":
// {"base64": "...", "subType": "04"}}, {"$regularExpression": {"pattern":
// "...", "options": "..."}}, {"$numberDecimal": "NaN"} (a decimal128 that is
// not a number), {"$minKey": 1}, {"$maxKey": 1}, {"$undefined": true},
// {"$symbol": "..."}, {"$code": "..."}, {"$code": "...", "$scope": {...}}
// and {"$dbPointer": {"$ref": "...", "$id": {"$oid": "..."}}}.
//
// Writing goes the other way: a tagged value is written as its type again,
// so a document read and written back is the same document. An int is an
// int64; a uint an int64, or a decimal128 past int64's range; a decimal a
// decimal128 (an error when 34 digits cannot hold it exactly); a time or a
// date a datetime, to the millisecond; bytes binary of subtype 0; a JSON
// value the document, array or scalar it holds.

func tag(name string, v fitsync.Value) fitsync.Value {
	return fitsync.Map(fitsync.Field{Name: name, Value: v})
}

// Value returns r as a map of its fields.
func (r Raw) Value() (fitsync.Value, error) {
	return RawValue{Type: TypeDocument, Data: r}.Value()
}

// String returns r as plain JSON, for messages and logs.
func (r Raw) String() string {
	v, err := r.Value()
	if err != nil {
		return "<" + err.Error() + ">"
	}
	b, err := fitsync.AppendPlain(nil, v)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// Value returns v as a record value.
func (v RawValue) Value() (fitsync.Value, error) {
	if n, err := valueSize(v.Type, v.Data, 0); err != nil {
		return fitsync.Value{}, err
	} else if n != len(v.Data) {
		return fitsync.Value{}, malformed("%d bytes after a value", len(v.Data)-n)
	}
	return decodeValue(v.Type, v.Data, 0)
}

func decodeDoc(b []byte, array bool, depth int) (fitsync.Value, error) {
	if depth > maxDepth {
		return fitsync.Value{}, malformed("nested deeper than %d", maxDepth)
	}
	var fields []fitsync.Field
	var elems []fitsync.Value
	err := Raw(b).Each(func(key string, e RawValue) error {
		x, err := decodeValue(e.Type, e.Data, depth+1)
		if err != nil {
			return err
		}
		if array {
			elems = append(elems, x)
		} else {
			fields = append(fields, fitsync.Field{Name: key, Value: x})
		}
		return nil
	})
	if err != nil {
		return fitsync.Value{}, err
	}
	if array {
		return fitsync.Array(elems...), nil
	}
	return fitsync.Map(fields...), nil
}

func cstrings(b []byte) (string, string) {
	i := bytes.IndexByte(b, 0)
	j := bytes.IndexByte(b[i+1:], 0)
	return string(b[:i]), string(b[i+1 : i+1+j])
}

// decodeValue reads a value of type t whose bytes b have been checked.
func decodeValue(t byte, b []byte, depth int) (fitsync.Value, error) {
	switch t {
	case TypeDouble:
		return fitsync.Float(math.Float64frombits(binary.LittleEndian.Uint64(b))), nil
	case TypeString:
		return fitsync.String(string(b[4 : len(b)-1])), nil
	case TypeDocument:
		return decodeDoc(b, false, depth)
	case TypeArray:
		return decodeDoc(b, true, depth)
	case TypeBinary:
		bin, _ := RawValue{Type: t, Data: b}.Binary()
		if bin.Subtype == 0 {
			return fitsync.Bytes(bin.Data), nil
		}
		return tag("$binary", fitsync.Map(
			fitsync.Field{Name: "base64", Value: fitsync.String(base64.StdEncoding.EncodeToString(bin.Data))},
			fitsync.Field{Name: "subType", Value: fitsync.String(fmt.Sprintf("%02x", bin.Subtype))})), nil
	case TypeUndefined:
		return tag("$undefined", fitsync.Bool(true)), nil
	case TypeObjectID:
		id, _ := RawValue{Type: t, Data: b}.ObjectID()
		return tag("$oid", fitsync.String(id.String())), nil
	case TypeBool:
		return fitsync.Bool(b[0] == 1), nil
	case TypeDateTime:
		tm, _ := RawValue{Type: t, Data: b}.Time()
		return fitsync.Time(tm), nil
	case TypeNull:
		return fitsync.Null(), nil
	case TypeRegex:
		p, o := cstrings(b)
		return tag("$regularExpression", fitsync.Map(
			fitsync.Field{Name: "pattern", Value: fitsync.String(p)},
			fitsync.Field{Name: "options", Value: fitsync.String(o)})), nil
	case TypeDBPointer:
		n, _ := strSize(b)
		var id ObjectID
		copy(id[:], b[n:])
		return tag("$dbPointer", fitsync.Map(
			fitsync.Field{Name: "$ref", Value: fitsync.String(string(b[4 : n-1]))},
			fitsync.Field{Name: "$id", Value: tag("$oid", fitsync.String(id.String()))})), nil
	case TypeJavaScript:
		return tag("$code", fitsync.String(string(b[4:len(b)-1]))), nil
	case TypeSymbol:
		return tag("$symbol", fitsync.String(string(b[4:len(b)-1]))), nil
	case TypeCodeWithScope:
		n, _ := strSize(b[4:])
		scope, err := decodeDoc(b[4+n:], false, depth)
		if err != nil {
			return fitsync.Value{}, err
		}
		return fitsync.Map(
			fitsync.Field{Name: "$code", Value: fitsync.String(string(b[8 : 4+n-1]))},
			fitsync.Field{Name: "$scope", Value: scope}), nil
	case TypeInt32:
		return fitsync.Int(int64(int32(binary.LittleEndian.Uint32(b)))), nil
	case TypeTimestamp:
		ts, _ := RawValue{Type: t, Data: b}.Timestamp()
		return tag("$timestamp", fitsync.Map(
			fitsync.Field{Name: "t", Value: fitsync.Int(int64(ts.T))},
			fitsync.Field{Name: "i", Value: fitsync.Int(int64(ts.I))})), nil
	case TypeInt64:
		return fitsync.Int(int64(binary.LittleEndian.Uint64(b))), nil
	case TypeDecimal128:
		d := decimal128(b)
		if d.IsNaN() || d.IsInf() {
			return tag("$numberDecimal", fitsync.String(d.String())), nil
		}
		return fitsync.Decimal(d.String()), nil
	case TypeMinKey:
		return tag("$minKey", fitsync.Int(1)), nil
	case TypeMaxKey:
		return tag("$maxKey", fitsync.Int(1)), nil
	}
	return fitsync.Value{}, malformed("type 0x%02x", t)
}

// MarshalValue writes a map as a BSON document.
func MarshalValue(v fitsync.Value) (Raw, error) {
	if v.Kind() != fitsync.KindMap {
		return nil, fmt.Errorf("mongowire: a document from a %s", v.Kind())
	}
	if _, ok := tagged(v.Fields()); ok {
		return nil, errors.New("mongowire: a document from a tagged value")
	}
	b, err := appendFields(nil, v.Fields(), 0)
	return Raw(b), err
}

func appendFields(dst []byte, fields []fitsync.Field, depth int) ([]byte, error) {
	start := len(dst)
	dst = append(dst, 0, 0, 0, 0)
	for _, f := range fields {
		var err error
		if dst, err = appendValue(dst, f.Name, f.Value, depth+1); err != nil {
			return dst, err
		}
	}
	return endDocument(dst, start), nil
}

func appendValue(dst []byte, key string, v fitsync.Value, depth int) ([]byte, error) {
	if depth > maxDepth {
		return dst, fmt.Errorf("mongowire: a value nested deeper than %d", maxDepth)
	}
	var err error
	switch v.Kind() {
	case fitsync.KindNull:
		return appendElem(dst, key, nil)
	case fitsync.KindBool:
		return appendElem(dst, key, v.AsBool())
	case fitsync.KindInt:
		return appendElem(dst, key, v.AsInt())
	case fitsync.KindUint:
		if u := v.AsUint(); u <= math.MaxInt64 {
			return appendElem(dst, key, int64(u))
		}
		d, err := ParseDecimal128(strconv.FormatUint(v.AsUint(), 10))
		if err != nil {
			return dst, err
		}
		return appendElem(dst, key, d)
	case fitsync.KindFloat:
		return appendElem(dst, key, v.AsFloat())
	case fitsync.KindDecimal:
		d, err := ParseDecimal128(v.Text())
		if err != nil {
			return dst, err
		}
		return appendElem(dst, key, d)
	case fitsync.KindString:
		return appendElem(dst, key, v.Text())
	case fitsync.KindBytes:
		if dst, err = appendKey(dst, TypeBinary, key); err == nil {
			s := v.Text()
			dst = binary.LittleEndian.AppendUint32(dst, uint32(len(s)))
			dst = append(dst, 0)
			dst = append(dst, s...)
		}
		return dst, err
	case fitsync.KindTime, fitsync.KindDate:
		return appendElem(dst, key, v.AsTime())
	case fitsync.KindJSON:
		return appendJSON(dst, key, []byte(v.Text()), depth)
	case fitsync.KindArray:
		if dst, err = appendKey(dst, TypeArray, key); err != nil {
			return dst, err
		}
		start := len(dst)
		dst = append(dst, 0, 0, 0, 0)
		for i, e := range v.Elems() {
			if dst, err = appendValue(dst, itoa(i), e, depth+1); err != nil {
				return dst, err
			}
		}
		return endDocument(dst, start), nil
	case fitsync.KindMap:
		if x, ok := tagged(v.Fields()); ok {
			return appendElem(dst, key, x)
		}
		if dst, err = appendKey(dst, TypeDocument, key); err != nil {
			return dst, err
		}
		return appendFields(dst, v.Fields(), depth)
	}
	return dst, fmt.Errorf("mongowire: cannot write a %s", v.Kind())
}

// tagged returns the BSON value a tagged map stands for, and whether it is
// one.
func tagged(fs []fitsync.Field) (any, bool) {
	str := func(v fitsync.Value) (string, bool) { return v.Text(), v.Kind() == fitsync.KindString }
	if len(fs) == 2 && fs[0].Name == "$code" && fs[1].Name == "$scope" {
		code, ok := str(fs[0].Value)
		if !ok || fs[1].Value.Kind() != fitsync.KindMap {
			return nil, false
		}
		scope, err := appendFields(nil, fs[1].Value.Fields(), 1)
		if err != nil {
			return nil, false
		}
		var b []byte
		b = binary.LittleEndian.AppendUint32(b, 0)
		b = appendString(b, code)
		b = append(b, scope...)
		binary.LittleEndian.PutUint32(b, uint32(len(b)))
		return RawValue{Type: TypeCodeWithScope, Data: b}, true
	}
	if len(fs) != 1 {
		return nil, false
	}
	v := fs[0].Value
	switch fs[0].Name {
	case "$oid":
		if s, ok := str(v); ok {
			if id, err := ParseObjectID(s); err == nil {
				return id, true
			}
		}
	case "$timestamp":
		t, ok1 := uint32Field(v, "t")
		i, ok2 := uint32Field(v, "i")
		if ok1 && ok2 && len(v.Fields()) == 2 {
			return Timestamp{T: t, I: i}, true
		}
	case "$binary":
		b64, ok1 := v.Get("base64")
		st, ok2 := v.Get("subType")
		if ok1 && ok2 && len(v.Fields()) == 2 && b64.Kind() == fitsync.KindString && st.Kind() == fitsync.KindString {
			data, err := base64.StdEncoding.DecodeString(b64.Text())
			n, err2 := strconv.ParseUint(st.Text(), 16, 8)
			if err == nil && err2 == nil && len(st.Text()) <= 2 {
				return Binary{Subtype: byte(n), Data: data}, true
			}
		}
	case "$regularExpression":
		p, ok1 := v.Get("pattern")
		o, ok2 := v.Get("options")
		if ok1 && ok2 && len(v.Fields()) == 2 && p.Kind() == fitsync.KindString && o.Kind() == fitsync.KindString &&
			bytes.IndexByte([]byte(p.Text()+o.Text()), 0) < 0 {
			return Regex{Pattern: p.Text(), Options: o.Text()}, true
		}
	case "$numberDecimal":
		if s, ok := str(v); ok {
			if d, err := ParseDecimal128(s); err == nil {
				return d, true
			}
		}
	case "$minKey":
		if v.Kind() == fitsync.KindInt && v.AsInt() == 1 {
			return MinKey{}, true
		}
	case "$maxKey":
		if v.Kind() == fitsync.KindInt && v.AsInt() == 1 {
			return MaxKey{}, true
		}
	case "$undefined":
		if v.AsBool() {
			return Undefined{}, true
		}
	case "$symbol":
		if s, ok := str(v); ok {
			return RawValue{Type: TypeSymbol, Data: appendString(nil, s)}, true
		}
	case "$code":
		if s, ok := str(v); ok {
			return RawValue{Type: TypeJavaScript, Data: appendString(nil, s)}, true
		}
	case "$dbPointer":
		ref, ok1 := v.Get("$ref")
		idv, ok2 := v.Get("$id")
		if ok1 && ok2 && len(v.Fields()) == 2 && ref.Kind() == fitsync.KindString && idv.Kind() == fitsync.KindMap {
			if x, ok := tagged(idv.Fields()); ok {
				if id, ok := x.(ObjectID); ok {
					return RawValue{Type: TypeDBPointer, Data: append(appendString(nil, ref.Text()), id[:]...)}, true
				}
			}
		}
	}
	return nil, false
}

func uint32Field(v fitsync.Value, name string) (uint32, bool) {
	f, ok := v.Get(name)
	if !ok {
		return 0, false
	}
	switch f.Kind() {
	case fitsync.KindInt:
		if n := f.AsInt(); n >= 0 && n <= math.MaxUint32 {
			return uint32(n), true
		}
	case fitsync.KindUint:
		if n := f.AsUint(); n <= math.MaxUint32 {
			return uint32(n), true
		}
	}
	return 0, false
}

// appendJSON writes the JSON document doc as the BSON value it holds: an
// object a document, an array an array; an integer an int64, or a
// decimal128 past its range; another number a double.
func appendJSON(dst []byte, key string, doc []byte, depth int) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	dst, err := appendJSONValue(dst, key, dec, depth)
	if err != nil {
		return dst, fmt.Errorf("mongowire: a JSON value: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return dst, errors.New("mongowire: a JSON value: more after it")
	}
	return dst, nil
}

func appendJSONValue(dst []byte, key string, dec *json.Decoder, depth int) ([]byte, error) {
	if depth > maxDepth {
		return dst, fmt.Errorf("nested deeper than %d", maxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return dst, err
	}
	switch x := tok.(type) {
	case json.Delim:
		t := TypeDocument
		if x == '[' {
			t = TypeArray
		}
		if dst, err = appendKey(dst, t, key); err != nil {
			return dst, err
		}
		start := len(dst)
		dst = append(dst, 0, 0, 0, 0)
		for i := 0; dec.More(); i++ {
			k := itoa(i)
			if t == TypeDocument {
				kt, err := dec.Token()
				if err != nil {
					return dst, err
				}
				k, _ = kt.(string)
			}
			if dst, err = appendJSONValue(dst, k, dec, depth+1); err != nil {
				return dst, err
			}
		}
		if _, err := dec.Token(); err != nil {
			return dst, err
		}
		return endDocument(dst, start), nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return appendElem(dst, key, i)
		}
		if d, err := ParseDecimal128(string(x)); err == nil && !strings.ContainsAny(string(x), ".eE") {
			return appendElem(dst, key, d)
		}
		f, err := x.Float64()
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return dst, err
		}
		return appendElem(dst, key, f)
	case string:
		return appendElem(dst, key, x)
	case bool:
		return appendElem(dst, key, x)
	case nil:
		return appendElem(dst, key, nil)
	}
	return dst, fmt.Errorf("a token %v", tok)
}

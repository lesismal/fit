package mongowire

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
)

// D is a BSON document written in order, as a command is.
type D []E

// E is one element of a D.
type E struct {
	Key   string
	Value any
}

// Doc returns a D of keys and values in turn, as Doc("insert", "c",
// "ordered", true), for packages other than this one, where vet asks for
// E's fields by name. It panics when a key is not a string or has no
// value: a mistake in the code that calls it.
func Doc(kv ...any) D {
	if len(kv)%2 != 0 {
		panic("mongowire.Doc: a key without a value")
	}
	d := make(D, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			panic(fmt.Sprintf("mongowire.Doc: a key of %T", kv[i]))
		}
		d = append(d, E{k, kv[i+1]})
	}
	return d
}

// A is a BSON array.
type A []any

// Marshal writes d as BSON. The values may be nil, bool, int, int32,
// int64, float64, string, []byte, Binary, ObjectID, Timestamp, time.Time,
// Regex, Decimal128, MinKey, MaxKey, Undefined, D, A, []D, []Raw, Raw (a
// document written as it is), RawValue, or a fitsync.Value.
func Marshal(d D) (Raw, error) {
	b, err := AppendDocument(nil, d)
	return Raw(b), err
}

// AppendDocument appends d as BSON to dst.
func AppendDocument(dst []byte, d D) ([]byte, error) {
	start := len(dst)
	dst = append(dst, 0, 0, 0, 0)
	for _, e := range d {
		var err error
		if dst, err = appendElem(dst, e.Key, e.Value); err != nil {
			return dst[:start], err
		}
	}
	return endDocument(dst, start), nil
}

func endDocument(dst []byte, start int) []byte {
	dst = append(dst, 0)
	binary.LittleEndian.PutUint32(dst[start:], uint32(len(dst)-start))
	return dst
}

func appendKey(dst []byte, t byte, key string) ([]byte, error) {
	if strings.IndexByte(key, 0) >= 0 {
		return dst, fmt.Errorf("mongowire: a key %q with a 0 byte", key)
	}
	dst = append(dst, t)
	dst = append(dst, key...)
	return append(dst, 0), nil
}

func appendString(dst []byte, s string) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(s)+1))
	dst = append(dst, s...)
	return append(dst, 0)
}

func appendCString(dst []byte, s string) ([]byte, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return dst, fmt.Errorf("mongowire: a regular expression %q with a 0 byte", s)
	}
	return append(append(dst, s...), 0), nil
}

func appendArray[T any](dst []byte, key string, elems []T) ([]byte, error) {
	dst, err := appendKey(dst, TypeArray, key)
	if err != nil {
		return dst, err
	}
	start := len(dst)
	dst = append(dst, 0, 0, 0, 0)
	for i, e := range elems {
		if dst, err = appendElem(dst, itoa(i), e); err != nil {
			return dst, err
		}
	}
	return endDocument(dst, start), nil
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

func appendElem(dst []byte, key string, v any) ([]byte, error) {
	var err error
	switch x := v.(type) {
	case fitsync.Value:
		return appendValue(dst, key, x, 0)
	case nil:
		return appendKey(dst, TypeNull, key)
	case bool:
		if dst, err = appendKey(dst, TypeBool, key); err == nil {
			dst = append(dst, b2u(x))
		}
	case int32:
		if dst, err = appendKey(dst, TypeInt32, key); err == nil {
			dst = binary.LittleEndian.AppendUint32(dst, uint32(x))
		}
	case int:
		return appendElem(dst, key, int64(x))
	case int64:
		if dst, err = appendKey(dst, TypeInt64, key); err == nil {
			dst = binary.LittleEndian.AppendUint64(dst, uint64(x))
		}
	case float64:
		if dst, err = appendKey(dst, TypeDouble, key); err == nil {
			dst = binary.LittleEndian.AppendUint64(dst, math.Float64bits(x))
		}
	case string:
		if dst, err = appendKey(dst, TypeString, key); err == nil {
			dst = appendString(dst, x)
		}
	case []byte:
		return appendElem(dst, key, Binary{Data: x})
	case Binary:
		if dst, err = appendKey(dst, TypeBinary, key); err == nil {
			if x.Subtype == 2 {
				dst = binary.LittleEndian.AppendUint32(dst, uint32(len(x.Data)+4))
				dst = append(dst, 2)
				dst = binary.LittleEndian.AppendUint32(dst, uint32(len(x.Data)))
			} else {
				dst = binary.LittleEndian.AppendUint32(dst, uint32(len(x.Data)))
				dst = append(dst, x.Subtype)
			}
			dst = append(dst, x.Data...)
		}
	case ObjectID:
		if dst, err = appendKey(dst, TypeObjectID, key); err == nil {
			dst = append(dst, x[:]...)
		}
	case Timestamp:
		if dst, err = appendKey(dst, TypeTimestamp, key); err == nil {
			dst = binary.LittleEndian.AppendUint32(dst, x.I)
			dst = binary.LittleEndian.AppendUint32(dst, x.T)
		}
	case time.Time:
		if dst, err = appendKey(dst, TypeDateTime, key); err == nil {
			dst = binary.LittleEndian.AppendUint64(dst, uint64(unixMilli(x)))
		}
	case Regex:
		if dst, err = appendKey(dst, TypeRegex, key); err == nil {
			if dst, err = appendCString(dst, x.Pattern); err == nil {
				dst, err = appendCString(dst, x.Options)
			}
		}
	case Decimal128:
		if dst, err = appendKey(dst, TypeDecimal128, key); err == nil {
			dst = x.append(dst)
		}
	case MinKey:
		return appendKey(dst, TypeMinKey, key)
	case MaxKey:
		return appendKey(dst, TypeMaxKey, key)
	case Undefined:
		return appendKey(dst, TypeUndefined, key)
	case D:
		if dst, err = appendKey(dst, TypeDocument, key); err == nil {
			dst, err = AppendDocument(dst, x)
		}
	case A:
		return appendArray(dst, key, x)
	case []D:
		return appendArray(dst, key, x)
	case []Raw:
		return appendArray(dst, key, x)
	case Raw:
		if err = x.Validate(); err != nil {
			return dst, err
		}
		if dst, err = appendKey(dst, TypeDocument, key); err == nil {
			dst = append(dst, x...)
		}
	case RawValue:
		if dst, err = appendKey(dst, x.Type, key); err == nil {
			dst = append(dst, x.Data...)
		}
	default:
		return dst, fmt.Errorf("mongowire: cannot write a %T", v)
	}
	return dst, err
}

func b2u(b bool) byte {
	if b {
		return 1
	}
	return 0
}

// unixMilli is t's milliseconds since the epoch, rounded down.
func unixMilli(t time.Time) int64 { return t.UnixMilli() }

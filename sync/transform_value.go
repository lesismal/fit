package fitsync

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Cast is the transform cast.
type Cast struct {
	Fields map[string]string `json:"fields" fit:"required" doc:"The fields cast, each to a type: string, int, uint, float, decimal, bool, bytes, time, date or json. Null stays null, and a value of the type already stays as it is. Numbers convert among themselves when the value fits exactly (1.5 is no int); strings parse as the type writes them (an int in decimal, a bool as true or false, a time in RFC 3339, a date as 2006-01-02, json as a document); a time takes an int or uint as Unix seconds, and a date; a date takes a time, its day in the time's own offset; json takes any value, as plain JSON; bytes take a string, decimal or json's text, and give a string their text when it is UTF-8. Anything else fails the record."`

	kinds map[string]Kind
	names map[string]bool
}

var castKinds = map[string]Kind{"string": KindString, "int": KindInt, "uint": KindUint, "float": KindFloat, "decimal": KindDecimal,
	"bool": KindBool, "bytes": KindBytes, "time": KindTime, "date": KindDate, "json": KindJSON}

// Provision checks the types.
func (c *Cast) Provision(ctx *module.Context) error {
	var e config.Errors
	c.kinds, c.names = map[string]Kind{}, map[string]bool{}
	for name, to := range c.Fields {
		k, ok := castKinds[to]
		if !ok {
			e = append(e, ctx.Params.Field("fields").Field(name).Errorf("%q is not one of string, int, uint, float, decimal, bool, bytes, time, date and json", to))
		}
		c.kinds[name], c.names[name] = k, true
	}
	if len(c.Fields) == 0 {
		e = append(e, ctx.Params.Field("fields").Errorf("no fields"))
	}
	return errs(e)
}

// Apply casts r's fields, in its rows and its key.
func (c *Cast) Apply(r *Record, emit func(*Record)) error {
	return editRecord(r, c.names, true, func(name string, v Value) (Value, error) { return convert(v, c.kinds[name]) }, emit)
}

// editRecord edits the fields named of r's rows, and of its key with key,
// and emits r, or its change when one changed.
func editRecord(r *Record, names map[string]bool, key bool, edit func(string, Value) (Value, error), emit func(*Record)) error {
	before, cb, err := editFields(r.Before, names, edit)
	if err != nil {
		return fmt.Errorf("before: %w", err)
	}
	after, ca, err := editFields(r.After, names, edit)
	if err != nil {
		return fmt.Errorf("after: %w", err)
	}
	k, ck := r.Key, false
	if key {
		if k, ck, err = editFields(r.Key, names, edit); err != nil {
			return fmt.Errorf("key: %w", err)
		}
	}
	if !cb && !ca && !ck {
		emit(r)
		return nil
	}
	o := r.Own()
	o.Before, o.After, o.Key = before, after, k
	emit(o)
	return nil
}

var decimalPattern = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)$`)

// integral returns s, a decimal, without a fraction of zeros, or false when
// the fraction is not all zeros.
func integral(s string) (string, bool) {
	i := strings.IndexByte(s, '.')
	if i < 0 {
		return s, true
	}
	if strings.Trim(s[i+1:], "0") != "" {
		return "", false
	}
	return s[:i], true
}

// convert casts v to the kind to, as Cast's doc says.
func convert(v Value, to Kind) (Value, error) {
	if v.kind == to || v.kind == KindNull {
		return v, nil
	}
	bad := fmt.Errorf("a %s cannot be cast to %s", v.kind, to)
	switch to {
	case KindString:
		switch v.kind {
		case KindArray, KindMap:
			return Value{}, bad
		case KindBytes:
			if !utf8.ValidString(v.str) {
				return Value{}, errors.New("bytes that are not UTF-8 cannot be cast to string")
			}
		}
		return String(textOf(v)), nil
	case KindInt, KindUint:
		var i int64
		var u uint64
		neg := false
		switch v.kind {
		case KindBool:
			u = v.num
		case KindInt:
			i = v.AsInt()
			neg, u = i < 0, uint64(i)
		case KindUint:
			u = v.num
		case KindFloat:
			f := v.AsFloat()
			if f != math.Trunc(f) || f < math.MinInt64 || f >= math.MaxUint64 {
				return Value{}, fmt.Errorf("%v is not an integer in range", f)
			}
			if neg = f < 0; neg {
				i = int64(f)
			} else {
				u = uint64(f)
			}
		case KindDecimal, KindString:
			s, ok := integral(v.str)
			if !ok {
				return Value{}, fmt.Errorf("%q is not an integer", v.str)
			}
			if strings.HasPrefix(s, "-") {
				var err error
				if i, err = strconv.ParseInt(s, 10, 64); err != nil {
					return Value{}, fmt.Errorf("%q is not an integer in range", v.str)
				}
				neg = true
			} else {
				var err error
				if u, err = strconv.ParseUint(strings.TrimPrefix(s, "+"), 10, 64); err != nil {
					return Value{}, fmt.Errorf("%q is not an integer in range", v.str)
				}
			}
		default:
			return Value{}, bad
		}
		switch {
		case to == KindUint && neg:
			return Value{}, fmt.Errorf("%d is negative", i)
		case to == KindUint:
			return Uint(u), nil
		case neg:
			return Int(i), nil
		case u > math.MaxInt64:
			return Value{}, fmt.Errorf("%d is out of range for int", u)
		}
		return Int(int64(u)), nil
	case KindFloat:
		switch v.kind {
		case KindBool, KindUint:
			return Float(float64(v.num)), nil
		case KindInt:
			return Float(float64(v.AsInt())), nil
		case KindDecimal, KindString:
			f, err := strconv.ParseFloat(v.str, 64)
			if err != nil {
				return Value{}, fmt.Errorf("%q is not a number", v.str)
			}
			return Float(f), nil
		}
	case KindDecimal:
		switch v.kind {
		case KindInt, KindUint:
			return Decimal(textOf(v)), nil
		case KindFloat:
			f := v.AsFloat()
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return Value{}, fmt.Errorf("%v is not a decimal", f)
			}
			return Decimal(strconv.FormatFloat(f, 'f', -1, 64)), nil
		case KindString:
			if !decimalPattern.MatchString(v.str) {
				return Value{}, fmt.Errorf("%q is not a decimal", v.str)
			}
			return Decimal(v.str), nil
		}
	case KindBool:
		switch v.kind {
		case KindInt, KindUint:
			if v.num > 1 {
				return Value{}, fmt.Errorf("%s is not 0 or 1", textOf(v))
			}
			return Bool(v.num == 1), nil
		case KindString:
			switch v.str {
			case "true":
				return Bool(true), nil
			case "false":
				return Bool(false), nil
			}
			return Value{}, fmt.Errorf("%q is not true or false", v.str)
		}
	case KindBytes:
		switch v.kind {
		case KindString, KindDecimal, KindJSON:
			return Value{kind: KindBytes, str: v.str}, nil
		}
	case KindTime:
		switch v.kind {
		case KindDate:
			return Value{kind: KindTime, tm: v.tm}, nil
		case KindInt:
			return Time(time.Unix(v.AsInt(), 0).UTC()), nil
		case KindUint:
			if v.num > math.MaxInt64 {
				return Value{}, fmt.Errorf("%d is out of range for a time", v.num)
			}
			return Time(time.Unix(int64(v.num), 0).UTC()), nil
		case KindString:
			t, err := time.Parse(time.RFC3339Nano, v.str)
			if err != nil {
				return Value{}, fmt.Errorf("%q is not a time in RFC 3339", v.str)
			}
			return Time(t), nil
		}
	case KindDate:
		switch v.kind {
		case KindTime:
			return Date(v.tm.Date()), nil
		case KindString:
			t, err := time.Parse(time.DateOnly, v.str)
			if err != nil {
				return Value{}, fmt.Errorf("%q is not a date as 2006-01-02", v.str)
			}
			return Date(t.Date()), nil
		}
	case KindJSON:
		switch v.kind {
		case KindString, KindBytes:
			if !json.Valid([]byte(v.str)) {
				return Value{}, fmt.Errorf("%q is not a JSON document", v.str)
			}
			return Value{kind: KindJSON, str: v.str}, nil
		}
		b, err := appendPlain(nil, v)
		if err != nil {
			return Value{}, err
		}
		return JSON(b), nil
	}
	return Value{}, bad
}

// Mask is the transform mask.
type Mask struct {
	Fields []string `json:"fields" fit:"required" doc:"The fields masked, at the top level of the rows before and after, and of the key for a hash."`
	With   string   `json:"with,omitempty" enum:"hmac_sha256,sha256,null" default:"\"hmac_sha256\"" doc:"hmac_sha256 replaces a value by the hex of its HMAC-SHA256 under the key key_env names, which cannot be reversed without the key; sha256 by the hex of its SHA-256, which anyone can reverse for a value from a small set (a phone number, an ID) by trying them all; null replaces it by null, which leaves the key as it is, since a key of nulls would no longer tell rows apart. A value is hashed as its text, so an int 5 and a string \"5\" hash the same; null stays null. The hashes keep the key unique and equal to the row's field. hmac_sha256 when left out."`
	KeyEnv string   `json:"key_env,omitempty" doc:"An environment variable holding the HMAC key, read when the pipeline starts: needed with hmac_sha256, and only with it. Changing the key changes every hash, so a sink keyed by them sees new rows."`

	names map[string]bool
	h     hash.Hash
	sum   []byte
}

// Provision reads the key.
func (m *Mask) Provision(ctx *module.Context) error {
	var e config.Errors
	if len(m.Fields) == 0 {
		e = append(e, ctx.Params.Field("fields").Errorf("no fields"))
	}
	m.names = map[string]bool{}
	for _, n := range m.Fields {
		m.names[n] = true
	}
	switch m.With {
	case "", "hmac_sha256":
		switch key := os.Getenv(m.KeyEnv); {
		case m.KeyEnv == "":
			e = append(e, ctx.Params.Field("key_env").Errorf("hmac_sha256 needs key_env, the environment variable holding its key"))
		case !envName.MatchString(m.KeyEnv):
			e = append(e, ctx.Params.Field("key_env").Errorf("%q is not an environment variable's name", m.KeyEnv))
		case key == "":
			e = append(e, ctx.Params.Field("key_env").Errorf("the environment variable %s is not set, or empty", m.KeyEnv))
		default:
			m.h = hmac.New(sha256.New, []byte(key))
		}
	default:
		if m.KeyEnv != "" {
			e = append(e, ctx.Params.Field("key_env").Errorf("key_env is for hmac_sha256"))
		}
		m.h = sha256.New()
	}
	return errs(e)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// removes reports whether the row's field name is null once m is done.
func (m *Mask) removes(name string) bool { return m.With == "null" && m.names[name] }

// Apply masks r's fields.
func (m *Mask) Apply(r *Record, emit func(*Record)) error {
	return editRecord(r, m.names, m.With != "null", m.mask, emit)
}

func (m *Mask) mask(_ string, v Value) (Value, error) {
	if m.With == "null" || v.IsNull() {
		return Null(), nil
	}
	m.h.Reset()
	m.h.Write([]byte(textOf(v)))
	m.sum = m.h.Sum(m.sum[:0])
	return String(hex.EncodeToString(m.sum)), nil
}

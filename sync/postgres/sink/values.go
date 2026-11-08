package syncpgsink

import (
	"encoding/hex"
	"errors"
	"strings"
	"time"

	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

// Param returns v as the wire writes it for a statement's parameter or a
// COPY row, as text the column's type reads, which the server infers from
// the column: null as NULL; a bool, an int, a uint or a float as their
// text (NaN and the infinities as PostgreSQL spells them); a decimal as
// its text; a string as it is (a NUL byte, which PostgreSQL's text cannot
// hold, is an error); bytes as bytea's hex text (\x0102); a time as the timestamp of its own
// wall clock and offset, which a timestamptz column reads as the instant
// and a timestamp column as the wall clock; a date as YYYY-MM-DD; JSON as
// its text; an array as an array literal of its elements (maps in it as
// JSON); a map as a JSON object, for a json or jsonb column.
func Param(v fitsync.Value) (any, error) {
	switch v.Kind() {
	case fitsync.KindNull:
		return nil, nil
	case fitsync.KindBool:
		return v.AsBool(), nil
	case fitsync.KindInt:
		return v.AsInt(), nil
	case fitsync.KindUint:
		return v.AsUint(), nil
	case fitsync.KindFloat:
		return v.AsFloat(), nil
	case fitsync.KindDecimal:
		return pgwire.Numeric(v.Text()), nil
	case fitsync.KindString:
		if strings.IndexByte(v.Text(), 0) >= 0 {
			return nil, errors.New("a string holds a NUL byte, which PostgreSQL's text cannot")
		}
		return v.Text(), nil
	case fitsync.KindBytes:
		// bytea's hex text, as COPY writes it, so that a row is the same
		// whether it was copied or inserted.
		return `\x` + hex.EncodeToString([]byte(v.Text())), nil
	case fitsync.KindTime:
		return v.AsTime(), nil
	case fitsync.KindDate:
		return v.AsTime().Format(time.DateOnly), nil
	case fitsync.KindJSON:
		return pgwire.JSON(v.Text()), nil
	case fitsync.KindArray:
		out := make([]any, len(v.Elems()))
		for i, e := range v.Elems() {
			p, err := Param(e)
			if err != nil {
				return nil, err
			}
			out[i] = p
		}
		return out, nil
	}
	b, err := fitsync.AppendPlain(nil, v)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(b), `\u0000`) {
		return nil, errors.New("a map holds a NUL character, which PostgreSQL's jsonb cannot")
	}
	return pgwire.JSON(b), nil
}

// QuoteTable quotes a table's name, split at its first dot into the schema
// and the table: shop.orders is "shop"."orders", orders is "orders", found
// by the search_path.
func QuoteTable(name string) string {
	if schema, table, ok := strings.Cut(name, "."); ok {
		return pgwire.QuoteIdentifier(schema) + "." + pgwire.QuoteIdentifier(table)
	}
	return pgwire.QuoteIdentifier(name)
}

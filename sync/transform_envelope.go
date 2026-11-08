package fitsync

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Envelope is the transform envelope.
type Envelope struct {
	Shape   string            `json:"shape,omitempty" enum:"flat,pair,debezium" default:"\"flat\"" doc:"flat makes the document the row (after, or before for a delete, or the key's fields when a delete has no before) with the meta fields added; pair makes it {\"before\": ..., \"after\": ...} with the meta fields; debezium makes it Debezium's envelope: before, after, source (name, namespace, db and table split at the namespace's first '.', ts_ms), op (c, u or d), ts_ms (the commit time in milliseconds, null when unknown) and transaction ({\"id\"}, or null). flat when left out."`
	Meta    map[string]string `json:"meta,omitempty" doc:"Metadata fields added to a flat or pair document, each named: the key one of op (insert, update or delete), namespace, source, commit_time (a time, null when unknown), tx_id (null when none) and deleted (true for a delete), the value the field's name: {\"op\": \"__op\", \"deleted\": \"__deleted\"}. None when left out; not with debezium."`
	Deletes string            `json:"deletes,omitempty" enum:"keep,drop,rewrite" default:"\"keep\"" doc:"What becomes of a delete: keep keeps it a delete, its document in after, so a sink still deletes by its key; drop drops it; rewrite makes it an update, so a sink upserts the document, which with flat needs meta's deleted to say it was removed. keep when left out."`

	meta []metaField
}

type metaField struct{ from, name string }

var metaNames = []string{"op", "namespace", "source", "commit_time", "tx_id", "deleted"}

// Provision checks meta.
func (e *Envelope) Provision(ctx *module.Context) error {
	var errs config.Errors
	for from, name := range e.Meta {
		if !slices.Contains(metaNames, from) {
			errs = append(errs, ctx.Params.Field("meta").Field(from).Errorf("%q is not one of %s", from, strings.Join(metaNames, ", ")))
		}
		if name == "" {
			errs = append(errs, ctx.Params.Field("meta").Field(from).Errorf("an empty name"))
		}
		e.meta = append(e.meta, metaField{from, name})
	}
	sort.Slice(e.meta, func(i, j int) bool {
		return slices.Index(metaNames, e.meta[i].from) < slices.Index(metaNames, e.meta[j].from)
	})
	if e.Shape == "debezium" && len(e.Meta) > 0 {
		errs = append(errs, ctx.Params.Field("meta").Errorf("meta is for flat and pair; debezium has its own"))
	}
	if e.Deletes == "rewrite" && (e.Shape == "" || e.Shape == "flat") && e.Meta["deleted"] == "" {
		errs = append(errs, ctx.Params.Field("deletes").Errorf("rewrite with flat needs meta's deleted, or the document would not say the row was removed"))
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// removes reports whether the row's top level loses field name: pair and
// debezium move every field down.
func (e *Envelope) removes(string) bool { return e.Shape == "pair" || e.Shape == "debezium" }

func (e *Envelope) metaValue(r *Record, from string) Value {
	switch from {
	case "op":
		return String(r.Op.String())
	case "namespace":
		return String(r.Namespace)
	case "source":
		return String(r.Source)
	case "commit_time":
		if r.CommitTime.IsZero() {
			return Null()
		}
		return Time(r.CommitTime)
	case "tx_id":
		if r.Tx.ID == "" {
			return Null()
		}
		return String(r.Tx.ID)
	}
	return Bool(r.Op == OpDelete)
}

// Apply puts r's document in after.
func (e *Envelope) Apply(r *Record, emit func(*Record)) error {
	if r.Op != OpInsert && r.Op != OpUpdate && r.Op != OpDelete {
		emit(r)
		return nil
	}
	if r.Op == OpDelete && e.Deletes == "drop" {
		return nil
	}
	var fs []Field
	switch e.Shape {
	case "pair":
		fs = append(make([]Field, 0, 2+len(e.meta)), Field{"before", r.Before}, Field{"after", r.After})
	case "debezium":
		db, table := "", r.Namespace
		if i := strings.IndexByte(r.Namespace, '.'); i >= 0 {
			db, table = r.Namespace[:i], r.Namespace[i+1:]
		}
		ts := Null()
		if !r.CommitTime.IsZero() {
			ts = Int(r.CommitTime.UnixMilli())
		}
		tx := Null()
		if r.Tx.ID != "" {
			tx = Map(Field{"id", String(r.Tx.ID)})
		}
		fs = []Field{{"before", r.Before}, {"after", r.After},
			{"source", Map(Field{"name", String(r.Source)}, Field{"namespace", String(r.Namespace)}, Field{"db", String(db)}, Field{"table", String(table)}, Field{"ts_ms", ts})},
			{"op", String([]string{OpInsert: "c", OpUpdate: "u", OpDelete: "d"}[r.Op])}, {"ts_ms", ts}, {"transaction", tx}}
	default:
		row := r.After
		if r.Op == OpDelete {
			if row = r.Before; row.IsNull() {
				row = r.Key
			}
		}
		if row.kind != KindMap {
			return fmt.Errorf("a %s's row is a %s, not a map to make a flat document of", r.Op, row.kind)
		}
		fs = append(make([]Field, 0, len(row.fields)+len(e.meta)), row.fields...)
	}
	for _, m := range e.meta {
		v := e.metaValue(r, m.from)
		if i := indexField(fs, m.name); i >= 0 {
			fs[i].Value = v
		} else {
			fs = append(fs, Field{m.name, v})
		}
	}
	o := r.Own()
	o.Before, o.After = Null(), Map(fs...)
	if o.Op == OpDelete && e.Deletes == "rewrite" {
		o.Op = OpUpdate
	}
	emit(o)
	return nil
}

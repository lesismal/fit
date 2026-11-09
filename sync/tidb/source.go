// Package synctidb is sync's TiDB as a source: a snapshot of tables read
// over MySQL's protocol at one TiDB timestamp (tidb_snapshot), then the
// changes TiCDC writes to Kafka after it, in canal-json or TiCDC's simple
// protocol, read by the kafka source from offsets in the checkpoint. TiDB
// as a sink is the mysql sink. The snapshot's connection is
// sync/mysql/wire's, Kafka's sync/kafka/wire's, each on the pipeline's
// goroutine, never on a fib worker. See docs/decisions/036-sync.md.
package synctidb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	synckafka "github.com/lesismal/fit/sync/kafka"
	syncmysql "github.com/lesismal/fit/sync/mysql"
	mysqlwire "github.com/lesismal/fit/sync/mysql/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".tidb", New: func() any { return new(Source) },
		Doc: "Reads TiDB: first a snapshot of tables over MySQL's protocol, all at one TiDB timestamp (tidb_snapshot), in pages by primary key, then the changes a TiCDC changefeed writes to Kafka in canal-json or the simple protocol, read by the kafka source from offsets in the checkpoint, those committed at or before the snapshot's timestamp left out. The changefeed must have started at or before the snapshot, and the topic must keep its messages since. Records carry their commit TS as their transaction and commit time."})
}

// The defaults of the source's params.
const (
	DefaultAddress = "127.0.0.1:4000"
	DefaultPage    = 1000
)

// Source is the source tidb.
type Source struct {
	Address     string           `json:"address,omitempty" default:"\"127.0.0.1:4000\"" doc:"TiDB's MySQL host and port, for the snapshot and the tables' schemas. 127.0.0.1:4000 when left out."`
	Username    string           `json:"username,omitempty" doc:"The user to log in as; it needs SELECT on the tables. root when left out."`
	PasswordEnv string           `json:"password_env,omitempty" doc:"The environment variable holding the password, read when the pipeline loads; none when left out."`
	TLS         bool             `json:"tls,omitempty" doc:"Connect over TLS, required and verified against the system's roots or ca_file's."`
	CAFile      string           `json:"ca_file,omitempty" doc:"A PEM file of the certificates TiDB's is checked against, in place of the system's roots; needs tls."`
	CertFile    string           `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it; needs tls and key_file."`
	KeyFile     string           `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName  string           `json:"server_name,omitempty" doc:"The name TiDB's certificate must have; address's host when left out."`
	Timeout     config.Duration  `json:"timeout,omitempty" default:"\"60s\"" doc:"How long connecting and each statement of the snapshot may take. 60s when left out or 0."`
	Tables      []string         `json:"tables,omitempty" doc:"The tables, as db.table, the snapshot reads and the changes are kept of; each needs a primary key. Every table the changefeed writes, with no snapshot, when left out."`
	Snapshot    string           `json:"snapshot,omitempty" enum:"initial,never" default:"\"initial\"" doc:"What a pipeline without a checkpoint does first: initial, read tables at a timestamp, then the changes after it; never, only the changes, from the kafka source's start. initial when left out; it needs tables."`
	PageRows    int              `json:"page_rows,omitempty" min:"0" max:"100000" default:"1000" doc:"The rows one SELECT of the snapshot reads. 1000 when left out or 0."`
	Protocol    string           `json:"protocol,omitempty" enum:"canal-json,simple" default:"\"canal-json\"" doc:"The changefeed's protocol: canal-json (with enable-tidb-extension=true, for the commit TS) or simple. canal-json when left out."`
	TimeZone    string           `json:"time_zone,omitempty" default:"\"UTC\"" doc:"The changefeed's time-zone, in which it writes DATETIME values (and canal-json TIMESTAMP ones). UTC when left out."`
	Kafka       synckafka.Source `json:"kafka" fit:"required" doc:"Where the changefeed writes: the kafka source's params (brokers, topics, TLS, SASL, start), without codec, which protocol picks."`

	cfg      mysqlwire.Config
	dial     func(context.Context, mysqlwire.Config) (db, error)
	stream   stream
	loc      *time.Location
	tables   map[string]bool
	simple   *synckafka.TiCDCSimple
	schemas  map[string]*table
	pageRows int
}

// stream is what reads the changes: the kafka source, or a fake in tests.
type stream interface {
	Run(ctx context.Context, r *fitsync.Run) error
	Test(ctx context.Context) error
}

// position is the source's: the snapshot's timestamp, and where the
// snapshot is, or, once it is done, the kafka source's position.
type position struct {
	TS       uint64          `json:"ts,omitempty"`
	Snapshot *snapPos        `json:"snapshot,omitempty"`
	Kafka    json.RawMessage `json:"kafka,omitempty"`
}

type snapPos struct {
	// Table is the index in tables of the table being read; After the key
	// of the last row read of it, in canonical JSON.
	Table int              `json:"table"`
	After *json.RawMessage `json:"after,omitempty"`
}

// Provision checks the params, the kafka source's among them.
func (s *Source) Provision(ctx *module.Context) error {
	cfg, errs := syncmysql.WireConfig(ctx, syncmysql.ConnParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	if s.Address == "" {
		cfg.Addr = DefaultAddress
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	s.cfg = cfg
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	s.tables = map[string]bool{}
	for _, t := range s.Tables {
		db, tb, ok := strings.Cut(t, ".")
		if !ok || db == "" || tb == "" || s.tables[t] {
			add("tables", "%q is not db.table, or named twice", t)
		}
		s.tables[t] = true
	}
	if s.Snapshot != "never" && len(s.Tables) == 0 {
		add("tables", "snapshot initial needs tables")
	}
	loc, err := time.LoadLocation(s.TimeZone)
	if s.TimeZone == "" {
		loc, err = time.UTC, nil
	}
	if err != nil {
		add("time_zone", "%v", err)
	}
	s.loc = loc
	if s.Kafka.Codec != nil {
		add("kafka", "codec is protocol's to pick")
	}
	s.pageRows = DefaultPage
	if s.PageRows > 0 {
		s.pageRows = s.PageRows
	}
	if len(errs) > 0 {
		return errs
	}
	sub := *ctx
	sub.Params = ctx.Params.Field("kafka")
	if err := s.Kafka.Provision(&sub); err != nil {
		return err
	}
	if s.Protocol == "simple" {
		s.simple = &synckafka.TiCDCSimple{TimeZone: s.TimeZone}
		s.Kafka.SetCodec(s.simple)
		s.simple.Provision(&sub) // the zone loaded above
	} else {
		c := &synckafka.CanalJSON{TimeZone: s.TimeZone}
		c.Provision(&sub)
		s.Kafka.SetCodec(c)
	}
	if s.dial == nil {
		s.dial = dialWire
	}
	if s.stream == nil {
		s.stream = &s.Kafka
	}
	return nil
}

// Cleanup lets the kafka source go.
func (s *Source) Cleanup() { s.Kafka.Cleanup() }

// Test logs in to TiDB and runs SELECT 1, then tests Kafka as the kafka
// source does.
func (s *Source) Test(ctx context.Context) error {
	c, err := s.dial(ctx, s.cfg)
	if err != nil {
		return err
	}
	_, err = c.Exec(ctx, "SELECT 1")
	c.Close()
	if err != nil {
		return err
	}
	return s.stream.Test(ctx)
}

// Run reads the snapshot, when there is one to read, then the changes.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var p position
	if r.From != nil {
		if err := json.Unmarshal(r.From, &p); err != nil {
			return fmt.Errorf("tidb: the checkpoint %s: %w", r.From, err)
		}
	} else if s.Snapshot != "never" {
		p.Snapshot = &snapPos{}
	}
	if len(s.Tables) > 0 {
		c, err := s.dial(ctx, s.cfg)
		if err != nil {
			return fmt.Errorf("tidb: %w", err)
		}
		err = s.seed(ctx, c)
		if err == nil && p.Snapshot != nil {
			err = s.snapshot(ctx, c, &p, r)
		}
		c.Close()
		if err != nil {
			return err
		}
	}
	return s.changes(ctx, r, p.TS, p.Kafka)
}

// changes runs the kafka source from its position, the records of other
// tables, and those committed at or before ts, left out.
func (s *Source) changes(ctx context.Context, r *fitsync.Run, ts uint64, from json.RawMessage) error {
	var kfrom fitsync.Position
	if len(from) > 0 {
		kfrom = fitsync.Position(from)
	}
	emit := func(rec *fitsync.Record) error {
		if rec.Position != nil {
			b, _ := json.Marshal(position{TS: ts, Kafka: json.RawMessage(rec.Position)})
			rec.Position = b
		}
		if !s.keep(rec, ts) {
			// Its position comes with the next record kept: a message's
			// records are all of one table and commit, kept or left out
			// together.
			return nil
		}
		s.labels(rec)
		rec.Source = "tidb"
		return r.Emit(rec)
	}
	return s.stream.Run(ctx, &fitsync.Run{From: kfrom, Once: r.Once, Emit: emit, Pipeline: r.Pipeline, Lag: r.Lag})
}

// keep reports whether a change is one the source hands on: of a table it
// keeps, committed after the snapshot.
func (s *Source) keep(rec *fitsync.Record, ts uint64) bool {
	if len(s.tables) > 0 && !s.tables[rec.Namespace] {
		return false
	}
	if ts > 0 {
		if cts, err := strconv.ParseUint(rec.Tx.ID, 10, 64); err == nil && cts <= ts {
			return false
		}
	}
	return true
}

// errNoKey is a table without a primary key, which the snapshot cannot
// page through.
var errNoKey = errors.New("has no primary key")

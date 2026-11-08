// Package syncpostgres is sync's PostgreSQL, for PostgreSQL 13 to 17: the
// source postgres, which follows a logical replication slot with pgoutput
// and confirms to the slot only what every sink has, and the sink postgres.
// Both speak the protocol through sync/postgres/wire, on connections of
// their own on the pipeline's goroutines, never on fib's workers. See
// docs/decisions/036-sync.md.
package syncpostgres

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	pgwire "github.com/lesismal/fit/sync/postgres/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".postgres", New: func() any { return new(Source) },
		Doc: "Follows PostgreSQL's changes (13 to 17, wal_level logical) through logical replication: a slot and a publication, created when missing with create, read with pgoutput, large transactions streamed and kept on disk until they commit. With snapshot initial, the tables are first read whole, in chunks by key in parallel, from a snapshot exported at the point the stream then starts from, so that the hand-over is exact; a job (mode once) reads that snapshot alone. Inserts, updates, deletes and truncates become records of db.table namespaces (schema.table), typed by the columns' types, keyed by the primary key or the replica identity, whole transactions at a time. Its position is the LSN at a transaction's end, confirmed to the slot only once every sink has it and the checkpoint is saved, so the server keeps the WAL until then; the slot's lag is logged past lag_warning."})
}

// The defaults of the source's params.
const (
	DefaultStatusInterval = 10 * time.Second
	DefaultLagWarning     = 1 << 30
	DefaultChunkRows      = 100000
	DefaultParallel       = 4
	sourceDefaultAddress  = "127.0.0.1:5432"
	sourceConnectTimeout  = 10 * time.Second
)

// Source is the source postgres.
type Source struct {
	Address          string          `json:"address,omitempty" default:"\"127.0.0.1:5432\"" doc:"The server's host and port, the port 5432 when only a host is given. 127.0.0.1:5432 when left out."`
	User             string          `json:"user" fit:"required" doc:"The user to connect as: one with the REPLICATION attribute (or a superuser), allowed to read the published tables, and, with create, to create the publication."`
	PasswordEnv      string          `json:"password_env,omitempty" doc:"The environment variable holding the password, read when the pipeline loads; none when left out."`
	Database         string          `json:"database" fit:"required" doc:"The database whose changes are read: a slot belongs to one database."`
	SSLMode          string          `json:"sslmode,omitempty" enum:"disable,require,verify-ca,verify-full" default:"\"verify-full\"" doc:"TLS to the server: verify-full, the certificate and the host name checked against the system's roots or ca_file's; verify-ca, the certificate alone; require, TLS unchecked; disable, none. There is no prefer: a silent fall back to plain text is not offered. verify-full when left out."`
	CAFile           string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots."`
	CertFile         string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, presented to a server that asks for one; needs key_file."`
	KeyFile          string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName       string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; address's host when left out."`
	ChannelBinding   string          `json:"channel_binding,omitempty" enum:"prefer,require,disable" default:"\"prefer\"" doc:"SCRAM-SHA-256's channel binding to the TLS connection: prefer, when TLS is used and the server offers it; require, fail without it; disable. prefer when left out."`
	ConnectTimeout   config.Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long connecting, TLS and authentication may take. 10s when left out or 0."`
	Publication      string          `json:"publication" fit:"required" doc:"The publication read: the tables whose changes come, and which operations. With create, made FOR TABLE tables when it does not exist; it is never changed."`
	Slot             string          `json:"slot" fit:"required" doc:"The logical replication slot read, of plugin pgoutput: lowercase letters, digits and underscores. With create, made when it does not exist; it is never dropped: drop it (pg_drop_replication_slot) when the pipeline is gone, or the server keeps its WAL for good."`
	Tables           []string        `json:"tables,omitempty" doc:"The tables, schema.table or table (in public), the publication is made for with create; with a publication that exists, each must be in it. Required with create."`
	Create           bool            `json:"create,omitempty" doc:"Create the publication and the slot when they do not exist, rather than fail. With snapshot initial, the slot made exports the snapshot the tables are read from; with never, changes come from its creation on."`
	Snapshot         string          `json:"snapshot,omitempty" enum:"initial,never" default:"\"initial\"" doc:"Where a pipeline without a checkpoint starts: initial, every row of the tables, read from a snapshot exported at the point the stream then starts from (the slot's own when create makes it, a temporary slot's otherwise), then the changes after it; never, the changes from the slot's position on. A job (mode once) reads the snapshot and stops, or, with never, the changes up to the server's end. initial when left out."`
	SnapshotParallel int             `json:"snapshot_parallel,omitempty" min:"0" max:"64" default:"4" doc:"How many chunks of the snapshot are read at once, each on a connection of its own, before the stream; a job reads job.max_parallel at a time instead. 4 when left out or 0."`
	ChunkRows        int64           `json:"chunk_rows,omitempty" min:"0" default:"100000" doc:"The rows of a chunk of the snapshot: a table with a primary key, or a replica identity index, is split into ranges of that key of this many rows, each read in a transaction of its own and resumed after its last row; a table without one is read whole. 100000 when left out or 0."`
	Streaming        string          `json:"streaming,omitempty" enum:"auto,off" default:"\"auto\"" doc:"Large transactions streamed by the server while they run (PostgreSQL 14 and later), kept in files under spool_dir until they commit, rather than spilled by the server and sent at their commit: auto, on servers that have it; off. auto when left out."`
	SpoolDir         string          `json:"spool_dir,omitempty" doc:"The directory streamed transactions are kept in until they commit or abort, a file each, removed then. The system's temporary directory when left out."`
	StatusInterval   config.Duration `json:"status_interval,omitempty" default:"\"10s\"" doc:"How often the source tells the server the position the sinks have, also when it has not moved, which keeps the connection alive (below the server's wal_sender_timeout). 10s when left out or 0."`
	LagWarning       int64           `json:"lag_warning,omitempty" min:"0" default:"1073741824" doc:"The slot's lag, in bytes of WAL between the server's end and the position confirmed, past which a warning is logged: the WAL the server keeps for the pipeline. 1 GiB when left out or 0."`

	cfg  pgwire.Config
	wake chan struct{}

	mu sync.Mutex
	st slotState
	// held is a snapshot's exporter kept from a run that failed, so that
	// the next run resumes in it; tables, the tables a job reads.
	held   *exporter
	tables map[string]*tableInfo
}

// slotState is what the source has read and what it may confirm.
type slotState struct {
	// confirmed is the last checkpoint saved; emitted the end of the last
	// transaction with records handed on; idle the latest position before
	// which nothing is owed: every transaction up to it had no records or
	// was handed on.
	confirmed, emitted, idle pgwire.LSN
	// end is the server's WAL end, as it last said.
	end    pgwire.LSN
	warned bool
}

// sourcePosition is the source's position: the LSN at the end of a
// transaction, or, while the snapshot is read, the point the stream starts
// from after it and the snapshot's progress.
type sourcePosition struct {
	LSN      string         `json:"lsn"`
	Snapshot *snapshotState `json:"snapshot,omitempty"`
}

func lsnPosition(l pgwire.LSN) fitsync.Position {
	b, _ := json.Marshal(sourcePosition{LSN: l.String()})
	return b
}

func parsePosition(p fitsync.Position) (pgwire.LSN, error) {
	l, _, err := parseSourcePosition(p)
	return l, err
}

// parseSourcePosition reads a position, with the snapshot's progress when
// it has one.
func parseSourcePosition(p fitsync.Position) (pgwire.LSN, *snapshotState, error) {
	if p == nil {
		return 0, nil, nil
	}
	var sp sourcePosition
	if err := json.Unmarshal(p, &sp); err != nil {
		return 0, nil, fmt.Errorf("postgres: the checkpoint %s: %w", p, err)
	}
	l, err := pgwire.ParseLSN(sp.LSN)
	if err != nil {
		return 0, nil, fmt.Errorf("postgres: the checkpoint %s: %w", p, err)
	}
	return l, sp.Snapshot, nil
}

var (
	sourceEnvName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	sourceSlotName = regexp.MustCompile(`^[a-z0-9_]{1,63}$`)
)

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	s.wake = make(chan struct{}, 1)
	c := pgwire.Config{Addr: s.Address, User: s.User, Database: s.Database, SSLMode: s.SSLMode,
		ChannelBinding: s.ChannelBinding, Replication: true, ApplicationName: "fit-sync",
		ConnectTimeout: s.ConnectTimeout.Std(),
		Params:         map[string]string{"DateStyle": "ISO", "TimeZone": "UTC", "extra_float_digits": "3", "bytea_output": "hex", "IntervalStyle": "postgres"}}
	if c.Addr == "" {
		c.Addr = sourceDefaultAddress
	}
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = sourceConnectTimeout
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil && strings.Contains(c.Addr, ":") {
		add("address", "%q is not a host and port", c.Addr)
	}
	switch {
	case s.PasswordEnv != "" && !sourceEnvName.MatchString(s.PasswordEnv):
		add("password_env", "%q is not an environment variable's name", s.PasswordEnv)
	case s.PasswordEnv != "":
		if c.Password = os.Getenv(s.PasswordEnv); c.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", s.PasswordEnv)
		}
	}
	if s.SSLMode == pgwire.SSLDisable {
		for _, f := range []struct{ name, value string }{{"ca_file", s.CAFile}, {"cert_file", s.CertFile}, {"key_file", s.KeyFile}, {"server_name", s.ServerName}} {
			if f.value != "" {
				add(f.name, "%s needs TLS, which sslmode disable turns off", f.name)
			}
		}
		if s.ChannelBinding == pgwire.ChannelBindingRequire {
			add("channel_binding", "channel binding needs TLS, which sslmode disable turns off")
		}
	} else {
		tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: s.ServerName}
		if s.CAFile != "" {
			pem, err := os.ReadFile(s.CAFile)
			tc.RootCAs = x509.NewCertPool()
			if err != nil {
				add("ca_file", "%v", err)
			} else if !tc.RootCAs.AppendCertsFromPEM(pem) {
				add("ca_file", "%s holds no PEM certificate", s.CAFile)
			}
		}
		if (s.CertFile == "") != (s.KeyFile == "") {
			add("cert_file", "cert_file and key_file go together")
		} else if s.CertFile != "" {
			cert, err := tls.LoadX509KeyPair(s.CertFile, s.KeyFile)
			if err != nil {
				add("cert_file", "%v", err)
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		c.TLS = tc
	}
	if !sourceSlotName.MatchString(s.Slot) {
		add("slot", "%q is not a slot's name: 1 to 63 lowercase letters, digits and underscores", s.Slot)
	}
	if s.Publication == "" || len(s.Publication) > 63 || strings.ContainsRune(s.Publication, 0) {
		add("publication", "%q is not a publication's name: 1 to 63 bytes", s.Publication)
	}
	seen := map[string]bool{}
	for _, t := range s.Tables {
		schema, name := splitTable(t)
		if schema == "" || name == "" || strings.ContainsRune(t, 0) || seen[schema+"."+name] {
			add("tables", "%q is not schema.table or table, or is named twice", t)
		}
		seen[schema+"."+name] = true
	}
	if s.Create && len(s.Tables) == 0 {
		add("tables", "create needs the tables the publication is made for")
	}
	if s.SpoolDir != "" {
		if fi, err := os.Stat(s.SpoolDir); err != nil || !fi.IsDir() {
			add("spool_dir", "%q is not a directory", s.SpoolDir)
		}
	}
	s.cfg = c
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// splitTable splits schema.table at its first dot; a table alone is in
// public.
func splitTable(t string) (schema, name string) {
	if s, n, ok := strings.Cut(t, "."); ok {
		return s, n
	}
	return "public", t
}

func (s *Source) statusInterval() time.Duration {
	if s.StatusInterval <= 0 {
		return DefaultStatusInterval
	}
	return s.StatusInterval.Std()
}

func (s *Source) lagWarning() int64 {
	if s.LagWarning <= 0 {
		return DefaultLagWarning
	}
	return s.LagWarning
}

// sqlConn is what the source uses of a connection: queries in text.
type sqlConn interface {
	ServerVersion() int
	SimpleQuery(ctx context.Context, sql string) ([]pgwire.Result, error)
	Close() error
}

// replConn is what the source uses of a replication connection.
type replConn interface {
	sqlConn
	IdentifySystem(ctx context.Context) (pgwire.System, error)
	CreateReplicationSlot(ctx context.Context, name string, o pgwire.SlotOptions) (pgwire.Slot, error)
	StartReplication(ctx context.Context, slot string, start pgwire.LSN, options ...string) error
	ReceiveReplication(ctx context.Context) (any, error)
	SendStandbyStatus(ctx context.Context, st pgwire.StandbyStatus) error
}

// dialSource connects; tests replace it.
var dialSource = func(ctx context.Context, cfg pgwire.Config) (replConn, error) {
	c, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// dialSQL connects for the snapshot's queries, without replication; tests
// replace it.
var dialSQL = func(ctx context.Context, cfg pgwire.Config) (sqlConn, error) {
	cfg.Replication = false
	c, err := pgwire.Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Test connects and checks what Run would need, creating nothing: wal_level,
// the publication and its tables' replica identities, and the slot.
func (s *Source) Test(ctx context.Context) error {
	c, err := dialSource(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = s.prepare(ctx, c, false, false)
	return err
}

// setup is what prepare finds.
type setup struct {
	// pk are the published tables' primary keys.
	pk map[string][]string
	// tables are the published tables, as the snapshot reads them, and
	// names their names, sorted.
	tables map[string]*tableInfo
	names  []string
	// pub are the publication's column lists and row filters (15+).
	pub map[string]pubTable
	// slot is the slot made, when prepare made it, with the snapshot it
	// exported when asked to.
	slot *pgwire.Slot
}

// pubTable is a published table's column list and row filter.
type pubTable struct {
	cols   []string
	filter string
}

// query runs one statement, its rows as text.
func query(ctx context.Context, c sqlConn, sql string) ([][][]byte, error) {
	res, err := c.SimpleQuery(ctx, sql)
	if err != nil {
		return nil, err
	}
	if len(res) == 0 {
		return nil, nil
	}
	return res[0].Rows, nil
}

// prepare checks the server, the publication and the slot, and, when
// create is allowed and set, makes those missing; a slot made exports its
// snapshot when export is set.
func (s *Source) prepare(ctx context.Context, c replConn, mayCreate, export bool) (*setup, error) {
	rows, err := query(ctx, c, "SHOW wal_level")
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 || string(rows[0][0]) != "logical" {
		return nil, errors.New("postgres: wal_level is not logical: set wal_level = logical and restart the server")
	}
	st, err := s.publication(ctx, c, mayCreate, false)
	if err != nil {
		return nil, err
	}
	rows, err = query(ctx, c, "SELECT plugin, slot_type, database FROM pg_catalog.pg_replication_slots WHERE slot_name = "+pgwire.QuoteLiteral(s.Slot))
	if err != nil {
		return nil, err
	}
	switch {
	case len(rows) == 1 && len(rows[0]) == 3:
		if string(rows[0][0]) != "pgoutput" || string(rows[0][1]) != "logical" || string(rows[0][2]) != s.Database {
			return nil, fmt.Errorf("postgres: the slot %s is %s of plugin %s on database %s, not logical of pgoutput on %s", s.Slot, rows[0][1], rows[0][0], rows[0][2], s.Database)
		}
	case !s.Create:
		return nil, fmt.Errorf("postgres: the slot %s does not exist: create it, or set create", s.Slot)
	case mayCreate:
		o := pgwire.SlotOptions{Plugin: "pgoutput", Snapshot: pgwire.SnapshotNothing}
		if export {
			// The snapshot is this connection's until its next command: the
			// run reads the tables in it before START_REPLICATION.
			o.Snapshot = pgwire.SnapshotExport
		}
		slot, err := c.CreateReplicationSlot(ctx, s.Slot, o)
		if err != nil {
			return nil, err
		}
		st.slot = &slot
	}
	return st, nil
}

// publication finds the publication's tables, their keys and how the
// snapshot reads them; with create, and mayCreate, it makes a missing
// publication. A job reads tables alone: it takes tables when there is no
// publication, and checks no replica identity.
func (s *Source) publication(ctx context.Context, c sqlConn, mayCreate, job bool) (*setup, error) {
	pub := pgwire.QuoteLiteral(s.Publication)
	rows, err := query(ctx, c, "SELECT pubupdate, pubdelete FROM pg_catalog.pg_publication WHERE pubname = "+pub)
	if err != nil {
		return nil, err
	}
	keyed := !job // the publication publishes updates or deletes
	var tables []string
	st := &setup{pk: map[string][]string{}, tables: map[string]*tableInfo{}, pub: map[string]pubTable{}}
	switch {
	case len(rows) == 1 && len(rows[0]) == 2:
		keyed = keyed && (string(rows[0][0]) == "t" || string(rows[0][1]) == "t")
		// Column lists and row filters came with PostgreSQL 15.
		more := ""
		if c.ServerVersion() >= 150000 {
			more = ", attnames, rowfilter"
		}
		pt, err := query(ctx, c, "SELECT schemaname, tablename"+more+" FROM pg_catalog.pg_publication_tables WHERE pubname = "+pub)
		if err != nil {
			return nil, err
		}
		in := map[string]bool{}
		for _, r := range pt {
			if len(r) < 2 {
				continue
			}
			ns := string(r[0]) + "." + string(r[1])
			in[ns] = true
			tables = append(tables, ns)
			if len(r) == 4 {
				var p pubTable
				if a := decodeText(1003, string(r[2])); r[2] != nil && a.Kind() == fitsync.KindArray {
					for _, v := range a.Elems() {
						p.cols = append(p.cols, v.Text())
					}
				}
				p.filter = string(r[3])
				st.pub[ns] = p
			}
		}
		for _, t := range s.Tables {
			if schema, name := splitTable(t); !in[schema+"."+name] {
				return nil, fmt.Errorf("postgres: the publication %s has no table %s.%s: add it with ALTER PUBLICATION, or leave it out of tables", s.Publication, schema, name)
			}
		}
	case job && len(s.Tables) > 0:
		for _, t := range s.Tables {
			schema, name := splitTable(t)
			tables = append(tables, schema+"."+name)
		}
	case !s.Create:
		return nil, fmt.Errorf("postgres: the publication %s does not exist: create it, or set create", s.Publication)
	default:
		var b strings.Builder
		for i, t := range s.Tables {
			schema, name := splitTable(t)
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pgwire.QuoteIdentifier(schema) + "." + pgwire.QuoteIdentifier(name))
			tables = append(tables, schema+"."+name)
		}
		if mayCreate {
			if _, err := c.SimpleQuery(ctx, "CREATE PUBLICATION "+pgwire.QuoteIdentifier(s.Publication)+" FOR TABLE "+b.String()); err != nil {
				return nil, err
			}
		}
	}
	if err := s.identities(ctx, c, tables, keyed, st); err != nil {
		return nil, err
	}
	st.names = slices.Sorted(maps.Keys(st.tables))
	return st, nil
}

// identities reads the tables' primary keys, replica identities and
// columns, and refuses a table whose updates and deletes would carry no
// key.
func (s *Source) identities(ctx context.Context, c sqlConn, tables []string, keyed bool, st *setup) error {
	if len(tables) == 0 {
		return nil
	}
	var in strings.Builder
	for i, t := range tables {
		schema, name := splitTable(t)
		if i > 0 {
			in.WriteString(", ")
		}
		in.WriteString("(" + pgwire.QuoteLiteral(schema) + ", " + pgwire.QuoteLiteral(name) + ")")
	}
	rows, err := query(ctx, c, `SELECT n.nspname, c.relname, c.relreplident,
  (SELECT json_agg(a.attname ORDER BY k.i) FROM pg_catalog.pg_index x
     CROSS JOIN LATERAL unnest(x.indkey::int2[]) WITH ORDINALITY AS k(attnum, i)
     JOIN pg_catalog.pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum
   WHERE x.indrelid = c.oid AND x.indisprimary),
  (SELECT json_agg(a.attname ORDER BY k.i) FROM pg_catalog.pg_index x
     CROSS JOIN LATERAL unnest(x.indkey::int2[]) WITH ORDINALITY AS k(attnum, i)
     JOIN pg_catalog.pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum
   WHERE x.indrelid = c.oid AND x.indisreplident),
  (SELECT json_agg(a.attname ORDER BY a.attnum) FROM pg_catalog.pg_attribute a
   WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = '')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE (n.nspname, c.relname) IN (`+in.String()+`)`)
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, r := range rows {
		if len(r) != 6 {
			continue
		}
		ns := string(r[0]) + "." + string(r[1])
		found[ns] = true
		var pk, ri, cols []string
		for _, f := range []struct {
			b    []byte
			v    *[]string
			what string
		}{{r[3], &pk, "primary key"}, {r[4], &ri, "replica identity"}, {r[5], &cols, "columns"}} {
			if f.b != nil {
				if err := json.Unmarshal(f.b, f.v); err != nil {
					return fmt.Errorf("postgres: the %s of %s: %w", f.what, ns, err)
				}
			}
		}
		if len(pk) > 0 {
			st.pk[ns] = pk
		}
		st.tables[ns] = newTableInfo(ns, string(r[2]), pk, ri, cols, st.pub[ns])
		if !keyed {
			continue
		}
		switch ident := string(r[2]); {
		case ident == "n":
			return fmt.Errorf("postgres: %s has REPLICA IDENTITY NOTHING, so its updates and deletes carry no key: give it a primary key or REPLICA IDENTITY FULL", ns)
		case ident == "d" && len(pk) == 0:
			return fmt.Errorf("postgres: %s has no primary key, so its updates and deletes carry no key: add one, or set REPLICA IDENTITY FULL or USING INDEX", ns)
		}
	}
	for _, t := range tables {
		if !found[t] {
			return fmt.Errorf("postgres: the table %s does not exist", t)
		}
	}
	return nil
}

// Run reads the snapshot when there is no checkpoint (snapshot initial), or
// the rest of one the checkpoint is in, then follows the slot from the
// checkpoint's LSN, the snapshot's point, or the slot's confirmed position.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	from, snap, err := parseSourcePosition(r.From)
	if err != nil {
		return err
	}
	c, err := dialSource(ctx, s.cfg)
	if err != nil {
		return err
	}
	defer c.Close()
	fresh := r.From == nil && s.Snapshot != "never" && !r.Once
	st, err := s.prepare(ctx, c, true, fresh)
	if err != nil {
		return err
	}
	if fresh || snap != nil {
		if from, err = s.snapshot(ctx, r, st, from, snap); err != nil {
			return err
		}
	} else {
		s.dropHeld()
	}
	var target pgwire.LSN
	if r.Once {
		sys, err := c.IdentifySystem(ctx)
		if err != nil {
			return err
		}
		target = sys.XLogPos
	}
	// Protocol 2 and streaming from PostgreSQL 14; 1 before.
	opts := []string{"proto_version", "1", "publication_names", pgwire.QuoteIdentifier(s.Publication)}
	if c.ServerVersion() >= 140000 && s.Streaming != "off" {
		opts = []string{"proto_version", "2", "publication_names", pgwire.QuoteIdentifier(s.Publication), "streaming", "on"}
	}
	if err := c.StartReplication(ctx, s.Slot, from, opts...); err != nil {
		return err
	}
	s.mu.Lock()
	s.st.confirmed, s.st.emitted, s.st.idle = from, from, from
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.statusLoop(ctx, c, r.Once)
	}()
	defer func() {
		cancel()
		wg.Wait()
	}()

	var reached pgwire.LSN
	d := &decoder{rels: map[uint32]*relation{}, pk: st.pk, from: from, emit: r.Emit,
		spools: map[uint32]*spool{}, spoolDir: s.SpoolDir}
	defer d.close()
	d.onCommit = func(end pgwire.LSN, emitted bool) {
		reached = max(reached, end)
		s.mu.Lock()
		if emitted {
			s.st.emitted = max(s.st.emitted, end)
		}
		s.st.idle = max(s.st.idle, end)
		s.mu.Unlock()
	}
	for {
		if r.Once && target > 0 && reached >= target && !d.inTx && len(d.spools) == 0 {
			return nil
		}
		m, err := c.ReceiveReplication(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		switch m := m.(type) {
		case *pgwire.XLogData:
			s.serverEnd(m.ServerEnd)
			if err := d.handle(m.Data); err != nil {
				return err
			}
		case *pgwire.Keepalive:
			s.serverEnd(m.ServerEnd)
			// Mid-transaction, the server has read past the commit of the
			// transaction it is sending; between them, everything before
			// its position has come.
			if !d.inTx {
				reached = max(reached, m.ServerEnd)
				s.mu.Lock()
				s.st.idle = max(s.st.idle, m.ServerEnd)
				s.mu.Unlock()
			}
			if m.ReplyRequested {
				s.sendStatus(ctx, c, false)
			}
		}
	}
}

func (s *Source) serverEnd(l pgwire.LSN) {
	s.mu.Lock()
	s.st.end = max(s.st.end, l)
	s.mu.Unlock()
}

// Confirm takes a saved checkpoint: the slot is told it at once.
func (s *Source) Confirm(p fitsync.Position) {
	l, err := parsePosition(p)
	if err != nil || l == 0 {
		return
	}
	s.mu.Lock()
	s.st.confirmed = max(s.st.confirmed, l)
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// flush is the position the slot may be told: the checkpoint, or, once
// every transaction handed on is in it, the latest position before which
// nothing is owed, so that a slot of a quiet table does not hold the WAL of
// the others.
func (st *slotState) flush() pgwire.LSN {
	if st.confirmed < st.emitted {
		return st.confirmed
	}
	return max(st.confirmed, st.idle)
}

// Lag is the WAL, in bytes, between the server's end and what the slot was
// last told it may release.
func (s *Source) Lag() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.st.flush(); s.st.end > f {
		return int64(s.st.end - f)
	}
	return 0
}

// statusLoop tells the server the flushed position every status_interval
// and at each checkpoint, until ctx ends; in a job it asks for a keepalive
// each time, which says how far the server has read.
func (s *Source) statusLoop(ctx context.Context, c replConn, reply bool) {
	t := time.NewTicker(s.statusInterval())
	defer t.Stop()
	for {
		s.sendStatus(ctx, c, reply)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
	}
}

func (s *Source) sendStatus(ctx context.Context, c replConn, reply bool) {
	s.mu.Lock()
	f := s.st.flush()
	var lag int64
	if s.st.end > f {
		lag = int64(s.st.end - f)
	}
	warn := lag > s.lagWarning()
	changed := warn != s.st.warned
	s.st.warned = warn
	s.mu.Unlock()
	if changed {
		log := logs.Named(config.LoggerSync)
		if warn {
			log.Warnf("sync postgres: the slot %s is %d bytes of WAL behind the server, which keeps them until the sinks catch up", s.Slot, lag)
		} else {
			log.Infof("sync postgres: the slot %s has caught up to %d bytes behind the server", s.Slot, lag)
		}
	}
	sctx, cancel := context.WithTimeout(ctx, s.statusInterval())
	defer cancel()
	// An error breaks the connection, which the stream's reader sees.
	c.SendStandbyStatus(sctx, pgwire.StandbyStatus{Write: f, Flush: f, Apply: f, ReplyRequested: reply})
}

package syncmongo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".mongo", New: func() any { return new(Sink) },
		Doc: "Writes records to MongoDB (6, 7 and 8), a replica set's primary, a standalone server or a mongos, as unordered bulk writes: inserts, updates and messages as upserts that replace the document whose _id the record's key makes with the row, deletes as deletes by that _id, into the database and collection the namespace (db.collection) or database and collection name; a replay converges. A document the server refuses (a validator, a unique index, a document too large) fails its record, which on_error takes, those before it written; other errors are retried by the pipeline."})
}

// The defaults and bounds of the sink.
const (
	DefaultSinkMaxRequestBytes = 8 << 20
	// MaxDocumentBytes is the largest document MongoDB stores.
	MaxDocumentBytes = 16 << 20
	// maxWriteOps is the most statements of one update or delete, MongoDB's
	// maxWriteBatchSize.
	maxWriteOps = 100000
)

// These are variables so that tests can make them small.
var (
	sinkMaxDocBytes = MaxDocumentBytes
	sinkMaxOps      = maxWriteOps
)

// Sink is the sink mongo.
type Sink struct {
	URI             string          `json:"uri,omitempty" doc:"The servers as a connection string, mongodb://[user@]host1[:port1][,host2...][/authdb][?options], with the options replicaSet, authSource, authMechanism (SCRAM-SHA-256), tls, tlsCAFile, tlsCertificateKeyFile, connectTimeoutMS, socketTimeoutMS, serverSelectionTimeoutMS, maxPoolSize and appName; any other is refused, and so is a password in it: password_env holds it. mongodb+srv is not read. uri or hosts."`
	Hosts           []string        `json:"hosts,omitempty" doc:"The servers to find the primary from, host or host:port (27017 when left out), in place of uri: a replica set's members (the others are learned from them), a standalone server, or mongos routers."`
	ReplicaSet      string          `json:"replica_set,omitempty" doc:"The replica set's name: a server of another set is not taken. Any set when left out."`
	Username        string          `json:"username,omitempty" doc:"The user to authenticate as, with SCRAM-SHA-256 and password_env; no authentication when left out."`
	PasswordEnv     string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	AuthSource      string          `json:"auth_source,omitempty" doc:"The database the user is defined in. admin when left out."`
	TLS             bool            `json:"tls,omitempty" doc:"Connect over TLS, the servers' certificates checked against the system's roots or ca_file's; the check cannot be turned off. These TLS params replace the connection string's TLS options."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the servers' are checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile        string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, presented to servers that ask for one; with its key in it too, as MongoDB's tlsCertificateKeyFile, unless key_file is set. Needs tls."`
	KeyFile         string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key, when cert_file does not hold it."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the servers' certificates must have, and the TLS server name sent; each server's host when left out."`
	ConnectTimeout  config.Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long connecting to a server may take, its TLS, hello and authentication. 10s when left out or 0."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one command, a bulk write of up to max_request_bytes, may take. 60s when left out or 0."`
	MaxConns        int             `json:"max_conns,omitempty" min:"0" max:"1000" default:"4" doc:"The most connections open to the primary, shared by a job's parallel writers. 4 when left out or 0."`
	Database        string          `json:"database,omitempty" doc:"The database every record is written to. When left out, the record's namespace's part before its first '.', a namespace without one failing its record."`
	Collection      string          `json:"collection,omitempty" doc:"The collection every record is written to. When left out, the record's namespace after its first '.', or the whole namespace when it has none and database is set."`
	WriteConcern    string          `json:"write_concern,omitempty" doc:"The write concern's w: majority, a number of members (1 or more; 0, unacknowledged writes, is refused), or a tag set's name. The server's default (majority on a replica set) when left out."`
	Journal         bool            `json:"journal,omitempty" doc:"Ask for the writes to be in the journal before they are acknowledged (j: true)."`
	WriteTimeout    config.Duration `json:"write_timeout,omitempty" doc:"How long the server waits for the write concern (wtimeout) before it answers with an error, which the pipeline retries; the writes stay. Without a limit when left out."`
	MaxRequestBytes int64           `json:"max_request_bytes,omitempty" min:"0" max:"41943040" default:"8388608" doc:"The most bytes of documents in one bulk write; a batch larger is sent in several. A document larger than 16 MiB fails its record. 8 MiB when left out or 0."`

	client   *mongowire.Client
	wc       mongowire.D
	maxBytes int
}

// Provision checks the params and reads the password and TLS files;
// nothing is sent until the first write or test.
func (s *Sink) Provision(ctx *module.Context) error {
	o, errs := sinkOptions(ctx, sinkConnParams{URI: s.URI, Hosts: s.Hosts, ReplicaSet: s.ReplicaSet, Username: s.Username,
		PasswordEnv: s.PasswordEnv, AuthSource: s.AuthSource, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile,
		KeyFile: s.KeyFile, ServerName: s.ServerName, ConnectTimeout: s.ConnectTimeout, Timeout: s.Timeout, MaxConns: s.MaxConns})
	if w := s.WriteConcern; w != "" {
		if n, err := strconv.Atoi(w); err == nil {
			if n < 1 {
				errs = append(errs, ctx.Params.Field("write_concern").Errorf("w %d: unacknowledged writes cannot be checkpointed", n))
			}
			s.wc = append(s.wc, mongowire.E{Key: "w", Value: int32(n)})
		} else {
			s.wc = append(s.wc, mongowire.E{Key: "w", Value: w})
		}
	}
	if s.Journal {
		s.wc = append(s.wc, mongowire.E{Key: "j", Value: true})
	}
	if s.WriteTimeout > 0 {
		s.wc = append(s.wc, mongowire.E{Key: "wtimeout", Value: s.WriteTimeout.Std().Milliseconds()})
	}
	if strings.ContainsAny(s.Database, "/\\. \"$\x00") {
		errs = append(errs, ctx.Params.Field("database").Errorf("%q is not a database's name", s.Database))
	}
	if strings.HasPrefix(s.Collection, "$") || strings.ContainsRune(s.Collection, 0) {
		errs = append(errs, ctx.Params.Field("collection").Errorf("%q is not a collection's name", s.Collection))
	}
	s.maxBytes = int(s.MaxRequestBytes)
	if s.maxBytes <= 0 {
		s.maxBytes = DefaultSinkMaxRequestBytes
	}
	if len(errs) > 0 {
		return errs
	}
	c, err := mongowire.New(o)
	if err != nil {
		return err
	}
	s.client = c
	return nil
}

// Cleanup closes the client.
func (s *Sink) Cleanup() {
	if s.client != nil {
		s.client.Close()
	}
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: a key goes to one writer, and the client's pool is
// shared.
func (s *Sink) ParallelWrites() {}

// Test finds the primary, authenticates and pings it.
func (s *Sink) Test(ctx context.Context) error {
	_, err := s.client.Run(ctx, "admin", mongowire.Doc("ping", int32(1)))
	return err
}

// sinkOp is a record as the sink writes it: an update or delete statement
// of a collection.
type sinkOp struct {
	idx      int
	db, coll string
	// key is the database, the collection and the _id's BSON: the
	// document the statement writes.
	key  string
	stmt mongowire.Raw
	del  bool
	// skip says the record writes nothing: DDL.
	skip bool
	// err is why the record cannot be written; werr why its write failed.
	err, werr error
}

// target returns the database and collection a namespace goes to.
func (s *Sink) target(ns string) (db, coll string, err error) {
	nsDB, nsColl, dotted := strings.Cut(ns, ".")
	if !dotted {
		nsDB, nsColl = "", ns
	}
	db, coll = s.Database, s.Collection
	if db == "" {
		db = nsDB
	}
	if coll == "" {
		coll = nsColl
	}
	if db == "" || coll == "" {
		return "", "", fmt.Errorf("the namespace %q names no database and collection (db.collection), and database or collection is not set", ns)
	}
	return db, coll, nil
}

// idOf returns the _id r's key makes: a key of one field its value, of
// several a document of them, any other the key itself; with no key, the
// row's _id.
func idOf(r *fitsync.Record) (fitsync.Value, error) {
	k := r.Key
	switch k.Kind() {
	case fitsync.KindNull:
		row := r.After
		if r.Op == fitsync.OpDelete || row.IsNull() {
			row = r.Before
		}
		if id, ok := row.Get("_id"); ok && !id.IsNull() {
			return id, nil
		}
		return fitsync.Value{}, errors.New("the record has no key, and its row no _id")
	case fitsync.KindMap:
		fs := k.Fields()
		if len(fs) == 0 {
			return fitsync.Value{}, errors.New("the record's key has no fields")
		}
		for _, f := range fs {
			if strings.HasPrefix(f.Name, "$") {
				return k, nil // a tagged value, as {"$oid": ...}
			}
			if f.Value.IsNull() {
				return fitsync.Value{}, fmt.Errorf("the key's field %s is null", f.Name)
			}
		}
		if len(fs) == 1 {
			return fs[0].Value, nil
		}
	}
	return k, nil
}

// build makes the statement of batch[i].
func (s *Sink) build(i int, r *fitsync.Record) *sinkOp {
	o := &sinkOp{idx: i}
	if r.Op == fitsync.OpDDL {
		o.skip = true
		return o
	}
	var err error
	if o.db, o.coll, err = s.target(r.Namespace); err != nil {
		o.err = err
		return o
	}
	id, err := idOf(r)
	if err != nil {
		o.err = err
		return o
	}
	q, err := mongowire.Marshal(mongowire.Doc("_id", id))
	if err != nil {
		o.err = fmt.Errorf("the _id: %w", err)
		return o
	}
	o.key = o.db + "\x00" + o.coll + "\x00" + string(q)
	if r.Op == fitsync.OpDelete {
		o.del = true
		o.stmt, o.err = mongowire.Marshal(mongowire.Doc("q", q, "limit", int32(1)))
		return o
	}
	if r.After.Kind() != fitsync.KindMap {
		o.err = fmt.Errorf("the row is a %s, not a map", r.After.Kind())
		return o
	}
	fields := make([]fitsync.Field, 0, len(r.After.Fields())+1)
	fields = append(fields, fitsync.Field{Name: "_id", Value: id})
	for _, f := range r.After.Fields() {
		if f.Name != "_id" {
			fields = append(fields, f)
		}
	}
	doc, err := mongowire.MarshalValue(fitsync.Map(fields...))
	if err != nil {
		o.err = err
		return o
	}
	if len(doc) > sinkMaxDocBytes {
		o.err = fmt.Errorf("the document is %d bytes, more than MongoDB's %d", len(doc), sinkMaxDocBytes)
		return o
	}
	o.stmt, o.err = mongowire.Marshal(mongowire.Doc("q", q, "u", doc, "upsert", true))
	return o
}

// Write writes the batch. Each document's last record in the batch is
// written, upserts and deletes of each collection in one unordered bulk
// write (or several, by max_request_bytes): earlier records of a document
// are covered by its last. When a document fails, the records from its
// first in the batch on are written again in runs of distinct documents,
// in order, to find the first record that fails, which a *BatchError names.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	ops := make([]*sinkOp, 0, len(batch))
	for i, r := range batch {
		o := s.build(i, r)
		if o.err != nil {
			if err := s.writeOps(ctx, ops); err != nil {
				return err
			}
			return &fitsync.BatchError{Index: i, Err: fitsync.Permanent(o.err)}
		}
		ops = append(ops, o)
	}
	return s.writeOps(ctx, ops)
}

// writeOps writes ops, whose idx is their index, each document's last.
func (s *Sink) writeOps(ctx context.Context, ops []*sinkOp) error {
	first := make(map[string]int, len(ops))
	last := make(map[string]int, len(ops))
	for i, o := range ops {
		if o.skip {
			continue
		}
		if _, ok := first[o.key]; !ok {
			first[o.key] = i
		}
		last[o.key] = i
	}
	send := make([]*sinkOp, 0, len(last))
	for i, o := range ops {
		if !o.skip && last[o.key] == i {
			send = append(send, o)
		}
	}
	stopped := s.send(ctx, send)
	var failed *sinkOp
	for _, o := range send {
		if o.werr != nil && (failed == nil || first[o.key] < first[failed.key]) {
			failed = o
		}
	}
	if failed == nil {
		return nil
	}
	j := first[failed.key]
	if stopped || j == failed.idx {
		return &fitsync.BatchError{Index: j, Err: failed.werr}
	}
	return s.writeRuns(ctx, ops[j:])
}

// writeRuns writes ops in order, in runs of distinct documents, and
// returns at the first record that fails.
func (s *Sink) writeRuns(ctx context.Context, ops []*sinkOp) error {
	for start := 0; start < len(ops); {
		seen := map[string]bool{}
		var run []*sinkOp
		end := start
		for ; end < len(ops); end++ {
			o := ops[end]
			if o.skip {
				continue
			}
			if seen[o.key] {
				break
			}
			seen[o.key] = true
			o.werr = nil
			run = append(run, o)
		}
		s.send(ctx, run)
		for _, o := range run {
			if o.werr != nil {
				return &fitsync.BatchError{Index: o.idx, Err: o.werr}
			}
		}
		start = end
	}
	return nil
}

// sinkCmd is one bulk write: an update's or a delete's statements of a
// collection.
type sinkCmd struct {
	db, coll string
	del      bool
	ops      []*sinkOp
}

// commands groups ops by collection and kind, in the order they first
// come, each group cut by max_request_bytes and the most statements.
func (s *Sink) commands(ops []*sinkOp) []*sinkCmd {
	var cmds []*sinkCmd
	open := map[string]*sinkCmd{}
	size := map[*sinkCmd]int{}
	for _, o := range ops {
		g := o.db + "\x00" + o.coll
		if o.del {
			g += "\x00d"
		}
		c := open[g]
		if c != nil && (len(c.ops) >= sinkMaxOps || size[c]+len(o.stmt) > s.maxBytes) {
			c = nil
		}
		if c == nil {
			c = &sinkCmd{db: o.db, coll: o.coll, del: o.del}
			open[g] = c
			cmds = append(cmds, c)
		}
		c.ops = append(c.ops, o)
		size[c] += len(o.stmt)
	}
	return cmds
}

// send writes ops, whose documents are distinct, setting each failed one's
// werr. A command's error, or its write concern's, stops it: the ops of
// that command and of those not sent fail with it, and send returns true.
func (s *Sink) send(ctx context.Context, ops []*sinkOp) (stopped bool) {
	cmds := s.commands(ops)
	for n, c := range cmds {
		name, seq := "update", "updates"
		if c.del {
			name, seq = "delete", "deletes"
		}
		cmd := mongowire.Doc(name, c.coll, "ordered", false)
		if len(s.wc) > 0 {
			cmd = append(cmd, mongowire.E{Key: "writeConcern", Value: s.wc})
		}
		docs := make([]mongowire.Raw, len(c.ops))
		for i, o := range c.ops {
			docs[i] = o.stmt
		}
		reply, err := s.client.Run(ctx, c.db, cmd, mongowire.Sequence{ID: seq, Docs: docs})
		if err == nil {
			s.writeErrors(c, reply)
			err = writeConcernError(c, reply)
		} else {
			err = commandError(c, err)
		}
		if err == nil {
			continue
		}
		for _, rest := range cmds[n:] {
			for _, o := range rest.ops {
				if o.werr == nil {
					o.werr = err
				}
			}
		}
		return true
	}
	return false
}

// The codes of a document's write errors that may pass: the primary
// stepping down, a write conflict, a lock or a time limit.
var transientWriteCodes = []int32{6, 7, 24, 46, 50, 64, 89, 91, 112, 133, 189, 262, 9001, 10058, 10107, 11600, 11602, 13435, 13436}

// The codes of a command's errors that cannot pass, the collection's or
// the statements' fault; any other (authentication, a primary lost, a
// write concern) is retried.
var permanentCommandCodes = []int32{2, 9, 14, 72, 73, 10334}

// writeErrors reads a reply's writeErrors into its ops' werr.
func (s *Sink) writeErrors(c *sinkCmd, reply mongowire.Raw) {
	v, ok := reply.Lookup("writeErrors")
	if !ok {
		return
	}
	for _, we := range v.Array() {
		d := we.Document()
		var i, code int64 = -1, 0
		if x, ok := d.Lookup("index"); ok {
			i, _ = x.Int()
		}
		if x, ok := d.Lookup("code"); ok {
			code, _ = x.Int()
		}
		msg := "write error"
		if x, ok := d.Lookup("errmsg"); ok {
			msg, _ = x.StringValue()
		}
		if i < 0 || i >= int64(len(c.ops)) {
			continue
		}
		err := fmt.Errorf("mongo: %s.%s: %s (%d)", c.db, c.coll, msg, code)
		if !slices.Contains(transientWriteCodes, int32(code)) {
			err = fitsync.Permanent(err)
		}
		c.ops[i].werr = err
	}
}

// writeConcernError returns a reply's writeConcernError: the writes were
// made, not acknowledged as asked; a write made again is the same.
func writeConcernError(c *sinkCmd, reply mongowire.Raw) error {
	v, ok := reply.Lookup("writeConcernError")
	if !ok {
		return nil
	}
	d := v.Document()
	var code int64
	if x, ok := d.Lookup("code"); ok {
		code, _ = x.Int()
	}
	msg := "the write concern failed"
	if x, ok := d.Lookup("errmsg"); ok {
		msg, _ = x.StringValue()
	}
	return fmt.Errorf("mongo: %s.%s: the write concern: %s (%d)", c.db, c.coll, msg, code)
}

// commandError wraps a command's error, permanent when the command cannot
// pass as it is.
func commandError(c *sinkCmd, err error) error {
	err = fmt.Errorf("mongo: %s.%s: %w", c.db, c.coll, err)
	var ce *mongowire.CommandError
	if errors.As(err, &ce) && slices.Contains(permanentCommandCodes, ce.Code) {
		return fitsync.Permanent(err)
	}
	return err
}

var (
	_ fitsync.ParallelSink = (*Sink)(nil)
	_ fitsync.Tester       = (*Sink)(nil)
	_ module.Cleaner       = (*Sink)(nil)
)

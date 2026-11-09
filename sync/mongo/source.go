package syncmongo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".mongo", New: func() any { return new(Source) },
		Doc: "Reads MongoDB (6.0 and later, a replica set or a sharded cluster): a snapshot of the collections by _id, then their change stream with fullDocument, from the stream's start taken before the snapshot, so that the two overlap. The checkpoint is the change stream's resume token. A record's namespace is db.collection, its key {\"_id\": ...}, its after the document; the records of a transaction carry its boundaries. A token the oplog no longer holds, or an invalidated stream, is an error saying a new snapshot is needed. In a job (mode once) the snapshot is split into ranges of _id, read job.max_parallel at a time."})
}

// The defaults of the source's params.
const (
	DefaultReadBatch      = 1000
	MaxReadBatch          = 100000
	DefaultMaxAwait       = time.Second
	DefaultChunkDocuments = 100000
)

// The source's retries of a cursor cut by a failover or a lost connection,
// before the error is the pipeline's, which restarts it from its checkpoint.
var (
	maxRetries   = 5
	retryBackoff = 100 * time.Millisecond
)

// Source is the source mongo.
type Source struct {
	URI            string          `json:"uri,omitempty" doc:"The servers as a connection string, mongodb://[user@]host1[:port1][,host2...][/authdb][?options], as the MongoDB drivers take it, with the options replicaSet, authSource, authMechanism (SCRAM-SHA-256), tls, tlsCAFile, tlsCertificateKeyFile, connectTimeoutMS, socketTimeoutMS, serverSelectionTimeoutMS, maxPoolSize and appName; any other is refused, and so is a password in it: password_env holds it. mongodb+srv is not read. uri or hosts; the params below replace what it sets."`
	Hosts          []string        `json:"hosts,omitempty" doc:"The servers to find the primary from, host or host:port (27017 when left out), in place of uri: a replica set's members (the others are learned from them), a standalone server, or mongos routers. The primary is found again after a failover."`
	ReplicaSet     string          `json:"replica_set,omitempty" doc:"The replica set's name: a server of another set is not taken. Any set when left out."`
	Username       string          `json:"username,omitempty" doc:"The user to authenticate as, with SCRAM-SHA-256 and password_env, in place of a user in uri; no authentication when neither names one."`
	PasswordEnv    string          `json:"password_env,omitempty" doc:"The environment variable holding the user's password, read when the pipeline loads; needs a user, in username or uri."`
	AuthSource     string          `json:"auth_source,omitempty" doc:"The database the user is defined in. admin when left out."`
	TLS            bool            `json:"tls,omitempty" doc:"Connect over TLS, the servers' certificates checked against the system's roots or ca_file's; the check cannot be turned off. These TLS params replace the connection string's TLS options."`
	CAFile         string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the servers' are checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile       string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, presented to servers that ask for one; with its key in it too, as MongoDB's tlsCertificateKeyFile, unless key_file is set. Needs tls."`
	KeyFile        string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key, when cert_file does not hold it."`
	ServerName     string          `json:"server_name,omitempty" doc:"The name the servers' certificates must have, and the TLS server name sent; each server's host when left out."`
	ConnectTimeout config.Duration `json:"connect_timeout,omitempty" default:"\"10s\"" doc:"How long connecting to a server may take, its TLS, hello and authentication. 10s when left out or 0."`
	Timeout        config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one command may take, from sending it to the end of its answer: a batch of a cursor, a bulk write of up to the sink's max_request_bytes. 60s when left out or 0."`
	MaxConns       int             `json:"max_conns,omitempty" min:"0" max:"1000" default:"4" doc:"The most connections open to the primary, the open cursors' among them, shared by a job's parallel readers or writers. 4 when left out or 0."`
	Database       string          `json:"database,omitempty" doc:"The database read. Every database but admin, config and local when left out."`
	Collections    []string        `json:"collections,omitempty" doc:"The collections of database read. Every collection of it (not views, time series or system collections) when left out; needs database."`
	FullDocument   string          `json:"full_document,omitempty" enum:"updateLookup,whenAvailable,required" default:"\"updateLookup\"" doc:"What an update's after is: updateLookup, the document as it is when the change is read (an update of a document deleted since is passed over: its delete follows); whenAvailable or required, its post-image, which the collection must record (changeStreamPreAndPostImages), passed over or an error when there is none. updateLookup when left out."`
	Before         string          `json:"full_document_before_change,omitempty" enum:"off,whenAvailable,required" default:"\"off\"" doc:"Whether updates and deletes carry the document as it was, their before: off, no; whenAvailable or required, its pre-image, which the collection must record (changeStreamPreAndPostImages), left out or an error when there is none. off when left out."`
	Snapshot       string          `json:"snapshot,omitempty" enum:"initial,never" default:"\"initial\"" doc:"Where a pipeline without a checkpoint starts: initial, every document there is, then the changes from just before it read them; never, the changes from now. initial when left out."`
	BatchSize      int             `json:"batch_size,omitempty" min:"0" max:"100000" default:"1000" doc:"The most documents, or changes, one batch of a cursor holds. 1000 when left out or 0."`
	MaxAwait       config.Duration `json:"max_await,omitempty" default:"\"1s\"" doc:"How long the server holds a request for changes while there are none, below timeout; also how long the last record of a transaction may wait to be known as its last. 1s when left out or 0."`
	ChunkDocs      int64           `json:"chunk_documents,omitempty" min:"0" default:"100000" doc:"In a job (mode once), the documents a chunk of the snapshot is reckoned to hold: each collection is split into as many ranges of _id as its count needs, from a sample of _ids, read job.max_parallel at a time. 100000 when left out or 0."`

	client *mongowire.Client
}

// conn is the source's ConnParams.
func (s *Source) conn() ConnParams {
	return ConnParams{URI: s.URI, Hosts: s.Hosts, ReplicaSet: s.ReplicaSet, Username: s.Username, PasswordEnv: s.PasswordEnv,
		AuthSource: s.AuthSource, TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName,
		ConnectTimeout: s.ConnectTimeout, Timeout: s.Timeout, MaxConns: s.MaxConns}
}

// Provision checks the params; nothing is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	c, errs := NewClient(ctx, s.conn())
	s.client = c
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.Database != "" && !mongoName(s.Database, "/\\. \"$") {
		add("database", "%q is not a database's name", s.Database)
	}
	if len(s.Collections) > 0 && s.Database == "" {
		add("collections", "collections needs database")
	}
	for i, c := range s.Collections {
		if !mongoName(c, "$") || strings.HasPrefix(c, "system.") || slices.Index(s.Collections, c) != i {
			errs = append(errs, ctx.Params.Field("collections").Index(i).Errorf("%q is not a collection's name, or is named twice", c))
		}
	}
	if s.client != nil && s.maxAwait() >= s.client.Options().Timeout {
		add("max_await", "max_await %v is not below timeout %v", s.maxAwait(), s.client.Options().Timeout)
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

func mongoName(n, bad string) bool {
	return n != "" && !strings.ContainsAny(n, bad+"\x00")
}

// Cleanup closes the client.
func (s *Source) Cleanup() {
	if s.client != nil {
		s.client.Close()
	}
}

// Test checks that the source reaches a primary, authenticates, and that
// the deployment has change streams.
func (s *Source) Test(ctx context.Context) error {
	if err := Ping(ctx, s.client); err != nil {
		return err
	}
	r, err := s.client.Run(ctx, "admin", mongowire.Doc("hello", 1))
	if err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	set, _ := r.Lookup("setName")
	msg, _ := r.Lookup("msg")
	if name, _ := set.StringValue(); name == "" {
		if m, _ := msg.StringValue(); m != "isdbgrid" {
			return errors.New("mongo: the server is a standalone server: change streams need a replica set or a sharded cluster")
		}
	}
	return nil
}

func (s *Source) batchSize() int32 {
	if s.BatchSize <= 0 {
		return DefaultReadBatch
	}
	return int32(min(s.BatchSize, MaxReadBatch))
}

func (s *Source) maxAwait() time.Duration {
	if s.MaxAwait <= 0 {
		return DefaultMaxAwait
	}
	return s.MaxAwait.Std()
}

// position is the source's checkpoint: Token, a change stream's resume
// token, once it reads the stream; Start, the stream's token taken before
// the snapshot, NS and ID, the collection and the last _id read, while it
// reads the snapshot. A chunk's position is its last ID alone. The token and
// the _id are in the record's canonical JSON.
type position struct {
	Token json.RawMessage `json:"token,omitempty"`
	Start json.RawMessage `json:"start,omitempty"`
	NS    string          `json:"ns,omitempty"`
	ID    json.RawMessage `json:"id,omitempty"`
}

func (p position) marshal() fitsync.Position {
	b, _ := json.Marshal(p)
	return b
}

// valueOf returns a document of a reply as a value. The reply was checked
// whole when it was read, deeper than any document it holds, so that the
// document reads.
func valueOf(d mongowire.Raw) fitsync.Value {
	v, _ := d.Value()
	return v
}

// jsonOf returns a value read from BSON in canonical JSON, which every kind
// BSON reads as has.
func jsonOf(v fitsync.Value) json.RawMessage {
	b, _ := v.MarshalJSON()
	return b
}

// docOf reads a document from its canonical JSON.
func docOf(b json.RawMessage) (mongowire.Raw, error) {
	var v fitsync.Value
	if err := v.UnmarshalJSON(b); err != nil {
		return nil, err
	}
	if v.Kind() != fitsync.KindMap {
		return nil, fmt.Errorf("%s is not a document", b)
	}
	return mongowire.MarshalValue(v)
}

// Run reads the snapshot, unless the checkpoint is past it or snapshot is
// never, then the change stream.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var pos position
	if r.From != nil {
		if err := json.Unmarshal(r.From, &pos); err != nil {
			return fmt.Errorf("mongo: the checkpoint %s: %w", r.From, err)
		}
	}
	var start json.RawMessage
	switch {
	case pos.Token != nil:
		start = pos.Token
	case pos.Start != nil:
		if err := s.snapshot(ctx, r, &pos); err != nil {
			return err
		}
		start = pos.Start
	case r.From != nil:
		return fmt.Errorf("mongo: the checkpoint %s has neither a token nor a snapshot's start", r.From)
	case s.Snapshot != "never":
		tok, err := s.startToken(ctx)
		if err != nil {
			return err
		}
		pos.Start = jsonOf(valueOf(tok))
		// The stream's start with the snapshot not begun is the
		// pipeline's checkpoint before its first record: a run killed
		// before the next reads the snapshot again and streams from this
		// start, replaying the changes since over it, rather than from a
		// later one, which would lose them.
		if err := r.SaveStart(pos.marshal()); err != nil {
			return err
		}
		if err := s.snapshot(ctx, r, &pos); err != nil {
			return err
		}
		start = pos.Start
	}
	var token mongowire.Raw
	if start != nil {
		var err error
		if token, err = docOf(start); err != nil {
			return fmt.Errorf("mongo: the checkpoint's token: %w", err)
		}
	}
	st := &streamer{s: s, r: r, token: token}
	return st.run(ctx)
}

// target is the change stream's: the deployment, a database, or one
// collection; and the database the command goes to.
func (s *Source) target() (db string, aggregate any) {
	switch {
	case s.Database == "":
		return "admin", 1
	case len(s.Collections) == 1:
		return s.Database, s.Collections[0]
	}
	return s.Database, 1
}

// openStream opens the change stream after token, or from now when token is
// nil.
func (s *Source) openStream(ctx context.Context, token mongowire.Raw, batch int32) (*mongowire.Cursor, error) {
	cs := mongowire.Doc("fullDocument", s.fullDocument())
	if s.Before != "" && s.Before != "off" {
		cs = append(cs, mongowire.E{Key: "fullDocumentBeforeChange", Value: s.Before})
	}
	if token != nil {
		cs = append(cs, mongowire.E{Key: "resumeAfter", Value: token})
	}
	if s.Database == "" {
		cs = append(cs, mongowire.E{Key: "allChangesForCluster", Value: true})
	}
	pipeline := mongowire.A{mongowire.Doc("$changeStream", cs)}
	if len(s.Collections) > 1 {
		colls := make(mongowire.A, len(s.Collections))
		for i, c := range s.Collections {
			colls[i] = c
		}
		pipeline = append(pipeline, mongowire.Doc("$match", mongowire.Doc("$or", mongowire.A{
			mongowire.Doc("ns.coll", mongowire.Doc("$in", colls)),
			mongowire.Doc("operationType", mongowire.Doc("$in", mongowire.A{"invalidate", "dropDatabase"})),
		})))
	}
	db, agg := s.target()
	cmd := mongowire.Doc("aggregate", agg, "pipeline", pipeline, "cursor", mongowire.Doc("batchSize", batch))
	cur, err := s.client.Cursor(ctx, db, cmd, mongowire.CursorOptions{BatchSize: s.batchSize(), MaxAwait: s.maxAwait()})
	if err != nil {
		return nil, streamErr(err)
	}
	return cur, nil
}

func (s *Source) fullDocument() string {
	if s.FullDocument == "" {
		return "updateLookup"
	}
	return s.FullDocument
}

// startToken returns the change stream's token now, which the stream
// resumes from after the snapshot: what changes while it is read is read
// again from the stream.
func (s *Source) startToken(ctx context.Context) (mongowire.Raw, error) {
	cur, err := s.openStream(ctx, nil, 0)
	if err != nil {
		return nil, err
	}
	defer closeCursor(ctx, cur)
	tok := cur.PostBatchResumeToken()
	if tok == nil {
		return nil, errors.New("mongo: the change stream answered without a resume token")
	}
	return append(mongowire.Raw(nil), tok...), nil
}

// closeCursor closes cur, even once ctx has ended.
func closeCursor(ctx context.Context, cur *mongowire.Cursor) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = cur.Close(ctx)
}

// The errors of a change stream that cannot resume from its token.
var noResumeCodes = []int32{
	286, // ChangeStreamHistoryLost
	280, // ChangeStreamFatalError
	260, // InvalidResumeToken
}

// errInvalidated is an invalidate event.
var errInvalidated = errors.New("mongo: the change stream is invalidated (its collection or database was dropped or renamed): a new snapshot is needed (the pipeline's snapshot, or a reset of its checkpoint)")

// streamErr says what a change stream's error means.
func streamErr(err error) error {
	var ce *mongowire.CommandError
	if errors.As(err, &ce) && slices.Contains(noResumeCodes, ce.Code) {
		return fmt.Errorf("mongo: the change stream cannot resume from the checkpoint's token, which the oplog no longer holds or which is not of this stream: a new snapshot is needed (the pipeline's snapshot, or a reset of its checkpoint): %w", err)
	}
	return fmt.Errorf("mongo: %w", err)
}

// The codes of errors a cursor is opened again after, besides a
// connection's and a not-primary's.
var resumableCodes = []int32{
	6,     // HostUnreachable
	7,     // HostNotFound
	43,    // CursorNotFound
	63,    // StaleShardVersion
	89,    // NetworkTimeout
	133,   // FailedToSatisfyReadPreference
	150,   // StaleEpoch
	234,   // RetryChangeStream
	237,   // CursorKilled
	9001,  // SocketException
	13388, // StaleConfig
}

// resumable reports whether a cursor cut by err can be opened again where it
// was: a failover, a connection lost, a cursor the server timed out.
func resumable(err error) bool {
	if mongowire.IsNetError(err) || mongowire.IsNotPrimary(err) {
		return true
	}
	var ce *mongowire.CommandError
	return errors.As(err, &ce) && (slices.Contains(resumableCodes, ce.Code) || ce.HasLabel("ResumableChangeStreamError"))
}

// retry reports whether a cursor cut by err is to be opened again, after a
// backoff, counting failures.
func retryCursor(ctx context.Context, err error, failures *int) bool {
	if ctx.Err() != nil || !resumable(err) || *failures >= maxRetries {
		return false
	}
	*failures++
	return sleepCtx(ctx, retryBackoff<<(*failures-1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// streamer reads a change stream.
type streamer struct {
	s *Source
	r *fitsync.Run
	// token is where the stream is opened again after: the last event's,
	// or the last batch's post-batch token.
	token mongowire.Raw
	// held is a transaction's record, held until the next tells whether it
	// is the transaction's last.
	held *fitsync.Record
}

func (st *streamer) run(ctx context.Context) error {
	failures := 0
	for {
		cur, err := st.s.openStream(ctx, st.token, st.s.batchSize())
		if err == nil {
			err = st.follow(ctx, cur, &failures)
			closeCursor(ctx, cur)
		}
		if err == nil {
			return st.flush()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !retryCursor(ctx, err, &failures) {
			return err
		}
	}
}

// follow reads cur's batches, until an error, or, in a job, until a batch
// asked for is empty: the stream is caught up.
func (st *streamer) follow(ctx context.Context, cur *mongowire.Cursor, failures *int) error {
	asked := false
	for {
		batch := cur.Batch()
		for _, ev := range batch {
			rec, tx, err := st.s.event(ev)
			if err != nil {
				return err
			}
			if id, ok := ev.Lookup("_id"); ok {
				st.token = append(mongowire.Raw(nil), id.Document()...)
			}
			if rec != nil {
				if err := st.push(rec, tx); err != nil {
					return err
				}
			}
		}
		if t := cur.PostBatchResumeToken(); t != nil {
			st.token = append(mongowire.Raw(nil), t...)
		}
		if len(batch) == 0 && asked {
			// Nothing more came while the server waited: a transaction
			// held has ended.
			if err := st.flush(); err != nil {
				return err
			}
			if st.r.Once {
				return nil
			}
		}
		more, err := cur.NextBatch(ctx)
		if err != nil {
			return streamErr(err)
		}
		if !more {
			return errors.New("mongo: the server closed the change stream's cursor")
		}
		asked = true
		*failures = 0
	}
}

// push hands rec on, of transaction tx ("" for none): a transaction's
// record is held until the next record tells whether it is the last.
func (st *streamer) push(rec *fitsync.Record, tx string) error {
	if h := st.held; h != nil {
		h.Tx.Last = h.Tx.ID != tx
		st.held = nil
		if err := st.r.Emit(h); err != nil {
			return err
		}
		if tx != "" && !h.Tx.Last {
			rec.Tx = fitsync.Tx{ID: tx}
			st.held = rec
			return nil
		}
	}
	if tx == "" {
		return st.r.Emit(rec)
	}
	rec.Tx = fitsync.Tx{ID: tx, First: true}
	st.held = rec
	return nil
}

// flush hands on the record held, the last of its transaction.
func (st *streamer) flush() error {
	h := st.held
	if h == nil {
		return nil
	}
	st.held = nil
	h.Tx.Last = true
	return st.r.Emit(h)
}

// event makes a change event's record, and names its transaction; nil for
// an event passed over.
func (s *Source) event(ev mongowire.Raw) (*fitsync.Record, string, error) {
	opv, _ := ev.Lookup("operationType")
	op, _ := opv.StringValue()
	idv, ok := ev.Lookup("_id")
	if !ok || idv.Type != mongowire.TypeDocument {
		return nil, "", errors.New("mongo: a change event without its resume token: a pipeline may not change or remove _id")
	}
	if op == "invalidate" {
		return nil, "", errInvalidated
	}
	tok := jsonOf(valueOf(idv.Document()))
	nsv, _ := ev.Lookup("ns")
	ns := eventNS(nsv.Document())
	rec := &fitsync.Record{Source: "mongo", Namespace: ns, Position: position{Token: tok}.marshal()}
	if t, ok := rawField(ev, "wallTime").Time(); ok {
		rec.CommitTime = t
	} else if ts, ok := rawField(ev, "clusterTime").Timestamp(); ok {
		rec.CommitTime = time.Unix(int64(ts.T), 0).UTC()
	}
	doc := func(field string) fitsync.Value {
		v, ok := ev.Lookup(field)
		if !ok || v.Type != mongowire.TypeDocument {
			return fitsync.Null()
		}
		return valueOf(v.Document())
	}
	switch op {
	case "insert", "update", "replace", "delete":
		id, ok := doc("documentKey").Get("_id")
		if !ok {
			return nil, "", fmt.Errorf("mongo: a change event of %s without documentKey._id", ns)
		}
		rec.Key = fitsync.Map(fitsync.Field{Name: "_id", Value: id})
		rec.Before = doc("fullDocumentBeforeChange")
		switch op {
		case "insert":
			rec.Op = fitsync.OpInsert
		case "delete":
			rec.Op = fitsync.OpDelete
		default:
			rec.Op = fitsync.OpUpdate
		}
		if op != "delete" {
			if rec.After = doc("fullDocument"); rec.After.IsNull() {
				return nil, "", nil // deleted since, or without its post-image
			}
		}
	default:
		// drop, rename, dropDatabase, and what else the server reports.
		f := []fitsync.Field{{Name: "operationType", Value: fitsync.String(op)}, {Name: "ns", Value: fitsync.String(ns)}}
		if to, ok := ev.Lookup("to"); ok {
			f = append(f, fitsync.Field{Name: "to", Value: fitsync.String(eventNS(to.Document()))})
		}
		rec.Op, rec.After = fitsync.OpDDL, fitsync.Map(f...)
	}
	return rec, txID(ev), nil
}

func rawField(d mongowire.Raw, key string) mongowire.RawValue {
	v, _ := d.Lookup(key)
	return v
}

// eventNS is an event's ns as db.collection, or db alone.
func eventNS(ns mongowire.Raw) string {
	db, _ := rawField(ns, "db").StringValue()
	if coll, ok := rawField(ns, "coll").StringValue(); ok {
		return db + "." + coll
	}
	return db
}

// txID names an event's transaction, "" when it is in none: its session's ID
// and its number in the session.
func txID(ev mongowire.Raw) string {
	n, ok := rawField(ev, "txnNumber").Int()
	if !ok {
		return ""
	}
	id, _ := rawField(rawField(ev, "lsid").Document(), "id").Binary()
	return hex.EncodeToString(id.Data) + ":" + strconv.FormatInt(n, 10)
}

// snapshot reads the documents of every collection, in the order of their
// namespaces, each by _id, from where pos says; the last record of all
// carries the stream's start as its position, so that a restart after it
// reads the stream.
func (s *Source) snapshot(ctx context.Context, r *fitsync.Run, pos *position) error {
	nss, err := s.namespaces(ctx)
	if err != nil {
		return err
	}
	var held *fitsync.Record
	for _, ns := range nss {
		if ns < pos.NS {
			continue
		}
		var lo *fitsync.Value
		if ns == pos.NS && pos.ID != nil {
			var id fitsync.Value
			if err := id.UnmarshalJSON(pos.ID); err != nil {
				return fmt.Errorf("mongo: the checkpoint's _id %s: %w", pos.ID, err)
			}
			lo = &id
		}
		start := pos.Start
		err := s.scan(ctx, ns, lo, lo != nil, nil, func(id json.RawMessage) fitsync.Position {
			return position{Start: start, NS: ns, ID: id}.marshal()
		}, func(rec *fitsync.Record) error {
			if held != nil {
				if err := r.Emit(held); err != nil {
					return err
				}
			}
			held = rec
			return nil
		})
		if err != nil {
			return err
		}
	}
	if held != nil {
		held.Position = position{Token: pos.Start}.marshal()
		return r.Emit(held)
	}
	return nil
}

// nsParts splits db.collection.
func nsParts(ns string) (string, string) {
	db, coll, _ := strings.Cut(ns, ".")
	return db, coll
}

// scan reads the documents of collection ns in _id order, from lo (after it
// with skip) to before hi (nil: unbounded), handing each record, its
// position made by posOf, to each. A cursor cut by a failover, or timed out
// while the pipeline held it, is opened again after the last _id read.
func (s *Source) scan(ctx context.Context, ns string, lo *fitsync.Value, skip bool, hi *fitsync.Value,
	posOf func(id json.RawMessage) fitsync.Position, each func(*fitsync.Record) error) error {
	db, coll := nsParts(ns)
	failures := 0
	for {
		cmd := mongowire.Doc("find", coll, "filter", mongowire.Doc(), "sort", mongowire.Doc("_id", 1), "hint", mongowire.Doc("_id", 1),
			"batchSize", s.batchSize(), "readConcern", mongowire.Doc("level", "majority"))
		if lo != nil {
			cmd = append(cmd, mongowire.E{Key: "min", Value: mongowire.Doc("_id", *lo)})
		}
		if hi != nil {
			cmd = append(cmd, mongowire.E{Key: "max", Value: mongowire.Doc("_id", *hi)})
		}
		cur, err := s.client.Cursor(ctx, db, cmd, mongowire.CursorOptions{BatchSize: s.batchSize()})
		for err == nil {
			var d mongowire.Raw
			if d, err = cur.Next(ctx); err != nil || d == nil {
				break
			}
			failures = 0
			v := valueOf(d)
			id, _ := v.Get("_id")
			if skip && lo != nil && id.Equal(*lo) {
				continue
			}
			lo, skip = &id, true
			rec := &fitsync.Record{Source: "mongo", Namespace: ns, Op: fitsync.OpInsert,
				Key: fitsync.Map(fitsync.Field{Name: "_id", Value: id}), After: v, Position: posOf(jsonOf(id))}
			if err = each(rec); err != nil {
				closeCursor(ctx, cur)
				return err
			}
		}
		if cur != nil {
			closeCursor(ctx, cur)
		}
		if err == nil {
			return nil
		}
		if !retryCursor(ctx, err, &failures) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("mongo: reading %s: %w", ns, err)
		}
	}
}

// systemDBs are the databases a deployment's snapshot leaves out, as its
// change stream does.
var systemDBs = []string{"admin", "config", "local"}

// namespaces lists the collections the source reads, as db.collection,
// sorted.
func (s *Source) namespaces(ctx context.Context) ([]string, error) {
	var nss []string
	if len(s.Collections) > 0 {
		for _, c := range s.Collections {
			nss = append(nss, s.Database+"."+c)
		}
		slices.Sort(nss)
		return nss, nil
	}
	dbs := []string{s.Database}
	if s.Database == "" {
		r, err := s.client.Run(ctx, "admin", mongowire.Doc("listDatabases", 1, "nameOnly", true, "authorizedDatabases", true))
		if err != nil {
			return nil, fmt.Errorf("mongo: listing the databases: %w", err)
		}
		dbs = dbs[:0]
		for _, d := range rawField(r, "databases").Array() {
			if name, _ := rawField(d.Document(), "name").StringValue(); !slices.Contains(systemDBs, name) {
				dbs = append(dbs, name)
			}
		}
	}
	for _, db := range dbs {
		cur, err := s.client.Cursor(ctx, db, mongowire.Doc("listCollections", 1, "filter", mongowire.Doc("type", "collection"),
			"nameOnly", true, "authorizedCollections", true), mongowire.CursorOptions{})
		for err == nil {
			var d mongowire.Raw
			if d, err = cur.Next(ctx); err != nil || d == nil {
				break
			}
			if name, _ := rawField(d, "name").StringValue(); !strings.HasPrefix(name, "system.") {
				nss = append(nss, db+"."+name)
			}
		}
		if cur != nil {
			closeCursor(ctx, cur)
		}
		if err != nil {
			return nil, fmt.Errorf("mongo: listing the collections of %s: %w", db, err)
		}
	}
	slices.Sort(nss)
	return nss, nil
}

// sampleFactor is how many _ids are sampled for each chunk's bound.
const sampleFactor = 4

// Chunks splits a job's snapshot into ranges of _id: each collection into as
// many as its count needs, bounded by a sorted sample of its _ids. None with
// snapshot never, whose job reads the change stream.
func (s *Source) Chunks(ctx context.Context) ([]fitsync.Chunk, error) {
	if s.Snapshot == "never" {
		return nil, nil
	}
	nss, err := s.namespaces(ctx)
	if err != nil {
		return nil, err
	}
	if len(nss) > fitsync.MaxChunks {
		return nil, fmt.Errorf("mongo: %d collections, past the %d chunks a job may have", len(nss), fitsync.MaxChunks)
	}
	per := s.ChunkDocs
	if per <= 0 {
		per = DefaultChunkDocuments
	}
	var plan []fitsync.Chunk
	for i, ns := range nss {
		db, coll := nsParts(ns)
		r, err := s.client.Run(ctx, db, mongowire.Doc("count", coll))
		if err != nil {
			return nil, fmt.Errorf("mongo: counting %s: %w", ns, err)
		}
		n, _ := rawField(r, "n").Int()
		room := fitsync.MaxChunks - len(plan) - (len(nss) - i - 1)
		k := int(min(max((n+per-1)/per, 1), int64(room)))
		var bounds []json.RawMessage
		if k > 1 {
			if bounds, err = s.bounds(ctx, ns, k); err != nil {
				return nil, err
			}
		}
		k = len(bounds) + 1
		for j := range k {
			c := fitsync.Chunk{Namespace: ns, Rows: n / int64(k)}
			if j < int(n%int64(k)) {
				c.Rows++
			}
			if j > 0 {
				c.Lo = bounds[j-1]
			}
			if j < len(bounds) {
				c.Hi = bounds[j]
			}
			plan = append(plan, c)
		}
	}
	return plan, nil
}

// bounds returns at most k-1 _ids splitting collection ns into k ranges of
// about as many documents, from a sample, in canonical JSON.
func (s *Source) bounds(ctx context.Context, ns string, k int) ([]json.RawMessage, error) {
	db, coll := nsParts(ns)
	cur, err := s.client.Cursor(ctx, db, mongowire.Doc("aggregate", coll, "pipeline", mongowire.A{
		mongowire.Doc("$sample", mongowire.Doc("size", k*sampleFactor)),
		mongowire.Doc("$project", mongowire.Doc("_id", 1)),
		mongowire.Doc("$sort", mongowire.Doc("_id", 1)),
	}, "allowDiskUse", true, "cursor", mongowire.Doc()), mongowire.CursorOptions{})
	var ids []fitsync.Value
	for err == nil {
		var d mongowire.Raw
		if d, err = cur.Next(ctx); err != nil || d == nil {
			break
		}
		id, _ := valueOf(d).Get("_id")
		ids = append(ids, id)
	}
	if cur != nil {
		closeCursor(ctx, cur)
	}
	if err != nil {
		return nil, fmt.Errorf("mongo: sampling %s: %w", ns, err)
	}
	var out []json.RawMessage
	var last *fitsync.Value
	for j := 1; j < k; j++ {
		i := j * len(ids) / k
		if i == 0 || i >= len(ids) || last != nil && ids[i].Equal(*last) {
			continue
		}
		last = &ids[i]
		out = append(out, jsonOf(ids[i]))
	}
	return out, nil
}

// ReadChunk reads a range of _id of a collection, after from's _id when
// from is set.
func (s *Source) ReadChunk(ctx context.Context, c fitsync.Chunk, from fitsync.Position, emit func(*fitsync.Record) error) error {
	value := func(b json.RawMessage) (*fitsync.Value, error) {
		if b == nil {
			return nil, nil
		}
		var v fitsync.Value
		if err := v.UnmarshalJSON(b); err != nil {
			return nil, fmt.Errorf("mongo: the chunk's bound %s: %w", b, err)
		}
		return &v, nil
	}
	lo, err := value(c.Lo)
	if err != nil {
		return err
	}
	hi, err := value(c.Hi)
	if err != nil {
		return err
	}
	skip := false
	if from != nil {
		var p position
		if err := json.Unmarshal(from, &p); err != nil {
			return fmt.Errorf("mongo: the chunk's position %s: %w", from, err)
		}
		if lo, err = value(p.ID); err != nil {
			return err
		}
		skip = lo != nil
	}
	return s.scan(ctx, c.Namespace, lo, skip, hi, func(id json.RawMessage) fitsync.Position {
		return position{ID: id}.marshal()
	}, emit)
}

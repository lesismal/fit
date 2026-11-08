package syncredis

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/redis/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".redis", New: func() any { return new(Source) },
		Doc: "Reads Redis or Valkey streams with XREAD, from the entry IDs of the checkpoint, each entry a record: a message of its fields, or the record the sink redis wrote with form record. Its position is the last entry ID read of each stream, so a restart reads on after it; no consumer group is made or acknowledged. In a job (mode once) it reads what the streams hold and stops."})
}

// The defaults of the source's params.
const (
	DefaultCount = 1000
	DefaultBlock = 5 * time.Second
	// recordField is the field an entry of form record holds the record in.
	recordField = "record"
)

// Source is the source redis.
type Source struct {
	Address     string          `json:"address,omitempty" default:"\"127.0.0.1:6379\"" doc:"The server's host and port, a standalone server or a primary (not Redis Cluster). 127.0.0.1:6379 when left out."`
	Username    string          `json:"username,omitempty" doc:"The ACL user to authenticate as, with password_env; the default user when left out."`
	PasswordEnv string          `json:"password_env,omitempty" doc:"The environment variable holding the password, read when the pipeline loads; no authentication when left out."`
	DB          int             `json:"db,omitempty" min:"0" doc:"The database selected. 0 when left out."`
	Protocol    int             `json:"protocol,omitempty" min:"0" max:"3" doc:"The RESP version spoken, 2 or 3. When left out, 3 if the server knows HELLO (Redis 6 and later, Valkey), 2 otherwise."`
	TLS         bool            `json:"tls,omitempty" doc:"Connect over TLS, the server's certificate checked against the system's roots or ca_file's; the check cannot be turned off."`
	CAFile      string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own; needs tls."`
	CertFile    string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one (tls-auth-clients); needs tls and key_file."`
	KeyFile     string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName  string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; address's host when left out."`
	Timeout     config.Duration `json:"timeout,omitempty" default:"\"10s\"" doc:"How long connecting, and each command but XREAD's wait, may take. 10s when left out or 0."`
	Streams     []string        `json:"streams" fit:"required" doc:"The streams read, by their keys, together, in one XREAD."`
	Form        string          `json:"form,omitempty" enum:"fields,record" default:"\"fields\"" doc:"What an entry is: fields, a message record whose row is the entry's fields, its key the entry's ID; record, the record held in its record field in canonical JSON, as the sink redis writes with form record, with its operation, key, namespace and rows (an entry without one is read as fields). fields when left out."`
	Namespace   string          `json:"namespace,omitempty" doc:"The records' namespace. The stream's key when left out, or, for form record, the record's own."`
	Snapshot    string          `json:"snapshot,omitempty" enum:"initial,never" default:"\"initial\"" doc:"Where a pipeline without a checkpoint starts: initial, at each stream's first entry; never, after its last entry now, reading only what comes after. initial when left out."`
	Count       int             `json:"count,omitempty" min:"0" max:"100000" default:"1000" doc:"The most entries one XREAD reads of each stream. 1000 when left out or 0."`
	Block       config.Duration `json:"block,omitempty" default:"\"5s\"" doc:"How long one XREAD waits for new entries before asking again. 5s when left out or 0."`

	conn conn
}

// position is the source's: the last entry ID read of each stream.
type position struct {
	Streams map[string]string `json:"streams"`
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the source runs.
func (s *Source) Provision(ctx *module.Context) error {
	o, errs := options(ctx, connParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv, DB: s.DB, Protocol: s.Protocol,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn.opts = o
	if len(s.Streams) == 0 {
		errs = append(errs, ctx.Params.Field("streams").Errorf("no streams"))
	}
	seen := map[string]bool{}
	for _, st := range s.Streams {
		if st == "" || seen[st] || !utf8.ValidString(st) {
			errs = append(errs, ctx.Params.Field("streams").Errorf("%q is empty, not UTF-8 or named twice", st))
		}
		seen[st] = true
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Cleanup closes the connection.
func (s *Source) Cleanup() { s.conn.close() }

// Test connects, authenticates and selects the database on a connection of
// its own, and PINGs.
func (s *Source) Test(ctx context.Context) error { return s.conn.test(ctx) }

func (s *Source) count() int {
	if s.Count <= 0 {
		return DefaultCount
	}
	return s.Count
}

func (s *Source) block() time.Duration {
	if s.Block <= 0 {
		return DefaultBlock
	}
	return s.Block.Std()
}

// do runs one command within the timeout.
func (s *Source) do(ctx context.Context, args ...string) (wire.Value, error) {
	ctx, cancel := context.WithTimeout(ctx, s.conn.opts.DialTimeout)
	defer cancel()
	c, err := s.conn.get(ctx)
	if err != nil {
		return wire.Value{}, err
	}
	v, err := c.Do(ctx, args...)
	if err == nil {
		err = v.Err()
	}
	return v, err
}

// start returns the IDs each stream is read after: the checkpoint's, or,
// for a stream it has none of, 0-0, or its last entry's for snapshot never.
func (s *Source) start(ctx context.Context, from fitsync.Position) ([]string, error) {
	var p position
	if from != nil {
		if err := json.Unmarshal(from, &p); err != nil {
			return nil, fmt.Errorf("redis: the checkpoint %s: %w", from, err)
		}
	}
	ids := make([]string, len(s.Streams))
	for i, st := range s.Streams {
		if id, ok := p.Streams[st]; ok {
			if _, _, ok := ParseID(id); !ok {
				return nil, fmt.Errorf("redis: the checkpoint's ID %q of %s", id, st)
			}
			ids[i] = id
			continue
		}
		ids[i] = "0-0"
		if s.Snapshot == "never" {
			v, err := s.do(ctx, "XREVRANGE", st, "+", "-", "COUNT", "1")
			if err != nil {
				return nil, fmt.Errorf("redis: the last entry of %s: %w", st, err)
			}
			if len(v.Elems) > 0 && len(v.Elems[0].Elems) > 0 {
				if id, ok := v.Elems[0].Elems[0].Text(); ok {
					ids[i] = id
				}
			}
		}
	}
	return ids, nil
}

// Run reads the streams with XREAD, after the IDs of the checkpoint, until
// ctx ends, or, in a job, until an XREAD finds nothing more.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	ids, err := s.start(ctx, r.From)
	if err != nil {
		return err
	}
	index := make(map[string]int, len(s.Streams))
	for i, st := range s.Streams {
		index[st] = i
	}
	count := strconv.Itoa(s.count())
	block := s.block()
	for {
		args := []string{"XREAD", "COUNT", count}
		if !r.Once {
			args = append(args, "BLOCK", strconv.FormatInt(max(block.Milliseconds(), 1), 10))
		}
		args = append(args, "STREAMS")
		args = append(args, s.Streams...)
		args = append(args, ids...)
		c, err := s.conn.get(ctx)
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		rctx, cancel := context.WithTimeout(ctx, s.conn.opts.DialTimeout+block)
		v, err := c.Do(rctx, args...)
		cancel()
		if err == nil {
			err = v.Err()
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("redis: XREAD: %w", err)
		}
		if v.IsNull() {
			if r.Once {
				return nil
			}
			continue
		}
		replies, ok := streamReplies(v)
		if !ok {
			return fmt.Errorf("redis: XREAD answered a %s", v.Kind)
		}
		read := 0
		for _, sr := range replies {
			i, ok := index[sr.stream]
			if !ok {
				return fmt.Errorf("redis: XREAD answered of a stream %q not asked for", sr.stream)
			}
			for _, e := range sr.entries {
				id, fields, err := entry(e)
				if err != nil {
					return fmt.Errorf("redis: %s: %w", sr.stream, err)
				}
				ids[i] = id
				read++
				rec := s.record(sr.stream, id, fields)
				rec.Position = s.position(ids)
				if err := r.Emit(rec); err != nil {
					return err
				}
			}
		}
		if read == 0 && r.Once {
			return nil
		}
	}
}

// streamReply is one stream's part of XREAD's reply.
type streamReply struct {
	stream  string
	entries []wire.Value
}

// streamReplies reads XREAD's reply: a map of the streams to their entries
// in RESP3, an array of pairs in RESP2.
func streamReplies(v wire.Value) ([]streamReply, bool) {
	var out []streamReply
	add := func(k, e wire.Value) bool {
		name, ok := k.Text()
		if !ok || (e.Kind != wire.KindArray && !e.IsNull()) {
			return false
		}
		out = append(out, streamReply{name, e.Elems})
		return true
	}
	switch v.Kind {
	case wire.KindMap:
		for i := 0; i+1 < len(v.Elems); i += 2 {
			if !add(v.Elems[i], v.Elems[i+1]) {
				return nil, false
			}
		}
	case wire.KindArray:
		for _, p := range v.Elems {
			if p.Kind != wire.KindArray || len(p.Elems) != 2 || !add(p.Elems[0], p.Elems[1]) {
				return nil, false
			}
		}
	default:
		return nil, false
	}
	return out, true
}

// entry reads a stream entry: its ID and its fields and values.
func entry(e wire.Value) (string, []wire.Value, error) {
	if e.Kind != wire.KindArray || len(e.Elems) != 2 {
		return "", nil, fmt.Errorf("an entry that is a %s of %d", e.Kind, len(e.Elems))
	}
	id, ok := e.Elems[0].Text()
	if _, _, valid := ParseID(id); !ok || !valid {
		return "", nil, fmt.Errorf("an entry ID %q", id)
	}
	fields, ok := e.Elems[1].Pairs()
	if !ok && !e.Elems[1].IsNull() {
		return "", nil, fmt.Errorf("entry %s: fields that are a %s", id, e.Elems[1].Kind)
	}
	return id, fields, nil
}

// ParseID parses a stream entry's ID, ms-seq.
func ParseID(id string) (ms, seq uint64, ok bool) {
	a, b, found := strings.Cut(id, "-")
	if !found {
		return 0, 0, false
	}
	ms, err1 := strconv.ParseUint(a, 10, 64)
	seq, err2 := strconv.ParseUint(b, 10, 64)
	return ms, seq, err1 == nil && err2 == nil
}

// text makes a field's value a string when it is UTF-8, bytes otherwise.
func text(s string) fitsync.Value {
	if utf8.ValidString(s) {
		return fitsync.String(s)
	}
	return fitsync.Bytes([]byte(s))
}

// record makes an entry's record.
func (s *Source) record(stream, id string, fields []wire.Value) *fitsync.Record {
	ms, _, _ := ParseID(id)
	at := time.UnixMilli(int64(ms)).UTC()
	if s.Form == "record" {
		for i := 0; i+1 < len(fields); i += 2 {
			if k, _ := fields[i].Text(); k != recordField {
				continue
			}
			doc, _ := fields[i+1].Text()
			rec, err := fitsync.ParseRecord([]byte(doc))
			if err != nil {
				break
			}
			rec.Source, rec.Tx, rec.Position = "redis", fitsync.Tx{}, nil
			if s.Namespace != "" || rec.Namespace == "" {
				rec.Namespace = s.namespace(stream)
			}
			if rec.CommitTime.IsZero() {
				rec.CommitTime = at
			}
			return rec
		}
	}
	row := make([]fitsync.Field, 0, len(fields)/2)
	for i := 0; i+1 < len(fields); i += 2 {
		k, _ := fields[i].Text()
		v, _ := fields[i+1].Text()
		row = append(row, fitsync.Field{Name: k, Value: text(v)})
	}
	return &fitsync.Record{Source: "redis", Namespace: s.namespace(stream), Op: fitsync.OpMessage,
		Key: fitsync.Map(fitsync.Field{Name: "id", Value: fitsync.String(id)}), After: fitsync.Map(row...), CommitTime: at}
}

func (s *Source) namespace(stream string) string {
	if s.Namespace != "" {
		return s.Namespace
	}
	return stream
}

// position writes the IDs as the source's position.
func (s *Source) position(ids []string) fitsync.Position {
	p := position{Streams: make(map[string]string, len(ids))}
	for i, st := range s.Streams {
		p.Streams[st] = ids[i]
	}
	b, _ := json.Marshal(p) // the streams' keys are UTF-8, checked by Provision
	return b
}

package syncredis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/redis/wire"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".redis", New: func() any { return new(Sink) },
		Doc: "Writes records to Redis or Valkey, pipelined: with mode stream, each record an entry added to a stream named from its namespace with XADD, its fields the row's (form fields) or the record in canonical JSON (form record), the entry's ID the server's or, with entry_id key, the record's key, so that a replay adds nothing twice; with mode keys, one key per record, named from its namespace and key: SET of the row as JSON or of the record, or HSET of the row's fields in place of the hash, with an optional TTL, and DEL on a delete."})
}

// The defaults of the sink's params.
const (
	DefaultMaxPipeline = 1000
	defaultStream      = "{namespace}"
	defaultKey         = "{namespace}:{key}"
)

// Sink is the sink redis.
type Sink struct {
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
	Timeout     config.Duration `json:"timeout,omitempty" default:"\"10s\"" doc:"How long connecting, and each round trip of a batch's commands, may take. 10s when left out or 0."`
	Mode        string          `json:"mode,omitempty" enum:"stream,keys" default:"\"stream\"" doc:"How records are written: stream, each an entry added to a stream with XADD; keys, each the value of a key of its own, set, replaced or deleted. stream when left out."`
	Stream      string          `json:"stream,omitempty" default:"\"{namespace}\"" doc:"With mode stream, the stream's key: text with {namespace}, the record's namespace, and {key}, its key's text. {namespace} when left out."`
	Form        string          `json:"form,omitempty" enum:"fields,record" default:"\"fields\"" doc:"With mode stream, what an entry holds: fields, the row's fields (after, or before for a delete), each value as text, maps and arrays as JSON, nulls left out; record, one field, record, holding the whole record in its canonical JSON, which the source redis reads back with every type, the operation and the key. fields when left out."`
	OpField     string          `json:"op_field,omitempty" doc:"With mode stream and form fields, a field set to the record's operation (insert, update, delete, message) before the row's fields, so that a delete can be told from an insert. None when left out."`
	EntryID     string          `json:"entry_id,omitempty" enum:"auto,key" default:"\"auto\"" doc:"With mode stream, the entry's ID: auto, the server's (XADD *), so that a batch written again after a crash adds its entries again; key, the record's key (its id field, or the key itself), an ID as ms-seq as the source redis gives, so that a replay's entries, at or below the stream's last, are refused by the server and counted as written: a stream copied exactly once. auto when left out."`
	MaxLen      int64           `json:"max_len,omitempty" min:"0" doc:"With mode stream, trim each stream to about this many entries as they are added (XADD MAXLEN ~). Not trimmed when left out or 0."`
	Key         string          `json:"key,omitempty" default:"\"{namespace}:{key}\"" doc:"With mode keys, the key a record is written to: text with {namespace}, the record's namespace, and {key}, its key's text, a key of several fields their values joined by ':'. A record without a key fails. {namespace}:{key} when left out."`
	Value       string          `json:"value,omitempty" enum:"json,record,hash" default:"\"json\"" doc:"With mode keys, what a key holds: json, the row as a plain JSON object (SET); record, the whole record in its canonical JSON (SET); hash, the row's fields as a hash (HSET), each value as text, maps and arrays as JSON, nulls left out, the key deleted and set in one MULTI so that no field of an earlier row stays. A delete deletes the key (DEL). json when left out."`
	TTL         config.Duration `json:"ttl,omitempty" doc:"With mode keys, how long a key written lives (SET PX, PEXPIRE). For ever when left out or 0."`
	MaxPipeline int             `json:"max_pipeline,omitempty" min:"0" max:"100000" default:"1000" doc:"The most commands sent in one round trip; a batch with more is sent in several, in order. 1000 when left out or 0."`

	conn          conn
	stream, key   []part
	maxPipeline   int
	ttlMillis     string
	keysMode, ids bool
}

// part is a piece of a name's template.
type part struct {
	text string
	// what is "namespace" or "key" for a placeholder, "" for text.
	what string
}

// parseTemplate splits a template into its text and placeholders.
func parseTemplate(t string) ([]part, error) {
	var out []part
	rest := t
	for rest != "" {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			out = append(out, part{text: rest})
			break
		}
		if i > 0 {
			out = append(out, part{text: rest[:i]})
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return nil, fmt.Errorf("%q has a { without a }", t)
		}
		switch name := rest[i+1 : i+j]; name {
		case "namespace", "key":
			out = append(out, part{what: name})
		default:
			return nil, fmt.Errorf("%q has {%s}: only {namespace} and {key}", t, name)
		}
		rest = rest[i+j+1:]
	}
	if strings.ContainsRune(strings.ReplaceAll(strings.ReplaceAll(t, "{namespace}", ""), "{key}", ""), '}') {
		return nil, fmt.Errorf("%q has a } without a {", t)
	}
	return out, nil
}

// Provision checks the params and reads the password and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	o, errs := options(ctx, connParams{Address: s.Address, Username: s.Username, PasswordEnv: s.PasswordEnv, DB: s.DB, Protocol: s.Protocol,
		TLS: s.TLS, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn.opts = o
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	s.keysMode = s.Mode == "keys"
	s.ids = s.EntryID == "key"
	if s.keysMode {
		for _, f := range []struct{ name, value string }{{"stream", s.Stream}, {"form", s.Form}, {"op_field", s.OpField}, {"entry_id", s.EntryID}} {
			if f.value != "" {
				add(f.name, "%s is mode stream's", f.name)
			}
		}
		if s.MaxLen != 0 {
			add("max_len", "max_len is mode stream's")
		}
		t := s.Key
		if t == "" {
			t = defaultKey
		}
		var err error
		if s.key, err = parseTemplate(t); err != nil {
			add("key", "%v", err)
		}
		if s.TTL > 0 {
			ms := s.TTL.Std().Milliseconds()
			if ms < 1 {
				add("ttl", "%s is less than a millisecond", s.TTL.Std())
			}
			s.ttlMillis = strconv.FormatInt(ms, 10)
		}
	} else {
		if s.Key != "" || s.Value != "" {
			add("key", "key and value are mode keys'")
		}
		if s.TTL != 0 {
			add("ttl", "ttl is mode keys'")
		}
		if s.OpField != "" && s.Form == "record" {
			add("op_field", "form record holds the operation already")
		}
		t := s.Stream
		if t == "" {
			t = defaultStream
		}
		var err error
		if s.stream, err = parseTemplate(t); err != nil {
			add("stream", "%v", err)
		}
	}
	s.maxPipeline = s.MaxPipeline
	if s.maxPipeline <= 0 {
		s.maxPipeline = DefaultMaxPipeline
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// Cleanup closes the connection.
func (s *Sink) Cleanup() { s.conn.close() }

// keyText is a record's key as text: a map's values' joined by ':'.
func keyText(k fitsync.Value) (string, error) {
	switch k.Kind() {
	case fitsync.KindNull:
		return "", errors.New("the record has no key")
	case fitsync.KindMap:
		fs := k.Fields()
		if len(fs) == 0 {
			return "", errors.New("the record's key has no fields")
		}
		parts := make([]string, len(fs))
		for i, f := range fs {
			if f.Value.IsNull() {
				return "", fmt.Errorf("the key's field %s is null", f.Name)
			}
			parts[i] = fitsync.TextOf(f.Value)
		}
		return strings.Join(parts, ":"), nil
	}
	return fitsync.TextOf(k), nil
}

// name fills in a template for r.
func name(t []part, r *fitsync.Record) (string, error) {
	var b strings.Builder
	for _, p := range t {
		switch p.what {
		case "":
			b.WriteString(p.text)
		case "namespace":
			b.WriteString(r.Namespace)
		case "key":
			k, err := keyText(r.Key)
			if err != nil {
				return "", err
			}
			b.WriteString(k)
		}
	}
	if b.Len() == 0 {
		return "", errors.New("the name is empty")
	}
	return b.String(), nil
}

// row is the row a record writes: after, or before when after is null (a
// delete), or the key when both are.
func row(r *fitsync.Record) fitsync.Value {
	if !r.After.IsNull() {
		return r.After
	}
	if !r.Before.IsNull() {
		return r.Before
	}
	return r.Key
}

// appendFields appends a map's fields and their values' text, nulls left
// out.
func appendFields(args []string, m fitsync.Value) ([]string, error) {
	if m.Kind() != fitsync.KindMap {
		return nil, fmt.Errorf("the row is a %s, not a map", m.Kind())
	}
	for _, f := range m.Fields() {
		if f.Value.IsNull() {
			continue
		}
		v, err := fieldText(f.Value)
		if err != nil {
			return nil, fmt.Errorf("the field %s: %w", f.Name, err)
		}
		args = append(args, f.Name, v)
	}
	return args, nil
}

// fieldText is a field's value as a stream entry's or a hash's field holds
// it: its text, a map or an array as plain JSON.
func fieldText(v fitsync.Value) (string, error) {
	if k := v.Kind(); k == fitsync.KindMap || k == fitsync.KindArray {
		b, err := fitsync.AppendPlain(nil, v)
		return string(b), err
	}
	return fitsync.TextOf(v), nil
}

// entryID is the entry ID a record's key gives.
func entryID(k fitsync.Value) (string, error) {
	if k.Kind() == fitsync.KindMap {
		var ok bool
		if k, ok = k.Get("id"); !ok {
			return "", errors.New("the record's key has no id field")
		}
	}
	id := fitsync.TextOf(k)
	if _, _, ok := ParseID(id); !ok {
		return "", fmt.Errorf("the key %q is not an entry ID, ms-seq", id)
	}
	return id, nil
}

// item is a record as the sink writes it: its commands, or why it cannot be
// written.
type item struct {
	cmds [][]string
	// multi says cmds are a MULTI ... EXEC.
	multi bool
	err   error
}

// build makes r's item.
func (s *Sink) build(r *fitsync.Record) item {
	if r.Op == fitsync.OpDDL {
		return item{}
	}
	if s.keysMode {
		return s.buildKey(r)
	}
	stream, err := name(s.stream, r)
	if err != nil {
		return item{err: err}
	}
	args := []string{"XADD", stream}
	if s.MaxLen > 0 {
		args = append(args, "MAXLEN", "~", strconv.FormatInt(s.MaxLen, 10))
	}
	if s.ids {
		id, err := entryID(r.Key)
		if err != nil {
			return item{err: err}
		}
		args = append(args, id)
	} else {
		args = append(args, "*")
	}
	n := len(args)
	if s.Form == "record" {
		doc, err := r.AppendJSON(nil)
		if err != nil {
			return item{err: err}
		}
		args = append(args, recordField, string(doc))
	} else {
		if s.OpField != "" {
			args = append(args, s.OpField, r.Op.String())
		}
		if args, err = appendFields(args, row(r)); err != nil {
			return item{err: err}
		}
		if len(args) == n {
			return item{err: errors.New("the row has no field that is not null")}
		}
	}
	return item{cmds: [][]string{args}}
}

func (s *Sink) buildKey(r *fitsync.Record) item {
	key, err := name(s.key, r)
	if err != nil {
		return item{err: err}
	}
	if r.Op == fitsync.OpDelete {
		return item{cmds: [][]string{{"DEL", key}}}
	}
	switch s.Value {
	case "hash":
		hset, err := appendFields([]string{"HSET", key}, row(r))
		if err != nil {
			return item{err: err}
		}
		cmds := [][]string{{"MULTI"}, {"DEL", key}}
		if len(hset) > 2 {
			cmds = append(cmds, hset)
			if s.ttlMillis != "" {
				cmds = append(cmds, []string{"PEXPIRE", key, s.ttlMillis})
			}
		}
		return item{cmds: append(cmds, []string{"EXEC"}), multi: true}
	case "record":
		doc, err := r.AppendJSON(nil)
		if err != nil {
			return item{err: err}
		}
		return item{cmds: [][]string{s.set(key, doc)}}
	}
	doc, err := fitsync.AppendPlain(nil, row(r))
	if err != nil {
		return item{err: err}
	}
	return item{cmds: [][]string{s.set(key, doc)}}
}

func (s *Sink) set(key string, doc []byte) []string {
	if s.ttlMillis != "" {
		return []string{"SET", key, string(doc), "PX", s.ttlMillis}
	}
	return []string{"SET", key, string(doc)}
}

// transient says whether an error the server answered with may pass: the
// server loading, out of memory, read-only in a failover, busy, or refusing
// the user; any other is the record's, as WRONGTYPE.
func transient(e *wire.Error) bool {
	switch e.Code() {
	case "LOADING", "OOM", "READONLY", "BUSY", "TRYAGAIN", "MASTERDOWN", "NOREPLICAS", "CLUSTERDOWN", "MOVED", "ASK",
		"NOAUTH", "NOPERM", "WRONGPASS", "MISCONF":
		return true
	}
	return false
}

// replyError returns the error of a record's reply, nil when it was written;
// an entry refused as at or below its stream's last is written, when entry
// IDs come from the keys.
func (s *Sink) replyError(v wire.Value) error {
	err := v.Err()
	if err == nil {
		return nil
	}
	if s.ids && strings.Contains(err.Error(), "equal or smaller than the target stream top item") {
		return nil
	}
	return err
}

// itemError returns the error of a record's replies.
func (s *Sink) itemError(it item, replies []wire.Value) error {
	if !it.multi {
		for _, v := range replies {
			if err := s.replyError(v); err != nil {
				return err
			}
		}
		return nil
	}
	exec := replies[len(replies)-1]
	for _, v := range replies[:len(replies)-1] {
		if err := v.Err(); err != nil {
			return err
		}
	}
	if err := exec.Err(); err != nil {
		return err
	}
	for _, v := range exec.Elems {
		if err := v.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Write sends the batch's commands, pipelined, at most max_pipeline in one
// round trip. The first record the server refuses ends the write: a
// *BatchError names it, for good unless the server's error may pass.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	items := make([]item, len(batch))
	for i, r := range batch {
		items[i] = s.build(r)
	}
	start := 0
	for start < len(batch) {
		end, n := start, 0
		var cmds [][]string
		for end < len(batch) && (n == 0 || n+len(items[end].cmds) <= s.maxPipeline) && items[end].err == nil {
			cmds = append(cmds, items[end].cmds...)
			n += len(items[end].cmds)
			end++
		}
		if len(cmds) > 0 {
			if err := s.round(ctx, items[start:end], cmds); err != nil {
				var be *fitsync.BatchError
				if errors.As(err, &be) {
					be.Index += start
					return be
				}
				if start > 0 {
					return &fitsync.BatchError{Index: start, Err: err}
				}
				return err
			}
		}
		if end < len(batch) && items[end].err != nil {
			return &fitsync.BatchError{Index: end, Err: fitsync.Permanent(items[end].err)}
		}
		start = end
	}
	return nil
}

// round sends one round trip's commands and checks each record's replies.
func (s *Sink) round(ctx context.Context, items []item, cmds [][]string) error {
	ctx, cancel := context.WithTimeout(ctx, timeout(s.Timeout))
	defer cancel()
	c, err := s.conn.get(ctx)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	replies, err := c.Pipeline(ctx, cmds)
	if err != nil {
		return fmt.Errorf("redis: %w", err)
	}
	at := 0
	for i, it := range items {
		err := s.itemError(it, replies[at:at+len(it.cmds)])
		at += len(it.cmds)
		if err == nil {
			continue
		}
		var we *wire.Error
		if !errors.As(err, &we) || !transient(we) {
			err = fitsync.Permanent(err)
		}
		if i == 0 {
			return err
		}
		return &fitsync.BatchError{Index: i, Err: err}
	}
	return nil
}

var (
	_ fitsync.Sink   = (*Sink)(nil)
	_ fitsync.Source = (*Source)(nil)
	_ module.Cleaner = (*Sink)(nil)
	_ module.Cleaner = (*Source)(nil)
)

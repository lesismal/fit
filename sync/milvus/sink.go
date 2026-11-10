// Package syncmilvus is sync's Milvus: the sink milvus, which upserts
// records as entities and deletes them by primary key over Milvus 2.4's
// RESTful API v2, through fib's asynchronous HTTP client. See docs/sync.md.
package syncmilvus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/tmpl"
	"github.com/lesismal/fit/sync/internal/vecutil"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".milvus", New: func() any { return new(Sink) },
		Doc: "Writes records to Milvus (2.4 and later) collections over its RESTful API v2, through fib's asynchronous HTTP client: inserts and updates upsert the row as an entity, its primary key from the record's key, deletes delete by it. A batch's records of one key are written as its last; a 429, a 5xx or a Milvus error of a busy server is tried again with a backoff, and an entity Milvus refuses (a vector of the wrong size, a field the schema lacks) fails its record, which on_error takes."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 8 << 20
	DefaultPrimaryKey      = "id"
)

// Sink is the sink milvus.
type Sink struct {
	URL             string          `json:"url" fit:"required" doc:"Milvus's RESTful endpoint, as http://milvus:19530 (Zilliz Cloud's cluster endpoint too). fib's client does not follow redirects."`
	TokenEnv        string          `json:"token_env,omitempty" doc:"The environment variable holding a token, sent as Authorization: Bearer, read when the pipeline loads: an API key, or user:password. Not with username."`
	Username        string          `json:"username,omitempty" doc:"The user, with password_env, sent as the token user:password."`
	PasswordEnv     string          `json:"password_env,omitempty" doc:"The environment variable holding username's password, read when the pipeline loads."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots. The check cannot be turned off."`
	CertFile        string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile         string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; the URL's host when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long one request may take, from connecting to the end of its answer. 30s when left out or 0."`
	Database        string          `json:"database,omitempty" doc:"The database of the collections; default when left out."`
	Collection      string          `json:"collection" fit:"required" doc:"The collection a record's entity goes to, which must exist with a primary key that is not auto_id: text with placeholders, {namespace} (or {$namespace}, as record templates write it) the record's namespace and {name} the row's field name; {{ is a brace."`
	PrimaryKey      string          `json:"primary_key,omitempty" default:"\"id\"" doc:"The collection's primary key field, set from the record's key: an Int64 key from a single int or uint, a VarChar from any other key's text (a single value's, or a map's values joined by ':'). id when left out."`
	Fields          []string        `json:"fields,omitempty" doc:"The row's fields written, the vector fields among them, beside the primary key; every field of the row when left out, which the collection takes when its schema has them or its dynamic field is on. Vectors are arrays of numbers, as the embed transform makes."`
	MaxRequestBytes int64           `json:"max_request_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes of one request; a batch larger is sent in several, and an entity larger fails its record. 8 MiB when left out or 0."`

	client     *fibrest.Client
	collection []tmpl.Part
	db         json.RawMessage

	mu      sync.Mutex
	pkTypes map[string]string // a collection's primary key's type, from describe
}

var fieldName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.PrimaryKey == "" {
		s.PrimaryKey = DefaultPrimaryKey
	}
	if !fieldName.MatchString(s.PrimaryKey) {
		add("primary_key", "%q is not a field's name", s.PrimaryKey)
	}
	var err error
	if s.collection, err = tmpl.Parse(s.Collection); err != nil {
		add("collection", "%v", err)
	}
	if s.Database != "" {
		s.db, _ = json.Marshal(s.Database)
	}
	header := stdhttp.Header{}
	switch {
	case s.TokenEnv != "" && (s.Username != "" || s.PasswordEnv != ""):
		add("token_env", "a token or a username, not both")
	case s.TokenEnv != "":
		tok, fe := fibrest.Secret("token_env", s.TokenEnv)
		if fe != nil {
			add(fe.Field, "%v", fe.Err)
		}
		header.Set("Authorization", "Bearer "+tok)
	case s.Username != "" && s.PasswordEnv != "":
		pw, fe := fibrest.Secret("password_env", s.PasswordEnv)
		if fe != nil {
			add(fe.Field, "%v", fe.Err)
		}
		header.Set("Authorization", "Bearer "+s.Username+":"+pw)
	case s.Username != "" || s.PasswordEnv != "":
		add("username", "username and password_env go together")
	}
	if strings.ContainsAny(s.Username, ":\r\n") {
		add("username", "a username has no ':' or line breaks")
	}
	var ferrs []fibrest.FieldError
	s.client, ferrs = fibrest.New(ctx.Engine(), fibrest.Params{URL: s.URL, CAFile: s.CAFile, CertFile: s.CertFile, KeyFile: s.KeyFile,
		ServerName: s.ServerName, Timeout: s.Timeout.Std(), MaxResponseBytes: 1 << 20, Header: header})
	for _, fe := range ferrs {
		add(fe.Field, "%v", fe.Err)
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: a key goes to one writer.
func (s *Sink) ParallelWrites() {}

// Cleanup closes the client.
func (s *Sink) Cleanup() {
	if s.client != nil {
		s.client.Close()
	}
}

func (s *Sink) maxRequest() int {
	if s.MaxRequestBytes <= 0 {
		return DefaultMaxRequestBytes
	}
	return int(s.MaxRequestBytes)
}

// op is a record as a write: an entity's JSON, or a primary key's literal
// in a filter, to delete.
type op struct {
	collection string
	pk         string // the key's literal in a filter expression
	entity     []byte // nil for a delete
	skip       bool
}

// Write writes the batch: each key's last record, upserts and deletes, in
// requests of at most max_request_bytes per collection. A record that
// cannot be an entity, or that Milvus refuses, fails for good at its
// index, those before it written.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	ops := make([]op, len(batch))
	bad, badErr := len(batch), error(nil)
	for i, r := range batch {
		if ops[i], badErr = s.op(r); badErr != nil {
			bad = i
			break
		}
	}
	err := fibrest.WriteFinding(ctx, bad, func(ctx context.Context, lo, hi int) error {
		return s.send(ctx, ops[lo:hi])
	})
	if err != nil {
		return err
	}
	if badErr != nil {
		return &fitsync.BatchError{Index: bad, Err: fitsync.Permanent(badErr)}
	}
	return nil
}

// PrimaryKey returns a key's primary key value, as JSON: a single int or
// uint as a number, any other key its text (vecutil.KeyText) as a string.
func PrimaryKey(key fitsync.Value) (string, error) {
	if v, ok := vecutil.Single(key); ok {
		switch v.Kind() {
		case fitsync.KindInt:
			return strconv.FormatInt(v.AsInt(), 10), nil
		case fitsync.KindUint:
			return strconv.FormatUint(v.AsUint(), 10), nil
		}
	}
	t, ok := vecutil.KeyText(key)
	if !ok {
		return "", errors.New("the record has no key to make the primary key of")
	}
	b, _ := json.Marshal(t)
	return string(b), nil
}

// op makes r's write.
func (s *Sink) op(r *fitsync.Record) (op, error) {
	switch r.Op {
	case fitsync.OpDDL:
		return op{skip: true}, nil
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpDelete, fitsync.OpMessage:
	default:
		return op{}, fmt.Errorf("a record of operation %s", r.Op)
	}
	pk, err := PrimaryKey(r.Key)
	if err != nil {
		return op{}, err
	}
	row := vecutil.RowOf(r)
	o := op{pk: pk}
	if o.collection, err = tmpl.Expand(s.collection, r, row); err != nil {
		return op{}, fmt.Errorf("collection: %w", err)
	}
	if o.collection == "" {
		return op{}, errors.New("the collection's name is empty")
	}
	if r.Op == fitsync.OpDelete {
		return o, nil
	}
	if row.Kind() != fitsync.KindMap {
		return op{}, fmt.Errorf("a %s's row is a %s, not a map", r.Op, row.Kind())
	}
	name, _ := json.Marshal(s.PrimaryKey)
	b := append([]byte{'{'}, name...)
	b = append(append(b, ':'), pk...)
	for _, f := range row.Fields() {
		if f.Name == s.PrimaryKey || len(s.Fields) > 0 && !contains(s.Fields, f.Name) {
			continue
		}
		name, _ := json.Marshal(f.Name)
		b = append(append(append(b, ','), name...), ':')
		if b, err = fitsync.AppendPlain(b, f.Value); err != nil {
			return op{}, fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	o.entity = append(b, '}')
	if len(o.entity)+256 > s.maxRequest() {
		return op{}, fmt.Errorf("the entity is %d bytes, more than max_request_bytes", len(o.entity))
	}
	return o, nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// send writes ops, each key's last, in requests by collection: deletes,
// then upserts, of other keys.
func (s *Sink) send(ctx context.Context, ops []op) error {
	keys := make([]string, len(ops))
	for i, o := range ops {
		if o.skip {
			keys[i] = "\x01" + strconv.Itoa(i)
		} else {
			keys[i] = o.collection + "\x00" + o.pk
		}
	}
	type pending struct{ upserts, deletes [][]byte }
	by := map[string]*pending{}
	var order []string
	for _, i := range vecutil.Last(keys) {
		o := ops[i]
		if o.skip {
			continue
		}
		p := by[o.collection]
		if p == nil {
			p = &pending{}
			by[o.collection] = p
			order = append(order, o.collection)
		}
		if o.entity == nil {
			p.deletes = append(p.deletes, []byte(o.pk))
		} else {
			p.upserts = append(p.upserts, o.entity)
		}
	}
	for _, c := range order {
		p := by[c]
		head, _ := json.Marshal(c)
		head = append([]byte(`{"collectionName":`), head...)
		if s.db != nil {
			head = append(append(head, `,"dbName":`...), s.db...)
		}
		if len(p.deletes) > 0 {
			typ, err := s.pkType(ctx, c, head)
			if err != nil {
				return err
			}
			for i, d := range p.deletes {
				p.deletes[i] = literalFor(typ, d)
			}
		}
		del := append(append([]byte(nil), head...), `,"filter":`...)
		if err := s.chunks(ctx, "/v2/vectordb/entities/delete", del, s.PrimaryKey+" in [", "]", p.deletes, true); err != nil {
			return err
		}
		ups := append(append([]byte(nil), head...), `,"data":[`...)
		if err := s.chunks(ctx, "/v2/vectordb/entities/upsert", ups, "", "]}", p.upserts, false); err != nil {
			return err
		}
	}
	return nil
}

// pkType returns the type of collection c's primary key, VarChar or
// Int64, describing c at its first delete: a delete's filter must hold the
// key as the field's type, where an upsert's entity is converted (an int
// key into a VarChar, a string of digits into an Int64). "" when Milvus
// does not say (a token that may not describe), the key then as it is.
func (s *Sink) pkType(ctx context.Context, c string, head []byte) (string, error) {
	s.mu.Lock()
	typ, ok := s.pkTypes[c]
	s.mu.Unlock()
	if ok {
		return typ, nil
	}
	const path = "/v2/vectordb/collections/describe"
	resp, err := s.post(ctx, path, append(append([]byte(nil), head...), '}'))
	if err != nil {
		return "", err
	}
	var a struct {
		milvusAnswer
		Data struct {
			Fields []struct {
				Name       string `json:"name"`
				Type       string `json:"type"`
				PrimaryKey bool   `json:"primaryKey"`
			} `json:"fields"`
		} `json:"data"`
	}
	switch {
	case resp.Status != stdhttp.StatusOK || json.Unmarshal(resp.Body, &a) != nil || busy(a.Code):
		// Not answered, after the tries: the pipeline tries again.
		return "", fibrest.StatusError(path, resp)
	case a.Code == 100:
		// No collection (yet): the delete's own answer tells.
		return "", nil
	}
	for _, f := range a.Data.Fields {
		if f.PrimaryKey {
			typ = f.Type
		}
	}
	s.mu.Lock()
	if s.pkTypes == nil {
		s.pkTypes = map[string]string{}
	}
	s.pkTypes[c] = typ
	s.mu.Unlock()
	return typ, nil
}

// literalFor returns a primary key's JSON as a filter's literal of the
// field's type: a number quoted for a VarChar, a string of an int64's
// digits unquoted for an Int64; as it is otherwise.
func literalFor(typ string, pk []byte) []byte {
	switch {
	case typ == "VarChar" && len(pk) > 0 && pk[0] != '"':
		b, _ := json.Marshal(string(pk))
		return b
	case typ == "Int64" && len(pk) > 0 && pk[0] == '"':
		var t string
		if json.Unmarshal(pk, &t) == nil {
			if n, err := strconv.ParseInt(t, 10, 64); err == nil {
				return strconv.AppendInt(nil, n, 10)
			}
		}
	}
	return pk
}

// chunks sends items in requests of at most max_request_bytes: head, then
// for a delete a JSON string of open, the items and close, or for an upsert
// the items and close.
func (s *Sink) chunks(ctx context.Context, path string, head []byte, open, close string, items [][]byte, del bool) error {
	var list []byte
	n := 0
	flush := func() error {
		if n == 0 {
			return nil
		}
		body := append([]byte(nil), head...)
		if del {
			f, _ := json.Marshal(open + string(list) + close)
			body = append(append(body, f...), '}')
		} else {
			body = append(append(body, list...), close...)
		}
		list, n = list[:0], 0
		return s.request(ctx, path, body)
	}
	for _, it := range items {
		if n > 0 && len(head)+len(list)+len(it)+len(open)+len(close)+16 > s.maxRequest() {
			if err := flush(); err != nil {
				return err
			}
		}
		if n > 0 {
			list = append(list, ',')
		}
		list = append(list, it...)
		n++
	}
	return flush()
}

// milvusAnswer is the RESTful API's answer: code 0, or an error.
type milvusAnswer struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// request sends one upsert or delete.
func (s *Sink) request(ctx context.Context, path string, body []byte) error {
	resp, err := s.post(ctx, path, body)
	if err != nil {
		return err
	}
	return answer(path, resp)
}

// post sends a request of JSON, again while the answer is a 408, 429 or
// 5xx, or a Milvus error of a busy server.
func (s *Sink) post(ctx context.Context, path string, body []byte) (*fibrest.Response, error) {
	r := fibrest.Request{Method: stdhttp.MethodPost, Path: path, Body: body, Header: stdhttp.Header{"Content-Type": {"application/json"}}}
	r.Retry = func(resp *fibrest.Response) bool {
		if fibrest.Transient(resp.Status) {
			return true
		}
		var a milvusAnswer
		return json.Unmarshal(resp.Body, &a) == nil && busy(a.Code)
	}
	return s.client.DoRetry(ctx, r, 0)
}

// answer turns an answer into the write's error: nil for code 0, a
// refusal for an error of what was sent.
func answer(what string, resp *fibrest.Response) error {
	var a milvusAnswer
	if resp.Status != stdhttp.StatusOK || json.Unmarshal(resp.Body, &a) != nil {
		if resp.Status == stdhttp.StatusBadRequest || resp.Status == stdhttp.StatusRequestEntityTooLarge {
			return fibrest.Refused(fibrest.StatusError(what, resp))
		}
		return fibrest.StatusError(what, resp)
	}
	switch {
	case a.Code == 0:
		return nil
	case refused(a.Code):
		return fibrest.Refused(fmt.Errorf("%s: Milvus refused it (%d): %s", what, a.Code, a.Message))
	}
	return fmt.Errorf("%s: Milvus answered %d: %s", what, a.Code, a.Message)
}

// busy reports whether a Milvus error code says the server is busy or not
// ready, which may pass: 1 to 9 (not ready, unavailable, memory, rate,
// quota and disk limits).
func busy(code int) bool { return code >= 1 && code <= 9 }

// refused reports whether a Milvus error code is about what was sent: a
// parameter (1100) or the data (1800 to 1899).
func refused(code int) bool { return code == 1100 || code >= 1800 && code <= 1899 }

// Test describes the collection, or lists the collections when collection
// is a template, with the sink's credentials. It writes nothing.
func (s *Sink) Test(ctx context.Context) error {
	path, body := "/v2/vectordb/collections/list", []byte("{")
	if c, ok := tmpl.Static(s.collection); ok {
		path = "/v2/vectordb/collections/describe"
		name, _ := json.Marshal(c)
		body = append(append(body, `"collectionName":`...), name...)
	}
	if s.db != nil {
		if len(body) > 1 {
			body = append(body, ',')
		}
		body = append(append(body, `"dbName":`...), s.db...)
	}
	resp, err := s.client.Do(ctx, fibrest.Request{Method: stdhttp.MethodPost, Path: path, Body: append(body, '}'),
		Header: stdhttp.Header{"Content-Type": {"application/json"}}})
	if err != nil {
		return err
	}
	return answer(path, resp)
}

var (
	_ fitsync.ParallelSink = (*Sink)(nil)
	_ fitsync.Tester       = (*Sink)(nil)
	_ module.Cleaner       = (*Sink)(nil)
)

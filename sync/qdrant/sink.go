// Package syncqdrant is sync's Qdrant: the sink qdrant, which upserts
// records as points, by an ID made from the record's key, and deletes
// them, over Qdrant's REST API through fib's asynchronous HTTP client. See
// docs/sync.md.
package syncqdrant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	stdhttp "net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
	"github.com/lesismal/fit/sync/internal/tmpl"
	"github.com/lesismal/fit/sync/internal/vecutil"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".qdrant", New: func() any { return new(Sink) },
		Doc: "Writes records to Qdrant collections over its REST API, through fib's asynchronous HTTP client: inserts and updates upsert a point of the row's vector field and the rest of the row as payload, deletes delete it, by an ID from the record's key (an unsigned integer or a UUID as they are, any other key a UUID made from its text, the same each time), waiting for each write to be applied. A batch's records of one ID are written as its last; a 429 or 5xx is tried again with a backoff, and a point Qdrant refuses (a vector of the wrong size) fails its record, which on_error takes."})
}

// The defaults of the sink's params.
const (
	DefaultMaxRequestBytes = 8 << 20
	DefaultVectorField     = "embedding"
)

// Sink is the sink qdrant.
type Sink struct {
	URL             string          `json:"url" fit:"required" doc:"Qdrant's REST endpoint, as http://qdrant:6333. fib's client does not follow redirects."`
	APIKeyEnv       string          `json:"api_key_env,omitempty" doc:"The environment variable holding an API key, sent as the api-key header, read when the pipeline loads."`
	CAFile          string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots. The check cannot be turned off."`
	CertFile        string          `json:"cert_file,omitempty" doc:"A PEM file of a client certificate, and the chain after it, presented to a server that asks for one; needs key_file."`
	KeyFile         string          `json:"key_file,omitempty" doc:"The PEM file of cert_file's private key."`
	ServerName      string          `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; the URL's host when left out."`
	Timeout         config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long one request may take, from connecting to the end of its answer. 30s when left out or 0."`
	Collection      string          `json:"collection" fit:"required" doc:"The collection a record's point goes to, which must exist: text with placeholders, {namespace} (or {$namespace}, as record templates write it) the record's namespace and {name} the row's field name; {{ is a brace."`
	VectorField     string          `json:"vector_field,omitempty" default:"\"embedding\"" doc:"The row's field holding the point's vector: an array of numbers, or JSON of one, as the embed transform makes. A record without it fails. embedding when left out."`
	VectorName      string          `json:"vector_name,omitempty" doc:"The collection's named vector the vector is, for a collection of several; its one unnamed vector when left out."`
	Payload         []string        `json:"payload,omitempty" doc:"The row's fields kept in the point's payload; every field but vector_field when left out."`
	MaxRequestBytes int64           `json:"max_request_bytes,omitempty" min:"0" default:"8388608" doc:"The most bytes of one request; a batch larger is sent in several, and a point larger fails its record. Below Qdrant's service.max_request_size_mb (32 MiB). 8 MiB when left out or 0."`

	client     *fibrest.Client
	collection []tmpl.Part
}

// Provision checks the params and reads the secrets and TLS files; nothing
// is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if s.VectorField == "" {
		s.VectorField = DefaultVectorField
	}
	var err error
	if s.collection, err = tmpl.Parse(s.Collection); err != nil {
		add("collection", "%v", err)
	}
	header := stdhttp.Header{}
	if s.APIKeyEnv != "" {
		key, fe := fibrest.Secret("api_key_env", s.APIKeyEnv)
		if fe != nil {
			add(fe.Field, "%v", fe.Err)
		}
		header.Set("api-key", key)
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

// op is a record as a write: a point's JSON, or an ID's to delete.
type op struct {
	collection string
	id         string // the ID's JSON
	point      []byte // nil for a delete
	skip       bool
}

// Write writes the batch: each ID's last record, upserts and deletes, in
// requests of at most max_request_bytes per collection. A record that
// cannot be a point, or that Qdrant refuses, fails for good at its index,
// those before it written.
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

// op makes r's write.
func (s *Sink) op(r *fitsync.Record) (op, error) {
	switch r.Op {
	case fitsync.OpDDL:
		return op{skip: true}, nil
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpDelete, fitsync.OpMessage:
	default:
		return op{}, fmt.Errorf("a record of operation %s", r.Op)
	}
	id, err := PointID(r.Key)
	if err != nil {
		return op{}, err
	}
	row := vecutil.RowOf(r)
	o := op{id: id}
	if o.collection, err = tmpl.Expand(s.collection, r, row); err != nil {
		return op{}, fmt.Errorf("collection: %w", err)
	}
	if o.collection == "" || strings.ContainsAny(o.collection, "/\\?#") {
		return op{}, fmt.Errorf("%q is not a collection's name", o.collection)
	}
	if r.Op == fitsync.OpDelete {
		return o, nil
	}
	if row.Kind() != fitsync.KindMap {
		return op{}, fmt.Errorf("a %s's row is a %s, not a map", r.Op, row.Kind())
	}
	v, ok := row.Get(s.VectorField)
	if !ok || v.IsNull() {
		return op{}, fmt.Errorf("the row has no vector in %s", s.VectorField)
	}
	vec, err := Vector(v)
	if err != nil {
		return op{}, fmt.Errorf("%s: %w", s.VectorField, err)
	}
	b := append([]byte(`{"id":`), id...)
	b = append(b, `,"vector":`...)
	if s.VectorName != "" {
		b = append(b, '{')
		name, _ := json.Marshal(s.VectorName)
		b = append(append(b, name...), ':')
	}
	b = append(b, vec...)
	if s.VectorName != "" {
		b = append(b, '}')
	}
	b = append(b, `,"payload":{`...)
	n := 0
	for _, f := range row.Fields() {
		if f.Name == s.VectorField || len(s.Payload) > 0 && !contains(s.Payload, f.Name) {
			continue
		}
		if n > 0 {
			b = append(b, ',')
		}
		n++
		name, _ := json.Marshal(f.Name)
		b = append(append(b, name...), ':')
		if b, err = fitsync.AppendPlain(b, f.Value); err != nil {
			return op{}, fmt.Errorf("%s: %w", f.Name, err)
		}
	}
	o.point = append(b, "}}"...)
	if len(o.point)+64 > s.maxRequest() {
		return op{}, fmt.Errorf("the point is %d bytes, more than max_request_bytes", len(o.point))
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

// PointID returns a key's point ID, as JSON: a single int or uint not
// below 0 as it is; a single string that is a UUID as it is, lowercase;
// any other key a UUID made from the first 16 bytes of the SHA-256 of its
// text (vecutil.KeyText), its version 8 and its variant RFC 9562's, the
// same each time.
func PointID(key fitsync.Value) (string, error) {
	if v, ok := vecutil.Single(key); ok {
		switch v.Kind() {
		case fitsync.KindInt:
			if v.AsInt() >= 0 {
				return strconv.FormatInt(v.AsInt(), 10), nil
			}
		case fitsync.KindUint:
			return strconv.FormatUint(v.AsUint(), 10), nil
		case fitsync.KindString:
			if u, ok := uuid(v.Text()); ok {
				return `"` + u + `"`, nil
			}
		}
	}
	t, ok := vecutil.KeyText(key)
	if !ok {
		return "", errors.New("the record has no key to make the point's ID of")
	}
	h := sha256.Sum256([]byte(t))
	h[6] = h[6]&0x0f | 0x80
	h[8] = h[8]&0x3f | 0x80
	x := hex.EncodeToString(h[:16])
	return `"` + x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:] + `"`, nil
}

// uuid returns s, lowercase, when it is a UUID in its 36 characters' form.
func uuid(s string) (string, bool) {
	if len(s) != 36 {
		return "", false
	}
	for i := range len(s) {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return "", false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return "", false
			}
		}
	}
	return strings.ToLower(s), true
}

// Vector returns v as a JSON array of numbers: an array of ints, uints,
// floats and decimals, or JSON of an array of numbers.
func Vector(v fitsync.Value) ([]byte, error) {
	switch v.Kind() {
	case fitsync.KindArray:
		b := []byte{'['}
		for i, e := range v.Elems() {
			if i > 0 {
				b = append(b, ',')
			}
			switch e.Kind() {
			case fitsync.KindInt, fitsync.KindUint, fitsync.KindFloat, fitsync.KindDecimal:
				if e.Kind() == fitsync.KindFloat && (math.IsNaN(e.AsFloat()) || math.IsInf(e.AsFloat(), 0)) {
					return nil, fmt.Errorf("element %d is %v", i, e.AsFloat())
				}
				b, _ = fitsync.AppendPlain(b, e)
			default:
				return nil, fmt.Errorf("element %d is a %s, not a number", i, e.Kind())
			}
		}
		if len(v.Elems()) == 0 {
			return nil, errors.New("an empty vector")
		}
		return append(b, ']'), nil
	case fitsync.KindJSON:
		var fs []float64
		if err := json.Unmarshal([]byte(v.Text()), &fs); err != nil || len(fs) == 0 {
			return nil, errors.New("JSON that is not an array of numbers")
		}
		var c bytes.Buffer
		_ = json.Compact(&c, []byte(v.Text()))
		return c.Bytes(), nil
	}
	return nil, fmt.Errorf("a %s, not an array of numbers", v.Kind())
}

// send writes ops, each ID's last, in requests by collection.
func (s *Sink) send(ctx context.Context, ops []op) error {
	keys := make([]string, len(ops))
	for i, o := range ops {
		if !o.skip {
			keys[i] = o.collection + "\x00" + o.id
		} else {
			keys[i] = "\x01" + strconv.Itoa(i)
		}
	}
	type pending struct {
		upserts [][]byte
		deletes []string
	}
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
		if o.point == nil {
			p.deletes = append(p.deletes, o.id)
		} else {
			p.upserts = append(p.upserts, o.point)
		}
	}
	for _, c := range order {
		p := by[c]
		path := "/collections/" + url.PathEscape(c) + "/points"
		dels := make([][]byte, len(p.deletes))
		for i, id := range p.deletes {
			dels[i] = []byte(id)
		}
		if err := s.chunks(ctx, path, dels, true); err != nil {
			return err
		}
		if err := s.chunks(ctx, path, p.upserts, false); err != nil {
			return err
		}
	}
	return nil
}

// chunks sends items, IDs or points, in requests of at most
// max_request_bytes: deletes to path/delete, upserts to path.
func (s *Sink) chunks(ctx context.Context, path string, items [][]byte, del bool) error {
	body := []byte(`{"points":[`)
	start := len(body)
	flush := func() error {
		if len(body) == start {
			return nil
		}
		err := s.request(ctx, path, append(body, "]}"...), del)
		body = body[:start]
		return err
	}
	for _, it := range items {
		if len(body) > start && len(body)+len(it)+3 > s.maxRequest() {
			if err := flush(); err != nil {
				return err
			}
		}
		if len(body) > start {
			body = append(body, ',')
		}
		body = append(body, it...)
	}
	return flush()
}

// request sends one upsert or delete, waiting for it to be applied.
func (s *Sink) request(ctx context.Context, path string, body []byte, del bool) error {
	method := stdhttp.MethodPut
	if del {
		method, path = stdhttp.MethodPost, path+"/delete"
	}
	resp, err := s.client.DoRetry(ctx, fibrest.Request{Method: method, Path: path, Query: "wait=true", Body: body,
		Header: stdhttp.Header{"Content-Type": {"application/json"}}}, 0)
	if err != nil {
		return err
	}
	switch st := resp.Status; {
	case st >= 200 && st < 300:
		return nil
	case st == stdhttp.StatusBadRequest, st == stdhttp.StatusUnprocessableEntity, st == stdhttp.StatusRequestEntityTooLarge:
		return fibrest.Refused(fibrest.StatusError(path, resp))
	}
	return fibrest.StatusError(path, resp)
}

// Test asks for the collection, or the list of them when collection is a
// template, with the sink's API key. It writes nothing.
func (s *Sink) Test(ctx context.Context) error {
	path := "/collections"
	if c, ok := tmpl.Static(s.collection); ok {
		path += "/" + url.PathEscape(c)
	}
	resp, err := s.client.Do(ctx, fibrest.Request{Method: stdhttp.MethodGet, Path: path})
	if err != nil {
		return err
	}
	if resp.Status != stdhttp.StatusOK {
		return fibrest.StatusError(path, resp)
	}
	return nil
}

var (
	_ fitsync.ParallelSink = (*Sink)(nil)
	_ fitsync.Tester       = (*Sink)(nil)
	_ module.Cleaner       = (*Sink)(nil)
)

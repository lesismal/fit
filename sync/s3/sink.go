package syncs3

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sinks + ".s3", New: func() any { return new(Sink) },
		Doc: "Writes each batch as objects of lines to S3 or a compatible (MinIO, SeaweedFS, Ceph, R2), by a codec, gzipped when gzip says, each object put whole, so it is seen only once complete. An object is named by the key template from its first record's commit time and position: a batch written again after a crash, cut the same way, is the same object with the same content, written over. Objects roll by the sink's batch (max_bytes, max_wait) and max_object_bytes. Requests are signed with AWS Signature Version 4 and sent through fib's asynchronous HTTP client."})
}

// The defaults of the sink's params.
const (
	DefaultKeyTemplate          = "{namespace}/{date}/{timestamp}-{position}{ext}"
	DefaultMaxObjectBytes int64 = 64 << 20
	// MaxObjectBytes is the most a single PUT takes.
	MaxObjectBytes int64 = 5 << 30
)

// Sink is the sink s3.
type Sink struct {
	Endpoint           string            `json:"endpoint,omitempty" doc:"The server, as https://minio.internal:9000 or https://s3.eu-west-1.amazonaws.com, with no path: an S3 compatible's address, or AWS's endpoint of region. https://s3.<region>.amazonaws.com when left out."`
	Region             string            `json:"region,omitempty" default:"\"us-east-1\"" doc:"The region requests are signed for: the bucket's on AWS, and what the server is set to elsewhere (MinIO's and most compatibles' us-east-1, R2's auto). us-east-1 when left out."`
	Bucket             string            `json:"bucket" fit:"required" doc:"The bucket. It is not made: it is the user's."`
	Addressing         string            `json:"addressing,omitempty" enum:"path,virtual" doc:"How the bucket is named in a request: path, as https://endpoint/bucket/key, which every compatible takes; virtual, as https://bucket.endpoint/key, as AWS prefers. virtual when endpoint is left out (AWS), path otherwise."`
	AccessKeyID        string            `json:"access_key_id,omitempty" doc:"The access key's ID, sent in each request's signature; with secret_access_key_env. Requests are not signed when both are left out, for a public bucket."`
	SecretAccessKeyEnv string            `json:"secret_access_key_env,omitempty" doc:"The environment variable holding access_key_id's secret, read when the pipeline loads."`
	SessionTokenEnv    string            `json:"session_token_env,omitempty" doc:"The environment variable holding a session token, for temporary credentials, sent as x-amz-security-token; none when left out. Temporary credentials are read once: they are not renewed while the pipeline runs."`
	CAFile             string            `json:"ca_file,omitempty" doc:"A PEM file of the certificates the server's is checked against, in place of the system's roots, for a CA of its own. The check cannot be turned off."`
	ServerName         string            `json:"server_name,omitempty" doc:"The name the server's certificate must have, and the TLS server name sent; the endpoint's host when left out."`
	Timeout            config.Duration   `json:"timeout,omitempty" default:"\"60s\"" doc:"How long one request may take, from connecting to the end of its answer. 60s when left out or 0."`
	Key                string            `json:"key,omitempty" default:"\"{namespace}/{date}/{timestamp}-{position}{ext}\"" doc:"The objects' keys: text with placeholders, of the object's first record: {namespace} (a leading / dropped), {date} as 2006-01-02, {yyyy}, {mm}, {dd}, {hh}, {timestamp} as 20060102T150405.000000000Z, all from its commit time in UTC (or the time of the write, when it has none), {position}, 20 hex digits from its position, and {ext}, the codec's extension (.txt for line, .jsonl for json, .csv for csv) and .gz when gzipped. It must have {position}, so that two objects do not have one name. {namespace}/{date}/{timestamp}-{position}{ext} when left out."`
	Codec              *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes a line of a record: line when left out, which writes after's line field, or the record's canonical JSON when it has none; json writes it as JSON, csv as comma-separated values."`
	Gzip               bool              `json:"gzip,omitempty" doc:"Gzip each object, its key ending in .gz through {ext}, with Content-Type application/gzip. false when left out."`
	MaxObjectBytes     int64             `json:"max_object_bytes,omitempty" min:"0" max:"5368709120" default:"67108864" doc:"The most bytes of lines (before gzip) in one object: a batch larger is written as several, in order. A record larger is an object of its own. 64 MiB when left out or 0."`

	conn  *Conn
	codec fitsync.Codec
	tmpl  []keyPart
	byNS  bool
	ext   string
	ctype string
	nowFn func() time.Time
}

// keyPart is a key template's text, or a placeholder.
type keyPart struct {
	text, ph string
}

var placeholders = map[string]bool{"namespace": true, "date": true, "yyyy": true, "mm": true, "dd": true, "hh": true,
	"timestamp": true, "position": true, "ext": true}

func parseKey(t string) ([]keyPart, error) {
	var parts []keyPart
	rest := t
	for rest != "" {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			if strings.IndexByte(rest, '}') >= 0 {
				return nil, fmt.Errorf("a } with no {")
			}
			parts = append(parts, keyPart{text: rest})
			break
		}
		if strings.IndexByte(rest[:i], '}') >= 0 {
			return nil, fmt.Errorf("a } with no {")
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return nil, fmt.Errorf("a { is not closed")
		}
		name := rest[i+1 : i+j]
		if !placeholders[name] {
			return nil, fmt.Errorf("{%s} is not one of {namespace}, {date}, {yyyy}, {mm}, {dd}, {hh}, {timestamp}, {position} and {ext}", name)
		}
		parts = append(parts, keyPart{text: rest[:i]}, keyPart{ph: name})
		rest = rest[i+j+1:]
	}
	return parts, nil
}

// Provision checks the params, reads the secrets and loads the codec;
// nothing is sent until the first write.
func (s *Sink) Provision(ctx *module.Context) error {
	conn, errs := NewConn(ctx, ConnParams{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, Addressing: s.Addressing,
		AccessKeyID: s.AccessKeyID, SecretAccessKeyEnv: s.SecretAccessKeyEnv, SessionTokenEnv: s.SessionTokenEnv,
		CAFile: s.CAFile, ServerName: s.ServerName, Timeout: s.Timeout})
	s.conn = conn
	tmpl := s.Key
	if tmpl == "" {
		tmpl = DefaultKeyTemplate
	}
	parts, err := parseKey(tmpl)
	if err != nil {
		errs = append(errs, ctx.Params.Field("key").Errorf("%q: %v", tmpl, err))
	}
	hasPos := false
	for _, p := range parts {
		hasPos = hasPos || p.ph == "position"
		s.byNS = s.byNS || p.ph == "namespace"
	}
	if err == nil && !hasPos {
		errs = append(errs, ctx.Params.Field("key").Errorf("%q has no {position}: two objects would have one name", tmpl))
	}
	s.tmpl = parts
	if len(errs) > 0 {
		return errs
	}
	if s.codec, err = loadCodec(ctx, s.Codec); err != nil {
		return err
	}
	mod := "line"
	if s.Codec != nil {
		mod = s.Codec.Module
	}
	switch mod {
	case "json":
		s.ext, s.ctype = ".jsonl", "application/x-ndjson"
	case "csv":
		s.ext, s.ctype = ".csv", "text/csv; charset=utf-8"
	default:
		s.ext, s.ctype = ".txt", "text/plain; charset=utf-8"
	}
	if s.Gzip {
		s.ext, s.ctype = s.ext+".gz", "application/gzip"
	}
	return nil
}

// loadCodec loads a codec ref, line when there is none.
func loadCodec(ctx *module.Context, ref *config.ModuleRef) (fitsync.Codec, error) {
	if ref == nil {
		return &fitsync.LineCodec{}, nil
	}
	v, err := ctx.Load(fitsync.Codecs, *ref)
	if err != nil {
		return nil, err
	}
	c, ok := v.(fitsync.Codec)
	if !ok {
		module.Cleanup(v)
		return nil, ref.At.Field("module").Errorf("%s is not a codec", ref.Module)
	}
	return c, nil
}

// ParallelWrites marks the sink as one a job may write through several
// writers at once: each writer's objects have names of their own.
func (s *Sink) ParallelWrites() {}

// Cleanup closes the client and lets the codec go.
func (s *Sink) Cleanup() {
	if s.conn != nil {
		s.conn.Close()
	}
	module.Cleanup(s.codec)
}

func (s *Sink) maxObject() int64 {
	if s.MaxObjectBytes <= 0 {
		return DefaultMaxObjectBytes
	}
	return min(s.MaxObjectBytes, MaxObjectBytes)
}

// object is the records batch[lo:hi] as lines.
type object struct {
	lo, hi int
	data   []byte
}

// Write puts the batch's records as objects, in order, and returns once
// every one is stored.
func (s *Sink) Write(ctx context.Context, batch []*fitsync.Record) error {
	objs, failed := s.objects(batch)
	for _, o := range objs {
		key, body, err := s.render(batch, o)
		if err == nil {
			err = s.conn.Put(ctx, key, s.ctype, body)
		}
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			if o.lo == 0 {
				return fmt.Errorf("s3: %w", err)
			}
			return &fitsync.BatchError{Index: o.lo, Err: fmt.Errorf("s3: %w", err)}
		}
	}
	if failed != nil {
		return failed
	}
	return nil
}

// objects encodes the batch into objects: a new one at each change of
// namespace when the key names it, and past max_object_bytes. A record the
// codec fails on ends them, with its error.
func (s *Sink) objects(batch []*fitsync.Record) ([]object, *fitsync.BatchError) {
	var objs []object
	cur := object{}
	most := s.maxObject()
	var failed *fitsync.BatchError
	for i, r := range batch {
		n := len(cur.data)
		d, err := s.codec.Encode(cur.data, batch[i:i+1])
		if err != nil {
			failed = &fitsync.BatchError{Index: i, Err: fitsync.Permanent(fmt.Errorf("s3: encoding: %w", err))}
			break
		}
		cur.data = d
		if cur.hi > cur.lo && (int64(len(d)) > most || s.byNS && r.Namespace != batch[cur.lo].Namespace) {
			next := object{lo: i, hi: i, data: bytes.Clone(d[n:])}
			cur.data = d[:n]
			objs = append(objs, cur)
			cur = next
		}
		cur.hi = i + 1
	}
	if cur.hi > cur.lo {
		objs = append(objs, cur)
	}
	return objs, failed
}

// render returns o's key and body.
func (s *Sink) render(batch []*fitsync.Record, o object) (string, []byte, error) {
	body := o.data
	if s.Gzip {
		var buf bytes.Buffer
		// Writing to a bytes.Buffer does not fail. The header has no time
		// or name, so the same lines make the same bytes.
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = buf.Bytes()
	}
	first := batch[o.lo]
	t := first.CommitTime
	if t.IsZero() {
		if s.nowFn != nil {
			t = s.nowFn()
		} else {
			t = time.Now()
		}
	}
	t = t.UTC()
	var b strings.Builder
	for _, p := range s.tmpl {
		switch p.ph {
		case "":
			b.WriteString(p.text)
		case "namespace":
			b.WriteString(strings.TrimLeft(first.Namespace, "/"))
		case "date":
			b.WriteString(t.Format("2006-01-02"))
		case "yyyy":
			b.WriteString(t.Format("2006"))
		case "mm":
			b.WriteString(t.Format("01"))
		case "dd":
			b.WriteString(t.Format("02"))
		case "hh":
			b.WriteString(t.Format("15"))
		case "timestamp":
			b.WriteString(t.Format("20060102T150405.000000000Z"))
		case "position":
			b.WriteString(positionToken(batch, o))
		case "ext":
			b.WriteString(s.ext)
		}
	}
	key := strings.TrimLeft(b.String(), "/")
	if key == "" || len(key) > 1024 {
		return "", nil, fitsync.Permanent(fmt.Errorf("the key %q is empty or longer than 1024 bytes", key))
	}
	return key, body, nil
}

// positionToken names an object by its first record: 20 hex digits of a
// SHA-256 of its namespace and position, or, when it has none, of the
// nearest position before it in the batch and how far it is past it (+n),
// or the nearest after it (-n), or, in a batch with no position at all, of
// the object's lines.
func positionToken(batch []*fitsync.Record, o object) string {
	h := sha256.New()
	first := batch[o.lo]
	h.Write([]byte(first.Namespace))
	h.Write([]byte{0})
	found := false
	for j := o.lo; j >= 0 && !found; j-- {
		if batch[j].Position != nil {
			h.Write(batch[j].Position)
			h.Write([]byte("+" + strconv.Itoa(o.lo-j)))
			found = true
		}
	}
	for j := o.lo + 1; j < len(batch) && !found; j++ {
		if batch[j].Position != nil {
			h.Write(batch[j].Position)
			h.Write([]byte("-" + strconv.Itoa(j-o.lo)))
			found = true
		}
	}
	if !found {
		h.Write([]byte("lines"))
		h.Write(o.data)
	}
	return hex.EncodeToString(h.Sum(nil))[:20]
}

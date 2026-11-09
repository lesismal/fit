// Package syncembed is sync's transform embed: it computes a vector from
// text fields of each record through an OpenAI-compatible embeddings API
// (POST {url}/embeddings), fit's own llm.proxy when given no URL, so that a
// pipeline from a database to a vector store (the qdrant and milvus sinks)
// needs nothing else. Requests go out through fib's asynchronous HTTP
// client, a batch's texts in requests of batch_records, several at once,
// waited on from the pipeline's goroutine, never a fib worker's; vectors
// are cached by a hash of their text, within a bound. See docs/sync.md.
package syncembed

import (
	"container/list"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math"
	"net"
	stdhttp "net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	"github.com/lesismal/fit/sync/internal/fibrest"
)

func init() {
	module.Register(module.Info{ID: fitsync.Transforms + ".embed", New: func() any { return new(Embed) },
		Doc: "Computes a vector from text fields of each insert and update, through an OpenAI-compatible embeddings API (fit's own llm.proxy when given no url, or any endpoint), and sets it in a field of the row, as the qdrant and milvus sinks read it. A batch's texts go in requests of batch_records, max_parallel at once, through fib's asynchronous HTTP client; vectors are cached by a hash of the model and the text, within cache_bytes. A request that fails, after attempts, fails its records, which on_error takes."})
}

// The defaults of the transform's params.
const (
	DefaultInto         = "embedding"
	DefaultBatchRecords = 64
	DefaultMaxParallel  = 4
	DefaultMaxTextBytes = 32 << 10
	DefaultCacheBytes   = 32 << 20
	DefaultTimeout      = 30 * time.Second
	DefaultAttempts     = 3
)

// Embed is the transform embed.
type Embed struct {
	URL          string          `json:"url,omitempty" doc:"The API's base URL, to which /embeddings is added, as https://api.openai.com/v1 or http://ollama:11434/v1. This config's llm.proxy's api, on loopback, when left out; one or the other is needed."`
	Model        string          `json:"model" fit:"required" doc:"The model the vectors are computed by, as text-embedding-3-small."`
	APIKeyEnv    string          `json:"api_key_env,omitempty" doc:"The environment variable holding the API's key, sent as Authorization: Bearer, read when the pipeline loads: one of llm.proxy's api_keys for it. None when left out."`
	Fields       []string        `json:"fields" fit:"required" doc:"The row's fields whose text is embedded, joined by separator in this order; a field missing or null is left out. A record with none of them passes without a vector."`
	Separator    string          `json:"separator,omitempty" doc:"What joins the fields' texts. A line break when left out."`
	Into         string          `json:"into,omitempty" default:"\"embedding\"" doc:"The row's field the vector is set in, an array of floats, replacing what is there. embedding when left out."`
	Dimensions   int             `json:"dimensions,omitempty" min:"0" doc:"The vectors' size asked of a model that can shorten them (OpenAI's text-embedding-3 models); the model's own when left out or 0."`
	BatchRecords int             `json:"batch_records,omitempty" min:"0" max:"2048" default:"64" doc:"The most texts one request carries. 64 when left out or 0."`
	MaxParallel  int             `json:"max_parallel,omitempty" min:"0" max:"16" default:"4" doc:"The most requests a batch sends at once. 4 when left out or 0."`
	MaxTextBytes int             `json:"max_text_bytes,omitempty" min:"0" default:"32768" doc:"The longest text embedded; a record whose text is longer fails, which on_error takes, rather than be cut. Keep it within the model's input tokens. 32 KiB when left out or 0."`
	CacheBytes   int64           `json:"cache_bytes,omitempty" min:"0" default:"33554432" doc:"The most bytes of vectors kept, by a hash of the model, the dimensions and the text, the least used dropped first, so that a text seen again is not sent again: a replay, an update that leaves the text as it was. 32 MiB when left out or 0."`
	Timeout      config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long one request may take, from connecting to the end of its answer. 30s when left out or 0."`
	Attempts     int             `json:"attempts,omitempty" min:"0" max:"10" default:"3" doc:"How many times a request is sent while it fails to be answered or is answered 408, 429 or 5xx, with a backoff between, before its records fail. 3 when left out or 0."`
	CAFile       string          `json:"ca_file,omitempty" doc:"A PEM file of the certificates the API's is checked against, in place of the system's roots. The check cannot be turned off; llm.proxy's own certificate is pinned."`
	ServerName   string          `json:"server_name,omitempty" doc:"The name the API's certificate must have, and the TLS server name sent; the URL's host when left out."`

	client *fibrest.Client
	ctx    context.Context
	cancel context.CancelFunc
	cache  *cache
	salt   string
}

// Provision checks the params, finds llm.proxy's API when given no URL, and
// reads the secret and TLS files; nothing is sent until the first batch.
func (e *Embed) Provision(ctx *module.Context) error {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	if e.Into == "" {
		e.Into = DefaultInto
	}
	if e.Separator == "" {
		e.Separator = "\n"
	}
	if e.BatchRecords <= 0 {
		e.BatchRecords = DefaultBatchRecords
	}
	if e.MaxParallel <= 0 {
		e.MaxParallel = DefaultMaxParallel
	}
	if e.MaxTextBytes <= 0 {
		e.MaxTextBytes = DefaultMaxTextBytes
	}
	if e.CacheBytes <= 0 {
		e.CacheBytes = DefaultCacheBytes
	}
	if e.Attempts <= 0 {
		e.Attempts = DefaultAttempts
	}
	if len(e.Fields) == 0 {
		add("fields", "no fields")
	}
	header := stdhttp.Header{"Content-Type": {"application/json"}}
	if e.APIKeyEnv != "" {
		key, fe := fibrest.Secret("api_key_env", e.APIKeyEnv)
		if fe != nil {
			add(fe.Field, "%v", fe.Err)
		}
		header.Set("Authorization", "Bearer "+key)
	}
	p := fibrest.Params{URL: e.URL, CAFile: e.CAFile, ServerName: e.ServerName, Timeout: e.Timeout.Std(), MaxResponseBytes: 64 << 20, Header: header}
	if e.URL == "" {
		api := ctx.LLMProxy()
		if api == nil {
			add("url", "no url, and this config runs no llm.proxy with an api to call")
		} else {
			var err error
			if p.URL, p.PinnedCert, err = ProxyURL(api); err != nil {
				add("url", "llm.proxy's api: %v", err)
			}
		}
	}
	var ferrs []fibrest.FieldError
	e.client, ferrs = fibrest.New(ctx.Engine(), p)
	if e.URL != "" || p.URL != "" {
		for _, fe := range ferrs {
			add(fe.Field, "%v", fe.Err)
		}
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	e.cache = newCache(e.CacheBytes)
	e.salt = fmt.Sprintf("%s\x00%d\x00", e.Model, e.Dimensions)
	if len(errs) > 0 {
		e.Cleanup()
		return errs
	}
	return nil
}

// ProxyURL returns the base URL of llm.proxy's API, on loopback whatever
// address it listens on, and with TLS the DER of its certificate to pin.
func ProxyURL(api *config.LLMProxyAPI) (string, []byte, error) {
	host, port, err := net.SplitHostPort(api.Address)
	if err != nil {
		return "", nil, err
	}
	switch ip := net.ParseIP(host); {
	case host == "" || ip != nil && ip.IsUnspecified() && ip.To4() != nil:
		host = "127.0.0.1"
	case ip != nil && ip.IsUnspecified():
		host = "::1"
	}
	if api.CertFile == "" {
		return "http://" + net.JoinHostPort(host, port) + "/v1", nil, nil
	}
	der, err := leafDER(api.CertFile)
	if err != nil {
		return "", nil, fmt.Errorf("cert_file: %w", err)
	}
	return "https://" + net.JoinHostPort(host, port) + "/v1", der, nil
}

// leafDER reads the first certificate of a PEM file.
func leafDER(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return nil, errors.New("no certificate in the file")
		}
		if b.Type == "CERTIFICATE" {
			if _, err := x509.ParseCertificate(b.Bytes); err != nil {
				return nil, err
			}
			return b.Bytes, nil
		}
	}
}

// Cleanup stops the requests under way and closes the client.
func (e *Embed) Cleanup() {
	if e.cancel != nil {
		e.cancel()
	}
	if e.client != nil {
		e.client.Close()
	}
}

// Apply embeds one record, as ApplyBatch does a batch of one.
func (e *Embed) Apply(r *fitsync.Record, emit func(*fitsync.Record)) error {
	var err error
	if berr := e.ApplyBatch([]*fitsync.Record{r}, func(_ int, o *fitsync.Record) { emit(o) }, func(_ int, ferr error) { err = ferr }); berr != nil {
		return berr
	}
	return err
}

// job is a text the batch needs a vector of.
type job struct {
	key  [32]byte
	text string
	vec  []float32
	err  error
}

// ApplyBatch sets the vector of each record's text, asking the API for
// those not cached, and emits the records in order; a record whose
// request failed is failed, which on_error takes.
func (e *Embed) ApplyBatch(in []*fitsync.Record, emit func(int, *fitsync.Record), fail func(int, error)) error {
	keys := make([]*job, len(in))
	jobs := map[[32]byte]*job{}
	var todo []*job
	for i, r := range in {
		text, ok, err := e.text(r)
		if err != nil {
			keys[i] = &job{err: err}
			continue
		}
		if !ok {
			continue
		}
		k := sha256.Sum256([]byte(e.salt + text))
		j := jobs[k]
		if j == nil {
			j = &job{key: k, text: text}
			jobs[k] = j
			if v, hit := e.cache.get(k); hit {
				j.vec = v
			} else {
				todo = append(todo, j)
			}
		}
		keys[i] = j
	}
	e.fetch(todo)
	for i, r := range in {
		j := keys[i]
		switch {
		case j == nil:
			emit(i, r)
		case j.err != nil:
			fail(i, j.err)
		default:
			o := r.Own()
			o.After = with(o.After, e.Into, j.vec)
			emit(i, o)
		}
	}
	return e.ctx.Err()
}

// text returns the text r's vector is of, and false when r gets none: not
// an insert, update or message, or none of the fields.
func (e *Embed) text(r *fitsync.Record) (string, bool, error) {
	switch r.Op {
	case fitsync.OpInsert, fitsync.OpUpdate, fitsync.OpMessage:
	default:
		return "", false, nil
	}
	if r.After.Kind() != fitsync.KindMap {
		return "", false, nil
	}
	var b strings.Builder
	n := 0
	for _, f := range e.Fields {
		v, ok := r.After.Get(f)
		if !ok || v.IsNull() {
			continue
		}
		if n > 0 {
			b.WriteString(e.Separator)
		}
		n++
		b.WriteString(fitsync.TextOf(v))
	}
	if n == 0 {
		return "", false, nil
	}
	if b.Len() > e.MaxTextBytes {
		return "", false, fmt.Errorf("embed: the text is %d bytes, more than max_text_bytes", b.Len())
	}
	return b.String(), true, nil
}

// with returns row with field name set to vec, in place or added last.
func with(row fitsync.Value, name string, vec []float32) fitsync.Value {
	elems := make([]fitsync.Value, len(vec))
	for i, f := range vec {
		elems[i] = fitsync.Float(float64(f))
	}
	v := fitsync.Array(elems...)
	fs := append([]fitsync.Field(nil), row.Fields()...)
	for i := range fs {
		if fs[i].Name == name {
			fs[i].Value = v
			return fitsync.Map(fs...)
		}
	}
	return fitsync.Map(append(fs, fitsync.Field{Name: name, Value: v})...)
}

// fetch asks the API for todo's vectors, in requests of batch_records,
// max_parallel at once, and caches them.
func (e *Embed) fetch(todo []*job) {
	if len(todo) == 0 {
		return
	}
	sem := make(chan struct{}, e.MaxParallel)
	var wg sync.WaitGroup
	for start := 0; start < len(todo); start += e.BatchRecords {
		part := todo[start:min(start+e.BatchRecords, len(todo))]
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			e.request(part, true)
		}()
	}
	wg.Wait()
	for _, j := range todo {
		if j.err == nil {
			e.cache.put(j.key, j.vec)
		}
	}
}

// request asks for part's vectors, setting each job's vector or error; a
// request refused for what it carries (400, 413, 422) is asked again text
// by text, with split, so that one text the API refuses fails its records
// alone.
func (e *Embed) request(part []*job, split bool) {
	body := map[string]any{"model": e.Model, "input": texts(part), "encoding_format": "float"}
	if e.Dimensions > 0 {
		body["dimensions"] = e.Dimensions
	}
	b, _ := json.Marshal(body)
	resp, err := e.client.DoRetry(e.ctx, fibrest.Request{Method: stdhttp.MethodPost, Path: "/embeddings", Body: b}, e.Attempts)
	if err == nil {
		switch st := resp.Status; {
		case st == stdhttp.StatusOK:
			err = e.parse(part, resp.Body)
		case st == stdhttp.StatusBadRequest || st == stdhttp.StatusRequestEntityTooLarge || st == stdhttp.StatusUnprocessableEntity:
			if split && len(part) > 1 {
				for _, j := range part {
					e.request([]*job{j}, false)
				}
				return
			}
			err = fitsync.Permanent(fibrest.StatusError("embed: the embeddings API", resp))
		default:
			err = fibrest.StatusError("embed: the embeddings API", resp)
		}
	} else {
		err = fmt.Errorf("embed: the embeddings API: %w", err)
	}
	if err != nil {
		for _, j := range part {
			j.err = err
		}
	}
}

func texts(part []*job) []string {
	out := make([]string, len(part))
	for i, j := range part {
		out[i] = j.text
	}
	return out
}

// parse reads an answer's vectors into part, by their index.
func (e *Embed) parse(part []*job, body []byte) error {
	var a struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &a); err != nil {
		return fmt.Errorf("embed: the embeddings API's answer: %v: %s", err, fibrest.Clip(body))
	}
	if len(a.Data) != len(part) {
		return fmt.Errorf("embed: the embeddings API answered %d vectors for %d texts", len(a.Data), len(part))
	}
	for _, d := range a.Data {
		if d.Index < 0 || d.Index >= len(part) || part[d.Index].vec != nil || len(d.Embedding) == 0 {
			return fmt.Errorf("embed: the embeddings API answered a vector of index %d, twice, or empty", d.Index)
		}
		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("embed: the embeddings API answered %v", f)
			}
			v[i] = float32(f)
		}
		part[d.Index].vec = v
	}
	return nil
}

// cache keeps vectors by their key, within a bound of bytes, the least
// used dropped first.
type cache struct {
	mu    sync.Mutex
	most  int64
	size  int64
	ll    *list.List
	items map[[32]byte]*list.Element
}

type entry struct {
	key [32]byte
	vec []float32
}

const entryOverhead = 128

func newCache(most int64) *cache {
	return &cache{most: most, ll: list.New(), items: map[[32]byte]*list.Element{}}
}

func (c *cache) get(k [32]byte) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return nil, false
	}
	c.ll.MoveToFront(el)
	return el.Value.(*entry).vec, true
}

func (c *cache) put(k [32]byte, vec []float32) {
	n := int64(len(vec)*4 + entryOverhead)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[k]; ok || n > c.most {
		return
	}
	c.items[k] = c.ll.PushFront(&entry{k, vec})
	c.size += n
	for c.size > c.most {
		el := c.ll.Back()
		en := el.Value.(*entry)
		c.ll.Remove(el)
		delete(c.items, en.key)
		c.size -= int64(len(en.vec)*4 + entryOverhead)
	}
}

// Len returns how many vectors the cache holds, for tests.
func (c *cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

var (
	_ fitsync.BatchTransform = (*Embed)(nil)
	_ module.Cleaner         = (*Embed)(nil)
)

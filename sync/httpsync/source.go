// Package httpsync is sync's HTTP: the source http, which takes records a
// route's sync_ingest handler hands it and answers each request once every
// sink has acknowledged its records, and the sink http, which posts batches
// as JSON or NDJSON through fib's asynchronous HTTP client. See
// docs/decisions/036-sync.md.
package httpsync

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	stdhttp "net/http"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
)

func init() {
	module.Register(module.Info{ID: fitsync.Sources + ".http", New: func() any { return new(Source) },
		Doc: "Takes records posted to a route whose handler is sync_ingest naming the pipeline: a body of JSON (an object, an array of objects, or several of them, as NDJSON) or of a codec's format becomes records, and the request is answered 200 only once every sink has acknowledged them, 503 with Retry-After when the source's pending requests are full or the wait passes the handler's timeout, so a client that retries until answered loses nothing. It has no checkpoint to resume from: what was not answered 200 is the client's to send again."})
}

// The defaults of the source's params.
const (
	DefaultMaxPendingRequests = 1024
	DefaultMaxPendingBytes    = 64 << 20
	DefaultNamespace          = "http"
)

// The errors Offer refuses a body with.
var (
	// ErrBusy says the source's pending requests or bytes are full, or the
	// source is not running: the client tries again later (503).
	ErrBusy = errors.New("the pipeline is busy: try again")
	// ErrMediaType says the body's Content-Type is not one the source reads
	// (415).
	ErrMediaType = errors.New("the Content-Type is not one the pipeline reads")
)

// Source is the source http.
type Source struct {
	Codec              *config.ModuleRef `json:"codec,omitempty" module:"sync.codecs" doc:"What makes records of a body, in place of JSON: each line of it, or the whole body (framing), goes to the codec, whatever the request's Content-Type. JSON when left out: the body is JSON or NDJSON, its Content-Type application/json, application/x-ndjson, application/jsonl, a +json type, text/plain or none."`
	Framing            string            `json:"framing,omitempty" enum:"lines,body" default:"\"lines\"" doc:"With codec, what the codec is handed: lines, each line of the body (empty lines skipped, a trailing \\r dropped), or body, the whole body at once, for a format of several lines per record. lines when left out."`
	Form               string            `json:"form,omitempty" enum:"plain,record" default:"\"plain\"" doc:"Without codec, what each JSON object is: plain, a row, made a message record whose after holds its members; record, a whole record in its canonical JSON, with its operation, key and namespace. plain when left out."`
	Namespace          string            `json:"namespace,omitempty" default:"\"http\"" doc:"The namespace of records that come without one. http when left out."`
	MaxPendingRequests int               `json:"max_pending_requests,omitempty" min:"0" max:"1000000" default:"1024" doc:"The most requests taken and not yet answered for good, waiting to be read or for their records' acknowledgement; one past it is answered 503 at once. 1024 when left out or 0."`
	MaxPendingBytes    int64             `json:"max_pending_bytes,omitempty" min:"0" default:"67108864" doc:"The most bytes of bodies taken and not yet read into records; a body past it is answered 503 at once. The records are bounded by the pipeline's queue. 64 MiB when left out or 0."`

	codec fitsync.Codec
	json  fitsync.JSONCodec

	mu      sync.Mutex
	running bool
	inbox   chan *request
	// count and bytes are the requests pending and their bodies' bytes
	// not yet read; waiting are the requests whose records were emitted,
	// in order, until acknowledged.
	count   int
	bytes   int64
	waiting []*request
	runID   string
	seq     uint64
}

// request is a body offered to the source.
type request struct {
	body        []byte
	contentType string
	answer      func(Outcome)
	at          time.Time
	seq         uint64
	records     int
}

// Outcome is what a request is answered.
type Outcome struct {
	// Status is 200 once every sink has its records, 400 for a body that
	// is not what the source reads, or 503 when they may not be written.
	Status int
	// Records is how many records the body held, with 200.
	Records int
	// Err says why, with a status other than 200.
	Err string
}

// Provision loads the codec.
func (s *Source) Provision(ctx *module.Context) error {
	if s.MaxPendingRequests == 0 {
		s.MaxPendingRequests = DefaultMaxPendingRequests
	}
	if s.MaxPendingBytes == 0 {
		s.MaxPendingBytes = DefaultMaxPendingBytes
	}
	if s.Namespace == "" {
		s.Namespace = DefaultNamespace
	}
	s.json.Form = s.Form
	if s.Codec != nil {
		v, err := ctx.Load(fitsync.Codecs, *s.Codec)
		if err != nil {
			return err
		}
		c, ok := v.(fitsync.Codec)
		if !ok {
			module.Cleanup(v)
			return s.Codec.At.Field("module").Errorf("%s is not a codec", s.Codec.Module)
		}
		s.codec = c
	} else if s.Framing != "" {
		return ctx.Errorf("framing", "framing is codec's: without one the body is JSON")
	}
	return nil
}

// Cleanup lets the codec go, and answers what is still pending 503.
func (s *Source) Cleanup() {
	module.Cleanup(s.codec)
	s.mu.Lock()
	w := s.waiting
	s.waiting = nil
	s.mu.Unlock()
	for _, q := range w {
		s.finish(q, Outcome{Status: stdhttp.StatusServiceUnavailable, Err: "the pipeline stopped"})
	}
}

// registry is the running http sources, by pipeline.
var registry = struct {
	sync.Mutex
	m map[string]*Source
}{m: map[string]*Source{}}

// Lookup returns the http source running in the pipeline named name, nil
// when none is.
func Lookup(name string) *Source {
	registry.Lock()
	defer registry.Unlock()
	return registry.m[name]
}

// Offer hands the source a request's body, which it reads into records on
// the pipeline's goroutine; answer is called once, from a goroutine of the
// pipeline's or the caller's, with the outcome. It never waits: when the
// source has no room it returns ErrBusy, and answer is not called.
func (s *Source) Offer(body []byte, contentType string, answer func(Outcome)) error {
	if s.codec == nil && !jsonType(contentType) {
		return ErrMediaType
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.count >= s.MaxPendingRequests || s.bytes > 0 && s.bytes+int64(len(body)) > s.MaxPendingBytes {
		return ErrBusy
	}
	q := &request{body: body, contentType: contentType, answer: answer, at: time.Now()}
	select {
	case s.inbox <- q:
	default:
		return ErrBusy
	}
	s.count++
	s.bytes += int64(len(body))
	return nil
}

// jsonType reports whether a Content-Type is one JSON is read from.
func jsonType(ct string) bool {
	if ct == "" {
		return true
	}
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch mt {
	case "application/json", "application/x-ndjson", "application/ndjson", "application/jsonl", "application/x-jsonlines", "text/plain":
		return true
	}
	return strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")
}

// finish answers q, once, and lets its room go.
func (s *Source) finish(q *request, o Outcome) {
	s.mu.Lock()
	s.count--
	s.mu.Unlock()
	q.answer(o)
}

// position is the source's position: a run of it and the request's number
// within it. It is not resumed from.
type position struct {
	Run     string `json:"run"`
	Request uint64 `json:"request"`
}

// Run registers the source under its pipeline, and reads the bodies offered
// to it into records until ctx ends. It never ends by itself, in a job (mode
// once) too.
func (s *Source) Run(ctx context.Context, r *fitsync.Run) error {
	var id [6]byte
	_, _ = rand.Read(id[:])
	s.mu.Lock()
	stale := s.waiting
	s.waiting = nil
	if s.inbox == nil {
		s.inbox = make(chan *request, s.MaxPendingRequests)
	}
	s.runID, s.running = hex.EncodeToString(id[:]), true
	s.mu.Unlock()
	// What an earlier run emitted and was not acknowledged is gone with it.
	for _, q := range stale {
		s.finish(q, Outcome{Status: stdhttp.StatusServiceUnavailable, Err: "the pipeline restarted"})
	}
	registry.Lock()
	if old := registry.m[r.Pipeline]; old != nil && old != s {
		old.mu.Lock()
		old.running = false
		old.mu.Unlock()
	}
	registry.m[r.Pipeline] = s
	registry.Unlock()
	defer func() {
		registry.Lock()
		if registry.m[r.Pipeline] == s {
			delete(registry.m, r.Pipeline)
		}
		registry.Unlock()
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
		for {
			select {
			case q := <-s.inbox:
				s.mu.Lock()
				s.bytes -= int64(len(q.body))
				s.mu.Unlock()
				s.finish(q, Outcome{Status: stdhttp.StatusServiceUnavailable, Err: "the pipeline is stopping"})
			default:
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case q := <-s.inbox:
			if err := s.take(q, r); err != nil {
				return err
			}
		}
	}
}

// take reads q into records and emits them, the last carrying the position
// its answer waits for.
func (s *Source) take(q *request, r *fitsync.Run) error {
	recs, err := s.decode(q)
	s.mu.Lock()
	s.bytes -= int64(len(q.body))
	s.mu.Unlock()
	q.body = nil
	if err != nil {
		s.finish(q, Outcome{Status: stdhttp.StatusBadRequest, Err: err.Error()})
		return nil
	}
	if len(recs) == 0 {
		s.finish(q, Outcome{Status: stdhttp.StatusOK})
		return nil
	}
	s.mu.Lock()
	s.seq++
	q.seq, q.records = s.seq, len(recs)
	pos, _ := json.Marshal(position{s.runID, q.seq})
	s.waiting = append(s.waiting, q)
	s.mu.Unlock()
	tx := ""
	if len(recs) > 1 {
		tx = fmt.Sprintf("http-%s-%d", s.runID, q.seq)
	}
	for i, rec := range recs {
		rec.Source = "http"
		if rec.Namespace == "" {
			rec.Namespace = s.Namespace
		}
		if rec.CommitTime.IsZero() {
			rec.CommitTime = q.at
		}
		rec.Tx = fitsync.Tx{ID: tx, First: tx != "" && i == 0, Last: tx != "" && i == len(recs)-1}
		rec.Position = nil
		if i == len(recs)-1 {
			rec.Position = pos
		}
		if err := r.Emit(rec); err != nil {
			s.drop(q, "the pipeline is stopping")
			return err
		}
	}
	return nil
}

// drop takes q out of waiting, if it is there, and answers it 503.
func (s *Source) drop(q *request, why string) {
	s.mu.Lock()
	found := false
	for i, w := range s.waiting {
		if w == q {
			s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
			found = true
			break
		}
	}
	s.mu.Unlock()
	if found {
		s.finish(q, Outcome{Status: stdhttp.StatusServiceUnavailable, Err: why})
	}
}

// Acknowledged answers 200 every request whose records every sink has.
func (s *Source) Acknowledged(p fitsync.Position) {
	var at position
	if json.Unmarshal(p, &at) != nil {
		return
	}
	s.mu.Lock()
	if at.Run != s.runID {
		s.mu.Unlock()
		return
	}
	n := 0
	for n < len(s.waiting) && s.waiting[n].seq <= at.Request {
		n++
	}
	done := s.waiting[:n:n]
	s.waiting = s.waiting[n:]
	s.mu.Unlock()
	for _, q := range done {
		s.finish(q, Outcome{Status: stdhttp.StatusOK, Records: q.records})
	}
}

// decode reads a body into records, all or none.
func (s *Source) decode(q *request) ([]*fitsync.Record, error) {
	var recs []*fitsync.Record
	emit := func(r *fitsync.Record) { recs = append(recs, r) }
	if s.codec != nil {
		if s.Framing == "body" {
			if err := s.codec.Decode(q.body, emit); err != nil {
				return nil, err
			}
			return recs, nil
		}
		rest := q.body
		for n := 1; len(rest) > 0; n++ {
			line := rest
			if i := bytes.IndexByte(rest, '\n'); i >= 0 {
				line, rest = rest[:i], rest[i+1:]
			} else {
				rest = nil
			}
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				continue
			}
			if err := s.codec.Decode(line, emit); err != nil {
				return nil, fmt.Errorf("line %d: %w", n, err)
			}
		}
		return recs, nil
	}
	d := json.NewDecoder(bytes.NewReader(q.body))
	for n := 1; ; n++ {
		var raw json.RawMessage
		if err := d.Decode(&raw); err == io.EOF {
			return recs, nil
		} else if err != nil {
			return nil, fmt.Errorf("value %d: %w", n, err)
		}
		if t := bytes.TrimLeft(raw, " \t\r\n"); len(t) > 0 && t[0] == '[' {
			var elems []json.RawMessage
			if err := json.Unmarshal(raw, &elems); err != nil {
				return nil, fmt.Errorf("value %d: %w", n, err)
			}
			for i, e := range elems {
				if err := s.json.Decode(e, emit); err != nil {
					return nil, fmt.Errorf("value %d, element %d: %w", n, i+1, err)
				}
			}
			continue
		}
		if err := s.json.Decode(raw, emit); err != nil {
			return nil, fmt.Errorf("value %d: %w", n, err)
		}
	}
}

var (
	_ fitsync.Source       = (*Source)(nil)
	_ fitsync.Acknowledger = (*Source)(nil)
	_ module.Cleaner       = (*Source)(nil)
)

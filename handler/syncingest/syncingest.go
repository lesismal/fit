// Package syncingest is the sync_ingest handler: it hands the bodies posted
// to its route to the http source of a sync pipeline, and answers each once
// every sink of the pipeline has its records. See docs/decisions/036-sync.md.
package syncingest

import (
	"encoding/json"
	stdhttp "net/http"
	"strconv"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/sync/httpsync"
)

func init() {
	module.Register(module.Info{ID: module.Handlers + ".sync_ingest", New: func() any { return new(Ingest) },
		Doc: "Hands the bodies POSTed to the route to the http source of the sync pipeline it names, and answers each 200 once every sink of the pipeline has acknowledged its records; 503 with Retry-After when the pipeline is not running, its pending requests are full, or the wait passes timeout, so that a client that sends again until answered 200 loses nothing. The route's middleware, authentication and limits, applies as to any handler."})
}

// The defaults of the handler's params.
const (
	DefaultTimeout      = 30 * time.Second
	DefaultMaxBodyBytes = 8 << 20
	DefaultRetryAfter   = time.Second
	MaxBodyBytes        = 1 << 30
)

// Ingest is the handler sync_ingest.
type Ingest struct {
	Pipeline     string          `json:"pipeline" fit:"required" doc:"The sync pipeline whose source, an http source, takes the bodies. While it is not running (not in the config, starting again, or stopped), requests are answered 503."`
	Timeout      config.Duration `json:"timeout,omitempty" default:"\"30s\"" doc:"How long a request waits for every sink to acknowledge its records before it is answered 503; its records may still be written, and a client sending it again then writes them twice, which an idempotent sink folds. 30s when left out or 0."`
	MaxBodyBytes int64           `json:"max_body_bytes,omitempty" min:"0" max:"1073741824" default:"8388608" doc:"The largest body taken; a larger one is answered 413. 8 MiB when left out or 0."`
	RetryAfter   config.Duration `json:"retry_after,omitempty" default:"\"1s\"" doc:"The Retry-After a 503 carries, in whole seconds, at least 1. 1s when left out or 0."`

	graph      module.Holder
	timeout    time.Duration
	limit      int64
	retryAfter string
}

// Provision checks the params.
func (h *Ingest) Provision(ctx *module.Context) error {
	if h.Pipeline == "" {
		return ctx.Errorf("pipeline", "no pipeline")
	}
	h.graph = ctx.Graph()
	h.timeout = h.Timeout.Std()
	if h.timeout <= 0 {
		h.timeout = DefaultTimeout
	}
	h.limit = h.MaxBodyBytes
	if h.limit <= 0 {
		h.limit = DefaultMaxBodyBytes
	}
	ra := h.RetryAfter.Std()
	if ra <= 0 {
		ra = DefaultRetryAfter
	}
	h.retryAfter = strconv.FormatInt(max(1, int64((ra+time.Second-1)/time.Second)), 10)
	return nil
}

// answer is a response's body.
type answer struct {
	Records int    `json:"records"`
	Error   string `json:"error,omitempty"`
}

// write answers o.
func (h *Ingest) write(c *fibhttp.Context, o httpsync.Outcome) {
	hdr := stdhttp.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}
	switch o.Status {
	case stdhttp.StatusServiceUnavailable:
		hdr.Set("Retry-After", h.retryAfter)
	case stdhttp.StatusMethodNotAllowed:
		hdr.Set("Allow", "POST")
	}
	b, _ := json.Marshal(answer{Records: o.Records, Error: o.Err})
	_ = c.WriteResponse(fibhttp.Response{StatusCode: o.Status, Header: hdr, Body: append(b, '\n')})
}

// ServeHTTP reads the body on the worker, as it comes, and hands it to the
// source without waiting; the answer is written later, from the pipeline's
// goroutine or the timeout's.
func (h *Ingest) ServeHTTP(c *fibhttp.Context) {
	if c.Request.Method != stdhttp.MethodPost {
		h.write(c, httpsync.Outcome{Status: stdhttp.StatusMethodNotAllowed, Err: "POST records"})
		return
	}
	if httpsync.Lookup(h.Pipeline) == nil {
		h.write(c, httpsync.Outcome{Status: stdhttp.StatusServiceUnavailable, Err: "the pipeline is not running"})
		return
	}
	if c.Request.ContentLength > h.limit {
		h.write(c, httpsync.Outcome{Status: stdhttp.StatusRequestEntityTooLarge, Err: "the body is too large"})
		return
	}
	var buf []byte
	finished := false
	c.OnBody(func(data []byte, fin bool, err error) {
		if finished {
			return
		}
		if err != nil {
			finished = true
			return
		}
		if int64(len(buf)+len(data)) > h.limit {
			finished = true
			h.write(c, httpsync.Outcome{Status: stdhttp.StatusRequestEntityTooLarge, Err: "the body is too large"})
			return
		}
		buf = append(buf, data...)
		if fin {
			finished = true
			h.offer(c, buf)
		}
	})
}

// offer hands body to the source, and holds the request until it is
// answered: by the source, or by the timeout.
func (h *Ingest) offer(c *fibhttp.Context, body []byte) {
	src := httpsync.Lookup(h.Pipeline)
	if src == nil {
		h.write(c, httpsync.Outcome{Status: stdhttp.StatusServiceUnavailable, Err: "the pipeline is not running"})
		return
	}
	h.graph.Retain()
	out := make(chan httpsync.Outcome, 1)
	run := c.Async(func() {
		defer h.graph.Release()
		h.write(c, <-out)
	})
	// mu orders the timer's start before an answer reads it: the source
	// never answers within Offer, and an answer waits here for it.
	var mu sync.Mutex
	var once sync.Once
	var timer *time.Timer
	finish := func(o httpsync.Outcome) {
		once.Do(func() {
			mu.Lock()
			t := timer
			mu.Unlock()
			if t != nil {
				t.Stop()
			}
			out <- o
			run()
		})
	}
	mu.Lock()
	err := src.Offer(body, c.Request.Header.Get("Content-Type"), finish)
	if err == nil {
		timer = time.AfterFunc(h.timeout, func() {
			finish(httpsync.Outcome{Status: stdhttp.StatusServiceUnavailable, Err: "the pipeline's sinks did not acknowledge the records in time"})
		})
	}
	mu.Unlock()
	switch err {
	case nil:
	case httpsync.ErrMediaType:
		finish(httpsync.Outcome{Status: stdhttp.StatusUnsupportedMediaType, Err: err.Error()})
	default:
		finish(httpsync.Outcome{Status: stdhttp.StatusServiceUnavailable, Err: err.Error()})
	}
}

var _ module.Provisioner = (*Ingest)(nil)

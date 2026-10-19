// Package fibtest runs fib HTTP handlers in tests without a network: the
// Context it makes answers into a Recorder rather than onto a connection.
package fibtest

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// Recorder is the stream a test Context answers through. It keeps the
// response, and only takes whole ones: a handler that writes piece by piece
// has its response held by fib and handed over whole once it is done.
type Recorder struct {
	mu       sync.Mutex
	Response fibhttp.Response
	Written  bool
	written  chan struct{}
}

// WriteResponse records the response.
func (r *Recorder) WriteResponse(_ *stdhttp.Request, response fibhttp.Response) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Written {
		return errors.New("fibtest: response written twice")
	}
	r.Response, r.Written = response, true
	if r.written != nil {
		close(r.written)
	}
	return nil
}

// signal returns the channel closed once the response is written. r.mu is
// held.
func (r *Recorder) signal() chan struct{} {
	if r.written == nil {
		r.written = make(chan struct{})
	}
	return r.written
}

// Wait waits up to d for the response, and reports whether it came.
func (r *Recorder) Wait(d time.Duration) bool {
	r.mu.Lock()
	if r.Written {
		// Answered as the handler ran, as most are: nothing to wait on.
		r.mu.Unlock()
		return true
	}
	ch := r.signal()
	r.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	}
}

// WriteInterim refuses informational responses, which no test needs yet.
func (r *Recorder) WriteInterim(int, stdhttp.Header) error {
	return errors.New("fibtest: interim responses are not supported")
}

// Push refuses server pushes.
func (r *Recorder) Push(*stdhttp.Request, string, *stdhttp.PushOptions) error {
	return errors.New("fibtest: push is not supported")
}

// Timeout is how long ServeRequest waits for a handler that answers after
// it returns.
var Timeout = 10 * time.Second

// Serve runs h for a request with method and target, as httptest.NewRequest
// makes one, and returns what it answered.
func Serve(h fibhttp.Handler, method, target string) *Recorder {
	return ServeRequest(h, httptest.NewRequest(method, target, nil))
}

// ServeRequest runs h for r, as fib's servers run handlers, and returns what
// it answered: a response written through Write is finished when h returns,
// as the server finishes it, and one h retained the request for is waited
// for, up to Timeout, as it is answered from elsewhere.
func ServeRequest(h fibhttp.Handler, r *stdhttp.Request) *Recorder {
	rec := &Recorder{}
	fibhttp.Serve(h, fibhttp.NewStreamContext(nil, r, rec))
	rec.Wait(Timeout)
	return rec
}

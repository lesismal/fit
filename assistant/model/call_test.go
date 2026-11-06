package model

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCancelFromEvent: a caller that cancels while it is handed a piece
// hears no more pieces, and the call ends with its cause.
func TestCancelFromEvent(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: %s\n\ndata: %s\n\n", chunkHi, chunkHi, chunkHi)
		flush(w)
		wait(r)
	})
	c := newClient(t, Limits{})
	stop := errors.New("stop")
	n := 0
	done := make(chan error, 1)
	var k *Call
	ready := make(chan struct{})
	k = c.Stream(compat(s.URL), hello, 0, func(Event) {
		<-ready
		n++
		k.Cancel(stop)
	}, func(_ *Result, err error) { done <- err })
	close(ready)
	if err := <-done; !errors.Is(err, stop) || n != 1 {
		t.Fatalf("%v after %d events", err, n)
	}
}

// TestCancelWhileReadingError: a call cancelled while it reads a failed
// answer's message ends with the cancel.
func TestCancelWhileReadingError(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":`))
		flush(w)
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"message":"slow"}}`))
		flush(w)
		wait(r)
	})
	c := newClient(t, Limits{})
	stop := errors.New("stop")
	done := make(chan error, 1)
	k := c.Stream(compat(s.URL), hello, 0, nil, func(_ *Result, err error) { done <- err })
	time.Sleep(50 * time.Millisecond)
	k.Cancel(stop)
	if err := <-done; !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

// TestModelsBadList: a list that is not JSON fails, naming the provider.
func TestModelsBadList(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{`)) })
	_, err := listModels(t, newClient(t, Limits{}), &Provider{Name: "p", Kind: KindOpenAI, BaseURL: s.URL})
	if err == nil || !strings.HasPrefix(err.Error(), "p: the model list") {
		t.Fatal(err)
	}
}

// TestBadLastEvent: an answer whose last event, without its blank line,
// does not parse fails.
func TestBadLastEvent(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("data: {nope")) })
	if o := stream(t, newClient(t, Limits{}), compat(s.URL), hello, 0); o.err == nil || !strings.Contains(o.err.Error(), "not the JSON") {
		t.Fatal(o.err)
	}
}

// TestCallStates: what a call does when it is started, finished or
// described in the states races leave it in.
func TestCallStates(t *testing.T) {
	c := newClient(t, Limits{})
	var got []error
	p := &Provider{Name: "p", Key: "sk-secret-123"}
	k := c.prepare(p, http.MethodGet, "http://127.0.0.1:1", nil, 1, time.Second, nil, func(err error) { got = append(got, err) })
	k.t, _ = c.acquire(p)
	k.ended = true
	k.start() // ended: nothing happens
	k.ended = false
	k.cause = errors.New("gone")
	k.start() // abandoned before it was sent: it ends
	k.finish(errors.New("twice"))
	if len(got) != 1 || got[0] == nil || !strings.Contains(got[0].Error(), "gone") {
		t.Fatalf("%v", got)
	}
	if err := k.describe(errors.New("dial with sk-secret-123 failed")); err.Error() != "p: dial with *** failed" {
		t.Errorf("%v", err)
	}
	a := &answer{}
	if a.end("none") != nil {
		t.Error("ending a call never begun")
	}
}

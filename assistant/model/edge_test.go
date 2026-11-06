package model

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// serve starts a server of h, closed when the test ends.
func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	// The body is read first: net/http notices a client gone only after.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); h(w, r) }))
	t.Cleanup(s.Close)
	return s
}

// wait holds a handler until its client goes, or 5s.
func wait(r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-time.After(5 * time.Second):
	}
}

func flush(w http.ResponseWriter) { w.(http.Flusher).Flush() }

func sse(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		fmt.Fprintf(w, "data: %s\n\n", e)
		flush(w)
	}
}

var hello = &Request{Model: "m", Messages: []Message{{Role: RoleUser, Content: "hi"}}}

func compat(url string) *Provider {
	return &Provider{Name: "p", Kind: KindOpenAICompatible, BaseURL: url, Retries: -1}
}

const chunkHi = `{"choices":[{"delta":{"content":"hi"}}]}`

// TestFirstByteTimeout: an API that sends its header and nothing more is
// given up at the first-byte timeout.
func TestFirstByteTimeout(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		flush(w)
		wait(r)
	})
	p := compat(s.URL)
	p.FirstByteTimeout = 200 * time.Millisecond
	start := time.Now()
	o := stream(t, newClient(t, Limits{}), p, hello, 0)
	if !errors.Is(o.err, ErrFirstByte) || time.Since(start) > 4*time.Second {
		t.Fatalf("%v after %v", o.err, time.Since(start))
	}
}

// TestFirstByteTimeoutBeforeHeader: one that does not even answer is given
// up the same way.
func TestFirstByteTimeoutBeforeHeader(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) { wait(r) })
	p := compat(s.URL)
	p.FirstByteTimeout = 200 * time.Millisecond
	if o := stream(t, newClient(t, Limits{}), p, hello, 0); !errors.Is(o.err, ErrFirstByte) {
		t.Fatal(o.err)
	}
}

// TestIdleTimeout: an answer that goes silent is given up at the idle
// timeout, after what came before it was handed on.
func TestIdleTimeout(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunkHi)
		wait(r)
	})
	p := compat(s.URL)
	p.IdleTimeout = 200 * time.Millisecond
	o := stream(t, newClient(t, Limits{}), p, hello, 0)
	if !errors.Is(o.err, ErrIdle) || len(o.events) != 1 || o.events[0].Text != "hi" {
		t.Fatalf("%v %+v", o.err, o.events)
	}
}

// TestTotalTimeout: an answer that trickles on is given up at the call's
// timeout, however busy.
func TestTotalTimeout(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for {
			fmt.Fprintf(w, "data: %s\n\n", chunkHi)
			flush(w)
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	p := compat(s.URL)
	p.Timeout = 300 * time.Millisecond
	if o := stream(t, newClient(t, Limits{}), p, hello, 0); !errors.Is(o.err, ErrTimeout) || len(o.events) == 0 {
		t.Fatalf("%v, %d events", o.err, len(o.events))
	}
}

// TestCutStream: an answer whose connection is cut midway fails, and is not
// tried again.
func TestCutStream(t *testing.T) {
	var calls atomic.Int32
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, buf, _ := w.(http.Hijacker).Hijack()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		body := "data: " + chunkHi + "\n\ndata: {\"cho"
		fmt.Fprintf(buf, "%x\r\n%s\r\n", len(body), body)
		buf.Flush()
		time.Sleep(50 * time.Millisecond)
		conn.Close()
	})
	p := compat(s.URL)
	p.Retries = 3
	o := stream(t, newClient(t, Limits{}), p, hello, 0)
	if o.err == nil || len(o.events) != 1 || calls.Load() != 1 {
		t.Fatalf("%v, %d events, %d calls", o.err, len(o.events), calls.Load())
	}
}

// TestIncomplete: an answer that ends without saying it is done fails, for
// every kind.
func TestIncomplete(t *testing.T) {
	for kind, body := range map[string]string{
		KindOpenAICompatible: "data: " + chunkHi + "\n\n",
		KindAnthropic:        "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"m\"}}\n\n",
		KindGemini:           "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n",
		KindOpenAIResponses:  "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
		KindOllama:           "{\"message\":{\"content\":\"hi\"},\"done\":false}\n",
	} {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		p := compat(s.URL)
		p.Kind = kind
		if o := stream(t, newClient(t, Limits{}), p, hello, 0); !errors.Is(o.err, ErrIncomplete) {
			t.Errorf("%s: %v", kind, o.err)
		}
	}
}

// TestTooLarge: an answer past the call's bound, a line past the line's,
// an event's data past it, or too many tool calls, fails, and the server
// sees the connection go.
func TestTooLarge(t *testing.T) {
	big := strings.Repeat("x", 4096)
	for name, tc := range map[string]struct {
		lim  Limits
		max  int64
		body func(w http.ResponseWriter)
	}{
		"answer": {max: 10_000, body: func(w http.ResponseWriter) {
			for i := 0; i < 100; i++ {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", big)
				flush(w)
			}
		}},
		"line": {lim: Limits{MaxEventBytes: 1000}, body: func(w http.ResponseWriter) {
			fmt.Fprintf(w, "data: %s", big)
			flush(w)
		}},
		"line in pieces": {lim: Limits{MaxEventBytes: 1000}, body: func(w http.ResponseWriter) {
			fmt.Fprint(w, "data: "+big[:600])
			flush(w)
			time.Sleep(20 * time.Millisecond)
			fmt.Fprint(w, big[:600]+"\n")
			flush(w)
		}},
		"event": {lim: Limits{MaxEventBytes: 1000}, body: func(w http.ResponseWriter) {
			for i := 0; i < 10; i++ {
				fmt.Fprintf(w, "data: %s\n", big[:600])
			}
			flush(w)
		}},
		"first data": {lim: Limits{MaxEventBytes: 1000}, body: func(w http.ResponseWriter) {
			fmt.Fprintf(w, "data: %s\n\n", big[:1500])
			flush(w)
		}},
		"tool calls": {lim: Limits{MaxToolCalls: 2}, body: func(w http.ResponseWriter) {
			for i := 0; i < 3; i++ {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"c%d\",\"function\":{\"name\":\"t\",\"arguments\":\"{}\"}}]}}]}\n\n", i, i)
			}
			flush(w)
		}},
	} {
		var gone atomic.Bool
		s := serve(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			tc.body(w)
			wait(r)
			gone.Store(r.Context().Err() != nil)
		})
		o := stream(t, newClient(t, tc.lim), compat(s.URL), hello, tc.max)
		if !errors.Is(o.err, ErrTooLarge) {
			t.Errorf("%s: %v", name, o.err)
		}
		deadline := time.Now().Add(4 * time.Second)
		for !gone.Load() && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if !gone.Load() {
			t.Errorf("%s: the server did not see the connection go", name)
		}
	}
}

// TestRetries: a call that failed before its answer is tried again, after
// what Retry-After asks, as often as Retries allows; a 4xx but 408 and 429
// is not.
func TestRetries(t *testing.T) {
	var calls atomic.Int32
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"message":"overloaded"}}`))
		case 2:
			w.Header().Set("Retry-After-Ms", "10")
			w.WriteHeader(429)
		case 3:
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close() // no answer at all
		default:
			sse(w, chunkHi, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`, "[DONE]")
		}
	})
	p := compat(s.URL)
	p.Retries = 3
	o := stream(t, newClient(t, Limits{Backoff: 10 * time.Millisecond}), p, hello, 0)
	if o.err != nil || o.res.Attempts != 4 || o.res.Text != "hi" {
		t.Fatalf("%v %+v", o.err, o.res)
	}

	calls.Store(0)
	p.Retries = 1
	o = stream(t, newClient(t, Limits{Backoff: 10 * time.Millisecond}), p, hello, 0)
	var ae *APIError
	if !errors.As(o.err, &ae) || ae.Status != 429 || calls.Load() != 2 {
		t.Fatalf("retries spent: %v, %d calls", o.err, calls.Load())
	}

	var bad atomic.Int32
	s2 := serve(t, func(w http.ResponseWriter, r *http.Request) {
		bad.Add(1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":"bad model"}`))
	})
	p2 := compat(s2.URL)
	p2.Retries = 3
	if o := stream(t, newClient(t, Limits{}), p2, hello, 0); !errors.As(o.err, &ae) || ae.Message != "bad model" || bad.Load() != 1 {
		t.Fatalf("a 400: %v, %d calls", o.err, bad.Load())
	}
}

// TestRetryConnectRefused: a connect refused is tried again, and the call
// fails once the retries are spent.
func TestRetryConnectRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	p := compat("http://" + addr)
	p.Retries = 2
	c := newClient(t, Limits{Backoff: 5 * time.Millisecond})
	o := stream(t, c, p, hello, 0)
	if o.err == nil || !strings.HasPrefix(o.err.Error(), "p: ") {
		t.Fatalf("%v", o.err)
	}
}

// TestCancel: a call cancelled midway, as when the browser goes away, ends
// with the cancel's error, hands on nothing after it, and its connection is
// closed; so is one cancelled before its answer, and one waiting to be
// tried again.
func TestCancel(t *testing.T) {
	var gone atomic.Bool
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, chunkHi)
		wait(r)
		gone.Store(true)
	})
	c := newClient(t, Limits{})
	stop := errors.New("the browser went away")
	var mu sync.Mutex
	var events []Event
	var ended bool
	done := make(chan error, 1)
	var k *Call
	started := make(chan struct{})
	k = c.Stream(compat(s.URL), hello, 0, func(e Event) {
		mu.Lock()
		if ended {
			t.Error("an event after the end")
		}
		events = append(events, e)
		mu.Unlock()
		close(started)
	}, func(_ *Result, err error) {
		mu.Lock()
		ended = true
		mu.Unlock()
		done <- err
	})
	<-started
	k.Cancel(stop)
	k.Cancel(nil)
	if err := <-done; !errors.Is(err, stop) {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !gone.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !gone.Load() {
		t.Error("the server did not see the call go")
	}
	(*Call)(nil).Cancel(nil)

	// Before the answer.
	s2 := serve(t, func(w http.ResponseWriter, r *http.Request) { wait(r) })
	ch := make(chan error, 1)
	k = c.Stream(compat(s2.URL), hello, 0, nil, func(_ *Result, err error) { ch <- err })
	time.Sleep(50 * time.Millisecond)
	k.Cancel(nil)
	if err := <-ch; err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal(err)
	}

	// Between attempts.
	s3 := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
	p := compat(s3.URL)
	p.Retries = 5
	k = NewClient(engine(t), Limits{Backoff: 5 * time.Second}).Stream(p, hello, 0, nil, func(_ *Result, err error) { ch <- err })
	deadline = time.Now().Add(5 * time.Second)
	for k.Attempts() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	k.Cancel(stop)
	if err := <-ch; !errors.Is(err, stop) || time.Since(start) > 2*time.Second {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

// TestRefusedAtOnce: a key that would go in the clear, a bad URL, and a
// closed client fail before anything is sent.
func TestRefusedAtOnce(t *testing.T) {
	c := newClient(t, Limits{})
	for _, p := range []*Provider{
		{Name: "p", Kind: KindOpenAI, BaseURL: "http://192.0.2.1/v1", Key: "sk-123456"},
		{Name: "p", Kind: KindOpenAI, BaseURL: "ftp://x"},
		{Name: "p", Kind: KindOpenAI, BaseURL: "http://[::1"},
	} {
		var got error
		c.Stream(p, hello, 0, nil, func(_ *Result, err error) { got = err })
		if got == nil {
			t.Errorf("%s: no error at once", p.BaseURL)
		}
	}
	var got error
	c.Stream(&Provider{Name: "p", Kind: KindOpenAI, BaseURL: "http://192.0.2.1/v1", Key: "sk-123456"}, hello, 0, nil, func(_ *Result, err error) { got = err })
	if !errors.Is(got, ErrKeyInClear) {
		t.Error(got)
	}
	c.Close()
	c.Stream(compat("http://127.0.0.1:1"), hello, 0, nil, func(_ *Result, err error) { got = err })
	if !errors.Is(got, ErrClosed) {
		t.Error(got)
	}
	if !loopback("localhost") || !loopback("::1") || loopback("example.com") {
		t.Error("loopback")
	}
}

// TestTransportsBounded: providers with many connect timeouts share at most
// maxTransports fib clients; one retired is closed once its calls end.
func TestTransportsBounded(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	})
	c := newClient(t, Limits{})
	for i := 0; i < maxTransports+3; i++ {
		p := compat(s.URL)
		p.ConnectTimeout = time.Duration(i+1) * time.Second
		if o := stream(t, c, p, hello, 0); o.err != nil {
			t.Fatal(o.err)
		}
	}
	c.mu.Lock()
	n := len(c.transports)
	c.mu.Unlock()
	if n != maxTransports {
		t.Errorf("%d transports", n)
	}
	// One retired while its call is in flight is closed when it ends.
	tr, _ := c.acquire(&Provider{ConnectTimeout: 3 * time.Second})
	for i := 0; i < maxTransports; i++ {
		_, _ = c.acquire(&Provider{ConnectTimeout: time.Duration(100+i) * time.Second})
	}
	if !tr.retired {
		t.Fatal("not retired")
	}
	c.release(tr)
}

// TestPinnedCert: llm.proxy's API over TLS is reached with its own
// certificate pinned, and another certificate is refused.
func TestPinnedCert(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(s.Close)
	c := newClient(t, Limits{})
	p := &Provider{Name: "mesh", Kind: KindLLMProxy, BaseURL: s.URL, Key: "sk-proxy-123", PinnedCert: s.Certificate().Raw, Retries: -1}
	if o := stream(t, c, p, hello, 0); o.err != nil || o.res.Text != "ok" {
		t.Fatalf("%v", o.err)
	}
	other := httptest.NewTLSServer(http.NotFoundHandler())
	other.Close()
	p.PinnedCert = append([]byte(nil), p.PinnedCert...)
	p.PinnedCert[len(p.PinnedCert)-1] ^= 1
	p.ConnectTimeout = 2 * time.Second
	if o := stream(t, c, p, hello, 0); o.err == nil {
		t.Fatal("another certificate was taken")
	}
	_ = tls.VersionTLS12
}

// TestErrorBodyBounded: of a failed answer only MaxErrorBytes are read.
func TestErrorBodyBounded(t *testing.T) {
	s := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		for i := 0; i < 100; i++ {
			_, _ = w.Write([]byte(strings.Repeat("e", 1000)))
			flush(w)
		}
		wait(r)
	})
	o := stream(t, newClient(t, Limits{MaxErrorBytes: 2000}), compat(s.URL), hello, 0)
	var ae *APIError
	if !errors.As(o.err, &ae) || ae.Status != 500 || len(ae.Message) > 600 {
		t.Fatalf("%v", o.err)
	}
}

// TestStreamErrors: an error the API sends midway, or an event that is not
// JSON, fails the call with what it said.
func TestStreamErrors(t *testing.T) {
	for kind, body := range map[string]string{
		KindOpenAICompatible: "data: {\"error\":{\"message\":\"boom\"}}\n\n",
		KindAnthropic:        "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"boom\"}}\n\n",
		KindGemini:           "data: {\"error\":{\"code\":500,\"message\":\"boom\"}}\n\n",
		KindOpenAIResponses:  "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"boom\"}}}\n\n",
		KindOllama:           "{\"error\":\"boom\"}\n",
		KindAzureOpenAI:      "data: {\"error\":{\"message\":\"boom\"}}\n\n",
		KindOpenAI:           "event: error\ndata: {\"type\":\"error\",\"message\":\"boom\"}\n\n",
	} {
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		p := compat(s.URL)
		p.Kind, p.APIVersion = kind, "v"
		if kind == KindOpenAI {
			p.Kind = KindOpenAIResponses
		}
		o := stream(t, newClient(t, Limits{}), p, hello, 0)
		var ae *APIError
		if !errors.As(o.err, &ae) || ae.Message != "boom" {
			t.Errorf("%s: %v", kind, o.err)
		}
	}
	for _, kind := range []string{KindOpenAICompatible, KindAnthropic, KindGemini, KindOpenAIResponses, KindOllama} {
		body := "data: {nope\n\n"
		if kind == KindOllama {
			body = "{nope\n"
		}
		s := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) })
		p := compat(s.URL)
		p.Kind = kind
		if o := stream(t, newClient(t, Limits{}), p, hello, 0); o.err == nil || !strings.Contains(o.err.Error(), "not the JSON expected") {
			t.Errorf("%s: %v", kind, o.err)
		}
	}
}

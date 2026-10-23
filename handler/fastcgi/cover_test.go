package fastcgi

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/internal/fibtest"
	"github.com/lesismal/fit/internal/filepool"
	"github.com/lesismal/fit/upstream"
)

// nopHolder is a config hold that holds nothing, as a handler checked
// without a config has.
type nopHolder struct{}

func (nopHolder) Retain()  {}
func (nopHolder) Release() {}

// pacedStream is a stream that takes a response as it is written, says it
// has more than its limit queued after every write, and fails the write
// deadlines it is given when deadlineErr is set; writable is what
// NotifyWritable reports.
type pacedStream struct {
	fibtest.Recorder
	mu          sync.Mutex
	status      int
	body        []byte
	deadlineErr error
	writable    bool
}

func (s *pacedStream) BeginResponse(_ *stdhttp.Request, status int, _ stdhttp.Header) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	return nil
}

func (s *pacedStream) WriteBody(p []byte, _ bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.body = append(s.body, p...)
	return nil
}

func (s *pacedStream) EndResponse(stdhttp.Header, bool) error { return nil }

func (s *pacedStream) WriteBodyNoWait(p []byte) (bool, error) { return true, s.WriteBody(p, false) }

func (s *pacedStream) NotifyWritable(func(error)) bool { return s.writable }

func (s *pacedStream) SetReadDeadline(time.Time) error { return nil }

func (s *pacedStream) SetWriteDeadline(t time.Time) error {
	if t.IsZero() {
		return nil
	}
	return s.deadlineErr
}

// dialed returns a connection of a pool to a fake application.
func dialed(t *testing.T) *conn {
	t.Helper()
	a := newFakeApp(t)
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	p := &pool{engine: engine, network: "tcp", addr: a.addr, connect: 5 * time.Second, maxConns: 1}
	got := make(chan *conn, 1)
	p.get(func(c *conn, err error) {
		if err != nil {
			t.Error(err)
		}
		got <- c
	})
	c := waitFor(t, got, "a connection")
	t.Cleanup(c.close)
	return c
}

// TestWriteHeld checks a response that has more than its limit queued for
// the client: the connection's reads held and let go once the client has
// room, and the exchange given up when the stall deadline cannot be set or
// the client has gone.
func TestWriteHeld(t *testing.T) {
	c := dialed(t)
	big := []byte(strings.Repeat("x", 64<<10)) // past what a first write holds back
	for _, tt := range []struct {
		name        string
		deadlineErr error
		writable    bool
		answered    bool
		want        error
		held        bool
	}{
		{"room already", nil, false, false, nil, false},
		{"waits for room", nil, true, false, nil, true},
		{"no deadline", errors.New("deadline refused"), false, false, errClientGone, true},
		{"client gone", nil, false, true, errClientGone, false},
	} {
		s := &pacedStream{deadlineErr: tt.deadlineErr, writable: tt.writable}
		r := httptest.NewRequest("GET", "/x.php", nil)
		var err error
		fibhttp.Serve(fibhttp.HandlerFunc(func(fc *fibhttp.Context) {
			if tt.answered {
				_ = fc.Respond(200, "text/plain", []byte("done"))
			}
			x := &exchange{h: &FastCGI{}, c: fc, conn: c}
			x.mu.Lock()
			err = x.write(big)
			held := x.held
			x.mu.Unlock()
			if held != tt.held {
				t.Errorf("%s: held %v", tt.name, held)
			}
		}), fibhttp.NewStreamContext(nil, r, s))
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", tt.name, err, tt.want)
		}
	}
}

// TestSendFails checks a request whose connection fails as its params or
// its body go out: 502.
func TestSendFails(t *testing.T) {
	h := &FastCGI{graph: nopHolder{}}
	for _, body := range []bool{false, true} {
		rec := fibtest.ServeRequest(fibhttp.HandlerFunc(func(fc *fibhttp.Context) {
			fc.Retain()
			x := &exchange{h: h, c: fc, timer: time.AfterFunc(time.Hour, func() {})}
			c := &conn{p: &pool{maxConns: 1}} // never connected: sending fails
			if body {
				x.conn = c
				x.body([]byte("data"), false, nil)
			} else {
				x.connected(c, nil)
			}
		}), httptest.NewRequest("GET", "/x.php", nil))
		if rec.Response.StatusCode != 502 {
			t.Errorf("body %v: %d", body, rec.Response.StatusCode)
		}
	}
}

// TestEndedTwice checks that an exchange already over ignores the end of
// the request and an answer that come after.
func TestEndedTwice(t *testing.T) {
	x := &exchange{done: true}
	x.end(true)
	x.answer(stdhttp.StatusServiceUnavailable)
}

// TestLookupsBusy checks try_files when the lookup pool cannot take the
// lookup: 503 at once for a full queue, and for one that waited too long.
func TestLookupsBusy(t *testing.T) {
	old := lookupPool()
	lookups = filepool.New(1, 1, 50*time.Millisecond)
	t.Cleanup(func() { lookups = old })

	running, unblock := make(chan struct{}), make(chan struct{})
	lookups.Submit(func() {
		close(running)
		<-unblock
	}, nil)
	<-running
	h := &FastCGI{TryFiles: []string{"{path}"}, Root: t.TempDir(), graph: nopHolder{}}
	// The first lookup queues, and the second finds the queue full.
	queued, refused := &fibtest.Recorder{}, &fibtest.Recorder{}
	fibhttp.Serve(h, fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "/x.php", nil), queued))
	fibhttp.Serve(h, fibhttp.NewStreamContext(nil, httptest.NewRequest("GET", "/y.php", nil), refused))
	if !refused.Wait(10*time.Second) || refused.Response.StatusCode != 503 {
		t.Errorf("queue full: %d", refused.Response.StatusCode)
	}
	time.Sleep(100 * time.Millisecond) // past the queue's timeout
	close(unblock)
	if !queued.Wait(10*time.Second) || queued.Response.StatusCode != 503 {
		t.Errorf("waited too long: %d", queued.Response.StatusCode)
	}
}

// TestPoolDialFails checks an address the engine refuses at once, and an
// idle timer that fires for a connection already taken.
func TestPoolDialFails(t *testing.T) {
	engine, stop, err := upstream.StartEngine()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	p := &pool{engine: engine, network: "tcp", addr: "no port", connect: time.Second, maxConns: 1}
	got := make(chan error, 1)
	p.get(func(_ *conn, err error) { got <- err })
	if err := waitFor(t, got, "the dial's error"); err == nil {
		t.Error("dialed a malformed address")
	}
	if p.open != 0 {
		t.Errorf("%d open", p.open)
	}
	(&conn{p: p}).idleTimeout()
}

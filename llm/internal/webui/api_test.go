package webui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/cookiejar"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/lesismal/fib"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/pool"
	"github.com/lesismal/fit/llm/internal/wire"
)

// startUI serves router's methods through a console on loopback, with
// extra endpoints of the test's own, and returns its base URL.
func startUI(t *testing.T, router *mesh.Router, p *pool.Pool, extra func(api *console.API)) string {
	t.Helper()
	engine, err := fib.New(fib.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = engine.Run(); close(done) }()
	t.Cleanup(func() { engine.Stop(); <-done; _ = engine.Close() })
	hash, _ := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	c, err := console.Start(engine, console.Options{
		Config: &config.RoleConsole{Address: "127.0.0.1:0", MaxConns: 64,
			Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}}},
		Cookie: "fit_webui_test",
		Pages:  fstest.MapFS{"index.html": {Data: []byte("<p>")}},
		Routes: func(api *console.API) {
			Register(api, Options{Router: router, Pool: p, Me: func() map[string]any { return map[string]any{"role": "test"} }})
			if extra != nil {
				extra(api)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Stop(context.Background()) })
	return strings.TrimSuffix(c.URL(), "/")
}

type browser struct {
	t    *testing.T
	c    *stdhttp.Client
	base string
	csrf string
}

func login(t *testing.T, base string) *browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	b := &browser{t: t, c: &stdhttp.Client{Jar: jar, Timeout: 30 * time.Second}, base: base}
	status, body := b.do("POST", "/api/login", `{"username":"admin","password":"secret"}`)
	if status != 200 {
		t.Fatalf("login: %d %s", status, body)
	}
	var s struct{ CSRF string }
	_ = json.Unmarshal([]byte(body), &s)
	b.csrf = s.CSRF
	return b
}

// open sends a request and returns the response, its body unread.
func (b *browser) open(method, path, body string) *stdhttp.Response {
	b.t.Helper()
	req, _ := stdhttp.NewRequest(method, b.base+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(console.CSRFHeader, b.csrf)
	resp, err := b.c.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	return resp
}

func (b *browser) do(method, path, body string) (int, string) {
	b.t.Helper()
	resp := b.open(method, path, body)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data)
}

type echoReq struct {
	Say string `json:"say"`
}

func TestRPC(t *testing.T) {
	r := mesh.NewRouter()
	mesh.Handle(r, "/t/echo", nil, 0, func(_ context.Context, _ model.Identity, req *echoReq) (any, error) {
		return req, nil
	})
	mesh.Handle(r, "/t/list", nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return []int{1}, nil
	})
	mesh.Handle(r, "/t/conflict", nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return nil, mesh.Errorf(model.CodeConflict, "taken")
	})
	mesh.Handle(r, "/t/timeout", nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		return nil, context.DeadlineExceeded
	})
	p := pool.New(2, 8)
	t.Cleanup(func() { p.Stop(context.Background()) })
	b := login(t, startUI(t, r, p, nil))

	for _, c := range []struct {
		path, body string
		status     int
		want       string
	}{
		{"/api/me", "", 200, `"role":"test"`},
		{"/api/rpc/t/echo", `{"say":"hi"}`, 200, `{"say":"hi"}`},
		{"/api/rpc/t/echo", "  ", 200, `{"say":""}`},
		{"/api/rpc/t/echo", "{", 400, `"code":1000`},
		{"/api/rpc/t/echo", `{"say":1}`, 400, `"code":1000`},
		{"/api/rpc/t/list", "", 200, `[1]`},
		{"/api/rpc/t/conflict", "", 409, `"error":"taken","code":3001`},
		{"/api/rpc/t/timeout", "", 504, `"error":"timeout"`},
	} {
		method := "POST"
		if c.path == "/api/me" {
			method = "GET"
		}
		status, body := b.do(method, c.path, c.body)
		if status != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s %q: %d %s", c.path, c.body, status, body)
		}
	}
}

func TestRPCBusy(t *testing.T) {
	r := mesh.NewRouter()
	ran := atomic.Bool{}
	mesh.Handle(r, "/t/x", nil, 0, func(context.Context, model.Identity, *struct{}) (any, error) {
		ran.Store(true)
		return "x", nil
	})
	// One worker, held, and no room to wait: every method is refused.
	p := pool.New(1, 0)
	hold := make(chan struct{})
	for p.Go(func() { <-hold }) != nil {
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() { close(hold); p.Stop(context.Background()) })
	b := login(t, startUI(t, r, p, nil))
	if status, body := b.do("POST", "/api/rpc/t/x", ""); status != 503 || !strings.Contains(body, `"code":4003`) {
		t.Fatalf("busy: %d %s", status, body)
	}
	if ran.Load() {
		t.Fatal("a refused method ran")
	}
}

func TestStatusOf(t *testing.T) {
	for code, want := range map[int]int{
		model.CodeOK: 200, model.CodeBadRequest: 400, model.CodeUnauthorized: 401, model.CodeForbidden: 403,
		model.CodeNotFound: 404, model.CodeConflict: 409, model.CodeRateLimited: 429, model.CodeUnavailable: 503,
		model.CodeInternal: 500, 12345: 500,
	} {
		if got := StatusOf(code); got != want {
			t.Errorf("StatusOf(%d) = %d, want %d", code, got, want)
		}
	}
}

// events reads Server-Sent Events: name ("" for data) and data.
func events(t *testing.T, r io.Reader) [][2]string {
	t.Helper()
	var out [][2]string
	var name string
	var data []string
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			out = append(out, [2]string{name, strings.Join(data, "\n")})
			name, data = "", nil
		case strings.HasPrefix(line, "event: "):
			name = line[len("event: "):]
		case strings.HasPrefix(line, "data: "):
			data = append(data, line[len("data: "):])
		}
	}
	return out
}

func TestStream(t *testing.T) {
	r := mesh.NewRouter()
	mesh.HandleStream(r, "/s/ok", nil, func(ctx context.Context, _ model.Identity, _ *struct{}, st *wire.Stream) (any, error) {
		for _, f := range []string{"a", "", "x\ny"} {
			if err := st.Send(ctx, []byte(f)); err != nil {
				return nil, err
			}
		}
		return map[string]int{"n": 3}, nil
	})
	mesh.HandleStream(r, "/s/refuse", nil, func(context.Context, model.Identity, *struct{}, *wire.Stream) (any, error) {
		return nil, mesh.Errorf(model.CodeForbidden, "not you")
	})
	mesh.HandleStream(r, "/s/late", nil, func(ctx context.Context, _ model.Identity, _ *struct{}, st *wire.Stream) (any, error) {
		if err := st.Send(ctx, []byte(`"one"`)); err != nil {
			return nil, err
		}
		return nil, mesh.Errorf(model.CodeConflict, "gone wrong")
	})
	p := pool.New(2, 8)
	t.Cleanup(func() { p.Stop(context.Background()) })
	b := login(t, startUI(t, r, p, nil))

	resp := b.open("POST", "/api/stream/s/ok", "")
	got := events(t, resp.Body)
	resp.Body.Close()
	want := [][2]string{{"", "a"}, {"", "null"}, {"", "x\ny"}, {"end", `{"n":3}`}}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" || !equal(got, want) {
		t.Fatalf("ok: %d %v", resp.StatusCode, got)
	}
	if status, body := b.do("POST", "/api/stream/s/refuse", ""); status != 403 || !strings.Contains(body, `"error":"not you"`) {
		t.Fatalf("refused before any frame: %d %s", status, body)
	}
	resp = b.open("POST", "/api/stream/s/late", "")
	got = events(t, resp.Body)
	resp.Body.Close()
	if want := [][2]string{{"", `"one"`}, {"error", `{"code":3001,"message":"gone wrong"}`}}; !equal(got, want) {
		t.Fatalf("error after a frame: %v", got)
	}
	if status, body := b.do("POST", "/api/stream/s/ok", "{"); status != 400 || !strings.Contains(body, "not JSON") {
		t.Fatalf("bad body: %d %s", status, body)
	}
}

func equal(a, b [][2]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStreamLimit(t *testing.T) {
	r := mesh.NewRouter()
	var open atomic.Int32
	mesh.HandleStream(r, "/s/wait", nil, func(ctx context.Context, _ model.Identity, _ *struct{}, st *wire.Stream) (any, error) {
		open.Add(1)
		defer open.Add(-1)
		if err := st.Send(ctx, []byte("1")); err != nil {
			return nil, err
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	p := pool.New(2, 8)
	t.Cleanup(func() { p.Stop(context.Background()) })
	b := login(t, startUI(t, r, p, nil))

	var resps []*stdhttp.Response
	for range MaxStreams {
		resp := b.open("POST", "/api/stream/s/wait", "")
		if resp.StatusCode != 200 {
			t.Fatalf("stream %d: %d", len(resps), resp.StatusCode)
		}
		resps = append(resps, resp)
	}
	if status, body := b.do("POST", "/api/stream/s/wait", ""); status != 429 || !strings.Contains(body, `"code":4000`) {
		t.Fatalf("one too many: %d %s", status, body)
	}
	// Closing a browser's connection cancels its stream, which frees its place.
	for _, resp := range resps {
		resp.Body.Close()
	}
	b.c.CloseIdleConnections()
	deadline := time.Now().Add(10 * time.Second)
	for open.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("%d streams still open", open.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		resp := b.open("POST", "/api/stream/s/wait", "")
		resp.Body.Close()
		if resp.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no place freed: %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// bigStream is a stream method sending total bytes in frames of 64 KiB, and
// what the test sees of its progress.
type bigStream struct {
	total int
	sent  atomic.Int64
	ended chan error
}

func (bs *bigStream) run(ctx context.Context, _ model.Identity, _ *struct{}, st *wire.Stream) (any, error) {
	frame := bytes.Repeat([]byte("z"), 64<<10)
	var err error
	for int(bs.sent.Load()) < bs.total {
		if err = st.Send(ctx, frame); err != nil {
			break
		}
		bs.sent.Add(int64(len(frame)))
	}
	bs.ended <- err
	return "done", err
}

// stalled waits until bs stops sending: the browser holds it back, or it
// sent everything. A slow machine may make it look stalled too early; the
// test reads the rest either way.
func (bs *bigStream) stalled(t *testing.T) {
	t.Helper()
	last, still := int64(-1), 0
	for still < 6 && int(bs.sent.Load()) < bs.total {
		time.Sleep(50 * time.Millisecond)
		if n := bs.sent.Load(); n == last {
			still++
		} else {
			last, still = n, 0
		}
	}
}

func TestStreamSlowBrowser(t *testing.T) {
	r := mesh.NewRouter()
	bs := &bigStream{total: 64 << 20, ended: make(chan error, 2)}
	mesh.HandleStream(r, "/s/big", nil, bs.run)
	p := pool.New(2, 8)
	t.Cleanup(func() { p.Stop(context.Background()) })
	b := login(t, startUI(t, r, p, nil))

	// A browser that reads only once the stream is held back gets it all.
	resp := b.open("POST", "/api/stream/s/big", "")
	bs.stalled(t)
	if int(bs.sent.Load()) >= bs.total {
		t.Logf("the stream was never held back")
	}
	n, end := 0, ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if d, ok := strings.CutPrefix(line, "data: "); ok && strings.HasPrefix(d, "z") {
			n += len(d)
		} else if ok {
			end = d
		}
	}
	resp.Body.Close()
	if n != bs.total || end != `"done"` {
		t.Fatalf("read %d of %d bytes, end %q", n, bs.total, end)
	}
	if err := <-bs.ended; err != nil {
		t.Fatal(err)
	}

	// A browser that goes away while the stream is held back cancels it.
	bs.sent.Store(0)
	resp = b.open("POST", "/api/stream/s/big", "")
	bs.stalled(t)
	held := int(bs.sent.Load()) < bs.total
	resp.Body.Close()
	b.c.CloseIdleConnections()
	select {
	case err := <-bs.ended:
		if held && err == nil {
			t.Fatal("a stream held back ended well after its browser went away")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the stream went on after its browser went away")
	}
}

// TestSSEEnds covers what a stream from OpenLocal never gives: an empty
// result, and a frame after the end.
func TestSSEEnds(t *testing.T) {
	d, _, closeFn := wire.Pipe(wire.Options{}, wire.Handlers{}, wire.Handlers{
		Stream: func(context.Context, *wire.Stream) ([]byte, error) { return nil, nil },
	})
	t.Cleanup(closeFn)
	var (
		mu   sync.Mutex
		last *sse
		done = make(chan struct{}, 1)
	)
	extra := func(api *console.API) {
		api.Handle("POST", "/api/test/empty", func(r *console.Request) {
			st, err := d.OpenStream(context.Background(), wire.Head{Method: "/x"}, nil)
			if err != nil {
				Fail(r.Context, err)
				return
			}
			r.Context.Retain()
			s := &sse{c: r.Context, st: st, done: func() { done <- struct{}{} }}
			mu.Lock()
			last = s
			mu.Unlock()
			st.OnData(s.frame)
		})
	}
	p := pool.New(1, 1)
	t.Cleanup(func() { p.Stop(context.Background()) })
	b := login(t, startUI(t, mesh.NewRouter(), p, extra))
	resp := b.open("POST", "/api/test/empty", "")
	got := events(t, resp.Body)
	resp.Body.Close()
	if want := [][2]string{{"end", "null"}}; resp.StatusCode != 200 || !equal(got, want) {
		t.Fatalf("empty result: %d %v", resp.StatusCode, got)
	}
	<-done
	mu.Lock()
	s := last
	mu.Unlock()
	// Ended: a late frame is only released, and the response left alone.
	s.frame([]byte("late"), nil)
	s.frame(nil, errors.New("late"))
}

// TestSSEGone covers a frame arriving once the browser has gone: it cannot
// be written, and the stream is canceled.
func TestSSEGone(t *testing.T) {
	next, ended := make(chan struct{}), make(chan error, 1)
	d, _, closeFn := wire.Pipe(wire.Options{}, wire.Handlers{}, wire.Handlers{
		Stream: func(ctx context.Context, st *wire.Stream) ([]byte, error) {
			if err := st.Send(ctx, []byte("1")); err != nil {
				return nil, err
			}
			<-next
			err := st.Send(ctx, []byte("2"))
			if err == nil {
				// The second frame is in flight; the end waits for the cancel.
				<-st.Done()
			}
			ended <- err
			return nil, err
		},
	})
	t.Cleanup(closeFn)
	gone, done := make(chan struct{}), make(chan struct{})
	extra := func(api *console.API) {
		api.Handle("POST", "/api/test/gone", func(r *console.Request) {
			st, err := d.OpenStream(context.Background(), wire.Head{Method: "/x"}, nil)
			if err != nil {
				Fail(r.Context, err)
				return
			}
			r.Context.Retain()
			s := &sse{c: r.Context, st: st, done: func() { close(done) }}
			r.Context.OnCancel(func(error) { close(gone) })
			st.OnData(s.frame)
		})
	}
	p := pool.New(1, 1)
	t.Cleanup(func() { p.Stop(context.Background()) })
	b := login(t, startUI(t, mesh.NewRouter(), p, extra))
	resp := b.open("POST", "/api/test/gone", "")
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != "data: 1\n" {
		t.Fatalf("first event: %q %v", line, err)
	}
	resp.Body.Close()
	b.c.CloseIdleConnections()
	select {
	case <-gone:
	case <-time.After(10 * time.Second):
		t.Fatal("the browser's going was not noticed")
	}
	close(next)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end")
	}
	<-ended
}

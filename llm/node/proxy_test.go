package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// fakeUpstream is an OpenAI-like upstream: the checks' GET .../models get an
// empty list; /echo/ answers with the body it got,
// /hang/ waits until the test ends or the caller leaves, /big/ streams
// bigSize bytes, /slow/ reads the body only after a pause.
type fakeUpstream struct {
	*httptest.Server
	entered chan string
	release chan struct{}
}

const bigSize = 4 << 20

func newUpstream(t *testing.T) *fakeUpstream {
	u := &fakeUpstream{entered: make(chan string, 16), release: make(chan struct{})}
	u.Server = httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"): // the node's checks
			_, _ = w.Write([]byte(`{"data":[]}`))
		case strings.HasPrefix(r.URL.Path, "/hang/"):
			u.entered <- r.URL.Path
			select {
			case <-u.release:
			case <-r.Context().Done():
			}
		case strings.HasPrefix(r.URL.Path, "/big/"):
			w.Header().Set("Content-Type", "text/event-stream")
			chunk := bytes.Repeat([]byte("x"), 32<<10)
			for sent := 0; sent < bigSize; sent += len(chunk) {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(stdhttp.Flusher).Flush()
			}
		case strings.HasPrefix(r.URL.Path, "/slow/"):
			time.Sleep(300 * time.Millisecond)
			n, _ := io.Copy(io.Discard, r.Body)
			_, _ = w.Write([]byte(strings.Repeat("y", int(n%7))))
		default:
			b, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(func() {
		close(u.release)
		u.Close()
	})
	return u
}

func openProxy(ctx context.Context, n *Node, req model.ProxyReq) (*wire.Stream, error) {
	raw, _ := json.Marshal(req)
	return n.Router.OpenLocal(ctx, client, model.MethodNodeProxy, raw)
}

// readProxy reads a proxy stream to its end: the head, then the body.
func readProxy(ctx context.Context, st *wire.Stream) (head model.ProxyHead, body []byte, err error) {
	first, err := st.Recv(ctx)
	if err != nil {
		return head, nil, err
	}
	if err := json.Unmarshal(first, &head); err != nil {
		return head, nil, err
	}
	for {
		b, err := st.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return head, body, nil
		}
		if err != nil {
			return head, body, err
		}
		body = append(body, b...)
	}
}

// doProxy runs one call with a body in the request.
func doProxy(t *testing.T, n *Node, req model.ProxyReq) (model.ProxyHead, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if req.API == "" {
		req.API = model.APIOpenAI
	}
	if req.Path == "" {
		req.Path = "chat/completions"
	}
	st, err := openProxy(ctx, n, req)
	if err != nil {
		return model.ProxyHead{}, nil, err
	}
	return readProxy(ctx, st)
}

// streamProxy runs one call whose body follows in frames.
func streamProxy(t *testing.T, n *Node, req model.ProxyReq, frames ...[]byte) (model.ProxyHead, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req.API, req.Path, req.BodyStream = model.APIOpenAI, "chat/completions", true
	st, err := openProxy(ctx, n, req)
	if err != nil {
		return model.ProxyHead{}, nil, err
	}
	go func() {
		for _, f := range frames {
			if st.Send(ctx, f) != nil {
				return
			}
		}
		_ = st.CloseSend()
	}()
	return readProxy(ctx, st)
}

func idle(t *testing.T, n *Node) {
	t.Helper()
	waitFor(t, "no call in flight", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.total == 0
	})
}

func TestProxyRefusals(t *testing.T) {
	u := newUpstream(t)
	n := startNode(t, &config.LLMNode{
		Proxy: &config.LLMNodeProxy{MaxBodyBytes: 64},
		Models: []config.LLMModel{
			{Name: "m1", API: model.APIOpenAI, BaseURL: u.URL + "/echo"},
			{Name: "off", API: model.APIOpenAI, BaseURL: u.URL, Disabled: true},
			{Name: "up", API: model.APIOpenAI, BaseURL: u.URL + "/echo", Upstream: "real-id"},
			{Name: "bad", API: model.APIOpenAI, BaseURL: "http://a b"},
			{Name: "gone", API: model.APIOpenAI, BaseURL: "http://127.0.0.1:1"},
		},
	}, nil)
	// The check of bad fails at once; with its error cleared, the call fails
	// to make its request.
	waitFor(t, "the check of bad", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		r := n.rt[key("bad", model.APIOpenAI)]
		return r != nil && r.err != ""
	})
	n.setModelErr(model.Model{Name: "bad", API: model.APIOpenAI}, "", false)
	for _, tc := range []struct {
		req  model.ProxyReq
		code int
		msg  string
	}{
		{model.ProxyReq{Model: "m1", Path: "admin"}, model.CodeBadRequest, "not allowed"},
		{model.ProxyReq{Model: "off"}, model.CodeUnavailable, "disabled"},
		{model.ProxyReq{Model: "up", Body: []byte("[1]")}, model.CodeBadRequest, "JSON object"},
		{model.ProxyReq{Model: "bad", Body: []byte("{}")}, model.CodeUnavailable, "model bad"},
		{model.ProxyReq{Model: "gone", Body: []byte(`{"model":"gone"}`)}, model.CodeUnavailable, "upstream unreachable"},
	} {
		_, _, err := doProxy(t, n, tc.req)
		var we *wire.Error
		if !errors.As(err, &we) || we.Code != tc.code || !strings.Contains(we.Msg, tc.msg) {
			t.Fatalf("%+v: %v", tc.req, err)
		}
	}
	// A model in error is not served until its error clears.
	m1 := model.Model{Name: "m1", API: model.APIOpenAI}
	n.setModelErr(m1, "down", false)
	if _, _, err := doProxy(t, n, model.ProxyReq{Model: "m1"}); code(err) != model.CodeUnavailable {
		t.Fatalf("model in error: %v", err)
	}
	n.setModelErr(m1, "", false)
	// A model without runtime state gets it.
	n.mu.Lock()
	delete(n.rt, key("m1", model.APIOpenAI))
	n.mu.Unlock()
	if _, body, err := doProxy(t, n, model.ProxyReq{Model: "m1", Body: []byte(`{"model":"m1"}`)}); err != nil || string(body) != `{"model":"m1"}` {
		t.Fatalf("echo = %q %v", body, err)
	}

	// Streamed bodies: one held whole to set its model, past the bound or
	// not an object, and one passed through, past the bound.
	if _, body, err := streamProxy(t, n, model.ProxyReq{Model: "up"}, []byte(`{"model":`), []byte(`"up"}`)); err != nil || !strings.Contains(string(body), `"real-id"`) {
		t.Fatalf("rewritten stream = %q %v", body, err)
	}
	if _, _, err := streamProxy(t, n, model.ProxyReq{Model: "up"}, []byte("[1]")); code(err) != model.CodeBadRequest {
		t.Fatalf("streamed array: %v", err)
	}
	big := bytes.Repeat([]byte("z"), 40)
	if _, _, err := streamProxy(t, n, model.ProxyReq{Model: "up"}, big, big); code(err) != model.CodeBadRequest {
		t.Fatalf("streamed past the bound, held: %v", err)
	}
	// Passed through, past the bound: the caller's bad request, which
	// leaves the model as it was rather than in error.
	for range 5 {
		if _, _, err := streamProxy(t, n, model.ProxyReq{Model: "m1"}, big, big); code(err) != model.CodeBadRequest {
			t.Fatalf("streamed past the bound, passed through: %v", err)
		}
		n.mu.Lock()
		msg := n.rt[key("m1", model.APIOpenAI)].err
		n.mu.Unlock()
		if msg != "" {
			t.Fatalf("model in error after a body past the bound: %s", msg)
		}
	}
	idle(t, n)
}

func TestProxyBusyAndRemoved(t *testing.T) {
	u := newUpstream(t)
	member := func(models ...config.LLMModel) *config.LLMNode {
		return &config.LLMNode{ID: "node-t", Proxy: &config.LLMNodeProxy{MaxInflight: 1}, Models: models}
	}
	hang := config.LLMModel{Name: "hang", API: model.APIOpenAI, BaseURL: u.URL + "/hang"}
	echo := config.LLMModel{Name: "m1", API: model.APIOpenAI, BaseURL: u.URL + "/echo"}
	n := startNode(t, member(hang, echo), nil)
	errc := make(chan error, 1)
	go func() {
		_, _, err := doProxy(t, n, model.ProxyReq{Model: "hang", Body: []byte("{}")})
		errc <- err
	}()
	<-u.entered
	if _, _, err := doProxy(t, n, model.ProxyReq{Model: "m1", Body: []byte("{}")}); code(err) != model.CodeUnavailable {
		t.Fatalf("node busy: %v", err)
	}
	// The model goes while a call to it is in flight: its state goes with
	// the call.
	if err := n.Apply(member(echo)); err != nil {
		t.Fatal(err)
	}
	u.release <- struct{}{}
	if err := <-errc; err != nil {
		t.Fatalf("the call in flight: %v", err)
	}
	idle(t, n)
	n.mu.Lock()
	_, kept := n.rt[key("hang", model.APIOpenAI)]
	n.mu.Unlock()
	if kept {
		t.Fatal("the state of a removed model was kept")
	}
}

func TestProxyTimeoutAndCancel(t *testing.T) {
	u := newUpstream(t)
	n := startNode(t, &config.LLMNode{
		Proxy:  &config.LLMNodeProxy{Timeout: config.Duration(200 * time.Millisecond)},
		Models: []config.LLMModel{{Name: "hang", API: model.APIOpenAI, BaseURL: u.URL + "/hang"}},
	}, nil)
	// The whole call is bounded.
	if _, _, err := doProxy(t, n, model.ProxyReq{Model: "hang", Body: []byte("{}")}); err == nil {
		t.Fatal("a call past its timeout succeeded")
	}
	<-u.entered
	idle(t, n)

	// The caller leaves while its streamed body is on its way and the
	// upstream has not answered.
	n = startNode(t, &config.LLMNode{ID: "node-t2",
		Models: []config.LLMModel{{Name: "hang", API: model.APIOpenAI, BaseURL: u.URL + "/hang"}}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	st, err := openProxy(ctx, n, model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "hang", BodyStream: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctx, []byte(`{"model":`)); err != nil {
		t.Fatal(err)
	}
	<-u.entered
	cancel()
	idle(t, n)
}

func TestProxyCallerCancelsWhileHeld(t *testing.T) {
	u := newUpstream(t)
	n := startNode(t, &config.LLMNode{Models: []config.LLMModel{
		{Name: "m1", API: model.APIOpenAI, BaseURL: u.URL + "/echo"},
		{Name: "up", API: model.APIOpenAI, BaseURL: u.URL + "/echo", Upstream: "real-id"},
	}}, nil)
	// A body held whole to set its model: the caller leaves before its end.
	ctx, cancel := context.WithCancel(context.Background())
	st, err := openProxy(ctx, n, model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "up", BodyStream: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Send(ctx, []byte(`{"model":`)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the call", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.total == 1
	})
	cancel()
	idle(t, n)
}

// A large response to a caller that reads slowly is held against its window
// and goes on as it reads.
func TestProxySlowReader(t *testing.T) {
	u := newUpstream(t)
	n := startNode(t, &config.LLMNode{Models: []config.LLMModel{{Name: "big", API: model.APIOpenAI, BaseURL: u.URL + "/big"}}}, nil)
	for _, pause := range []time.Duration{200 * time.Millisecond, 0} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		st, err := openProxy(ctx, n, model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "big", Body: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(pause)
		var got int
		first := true
		for {
			b, err := st.Recv(ctx)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if first {
				first = false
				continue
			}
			got += len(b)
			if got%(512<<10) < len(b) {
				time.Sleep(5 * time.Millisecond) // read in fits and starts
			}
		}
		cancel()
		var res model.ProxyResult
		if err := json.Unmarshal(st.Result(), &res); err != nil || got != bigSize || res.Bytes != bigSize {
			t.Fatalf("got %d bytes, result %+v %v", got, res, err)
		}
	}
	// The caller leaves in the middle of the body.
	ctx, cancel := context.WithCancel(context.Background())
	st, err := openProxy(ctx, n, model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "big", Body: []byte("{}")})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := st.Recv(ctx); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	idle(t, n)
}

// A large streamed body to an upstream slow to read it is held against the
// caller's window.
func TestProxySlowUpstreamReader(t *testing.T) {
	u := newUpstream(t)
	n := startNode(t, &config.LLMNode{Models: []config.LLMModel{{Name: "slow", API: model.APIOpenAI, BaseURL: u.URL + "/slow"}}}, nil)
	frame := bytes.Repeat([]byte("b"), 64<<10)
	frames := make([][]byte, 128) // 8 MiB
	for i := range frames {
		frames[i] = frame
	}
	head, _, err := streamProxy(t, n, model.ProxyReq{Model: "slow", BodySize: int64(len(frame) * len(frames))}, frames...)
	if err != nil || head.Status != 200 {
		t.Fatalf("slow upstream: %+v %v", head, err)
	}
	idle(t, n)
}

// A caller that leaves while the node holds the upstream body for want of
// credit, with nothing of it pending, ends the exchange: the stream's end
// runs the credit callback, which no credit will ever follow.
func TestProxyCallerLeavesWhileHeld(t *testing.T) {
	u := newUpstream(t)
	n := startNode(t, &config.LLMNode{Models: []config.LLMModel{{Name: "big", API: model.APIOpenAI, BaseURL: u.URL + "/big"}}}, nil)
	for range 3 {
		ctx, cancel := context.WithCancel(context.Background())
		st, err := openProxy(ctx, n, model.ProxyReq{API: model.APIOpenAI, Path: "chat/completions", Model: "big", Body: []byte("{}")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Recv(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond) // the window fills, and the node holds
		cancel()
		idle(t, n)
	}
}

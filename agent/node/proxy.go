package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/lesismal/fib"
	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
	"github.com/lesismal/fit/app"
)

type fibclientResponse = fibhttp.ClientResponse

// The bounds of the node's HTTP clients.
const (
	maxConnsPerHost    = 64
	maxPendingPerHost  = 256
	maxStreamedBody    = 1 << 30
	maxWholeBody       = 8 << 20
	streamThreshold    = 64 << 10
	maxProbeBody       = 64 << 10
	maxIdleConnPerHost = 16
)

// errIdle cuts a call whose upstream went silent mid-response.
var errIdle = errors.New("the upstream went silent in the middle of its response")

// newClients returns the node's HTTP clients, on the engine fit's upstream
// pools dial through: one for the calls, whose bodies stream both ways, and
// one for the checks. Each call sets its own timeouts.
func newClients(h *app.Host, connect func() time.Duration) (calls, probes *fibhttp.Client) {
	engine := h.Upstream
	if engine == nil {
		engine = h.Engine
	}
	cc := fibhttp.DefaultClientConfig()
	cc.Timeout = 0 // each call's own, through its Call
	cc.StreamResponseBody = true
	cc.StreamResponseBodyThreshold = streamThreshold
	cc.StreamUnknownLengthAtHeader = true
	cc.MaxStreamedBodyBytes = maxStreamedBody
	cc.MaxResponseBodyBytes = maxWholeBody
	cc.MaxConnsPerHost = maxConnsPerHost
	cc.MaxPendingPerHost = maxPendingPerHost
	cc.MaxIdleConnsPerHost = maxIdleConnPerHost
	// Each connect is bounded by the proxy's connect_timeout as it is now.
	cc.Dial = func(e *fib.Engine, network, addr string, _ time.Duration, h fib.Handler, done func(*fib.Connection, error)) error {
		return e.DialWithHandler(network, addr, connect(), h, done)
	}
	cc.ResponseHeaderTimeout = 0
	calls = fibhttp.NewClient(engine, cc)
	pc := fibhttp.DefaultClientConfig()
	pc.Timeout = checkTimeout
	pc.MaxResponseBodyBytes = maxProbeBody
	pc.MaxConnsPerHost = 4
	pc.MaxPendingPerHost = 16
	pc.MaxIdleConnsPerHost = 2
	probes = fibhttp.NewClient(engine, pc)
	return calls, probes
}

func unwrapURLErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func unavailable(format string, args ...any) error {
	return mesh.Errorf(model.CodeUnavailable, format, args...)
}

// acquire takes an inflight slot of the ready model (name, api), within the
// node's bound on calls.
func (n *Node) acquire(name, api string) (model.Model, *modelRT, limits, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	lim := n.lim
	i := slices.IndexFunc(n.models, func(m model.Model) bool { return m.Name == name && m.API == api })
	if i < 0 {
		return model.Model{}, nil, lim, unavailable("model %s (%s) is not served by this node", name, api)
	}
	m := n.models[i]
	r := n.rt[key(m.Name, m.API)]
	if r == nil {
		r = &modelRT{}
		n.rt[key(m.Name, m.API)] = r
	}
	switch {
	case m.Disabled:
		return m, nil, lim, unavailable("model %s is disabled", name)
	case r.err != "":
		return m, nil, lim, unavailable("model %s: %s", name, r.err)
	case m.MaxInflight > 0 && r.inflight >= m.MaxInflight:
		return m, nil, lim, unavailable("model %s is busy (%d in flight)", name, r.inflight)
	case n.total >= lim.inflight:
		return m, nil, lim, unavailable("the node is busy (%d calls in flight)", n.total)
	}
	r.inflight++
	n.total++
	return m, r, lim, nil
}

func (n *Node) release(m model.Model, r *modelRT) {
	n.mu.Lock()
	r.inflight--
	n.total--
	k := key(m.Name, m.API)
	if r.inflight == 0 && n.rt[k] == r && !slices.ContainsFunc(n.models, func(x model.Model) bool { return x.Name == m.Name && x.API == m.API }) {
		delete(n.rt, k)
	}
	n.mu.Unlock()
	n.changed()
}

// rewriteModel sets the top-level "model" of a JSON object body to upstream.
// Every other member keeps its raw bytes; only their order may change.
func rewriteModel(body []byte, upstream string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, errors.New("the request body must be a JSON object")
	}
	v, _ := json.Marshal(upstream)
	obj["model"] = v
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func sendJSON(ctx context.Context, st *wire.Stream, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return st.Send(ctx, b)
}

// proxy serves model.MethodNodeProxy: it forwards one agent call to the
// model's upstream on fib's asynchronous client and streams the response
// back as it arrives. The request body comes in the request, or, with
// BodyStream, as the opener's frames, which go upstream as they arrive,
// through a BodyWriter, held against the stream's window while the upstream
// is slow to take them; it is held whole only when the model's upstream ID
// differs from its name, to set the model it names, within the node's
// max_body_bytes. The response's head goes first, then its body, a frame
// per piece, held with HoldBody while the caller has granted no credit. The
// handler's goroutine only waits for the end; nothing waits on a fib worker.
// Every failure before the head frame that another node might not have is
// CodeUnavailable.
func (n *Node) proxy(ctx context.Context, caller model.Identity, req *model.ProxyReq, st *wire.Stream) (any, error) {
	start := time.Now()
	if !slices.Contains(model.ProxyPaths[req.API], req.Path) {
		return nil, mesh.BadRequest("path %q is not allowed for api %q", req.Path, req.API)
	}
	m, r, lim, err := n.acquire(req.Model, req.API)
	if err != nil {
		return nil, err
	}
	n.changed()
	defer n.release(m, r)
	if int64(len(req.Body)) > lim.body || req.BodySize > lim.body {
		return nil, mesh.Errorf(model.CodeBadRequest, "the request body is larger than the node's %d bytes", lim.body)
	}
	upstream := m.Upstream
	if upstream == "" {
		upstream = m.Name
	}
	// ProxyReq.Model is the body's model, so an equal upstream ID leaves
	// the body untouched, and a streamed one streams.
	rewrite := upstream != req.Model

	x := &exchange{n: n, st: st, lim: lim, done: make(chan struct{}),
		res: model.ProxyResult{Node: n.Self().ID, Model: m.Name}}
	hreq, err := stdhttp.NewRequest(stdhttp.MethodPost, m.BaseURL+"/"+req.Path, nil)
	if err != nil {
		return nil, unavailable("model %s: %v", m.Name, err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	setAuth(hreq.Header, m, req.Header)

	switch {
	case !req.BodyStream:
		body := req.Body
		if rewrite {
			if body, err = rewriteModel(body, upstream); err != nil {
				return nil, mesh.BadRequest("%v", err)
			}
		}
		hreq.Body, hreq.ContentLength = fibhttp.NewBufferedBody(body), int64(len(body))
	case rewrite:
		// The whole body first, within the bound, then the call.
		body, err := collect(ctx, st, lim.body)
		if err != nil {
			return nil, err
		}
		if body, err = rewriteModel(body, upstream); err != nil {
			return nil, mesh.BadRequest("%v", err)
		}
		hreq.Body, hreq.ContentLength = fibhttp.NewBufferedBody(body), int64(len(body))
	default:
		x.bw = fibhttp.NewBodyWriter(0)
		hreq.Body, hreq.ContentLength = x.bw, -1
		if req.BodySize > 0 {
			hreq.ContentLength = req.BodySize
		}
	}

	stopKeep := st.KeepAlive(0)
	x.stopKeep = stopKeep
	x.model = m
	call := n.client.Start(hreq, x.onResponse)
	x.mu.Lock()
	x.call = call
	x.mu.Unlock()
	select {
	case <-x.done: // cancelled before the call was there to cancel
		call.Cancel(context.Canceled)
	default:
	}
	if x.bw != nil {
		// Frames that came already wait in the stream, and come first.
		x.upload(x.bw)
	}
	x.arm(lim.firstByte)
	total := time.AfterFunc(lim.total, func() { x.cancel(context.DeadlineExceeded) })
	stopCtx := context.AfterFunc(ctx, func() { x.cancel(context.Cause(ctx)) })
	<-x.done
	stopCtx()
	total.Stop()
	x.stopTimers()

	x.mu.Lock()
	res, xerr := x.res, x.err
	x.mu.Unlock()
	res.DurationMs = time.Since(start).Milliseconds()
	n.log.Infof("proxy model=%s api=%s path=%s status=%d bytes=%d duration_ms=%d caller=%s:%s err=%v",
		m.Name, m.API, req.Path, res.Status, res.Bytes, res.DurationMs, caller.Role, caller.ID, xerr)
	if xerr != nil {
		return nil, xerr
	}
	return res, nil
}

// collect reads a streamed body whole, up to limit bytes.
func collect(ctx context.Context, st *wire.Stream, limit int64) ([]byte, error) {
	var buf []byte
	for {
		b, err := st.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return buf, nil
		}
		if err != nil {
			return nil, err
		}
		if int64(len(buf)+len(b)) > limit {
			return nil, mesh.Errorf(model.CodeBadRequest, "the request body is larger than the node's %d bytes", limit)
		}
		buf = append(buf, b...)
	}
}

// exchange is one call on its way upstream and back.
type exchange struct {
	n        *Node
	st       *wire.Stream
	lim      limits
	model    model.Model
	call     fibhttp.Call
	bw       *fibhttp.BodyWriter
	stopKeep func()

	mu      sync.Mutex
	resp    *fibhttp.ClientResponse
	timer   *time.Timer // first byte, then idle
	pending []byte      // a piece of the body waiting for credit
	held    bool
	res     model.ProxyResult
	err     error
	once    sync.Once
	done    chan struct{}
}

// upload forwards the stream's body frames into bw as they arrive.
func (x *exchange) upload(bw *fibhttp.BodyWriter) {
	var mu sync.Mutex
	var held int
	var total int64
	waiting := false
	x.st.OnData(func(data []byte, err error) {
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = bw.Close()
			} else {
				bw.CloseWithError(err)
			}
			return
		}
		total += int64(len(data))
		if total > x.lim.body {
			bw.CloseWithError(fmt.Errorf("the request body is larger than %d bytes", x.lim.body))
			x.cancel(mesh.Errorf(model.CodeBadRequest, "the request body is larger than the node's %d bytes", x.lim.body))
			return
		}
		if _, werr := bw.Write(data); werr != nil {
			x.st.Release(len(data))
			return // the request is over
		}
		mu.Lock()
		if !waiting && bw.Buffered() <= 256<<10 {
			mu.Unlock()
			x.st.Release(len(data))
			return
		}
		held += len(data)
		if waiting {
			mu.Unlock()
			return
		}
		waiting = true
		mu.Unlock()
		resume := func() {
			mu.Lock()
			n := held
			held, waiting = 0, false
			mu.Unlock()
			x.st.Release(n)
		}
		if !bw.OnDrain(resume) {
			resume()
		}
	})
}

// arm (re)starts the timer that cuts a call whose upstream is silent for d.
func (x *exchange) arm(d time.Duration) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.timer != nil {
		x.timer.Reset(d)
		return
	}
	x.timer = time.AfterFunc(d, func() { x.cancel(errIdle) })
}

func (x *exchange) stopTimers() {
	x.mu.Lock()
	if x.timer != nil {
		x.timer.Stop()
	}
	x.mu.Unlock()
	x.stopKeep()
}

// cancel abandons the upstream request for cause.
func (x *exchange) cancel(cause error) {
	if cause == nil {
		cause = context.Canceled
	}
	x.mu.Lock()
	resp, call := x.resp, x.call
	x.mu.Unlock()
	call.Cancel(cause)
	if resp != nil {
		resp.Close()
	}
	x.finish(nil, cause)
}

// finish ends the exchange once, with the result or the error.
func (x *exchange) finish(resp *fibhttp.ClientResponse, err error) {
	x.once.Do(func() {
		x.mu.Lock()
		if err != nil && x.err == nil {
			x.err = err
		}
		x.mu.Unlock()
		close(x.done)
	})
}

func (x *exchange) onResponse(resp *fibhttp.ClientResponse, err error) {
	if err != nil {
		if x.st.Context().Err() != nil {
			x.finish(nil, x.st.Context().Err())
			return
		}
		var me *wire.Error
		switch {
		case errors.As(err, &me):
			x.finish(nil, err)
		case errors.Is(err, errIdle):
			x.finish(nil, unavailable("model %s: the upstream did not answer in time", x.model.Name))
		default:
			msg := "upstream unreachable: " + unwrapURLErr(err).Error()
			x.n.setModelErr(x.model, msg, false)
			x.finish(nil, unavailable("model %s: %s", x.model.Name, msg))
		}
		return
	}
	x.mu.Lock()
	x.resp = resp
	x.res.Status = resp.StatusCode
	x.mu.Unlock()
	head := model.ProxyHead{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Node: x.res.Node}
	b, _ := json.Marshal(head)
	if ok, err := x.st.TrySend(b); err != nil || !ok {
		resp.Close()
		if err == nil {
			err = wire.ErrFlowControl
		}
		x.finish(nil, err)
		return
	}
	if resp.StatusCode < 500 {
		x.n.setModelErr(x.model, "", false) // the upstream answers
	}
	x.arm(x.lim.idle)
	resp.OnBody(x.onBody)
}

// onBody streams the response body to the caller, a frame per piece. It
// runs on the worker that reads the upstream, or on fib's side pool once a
// held body resumes, and never waits: with no credit left, the body is held
// until the caller grants more.
func (x *exchange) onBody(data []byte, fin bool, err error) {
	if err != nil {
		x.finish(nil, mesh.Errorf(model.CodeInternal, "upstream body: %v", err))
		return
	}
	x.mu.Lock()
	if x.timer != nil && !fin {
		x.timer.Reset(x.lim.idle)
	}
	resp := x.resp
	x.mu.Unlock()
	if len(data) > 0 {
		x.stopKeep()
		ok, serr := x.st.TrySend(data)
		if serr != nil {
			resp.Close()
			x.finish(nil, serr)
			return
		}
		x.mu.Lock()
		x.res.Bytes += int64(len(data))
		if !ok {
			// No credit: keep a copy, and the rest of the body waits.
			x.pending = append([]byte(nil), data...)
		}
		x.mu.Unlock()
		if !ok || !x.st.Credit() {
			x.hold(resp)
		}
	}
	if fin {
		x.mu.Lock()
		pending := x.pending != nil
		x.mu.Unlock()
		if !pending {
			x.finish(resp, nil)
		} else {
			x.mu.Lock()
			x.held = true // finish once the last piece has gone
			x.mu.Unlock()
		}
	}
}

// hold holds the upstream body until the caller grants credit.
func (x *exchange) hold(resp *fibhttp.ClientResponse) {
	resp.HoldBody(true)
	if !x.st.OnCredit(func() { x.resume(resp) }) {
		x.resume(resp)
	}
}

// resume sends what waits and lets the body go on.
func (x *exchange) resume(resp *fibhttp.ClientResponse) {
	x.mu.Lock()
	pending, ended := x.pending, x.held
	x.mu.Unlock()
	if pending != nil {
		ok, err := x.st.TrySend(pending)
		if err != nil {
			resp.Close()
			x.finish(nil, err)
			return
		}
		if !ok {
			if !x.st.OnCredit(func() { x.resume(resp) }) {
				x.resume(resp)
			}
			return
		}
		x.mu.Lock()
		x.pending = nil
		x.mu.Unlock()
	}
	if ended {
		x.finish(resp, nil)
		return
	}
	if !x.st.Credit() {
		if !x.st.OnCredit(func() { x.resume(resp) }) {
			x.resume(resp)
		}
		return
	}
	resp.HoldBody(false)
}

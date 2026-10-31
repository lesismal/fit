package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sort"
	"sync/atomic"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
)

// maxTries bounds the nodes one call is tried on.
const maxTries = 3

// proxy serves MethodHubProxy: it picks a node, opens its MethodNodeProxy
// with the same request and relays the frames both ways as they come: the
// request body the client streams, and the node's head and response body.
// Neither direction waits on a goroutine: each frame goes on while the far
// side has granted credit, and is held against the near side's window
// otherwise (wire.Pump). Until the node's head arrives, a CodeUnavailable
// error (or the node going offline) moves the call to the next node, as
// long as none of a streamed body has been taken from the client; after it,
// bytes have reached the client, so any failure ends the call.
func (h *Hub) proxy(ctx context.Context, _ model.Identity, req *model.ProxyReq, st *wire.Stream) (any, error) {
	if req.Model == "" || !slices.Contains(model.APIs, req.API) {
		return nil, mesh.BadRequest("model and a known api are required")
	}
	exclude := map[string]bool{}
	for _, id := range req.Exclude {
		exclude[id] = true
	}
	// Keep the client's stream alive while nodes are tried and the first
	// byte is awaited.
	stopKA := st.KeepAlive(0)
	defer stopKA()
	var lastErr error
	for range maxTries {
		id := h.pick(req.API, req.Model, exclude)
		if id == "" {
			break
		}
		exclude[id] = true
		res, retry, err := h.relay(ctx, id, req, st, stopKA)
		if !retry {
			return res, err
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, mesh.Errorf(model.CodeUnavailable, "no node could serve model %s: %v", req.Model, lastErr)
	}
	return nil, mesh.Errorf(model.CodeUnavailable, "no node serves model %s", req.Model)
}

type relayEnd struct {
	res   json.RawMessage
	err   error
	retry bool
}

// relay runs the call on node id; retry reports that nothing was relayed
// and another node may be tried.
func (h *Hub) relay(ctx context.Context, id string, req *model.ProxyReq, st *wire.Stream, headSent func()) (res any, retry bool, err error) {
	h.mu.Lock()
	h.inflight[id]++
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.inflight[id]--; h.inflight[id] <= 0 {
			delete(h.inflight, id)
		}
		h.mu.Unlock()
	}()

	nctx, cancel := context.WithCancel(ctx) // ending it cancels the node's stream
	defer cancel()
	nreq := *req
	nreq.Exclude = nil
	ns, err := h.m.OpenStream(nctx, id, model.MethodNodeProxy, nreq)
	if err != nil {
		return nil, ctx.Err() == nil, err
	}
	done := make(chan relayEnd, 1)
	var bodyTaken func() int
	if req.BodyStream {
		// The client's body goes on to the node; its end half-closes the
		// node's side.
		var taken atomic.Int64
		bodyTaken = func() int { return int(taken.Load()) }
		wire.Pump(st, ns, func() { taken.Store(1) }, func(err error, frames int) {
			if errors.Is(err, io.EOF) {
				_ = ns.CloseSend()
			}
		})
	}
	wire.Pump(ns, st, headSent, func(err error, frames int) {
		switch {
		case errors.Is(err, io.EOF) && frames == 0:
			done <- relayEnd{err: mesh.Errorf(model.CodeInternal, "node %s ended the call without a head", id)}
		case errors.Is(err, io.EOF):
			done <- relayEnd{res: json.RawMessage(ns.Result())}
		case frames == 0 && ctx.Err() == nil:
			// Nothing reached the client: another node may serve it, if
			// the node could not, or its link broke.
			var we *wire.Error
			retry := !errors.As(err, &we) || we.Code == model.CodeUnavailable
			done <- relayEnd{err: err, retry: retry}
		default:
			done <- relayEnd{err: err}
		}
	})
	var e relayEnd
	select {
	case e = <-done:
	case <-ctx.Done():
		ns.Cancel()
		e = <-done
	}
	if e.retry && bodyTaken != nil {
		ns.Cancel()
		// A body the client streamed cannot be sent again once any of it
		// was taken.
		if bodyTaken() > 0 {
			e.retry = false
		}
	}
	if e.err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, e.retry, e.err
	}
	return e.res, false, nil
}

// pick returns the online node serving (api, name) ready and not excluded
// with the least calls in flight: the model's inflight the node reported
// plus the streams this hub relays to it now; ties go round robin.
func (h *Hub) pick(api, name string, exclude map[string]bool) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	best, ties := -1, []string(nil)
	for id, n := range h.nodes {
		if exclude[id] || !h.m.Online(id) {
			continue
		}
		i := slices.IndexFunc(n.info.Models, func(mv model.ModelView) bool {
			return mv.Name == name && mv.API == api && mv.Ready()
		})
		if i < 0 {
			continue
		}
		load := n.info.Models[i].Inflight + h.inflight[id]
		switch {
		case best < 0 || load < best:
			best, ties = load, []string{id}
		case load == best:
			ties = append(ties, id)
		}
	}
	if len(ties) == 0 {
		return ""
	}
	sort.Strings(ties)
	h.rr++
	return ties[h.rr%len(ties)]
}

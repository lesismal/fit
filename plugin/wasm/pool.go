package wasm

import (
	"context"
	"io"
	stdhttp "net/http"
	"sync"
	"sync/atomic"
	"time"

	fibhttp "github.com/lesismal/fib/http"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"github.com/lesismal/fit/module"
)

// call is a request on its way through the plugin.
type call struct {
	m      *WASM
	c      *fibhttp.Context
	next   module.Handler
	queued time.Time
	// ops are the plugin's changes to the response's fields; status and
	// body its own answer, when it gave one.
	ops    []headerOp
	status int
	body   []byte
}

func (cl *call) release() {
	cl.c.Release()
	cl.m.graph.Release()
}

// pool runs the plugin's calls: a goroutine for each instance, taking calls
// from a bounded queue.
type pool struct {
	m       *WASM
	calls   chan *call
	workers []*worker
	stop    chan struct{}
	wg      sync.WaitGroup
	// logs counts the plugin's log lines this second, which are bounded.
	logs atomic.Int64
}

// worker is one instance of the plugin and the goroutine that runs its
// calls. current is the call it runs, for the host functions, and started
// when it began, for the watchdog: 0 while it runs none.
type worker struct {
	p       *pool
	mod     api.Module
	fn      api.Function
	ctx     context.Context
	cancel  context.CancelFunc
	stack   [1]uint64
	current *call
	started atomic.Int64
	// mu guards cancel, which the watchdog calls while the worker may be
	// making its instance again.
	mu sync.Mutex
}

type workerKey struct{}

func newPool(m *WASM) (*pool, error) {
	p := &pool{m: m, calls: make(chan *call, m.Queue), stop: make(chan struct{})}
	for range m.Instances {
		w := &worker{p: p}
		if err := w.instantiate(); err != nil {
			for _, w := range p.workers {
				w.close()
			}
			return nil, err
		}
		p.workers = append(p.workers, w)
	}
	for _, w := range p.workers {
		p.wg.Add(1)
		go w.run()
	}
	p.wg.Add(1)
	go p.watch()
	return p, nil
}

// instantiate makes the worker's instance, with a context of its own that
// the watchdog cancels to stop a call that runs too long.
func (w *worker) instantiate() error {
	ctx, cancel := context.WithCancel(context.Background())
	w.mu.Lock()
	w.ctx, w.cancel = context.WithValue(ctx, workerKey{}, w), cancel
	w.mu.Unlock()
	cfg := wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize").
		WithStdout(io.Discard).WithStderr(io.Discard).WithSysWalltime().WithSysNanotime()
	mod, err := w.p.m.plugin.runtime.InstantiateModule(w.ctx, w.p.m.plugin.compiled, cfg)
	if err != nil {
		w.stopCall()
		return err
	}
	w.mod, w.fn = mod, mod.ExportedFunction(exportOnRequest)
	return nil
}

// stopCall cancels the instance's context, which stops a call under way.
func (w *worker) stopCall() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		w.cancel()
	}
}

func (w *worker) close() {
	if w.mod != nil {
		_ = w.mod.Close(context.Background())
	}
	w.stopCall()
}

// submit queues cl, and reports false, queuing nothing, when the queue is
// full. It never waits.
func (p *pool) submit(cl *call) bool {
	select {
	case p.calls <- cl:
		return true
	default:
		return false
	}
}

func (w *worker) run() {
	defer w.p.wg.Done()
	defer w.close()
	for {
		select {
		case cl := <-w.p.calls:
			w.serve(cl)
		case <-w.p.stop:
			return
		}
	}
}

// serve runs the plugin for cl, and sends the request on, or answers it.
func (w *worker) serve(cl *call) {
	defer cl.release()
	m, c := w.p.m, cl.c
	if c.Err() != nil {
		return
	}
	if time.Since(cl.queued) > m.WaitTimeout.Std() {
		_ = c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("503 Service Unavailable\n"))
		return
	}
	if w.mod == nil || w.mod.IsClosed() {
		// The instance a stopped call left could not be made again.
		if err := w.instantiate(); err != nil {
			w.mod = nil
			w.fail(cl)
			return
		}
	}
	w.current = cl
	w.started.Store(time.Now().UnixNano())
	err := w.fn.CallWithStack(w.ctx, w.stack[:])
	w.started.Store(0)
	w.current = nil
	if err != nil {
		if w.ctx.Err() != nil || w.mod.IsClosed() {
			w.close()
			w.mod = nil
			_ = w.instantiate()
		}
		w.fail(cl)
		return
	}
	if cl.status != 0 || uint32(w.stack[0]) == actionStop {
		status := cl.status
		if status == 0 {
			status = stdhttp.StatusOK
		}
		h := stdhttp.Header{}
		cl.apply(h)
		if _, ok := h["Content-Type"]; !ok && len(cl.body) > 0 {
			h["Content-Type"] = []string{"text/plain; charset=utf-8"}
		}
		_ = c.WriteResponse(fibhttp.Response{StatusCode: status, Header: h, Body: cl.body})
		return
	}
	if len(cl.ops) > 0 {
		c.OnHeader(func(_ int, h stdhttp.Header) { cl.apply(h) })
	}
	cl.next.ServeHTTP(c)
}

// fail answers a request whose call failed, or sends it on without the
// plugin, as on_error says.
func (w *worker) fail(cl *call) {
	if w.p.m.OnError == "skip" {
		cl.ops, cl.status, cl.body = nil, 0, nil
		cl.next.ServeHTTP(cl.c)
		return
	}
	_ = cl.c.Respond(stdhttp.StatusInternalServerError, "text/plain; charset=utf-8", []byte("500 Internal Server Error\n"))
}

// watch stops the calls that run past the timeout, by cancelling their
// instance's context, which wazero checks as the call runs. It also lets
// the plugin log again each second.
func (p *pool) watch() {
	defer p.wg.Done()
	timeout := p.m.Timeout.Std()
	tick := time.NewTicker(max(timeout/4, time.Millisecond))
	defer tick.Stop()
	last := time.Now()
	for {
		select {
		case now := <-tick.C:
			for _, w := range p.workers {
				if s := w.started.Load(); s != 0 && now.UnixNano()-s > int64(timeout) {
					w.stopCall()
				}
			}
			if now.Sub(last) >= time.Second {
				p.logs.Store(0)
				last = now
			}
		case <-p.stop:
			return
		}
	}
}

// close stops the workers once their calls are done, answering those still
// queued 503.
func (p *pool) close() {
	close(p.stop)
	p.wg.Wait()
	for {
		select {
		case cl := <-p.calls:
			_ = cl.c.Respond(stdhttp.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("503 Service Unavailable\n"))
			cl.release()
		default:
			return
		}
	}
}

package mesh

import (
	"sort"
	"sync"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// MaxRoutes bounds the routes a role counts apart; the calls of any route
// past it are counted together, as model.RouteOther.
// MaxRoutes 角色单独统计的路由数上限；超出后的路由上的调用合并统计为 model.RouteOther。
const MaxRoutes = 1024

type routeKey struct{ from, to, model string }

type routeEntry struct {
	stats model.RouteStats // the calls that ended, and the counts of all / 已结束调用的字节数，及全部调用的计数
	live  map[*RouteCall]struct{}
}

// Routes counts the model calls a role relays, per route (model.RouteStats):
// a call's bytes are read from its stream as they cross, so a long answer
// shows as it streams, not when it ends. At most MaxRoutes routes, and the
// calls running, which the links' limits bound, are kept.
// Routes 按路由（model.RouteStats）统计角色转发的模型调用：调用的字节数随传输从其流中读取，所以长回答在流式传输时即可
// 看到，而不是结束时才计入。至多保存 MaxRoutes 条路由，以及受连接上限约束的进行中调用。
type Routes struct {
	mu sync.Mutex
	m  map[routeKey]*routeEntry
}

func newRoutes() *Routes { return &Routes{m: map[routeKey]*routeEntry{}} }

// RouteCall is one call on a route, from Begin to End.
// RouteCall 路由上的一次调用，从 Begin 到 End。
type RouteCall struct {
	r       *Routes
	e       *routeEntry
	st      *wire.Stream
	inbound bool
	req     int64
	ended   bool
}

// Begin counts a call from peer from to peer to, for model.
// Begin 记录一次从对端 from 到对端 to、调用 model 的调用。
func (r *Routes) Begin(from, to, name string) *RouteCall {
	k := routeKey{from, to, name}
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.m[k]
	if e == nil && len(r.m) >= MaxRoutes {
		k = routeKey{model.RouteOther, model.RouteOther, model.RouteOther}
		e = r.m[k]
	}
	if e == nil {
		e = &routeEntry{stats: model.RouteStats{From: k.from, To: k.to, Model: k.model}, live: map[*RouteCall]struct{}{}}
		r.m[k] = e
	}
	c := &RouteCall{r: r, e: e}
	e.live[c] = struct{}{}
	e.stats.Calls++
	e.stats.LastAt = time.Now().Unix()
	return c
}

// Attach counts the call's bytes from st: on a stream the role took
// (inbound), what it receives is the request and what it sends the
// response; on one it opened, the other way round.
// Attach 从 st 统计调用的字节数：角色接受的流（inbound）上，收到的是请求、发出的是响应；角色打开的流上则相反。
func (c *RouteCall) Attach(st *wire.Stream, inbound bool) {
	c.r.mu.Lock()
	if !c.ended {
		c.st, c.inbound = st, inbound
	}
	c.r.mu.Unlock()
}

// AddReq counts n bytes of the request carried outside the stream's data,
// as a body inline in its opening request.
// AddReq 计入流数据之外携带的 n 字节请求，例如内联在打开请求中的请求体。
func (c *RouteCall) AddReq(n int) {
	c.r.mu.Lock()
	c.req += int64(n)
	c.r.mu.Unlock()
}

// bytesLocked is the call's request and response bytes so far. r.mu is held.
func (c *RouteCall) bytesLocked() (req, resp int64) {
	req = c.req
	if c.st != nil {
		in, out := c.st.Bytes()
		if c.inbound {
			req, resp = req+in, out
		} else {
			req, resp = req+out, in
		}
	}
	return req, resp
}

// End ends the call, a failure when failed. Only its first End counts.
// End 结束调用，failed 时计为失败。只有第一次 End 计数。
func (c *RouteCall) End(failed bool) {
	c.r.mu.Lock()
	defer c.r.mu.Unlock()
	if c.ended {
		return
	}
	c.ended = true
	req, resp := c.bytesLocked()
	c.e.stats.ReqBytes += req
	c.e.stats.RespBytes += resp
	if failed {
		c.e.stats.Errors++
	}
	delete(c.e.live, c)
	c.st = nil
}

// Snapshot returns every route's counts, the calls running included as far
// as they got, sorted by from, to and model.
// Snapshot 返回每条路由的计数，进行中的调用按目前进度计入，按 from、to、model 排序。
func (r *Routes) Snapshot() []model.RouteStats {
	r.mu.Lock()
	out := make([]model.RouteStats, 0, len(r.m))
	for _, e := range r.m {
		s := e.stats
		for c := range e.live {
			req, resp := c.bytesLocked()
			s.ReqBytes += req
			s.RespBytes += resp
			s.Inflight++
		}
		out = append(out, s)
	}
	r.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Model < b.Model
	})
	return out
}

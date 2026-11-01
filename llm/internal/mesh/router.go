package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/wire"
)

// DefaultTimeout bounds a method, and a call whose context has no deadline.
// DefaultTimeout 方法的执行时限，也是 context 无截止时间时的调用超时。
const DefaultTimeout = 10 * time.Second

// Func is a method implementation: it receives the caller's identity and the raw JSON request.
// Func 方法实现：接收调用方身份与原始 JSON 请求。
type Func func(ctx context.Context, caller model.Identity, req json.RawMessage) (any, error)

// StreamFunc is a stream method implementation: it receives the caller's identity, the raw JSON request of the
// stream-open frame and the stream. It sends data with st.Send, reads what the caller sends with st.Recv, and ends
// the stream by returning: the result (JSON) or error is the stream-end the caller gets.
//
// StreamFunc 流方法实现：接收调用方身份、stream-open 帧中的原始 JSON 请求与流本身。用 st.Send 发送数据，用 st.Recv
// 读取调用方发来的数据，通过返回结束流：返回结果（JSON）或错误即调用方收到的 stream-end。
type StreamFunc func(ctx context.Context, caller model.Identity, req json.RawMessage, st *wire.Stream) (any, error)

// Route is a registered method. A stream method has Stream set instead of Fn and no Timeout (a stream is bounded by
// the caller's context and wire's idle timeout).
// Route 已注册的方法。流方法设置 Stream 而不是 Fn，且没有 Timeout（流由调用方的 context 与 wire 的空闲超时约束）。
type Route struct {
	Method  string
	Roles   []string // caller roles allowed (model.RoleLocal is always allowed) / 允许的调用方角色（model.RoleLocal 始终允许）
	Timeout time.Duration
	Fn      Func
	Stream  StreamFunc
}

// Router holds the methods a process serves. The same routes serve calls arriving over the mesh and calls
// made by the process's own web console (with caller role model.RoleLocal), so both go through one code path.
//
// Router 存放进程提供的方法。相同的路由既处理经互联到达的调用，也处理进程自身 Web 控制台发起的调用
// （调用方角色为 model.RoleLocal），两者走同一条代码路径。
type Router struct {
	routes  map[string]*Route
	streams map[string]*Route
}

// NewRouter creates an empty router.
// NewRouter 创建空路由。
func NewRouter() *Router { return &Router{routes: map[string]*Route{}, streams: map[string]*Route{}} }

func decodeReq[Req any](raw json.RawMessage) (*Req, error) {
	var req Req
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, Errorf(model.CodeBadRequest, "bad request: %v", err)
		}
	}
	return &req, nil
}

// Handle registers a typed method. timeout ≤ 0 means DefaultTimeout.
// Handle 注册类型化方法。timeout ≤ 0 表示 DefaultTimeout。
func Handle[Req any](r *Router, method string, roles []string, timeout time.Duration,
	fn func(ctx context.Context, caller model.Identity, req *Req) (any, error)) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	r.routes[method] = &Route{Method: method, Roles: roles, Timeout: timeout,
		Fn: func(ctx context.Context, caller model.Identity, raw json.RawMessage) (any, error) {
			req, err := decodeReq[Req](raw)
			if err != nil {
				return nil, err
			}
			return fn(ctx, caller, req)
		}}
}

// HandleStream registers a typed stream method (opened with OpenStream). Roles are checked like Handle's.
// HandleStream 注册类型化流方法（由 OpenStream 打开）。角色校验与 Handle 相同。
func HandleStream[Req any](r *Router, method string, roles []string,
	fn func(ctx context.Context, caller model.Identity, req *Req, st *wire.Stream) (any, error)) {
	r.streams[method] = &Route{Method: method, Roles: roles,
		Stream: func(ctx context.Context, caller model.Identity, raw json.RawMessage, st *wire.Stream) (any, error) {
			req, err := decodeReq[Req](raw)
			if err != nil {
				return nil, err
			}
			return fn(ctx, caller, req, st)
		}}
}

// Routes returns the registered (non-stream) methods sorted by name.
// Routes 返回按名称排序的已注册（非流）方法。
func (r *Router) Routes() []*Route {
	out := make([]*Route, 0, len(r.routes))
	for _, rt := range r.routes {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

// StreamRoutes returns the registered stream methods sorted by name.
func (r *Router) StreamRoutes() []*Route {
	out := make([]*Route, 0, len(r.streams))
	for _, rt := range r.streams {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

// Lookup returns the route of method.
// Lookup 返回 method 的路由。
func (r *Router) Lookup(method string) (*Route, bool) {
	rt, ok := r.routes[method]
	return rt, ok
}

// Allowed reports whether role may call the route.
// Allowed 判断 role 是否可以调用该路由。
func (rt *Route) Allowed(role string) bool {
	return role == model.RoleLocal || slices.Contains(rt.Roles, role)
}

// Invoke checks the caller's role and runs the method with its timeout.
// Invoke 校验调用方角色，并在方法超时时间内执行它。
func (r *Router) Invoke(ctx context.Context, caller model.Identity, method string, raw json.RawMessage) (any, error) {
	rt, ok := r.routes[method]
	if !ok {
		return nil, Errorf(model.CodeNotFound, "unknown method %s", method)
	}
	if !rt.Allowed(caller.Role) {
		return nil, Errorf(model.CodeForbidden, "%s may not call %s", caller.Role, method)
	}
	ctx, cancel := context.WithTimeout(ctx, rt.Timeout)
	defer cancel()
	return rt.Fn(ctx, caller, raw)
}

// InvokeStream checks the caller's role and runs the stream method on st.
// InvokeStream 校验调用方角色，并在 st 上执行流方法。
func (r *Router) InvokeStream(ctx context.Context, caller model.Identity, method string, raw json.RawMessage,
	st *wire.Stream) (any, error) {
	rt, ok := r.streams[method]
	if !ok {
		return nil, Errorf(model.CodeNotFound, "unknown stream method %s", method)
	}
	if !rt.Allowed(caller.Role) {
		return nil, Errorf(model.CodeForbidden, "%s may not call %s", caller.Role, method)
	}
	return rt.Stream(ctx, caller, raw, st)
}

// OpenLocal opens the stream method in this process as caller, for the process's own web console: the method runs
// on one end of an in-memory wire pipe (wire.Pipe), so it behaves exactly as over the mesh — flow control, cancel,
// idle timeout, errors as *wire.Error. Read the returned stream like one from Mesh.OpenStream; ending ctx cancels it.
//
// OpenLocal 以 caller 身份在本进程内打开流方法，供进程自身的 Web 控制台使用：方法运行在内存 wire 管道（wire.Pipe）
// 的一端，因此行为与经互联时完全相同——流控、取消、空闲超时、错误为 *wire.Error。返回的流与 Mesh.OpenStream 的
// 读法相同；ctx 结束即取消。
func (r *Router) OpenLocal(ctx context.Context, caller model.Identity, method string, req json.RawMessage) (*wire.Stream, error) {
	d, _, closeFn := wire.Pipe(wire.Options{}, wire.Handlers{}, wire.Handlers{
		Stream: func(ctx context.Context, st *wire.Stream) ([]byte, error) {
			data, err := r.InvokeStream(ctx, caller, method, st.Payload(), st)
			if err != nil {
				return nil, wireErr(method, err)
			}
			return json.Marshal(data)
		},
	})
	st, err := d.OpenStream(ctx, wire.Head{Method: method}, req)
	if err != nil {
		closeFn()
		return nil, err
	}
	go func() {
		<-st.Done()
		closeFn()
	}()
	return st, nil
}

// ErrCode converts a method error into (code, message). Business errors (*wire.Error) keep their code;
// any other error is reported as an internal error with its message, since every caller is a trusted process
// or an administrator.
//
// ErrCode 将方法错误转换为 (code, message)。业务错误（*wire.Error）保留其错误码；其他错误作为内部错误返回
// 并带上错误信息，因为调用方都是受信任的进程或管理员。
func ErrCode(err error) (int, string) {
	var e *wire.Error
	if errors.As(err, &e) {
		return e.Code, e.Msg
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return model.CodeInternal, "timeout"
	}
	return model.CodeInternal, err.Error()
}

// Errorf creates a business error (a *wire.Error: its code crosses the mesh unchanged).
// Errorf 创建业务错误（*wire.Error：其错误码原样经互联传递）。
func Errorf(code int, format string, args ...any) error {
	return &wire.Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// wireErr converts a method error for the wire and logs internal ones (not cancellation: a stream method returns
// its context's error when the caller went away, which is normal).
// wireErr 把方法错误转换为线上格式，并记录内部错误（取消除外：调用方离开时流方法返回其 context 的错误，属于正常情况）。
func wireErr(method string, err error) error {
	code, msg := ErrCode(err)
	return &wire.Error{Code: code, Msg: msg}
}

// BadRequest is a shortcut for a CodeBadRequest error.
// BadRequest CodeBadRequest 错误的快捷方式。
func BadRequest(format string, args ...any) error {
	return Errorf(model.CodeBadRequest, "%s", fmt.Sprintf(format, args...))
}

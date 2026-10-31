package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/agent/internal/model"
	"github.com/lesismal/fit/agent/internal/wire"
)

// nodeCallTimeout bounds a managed call (a model check probes the upstream).
// nodeCallTimeout 代管调用的时限（模型检查会探测上游）。
const nodeCallTimeout = 90 * time.Second

// nodeStreamMethods are the stream methods among model.NodeManageMethods.
// nodeStreamMethods model.NodeManageMethods 中的流方法。
var nodeStreamMethods = []string{model.MethodNodeJobFollow}

// nodeCall serves MethodHubNodeCall (console only): a non-stream management method forwarded to the node.
// nodeCall 处理 MethodHubNodeCall（仅控制台）：把非流管理方法转发给节点。
func (h *Hub) nodeCall(ctx context.Context, _ model.Identity, req *model.HubNodeCallReq) (any, error) {
	if !slices.Contains(model.NodeManageMethods, req.Method) || slices.Contains(nodeStreamMethods, req.Method) {
		return nil, mesh.BadRequest("%q is not a node management method", req.Method)
	}
	raw, err := h.m.CallRaw(ctx, req.ID, req.Method, req.Req)
	if err != nil {
		return nil, h.manageErr(req.ID, err)
	}
	return raw, nil
}

// nodeStream serves MethodHubNodeStream (console only): a management stream method of the node, its frames and
// result relayed verbatim.
// nodeStream 处理 MethodHubNodeStream（仅控制台）：节点的管理流方法，其帧与结果原样中继。
func (h *Hub) nodeStream(ctx context.Context, _ model.Identity, req *model.HubNodeCallReq, st *wire.Stream) (any, error) {
	if !slices.Contains(nodeStreamMethods, req.Method) {
		return nil, mesh.BadRequest("%q is not a node management stream method", req.Method)
	}
	payload := req.Req
	if len(payload) == 0 {
		payload = json.RawMessage("{}")
	}
	ns, err := h.m.OpenStream(ctx, req.ID, req.Method, payload)
	if err != nil {
		return nil, h.manageErr(req.ID, err)
	}
	for {
		b, err := ns.Recv(ctx)
		if errors.Is(err, io.EOF) {
			return json.RawMessage(ns.Result()), nil
		}
		if err != nil {
			return nil, h.manageErr(req.ID, err)
		}
		if err := st.Send(ctx, b); err != nil {
			return nil, err
		}
	}
}

// manageErr makes the errors of managed calls clear on the console.
// manageErr 让代管调用的错误在控制台上清楚易懂。
func (h *Hub) manageErr(id string, err error) error {
	if errors.Is(err, mesh.ErrOffline) {
		return mesh.Errorf(model.CodeUnavailable, "node %s is offline", h.nodeName(id))
	}
	var we *wire.Error
	if errors.As(err, &we) && we.Code == model.CodeForbidden {
		return mesh.Errorf(model.CodeForbidden, "node %s does not allow hub management (enable allow_hub_manage on the node): %s",
			h.nodeName(id), we.Msg)
	}
	if errors.As(err, &we) {
		return we
	}
	return fmt.Errorf("node %s: %w", h.nodeName(id), err)
}

func (h *Hub) nodeName(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := h.nodes[id]; n != nil && n.info.Name != "" && n.info.Name != id {
		return fmt.Sprintf("%s (%s)", n.info.Name, id)
	}
	return id
}

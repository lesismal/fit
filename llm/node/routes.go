package node

import (
	"context"
	"errors"
	"time"

	"github.com/lesismal/fit/llm/internal/mesh"
	"github.com/lesismal/fit/llm/internal/model"
	"github.com/lesismal/fit/llm/internal/role"
	"github.com/lesismal/fit/llm/internal/wire"
)

// Caller roles of the read methods (hubs and proxies) and of the management methods (hubs, checked at call time
// against NodeSettings.AllowHubManage). The local console (model.RoleLocal) may call everything.
// 读取方法的调用方角色（hub 与 proxy）与管理方法的调用方角色（hub，调用时按 NodeSettings.AllowHubManage 检查）。
// 本地控制台（model.RoleLocal）可以调用全部方法。
var (
	readRoles   = []string{model.RoleHub, model.RoleProxy}
	manageRoles = []string{model.RoleHub}
)

// allowManage is the call-time check of a management method: a hub only while the node allows it. Proxies never get
// here (the router refuses them), but are refused again in case a route is ever registered more widely.
// allowManage 管理方法的调用时检查：hub 仅在节点允许时可用。proxy 不会走到这里（路由已拒绝），但以防路由将来注册得更宽，这里再拒绝一次。
func (n *Node) allowManage(caller model.Identity) error {
	switch caller.Role {
	case model.RoleLocal:
		return nil
	case model.RoleHub:
		if n.settings().AllowHubManage {
			return nil
		}
		return mesh.Errorf(model.CodeForbidden, "this node does not allow hub management (allow_hub_manage)")
	}
	return mesh.Errorf(model.CodeForbidden, "%s may not manage this node", caller.Role)
}

func manage[Req any](n *Node, method string, timeout time.Duration,
	fn func(ctx context.Context, caller model.Identity, req *Req) (any, error)) {
	mesh.Handle(n.Router, method, manageRoles, timeout, func(ctx context.Context, caller model.Identity, req *Req) (any, error) {
		if err := n.allowManage(caller); err != nil {
			return nil, err
		}
		return fn(ctx, caller, req)
	})
}

type empty struct{}

// gpuStatusReq: Fresh skips the cache.
// gpuStatusReq：Fresh 跳过缓存。
type gpuStatusReq struct {
	Fresh bool `json:"fresh,omitempty"`
}

func (n *Node) register() {
	r := n.Router
	mesh.Handle(r, model.MethodNodeInfo, readRoles, 0, func(context.Context, model.Identity, *empty) (any, error) {
		return n.Info(), nil
	})
	mesh.HandleStream(r, model.MethodNodeProxy, readRoles, n.proxy)

	manage(n, model.MethodNodeModelList, 0, func(context.Context, model.Identity, *empty) (any, error) { return n.listModels(), nil })
	manage(n, model.MethodNodeModelSave, role.EditTimeout, n.saveModel)
	manage(n, model.MethodNodeModelDelete, role.EditTimeout, n.deleteModel)
	manage(n, model.MethodNodeModelCheck, checkTimeout+5*time.Second, n.checkModel)
	manage(n, model.MethodNodeProfileSet, role.EditTimeout, n.setProfile)
	manage(n, model.MethodNodeGPUStatus, 30*time.Second, func(ctx context.Context, _ model.Identity, req *gpuStatusReq) (any, error) {
		return n.gpuStatus(ctx, req.Fresh), nil
	})
	manage(n, model.MethodNodeGPUCatalog, 0, func(context.Context, model.Identity, *empty) (any, error) { return gpuCatalog(), nil })
	manage(n, model.MethodNodeGPUInstall, 0, n.gpuInstall)
	manage(n, model.MethodNodeGPUSet, 30*time.Second, n.gpuSet)
	manage(n, model.MethodNodeActionList, 0, func(context.Context, model.Identity, *empty) (any, error) { return Actions(), nil })
	manage(n, model.MethodNodeActionRun, 0, n.runAction)
	manage(n, model.MethodNodeJobList, 0, func(context.Context, model.Identity, *empty) (any, error) { return n.jobs.List(), nil })
	manage(n, model.MethodNodeJobGet, 0, func(_ context.Context, _ model.Identity, req *model.JobGetReq) (any, error) {
		j, ok := n.jobs.Get(req.ID, req.Offset)
		if !ok {
			return nil, mesh.Errorf(model.CodeNotFound, "job %d not found", req.ID)
		}
		return j, nil
	})
	manage(n, model.MethodNodeJobCancel, 0, func(_ context.Context, _ model.Identity, req *model.JobIDReq) (any, error) {
		if !n.jobs.Cancel(req.ID) {
			return nil, mesh.Errorf(model.CodeNotFound, "job %d is not running", req.ID)
		}
		return struct{}{}, nil
	})
	mesh.HandleStream(r, model.MethodNodeJobFollow, manageRoles,
		func(ctx context.Context, caller model.Identity, req *model.JobGetReq, st *wire.Stream) (any, error) {
			if err := n.allowManage(caller); err != nil {
				return nil, err
			}
			j, err := n.jobs.Follow(ctx, req.ID, req.Offset, func(j model.Job) error { return sendJSON(ctx, st, j) })
			if errors.Is(err, ErrJobNotFound) {
				return nil, mesh.Errorf(model.CodeNotFound, "%v", err)
			}
			return j, err
		})

	// Local only (roles nil): the node's own console. / 仅本地（roles 为 nil）：节点自身控制台。
	mesh.Handle(r, model.MethodNodeSettingsGet, nil, 0, func(context.Context, model.Identity, *empty) (any, error) {
		return n.getSettings(), nil
	})
	mesh.Handle(r, model.MethodNodeSettingsSet, nil, role.EditTimeout, n.setSettings)
}

func (n *Node) runAction(_ context.Context, _ model.Identity, req *model.ActionRunReq) (any, error) {
	c, title, err := BuildAction(req.ID, req.Params)
	if err != nil {
		return nil, mesh.BadRequest("%v", err)
	}
	c = c.Sudo(n.settings().GPUSudo)
	return n.startJob(JobKindAction, title, RunCommand(c))
}

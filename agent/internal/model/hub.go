package model

import "encoding/json"

// Hub methods a client may call.
// client 可调用的 hub 方法。
const (
	MethodHubModels = "/hub/models" // -> HubModels
	// MethodHubProxy is MethodNodeProxy through the hub: the hub picks a node serving ProxyReq.Model and relays.
	// MethodHubProxy 经 hub 的 MethodNodeProxy：hub 选择提供 ProxyReq.Model 的节点并中继。
	MethodHubProxy = "/hub/proxy"
)

// MethodHubModelsUpdate is the push of a hub to its clients whenever its HubModels changes.
// MethodHubModelsUpdate hub 的 HubModels 变化时推送给其 client。
const MethodHubModelsUpdate = "/hub/models/update"

// Hub local-only methods (the hub's own console).
// hub 仅本地的方法（hub 自身控制台）。
const (
	MethodHubNodes = "/hub/nodes" // -> []HubNode
	// MethodHubNodeCall calls a NodeManageMethods method on a node (HubNodeCallReq -> the node's reply).
	// MethodHubNodeCall 调用节点上的 NodeManageMethods 方法（HubNodeCallReq -> 节点的应答）。
	MethodHubNodeCall = "/hub/node/call"
	// MethodHubNodeStream opens a stream method of NodeManageMethods (MethodNodeJobFollow) on a node and relays its
	// frames and result verbatim (HubNodeStreamReq).
	// MethodHubNodeStream 打开节点上 NodeManageMethods 中的流方法（MethodNodeJobFollow），原样中继其帧与结果（HubNodeStreamReq）。
	MethodHubNodeStream = "/hub/node/stream"
	MethodHubProfileSet = "/hub/profile/set" // ProfileReq (the name) -> Identity
)

// HubNodeStreamReq opens Method on node ID with Req (MethodHubNodeStream).
// HubNodeStreamReq 以 Req 打开节点 ID 上的流方法 Method（MethodHubNodeStream）。
type HubNodeStreamReq = HubNodeCallReq

// HubModel is one model name across the nodes of a hub.
// HubModel hub 的节点中的一个模型名称。
type HubModel struct {
	Name  string   `json:"name"`
	API   string   `json:"api"`
	Nodes []string `json:"nodes"` // ids of online nodes serving it / 提供它的在线节点 ID
	Ready int      `json:"ready"` // how many of them are ready now / 其中当前就绪的数量
}

// HubModels is the model list of a hub.
// HubModels hub 的模型列表。
type HubModels struct {
	Hub    string     `json:"hub"`
	Models []HubModel `json:"models"`
	Seq    int64      `json:"seq"`
}

// HubNode is a node as the hub console lists it.
// HubNode hub 控制台列出的节点。
type HubNode struct {
	Info     NodeInfo   `json:"info"`
	Online   bool       `json:"online"`
	Conns    []PeerConn `json:"conns,omitempty"`
	LastSeen int64      `json:"last_seen"`
}

// HubNodeCallReq calls Method on node ID with Req.
// HubNodeCallReq 以 Req 调用节点 ID 上的 Method。
type HubNodeCallReq struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Req    json.RawMessage `json:"req,omitempty"`
}

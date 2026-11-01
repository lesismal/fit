package model

import "encoding/json"

// Hub methods a proxy may call.
// proxy 可调用的 hub 方法。
const (
	MethodHubModels = "/hub/models" // -> HubModels
	// MethodHubProxy is MethodNodeProxy through the hub: the hub picks a node serving ProxyReq.Model and relays.
	// MethodHubProxy 经 hub 的 MethodNodeProxy：hub 选择提供 ProxyReq.Model 的节点并中继。
	MethodHubProxy = "/hub/proxy"
)

// MethodHubNodeList is the list of a hub's nodes (HubNodeList), which a
// proxy reads when it links; MethodHubNodeListUpdate is the hub's push of it
// to its proxies when it changes. Proxies only show it: they never manage
// the nodes.
// MethodHubNodeList hub 的节点列表（HubNodeList），proxy 连接时读取；MethodHubNodeListUpdate 为列表变化时 hub
// 向其 proxy 的推送。proxy 只展示它，从不管理节点。
const (
	MethodHubNodeList       = "/hub/node/list"
	MethodHubNodeListUpdate = "/hub/node/list/update"
)

// The bounds of a HubNodeList: the models listed per node, and its size
// encoded, past which the nodes at its end (offline ones first) are left
// out.
// HubNodeList 的上限：每个节点列出的模型数，以及编码后的大小，超出时省略列表末尾的节点（先省略离线节点）。
const (
	MaxNodeListModels = 64
	MaxNodeListBytes  = 4 << 20
)

// NodeLink is a link between a hub and one of its nodes.
// NodeLink hub 与其一个节点之间的连接。
type NodeLink struct {
	Direction   string    `json:"direction"` // as the hub sees it: "in" or "out" / 从 hub 看："in" 或 "out"
	ConnectedAt int64     `json:"connected_at"`
	Draining    bool      `json:"draining,omitempty"`
	Link        LinkStats `json:"link"`
}

// NodeTraffic sums the calls a hub relayed to a node, over its proxies and
// models.
// NodeTraffic 汇总 hub 转发给某节点的调用（跨其 proxy 与模型）。
type NodeTraffic struct {
	Calls     int64 `json:"calls"`
	Errors    int64 `json:"errors"`
	Inflight  int64 `json:"inflight"`
	ReqBytes  int64 `json:"req_bytes"`
	RespBytes int64 `json:"resp_bytes"`
}

// NodeSummary is a node as a hub's proxies see it: what it is, its
// hardware without its disks and addresses, its models (MaxNodeListModels at
// most, of ModelsTotal), its load, and its links with the hub and their
// traffic.
// NodeSummary proxy 看到的 hub 的节点：基本信息、不含磁盘与地址的硬件、模型（至多 MaxNodeListModels 个，共
// ModelsTotal 个）、负载，以及与 hub 的连接及其流量。
type NodeSummary struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Online      bool        `json:"online"`
	LastSeen    int64       `json:"last_seen"`
	HW          HWInfo      `json:"hw"`
	Models      []ModelView `json:"models"`
	ModelsTotal int         `json:"models_total"`
	Inflight    int         `json:"inflight"`
	Jobs        int         `json:"jobs"`
	Links       []NodeLink  `json:"links,omitempty"`
	Traffic     NodeTraffic `json:"traffic"`
}

// HubNodeList is the list of a hub's nodes, online ones first. Total counts
// the nodes of its table: more than Nodes when the bounds left some out.
// Seq increases with every change, so a proxy drops an older one.
// HubNodeList hub 的节点列表，在线节点在前。Total 为节点表中的节点数：因上限省略了部分节点时大于 Nodes 的长度。
// Seq 随每次变化递增，proxy 据此丢弃旧列表。
type HubNodeList struct {
	Hub   string        `json:"hub"`
	Nodes []NodeSummary `json:"nodes"`
	Total int           `json:"total"`
	Seq   int64         `json:"seq"`
}

// HubSettings are a hub's own settings, as its pages read and set them
// (MethodHubSettingsGet, MethodHubSettingsSet); 0 is the default.
// HubSettings hub 自身的设置，供其页面读取与设置（MethodHubSettingsGet、MethodHubSettingsSet）；0 表示默认值。
type HubSettings struct {
	MaxNodes int `json:"max_nodes"`
}

// The hub's settings methods, its own pages only.
// hub 的设置方法，仅自身页面可调用。
const (
	MethodHubSettingsGet = "/hub/settings/get" // -> HubSettings
	MethodHubSettingsSet = "/hub/settings/set" // HubSettings -> HubSettings
)

// MethodHubModelsUpdate is the push of a hub to its proxies whenever its HubModels changes.
// MethodHubModelsUpdate hub 的 HubModels 变化时推送给其 proxy。
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
